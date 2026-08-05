package messaging

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/weclaw/codex"
	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/preference"
	"github.com/huixiangyang/weclaw/project"
	"github.com/huixiangyang/weclaw/session"
)

// Handler processes incoming WeChat messages and dispatches replies.
type Handler struct {
	codex               codex.Runtime
	contextTokens       sync.Map // map[userID]contextToken
	saveDir             string   // Linkhoard archive directory
	seenMsgs            sync.Map // map[int64]time.Time — dedup by message_id
	activeTasks         sync.Map // map[userID]*activeTask — 同一用户只允许一个活动任务
	pendingInstructions sync.Map // map[userID]string — 运行中仅暂存一条后续指令
	controlDispatches   sync.Map // map[userID]string — 数字菜单触发的一次性 Codex 请求
	controlMedia        sync.Map // map[userID]string — 数字菜单触发的交付物再次发送
	controlVoice        sync.Map // map[userID]bool — 数字菜单触发的一次性语音简报
	controlStates       sync.Map // map[userID]*controlState — 微信数字菜单和待输入状态
	progress            ProgressConfig
	projects            *project.Manager
	sessions            *session.Manager
	visual              controlVisualRenderer
	preferences         *preference.Store
	visualReplies       sync.Map // map[userID]*cachedVisualReply — 最近一条可取回的视觉长回复
	visualReplyEnabled  bool
	visualReplyMinRunes int
	automations         AutomationProvider
	activities          *ActivityStore
	library             *LibraryStore
	remoteLock          *RemoteLock
	voice               *VoiceBriefing
	bridgeVersion       string
	apiAddr             string
	startedAt           time.Time
}

// SetSessionManager 注入显式 Codex 会话管理器。
func (h *Handler) SetSessionManager(manager *session.Manager) {
	h.sessions = manager
	if manager != nil && h.projects != nil {
		manager.SetProjectResolver(func(ownerID string) string {
			return h.projects.Current(ownerID).ID
		})
	}
}

// SetProjectManager 注入项目白名单，并让会话归属跟随当前项目切换。
func (h *Handler) SetProjectManager(manager *project.Manager) {
	h.projects = manager
	if h.sessions != nil && manager != nil {
		h.sessions.SetProjectResolver(func(ownerID string) string {
			return manager.Current(ownerID).ID
		})
	}
}

// SetVisualRenderer 注入可信模板的微信视觉卡片渲染器。
func (h *Handler) SetVisualRenderer(renderer controlVisualRenderer) {
	h.visual = renderer
}

// SetPreferenceStore 注入按绑定用户隔离的视觉与回答偏好。
func (h *Handler) SetPreferenceStore(store *preference.Store) {
	h.preferences = store
}

// SetAutomationProvider 注入确定性自动化中心和手动检查能力。
func (h *Handler) SetAutomationProvider(provider AutomationProvider) {
	h.automations = provider
}

func (h *Handler) SetActivityStore(store *ActivityStore) {
	h.activities = store
}

func (h *Handler) SetLibraryStore(store *LibraryStore) {
	h.library = store
}

func (h *Handler) SetRemoteLock(lock *RemoteLock) {
	h.remoteLock = lock
}

func (h *Handler) SetVoiceBriefing(voice *VoiceBriefing) {
	h.voice = voice
}

// SetBridgeInfo 设置部署身份；运行期间保持不变，可安全用于微信状态快照。
func (h *Handler) SetBridgeInfo(version, apiAddr string) {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "dev"
	}
	h.bridgeVersion = version
	h.apiAddr = strings.TrimSpace(apiAddr)
}

