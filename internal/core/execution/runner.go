package execution

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/workspace"
)

// Channel 只负责渠道输入和输出；执行状态、取消终态与结果恢复由 Runner 决定。
type Channel interface {
	CanExecute(string) bool
	Prepare(context.Context, request.Task, request.LoadedRequest) (request.LoadedRequest, error)
	Progress(request.Task, request.LoadedRequest, string) ProgressCallbacks
	Deliver(context.Context, request.Task, request.LoadedRequest, request.Result) request.DeliveryReceipt
	Notify(context.Context, request.Task, string, string)
	DeferNotice(request.Task, string)
}

type RunnerDependencies struct {
	Targets    *target.Store
	Codex      codex.Runtime
	Workspaces *workspace.Manager
	Threads    *thread.Manager
	Requests   *request.Store
	Progress   ProgressConfig
	Channel    Channel
}

type Runner struct {
	deps   RunnerDependencies
	client codex.ThreadClient
}

func NewRunner(deps RunnerDependencies) (*Runner, error) {
	client, ok := deps.Codex.(codex.ThreadClient)
	if !ok || deps.Targets == nil || deps.Workspaces == nil || deps.Threads == nil || deps.Requests == nil || deps.Channel == nil {
		return nil, fmt.Errorf("execution runner dependencies incomplete")
	}
	if err := deps.Progress.Validate(); err != nil {
		return nil, err
	}
	return &Runner{deps: deps, client: client}, nil
}

func (r *Runner) CanExecute(owner string) bool { return r.deps.Channel.CanExecute(owner) }

