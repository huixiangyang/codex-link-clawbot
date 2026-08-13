package messaging

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/project"
	"github.com/huixiangyang/codex-link-clawbot/internal/session"
	"github.com/huixiangyang/codex-link-clawbot/internal/taskqueue"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

var (
	taskSummaryURLPattern         = regexp.MustCompile(`(?i)https?://[^\s，。！？；;]+`)
	taskSummaryUnixPathPattern    = regexp.MustCompile(`/[^\s，。！？；;]+`)
	taskSummaryWindowsPathPattern = regexp.MustCompile(`(?i)[a-z]:[\\/][^\s，。！？；;]+`)
)

// Handler processes incoming WeChat messages and dispatches replies.
type Handler struct {
	codex               codex.Runtime
	controlStates       *ControlStateStore
	intents             *IntentRegistry
	progress            ProgressConfig
	projects            *project.Manager
	sessions            *session.Manager
	visual              controlVisualRenderer
	preferences         *preference.Store
	visualReplies       sync.Map // map[userID]*cachedVisualReply — 最近一条可取回的视觉长回复
	visualReplyEnabled  bool
	visualReplyMinRunes int
	tasks               *taskqueue.Store
	coordinator         *Coordinator
	lifecycle           ingressLifecycle
	deliveries          *DeliveryStore
	pendingNotices      *PendingNoticeStore
	remoteLock          *RemoteLock
	voice               *VoiceBriefing
	bridgeVersion       string
	startedAt           time.Time
}

type ingressLifecycle interface {
	BeginIngress()
	EndIngress()
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

// SetControlStateStore 注入持久菜单 revision 存储；运行时必须显式配置。
func (h *Handler) SetControlStateStore(store *ControlStateStore) {
	h.controlStates = store
}

// SetProjectManager 注入 Codex 工作空间白名单，并让远程线程焦点跟随工作空间切换。
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

func (h *Handler) SetTaskQueue(store *taskqueue.Store, coordinator *Coordinator) {
	h.tasks = store
	h.coordinator = coordinator
}

func (h *Handler) SetRuntimeLifecycle(lifecycle ingressLifecycle) {
	h.lifecycle = lifecycle
}

func (h *Handler) SetDeliveryStore(store *DeliveryStore) {
	h.deliveries = store
}

func (h *Handler) SetPendingNoticeStore(store *PendingNoticeStore) {
	h.pendingNotices = store
}

func (h *Handler) SetRemoteLock(lock *RemoteLock) {
	h.remoteLock = lock
}

func (h *Handler) SetVoiceBriefing(voice *VoiceBriefing) {
	h.voice = voice
}

// SetBridgeInfo 设置部署身份；运行期间保持不变，可安全用于微信状态快照。
func (h *Handler) SetBridgeInfo(version string) {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "dev"
	}
	h.bridgeVersion = version
}

