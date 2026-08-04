package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/session"
)

// AgentFactory creates an agent by config name. Returns nil if the name is unknown.
type AgentFactory func(ctx context.Context, name string) agent.Agent

// SaveDefaultFunc persists the default agent name to config file.
type SaveDefaultFunc func(name string) error

// AgentMeta holds static config info about an agent (for /status display).
type AgentMeta struct {
	Name    string
	Type    string // "acp", "cli", "http"
	Command string // binary path or endpoint
	Model   string
}

// Handler processes incoming WeChat messages and dispatches replies.
type Handler struct {
	mu            sync.RWMutex
	defaultName   string
	agents        map[string]agent.Agent // name -> running agent
	agentMetas    []AgentMeta            // all configured agents (for /status)
	agentWorkDirs map[string]string      // agent name -> configured/runtime cwd
	customAliases map[string]string      // custom alias -> agent name (from config)
	factory       AgentFactory
	saveDefault   SaveDefaultFunc
	contextTokens sync.Map // map[userID]contextToken
	saveDir       string   // Linkhoard archive directory
	seenMsgs      sync.Map // map[int64]time.Time — dedup by message_id
	activeTasks   sync.Map // map[userID]*activeTask — 同一用户只允许一个活动任务
	progress      ProgressConfig
	sessions      *session.Manager
}

// SetSessionManager 注入显式 Codex 会话管理器。
func (h *Handler) SetSessionManager(manager *session.Manager) {
	h.sessions = manager
}

// NewHandler creates a new message handler.
func NewHandler(factory AgentFactory, saveDefault SaveDefaultFunc) *Handler {
	return &Handler{
		agents:        make(map[string]agent.Agent),
		agentWorkDirs: make(map[string]string),
		factory:       factory,
		saveDefault:   saveDefault,
		progress:      DefaultProgressConfig(),
	}
}

// SetProgressConfig 设置微信长任务的输入状态和文字进度节奏。
func (h *Handler) SetProgressConfig(progress ProgressConfig) {
	h.progress = progress
}

// SetSaveDir sets the Linkhoard archive directory.
func (h *Handler) SetSaveDir(dir string) {
	h.saveDir = dir
}

// cleanSeenMsgs removes entries older than 5 minutes from the dedup cache.
func (h *Handler) cleanSeenMsgs() {
	cutoff := time.Now().Add(-5 * time.Minute)
	h.seenMsgs.Range(func(key, value any) bool {
		if t, ok := value.(time.Time); ok && t.Before(cutoff) {
			h.seenMsgs.Delete(key)
		}
		return true
	})
}

// SetCustomAliases sets custom alias mappings from config.
func (h *Handler) SetCustomAliases(aliases map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.customAliases = aliases
}

// SetAgentMetas sets the list of all configured agents (for /status).
func (h *Handler) SetAgentMetas(metas []AgentMeta) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agentMetas = metas
}

// SetAgentWorkDirs sets the configured working directory for each agent.
func (h *Handler) SetAgentWorkDirs(workDirs map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.agentWorkDirs = make(map[string]string, len(workDirs))
	for name, dir := range workDirs {
		h.agentWorkDirs[name] = dir
	}
}

// SetDefaultAgent sets the default agent (already started).
func (h *Handler) SetDefaultAgent(name string, ag agent.Agent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.defaultName = name
	h.agents[name] = ag
	log.Printf("[handler] default agent ready: %s (%s)", name, ag.Info())
}

// getAgent returns a running agent by name, or starts it on demand via factory.
func (h *Handler) getAgent(ctx context.Context, name string) (agent.Agent, error) {
	// Fast path: already running
	h.mu.RLock()
	ag, ok := h.agents[name]
	h.mu.RUnlock()
	if ok {
		return ag, nil
	}

	// Slow path: create on demand
	if h.factory == nil {
		return nil, fmt.Errorf("agent %q not found and no factory configured", name)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Double-check after acquiring write lock
	if ag, ok := h.agents[name]; ok {
		return ag, nil
	}

	log.Printf("[handler] starting agent %q on demand...", name)
	ag = h.factory(ctx, name)
	if ag == nil {
		return nil, fmt.Errorf("agent %q not available", name)
	}

	h.agents[name] = ag
	log.Printf("[handler] agent started on demand: %s (%s)", name, ag.Info())
	return ag, nil
}

// getDefaultAgent returns the default agent (may be nil if not ready yet).
func (h *Handler) getDefaultAgent() agent.Agent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.defaultName == "" {
		return nil
	}
	return h.agents[h.defaultName]
}

func (h *Handler) getDefaultAgentEntry() (string, agent.Agent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.defaultName == "" {
		return "", nil
	}
	return h.defaultName, h.agents[h.defaultName]
}