// NewHandler 创建只路由到 Codex 的微信消息处理器。
func NewHandler(codex codex.Runtime) *Handler {
	return &Handler{
		codex:               codex,
		progress:            DefaultProgressConfig(),
		visualReplyEnabled:  true,
		visualReplyMinRunes: 900,
		bridgeVersion:       "dev",
		startedAt:           time.Now(),
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
	userLabel := ilink.LogLabel(msg.FromUserID)

	// 只接受扫码绑定账号发来的消息，避免群聊或其他联系人驱动本机 Codex。
	if ownerUserID := client.OwnerUserID(); ownerUserID != "" && msg.FromUserID != ownerUserID {
		log.Printf("[handler] rejected message from non-owner user %s", userLabel)
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
			log.Printf("[handler] received voice transcription from %s (chars=%d)", userLabel, len([]rune(text)))
		}
	}
	images := extractImages(msg)
	files := extractFiles(msg)
	if text == "" && len(images) == 0 && len(files) == 0 {
		log.Printf("[handler] received unsupported message from %s, skipping", userLabel)
		return
	}

	if len(images) > 0 || len(files) > 0 {
		log.Printf("[handler] received from %s (chars=%d images=%d files=%d)", userLabel, len([]rune(text)), len(images), len(files))
	} else {
		log.Printf("[handler] received from %s (chars=%d)", userLabel, len([]rune(text)))
	}

	// Store context token for this user
	h.contextTokens.Store(msg.FromUserID, msg.ContextToken)

	trimmed := strings.TrimSpace(text)
	clientID := NewClientID()
	if h.remoteLock != nil && h.remoteLock.IsLocked(msg.FromUserID) {
		reply := h.handleLockedInput(msg.FromUserID, trimmed)
		if err := h.sendControlReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[security] failed to send locked-state reply to %s: %v", userLabel, err)
		}
		return
	}
	if len(images) == 0 && len(files) == 0 && h.sendCachedVisualReply(ctx, client, msg, trimmed, clientID) {
		return
	}

	// 控制层只公开“/”和自然语言；数字菜单状态必须先于普通 Codex 消息解析。
	if reply, handled := h.handleControlInput(ctx, msg.FromUserID, trimmed, len(images) > 0 || len(files) > 0); handled {
		if prompt, ok := h.controlDispatches.LoadAndDelete(msg.FromUserID); ok {
			h.sendToCodex(ctx, client, msg, prompt.(string), nil, nil, clientID)
			return
		}
		if path, ok := h.controlMedia.LoadAndDelete(msg.FromUserID); ok {
			if err := SendMediaFromPath(ctx, client, msg.FromUserID, path.(string), msg.ContextToken); err != nil {
				reply = fmt.Sprintf("交付物发送失败：%v", err)
			}
		}
		if _, ok := h.controlVoice.LoadAndDelete(msg.FromUserID); ok {
			_, err := h.sendVoiceBriefing(ctx, client, msg.FromUserID, msg.ContextToken)
			if err != nil {
				reply = fmt.Sprintf("语音简报生成失败：%v", err)
				if sendErr := h.sendControlReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); sendErr != nil {
					log.Printf("[handler] failed to send voice error to %s: %v", userLabel, sendErr)
				}
			}
			// 成功时配套阅读卡和 MP3 已完整交付，不再追加低信息量状态卡。
			return
		}
		if err := h.sendControlReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send control result to %s: %v", userLabel, err)
		}
		return
	}

	// 任务运行期间普通消息直接返回快照，避免切换、归档等控制操作并发改写线程状态。
	if active, ok := h.activeTasks.Load(msg.FromUserID); ok {
		if len(images) == 0 && len(files) == 0 && trimmed != "" {
			reply := h.queuePendingInstruction(msg.FromUserID, trimmed)
			if err := h.sendControlReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to confirm pending instruction to %s: %v", userLabel, err)
			}
		} else {
			h.sendActiveTaskStatus(ctx, client, msg, active.(*activeTask))
		}
		return
	}

	// 纯链接归档直接处理，不消耗 Codex turn。
	if len(images) == 0 && len(files) == 0 && h.saveDir != "" && IsURL(trimmed) {
		rawURL := ExtractURL(trimmed)
		if rawURL != "" {
			log.Printf("[handler] saving URL to linkhoard for %s", userLabel)
			title, err := SaveLinkToLinkhoard(ctx, h.saveDir, rawURL)
			var reply string
			if err != nil {
				log.Printf("[handler] link save failed: %v", err)
				reply = fmt.Sprintf("保存失败: %v", err)
			} else {
				reply = fmt.Sprintf("已保存: %s", title)
				if h.library != nil {
					projectID := h.currentProjectID(msg.FromUserID)
					if _, recordErr := h.library.RecordLink(msg.FromUserID, projectID, title, rawURL); recordErr != nil {
						log.Printf("[library] failed to record link: %v", recordErr)
					}
				}
			}
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", userLabel, err)
			}
			return
		}
	}

	h.sendToCodex(ctx, client, msg, text, images, files, clientID)
}

