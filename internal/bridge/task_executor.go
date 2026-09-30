package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/access"
	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/delivery"
	"github.com/huixiangyang/codex-link-clawbot/internal/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/workspace"
)

// clientRegistry 属于微信适配层，调度器只读取 CanExecute 的结果。
type clientRegistry struct {
	clients  sync.Map
	contexts sync.Map
}

func (r *clientRegistry) register(ownerID string, client *ilink.Client) {
	if ownerID = strings.TrimSpace(ownerID); ownerID != "" && client != nil {
		r.clients.Store(ownerID, client)
	}
}

func (r *clientRegistry) load(ownerID string) (*ilink.Client, bool) {
	value, ok := r.clients.Load(ownerID)
	if !ok {
		return nil, false
	}
	return value.(*ilink.Client), true
}

// taskExecutor 组合 Codex 执行与微信交付，不持有 Handler 或调度器。
type taskExecutor struct {
	sendBusy       func(context.Context, *ilink.Client, ilink.WeixinMessage, request.Rejection) error
	targets        *target.Store
	codex          codex.Runtime
	progress       execution.ProgressConfig
	projects       *workspace.Manager
	sessions       *thread.Manager
	tasks          *request.Store
	pendingNotices *delivery.NoticeStore
	remoteLock     *access.RemoteLock
	clients        *clientRegistry
	sendReply      func(context.Context, *ilink.Client, ilink.WeixinMessage, request.Task, request.Result, string) deliveryReport
}

func (executor *taskExecutor) CanExecute(ownerID string) bool {
	_, available := executor.clients.load(ownerID)
	return available && (executor.remoteLock == nil || !executor.remoteLock.IsLocked(ownerID))
}