// NewHandler 创建只路由到 Codex 的微信消息处理器。
func NewHandler(codex codex.Runtime) *Handler {
	return &Handler{
		codex:               codex,
		intents:             mustDefaultIntentRegistry(),
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

// HandleMessage processes a single incoming message.
func (h *Handler) HandleMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) error {
	// Only process user messages that are finished
	if msg.MessageType != ilink.MessageTypeUser {
		return nil
	}
	if msg.MessageState != ilink.MessageStateFinish {
		return nil
	}
	userLabel := ilink.LogLabel(msg.FromUserID)

	// 只接受扫码绑定账号发来的消息，避免群聊或其他联系人驱动本机 Codex。
	if ownerUserID := client.OwnerUserID(); ownerUserID != "" && msg.FromUserID != ownerUserID {
		log.Printf("[handler] rejected message from non-owner user %s", userLabel)
		return nil
	}
	if h.coordinator != nil {
		h.coordinator.RegisterOwnerClient(msg.FromUserID, client)
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
		return nil
	}

	if len(images) > 0 || len(files) > 0 {
		log.Printf("[handler] received from %s (chars=%d images=%d files=%d)", userLabel, len([]rune(text)), len(images), len(files))
	} else {
		log.Printf("[handler] received from %s (chars=%d)", userLabel, len([]rune(text)))
	}

	trimmed := strings.TrimSpace(text)
	clientID := NewClientID()
	controlSourceKey, _ := sourceMessageKey(client, msg)
	if h.remoteLock != nil && h.remoteLock.IsLocked(msg.FromUserID) {
		reply := h.handleLockedInput(msg.FromUserID, trimmed)
		if h.isNoReplyDiagnostic(trimmed) {
			reply = h.buildNoReplyDiagnostic(msg.FromUserID)
		}
		if err := h.sendControlReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[security] failed to send locked-state reply to %s: %v", userLabel, err)
		}
		return nil
	}
	h.flushPendingNotices(ctx, client, msg.FromUserID, msg.ContextToken)
	if len(images) == 0 && len(files) == 0 && h.sendCachedVisualReply(ctx, client, msg, trimmed, clientID) {
		return nil
	}

	// 控制层只公开“/”和自然语言；数字菜单状态必须先于普通 Codex 消息解析。
	if result, handled := h.handleControlInput(ctx, msg.FromUserID, trimmed, len(images) > 0 || len(files) > 0, controlSourceKey); handled {
		if err := h.presentActionResult(ctx, client, msg, result, clientID); err != nil {
			log.Printf("[handler] failed to present action result to %s: %v", userLabel, err)
			// 入队和重试使用持久来源键，失败可以安全交给微信长轮询重投。
			// 其他控制动作可能已经修改本地状态，投递失败不能再次执行。
			if result.Effect.Kind == EffectEnqueuePrompt || result.Effect.Kind == EffectRetryTask {
				if result.rollback != nil && h.controlStates != nil {
					var rollbackErr error
					if result.rollback.State != nil {
						rollbackErr = h.controlStates.RollbackConsumedReceipt(
							result.rollback.OwnerID, result.rollback.SourceKey, *result.rollback.State,
						)
					} else {
						rollbackErr = h.controlStates.RollbackReservedReceipt(
							result.rollback.OwnerID, result.rollback.SourceKey,
							result.rollback.ActionID, result.rollback.Domain,
						)
					}
					if rollbackErr != nil {
						logControlStateError(msg.FromUserID, rollbackErr)
						return fmt.Errorf("present action result: %w; restore control receipt: %v", err, rollbackErr)
					}
				}
				return err
			}
		}
		return nil
	}

	return h.enqueueCodexTask(ctx, client, msg, text, images, files, clientID)
}

func (h *Handler) sendFrozenTaskText(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, taskID, clientID string) {
	if h.tasks == nil {
		return
	}
	result, err := h.tasks.LoadResult(msg.FromUserID, taskID)
	if err != nil {
		_ = h.sendControlReply(ctx, client, msg.FromUserID, "冻结结果已过期或损坏，无法取回文字。", msg.ContextToken, clientID)
		return
	}
	if err := SendTextReply(ctx, client, msg.FromUserID, result.Reply, msg.ContextToken, clientID); err != nil {
		log.Printf("[queue] failed to send manually recovered task text: %v", err)
		_ = h.sendControlReply(ctx, client, msg.FromUserID, "冻结文字发送失败。执行记录没有改写，可从 codex-link-clawbot 请求队列再次尝试。", msg.ContextToken, NewClientID())
	}
}