func (r *Runner) Execute(ctx context.Context, task request.Task, finalize func() bool) {
	payload, err := r.deps.Requests.LoadRequest(task.OwnerID, task.ID)
	if err != nil {
		r.failBefore(ctx, task, finalize, "请求输入损坏，已停止执行。", request.ReasonPayloadInvalid, err)
		return
	}
	definition, ok := r.deps.Workspaces.Get(task.ProjectID)
	if !ok {
		r.failBefore(ctx, task, finalize, "请求绑定的工作空间已不存在，未改用其他目录。", request.ReasonProjectUnavailable, nil)
		return
	}
	// 等待提示从准备阶段开始计时，附件下载和线程初始化也不会让用户一直静默等待。
	callbacks := r.deps.Channel.Progress(task, payload, definition.Name)
	callbacks.Persist = func(stage string) {
		if err := r.deps.Requests.UpdateStage(task.OwnerID, task.ID, presentation.NormalizeLine(stage, 120)); err != nil && ctx.Err() == nil {
			log.Printf("[execution] persist stage for %s: %v", task.ID, err)
		}
	}
	reporter, err := NewProgressReporter(ctx, r.deps.Progress, callbacks)
	if err != nil {
		r.failBefore(ctx, task, finalize, "阶段呈现配置无效。", request.ReasonPayloadInvalid, err)
		return
	}
	defer reporter.Close()
	payload, err = r.deps.Channel.Prepare(ctx, task, payload)
	if err != nil || ctx.Err() != nil {
		reporter.Close()
		r.failBefore(ctx, task, finalize, "附件接收或保存未完成，请在最近结果查看详情。", request.ReasonPayloadInvalid, err)
		return
	}
	outbox, err := r.deps.Requests.PrepareOutbox(task.OwnerID, task.ID)
	if err != nil {
		reporter.Close()
		r.failBefore(ctx, task, finalize, "无法创建请求交付目录。", request.ReasonPayloadInvalid, err)
		return
	}
	input := taskChatRequest(payload, outbox)
	input.WorkspaceRoot = definition.Root
	intent, err := r.deps.Targets.Resolve(task.OwnerID, task.TargetID)
	if err != nil || intent.WorkspaceID != task.ProjectID {
		reporter.Close()
		r.failBefore(ctx, task, finalize, "请求的会话目标不可用。", request.ReasonSessionUnavailable, err)
		return
	}
	targetThread := task.ThreadID
	if targetThread == "" {
		targetThread = intent.ThreadID
	}
	info, err := r.deps.Threads.OpenTaskThread(ctx, task.OwnerID, definition, targetThread, r.client, suggestedSessionName(input))
	if err != nil {
		reporter.Close()
		r.failBefore(ctx, task, finalize, "请求绑定的 Codex 线程不可用，未自动切换线程。", request.ReasonSessionUnavailable, err)
		return
	}
	if intent.ThreadID == "" {
		if err := r.deps.Targets.Bind(task.OwnerID, task.TargetID, info.ID); err != nil {
			reporter.Close()
			r.failBefore(ctx, task, finalize, "无法保存当前会话。", request.ReasonSessionUnavailable, err)
			return
		}
	}
	if task.ThreadID == "" {
		if err := r.deps.Requests.AttachThread(task.OwnerID, task.ID, info.ID); err != nil {
			reporter.Close()
			r.failBefore(ctx, task, finalize, "无法固定新建 Codex 线程，请求已停止。", request.ReasonSessionUnavailable, err)
			return
		}
		task.ThreadID = info.ID
	}

	started := time.Now()
	var reply string
	if client, ok := r.deps.Codex.(codex.TurnProgressClient); ok {
		reply, err = client.ChatThreadWithProgress(ctx, task.ThreadID, input, func(event codex.TurnPhaseEvent) {
			if event.TurnID != "" {
				if err := r.deps.Requests.AttachTurn(task.OwnerID, task.ID, event.TurnID); err != nil {
					log.Printf("[execution] persist turn for %s: %v", task.ID, err)
				}
			}
			reporter.Report(event)
		})
	} else {
		reply, err = r.client.ChatThread(ctx, task.ThreadID, input)
	}
	reporter.Close()
	if provider, ok := r.deps.Codex.(codex.UsageProvider); ok {
		if usage, found := provider.Usage(task.ThreadID); found {
			if err := r.deps.Requests.AttachUsage(task.OwnerID, task.ID, usage.Last.InputTokens, usage.Last.OutputTokens, usage.Last.TotalTokens); err != nil {
				log.Printf("[execution] persist usage for %s: %v", task.ID, err)
			}
		}
	}
	cancelled := finalize()
	if err != nil {
		switch {
		case cancelled && errors.Is(err, codex.ErrInterruptUnconfirmed):
			r.fail(ctx, task, "打断尚未确认，会话可能仍在执行。回复 #，再选 #4 刷新。", request.ReasonInterruptUnconfirmed, err)
		case cancelled && (errors.Is(err, codex.ErrTurnInterrupted) || errors.Is(err, context.Canceled)):
			r.cancelled(ctx, task)
		case ctx.Err() == nil:
			r.fail(ctx, task, "Codex 执行未完成。回复 #，再选 #5 查看原因与恢复操作。", request.ReasonCodexFailed, err)
		}
		return
	}
	log.Printf("[execution] completed %s (elapsed=%s chars=%d)", task.ID, time.Since(started), len([]rune(reply)))
	if err := r.deps.Requests.CompleteExecution(task.OwnerID, task.ID, reply); err != nil {
		r.fail(ctx, task, "Codex 已完成，但结果保存未完成。请恢复保存，不要重新执行。", request.ReasonResultFreezeFailed, err)
		return
	}
	result, err := r.freeze(task, reply)
	if err != nil {
		r.fail(ctx, task, "Codex 已完成，结果归档失败。回复 #，再选 #5 恢复保存。", request.ReasonResultFreezeFailed, err)
		return
	}
	if _, err := r.deps.Requests.BeginDelivery(task.OwnerID, task.ID); err != nil {
		r.fail(ctx, task, "无法保存请求发送状态。", request.ReasonDeliveryFailed, err)
		return
	}
	// 上游已确认成功时，迟到的取消不得破坏交付；收尾仍有明确时间上限。
	deliveryCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	receipt := r.deps.Channel.Deliver(deliveryCtx, task, payload, result)
	receipt.AttemptedAt = max(time.Now().Unix(), result.FrozenAt)
	if err := r.deps.Requests.CompleteDelivery(task.OwnerID, task.ID, receipt); err != nil {
		log.Printf("[execution] persist delivery for %s: %v", task.ID, err)
		_, _ = r.deps.Requests.Finish(task.OwnerID, task.ID, request.StateInterrupted, request.ReasonDeliveryAmbiguous)
		r.deps.Channel.DeferNotice(task, request.ReasonDeliveryAmbiguous)
		return
	}
	if receipt.Outcome == request.DeliveryExplicitFailure {
		r.deps.Channel.DeferNotice(task, request.ReasonDeliveryFailed)
	} else if receipt.Outcome == request.DeliveryAmbiguous {
		r.deps.Channel.DeferNotice(task, request.ReasonDeliveryAmbiguous)
	}
}