// isKnownAgent checks if a name corresponds to a configured agent.
func (h *Handler) isKnownAgent(name string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	// Check running agents
	if _, ok := h.agents[name]; ok {
		return true
	}
	// Check configured agents (metas)
	for _, meta := range h.agentMetas {
		if meta.Name == name {
			return true
		}
	}
	return false
}

// agentAliases maps short aliases to agent config names.
var agentAliases = map[string]string{
	"cc":  "claude",
	"cx":  "codex",
	"oc":  "openclaw",
	"cs":  "cursor",
	"km":  "kimi",
	"gm":  "gemini",
	"ocd": "opencode",
	"pi":  "pi",
	"cp":  "copilot",
	"dr":  "droid",
	"if":  "iflow",
	"kr":  "kiro",
	"qw":  "qwen",
}

// resolveAlias returns the full agent name for an alias, or the original name if no alias matches.
// Checks custom aliases (from config) first, then built-in aliases.
func (h *Handler) resolveAlias(name string) string {
	h.mu.RLock()
	custom := h.customAliases
	h.mu.RUnlock()
	if custom != nil {
		if full, ok := custom[name]; ok {
			return full
		}
	}
	if full, ok := agentAliases[name]; ok {
		return full
	}
	return name
}

// parseCommand checks if text starts with "/" or "@" followed by agent name(s).
// Supports multiple agents: "@cc @cx hello" returns (["claude","codex"], "hello").
// Returns (agentNames, actualMessage). Aliases are resolved automatically.
// If no command prefix, returns (nil, originalText).
func (h *Handler) parseCommand(text string) ([]string, string) {
	if !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "@") {
		return nil, text
	}

	// Parse consecutive @name or /name tokens from the start
	var names []string
	rest := text
	for {
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, "@") {
			break
		}

		// Strip prefix
		after := rest[1:]
		idx := strings.IndexAny(after, " /@")
		var token string
		if idx < 0 {
			// Rest is just the name, no message
			token = after
			rest = ""
		} else if after[idx] == '/' || after[idx] == '@' {
			// Next token is another @name or /name
			token = after[:idx]
			rest = after[idx:]
		} else {
			// Space — name ends here
			token = after[:idx]
			rest = strings.TrimSpace(after[idx+1:])
		}

		if token != "" {
			names = append(names, h.resolveAlias(token))
		}

		if rest == "" {
			break
		}
	}

	// Deduplicate names preserving order
	seen := make(map[string]bool)
	unique := names[:0]
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			unique = append(unique, n)
		}
	}

	return unique, rest
}

