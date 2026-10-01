package wechat

import (
	"context"
	"fmt"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/thread"
)

type sessionActivity struct {
	execution.Activity
	WorkspaceID string
}

func (h *Handler) sessionActivity(ctx context.Context, owner, targetID, threadID string) (sessionActivity, error) {
	activity := sessionActivity{Activity: execution.Activity{TargetID: targetID, ThreadID: threadID, Stage: "空闲，可发送工作内容"}}
	if targetID != "" {
		intent, err := h.targets.Resolve(owner, targetID)
		if err != nil {
			return activity, err
		}
		activity.WorkspaceID = intent.WorkspaceID
		if activity.ThreadID == "" {
			activity.ThreadID = intent.ThreadID
		}
		if intent.ThreadID != "" && activity.ThreadID != intent.ThreadID {
			return activity, fmt.Errorf("会话目标已改变")
		}
	}
	value, err := execution.ReadActivity(ctx, h.tasks, h.sessionClient, owner, targetID, activity.ThreadID)
	activity.Activity = value
	return activity, err
}

func (h *Handler) rejectBusy(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, source, targetID, threadID string) error {
	activity, _ := h.sessionActivity(ctx, msg.FromUserID, targetID, threadID)
	receipt := request.Rejection{Source: source, OwnerID: msg.FromUserID, TargetID: targetID, ThreadID: activity.ThreadID, TaskID: activity.TaskID, TurnID: activity.TurnID}
	if err := h.tasks.Reject(receipt); err != nil {
		return fmt.Errorf("保存拒绝回执失败：%w", err)
	}
	return h.showRejected(ctx, client, msg, receipt)
}

func (h *Handler) showRejected(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, receipt request.Rejection) error {
	if h.menus == nil {
		return h.sendBridgeNotice(ctx, client, msg.FromUserID, "提交失败：本会话正在执行。这条指令未提交，也不会稍后执行。回复 # 管理会话。", msg.ContextToken, NewClientID())
	}
	m := h.menus
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.session(msg.FromUserID)
	location := menuAction{kind: "session", targetID: receipt.TargetID, threadID: receipt.ThreadID}
	if err := m.show(ctx, msg.FromUserID, s, location); err != nil {
		// 读取状态失败也必须保留拒绝语义和可用的导航，不让新指令漏进执行入口。
		s.page = newNumberedPage("提交失败", thread.ShortCode(receipt.ThreadID))
		s.page.add(1, "刷新会话状态", "", location, false)
		s.page.add(3, "切换会话", "", menuAction{kind: "threads"}, false)
		s.location, s.closed, s.text, s.textOnly = location, false, "", false
	}
	s.page.Title = "提交失败"
	s.page.Notice = "收到指令时本会话正在执行。本条指令未提交，也不会稍后执行。请选择操作。"
	s.expires = m.now().Add(menuLifetime)
	return m.emit(ctx, client, msg, s)
}

// Interrupt 绑定具体请求或轮次，旧菜单不能打断同会话后来开始的新轮次。
func (r *Runtime) Interrupt(ctx context.Context, owner, targetID, threadID, taskID, turnID string) error {
	if r.Handler.remoteLock != nil && r.Handler.remoteLock.IsLocked(owner) {
		return fmt.Errorf("远程入口已锁定")
	}
	return r.Handler.conversations.Interrupt(ctx, owner, targetID, threadID, taskID, turnID)
}
