package wechat

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
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
	return r.redeliver(ctx, ownerID, taskID, operationID, false)
}

func (r *Runtime) redeliver(ctx context.Context, ownerID, taskID, operationID string, filesOnly bool) error {
	operation, err := uuid.Parse(operationID)
	if err != nil {
		return fmt.Errorf("恢复操作编号无效")
	}
	operationID = operation.String()
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
	if filesOnly {
		result, err := r.Handler.tasks.InspectResult(ownerID, taskID)
		if err != nil {
			return err
		}
		if len(result.Artifacts)+len(result.ImageURLs) == 0 {
			return fmt.Errorf("该结果没有文件，无需取回。可发送“全文 %s”阅读。", shortTaskID(taskID))
		}
	}
	result, duplicate, err := r.Handler.tasks.BeginRedelivery(ownerID, taskID, operationID)
	if err != nil {
		return err
	}
	if duplicate {
		for _, attempt := range result.Attempts {
			if attempt.OperationID == operationID {
				if attempt.Outcome != request.DeliverySucceeded {
					return fmt.Errorf("原投递尚未确认成功，请查看回执；没有再次发送")
				}
				return nil
			}
		}
		return fmt.Errorf("原投递回执不可用")
	}
	task, _ := r.Handler.tasks.Find(ownerID, taskID)
	var report deliveryReport
	if filesOnly {
		report = r.Handler.sendResultFiles(ctx, client, ownerID, token, task, result)
	} else {
		report = r.Handler.sendReplyWithMediaForTask(ctx, client, ilink.WeixinMessage{FromUserID: ownerID, ContextToken: token}, task, result, NewClientID())
	}
	receipt := request.DeliveryReceipt{OperationID: operationID, Outcome: report.Outcome, AttemptedAt: max(time.Now().Unix(), result.FrozenAt), MediaSent: report.MediaSent, TextSent: report.TextSent, FailureCode: report.Failure}
	if err := r.Handler.tasks.RecordDelivery(ownerID, taskID, receipt); err != nil {
		return err
	}
	if report.Outcome != request.DeliverySucceeded {
		return fmt.Errorf("微信投递未确认成功，请查看投递状态；网页结果仍可取回")
	}
	return nil
}

// ExpireMessageContexts 主动清理过期发送上下文，不让闲置实例长期保存令牌。
func (r *Runtime) ExpireMessageContexts() {
	if r.Handler.tasks != nil {
		if err := r.Handler.expireDrafts(); err != nil {
			log.Printf("[draft] expiry failed: %v", err)
		}
	}
	r.Handler.clients.contexts.Range(func(owner, value any) bool {
		if time.Since(value.(liveContext).received) > 24*time.Hour {
			r.Handler.clients.contexts.CompareAndDelete(owner, value)
		}
		return true
	})
}

// RestoreResult 只恢复归档，不调用 turn/start，也不重做项目修改。
func (r *Runtime) RestoreResult(ctx context.Context, owner, id string) error {
	return r.Handler.runner.RestoreResult(ctx, owner, id)
}