func (executor *taskExecutor) Execute(taskContext context.Context, task request.Task, finalize func() bool) {
	client, ok := executor.clients.load(task.OwnerID)
	if !ok {
		executor.failBeforeExecution(nil, taskContext, task, finalize, "微信发送客户端不可用。", request.ReasonProjectUnavailable, nil)
		return
	}

	requestPayload, err := executor.tasks.LoadRequest(task.OwnerID, task.ID)
	if err != nil {
		executor.failBeforeExecution(client, taskContext, task, finalize, "codex-link-clawbot 请求输入损坏，已停止执行。", request.ReasonPayloadInvalid, err)
		return
	}
	// 回执与附件下载属于本次执行，不占用微信接收循环。
	_ = SendTextReply(taskContext, client, task.OwnerID, taskAcknowledgement(task, false), requestPayload.ContextToken, NewClientID())
	if len(requestPayload.SourceData) > 0 {
		var incoming incomingAttachments
		if err = json.Unmarshal(requestPayload.SourceData, &incoming); err == nil {
			var text string
			var images, files []request.InputAttachment
			text, images, files, err = prepareRequestInput(taskContext, requestPayload.Text, incoming.Images, incoming.Files)
			if err == nil && taskContext.Err() == nil {
				err = executor.tasks.AttachInput(task.OwnerID, task.ID, text, images, files)
			}
		}
		if err != nil || taskContext.Err() != nil {
			executor.failBeforeExecution(client, taskContext, task, finalize, "附件接收未完成，请在最近结果查看详情并重新发送。", request.ReasonPayloadInvalid, err)
			return
		}
		requestPayload, err = executor.tasks.LoadRequest(task.OwnerID, task.ID)
		if err != nil {
			executor.failBeforeExecution(client, taskContext, task, finalize, "附件保存失败，请重新发送。", request.ReasonPayloadInvalid, err)
			return
		}
	}
	projectDefinition, ok := executor.projects.Get(task.ProjectID)
	if !ok {
		executor.failBeforeExecution(client, taskContext, task, finalize, "请求绑定的 Codex 工作空间已不存在，未改用其他目录。", request.ReasonProjectUnavailable, nil)
		return
	}
	outbox, err := executor.tasks.PrepareOutbox(task.OwnerID, task.ID)
	if err != nil {
		executor.failBeforeExecution(client, taskContext, task, finalize, "无法创建请求交付目录。", request.ReasonPayloadInvalid, err)
		return
	}
	turnRequest := taskChatRequest(requestPayload, outbox)
	turnRequest.WorkspaceRoot = projectDefinition.Root
	threadAgent, ok := executor.codex.(codex.ThreadClient)
	if !ok || executor.sessions == nil {
		executor.failBeforeExecution(client, taskContext, task, finalize, "Codex 线程运行时不可用。", request.ReasonSessionUnavailable, nil)
		return
	}
	intent, err := executor.targets.Resolve(task.OwnerID, task.TargetID)
	if err != nil || intent.WorkspaceID != task.ProjectID {
		executor.failBeforeExecution(client, taskContext, task, finalize, "请求的会话目标不可用。", request.ReasonSessionUnavailable, err)
		return
	}
	targetThreadID := task.ThreadID
	if targetThreadID == "" {
		targetThreadID = intent.ThreadID
	}
	thread, err := executor.sessions.OpenTaskThread(taskContext, task.OwnerID, thread.Workspace{
		ID: projectDefinition.ID, Name: projectDefinition.Name, Root: projectDefinition.Root,
	}, targetThreadID, threadAgent, suggestedSessionName(turnRequest))
	if err != nil {
		executor.failBeforeExecution(client, taskContext, task, finalize, "请求绑定的 Codex 线程不可用，未自动切换线程。", request.ReasonSessionUnavailable, err)
		return
	}
	if intent.ThreadID == "" {
		if err := executor.targets.Bind(task.OwnerID, task.TargetID, thread.ID); err != nil {
			executor.failBeforeExecution(client, taskContext, task, finalize, "无法保存当前会话。", request.ReasonSessionUnavailable, err)
			return
		}
	}
	if task.ThreadID == "" {
		if err := executor.tasks.AttachThread(task.OwnerID, task.ID, thread.ID); err != nil {
			executor.failBeforeExecution(client, taskContext, task, finalize, "无法固定新建 Codex 线程，请求已停止。", request.ReasonSessionUnavailable, err)
			return
		}
		task.ThreadID = thread.ID
	}

	reporter, err := execution.NewProgressReporter(taskContext, executor.progress, execution.ProgressCallbacks{
		Persist: func(stage string) {
			stage = presentation.Truncate(strings.Join(strings.Fields(stage), " "), 120)
			if updateErr := executor.tasks.UpdateStage(task.OwnerID, task.ID, stage); updateErr != nil && taskContext.Err() == nil {
				log.Printf("[execution] failed to update task stage: %v", updateErr)
			}
		},
		SendTyping: func(ctx context.Context) error {
			return SendTypingState(ctx, client, task.OwnerID, requestPayload.ContextToken)
		},
		SendPhase: func(ctx context.Context, stage string) error {
			return SendTextReply(ctx, client, task.OwnerID, "会话 "+thread.ID[max(0, len(thread.ID)-8):]+" · "+projectDefinition.Name+"\n"+stage, requestPayload.ContextToken, NewClientID())
		},
		OnError: func(operation string, reportErr error) {
			log.Printf("[progress] %s failed for %s: %v", operation, ilink.LogLabel(task.OwnerID), reportErr)
		},
	})
	if err != nil {
		executor.failBeforeExecution(client, taskContext, task, finalize, "长任务阶段呈现配置无效。", request.ReasonPayloadInvalid, err)
		return
	}
	defer reporter.Close()
	info := executor.codex.Info()
	log.Printf("[execution] dispatching task %s to Codex (%s, project=%s) for %s", shortTaskID(task.ID), info, task.ProjectID, ilink.LogLabel(task.OwnerID))
	started := time.Now()
	var reply string
	if progressCodex, supportsProgress := executor.codex.(codex.TurnProgressClient); supportsProgress {
		reply, err = progressCodex.ChatThreadWithProgress(taskContext, thread.ID, turnRequest, func(event codex.TurnPhaseEvent) {
			if event.TurnID != "" {
				_ = executor.tasks.AttachTurn(task.OwnerID, task.ID, event.TurnID)
			}
			reporter.Report(event)
		})
	} else {
		reply, err = threadAgent.ChatThread(taskContext, thread.ID, turnRequest)
	}
	reporter.Close()
	if usageProvider, ok := executor.codex.(codex.UsageProvider); ok {
		if usage, exists := usageProvider.Usage(thread.ID); exists {
			if usageErr := executor.tasks.AttachUsage(task.OwnerID, task.ID, usage.Last.InputTokens, usage.Last.OutputTokens, usage.Last.TotalTokens); usageErr != nil {
				log.Printf("[execution] failed to persist token usage: %v", usageErr)
			}
		}
	}
	cancelled := finalize()
	if err != nil {
		if cancelled && errors.Is(err, codex.ErrInterruptUnconfirmed) {
			noticeCtx, cancel := context.WithTimeout(context.WithoutCancel(taskContext), 10*time.Second)
			defer cancel()
			executor.failTask(noticeCtx, client, task, "打断尚未确认，会话可能仍在执行。回复 0，再选 4 刷新或再次打断。", request.ReasonInterruptUnconfirmed, err)
			return
		}
		if cancelled && (errors.Is(err, codex.ErrTurnInterrupted) || errors.Is(err, context.Canceled)) {
			_, _ = executor.tasks.Finish(task.OwnerID, task.ID, request.StateCancelled, request.ReasonUserCancelled)
			return
		}
		if taskContext.Err() != nil && !cancelled {
			return
		}
		executor.failTask(context.WithoutCancel(taskContext), client, task, "Codex 执行未完成。回复 0，再选 5 查看原因与恢复操作。", request.ReasonCodexFailed, err)
		return
	}
	log.Printf("[execution] Codex completed task %s (elapsed=%s chars=%d)", shortTaskID(task.ID), time.Since(started), len([]rune(reply)))
	if completeErr := executor.tasks.CompleteExecution(task.OwnerID, task.ID, reply); completeErr != nil {
		executor.failTask(context.WithoutCancel(taskContext), client, task, "Codex 已完成，但结果保存未完成。回复 0，再选 5 恢复保存，不要重新执行。", request.ReasonResultFreezeFailed, completeErr)
		return
	}
	artifacts, collectErr := collectArtifacts(outbox)
	if collectErr != nil {
		executor.failTask(taskContext, client, task, "Codex 已完成，文件归档失败。回复 0，再选 5 恢复保存。", request.ReasonResultFreezeFailed, collectErr)
		return
	}
	if len(artifacts.Skipped) > 0 {
		reply = appendArtifactSummary(reply, nil, artifacts.Skipped)
	}
	result, err := executor.tasks.FreezeResult(task.OwnerID, task.ID, request.FreezeResultInput{
		Reply: reply, ArtifactPaths: artifacts.Paths, ImageURLs: ExtractImageURLs(reply),
	})
	if err != nil {
		executor.failTask(taskContext, client, task, "Codex 已完成，结果保存失败。回复 0，再选 5 恢复保存。", request.ReasonResultFreezeFailed, err)
		return
	}
	if _, err := executor.tasks.BeginDelivery(task.OwnerID, task.ID); err != nil {
		executor.failTask(taskContext, client, task, "无法冻结请求发送状态。", request.ReasonDeliveryFailed, err)
		return
	}
	message := ilink.WeixinMessage{FromUserID: task.OwnerID, ContextToken: requestPayload.ContextToken}
	report := executor.sendReply(taskContext, client, message, task, result, NewClientID())
	attemptedAt := time.Now().Unix()
	if attemptedAt < result.FrozenAt {
		attemptedAt = result.FrozenAt
	}
	receipt := request.DeliveryReceipt{
		Outcome: report.Outcome, AttemptedAt: attemptedAt, MediaSent: report.MediaSent,
		TextSent: report.TextSent, FailureCode: report.Failure,
	}
	if err := executor.tasks.RecordDelivery(task.OwnerID, task.ID, receipt); err != nil {
		log.Printf("[execution] failed to persist delivery receipt for %s: %v", shortTaskID(task.ID), err)
		_, _ = executor.tasks.Finish(task.OwnerID, task.ID, request.StateInterrupted, request.ReasonDeliveryAmbiguous)
		executor.deferTaskRecoveryNotice(task, request.ReasonDeliveryAmbiguous)
		return
	}
	reason := ""
	if report.Outcome == request.DeliveryExplicitFailure {
		reason = request.ReasonDeliveryFailed
	}
	if report.Outcome == request.DeliveryAmbiguous {
		reason = request.ReasonDeliveryAmbiguous
	}
	if _, err := executor.tasks.Finish(task.OwnerID, task.ID, request.StateSucceeded, ""); err != nil {
		log.Printf("[execution] failed to finish task: %v", err)
	}
	if reason != "" {
		executor.deferTaskRecoveryNotice(task, reason)
	}

}