// HandleMessage processes a single incoming message.
func (h *Handler) HandleMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) {
	// Only process user messages that are finished
	if msg.MessageType != ilink.MessageTypeUser {
		return
	}
	if msg.MessageState != ilink.MessageStateFinish {
		return
	}

	// 只接受扫码绑定账号发来的消息，避免群聊或其他联系人驱动本机 Codex。
	if ownerUserID := client.OwnerUserID(); ownerUserID != "" && msg.FromUserID != ownerUserID {
		log.Printf("[handler] rejected message from non-owner user %s", msg.FromUserID)
		return
	}

	// Deduplicate by message_id to avoid processing the same message multiple times
	// (voice messages may trigger multiple finish-state updates)
	if msg.MessageID != 0 {
		if _, loaded := h.seenMsgs.LoadOrStore(msg.MessageID, time.Now()); loaded {
			return
		}
		// Clean up old entries periodically (fire-and-forget)
		go h.cleanSeenMsgs()
	}

	// Extract text from item list (text message or voice transcription)
	text := extractText(msg)
	if text == "" {
		if voiceText := extractVoiceText(msg); voiceText != "" {
			text = voiceText
			log.Printf("[handler] voice transcription from %s: %q", msg.FromUserID, truncate(text, 80))
		}
	}
	images := extractImages(msg)
	files := extractFiles(msg)
	if text == "" && len(images) == 0 && len(files) == 0 {
		log.Printf("[handler] received non-text message from %s, skipping", msg.FromUserID)
		return
	}

	if len(images) > 0 || len(files) > 0 {
		log.Printf("[handler] received from %s: images=%d files=%d text=%q", msg.FromUserID, len(images), len(files), truncate(text, 80))
	} else {
		log.Printf("[handler] received from %s: %q", msg.FromUserID, truncate(text, 80))
	}

	// Store context token for this user
	h.contextTokens.Store(msg.FromUserID, msg.ContextToken)

	trimmed := strings.TrimSpace(text)
	clientID := NewClientID()

	// 控制命令必须优先于忙碌拦截，否则运行中的任务既无法取消也无法查询。
	if trimmed == "/cancel" {
		reply := h.cancelActiveTask(msg.FromUserID)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send cancel result to %s: %v", msg.FromUserID, err)
		}
		return
	}
	if trimmed == "/status" {
		reply := h.buildTaskStatus(msg.FromUserID)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send task status to %s: %v", msg.FromUserID, err)
		}
		return
	}
	if trimmed == "/info" {
		reply := h.buildStatus()
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	}
	if trimmed == "/help" {
		reply := buildHelpText()
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	}
	if trimmed == "/session" || trimmed == "/sessions" || strings.HasPrefix(trimmed, "/sessions ") {
		reply := h.handleSessionReadCommand(ctx, msg.FromUserID, trimmed)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send session result to %s: %v", msg.FromUserID, err)
		}
		return
	}
	if trimmed == "/new" || trimmed == "/clear" {
		reply := "该命令已删除。请使用 /session new 创建新会话。"
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send removed-command notice to %s: %v", msg.FromUserID, err)
		}
		return
	}

	// 任务运行期间普通消息直接返回快照，避免切换、归档等命令并发改写线程状态。
	if active, ok := h.activeTasks.Load(msg.FromUserID); ok {
		h.sendActiveTaskStatus(ctx, client, msg, active.(*activeTask))
		return
	}

	// Intercept URLs: save to Linkhoard directly without AI agent
	if len(images) == 0 && len(files) == 0 && h.saveDir != "" && IsURL(trimmed) {
		rawURL := ExtractURL(trimmed)
		if rawURL != "" {
			log.Printf("[handler] saving URL to linkhoard: %s", rawURL)
			title, err := SaveLinkToLinkhoard(ctx, h.saveDir, rawURL)
			var reply string
			if err != nil {
				log.Printf("[handler] link save failed: %v", err)
				reply = fmt.Sprintf("保存失败: %v", err)
			} else {
				reply = fmt.Sprintf("已保存: %s", title)
			}
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			}
			return
		}
	}

	// 会改变会话状态的内置命令只允许在空闲时执行。
	if strings.HasPrefix(trimmed, "/session ") {
		reply := h.handleSessionMutationCommand(ctx, msg.FromUserID, trimmed)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send session mutation result to %s: %v", msg.FromUserID, err)
		}
		return
	} else if strings.HasPrefix(trimmed, "/cwd") {
		reply := h.handleCwd(trimmed)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	}

	// Route: "/agentname message" or "@agent1 @agent2 message" -> specific agent(s)
	agentNames, message := h.parseCommand(text)

	// No command prefix -> send to default agent
	if len(agentNames) == 0 {
		h.sendToDefaultAgent(ctx, client, msg, text, images, files, clientID)
		return
	}

	// No message -> switch default agent (only first name)
	if message == "" && len(images) == 0 && len(files) == 0 {
		if len(agentNames) == 1 && h.isKnownAgent(agentNames[0]) {
			reply := h.switchDefault(ctx, agentNames[0])
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			}
		} else if len(agentNames) == 1 && !h.isKnownAgent(agentNames[0]) {
			// Unknown agent -> forward to default
			h.sendToDefaultAgent(ctx, client, msg, text, images, files, clientID)
		} else {
			reply := "Usage: specify one agent to switch, or add a message to broadcast"
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			}
		}
		return
	}

	// Filter to known agents; if single unknown agent -> forward to default
	var knownNames []string
	for _, name := range agentNames {
		if h.isKnownAgent(name) {
			knownNames = append(knownNames, name)
		}
	}
	if len(knownNames) == 0 {
		// No known agents -> forward entire text to default agent
		h.sendToDefaultAgent(ctx, client, msg, text, images, files, clientID)
		return
	}

	if len(knownNames) == 1 {
		// Single agent
		h.sendToNamedAgent(ctx, client, msg, knownNames[0], message, images, files, clientID)
	} else {
		// Multi-agent broadcast: parallel dispatch, send replies as they arrive
		h.broadcastToAgents(ctx, client, msg, knownNames, message, images, files)
	}
}

// sendToDefaultAgent sends the message to the default agent and replies.
func (h *Handler) sendToDefaultAgent(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text string, images []*ilink.ImageItem, files []*ilink.FileItem, clientID string) {
	agentName, ag := h.getDefaultAgentEntry()
	var reply, artifactDir string
	if ag != nil {
		reporter, ok := h.beginTask(ctx, client, msg)
		if !ok {
			return
		}
		request, cleanup, prepareErr := h.prepareTaskInput(reporter, text, images, files)
		if prepareErr != nil {
			log.Printf("[handler] failed to prepare inbound attachments for %s: %v", msg.FromUserID, prepareErr)
			if h.finishTask(msg.FromUserID, reporter) {
				reply = fmt.Sprintf("附件处理失败：%v", prepareErr)
				h.sendReplyWithMedia(ctx, client, msg, reply, "", clientID)
			}
			return
		}
		defer cleanup()
		artifactDir = request.ArtifactDir

		var err error
		reply, err = h.chatWithAgent(reporter.task.context(), agentName, ag, msg.FromUserID, request, reporter.Report)
		if !h.finishTask(msg.FromUserID, reporter) {
			log.Printf("[handler] task cancelled for %s", msg.FromUserID)
			return
		}
		if err != nil {
			reply = fmt.Sprintf("Error: %v", err)
		}
	} else {
		log.Printf("[handler] default agent is unavailable for %s", msg.FromUserID)
		reply = "Codex 当前不可用，请稍后重试。"
	}

	h.sendReplyWithMedia(ctx, client, msg, reply, artifactDir, clientID)
}