func (r *Runner) failBefore(ctx context.Context, task request.Task, finalize func() bool, message, reason string, cause error) {
	if finalize() {
		r.cancelled(ctx, task)
	} else if ctx.Err() == nil {
		r.fail(ctx, task, message, reason, cause)
	}
}

func (r *Runner) fail(ctx context.Context, task request.Task, message, reason string, cause error) {
	if errors.Is(cause, codex.ErrThreadBusy) {
		reason = "session_busy"
	}
	if cause != nil {
		log.Printf("[execution] %s failed (%s): %v", task.ID, reason, cause)
	}
	if _, err := r.deps.Requests.Finish(task.OwnerID, task.ID, request.StateFailed, reason); err != nil {
		log.Printf("[execution] persist failure for %s: %v", task.ID, err)
	}
	noticeCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	r.deps.Channel.Notify(noticeCtx, task, message, reason)
}

func (r *Runner) cancelled(ctx context.Context, task request.Task) {
	if _, err := r.deps.Requests.Finish(task.OwnerID, task.ID, request.StateCancelled, request.ReasonUserCancelled); err != nil {
		log.Printf("[execution] persist cancellation for %s: %v", task.ID, err)
		return
	}
	noticeCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	r.deps.Channel.Notify(noticeCtx, task, "已确认停止 · "+thread.ShortCode(task.ID)+"\n会话 "+thread.ShortCode(task.ThreadID)+" · "+task.ProjectID+"\n已有修改保留，没有执行后续指令。", request.ReasonUserCancelled)
}

func (r *Runner) freeze(task request.Task, reply string) (request.Result, error) {
	artifacts, err := request.CollectArtifacts(filepath.Join(r.deps.Requests.Root(), task.ID, "output"))
	if err != nil {
		return request.Result{}, err
	}
	reply = request.AppendArtifactSummary(reply, nil, artifacts.Skipped)
	return r.deps.Requests.FreezeResult(task.OwnerID, task.ID, request.FreezeResultInput{Reply: reply, ArtifactPaths: artifacts.Paths, ImageURLs: presentation.ExtractImageURLs(reply)})
}

// RestoreResult 复用首次归档规则，只读原轮次，绝不调用 turn/start。
func (r *Runner) RestoreResult(ctx context.Context, owner, id string) error {
	task, ok := r.deps.Requests.Find(owner, id)
	if !ok || task.ExecutionCompletedAt == 0 || !task.ArchiveFailed || task.ResultExpiresAt <= time.Now().Unix() {
		return fmt.Errorf("当前结果不需要恢复保存")
	}
	checkpoint, err := r.deps.Requests.LoadCompletion(owner, id)
	reply := checkpoint.Reply
	if err != nil {
		reader, ok := r.deps.Codex.(codex.TurnReader)
		if !ok || task.TurnID == "" {
			return fmt.Errorf("无法取回完成记录，请在原 Codex 会话查看成果；不会重新执行")
		}
		result, readErr := reader.ReadTurn(ctx, task.ThreadID, task.TurnID)
		if readErr != nil || result.Status != "completed" {
			return fmt.Errorf("原轮次结果暂不可用，请稍后重试")
		}
		reply = result.Reply
	}
	_, err = r.freeze(task, reply)
	return err
}