func (h *Handler) retryCodexTask(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, taskID, clientID string) error {
	if h.tasks == nil || h.coordinator == nil || h.projects == nil {
		return fmt.Errorf("task queue is not initialized")
	}
	sourceKey, err := sourceMessageKey(client, msg)
	if err != nil {
		return err
	}
	task, err := h.tasks.Retry(msg.FromUserID, taskID, sourceKey, msg.ContextToken, true)
	if err != nil {
		reply := "请求无法重试：" + err.Error()
		if sendErr := h.sendControlReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); sendErr != nil {
			return fmt.Errorf("send retry rejection: %w", sendErr)
		}
		return nil
	}
	projectName := task.ProjectID
	if definition, ok := h.projects.Get(task.ProjectID); ok {
		projectName = definition.Name
	}
	if err := h.sendControlReply(ctx, client, msg.FromUserID, queuedTaskAcknowledgement(h.tasks, task, projectName, false), msg.ContextToken, clientID); err != nil {
		return fmt.Errorf("confirm retried task: %w", err)
	}
	if task.AwaitingAcknowledgement {
		if err := h.tasks.Acknowledge(msg.FromUserID, task.ID); err != nil {
			return fmt.Errorf("persist retried task acknowledgement: %w", err)
		}
	}
	h.coordinator.Wake()
	return nil
}

// enqueueCodexTask 只负责可靠入队。Codex 执行权始终由全局 Coordinator 持有。
func (h *Handler) enqueueCodexTask(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text string, images []*ilink.ImageItem, files []*ilink.FileItem, clientID string) error {
	return h.enqueueCodexTaskInProject(ctx, client, msg, text, images, files, clientID, "", "", false)
}

// enqueueCodexTaskInProject 允许工作流和任务复用冻结项目、既有线程或强制新线程；普通消息使用界面快照。
func (h *Handler) enqueueCodexTaskInProject(
	ctx context.Context,
	client *ilink.Client,
	msg ilink.WeixinMessage,
	text string,
	images []*ilink.ImageItem,
	files []*ilink.FileItem,
	clientID, projectID, threadID string,
	newThread bool,
) error {
	if h.tasks == nil || h.coordinator == nil || h.projects == nil || h.sessions == nil || h.preferences == nil {
		return fmt.Errorf("task queue is not initialized")
	}
	sourceKey, err := sourceMessageKey(client, msg)
	if err != nil {
		if sendErr := h.sendControlReply(ctx, client, msg.FromUserID, "这条微信消息没有稳定来源编号，无法安全入队。", msg.ContextToken, clientID); sendErr != nil {
			log.Printf("[queue] failed to send invalid source notice: %v", sendErr)
		}
		return nil
	}
	if existing, exists := h.tasks.FindBySource(sourceKey); exists {
		projectName := existing.ProjectID
		if definition, ok := h.projects.Get(existing.ProjectID); ok {
			projectName = definition.Name
		}
		reply := queuedTaskAcknowledgement(h.tasks, existing, projectName, true)
		if err := h.sendControlReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			return fmt.Errorf("confirm existing queued task: %w", err)
		}
		if existing.AwaitingAcknowledgement {
			if err := h.tasks.Acknowledge(msg.FromUserID, existing.ID); err != nil {
				return fmt.Errorf("persist existing task acknowledgement: %w", err)
			}
		}
		h.coordinator.Wake()
		return nil
	}
	if h.lifecycle != nil {
		h.lifecycle.BeginIngress()
		defer h.lifecycle.EndIngress()
	}
	text, queuedImages, queuedFiles, err := prepareQueuedInput(ctx, text, images, files)
	if err != nil {
		reply := "附件接收失败：" + err.Error()
		if sendErr := h.sendControlReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); sendErr != nil {
			log.Printf("[queue] failed to send attachment rejection: %v", sendErr)
		}
		return nil
	}
	currentProject := h.projects.Current(msg.FromUserID)
	if strings.TrimSpace(projectID) != "" {
		var exists bool
		currentProject, exists = h.projects.Get(strings.TrimSpace(projectID))
		if !exists {
			return fmt.Errorf("frozen project is unavailable")
		}
	}
	taskThreadID := strings.TrimSpace(threadID)
	if !newThread && taskThreadID == "" {
		taskThreadID = h.sessions.SnapshotThreadID(msg.FromUserID, currentProject.ID)
	}
	preferences := h.preferences.Get(msg.FromUserID)
	task, existed, err := h.tasks.Enqueue(taskqueue.EnqueueInput{
		SourceMessageKey:       sourceKey,
		OwnerID:                msg.FromUserID,
		ProjectID:              currentProject.ID,
		ThreadID:               taskThreadID,
		Summary:                taskActivitySummary(text, len(queuedImages), len(queuedFiles)),
		Text:                   text,
		ContextToken:           msg.ContextToken,
		ResponseMode:           preferences.ResponseMode,
		VisualStyle:            preferences.Style,
		RequireAcknowledgement: true,
		Images:                 queuedImages,
		Files:                  queuedFiles,
	})
	if err != nil {
		// 写盘失败必须向上返回，监控器不能推进微信同步游标。
		return fmt.Errorf("persist WeChat task: %w", err)
	}
	reply := queuedTaskAcknowledgement(h.tasks, task, currentProject.Name, existed)
	if err := h.sendControlReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
		// 确认失败也不推进游标；同一来源键重投只会返回已有任务。
		return fmt.Errorf("confirm queued task: %w", err)
	}
	if err := h.tasks.Acknowledge(msg.FromUserID, task.ID); err != nil {
		return fmt.Errorf("persist queued task acknowledgement: %w", err)
	}
	h.coordinator.Wake()
	return nil
}

