package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/huixiangyang/codex-link-clawbot/internal/access"
	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/delivery"
	"github.com/huixiangyang/codex-link-clawbot/internal/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/runtimecontrol"
	"github.com/huixiangyang/codex-link-clawbot/internal/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/workspace"
)

// Handler processes incoming WeChat messages and dispatches replies.
type Handler struct {
	noticeSending       atomic.Bool
	sessionClient       codex.ThreadClient
	targets             *target.Store
	projects            *workspace.Manager
	sessions            *thread.Manager
	visual              VisualRenderer
	preferences         *preference.Store
	visualReplyEnabled  bool
	visualReplyMinRunes int
	tasks               *request.Store
	coordinator         *execution.Coordinator
	clients             *clientRegistry
	lifecycle           Lifecycle
	pendingNotices      *delivery.NoticeStore
	remoteLock          *access.RemoteLock
	voice               *VoiceBriefing
	managementURL       string
	menus               *numberMenus
}

type Lifecycle interface {
	BeginIngress()
	EndIngress()
	Snapshot() runtimecontrol.Snapshot
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
		h.clients.register(msg.FromUserID, client)
		h.clients.updateContext(msg.FromUserID, msg.ContextToken)

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
		notice := "暂不支持这类消息，请发送文字、图片或文件。回复 0 打开菜单。"
		for _, item := range msg.ItemList {
			if item.Type == ilink.ItemTypeVoice {
				notice = "未取得语音文字，请重新发送文字或使用微信转文字。回复 0 打开菜单。"
				break
			}
		}
		return h.sendBridgeNotice(ctx, client, msg.FromUserID, notice, msg.ContextToken, NewClientID())
	}

	if len(images) > 0 || len(files) > 0 {
		log.Printf("[handler] received from %s (chars=%d images=%d files=%d)", userLabel, len([]rune(text)), len(images), len(files))
	} else {
		log.Printf("[handler] received from %s (chars=%d)", userLabel, len([]rune(text)))
	}

	clientID := NewClientID()
	if h.remoteLock == nil || !h.remoteLock.IsLocked(msg.FromUserID) {
		if h.coordinator != nil {
			h.coordinator.Effect(func(life context.Context) { h.flushPendingNotices(life, client, msg.FromUserID, msg.ContextToken) })
		} else {
			h.flushPendingNotices(ctx, client, msg.FromUserID, msg.ContextToken)
		}
	}
	if h.tasks != nil {
		if source, err := sourceMessageKey(client, msg); err == nil {
			if h.tasks.WasCleared(source) {
				return h.sendBridgeNotice(ctx, client, msg.FromUserID, request.ErrCleared.Error(), msg.ContextToken, clientID)
			}
			if existing, found := h.tasks.FindBySource(source); found {
				return h.sendBridgeNotice(ctx, client, msg.FromUserID, taskAcknowledgement(existing, true), msg.ContextToken, clientID)
			}
			if rejection, found := h.tasks.FindRejection(source); found {
				return h.showRejected(ctx, client, msg, rejection)
			}
		}
	}
	if len(images) == 0 && len(files) == 0 && h.menus != nil {
		if handled, err := h.menus.handle(ctx, client, msg, strings.TrimSpace(text)); handled {
			return err
		}
	}
	if h.remoteLock != nil && h.remoteLock.IsLocked(msg.FromUserID) {
		if err := SendTextReply(ctx, client, msg.FromUserID, "入口已锁定。回复 0 打开菜单并解锁。", msg.ContextToken, clientID); err != nil {
			log.Printf("[security] failed to send locked-state reply to %s: %v", userLabel, err)
		}
		return nil
	}
	if h.menus != nil {
		h.menus.leave(msg.FromUserID)
	}

	return h.startCodexTask(ctx, client, msg, text, images, files, clientID)
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