func taskChatRequest(payload request.LoadedRequest, outbox string) codex.ChatRequest {
	request := codex.ChatRequest{Text: payload.Text, ArtifactDir: outbox}
	for _, image := range payload.Images {
		request.LocalImages = append(request.LocalImages, image.AbsolutePath)
	}
	for _, file := range payload.Files {
		request.LocalFiles = append(request.LocalFiles, codex.LocalFile{
			Path: file.AbsolutePath, Name: file.Name, ContentType: file.ContentType, Size: file.Size,
		})
	}
	if len(request.LocalImages) > 0 && isImageAnnotationIntent(request.Text) {
		request.Text = strings.TrimSpace(request.Text + "\n\n[codex-link-clawbot 图片批注模式]\n请先理解图片和用户意图，再生成一张带有清晰、克制、移动端可读批注的 PNG。必须把最终图片写入本次 codex-link-clawbot 交付目录并回传；不得覆盖入站原图。")
	}
	return request
}

func (executor *taskExecutor) failBeforeExecution(client *ilink.Client, taskContext context.Context, task request.Task, finalize func() bool, notice, reason string, cause error) {
	if finalize() {
		if _, err := executor.tasks.Finish(task.OwnerID, task.ID, request.StateCancelled, request.ReasonUserCancelled); err != nil {
			log.Printf("[execution] failed to finish cancelled task: %v", err)
		}
		return
	}
	if taskContext.Err() != nil {
		return
	}
	executor.failTask(taskContext, client, task, notice, reason, cause)
}

