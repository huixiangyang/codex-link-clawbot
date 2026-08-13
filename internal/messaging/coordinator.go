package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/taskqueue"
)

type coordinatorTask struct {
	ownerID         string
	taskID          string
	cancel          context.CancelFunc
	cancelRequested bool
	finalizing      bool
	delivering      bool
}

// Coordinator 是进程内唯一的 Codex 执行权持有者，所有项目共享同一串行循环。
type Coordinator struct {
	handler *Handler
	tasks   *taskqueue.Store
	wake    chan struct{}
	clients sync.Map // map[ownerID]*ilink.Client

	mu       sync.Mutex
	active   *coordinatorTask
	draining bool
	// runtimeGate 保证 Codex cwd、thread 变更与 turn 执行不会跨绑定者并发。
	runtimeGate sync.Mutex
}

func NewCoordinator(handler *Handler, tasks *taskqueue.Store) (*Coordinator, error) {
	if handler == nil || tasks == nil {
		return nil, fmt.Errorf("handler and task store are required")
	}
	return &Coordinator{handler: handler, tasks: tasks, wake: make(chan struct{}, 1)}, nil
}

func (coordinator *Coordinator) RegisterClient(client *ilink.Client) {
	if client == nil || strings.TrimSpace(client.OwnerUserID()) == "" {
		return
	}
	coordinator.RegisterOwnerClient(client.OwnerUserID(), client)
}

func (coordinator *Coordinator) RegisterOwnerClient(ownerID string, client *ilink.Client) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" || client == nil {
		return
	}
	coordinator.clients.Store(ownerID, client)
	coordinator.Wake()
}

func (coordinator *Coordinator) Wake() {
	select {
	case coordinator.wake <- struct{}{}:
	default:
	}
}

func (coordinator *Coordinator) Run(ctx context.Context) error {
	cleanupTicker := time.NewTicker(time.Minute)
	defer cleanupTicker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if coordinator.isDraining() {
			if !coordinator.wait(ctx, cleanupTicker.C) {
				return ctx.Err()
			}
			continue
		}
		if !coordinator.runtimeGate.TryLock() {
			if !coordinator.wait(ctx, cleanupTicker.C) {
				return ctx.Err()
			}
			continue
		}
		blocked := coordinator.blockedOwners()
		task, claimed, err := coordinator.tasks.ClaimNext(blocked)
		if err != nil {
			coordinator.runtimeGate.Unlock()
			return fmt.Errorf("claim queued task: %w", err)
		}
		if !claimed {
			coordinator.runtimeGate.Unlock()
			if !coordinator.wait(ctx, cleanupTicker.C) {
				return ctx.Err()
			}
			continue
		}
		taskContext, cancel := context.WithCancel(ctx)
		active := &coordinatorTask{ownerID: task.OwnerID, taskID: task.ID, cancel: cancel}
		coordinator.mu.Lock()
		coordinator.active = active
		coordinator.mu.Unlock()
		coordinator.execute(taskContext, task, active)
		coordinator.runtimeGate.Unlock()
	}
}

// TryRuntimeControl 只在没有 Codex turn 占用运行时时执行短会话变更。
func (coordinator *Coordinator) TryRuntimeControl(action func()) bool {
	if action == nil || !coordinator.runtimeGate.TryLock() {
		return false
	}
	defer coordinator.runtimeGate.Unlock()
	action()
	coordinator.Wake()
	return true
}

func (coordinator *Coordinator) wait(ctx context.Context, cleanup <-chan time.Time) bool {
	select {
	case <-ctx.Done():
		return false
	case <-coordinator.wake:
		return true
	case <-cleanup:
		if err := coordinator.tasks.CleanupExpired(); err != nil {
			log.Printf("[queue] failed to clean expired payloads: %v", err)
		}
		return true
	}
}

func (coordinator *Coordinator) blockedOwners() map[string]bool {
	blocked := make(map[string]bool)
	for _, ownerID := range coordinator.tasks.Owners() {
		if _, exists := coordinator.clients.Load(ownerID); !exists {
			blocked[ownerID] = true
			continue
		}
		if coordinator.handler.remoteLock != nil && coordinator.handler.remoteLock.IsLocked(ownerID) {
			blocked[ownerID] = true
		}
	}
	return blocked
}