// sendToNamedAgent sends the message to a specific agent and replies.
func (h *Handler) sendToNamedAgent(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, name, message string, images []*ilink.ImageItem, files []*ilink.FileItem, clientID string) {
	reporter, ok := h.beginTask(ctx, client, msg)
	if !ok {
		return
	}

	ag, agErr := h.getAgent(ctx, name)
	if agErr != nil {
		log.Printf("[handler] agent %q not available: %v", name, agErr)
		reply := fmt.Sprintf("Agent %q is not available: %v", name, agErr)
		if h.finishTask(msg.FromUserID, reporter) {
			SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		}
		return
	}
	request, cleanup, prepareErr := h.prepareTaskInput(reporter, message, images, files)
	if prepareErr != nil {
		log.Printf("[handler] failed to prepare inbound attachments for %s: %v", msg.FromUserID, prepareErr)
		if h.finishTask(msg.FromUserID, reporter) {
			SendTextReply(ctx, client, msg.FromUserID, fmt.Sprintf("附件处理失败：%v", prepareErr), msg.ContextToken, clientID)
		}
		return
	}
	defer cleanup()

	reply, err := h.chatWithAgent(reporter.task.context(), name, ag, msg.FromUserID, request, reporter.Report)
	if !h.finishTask(msg.FromUserID, reporter) {
		log.Printf("[handler] task cancelled for %s", msg.FromUserID)
		return
	}
	if err != nil {
		reply = fmt.Sprintf("Error: %v", err)
	}
	h.sendReplyWithMedia(ctx, client, msg, reply, request.ArtifactDir, clientID)
}

// broadcastToAgents sends the message to multiple agents in parallel.
// Each reply is sent as a separate message with the agent name prefix.
func (h *Handler) broadcastToAgents(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, names []string, message string, images []*ilink.ImageItem, files []*ilink.FileItem) {
	reporter, ok := h.beginTask(ctx, client, msg)
	if !ok {
		return
	}
	request, cleanup, prepareErr := h.prepareTaskInput(reporter, message, images, files)
	if prepareErr != nil {
		log.Printf("[handler] failed to prepare inbound attachments for %s: %v", msg.FromUserID, prepareErr)
		if h.finishTask(msg.FromUserID, reporter) {
			SendTextReply(ctx, client, msg.FromUserID, fmt.Sprintf("附件处理失败：%v", prepareErr), msg.ContextToken, NewClientID())
		}
		return
	}
	defer cleanup()

	type result struct {
		name        string
		reply       string
		artifactDir string
	}

	ch := make(chan result, len(names))

	for index, name := range names {
		agentRequest := request
		agentRequest.ArtifactDir = filepath.Join(request.ArtifactDir, fmt.Sprintf("agent-%02d", index+1))
		if err := os.Mkdir(agentRequest.ArtifactDir, 0o700); err != nil {
			ch <- result{name: name, reply: fmt.Sprintf("Error: 创建 Agent 交付目录失败：%v", err)}
			continue
		}
		go func(n string, turnRequest agent.ChatRequest) {
			ag, err := h.getAgent(ctx, n)
			if err != nil {
				ch <- result{name: n, reply: fmt.Sprintf("Error: %v", err), artifactDir: turnRequest.ArtifactDir}
				return
			}
			reply, err := h.chatWithAgent(reporter.task.context(), n, ag, msg.FromUserID, turnRequest, reporter.Report)
			if err != nil {
				ch <- result{name: n, reply: fmt.Sprintf("Error: %v", err), artifactDir: turnRequest.ArtifactDir}
				return
			}
			ch <- result{name: n, reply: reply, artifactDir: turnRequest.ArtifactDir}
		}(name, agentRequest)
	}

	// Send replies as they arrive
	for range names {
		r := <-ch
		if reporter.task.cancelRequested() {
			continue
		}
		reply := fmt.Sprintf("[%s] %s", r.name, r.reply)
		clientID := NewClientID()
		h.sendReplyWithMedia(ctx, client, msg, reply, r.artifactDir, clientID)
	}
	h.finishTask(msg.FromUserID, reporter)
}

