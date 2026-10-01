package wechat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
)

// startCodexTask 按会话即时准入，忙碌输入只保存拒绝回执，不保存正文或附件。
func (h *Handler) startCodexTask(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text string, images []*ilink.ImageItem, files []*ilink.FileItem, clientID, expectedTarget string) error {
	if h.tasks == nil || h.coordinator == nil || h.targets == nil || h.projects == nil || h.sessions == nil || h.preferences == nil {
		return fmt.Errorf("execution service is not initialized")
	}
	sourceKey, err := sourceMessageKey(client, msg)
	if err != nil {
		return h.sendBridgeNotice(ctx, client, msg.FromUserID, "提交失败：消息缺少来源编号，请重新发送。", msg.ContextToken, clientID)
	}
	if existing, exists := h.tasks.FindBySource(sourceKey); exists {
		return h.sendBridgeNotice(ctx, client, msg.FromUserID, taskAcknowledgement(existing), msg.ContextToken, clientID)
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
	if expectedTarget != "" && intent.ID != expectedTarget {
		return h.sendBridgeNotice(ctx, client, msg.FromUserID, "草稿未提交：当前会话已变化，请切回草稿原会话。", msg.ContextToken, clientID)
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
		return h.showUnavailable(ctx, client, msg, intent)
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
			return h.sendBridgeNotice(ctx, client, msg.FromUserID, "附件未提交，请先补充处理要求。", msg.ContextToken, clientID)
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
	// 持久化成功才启动；重复来源只展示执行记录，不产生第二次执行。
	if !existed {
		admission.Launch(task)
	}
	if existed {
		return h.sendBridgeNotice(ctx, client, msg.FromUserID, taskAcknowledgement(task), msg.ContextToken, clientID)
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

func taskAcknowledgement(task request.Task) string {
	return "这条消息已处理，不会重复执行 · " + shortTaskID(task.ID) + "\n" + task.ProjectID + " · " + taskTargetLabel(task) + "\n" + task.Summary + "\n状态：" + task.Stage + "\n回复 # 管理会话。"
}

func taskActivitySummary(text string, imageCount, fileCount int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	text = presentation.NormalizeLine(text, 72)
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
	return presentation.NormalizeLine(text, 120)
}