// sendToCodex 把所有非控制消息统一发送到 Codex。
func (h *Handler) sendToCodex(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text string, images []*ilink.ImageItem, files []*ilink.FileItem, clientID string) {
	var reply, artifactDir string
	if h.codex != nil {
		reporter, ok := h.beginTask(ctx, client, msg)
		if !ok {
			return
		}
		activityID := h.startActivity(msg.FromUserID, taskActivitySummary(text, len(images), len(files)))
		reporter.task.setActivity(activityID)
		request, cleanup, prepareErr := h.prepareTaskInput(reporter, text, images, files)
		if prepareErr != nil {
			log.Printf("[handler] failed to prepare inbound attachments for %s: %v", ilink.LogLabel(msg.FromUserID), prepareErr)
			if h.finishTask(msg.FromUserID, reporter) {
				h.finishActivity(msg.FromUserID, activityID, ActivityFailed)
				reply = fmt.Sprintf("附件处理失败：%v", prepareErr)
				h.sendReplyWithMedia(ctx, client, msg, reply, "", clientID)
			} else {
				h.finishActivity(msg.FromUserID, activityID, ActivityCancelled)
			}
			return
		}
		defer cleanup()
		artifactDir = request.ArtifactDir

		var err error
		reply, err = h.chatWithCodex(reporter.task.context(), msg.FromUserID, request, reporter.Report)
		if !h.finishTask(msg.FromUserID, reporter) {
			h.finishActivity(msg.FromUserID, activityID, ActivityCancelled)
			log.Printf("[handler] task cancelled for %s", ilink.LogLabel(msg.FromUserID))
			h.drainPendingInstruction(ctx, client, msg.FromUserID)
			return
		}
		if err != nil {
			h.finishActivity(msg.FromUserID, activityID, ActivityFailed)
			reply = fmt.Sprintf("Error: %v", err)
		} else {
			h.finishActivity(msg.FromUserID, activityID, ActivitySucceeded)
		}
	} else {
		log.Printf("[handler] codex is unavailable for %s", ilink.LogLabel(msg.FromUserID))
		reply = "Codex 当前不可用，请稍后重试。"
	}

	h.sendReplyWithMedia(ctx, client, msg, reply, artifactDir, clientID)
	h.drainPendingInstruction(ctx, client, msg.FromUserID)
}

func (h *Handler) startActivity(userID, summary string) string {
	if h.activities == nil {
		return ""
	}
	projectID := ""
	if h.projects != nil {
		projectID = h.projects.Current(userID).ID
	}
	id, err := h.activities.StartForProject(userID, projectID, summary)
	if err != nil {
		log.Printf("[activity] failed to start task record: %v", err)
		return ""
	}
	return id
}

func (h *Handler) finishActivity(userID, id string, status ActivityStatus) {
	if h.activities == nil || id == "" {
		return
	}
	if err := h.activities.Finish(userID, id, status); err != nil {
		log.Printf("[activity] failed to finish task record: %v", err)
	}
}