// sendReplyWithMedia 发送最终文字、远程图片和本次 turn 的专属交付物。
func (h *Handler) sendReplyWithMedia(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, reply, artifactDir, clientID string) {
	imageURLs := ExtractImageURLs(reply)
	artifacts, collectErr := collectArtifacts(artifactDir)
	var sentPaths []string
	failed := append([]string(nil), artifacts.Skipped...)
	if collectErr != nil {
		failed = append(failed, collectErr.Error())
	}
	for _, attachmentPath := range artifacts.Paths {
		if err := SendMediaFromPath(ctx, client, msg.FromUserID, attachmentPath, msg.ContextToken); err != nil {
			log.Printf("[handler] failed to send attachment to %s: %v", msg.FromUserID, err)
			failed = append(failed, filepath.Base(attachmentPath)+"（上传失败）")
			continue
		}
		sentPaths = append(sentPaths, attachmentPath)
	}

	reply = appendArtifactSummary(reply, sentPaths, failed)

	if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
		log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
	}

	for _, imgURL := range imageURLs {
		if err := SendMediaFromURL(ctx, client, msg.FromUserID, imgURL, msg.ContextToken); err != nil {
			log.Printf("[handler] failed to send image to %s: %v", msg.FromUserID, err)
		}
	}
}

// chatWithAgent sends a message to an agent and returns the reply, with logging.
func (h *Handler) chatWithAgent(ctx context.Context, agentName string, ag agent.Agent, userID string, request agent.ChatRequest, onProgress agent.ProgressHandler) (string, error) {
	info := ag.Info()
	log.Printf("[handler] dispatching to agent (%s) for %s", info, userID)

	start := time.Now()
	var reply string
	var err error
	if threadAgent, ok := ag.(agent.ThreadAgent); ok {
		if h.sessions == nil {
			return "", fmt.Errorf("session manager is not initialized")
		}
		thread, sessionErr := h.sessions.EnsureActive(ctx, userID, agentName, threadAgent)
		if sessionErr != nil {
			return "", sessionErr
		}
		if progressAgent, supportsProgress := ag.(agent.ThreadProgressAgent); supportsProgress {
			reply, err = progressAgent.ChatThreadWithProgress(ctx, thread.ID, request, onProgress)
		} else {
			reply, err = threadAgent.ChatThread(ctx, thread.ID, request)
		}
		if touchErr := h.sessions.Touch(userID, agentName, thread.ID, time.Now().Unix()); touchErr != nil {
			log.Printf("[handler] failed to persist session recency (agent=%s, thread=%s): %v", agentName, thread.ID, touchErr)
		}
	} else if progressAgent, ok := ag.(agent.ProgressAgent); ok {
		reply, err = progressAgent.ChatWithProgress(ctx, userID, request, onProgress)
	} else {
		reply, err = ag.Chat(ctx, userID, request)
	}
	elapsed := time.Since(start)

	if err != nil {
		log.Printf("[handler] agent error (%s, elapsed=%s): %v", info, elapsed, err)
		return "", err
	}

	log.Printf("[handler] agent replied (%s, elapsed=%s): %q", info, elapsed, truncate(reply, 100))
	return reply, nil
}

func (h *Handler) prepareTaskInput(reporter *progressReporter, text string, images []*ilink.ImageItem, files []*ilink.FileItem) (agent.ChatRequest, func(), error) {
	if len(images) > 0 || len(files) > 0 {
		reporter.Report(agent.ProgressEvent{Kind: agent.ProgressActivity, Text: "正在接收微信附件"})
	}
	request, cleanup, err := prepareAgentInput(reporter.task.context(), text, images, files, "")
	if err != nil {
		return agent.ChatRequest{}, cleanup, err
	}
	if len(request.LocalImages) > 0 || len(request.LocalFiles) > 0 {
		reporter.Report(agent.ProgressEvent{Kind: agent.ProgressActivity, Text: "附件已接收，正在交给 Codex 分析"})
	}
	return request, cleanup, nil
}

func (h *Handler) beginTask(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) (*progressReporter, bool) {
	task := newActiveTask(ctx)
	actual, loaded := h.activeTasks.LoadOrStore(msg.FromUserID, task)
	if loaded {
		task.finish()
		h.sendActiveTaskStatus(ctx, client, msg, actual.(*activeTask))
		return nil, false
	}
	return newProgressReporter(task.context(), client, msg.FromUserID, msg.ContextToken, h.progress, task), true
}

func (h *Handler) sendActiveTaskStatus(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, task *activeTask) {
	if err := SendTextReply(ctx, client, msg.FromUserID, task.busySummary(), msg.ContextToken, NewClientID()); err != nil {
		log.Printf("[handler] failed to send active task status to %s: %v", msg.FromUserID, err)
	}
}