func (coordinator *Coordinator) execute(taskContext context.Context, task taskqueue.Task, active *coordinatorTask) {
	value, ok := coordinator.clients.Load(task.OwnerID)
	if !ok {
		_, _ = coordinator.tasks.Finish(task.OwnerID, task.ID, taskqueue.StateInterrupted, taskqueue.ReasonProjectUnavailable)
		return
	}
	client := value.(*ilink.Client)
	defer func() {
		active.cancel()
		coordinator.mu.Lock()
		if coordinator.active == active {
			coordinator.active = nil
		}
		coordinator.mu.Unlock()
	}()

	requestPayload, err := coordinator.tasks.LoadRequest(task.OwnerID, task.ID)
	if err != nil {
		coordinator.failBeforeExecution(client, task, active, "codex-link-clawbot 请求输入损坏，已停止执行。", taskqueue.ReasonPayloadInvalid, err)
		return
	}
	projectDefinition, ok := coordinator.handler.projects.Get(task.ProjectID)
	if !ok {
		coordinator.failBeforeExecution(client, task, active, "请求绑定的 Codex 工作空间已不存在，未改用其他目录。", taskqueue.ReasonProjectUnavailable, nil)
		return
	}
	coordinator.handler.codex.SetCwd(projectDefinition.Root)
	outbox, err := coordinator.tasks.PrepareOutbox(task.OwnerID, task.ID)
	if err != nil {
		coordinator.failBeforeExecution(client, task, active, "无法创建请求交付目录。", taskqueue.ReasonPayloadInvalid, err)
		return
	}
	request := queuedChatRequest(requestPayload, outbox)
	threadAgent, ok := coordinator.handler.codex.(codex.ThreadClient)
	if !ok || coordinator.handler.sessions == nil {
		coordinator.failBeforeExecution(client, task, active, "Codex 线程运行时不可用。", taskqueue.ReasonSessionUnavailable, nil)
		return
	}
	thread, err := coordinator.handler.sessions.OpenTaskThread(taskContext, task.OwnerID, task.ProjectID, task.ThreadID, threadAgent, suggestedSessionName(request))
	if err != nil {
		coordinator.failBeforeExecution(client, task, active, "请求绑定的 Codex 线程不可用，未自动切换线程。", taskqueue.ReasonSessionUnavailable, err)
		return
	}
	if task.ThreadID == "" {
		if err := coordinator.tasks.AttachThread(task.OwnerID, task.ID, thread.ID); err != nil {
			coordinator.failBeforeExecution(client, task, active, "无法固定新建 Codex 线程，请求已停止。", taskqueue.ReasonSessionUnavailable, err)
			return
		}
		task.ThreadID = thread.ID
	}
	threadSettings := coordinator.handler.sessions.SettingsForTask(task.OwnerID, task.ProjectID, thread.ID)
	request.Model = threadSettings.Model
	request.Effort = threadSettings.Effort

	reporter := newProgressReporter(taskContext, client, task.OwnerID, requestPayload.ContextToken, coordinator.handler.progress, func(stage string) {
		stage = truncateRunes(strings.Join(strings.Fields(stage), " "), 120)
		if err := coordinator.tasks.UpdateStage(task.OwnerID, task.ID, stage); err != nil && taskContext.Err() == nil {
			log.Printf("[queue] failed to update task stage: %v", err)
		}
	})
	defer reporter.Close()
	info := coordinator.handler.codex.Info()
	log.Printf("[queue] dispatching task %s to Codex (%s, project=%s) for %s", shortTaskID(task.ID), info, task.ProjectID, ilink.LogLabel(task.OwnerID))
	started := time.Now()
	var reply string
	if progressCodex, supportsProgress := coordinator.handler.codex.(codex.ProgressClient); supportsProgress {
		reply, err = progressCodex.ChatThreadWithProgress(taskContext, thread.ID, request, reporter.Report)
	} else {
		reply, err = threadAgent.ChatThread(taskContext, thread.ID, request)
	}
	reporter.Close()
	if usageProvider, ok := coordinator.handler.codex.(codex.UsageProvider); ok {
		if usage, exists := usageProvider.Usage(thread.ID); exists {
			if usageErr := coordinator.tasks.AttachUsage(task.OwnerID, task.ID, usage.Last.InputTokens, usage.Last.OutputTokens, usage.Last.TotalTokens); usageErr != nil {
				log.Printf("[queue] failed to persist token usage: %v", usageErr)
			}
		}
	}
	if touchErr := coordinator.handler.sessions.Touch(task.OwnerID, thread.ID, time.Now().Unix()); touchErr != nil {
		log.Printf("[queue] failed to persist session recency: %v", touchErr)
	}
	cancelled := coordinator.closeExecution(active, err == nil && taskContext.Err() == nil)
	if cancelled {
		_, finishErr := coordinator.tasks.Finish(task.OwnerID, task.ID, taskqueue.StateCancelled, taskqueue.ReasonUserCancelled)
		if finishErr != nil {
			log.Printf("[queue] failed to finish cancelled task: %v", finishErr)
		}
		return
	}
	// 服务退出时保留 running，让下次启动按“中断且不自动重试”恢复；不能伪装成用户取消。
	if errors.Is(taskContext.Err(), context.Canceled) {
		return
	}
	if err != nil {
		coordinator.failTask(client, task, "Codex 轮次执行失败，codex-link-clawbot 请求输入保留 24 小时供手动重试。", taskqueue.ReasonCodexFailed, err)
		return
	}
	log.Printf("[queue] Codex completed task %s (elapsed=%s chars=%d)", shortTaskID(task.ID), time.Since(started), len([]rune(reply)))
	artifacts, collectErr := collectArtifacts(outbox)
	if collectErr != nil {
		coordinator.failTask(client, task, "无法校验 Codex 交付文件，请求结果未开始发送。", taskqueue.ReasonResultFreezeFailed, collectErr)
		return
	}
	if len(artifacts.Skipped) > 0 {
		reply = appendArtifactSummary(reply, nil, artifacts.Skipped)
	}
	result, err := coordinator.tasks.FreezeResult(task.OwnerID, task.ID, taskqueue.FreezeResultInput{
		Reply: reply, ArtifactPaths: artifacts.Paths, ImageURLs: ExtractImageURLs(reply),
	})
	if err != nil {
		coordinator.failTask(client, task, "无法冻结请求发送计划，结果未开始发送。", taskqueue.ReasonResultFreezeFailed, err)
		return
	}
	if _, err := coordinator.tasks.BeginDelivery(task.OwnerID, task.ID); err != nil {
		coordinator.failTask(client, task, "无法冻结请求发送状态。", taskqueue.ReasonDeliveryFailed, err)
		return
	}
	message := ilink.WeixinMessage{FromUserID: task.OwnerID, ContextToken: requestPayload.ContextToken}
	report := coordinator.handler.sendReplyWithMediaForTask(taskContext, client, message, task, result, NewClientID())
	attemptedAt := time.Now().Unix()
	if attemptedAt < result.FrozenAt {
		attemptedAt = result.FrozenAt
	}
	receipt := taskqueue.DeliveryReceipt{
		Outcome: report.Outcome, AttemptedAt: attemptedAt, MediaSent: report.MediaSent,
		TextSent: report.TextSent, FailureCode: report.Failure,
	}
	if err := coordinator.tasks.RecordDelivery(task.OwnerID, task.ID, receipt); err != nil {
		log.Printf("[queue] failed to persist delivery receipt for %s: %v", shortTaskID(task.ID), err)
		_, _ = coordinator.tasks.Finish(task.OwnerID, task.ID, taskqueue.StateInterrupted, taskqueue.ReasonDeliveryAmbiguous)
		coordinator.deferTaskRecoveryNotice(task, taskqueue.ReasonDeliveryAmbiguous)
		return
	}
	terminalState := taskqueue.StateSucceeded
	reason := ""
	if report.Outcome == taskqueue.DeliveryExplicitFailure {
		terminalState = taskqueue.StateFailed
		reason = taskqueue.ReasonDeliveryFailed
	} else if report.Outcome == taskqueue.DeliveryAmbiguous {
		terminalState = taskqueue.StateInterrupted
		reason = taskqueue.ReasonDeliveryAmbiguous
	}
	if _, err := coordinator.tasks.Finish(task.OwnerID, task.ID, terminalState, reason); err != nil {
		log.Printf("[queue] failed to finish delivery task %s: %v", shortTaskID(task.ID), err)
	}
	if reason != "" {
		coordinator.deferTaskRecoveryNotice(task, reason)
	}
}

