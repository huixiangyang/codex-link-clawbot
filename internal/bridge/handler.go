package bridge

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/access"
	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/delivery"
	"github.com/huixiangyang/codex-link-clawbot/internal/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/workspace"
)

// Handler processes incoming WeChat messages and dispatches replies.
type Handler struct {
	codex               codex.Runtime
	progress            execution.ProgressConfig
	projects            *workspace.Manager
	sessions            *thread.Manager
	visual              VisualRenderer
	preferences         *preference.Store
	visualReplyEnabled  bool
	visualReplyMinRunes int
	tasks               *request.Store
	coordinator         *Coordinator
	lifecycle           Lifecycle
	deliveries          *delivery.Store
	pendingNotices      *delivery.NoticeStore
	remoteLock          *access.RemoteLock
	voice               *VoiceBriefing
	managementURL       string
}

type Lifecycle interface {
	BeginIngress()
	EndIngress()
}

type Dependencies struct {
	Codex               codex.Runtime
	Workspaces          *workspace.Manager
	Threads             *thread.Manager
	Visual              VisualRenderer
	Preferences         *preference.Store
	Requests            *request.Store
	Lifecycle           Lifecycle
	Deliveries          *delivery.Store
	PendingNotices      *delivery.NoticeStore
	RemoteLock          *access.RemoteLock
	Voice               *VoiceBriefing
	Progress            execution.ProgressConfig
	VisualReplyEnabled  bool
	VisualReplyMinRunes int
	ManagementURL       string
}

type Runtime struct {
	Handler     *Handler
	Coordinator *Coordinator
}

// NewRuntime 原子构造消息入口和唯一串行执行器，不允许运行期补注入依赖。
func NewRuntime(dependencies Dependencies) (*Runtime, error) {
	if dependencies.Codex == nil || dependencies.Workspaces == nil || dependencies.Threads == nil || dependencies.Preferences == nil ||
		dependencies.Requests == nil || dependencies.Lifecycle == nil || dependencies.Deliveries == nil ||
		dependencies.PendingNotices == nil || dependencies.RemoteLock == nil {
		return nil, fmt.Errorf("bridge dependencies are incomplete")
	}
	if err := dependencies.Progress.Validate(); err != nil {
		return nil, err
	}
	minimumRunes := dependencies.VisualReplyMinRunes
	if minimumRunes <= 0 {
		minimumRunes = 900
	}
	handler := &Handler{
		codex:    dependencies.Codex,
		progress: dependencies.Progress, projects: dependencies.Workspaces, sessions: dependencies.Threads,
		visual: dependencies.Visual, preferences: dependencies.Preferences, tasks: dependencies.Requests,
		lifecycle: dependencies.Lifecycle, deliveries: dependencies.Deliveries, pendingNotices: dependencies.PendingNotices,
		remoteLock: dependencies.RemoteLock, voice: dependencies.Voice, managementURL: strings.TrimRight(strings.TrimSpace(dependencies.ManagementURL), "/"),
		visualReplyEnabled: dependencies.VisualReplyEnabled, visualReplyMinRunes: minimumRunes,
	}
	coordinator, err := newCoordinator(handler, dependencies.Requests)
	if err != nil {
		return nil, err
	}
	handler.coordinator = coordinator
	return &Runtime{Handler: handler, Coordinator: coordinator}, nil
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
	if h.remoteLock != nil && h.remoteLock.IsLocked(msg.FromUserID) {
		if err := SendTextReply(ctx, client, msg.FromUserID, h.codexLinkMenu(msg.FromUserID, true), msg.ContextToken, clientID); err != nil {
			log.Printf("[security] failed to send locked-state reply to %s: %v", userLabel, err)
		}
		return nil
	}
	h.flushPendingNotices(ctx, client, msg.FromUserID, msg.ContextToken)
	if len(images) == 0 && len(files) == 0 && isCodexLinkMenu(trimmed) {
		if err := SendTextReply(ctx, client, msg.FromUserID, h.codexLinkMenu(msg.FromUserID, false), msg.ContextToken, clientID); err != nil {
			return fmt.Errorf("send Codex Link menu: %w", err)
		}
		return nil
	}

	return h.enqueueCodexTask(ctx, client, msg, text, images, files, clientID)
}

func isCodexLinkMenu(text string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(text)), " "))
	switch normalized {
	case "菜单", "codex", "codex 菜单", "codex菜单", "codex link", "codex-link":
		return true
	default:
		return false
	}
}

// codexLinkMenu 是微信中唯一的管理入口；其余消息全部作为 Codex 请求处理。
func (h *Handler) codexLinkMenu(ownerID string, locked bool) string {
	workspaceName := "未选择"
	threadLabel := "首次发送消息时自动创建"
	if h.projects != nil {
		current := h.projects.Current(ownerID)
		workspaceName = current.Name
		if h.sessions != nil {
			if threadID := h.sessions.SnapshotThreadID(ownerID, current.ID); threadID != "" {
				threadLabel = thread.ShortCode(threadID)
			}
		}
	}
	status := "已连接"
	if locked {
		status = "已锁定"
	}
	managementURL := h.managementURL
	if managementURL == "" {
		managementURL = "仅本机管理页面"
	}
	return strings.Join([]string{
		"Codex Link",
		"连接状态：" + status,
		"工作空间：" + workspaceName,
		"目标线程：" + threadLabel,
		"管理页面：" + managementURL,
		"",
		"直接发送文字、图片或文件即可交给 Codex。线程、队列、工作空间与安全设置请在管理页面处理。",
	}, "\n")
}