func (h *Handler) finishTask(userID string, reporter *progressReporter) bool {
	deliver := reporter.task.finish()
	reporter.Close()
	h.activeTasks.CompareAndDelete(userID, reporter.task)
	return deliver
}

func (h *Handler) cancelActiveTask(userID string) string {
	value, ok := h.activeTasks.Load(userID)
	if !ok {
		return "当前没有正在执行的任务。"
	}
	task := value.(*activeTask)
	if !task.requestCancel() {
		if task.cancelRequested() {
			return "当前任务正在取消，请稍候。"
		}
		return "当前没有正在执行的任务。"
	}
	return fmt.Sprintf("已请求取消当前任务。\n已运行：%s", formatElapsed(time.Since(task.started)))
}

func (h *Handler) buildTaskStatus(userID string) string {
	if value, ok := h.activeTasks.Load(userID); ok {
		return value.(*activeTask).statusSummary()
	}
	return "任务状态：空闲\n" + h.buildStatus()
}

func (h *Handler) sessionContext() (string, agent.ThreadAgent, error) {
	if h.sessions == nil {
		return "", nil, fmt.Errorf("会话管理器未初始化")
	}
	agentName, ag := h.getDefaultAgentEntry()
	if ag == nil {
		return "", nil, fmt.Errorf("当前没有可用的 Agent")
	}
	threadAgent, ok := ag.(agent.ThreadAgent)
	if !ok {
		return "", nil, fmt.Errorf("当前 Agent 不支持 Codex 会话管理")
	}
	return agentName, threadAgent, nil
}

func (h *Handler) handleSessionReadCommand(ctx context.Context, userID, command string) string {
	agentName, threadAgent, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	fields := strings.Fields(command)
	if len(fields) == 1 && fields[0] == "/session" {
		current, currentErr := h.sessions.Current(ctx, userID, agentName, threadAgent)
		if currentErr != nil {
			if errors.Is(currentErr, session.ErrNoActive) {
				return "当前没有会话。\n创建：/session new [名称]"
			}
			return formatSessionError(currentErr)
		}
		return formatSessionDetail(current)
	}
	if len(fields) == 0 || fields[0] != "/sessions" {
		return sessionCommandUsage()
	}

	archived := false
	pageNumber := 1
	args := fields[1:]
	if len(args) > 0 && args[0] == "archived" {
		archived = true
		args = args[1:]
	}
	if len(args) > 1 {
		return "用法：/sessions [页码] 或 /sessions archived [页码]"
	}
	if len(args) == 1 {
		parsed, parseErr := strconv.Atoi(args[0])
		if parseErr != nil || parsed <= 0 {
			return "页码必须是正整数。"
		}
		pageNumber = parsed
	}
	page, listErr := h.sessions.List(ctx, userID, agentName, threadAgent, archived, pageNumber, session.DefaultPageSize)
	if listErr != nil {
		return formatSessionError(listErr)
	}
	return formatSessionPage(page, archived)
}

func (h *Handler) handleSessionMutationCommand(ctx context.Context, userID, command string) string {
	agentName, threadAgent, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	fields := strings.Fields(command)
	if len(fields) < 2 || fields[0] != "/session" {
		return sessionCommandUsage()
	}
	subcommand := fields[1]
	argument := strings.TrimSpace(strings.TrimPrefix(command, "/session "+subcommand))

	switch subcommand {
	case "new":
		thread, createErr := h.sessions.New(ctx, userID, agentName, threadAgent, argument)
		if createErr != nil {
			return formatSessionError(createErr)
		}
		return "已创建并切换到新会话。\n" + formatThreadIdentity(thread)
	case "use":
		if argument == "" || len(strings.Fields(argument)) != 1 {
			return "用法：/session use <短编号>"
		}
		thread, useErr := h.sessions.Use(ctx, userID, agentName, threadAgent, argument)
		if useErr != nil {
			return formatSessionError(useErr)
		}
		return "已切换会话。\n" + formatThreadIdentity(thread)
	case "rename":
		if argument == "" {
			return "用法：/session rename <名称>"
		}
		thread, renameErr := h.sessions.Rename(ctx, userID, agentName, threadAgent, argument)
		if renameErr != nil {
			return formatSessionError(renameErr)
		}
		return "会话已重命名。\n" + formatThreadIdentity(thread)
	case "archive":
		if len(strings.Fields(argument)) > 1 {
			return "用法：/session archive [短编号]"
		}
		nextActive, archiveErr := h.sessions.Archive(ctx, userID, agentName, threadAgent, argument)
		if archiveErr != nil {
			return formatSessionError(archiveErr)
		}
		if nextActive == "" {
			return "会话已归档。\n当前没有可用会话；下一条普通消息会创建新会话。"
		}
		return fmt.Sprintf("会话已归档。\n已切换到：%s", session.ShortCode(nextActive))
	case "restore":
		if argument == "" || len(strings.Fields(argument)) != 1 {
			return "用法：/session restore <短编号>"
		}
		thread, restoreErr := h.sessions.Restore(ctx, userID, agentName, threadAgent, argument)
		if restoreErr != nil {
			return formatSessionError(restoreErr)
		}
		return "会话已恢复。\n" + formatThreadIdentity(thread)
	default:
		return sessionCommandUsage()
	}
}