func taskActivitySummary(text string, imageCount, fileCount int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	text = normalizeSessionLine(text, 72)
	text = sanitizeActivitySummary(text)
	if text == "" {
		switch {
		case imageCount > 0 && fileCount > 0:
			text = fmt.Sprintf("附件分析 · %d 张图片 · %d 个文件", imageCount, fileCount)
		case imageCount > 0:
			text = fmt.Sprintf("图片分析 · %d 张", imageCount)
		case fileCount > 0:
			text = fmt.Sprintf("文件分析 · %d 个", fileCount)
		default:
			text = "Codex 任务"
		}
		return text
	}
	if imageCount > 0 {
		text += fmt.Sprintf(" · %d 张图片", imageCount)
	}
	if fileCount > 0 {
		text += fmt.Sprintf(" · %d 个文件", fileCount)
	}
	return normalizeSessionLine(text, 120)
}

func sanitizeActivitySummary(summary string) string {
	summary = activityURLPattern.ReplaceAllString(summary, "[链接]")
	summary = activityWindowsPathPattern.ReplaceAllString(summary, "[本机路径]")
	summary = activityUnixPathPattern.ReplaceAllString(summary, "[本机路径]")
	return strings.Join(strings.Fields(summary), " ")
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
			log.Printf("[handler] failed to send attachment to %s: %v", ilink.LogLabel(msg.FromUserID), err)
			failed = append(failed, filepath.Base(attachmentPath)+"（上传失败）")
			continue
		}
		sentPaths = append(sentPaths, attachmentPath)
		if h.library != nil {
			if _, recordErr := h.library.RecordDelivery(msg.FromUserID, h.currentProjectID(msg.FromUserID), attachmentPath); recordErr != nil {
				log.Printf("[library] failed to archive delivery: %v", recordErr)
			}
		}
	}

	reply = appendArtifactSummary(reply, sentPaths, failed)

	delivered := false
	switch h.currentResponseMode(msg.FromUserID) {
	case preference.ResponseVoice:
		voiceDelivered, voiceErr := h.sendVoiceCodexReply(ctx, client, msg.FromUserID, reply, msg.ContextToken)
		delivered = voiceDelivered
		if voiceErr != nil {
			log.Printf("[voice] failed to send Codex voice response to %s: %v", ilink.LogLabel(msg.FromUserID), voiceErr)
		}
		if !delivered {
			delivered, voiceErr = h.sendVisualReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID, true)
			if voiceErr != nil {
				log.Printf("[visual] failed to send voice fallback reading cards to %s: %v", ilink.LogLabel(msg.FromUserID), voiceErr)
			}
		}
	case preference.ResponseReading:
		var visualErr error
		delivered, visualErr = h.sendVisualReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID, true)
		if visualErr != nil {
			log.Printf("[visual] failed to send forced reading reply to %s: %v", ilink.LogLabel(msg.FromUserID), visualErr)
		}
	default:
		var visualErr error
		delivered, visualErr = h.sendVisualReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID, false)
		if visualErr != nil {
			log.Printf("[visual] failed to send long reply to %s: %v", ilink.LogLabel(msg.FromUserID), visualErr)
		}
	}
	if !delivered {
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", ilink.LogLabel(msg.FromUserID), err)
		}
	}

	for _, imgURL := range imageURLs {
		if err := SendMediaFromURL(ctx, client, msg.FromUserID, imgURL, msg.ContextToken); err != nil {
			log.Printf("[handler] failed to send image to %s: %v", ilink.LogLabel(msg.FromUserID), err)
		}
	}
}