// startCodexTask 按会话即时准入，忙碌输入只保存拒绝回执，不保存正文或附件。
func (h *Handler) startCodexTask(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text string, images []*ilink.ImageItem, files []*ilink.FileItem, clientID string) error {
	if h.tasks == nil || h.coordinator == nil || h.targets == nil || h.projects == nil || h.sessions == nil || h.preferences == nil {
		return fmt.Errorf("execution service is not initialized")
	}
	sourceKey, err := sourceMessageKey(client, msg)
	if err != nil {
		return h.sendBridgeNotice(ctx, client, msg.FromUserID, "提交失败：消息缺少来源编号，请重新发送。", msg.ContextToken, clientID)
	}
	if existing, exists := h.tasks.FindBySource(sourceKey); exists {
		return h.sendBridgeNotice(ctx, client, msg.FromUserID, taskAcknowledgement(existing, true), msg.ContextToken, clientID)
	}
	if h.lifecycle != nil {
		h.lifecycle.BeginIngress()
		defer h.lifecycle.EndIngress()
	}
	// 下载前固定目标并占用会话，慢下载期间的第二条消息也不能进入等待状态。
	intent, err := h.targets.Capture(msg.FromUserID)
	if err != nil {
		return err
	}
	admission, err := h.coordinator.Begin(msg.FromUserID, intent.ID, intent.ThreadID, sourceKey)
	if errors.Is(err, execution.ErrDuplicateSource) {
		return h.sendBridgeNotice(ctx, client, msg.FromUserID, err.Error(), msg.ContextToken, clientID)
	}
	if errors.Is(err, request.ErrSessionBusy) {
		return h.rejectBusy(ctx, client, msg, sourceKey, intent.ID, intent.ThreadID)
	}
	if err != nil {
		return h.sendBridgeNotice(ctx, client, msg.FromUserID, "提交失败："+err.Error(), msg.ContextToken, clientID)
	}
	defer admission.Release()
	activity, err := h.sessionActivity(ctx, msg.FromUserID, intent.ID, intent.ThreadID)
	if err != nil {
		return h.sendBridgeNotice(ctx, client, msg.FromUserID, "提交失败：无法确认会话状态，请稍后重新发送。", msg.ContextToken, clientID)
	}
	if activity.Busy {
		return h.rejectBusy(ctx, client, msg, sourceKey, intent.ID, intent.ThreadID)
	}
	currentProject, exists := h.projects.Get(intent.WorkspaceID)
	if !exists {
		return fmt.Errorf("目标工作空间已不可用")
	}
	preferences := h.preferences.Get(msg.FromUserID)
	if preferences.ResponseMode == presentation.ResponseReading && h.visual == nil || preferences.ResponseMode == presentation.ResponseVoice && h.voice == nil {
		return h.sendBridgeNotice(ctx, client, msg.FromUserID, "所选回复模式当前不可用，请更改回复设置。", msg.ContextToken, clientID)
	}
	var sourceData json.RawMessage
	if len(images) > 0 || len(files) > 0 {
		if len(images) > maxInboundImages || len(files) > maxInboundFiles {
			return h.sendBridgeNotice(ctx, client, msg.FromUserID, "附件数量过多，请减少后重新发送。", msg.ContextToken, clientID)
		}
		sourceData, err = json.Marshal(incomingAttachments{Images: images, Files: files})
		if err != nil {
			return err
		}
		if strings.TrimSpace(text) == "" {
			text = defaultFilePrompt
			if len(files) == 0 {
				text = defaultImagePrompt
			}
		}
	}
	task, existed, err := h.tasks.Start(request.StartInput{
		TargetID: intent.ID, SourceMessageKey: sourceKey, OwnerID: msg.FromUserID,
		ProjectID: currentProject.ID, ThreadID: intent.ThreadID,
		Summary: taskActivitySummary(text, len(images), len(files)), Text: text,
		ContextToken: msg.ContextToken, ResponseMode: preferences.ResponseMode, VisualStyle: preferences.Style,
		SourceData: sourceData,
	})
	if errors.Is(err, request.ErrSessionBusy) {
		return h.rejectBusy(ctx, client, msg, sourceKey, intent.ID, intent.ThreadID)
	}
	if err != nil {
		if errors.Is(err, request.ErrCapacity) || errors.Is(err, request.ErrRejected) {
			return h.sendBridgeNotice(ctx, client, msg.FromUserID, "提交失败："+err.Error(), msg.ContextToken, clientID)
		}
		return fmt.Errorf("persist WeChat execution: %w", err)
	}
	// 即便微信接收回执丢失，也只执行这一次；重复来源只展示执行记录。
	if !existed {
		admission.Launch(task)
	}
	if existed {
		return h.sendBridgeNotice(ctx, client, msg.FromUserID, taskAcknowledgement(task, true), msg.ContextToken, clientID)
	}
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

func taskAcknowledgement(task request.Task, existed bool) string {
	status := "已开始处理"
	if existed {
		status = "这条消息已处理，不会重复执行"
	}
	return status + " · " + shortTaskID(task.ID) + "\n" + task.ProjectID + " · " + taskTargetLabel(task) + "\n" + task.Summary + "\n状态：" + task.Stage + "\n回复 0 管理会话。"
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

// sendCompletedTask 默认只交付适合小屏的完成说明；完整结果和文件按需取回。
func (h *Handler) sendCompletedTask(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, task request.Task, result request.Result, clientID string) deliveryReport {
	if task.ResponseMode == presentation.ResponseAdaptive {
		brief := len([]rune(result.Reply)) > 500 || len(result.Artifacts) > 0 || len(result.ImageURLs) > 0
		reply := MarkdownToPlainText(result.Reply)
		if brief {
			reply = presentation.Truncate(reply, 240)
		}
		heading := "结果"
		if brief {
			heading = "已完成"
		}
		body := heading + " · " + shortTaskID(task.ID) + " · " + task.ProjectID + "\n\n" + reply
		if len(result.Artifacts) > 0 {
			body += fmt.Sprintf("\n已保存 %d 个文件。", len(result.Artifacts))
		}
		if brief {
			body += "\n回复 0，再选 5，按数字阅读全文或取回文件。"
		}
		err := SendTextReply(ctx, client, msg.FromUserID, body, msg.ContextToken, clientID)
		if err != nil {
			outcome := request.DeliveryExplicitFailure
			reason := request.ReasonDeliveryFailed
			if outboundMayBeVisible(err) {
				outcome = request.DeliveryAmbiguous
				reason = request.ReasonDeliveryAmbiguous
			}
			return deliveryReport{Outcome: outcome, Failure: reason}
		}
		return deliveryReport{Outcome: request.DeliverySucceeded, TextSent: true}
	}
	return h.sendReplyWithMediaForTask(ctx, client, msg, task, result, clientID)
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
	// 不同会话可独立完成，结果回执始终标明来源，避免误认为当前会话的回答。
	reply := "结果 · " + shortTaskID(task.ID) + " · " + projectName + "\n\n" + result.Reply
	return h.deliverReplyPlan(ctx, client, msg, reply, paths, nil, result.ImageURLs, clientID,
		task.ResponseMode, task.VisualStyle, projectName)
}

type deliveryReport struct {
	Outcome   request.DeliveryOutcome
	MediaSent int
	TextSent  bool
	Failure   string
}

func (h *Handler) deliverReplyPlan(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, reply string, artifactPaths, initialFailures, imageURLs []string, clientID string, mode presentation.ResponseMode, style presentation.Style, projectName string) deliveryReport {
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

	}

	reply = appendArtifactSummary(reply, sentPaths, failed)

	delivered := false
	var deliveryErr error
	switch mode {
	case presentation.ResponseVoice:
		voiceDelivered, voiceErr := h.sendVoiceCodexReplySnapshot(ctx, client, msg.FromUserID, reply, msg.ContextToken)
		delivered = voiceDelivered
		if voiceDelivered {
			// 语音批次只有一个 MP3。
			report.MediaSent++
		}
		if voiceErr != nil {
			log.Printf("[voice] failed to send Codex voice response to %s: %v", ilink.LogLabel(msg.FromUserID), voiceErr)
			if delivered {
				deliveryErr = voiceErr
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
	return h.currentWorkspace(userID).ID
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

func (h *Handler) currentWorkspace(ownerID string) workspace.Definition {
	if h.projects == nil {
		return workspace.Definition{}
	}
	if h.targets == nil {
		return h.projects.List()[0]
	}
	definition, _ := h.projects.Get(h.targets.Current(ownerID).WorkspaceID)
	return definition
}

func taskTargetLabel(task request.Task) string {
	if task.ThreadID != "" {
		return thread.ShortCode(task.ThreadID)
	}
	return "当前工作空间的新会话"
}