func formatSessionPage(page session.Page, archived bool) string {
	kind := "会话"
	if archived {
		kind = "已归档会话"
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("%s %d/%d，共 %d 个", kind, page.Number, page.TotalPages, page.Total))
	if len(page.Items) == 0 {
		lines = append(lines, "", "暂无会话。")
		if !archived {
			lines = append(lines, "创建：/session new [名称]")
		}
		return strings.Join(lines, "\n")
	}
	for _, item := range page.Items {
		marker := "    "
		if item.Current {
			marker = "当前"
		}
		title := threadTitle(item.Info)
		status := formatThreadStatus(item.Info.Status)
		if item.Unavailable {
			status = "无法读取"
		}
		lines = append(lines, "", fmt.Sprintf("%s  %s  %s", marker, session.ShortCode(item.Info.ID), status), title)
		if item.Info.Cwd != "" {
			lines = append(lines, "目录："+item.Info.Cwd)
		}
		if item.Info.UpdatedAt > 0 {
			lines = append(lines, "更新："+formatSessionTime(item.Info.UpdatedAt))
		}
	}
	if archived {
		lines = append(lines, "", "恢复：/session restore <短编号>")
	} else {
		lines = append(lines, "", "切换：/session use <短编号>")
	}
	return strings.Join(lines, "\n")
}

func formatSessionDetail(thread session.ManagedThread) string {
	lines := []string{
		"当前会话",
		"名称：" + threadTitle(thread.Info),
		"短编号：" + session.ShortCode(thread.Info.ID),
		"完整编号：" + thread.Info.ID,
		"状态：" + formatThreadStatus(thread.Info.Status),
	}
	if thread.Info.Cwd != "" {
		lines = append(lines, "目录："+thread.Info.Cwd)
	}
	if thread.Info.CreatedAt > 0 {
		lines = append(lines, "创建："+formatSessionTime(thread.Info.CreatedAt))
	}
	if thread.Info.UpdatedAt > 0 {
		lines = append(lines, "更新："+formatSessionTime(thread.Info.UpdatedAt))
	}
	return strings.Join(lines, "\n")
}

func formatThreadIdentity(thread agent.ThreadInfo) string {
	return fmt.Sprintf("名称：%s\n短编号：%s\n状态：%s", threadTitle(thread), session.ShortCode(thread.ID), formatThreadStatus(thread.Status))
}

func threadTitle(thread agent.ThreadInfo) string {
	if name := strings.TrimSpace(thread.Name); name != "" {
		return normalizeSessionLine(name, 60)
	}
	if preview := strings.TrimSpace(thread.Preview); preview != "" {
		return normalizeSessionLine(preview, 60)
	}
	return "未命名会话"
}

func normalizeSessionLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func formatThreadStatus(status agent.ThreadStatus) string {
	switch status.Type {
	case "active":
		for _, flag := range status.ActiveFlags {
			if flag == "waitingOnApproval" {
				return "等待确认"
			}
		}
		return "执行中"
	case "idle":
		return "空闲"
	case "notLoaded", "":
		return "未加载"
	case "systemError":
		return "异常"
	default:
		return status.Type
	}
}

func formatSessionTime(timestamp int64) string {
	return time.Unix(timestamp, 0).Local().Format("2006-01-02 15:04")
}

func formatSessionError(err error) string {
	switch {
	case errors.Is(err, session.ErrNoActive):
		return "当前没有会话。"
	case errors.Is(err, session.ErrNotOwned):
		return "没有找到属于当前微信用户的会话。"
	case errors.Is(err, session.ErrAmbiguousCode):
		return "短编号不唯一，请输入更长的编号。"
	default:
		return fmt.Sprintf("会话操作失败：%v", err)
	}
}

func sessionCommandUsage() string {
	return `会话命令：
/sessions [页码]
/session
/session new [名称]
/session use <短编号>
/session rename <名称>
/session archive [短编号]
/sessions archived [页码]
/session restore <短编号>`
}