func (h *Handler) currentProjectID(userID string) string {
	if h.projects == nil {
		return ""
	}
	return h.projects.Current(userID).ID
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
	projectID := "injected-runtime"
	if h.projects != nil {
		currentProject := h.projects.Current(userID)
		projectID = currentProject.ID
		// 工作目录只来自受信任项目清单，杜绝微信消息注入任意本机路径。
		h.codex.SetCwd(currentProject.Root)
	}
	info := h.codex.Info()
	log.Printf("[handler] dispatching to codex (%s, project=%s) for %s", info, projectID, ilink.LogLabel(userID))

	start := time.Now()
	thread, err := h.sessions.EnsureActive(ctx, userID, threadAgent, suggestedSessionName(request))
	if err != nil {
		return "", err
	}
	if active, exists := h.activeTasks.Load(userID); exists && h.activities != nil {
		if attachErr := h.activities.AttachSession(userID, active.(*activeTask).activityID(), thread.ID); attachErr != nil {
			log.Printf("[activity] failed to attach session: %v", attachErr)
		}
	}
	var reply string
	if progressCodex, supportsProgress := h.codex.(codex.ProgressClient); supportsProgress {
		reply, err = progressCodex.ChatThreadWithProgress(ctx, thread.ID, request, onProgress)
	} else {
		reply, err = threadAgent.ChatThread(ctx, thread.ID, request)
	}
	if usageProvider, ok := h.codex.(codex.UsageProvider); ok && h.activities != nil {
		if usage, exists := usageProvider.Usage(thread.ID); exists {
			if active, running := h.activeTasks.Load(userID); running {
				if usageErr := h.activities.AttachUsage(userID, active.(*activeTask).activityID(), usage.Last); usageErr != nil {
					log.Printf("[activity] failed to attach token usage: %v", usageErr)
				}
			}
		}
	}
	if touchErr := h.sessions.Touch(userID, thread.ID, time.Now().Unix()); touchErr != nil {
		log.Printf("[handler] failed to persist session recency (thread=%s): %v", thread.ID, touchErr)
	}
	elapsed := time.Since(start)

	if err != nil {
		log.Printf("[handler] codex error (%s, elapsed=%s): %v", info, elapsed, err)
		return "", err
	}

	log.Printf("[handler] codex replied (%s, elapsed=%s, chars=%d)", info, elapsed, len([]rune(reply)))
	return reply, nil
}

func (h *Handler) queuePendingInstruction(userID, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "下一条指令不能为空。"
	}
	if _, exists := h.pendingInstructions.Load(userID); exists {
		return "已经暂存了一条后续指令。发送“清除暂存”后可以重新设置。"
	}
	h.pendingInstructions.Store(userID, text)
	return "已暂存下一条指令。当前任务结束后会自动继续执行。\n摘要：" + normalizeSessionLine(text, 72)
}

func (h *Handler) clearPendingInstruction(userID string) string {
	if _, exists := h.pendingInstructions.LoadAndDelete(userID); !exists {
		return "当前没有暂存的后续指令。"
	}
	return "已清除暂存指令。"
}

func (h *Handler) drainPendingInstruction(ctx context.Context, client *ilink.Client, userID string) {
	if h.remoteLock != nil && h.remoteLock.IsLocked(userID) {
		h.pendingInstructions.Delete(userID)
		return
	}
	if h.hasActiveTask(userID) {
		return
	}
	value, exists := h.pendingInstructions.LoadAndDelete(userID)
	if !exists {
		return
	}
	contextToken := ""
	if stored, ok := h.contextTokens.Load(userID); ok {
		contextToken, _ = stored.(string)
	}
	text := value.(string)
	_ = SendTextReply(ctx, client, userID, "开始执行已暂存指令："+normalizeSessionLine(text, 48), contextToken, NewClientID())
	h.sendToCodex(ctx, client, ilink.WeixinMessage{FromUserID: userID, ContextToken: contextToken}, text, nil, nil, NewClientID())
}

func suggestedSessionName(request codex.ChatRequest) string {
	text := strings.TrimSpace(request.Text)
	if text != "" {
		if index := strings.IndexByte(text, '\n'); index >= 0 {
			text = text[:index]
		}
		text = strings.Join(strings.Fields(text), " ")
		return truncateRunes(text, 36)
	}
	if len(request.LocalImages) > 0 {
		return "图片分析"
	}
	if len(request.LocalFiles) > 0 {
		name := strings.TrimSpace(request.LocalFiles[0].Name)
		if name != "" {
			return truncateRunes("文件分析 · "+name, 36)
		}
		return "文件分析"
	}
	return ""
}

