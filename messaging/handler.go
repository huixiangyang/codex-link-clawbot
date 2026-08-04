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

	"github.com/huixiangyang/weclaw/codex"
	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/session"
)

// Handler processes incoming WeChat messages and dispatches replies.
type Handler struct {
	codex         codex.Runtime
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

// NewHandler 创建只路由到 Codex 的微信消息处理器。
func NewHandler(codex codex.Runtime) *Handler {
	return &Handler{
		codex:    codex,
		progress: DefaultProgressConfig(),
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

	// 纯链接归档直接处理，不消耗 Codex turn。
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

	h.sendToCodex(ctx, client, msg, text, images, files, clientID)
}

// sendToCodex 把所有非内置命令统一发送到 Codex。
func (h *Handler) sendToCodex(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text string, images []*ilink.ImageItem, files []*ilink.FileItem, clientID string) {
	var reply, artifactDir string
	if h.codex != nil {
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
		reply, err = h.chatWithCodex(reporter.task.context(), msg.FromUserID, request, reporter.Report)
		if !h.finishTask(msg.FromUserID, reporter) {
			log.Printf("[handler] task cancelled for %s", msg.FromUserID)
			return
		}
		if err != nil {
			reply = fmt.Sprintf("Error: %v", err)
		}
	} else {
		log.Printf("[handler] codex is unavailable for %s", msg.FromUserID)
		reply = "Codex 当前不可用，请稍后重试。"
	}

	h.sendReplyWithMedia(ctx, client, msg, reply, artifactDir, clientID)
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

// chatWithCodex 在归属明确的活动线程里执行一次 Codex turn。
func (h *Handler) chatWithCodex(ctx context.Context, userID string, request codex.ChatRequest, onProgress codex.ProgressHandler) (string, error) {
	if h.codex == nil {
		return "", fmt.Errorf("codex is not initialized")
	}
	threadAgent, ok := h.codex.(codex.ThreadClient)
	if !ok {
		return "", fmt.Errorf("codex thread runtime is invalid")
	}
	if h.sessions == nil {
		return "", fmt.Errorf("session manager is not initialized")
	}
	info := h.codex.Info()
	log.Printf("[handler] dispatching to codex (%s) for %s", info, userID)

	start := time.Now()
	thread, err := h.sessions.EnsureActive(ctx, userID, threadAgent)
	if err != nil {
		return "", err
	}
	var reply string
	if progressCodex, supportsProgress := h.codex.(codex.ProgressClient); supportsProgress {
		reply, err = progressCodex.ChatThreadWithProgress(ctx, thread.ID, request, onProgress)
	} else {
		reply, err = threadAgent.ChatThread(ctx, thread.ID, request)
	}
	if touchErr := h.sessions.Touch(userID, thread.ID, time.Now().Unix()); touchErr != nil {
		log.Printf("[handler] failed to persist session recency (thread=%s): %v", thread.ID, touchErr)
	}
	elapsed := time.Since(start)

	if err != nil {
		log.Printf("[handler] codex error (%s, elapsed=%s): %v", info, elapsed, err)
		return "", err
	}

	log.Printf("[handler] codex replied (%s, elapsed=%s): %q", info, elapsed, truncate(reply, 100))
	return reply, nil
}

func (h *Handler) prepareTaskInput(reporter *progressReporter, text string, images []*ilink.ImageItem, files []*ilink.FileItem) (codex.ChatRequest, func(), error) {
	if len(images) > 0 || len(files) > 0 {
		reporter.Report(codex.ProgressEvent{Kind: codex.ProgressActivity, Text: "正在接收微信附件"})
	}
	request, cleanup, err := prepareCodexInput(reporter.task.context(), text, images, files, "")
	if err != nil {
		return codex.ChatRequest{}, cleanup, err
	}
	if len(request.LocalImages) > 0 || len(request.LocalFiles) > 0 {
		reporter.Report(codex.ProgressEvent{Kind: codex.ProgressActivity, Text: "附件已接收，正在交给 Codex 分析"})
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

func (h *Handler) sessionContext() (codex.ThreadClient, error) {
	if h.sessions == nil {
		return nil, fmt.Errorf("会话管理器未初始化")
	}
	if h.codex == nil {
		return nil, fmt.Errorf("Codex 当前不可用")
	}
	threadAgent, ok := h.codex.(codex.ThreadClient)
	if !ok {
		return nil, fmt.Errorf("Codex 线程运行时无效")
	}
	return threadAgent, nil
}

func (h *Handler) handleSessionReadCommand(ctx context.Context, userID, command string) string {
	threadAgent, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	fields := strings.Fields(command)
	if len(fields) == 1 && fields[0] == "/session" {
		current, currentErr := h.sessions.Current(ctx, userID, threadAgent)
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
	page, listErr := h.sessions.List(ctx, userID, threadAgent, archived, pageNumber, session.DefaultPageSize)
	if listErr != nil {
		return formatSessionError(listErr)
	}
	return formatSessionPage(page, archived)
}

func (h *Handler) handleSessionMutationCommand(ctx context.Context, userID, command string) string {
	threadAgent, err := h.sessionContext()
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
		thread, createErr := h.sessions.New(ctx, userID, threadAgent, argument)
		if createErr != nil {
			return formatSessionError(createErr)
		}
		return "已创建并切换到新会话。\n" + formatThreadIdentity(thread)
	case "use":
		if argument == "" || len(strings.Fields(argument)) != 1 {
			return "用法：/session use <短编号>"
		}
		thread, useErr := h.sessions.Use(ctx, userID, threadAgent, argument)
		if useErr != nil {
			return formatSessionError(useErr)
		}
		return "已切换会话。\n" + formatThreadIdentity(thread)
	case "rename":
		if argument == "" {
			return "用法：/session rename <名称>"
		}
		thread, renameErr := h.sessions.Rename(ctx, userID, threadAgent, argument)
		if renameErr != nil {
			return formatSessionError(renameErr)
		}
		return "会话已重命名。\n" + formatThreadIdentity(thread)
	case "archive":
		if len(strings.Fields(argument)) > 1 {
			return "用法：/session archive [短编号]"
		}
		nextActive, archiveErr := h.sessions.Archive(ctx, userID, threadAgent, argument)
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
		thread, restoreErr := h.sessions.Restore(ctx, userID, threadAgent, argument)
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

func formatThreadIdentity(thread codex.ThreadInfo) string {
	return fmt.Sprintf("名称：%s\n短编号：%s\n状态：%s", threadTitle(thread), session.ShortCode(thread.ID), formatThreadStatus(thread.Status))
}

func threadTitle(thread codex.ThreadInfo) string {
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

func formatThreadStatus(status codex.ThreadStatus) string {
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

// handleCwd 查询或修改 Codex 后续 thread/turn 的工作目录。
func (h *Handler) handleCwd(trimmed string) string {
	arg := strings.TrimSpace(strings.TrimPrefix(trimmed, "/cwd"))
	if arg == "" {
		if h.codex == nil {
			return "Codex 当前不可用。"
		}
		return fmt.Sprintf("cwd: %s", h.codex.Info().Cwd)
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

	if h.codex == nil {
		return "Codex 当前不可用。"
	}
	h.codex.SetCwd(absPath)
	log.Printf("[handler] updated codex cwd: %s", absPath)

	return fmt.Sprintf("cwd: %s", absPath)
}

// buildStatus 返回唯一 Codex 运行时摘要。
func (h *Handler) buildStatus() string {
	if h.codex == nil {
		return "Codex：不可用"
	}
	info := h.codex.Info()
	model := info.Model
	if model == "" {
		model = "使用 Codex 默认配置"
	}
	return fmt.Sprintf("Codex：运行中\n协议：App Server\n模型：%s\n工作目录：%s\nPID：%d", model, info.Cwd, info.PID)
}

func buildHelpText() string {
	return `可用命令：
直接发送图片 - 交给 Codex 分析
直接发送 PDF、代码、压缩包或日志 - 交给 Codex 检查
/status - 查看当前任务状态
/cancel - 取消当前任务
/info - 查看 Codex 运行信息
/sessions [页码] - 查看会话列表
/session - 查看当前会话
/session new [名称] - 创建会话
/session use 短编号 - 切换会话
/session rename 名称 - 重命名当前会话
/session archive [短编号] - 归档会话
/sessions archived [页码] - 查看已归档会话
/session restore 短编号 - 恢复会话
/cwd /path - 切换工作目录
/help - 查看命令列表`
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
