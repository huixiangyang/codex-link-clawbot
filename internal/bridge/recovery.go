package bridge

import (
	"context"
	"errors"
	"fmt"
	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
	"path/filepath"
	"strings"
	"time"
)

type liveContext struct {
	token    string
	received time.Time
}

func (r *clientRegistry) updateContext(ownerID, token string) {
	if strings.TrimSpace(token) != "" {
		r.contexts.Store(ownerID, liveContext{token: token, received: time.Now()})
	}
}
func (r *clientRegistry) context(ownerID string) (string, error) {
	value, ok := r.contexts.Load(ownerID)
	if !ok {
		return "", fmt.Errorf("需要新的微信消息上下文，请先发送“菜单”；现有结果仍可在网页查看和下载")
	}
	c := value.(liveContext)
	if time.Since(c.received) > 24*time.Hour {
		r.contexts.CompareAndDelete(ownerID, c)
		return "", fmt.Errorf("微信上下文已到期，请先发送“菜单”")
	}
	return c.token, nil
}
func (r *Runtime) Retry(ctx context.Context, ownerID, taskID, operationID string) (request.Task, error) {
	source, err := request.RecoverySource(ownerID, operationID)
	if err != nil {
		return request.Task{}, err
	}
	if r.Handler.tasks.WasCleared(source) {
		return request.Task{}, request.ErrCleared
	}
	if existing, ok := r.Handler.tasks.FindBySource(source); ok {
		if existing.OwnerID != ownerID || existing.RetryOf != taskID {
			return request.Task{}, fmt.Errorf("恢复编号已用于其他请求")
		}
		return existing, nil
	}
	if _, rejected := r.Handler.tasks.FindRejection(source); rejected {
		return request.Task{}, request.ErrRejected
	}
	if r.Handler.remoteLock.IsLocked(ownerID) {
		return request.Task{}, fmt.Errorf("远程入口已锁定")
	}
	token, err := r.Handler.clients.context(ownerID)
	if err != nil {
		return request.Task{}, err
	}
	original, ok := r.Handler.tasks.Find(ownerID, taskID)
	if !ok {
		return request.Task{}, fmt.Errorf("原请求不存在")
	}
	rejectBusy := func() (request.Task, error) {
		activity, _ := r.Handler.sessionActivity(ctx, ownerID, original.TargetID, original.ThreadID)
		receipt := request.Rejection{Source: source, OwnerID: ownerID, TargetID: original.TargetID, ThreadID: activity.ThreadID, TaskID: activity.TaskID, TurnID: activity.TurnID}
		if err := r.Handler.tasks.Reject(receipt); err != nil {
			return request.Task{}, err
		}
		return request.Task{}, request.ErrSessionBusy
	}
	admission, err := r.Coordinator.Begin(ownerID, original.TargetID, original.ThreadID, source)
	if errors.Is(err, request.ErrSessionBusy) {
		return rejectBusy()
	}
	if err != nil {
		return request.Task{}, err
	}
	defer admission.Release()
	activity, err := r.Handler.sessionActivity(ctx, ownerID, original.TargetID, original.ThreadID)
	if err != nil {
		return request.Task{}, err
	}
	if activity.Busy {
		return rejectBusy()
	}
	task, err := r.Handler.tasks.Retry(ownerID, taskID, source, token)
	if err == nil {
		admission.Launch(task)
	}
	return task, err
}
func (r *Runtime) Redeliver(ctx context.Context, ownerID, taskID, operationID string) error {
	if r.Handler.remoteLock.IsLocked(ownerID) {
		return fmt.Errorf("远程入口已锁定")
	}
	token, err := r.Handler.clients.context(ownerID)
	if err != nil {
		return err
	}
	client, ok := r.Handler.clients.load(ownerID)
	if !ok {
		return fmt.Errorf("微信连接不可用")
	}
	var actionErr error
	func() {
		result, duplicate, err := r.Handler.tasks.BeginRedelivery(ownerID, taskID, operationID)
		if err != nil {
			actionErr = err
			return
		}
		if duplicate {
			for _, attempt := range result.Attempts {
				if attempt.OperationID == operationID {
					if attempt.Outcome != request.DeliverySucceeded {
						actionErr = fmt.Errorf("原投递尚未确认成功，请查看回执；没有再次发送")
					}
					return
				}
			}
			actionErr = fmt.Errorf("原投递回执不可用")
			return
		}
		task, _ := r.Handler.tasks.Find(ownerID, taskID)
		report := r.Handler.sendReplyWithMediaForTask(ctx, client, ilink.WeixinMessage{FromUserID: ownerID, ContextToken: token}, task, result, NewClientID())
		receipt := request.DeliveryReceipt{OperationID: operationID, Outcome: report.Outcome, AttemptedAt: max(time.Now().Unix(), result.FrozenAt), MediaSent: report.MediaSent, TextSent: report.TextSent, FailureCode: report.Failure}
		actionErr = r.Handler.tasks.RecordDelivery(ownerID, taskID, receipt)
		if actionErr == nil && report.Outcome != request.DeliverySucceeded {
			actionErr = fmt.Errorf("微信投递未确认成功，请查看投递状态；网页结果仍可取回")
		}
	}()
	return actionErr
}

// ExpireMessageContexts 主动清理过期发送上下文，不让闲置实例长期保存令牌。
func (r *Runtime) ExpireMessageContexts() {
	r.Handler.clients.contexts.Range(func(owner, value any) bool {
		if time.Since(value.(liveContext).received) > 24*time.Hour {
			r.Handler.clients.contexts.CompareAndDelete(owner, value)
		}
		return true
	})
}

// RestoreResult 只恢复归档，不调用 turn/start，也不重做项目修改。
func (r *Runtime) RestoreResult(ctx context.Context, owner, id string) error {
	task, ok := r.Handler.tasks.Find(owner, id)
	if !ok || task.ExecutionCompletedAt == 0 || !task.ArchiveFailed || task.ResultExpiresAt <= time.Now().Unix() {
		return fmt.Errorf("当前结果不需要恢复保存")
	}
	checkpoint, err := r.Handler.tasks.LoadCompletion(owner, id)
	reply := checkpoint.Reply
	if err != nil {
		reader, ok := r.Handler.sessionClient.(codex.TurnReader)
		if !ok || task.TurnID == "" {
			return fmt.Errorf("无法取回完成记录，请在原 Codex 会话查看成果；不会重新执行")
		}
		result, readErr := reader.ReadTurn(ctx, task.ThreadID, task.TurnID)
		if readErr != nil || result.Status != "completed" {
			return fmt.Errorf("原轮次结果暂不可用，请稍后重试")
		}
		reply = result.Reply
	}
	artifacts, err := collectArtifacts(filepath.Join(r.Handler.tasks.Root(), id, "outbox"))
	if err != nil {
		return err
	}
	if len(artifacts.Skipped) > 0 {
		reply = appendArtifactSummary(reply, nil, artifacts.Skipped)
	}
	_, err = r.Handler.tasks.FreezeResult(owner, id, request.FreezeResultInput{Reply: reply, ArtifactPaths: artifacts.Paths, ImageURLs: ExtractImageURLs(reply)})
	return err
}