func (h *Handler) prepareTaskInput(reporter *progressReporter, text string, images []*ilink.ImageItem, files []*ilink.FileItem) (codex.ChatRequest, func(), error) {
	if len(images) > 0 || len(files) > 0 {
		reporter.Report(codex.ProgressEvent{Kind: codex.ProgressActivity, Text: "正在接收微信附件"})
	}
	request, cleanup, err := prepareCodexInput(reporter.task.context(), text, images, files, "")
	if err != nil {
		return codex.ChatRequest{}, cleanup, err
	}
	if len(request.LocalImages) > 0 && isImageAnnotationIntent(text) {
		request.Text = strings.TrimSpace(request.Text + "\n\n[WeClaw 图片批注模式]\n请先理解图片和用户意图，再生成一张带有清晰、克制、移动端可读批注的 PNG。必须把最终图片写入本次 WeClaw 交付目录并回传；不得覆盖入站原图。")
	}
	if len(request.LocalImages) > 0 || len(request.LocalFiles) > 0 {
		reporter.Report(codex.ProgressEvent{Kind: codex.ProgressActivity, Text: "附件已接收，正在交给 Codex 分析"})
	}
	return request, cleanup, nil
}

func isImageAnnotationIntent(text string) bool {
	normalized := normalizeControlPhrase(text)
	for _, marker := range []string{"批注图片", "标注图片", "批注这张图", "标注这张图", "在图上标注"} {
		if strings.Contains(normalized, normalizeControlPhrase(marker)) {
			return true
		}
	}
	return false
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
	if err := h.sendControlReply(ctx, client, msg.FromUserID, task.busySummary(), msg.ContextToken, NewClientID()); err != nil {
		log.Printf("[handler] failed to send active task status to %s: %v", ilink.LogLabel(msg.FromUserID), err)
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

// buildStatus 返回桥接器与唯一 Codex 运行时的完整摘要。
func (h *Handler) buildStatus() string {
	lines := []string{
		"运行中心",
		"WeClaw：运行中",
		"版本：" + h.bridgeVersion,
		"已运行：" + formatUptime(time.Since(h.startedAt)),
	}
	if h.apiAddr != "" {
		lines = append(lines, "本地接口："+h.apiAddr)
	}
	if h.codex == nil {
		return strings.Join(append(lines, "Codex：不可用"), "\n")
	}
	info := h.codex.Info()
	model := info.Model
	if model == "" {
		model = "使用 Codex 默认配置"
	}
	lines = append(lines,
		"Codex：运行中",
		"协议：App Server",
		"模型："+model,
		"项目目录："+info.Cwd,
	)
	if info.PID > 0 {
		lines = append(lines, fmt.Sprintf("Codex PID：%d", info.PID))
	}
	if usageProvider, ok := h.codex.(codex.UsageProvider); ok {
		if limits, exists := usageProvider.RateLimits(); exists {
			if limits.Primary != nil {
				lines = append(lines, fmt.Sprintf("主额度：已用 %d%%", limits.Primary.UsedPercent))
			}
			if limits.Secondary != nil {
				lines = append(lines, fmt.Sprintf("次额度：已用 %d%%", limits.Secondary.UsedPercent))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func formatUptime(elapsed time.Duration) string {
	if elapsed < 0 {
		elapsed = 0
	}
	days := int(elapsed / (24 * time.Hour))
	if days > 0 {
		hours := int((elapsed % (24 * time.Hour)) / time.Hour)
		if hours == 0 {
			return fmt.Sprintf("%d 天", days)
		}
		return fmt.Sprintf("%d 天 %d 小时", days, hours)
	}
	hours := int(elapsed / time.Hour)
	if hours > 0 {
		minutes := int((elapsed % time.Hour) / time.Minute)
		if minutes == 0 {
			return fmt.Sprintf("%d 小时", hours)
		}
		return fmt.Sprintf("%d 小时 %d 分", hours, minutes)
	}
	return formatElapsed(elapsed)
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