func (executor *taskExecutor) failTask(taskContext context.Context, client *ilink.Client, task request.Task, notice, reason string, cause error) {
	if errors.Is(cause, codex.ErrThreadBusy) {
		reason = "session_busy"
	}
	if cause != nil {
		log.Printf("[execution] task %s failed (%s): %v", shortTaskID(task.ID), reason, cause)
	}
	_, finishErr := executor.tasks.Finish(task.OwnerID, task.ID, request.StateFailed, reason)
	if finishErr != nil {
		log.Printf("[execution] failed to persist failed task %s: %v", shortTaskID(task.ID), finishErr)
	}
	payload, loadErr := executor.tasks.LoadRequest(task.OwnerID, task.ID)
	if loadErr == nil && client != nil {
		if errors.Is(cause, codex.ErrThreadBusy) && executor.sendBusy != nil {
			message := ilink.WeixinMessage{FromUserID: task.OwnerID, ContextToken: payload.ContextToken}
			if err := executor.sendBusy(taskContext, client, message, request.Rejection{OwnerID: task.OwnerID, TargetID: task.TargetID, ThreadID: task.ThreadID}); err != nil {
				log.Printf("[execution] failed to show session menu: %v", err)
			}
			return
		}
		if sendErr := SendTextReply(taskContext, client, task.OwnerID, notice, payload.ContextToken, NewClientID()); sendErr != nil {
			log.Printf("[execution] failed to send task failure notice: %v", sendErr)
			if !outboundMayBeVisible(sendErr) {
				executor.deferTaskRecoveryNotice(task, reason)
			}
		}
	} else {
		executor.deferTaskRecoveryNotice(task, reason)
	}
}

func (executor *taskExecutor) deferTaskRecoveryNotice(task request.Task, reason string) {
	if executor.pendingNotices == nil {
		return
	}
	title := "codex-link-clawbot 请求需要处理"
	action := "回复 0，再选 5 查看详情和恢复操作。"
	switch reason {
	case request.ReasonDeliveryFailed:
		title = "Codex 结果待取回"
		action = "Codex 已完成，但微信明确拒绝了本次交付。回复 0，再选 5 查看状态。"
	case request.ReasonDeliveryAmbiguous:
		title = "Codex 结果发送状态待确认"
		action = "Codex 已完成，但微信发送结果不确定。请先检查是否已收到，再回复 0，选择 5 处理。"
	case request.ReasonCodexFailed:
		title = "Codex 执行失败"
		action = "请求输入暂时保留，回复 0，再选 5 查看状态。"
	}
	_, _, err := executor.pendingNotices.Enqueue(task.OwnerID, delivery.NoticeInput{
		Kind: delivery.NoticeTaskRecovery, DedupKey: "task-recovery:" + task.ID + ":" + reason,
		ReferenceID: task.ID, Title: title, Body: "请求：" + task.Summary + "\n" + action, TTL: 24 * time.Hour,
	})
	if err != nil {
		log.Printf("[execution] defer task recovery notice for %s failed: %v", shortTaskID(task.ID), err)
	}
}

func shortTaskID(taskID string) string {
	const length = 6
	if len(taskID) <= length {
		return taskID
	}
	return taskID[len(taskID)-length:]
}