func queuedChatRequest(payload taskqueue.LoadedRequest, outbox string) codex.ChatRequest {
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

func (coordinator *Coordinator) failBeforeExecution(client *ilink.Client, task taskqueue.Task, active *coordinatorTask, notice, reason string, cause error) {
	if coordinator.closeExecution(active, false) {
		if _, err := coordinator.tasks.Finish(task.OwnerID, task.ID, taskqueue.StateCancelled, taskqueue.ReasonUserCancelled); err != nil {
			log.Printf("[queue] failed to finish cancelled task: %v", err)
		}
		return
	}
	coordinator.failTask(client, task, notice, reason, cause)
}

func (coordinator *Coordinator) failTask(client *ilink.Client, task taskqueue.Task, notice, reason string, cause error) {
	if cause != nil {
		log.Printf("[queue] task %s failed (%s): %v", shortTaskID(task.ID), reason, cause)
	}
	_, finishErr := coordinator.tasks.Finish(task.OwnerID, task.ID, taskqueue.StateFailed, reason)
	if finishErr != nil {
		log.Printf("[queue] failed to persist failed task %s: %v", shortTaskID(task.ID), finishErr)
	}
	payload, loadErr := coordinator.tasks.LoadRequest(task.OwnerID, task.ID)
	if loadErr == nil && client != nil {
		if sendErr := SendTextReply(context.Background(), client, task.OwnerID, notice, payload.ContextToken, NewClientID()); sendErr != nil {
			log.Printf("[queue] failed to send task failure notice: %v", sendErr)
			if !outboundMayBeVisible(sendErr) {
				coordinator.deferTaskRecoveryNotice(task, reason)
			}
		}
	} else {
		coordinator.deferTaskRecoveryNotice(task, reason)
	}
}

func (coordinator *Coordinator) deferTaskRecoveryNotice(task taskqueue.Task, reason string) {
	if coordinator.handler.pendingNotices == nil {
		return
	}
	title := "codex-link-clawbot 请求需要处理"
	action := "发送“请求队列”查看详情。"
	switch reason {
	case taskqueue.ReasonDeliveryFailed:
		title = "Codex 结果待取回"
		action = "Codex 已完成，但微信明确拒绝了本次交付。发送“请求队列”取回冻结结果。"
	case taskqueue.ReasonDeliveryAmbiguous:
		title = "Codex 结果发送状态待确认"
		action = "Codex 已完成，但微信发送结果不确定。请先检查是否已收到，再从“请求队列”处理。"
	case taskqueue.ReasonCodexFailed:
		title = "Codex 执行失败"
		action = "请求输入暂时保留，可从“请求队列”查看并手动重试。"
	}
	_, _, err := coordinator.handler.pendingNotices.Enqueue(task.OwnerID, PendingNoticeInput{
		Kind: PendingNoticeTaskRecovery, DedupKey: "task-recovery:" + task.ID + ":" + reason,
		ReferenceID: task.ID, Title: title, Body: "请求：" + task.Summary + "\n" + action, TTL: 24 * time.Hour,
	})
	if err != nil {
		log.Printf("[queue] defer task recovery notice for %s failed: %v", shortTaskID(task.ID), err)
	}
}

func (coordinator *Coordinator) closeExecution(active *coordinatorTask, delivering bool) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.active != active {
		return false
	}
	active.finalizing = true
	active.delivering = delivering
	return active.cancelRequested
}

func (coordinator *Coordinator) Cancel(ownerID string) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.active == nil || coordinator.active.ownerID != strings.TrimSpace(ownerID) || coordinator.active.cancelRequested || coordinator.active.finalizing || coordinator.active.delivering {
		return false
	}
	coordinator.active.cancelRequested = true
	coordinator.active.cancel()
	return true
}

func (coordinator *Coordinator) SetDraining(draining bool) {
	coordinator.mu.Lock()
	coordinator.draining = draining
	coordinator.mu.Unlock()
	coordinator.Wake()
}

func (coordinator *Coordinator) isDraining() bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.draining
}

func shortTaskID(taskID string) string {
	const length = 6
	if len(taskID) <= length {
		return taskID
	}
	return taskID[len(taskID)-length:]
}