func sourceMessageKey(client *ilink.Client, msg ilink.WeixinMessage) (string, error) {
	account := strings.TrimSpace(client.BotID())
	if account == "" {
		account = strings.TrimSpace(msg.ToUserID)
	}
	if account == "" {
		account = strings.TrimSpace(client.OwnerUserID())
	}
	if account == "" {
		return "", fmt.Errorf("message account is missing")
	}
	digest := sha256.Sum256([]byte(account))
	switch {
	case msg.MessageID != 0:
		return fmt.Sprintf("%x:message:%d", digest[:12], msg.MessageID), nil
	case msg.Seq != 0:
		return fmt.Sprintf("%x:seq:%d", digest[:12], msg.Seq), nil
	default:
		return "", fmt.Errorf("message id and sequence are missing")
	}
}

func queuedTaskAcknowledgement(store *taskqueue.Store, task taskqueue.Task, projectName string, existed bool) string {
	state := "已可靠加入队列"
	if existed {
		state = "这条消息已经入队，不会重复执行"
	}
	positionText := "等待协调器领取"
	if position, ok := store.QueuePosition(task.OwnerID, task.ID); ok {
		positionText = fmt.Sprintf("当前排位：%d", position)
	} else if task.State == taskqueue.StateRunning || task.State == taskqueue.StateDelivering {
		positionText = "当前状态：" + task.Stage
	} else if task.State.Terminal() {
		positionText = "当前状态：" + task.Stage
	}
	return strings.Join([]string{
		"codex-link-clawbot 请求已接收",
		state,
		"Codex 工作空间：" + projectName,
		positionText,
		"摘要：" + task.Summary,
	}, "\n")
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
			text = "Codex 轮次"
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
	summary = taskSummaryURLPattern.ReplaceAllString(summary, "[链接]")
	summary = taskSummaryWindowsPathPattern.ReplaceAllString(summary, "[本机路径]")
	summary = taskSummaryUnixPathPattern.ReplaceAllString(summary, "[本机路径]")
	return strings.Join(strings.Fields(summary), " ")
}

// sendReplyWithMedia 发送最终文字、远程图片和本次 turn 的专属交付物。
func (h *Handler) sendReplyWithMedia(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, reply, artifactDir, clientID string) deliveryReport {
	artifacts, collectErr := collectArtifacts(artifactDir)
	failed := append([]string(nil), artifacts.Skipped...)
	if collectErr != nil {
		failed = append(failed, collectErr.Error())
	}
	return h.deliverReplyPlan(ctx, client, msg, reply, artifacts.Paths, failed, ExtractImageURLs(reply), clientID,
		DeliverySource{ProjectID: h.currentProjectID(msg.FromUserID)}, h.currentResponseMode(msg.FromUserID), h.currentVisualStyle(msg.FromUserID), "")
}