// switchDefault switches the default agent. Starts it on demand if needed.
// The change is persisted to config file.
func (h *Handler) switchDefault(ctx context.Context, name string) string {
	ag, err := h.getAgent(ctx, name)
	if err != nil {
		log.Printf("[handler] failed to switch default to %q: %v", name, err)
		return fmt.Sprintf("Failed to switch to %q: %v", name, err)
	}

	h.mu.Lock()
	old := h.defaultName
	h.defaultName = name
	h.agents[name] = ag
	h.mu.Unlock()

	// Persist to config file
	if h.saveDefault != nil {
		if err := h.saveDefault(name); err != nil {
			log.Printf("[handler] failed to save default agent to config: %v", err)
		} else {
			log.Printf("[handler] saved default agent %q to config", name)
		}
	}

	info := ag.Info()
	log.Printf("[handler] switched default agent: %s -> %s (%s)", old, name, info)
	return fmt.Sprintf("switch to %s", name)
}

// handleCwd handles the /cwd command. It updates the working directory for all running agents.
func (h *Handler) handleCwd(trimmed string) string {
	arg := strings.TrimSpace(strings.TrimPrefix(trimmed, "/cwd"))
	if arg == "" {
		// No path provided — show current cwd of default agent
		ag := h.getDefaultAgent()
		if ag == nil {
			return "No agent running."
		}
		info := ag.Info()
		return fmt.Sprintf("cwd: (check agent config)\nagent: %s", info.Name)
	}

	// Expand ~ to home directory
	if arg == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			arg = home
		}
	} else if strings.HasPrefix(arg, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			arg = filepath.Join(home, arg[2:])
		}
	}

	// Resolve to absolute path
	absPath, err := filepath.Abs(arg)
	if err != nil {
		return fmt.Sprintf("Invalid path: %v", err)
	}

	// Verify directory exists
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Sprintf("Path not found: %s", absPath)
	}
	if !info.IsDir() {
		return fmt.Sprintf("Not a directory: %s", absPath)
	}

	// Update cwd on all running agents
	h.mu.RLock()
	agents := make(map[string]agent.Agent, len(h.agents))
	for name, ag := range h.agents {
		agents[name] = ag
	}
	h.mu.RUnlock()

	for name, ag := range agents {
		ag.SetCwd(absPath)
		log.Printf("[handler] updated cwd for agent %s: %s", name, absPath)
	}

	h.mu.Lock()
	for name := range agents {
		h.agentWorkDirs[name] = absPath
	}
	h.mu.Unlock()

	return fmt.Sprintf("cwd: %s", absPath)
}

// buildStatus returns a short status string showing the current default agent.
func (h *Handler) buildStatus() string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.defaultName == "" {
		return "agent: none (echo mode)"
	}

	ag, ok := h.agents[h.defaultName]
	if !ok {
		return fmt.Sprintf("agent: %s (not started)", h.defaultName)
	}

	info := ag.Info()
	return fmt.Sprintf("agent: %s\ntype: %s\nmodel: %s", h.defaultName, info.Type, info.Model)
}

func buildHelpText() string {
	return `可用命令：
直接发送图片 - 交给 Codex 分析
直接发送 PDF、代码、压缩包或日志 - 交给 Codex 检查
/status - 查看当前任务状态
/cancel - 取消当前任务
/info - 查看当前 Agent 信息
/sessions [页码] - 查看会话列表
/session - 查看当前会话
/session new [名称] - 创建会话
/session use 短编号 - 切换会话
/session rename 名称 - 重命名当前会话
/session archive [短编号] - 归档会话
/sessions archived [页码] - 查看已归档会话
/session restore 短编号 - 恢复会话
/cwd /path - 切换工作目录
/agent - 切换默认 Agent
/agent 消息 - 发送给指定 Agent
@a @b 消息 - 同时发送给多个 Agent
/help - 查看命令列表

快捷别名：/cc(claude) /cx(codex) /cs(cursor) /km(kimi) /gm(gemini) /oc(openclaw) /ocd(opencode) /pi(pi) /cp(copilot) /dr(droid) /if(iflow) /kr(kiro) /qw(qwen)`
}

func extractText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeText && item.TextItem != nil {
			return item.TextItem.Text
		}
	}
	return ""
}

func extractImages(msg ilink.WeixinMessage) []*ilink.ImageItem {
	var images []*ilink.ImageItem
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeImage && item.ImageItem != nil {
			images = append(images, item.ImageItem)
		}
	}
	return images
}

func extractFiles(msg ilink.WeixinMessage) []*ilink.FileItem {
	var files []*ilink.FileItem
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeFile && item.FileItem != nil {
			files = append(files, item.FileItem)
		}
	}
	return files
}

func extractVoiceText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeVoice && item.VoiceItem != nil && item.VoiceItem.Text != "" {
			return item.VoiceItem.Text
		}
	}
	return ""
}
