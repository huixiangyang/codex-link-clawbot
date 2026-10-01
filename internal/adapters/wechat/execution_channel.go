package wechat

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/access"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/delivery"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/thread"
)

type clientRegistry struct {
	clients  sync.Map
	contexts sync.Map
}

func (r *clientRegistry) register(owner string, client *ilink.Client) {
	if owner = strings.TrimSpace(owner); owner != "" && client != nil {
		r.clients.Store(owner, client)
	}
}

func (r *clientRegistry) load(owner string) (*ilink.Client, bool) {
	value, ok := r.clients.Load(owner)
	if !ok {
		return nil, false
	}
	return value.(*ilink.Client), true
}

// executionChannel 不执行 Codex 轮次，不决定请求终态，只实现微信侧输入与输出。
type executionChannel struct {
	clients        *clientRegistry
	tasks          *request.Store
	targets        *target.Store
	remoteLock     *access.RemoteLock
	pendingNotices *delivery.NoticeStore
	sendBusy       func(context.Context, *ilink.Client, ilink.WeixinMessage, request.Rejection) error
	sendReply      func(context.Context, *ilink.Client, ilink.WeixinMessage, request.Task, request.Result, string) deliveryReport
}

func (c *executionChannel) CanExecute(owner string) bool {
	_, available := c.clients.load(owner)
	return available && (c.remoteLock == nil || !c.remoteLock.IsLocked(owner))
}

func (c *executionChannel) Prepare(ctx context.Context, task request.Task, payload request.LoadedRequest) (request.LoadedRequest, error) {
	_, ok := c.clients.load(task.OwnerID)
	if !ok {
		return payload, fmt.Errorf("微信发送客户端不可用")
	}
	if len(payload.SourceData) == 0 {
		return payload, nil
	}
	var incoming incomingAttachments
	if err := json.Unmarshal(payload.SourceData, &incoming); err != nil {
		return payload, err
	}
	text, images, files, err := prepareRequestInput(ctx, payload.Text, incoming.Images, incoming.Files)
	if err != nil {
		return payload, err
	}
	if err := ctx.Err(); err != nil {
		return payload, err
	}
	if err := c.tasks.AttachInput(task.OwnerID, task.ID, text, images, files); err != nil {
		return payload, err
	}
	return c.tasks.LoadRequest(task.OwnerID, task.ID)
}

func (c *executionChannel) Progress(task request.Task, payload request.LoadedRequest, workspaceName string) execution.ProgressCallbacks {
	client, _ := c.clients.load(task.OwnerID)
	return execution.ProgressCallbacks{
		SendTyping: func(ctx context.Context) error {
			if client == nil {
				return fmt.Errorf("微信发送客户端不可用")
			}
			return SendTypingState(ctx, client, task.OwnerID, payload.ContextToken)
		},
		SendSlow: func(ctx context.Context) error {
			if client == nil {
				return fmt.Errorf("微信发送客户端不可用")
			}
			message := "还在处理中，完成后会直接回复。"
			if !isCurrentTask(c.targets, task) {
				message = workspaceName + " · " + shortTaskID(task.ID) + "\n" + message
			}
			sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return SendTextReply(sendCtx, client, task.OwnerID, message, payload.ContextToken, NewClientID())
		},
		OnError: func(operation string, err error) {
			log.Printf("[progress] %s failed for %s: %v", operation, ilink.LogLabel(task.OwnerID), err)
		},
	}
}

func (c *executionChannel) Deliver(ctx context.Context, task request.Task, payload request.LoadedRequest, result request.Result) request.DeliveryReceipt {
	client, ok := c.clients.load(task.OwnerID)
	if !ok {
		return request.DeliveryReceipt{Outcome: request.DeliveryExplicitFailure, FailureCode: request.ReasonDeliveryFailed}
	}
	message := ilink.WeixinMessage{FromUserID: task.OwnerID, ContextToken: payload.ContextToken}
	report := c.sendReply(ctx, client, message, task, result, NewClientID())
	return request.DeliveryReceipt{Outcome: report.Outcome, MediaSent: report.MediaSent, TextSent: report.TextSent, FailureCode: report.Failure}
}

func (c *executionChannel) Notify(ctx context.Context, task request.Task, notice, reason string) {
	payload, err := c.tasks.LoadRequest(task.OwnerID, task.ID)
	client, ok := c.clients.load(task.OwnerID)
	if err != nil || !ok {
		c.DeferNotice(task, reason)
		return
	}
	if reason == "session_busy" && c.sendBusy != nil {
		message := ilink.WeixinMessage{FromUserID: task.OwnerID, ContextToken: payload.ContextToken}
		if err := c.sendBusy(ctx, client, message, request.Rejection{OwnerID: task.OwnerID, TargetID: task.TargetID, ThreadID: task.ThreadID}); err != nil {
			log.Printf("[execution] show session menu: %v", err)
		}
		return
	}
	if err := SendTextReply(ctx, client, task.OwnerID, notice, payload.ContextToken, NewClientID()); err != nil {
		log.Printf("[execution] send failure notice: %v", err)
		if !outboundMayBeVisible(err) {
			c.DeferNotice(task, reason)
		}
	}
}

func (c *executionChannel) DeferNotice(task request.Task, reason string) {
	if c.pendingNotices == nil {
		return
	}
	title := "codex-link-clawbot 请求需要处理"
	action := "回复 #，再选 #5 查看详情和恢复操作。"
	switch reason {
	case request.ReasonDeliveryFailed:
		title, action = "Codex 结果待取回", "Codex 已完成，但微信明确拒绝了本次交付。回复 #，再选 #5 查看状态。"
	case request.ReasonDeliveryAmbiguous:
		title, action = "Codex 结果发送状态待确认", "Codex 已完成，但微信发送结果不确定。请先检查是否已收到，再回复 #，选择 #5 处理。"
	case request.ReasonCodexFailed:
		title, action = "Codex 执行失败", "请求输入暂时保留，回复 #，再选 #5 查看状态。"
	}
	_, _, err := c.pendingNotices.Enqueue(task.OwnerID, delivery.NoticeInput{
		Kind: delivery.NoticeTaskRecovery, DedupKey: "task-recovery:" + task.ID + ":" + reason,
		ReferenceID: task.ID, Title: title, Body: "请求：" + task.Summary + "\n" + action, TTL: 24 * time.Hour,
	})
	if err != nil {
		log.Printf("[execution] defer recovery notice for %s: %v", task.ID, err)
	}
}

func shortTaskID(id string) string { return thread.ShortCode(id) }