func (h *Handler) sendReplyWithMediaForTask(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, task taskqueue.Task, result taskqueue.Result, clientID string) deliveryReport {
	projectName := task.ProjectID
	if h.projects != nil {
		if definition, ok := h.projects.Get(task.ProjectID); ok {
			projectName = definition.Name
		}
	}
	paths := make([]string, 0, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		paths = append(paths, filepath.Join(h.tasks.Root(), task.ID, artifact.Path))
	}
	return h.deliverReplyPlan(ctx, client, msg, result.Reply, paths, nil, result.ImageURLs, clientID,
		DeliverySource{ProjectID: task.ProjectID, ThreadID: task.ThreadID, TaskID: task.ID}, task.ResponseMode, task.VisualStyle, projectName)
}

type deliveryReport struct {
	Outcome   taskqueue.DeliveryOutcome
	MediaSent int
	TextSent  bool
	Failure   string
}

func (h *Handler) deliverReplyPlan(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, reply string, artifactPaths, initialFailures, imageURLs []string, clientID string, source DeliverySource, mode preference.ResponseMode, style visual.Style, projectName string) deliveryReport {
	report := deliveryReport{Outcome: taskqueue.DeliverySucceeded}
	var sentPaths []string
	failed := append([]string(nil), initialFailures...)
	failedDelivery := len(failed) > 0
	mayBeVisible := false
	for _, attachmentPath := range artifactPaths {
		if err := SendMediaFromPath(ctx, client, msg.FromUserID, attachmentPath, msg.ContextToken); err != nil {
			log.Printf("[handler] failed to send attachment to %s: %v", ilink.LogLabel(msg.FromUserID), err)
			failed = append(failed, filepath.Base(attachmentPath)+"（上传失败）")
			failedDelivery = true
			mayBeVisible = mayBeVisible || outboundMayBeVisible(err) || report.MediaSent > 0
			continue
		}
		sentPaths = append(sentPaths, attachmentPath)
		report.MediaSent++
		if h.deliveries != nil && validateDeliverySource(source) == nil {
			if _, recordErr := h.deliveries.RecordDelivery(msg.FromUserID, source, attachmentPath); recordErr != nil {
				log.Printf("[delivery-store] failed to archive delivery: %v", recordErr)
			}
		}
	}

	reply = appendArtifactSummary(reply, sentPaths, failed)

	delivered := false
	var deliveryErr error
	switch mode {
	case preference.ResponseVoice:
		voiceDelivered, voiceErr := h.sendVoiceCodexReplySnapshot(ctx, client, msg.FromUserID, reply, msg.ContextToken, style, projectName)
		delivered = voiceDelivered
		if voiceDelivered {
			// 语音批次可能包含多页阅读卡、伴随卡和 MP3；当前收据至少记录该批次已可见。
			report.MediaSent++
		}
		if voiceErr != nil {
			log.Printf("[voice] failed to send Codex voice response to %s: %v", ilink.LogLabel(msg.FromUserID), voiceErr)
			if delivered {
				deliveryErr = voiceErr
			}
		}
		if !delivered {
			var visualCount int
			visualCount, voiceErr = h.sendVisualReplyWithStyle(ctx, client, msg.FromUserID, reply, msg.ContextToken, true, style)
			delivered = visualCount > 0
			report.MediaSent += visualCount
			if voiceErr != nil {
				log.Printf("[visual] failed to send voice fallback reading cards to %s: %v", ilink.LogLabel(msg.FromUserID), voiceErr)
				if delivered {
					deliveryErr = voiceErr
				}
			}
		}
	case preference.ResponseReading:
		var visualErr error
		var visualCount int
		visualCount, visualErr = h.sendVisualReplyWithStyle(ctx, client, msg.FromUserID, reply, msg.ContextToken, true, style)
		delivered = visualCount > 0
		report.MediaSent += visualCount
		if visualErr != nil {
			log.Printf("[visual] failed to send forced reading reply to %s: %v", ilink.LogLabel(msg.FromUserID), visualErr)
			if delivered {
				deliveryErr = visualErr
			}
		}
	default:
		var visualErr error
		var visualCount int
		visualCount, visualErr = h.sendVisualReplyWithStyle(ctx, client, msg.FromUserID, reply, msg.ContextToken, false, style)
		delivered = visualCount > 0
		report.MediaSent += visualCount
		if visualErr != nil {
			log.Printf("[visual] failed to send long reply to %s: %v", ilink.LogLabel(msg.FromUserID), visualErr)
			if delivered {
				deliveryErr = visualErr
			}
		}
	}
	if deliveryErr != nil {
		failedDelivery = true
		mayBeVisible = true
	}
	if !delivered {
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", ilink.LogLabel(msg.FromUserID), err)
			failedDelivery = true
			mayBeVisible = mayBeVisible || outboundMayBeVisible(err) || report.MediaSent > 0
		} else {
			report.TextSent = true
		}
	}

	for _, imgURL := range imageURLs {
		if err := SendMediaFromURL(ctx, client, msg.FromUserID, imgURL, msg.ContextToken); err != nil {
			log.Printf("[handler] failed to send image to %s: %v", ilink.LogLabel(msg.FromUserID), err)
			failedDelivery = true
			mayBeVisible = mayBeVisible || outboundMayBeVisible(err) || report.MediaSent > 0 || report.TextSent
		} else {
			report.MediaSent++
		}
	}
	if failedDelivery {
		if mayBeVisible || report.MediaSent > 0 || report.TextSent {
			report.Outcome = taskqueue.DeliveryAmbiguous
			report.Failure = taskqueue.ReasonDeliveryAmbiguous
		} else {
			report.Outcome = taskqueue.DeliveryExplicitFailure
			report.Failure = taskqueue.ReasonDeliveryFailed
		}
	}
	return report
}

