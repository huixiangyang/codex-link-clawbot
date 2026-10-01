package wechat

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
)

// sendCompletedTask 按提交时的回复偏好发送结果，文件仍可单独取回。
func (h *Handler) sendCompletedTask(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, task request.Task, result request.Result, clientID string) deliveryReport {
	if task.ResponseMode == presentation.ResponseAdaptive || task.ResponseMode == presentation.ResponseText {
		brief := task.ResponseMode == presentation.ResponseAdaptive && (len([]rune(result.Reply)) > 500 || len(result.Artifacts) > 0 || len(result.ImageURLs) > 0)
		reply := MarkdownToPlainText(result.Reply)
		excerpted := brief && len([]rune(reply)) > 240
		if brief {
			reply = presentation.Truncate(reply, 240)
		}
		body := h.resultContext(task, reply)
		if len(result.Artifacts) > 0 {
			body += fmt.Sprintf("\n已保存 %d 个文件。", len(result.Artifacts))
		}
		body += resultShortcuts(task, result, excerpted)
		return sendResultText(ctx, client, msg.FromUserID, body, msg.ContextToken)
	}
	return h.sendReplyWithMediaForTask(ctx, client, msg, task, result, clientID)
}

func (h *Handler) sendReplyWithMediaForTask(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, task request.Task, result request.Result, clientID string) deliveryReport {
	paths := make([]string, 0, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		paths = append(paths, filepath.Join(h.tasks.Root(), task.ID, artifact.Path))
	}
	// 切换会话后到达的结果保留来源，当前会话只显示回答正文。
	reply := h.resultContext(task, result.Reply) + resultShortcuts(task, result, false)
	return h.deliverReplyPlan(ctx, client, msg, reply, paths, result.ImageURLs, clientID,
		task.ResponseMode, task.VisualStyle)
}

type deliveryReport struct {
	Outcome   request.DeliveryOutcome
	MediaSent int
	TextSent  bool
	Failure   string
}

func (h *Handler) deliverReplyPlan(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, reply string, artifactPaths, imageURLs []string, clientID string, mode presentation.ResponseMode, style presentation.Style) deliveryReport {
	report := deliveryReport{Outcome: request.DeliverySucceeded}
	var sentPaths []string
	var failed []string
	failedDelivery := false
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

	reply = request.AppendArtifactSummary(reply, sentPaths, failed)

	delivered := false
	var deliveryErr error
	switch mode {
	case presentation.ResponseText:
		textReport := sendResultText(ctx, client, msg.FromUserID, MarkdownToPlainText(reply), msg.ContextToken)
		report.TextSent = textReport.TextSent
		delivered = true
		if textReport.Outcome != request.DeliverySucceeded {
			failedDelivery = true
			mayBeVisible = mayBeVisible || textReport.Outcome == request.DeliveryAmbiguous
		}
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

func (h *Handler) sendResultFiles(ctx context.Context, client *ilink.Client, owner, token string, task request.Task, result request.Result) deliveryReport {
	report := deliveryReport{Outcome: request.DeliverySucceeded}
	if err := SendTextReply(ctx, client, owner, h.resultHeading(task)+"\n正在取回文件。", token, NewClientID()); err != nil {
		report.Outcome, report.Failure = request.DeliveryExplicitFailure, request.ReasonDeliveryFailed
		if outboundMayBeVisible(err) {
			report.Outcome, report.Failure = request.DeliveryAmbiguous, request.ReasonDeliveryAmbiguous
		}
		return report
	}
	report.TextSent = true
	send := func(err error) {
		if err != nil {
			report.Outcome, report.Failure = request.DeliveryAmbiguous, request.ReasonDeliveryAmbiguous
		} else {
			report.MediaSent++
		}
	}
	for _, artifact := range result.Artifacts {
		send(SendMediaFromPath(ctx, client, owner, filepath.Join(h.tasks.Root(), task.ID, artifact.Path), token))
	}
	for _, url := range result.ImageURLs {
		send(SendMediaFromURL(ctx, client, owner, url, token))
	}
	return report
}