// enqueueCodexTask 只负责可靠入队。Codex 执行权始终由全局 Coordinator 持有。
func (h *Handler) enqueueCodexTask(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text string, images []*ilink.ImageItem, files []*ilink.FileItem, clientID string) error {
	if h.tasks == nil || h.coordinator == nil || h.projects == nil || h.sessions == nil || h.preferences == nil {
		return fmt.Errorf("task queue is not initialized")
	}
	sourceKey, err := sourceMessageKey(client, msg)
	if err != nil {
		if sendErr := h.sendBridgeNotice(ctx, client, msg.FromUserID, "这条微信消息没有稳定来源编号，无法安全入队。", msg.ContextToken, clientID); sendErr != nil {
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
		if err := h.sendBridgeNotice(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
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
		if sendErr := h.sendBridgeNotice(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); sendErr != nil {
			log.Printf("[queue] failed to send attachment rejection: %v", sendErr)
		}
		return nil
	}
	currentProject := h.projects.Current(msg.FromUserID)
	taskThreadID := h.sessions.SnapshotThreadID(msg.FromUserID, currentProject.ID)
	preferences := h.preferences.Get(msg.FromUserID)
	task, existed, err := h.tasks.Enqueue(request.EnqueueInput{
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
	if err := h.sendBridgeNotice(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
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

func queuedTaskAcknowledgement(store *request.Store, task request.Task, projectName string, existed bool) string {
	state := "已可靠加入队列"
	if existed {
		state = "这条消息已经入队，不会重复执行"
	}
	positionText := "等待协调器领取"
	if position, ok := store.QueuePosition(task.OwnerID, task.ID); ok {
		positionText = fmt.Sprintf("当前排位：%d", position)
	} else if task.State == request.StateRunning || task.State == request.StateDelivering {
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
	text = presentation.SanitizeActivity(text)
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

func normalizeSessionLine(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit == 1 {
		return "…"
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

// sendReplyWithMedia 发送最终文字、远程图片和本次 turn 的专属交付物。
func (h *Handler) sendReplyWithMedia(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, reply, artifactDir, clientID string) deliveryReport {
	artifacts, collectErr := collectArtifacts(artifactDir)
	failed := append([]string(nil), artifacts.Skipped...)
	if collectErr != nil {
		failed = append(failed, collectErr.Error())
	}
	return h.deliverReplyPlan(ctx, client, msg, reply, artifacts.Paths, failed, ExtractImageURLs(reply), clientID,
		delivery.Source{ProjectID: h.currentProjectID(msg.FromUserID)}, h.currentResponseMode(msg.FromUserID), h.currentVisualStyle(msg.FromUserID), "")
}

func (h *Handler) sendReplyWithMediaForTask(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, task request.Task, result request.Result, clientID string) deliveryReport {
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
		delivery.Source{ProjectID: task.ProjectID, ThreadID: task.ThreadID, TaskID: task.ID}, task.ResponseMode, task.VisualStyle, projectName)
}

type deliveryReport struct {
	Outcome   request.DeliveryOutcome
	MediaSent int
	TextSent  bool
	Failure   string
}

func (h *Handler) deliverReplyPlan(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, reply string, artifactPaths, initialFailures, imageURLs []string, clientID string, source delivery.Source, mode presentation.ResponseMode, style presentation.Style, projectName string) deliveryReport {
	report := deliveryReport{Outcome: request.DeliverySucceeded}
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
		if h.deliveries != nil && delivery.ValidSource(source) {
			if _, recordErr := h.deliveries.RecordDelivery(msg.FromUserID, source, attachmentPath); recordErr != nil {
				log.Printf("[delivery-store] failed to archive delivery: %v", recordErr)
			}
		}
	}

	reply = appendArtifactSummary(reply, sentPaths, failed)

	delivered := false
	var deliveryErr error
	switch mode {
	case presentation.ResponseVoice:
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
	case presentation.ResponseReading:
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
			report.Outcome = request.DeliveryAmbiguous
			report.Failure = request.ReasonDeliveryAmbiguous
		} else {
			report.Outcome = request.DeliveryExplicitFailure
			report.Failure = request.ReasonDeliveryFailed
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
		return presentation.Truncate(text, 36)
	}
	if len(request.LocalImages) > 0 {
		return "图片分析"
	}
	if len(request.LocalFiles) > 0 {
		name := strings.TrimSpace(request.LocalFiles[0].Name)
		if name != "" {
			return presentation.Truncate("文件分析 · "+name, 36)
		}
		return "文件分析"
	}
	return ""
}

func isImageAnnotationIntent(text string) bool {
	normalized := normalizeMessageIntent(text)
	for _, marker := range []string{"批注图片", "标注图片", "批注这张图", "标注这张图", "在图上标注"} {
		if strings.Contains(normalized, normalizeMessageIntent(marker)) {
			return true
		}
	}
	return false
}

func normalizeMessageIntent(text string) string {
	replacer := strings.NewReplacer(" ", "", "\t", "", "\n", "", "，", "", "。", "", "！", "", "？", "", ",", "", ".", "", "!", "", "?", "")
	return replacer.Replace(strings.ToLower(strings.TrimSpace(text)))
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