func (h *Handler) currentProjectID(userID string) string {
	if h.projects == nil {
		return ""
	}
	return h.projects.Current(userID).ID
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

func isImageAnnotationIntent(text string) bool {
	normalized := normalizeControlPhrase(text)
	for _, marker := range []string{"批注图片", "标注图片", "批注这张图", "标注这张图", "在图上标注"} {
		if strings.Contains(normalized, normalizeControlPhrase(marker)) {
			return true
		}
	}
	return false
}

func (h *Handler) cancelActiveTask(userID string) string {
	if h.coordinator == nil || !h.hasActiveTask(userID) {
		return "codex-link-clawbot 当前没有正在执行的请求。"
	}
	if !h.coordinator.Cancel(userID) {
		return "当前请求正在取消或已进入发送阶段，请稍候。"
	}
	return "已请求取消 codex-link-clawbot 当前执行。如果 Codex 轮次已经启动，也会请求中断；队列会保留取消记录。"
}

func (h *Handler) buildTaskStatus(userID string) string {
	if h.tasks != nil {
		for _, task := range h.tasks.List(userID) {
			if task.State == taskqueue.StateRunning || task.State == taskqueue.StateDelivering {
				return strings.Join([]string{
					"codex-link-clawbot 执行状态：" + taskStateText(task.State),
					"当前阶段：" + task.Stage,
					"摘要：" + task.Summary,
				}, "\n")
			}
		}
	}
	return "codex-link-clawbot 执行状态：空闲\n" + h.buildStatus()
}

func (h *Handler) sessionContext() (codex.ThreadClient, error) {
	if h.sessions == nil {
		return nil, fmt.Errorf("Codex 线程管理器未初始化")
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
		"codex-link-clawbot 运行状态",
		"codex-link-clawbot：运行中",
		"版本：" + h.bridgeVersion,
		"已运行：" + formatUptime(time.Since(h.startedAt)),
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
		"协议：Codex 应用服务",
		"模型："+model,
		"Codex 工作目录："+info.Cwd,
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
