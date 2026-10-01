package wechat

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
)

var resultCodePattern = regexp.MustCompile(`^(?:[a-f0-9]{8}|task-[a-f0-9]{32})$`)

func isConversationCommand(text string) bool {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "停止", "取消":
		return len(parts) == 1
	case "全文", "文件", "继续会话", "重发":
		return len(parts) == 2 && resultCodePattern.MatchString(parts[1])
	}
	return false
}

func (m *numberMenus) conversationCommand(ctx context.Context, owner string, s *menuSession, input string) error {
	parts := strings.Fields(input)
	if parts[0] == "停止" || parts[0] == "取消" {
		intent, err := m.h.targets.Capture(owner)
		if err != nil {
			return err
		}
		activity, err := m.h.sessionActivity(ctx, owner, intent.ID, intent.ThreadID)
		if err != nil {
			return m.show(ctx, owner, s, menuAction{kind: "unavailable", workspaceID: intent.WorkspaceID, targetID: intent.ID, threadID: intent.ThreadID})
		}
		if !activity.CanInterrupt {
			s.closed, s.text = true, "当前会话没有可打断的执行；没有打断其他会话。发送“菜单”查看运行状态。"
			return nil
		}
		// 用户显式停止当前会话，立即绑定观察到的请求或轮次，不追随后续目标切换。
		return m.execute(ctx, owner, s, menuAction{kind: "cancel", targetID: activity.TargetID, threadID: activity.ThreadID, taskID: activity.TaskID, turnID: activity.TurnID})
	}
	var found []request.Task
	for _, task := range m.h.tasks.List(owner) {
		if task.ID == parts[1] || shortTaskID(task.ID) == parts[1] {
			found = append(found, task)
		}
	}
	if len(found) != 1 {
		return fmt.Errorf("结果编号不存在或不唯一，请发送“菜单”，选择 #5 核对。")
	}
	task := found[0]
	switch parts[0] {
	case "全文":
		return m.show(ctx, owner, s, menuAction{kind: "read", taskID: task.ID})
	case "文件":
		return m.execute(ctx, owner, s, menuAction{kind: "files", taskID: task.ID, operationID: uuid.NewString()})
	case "重发":
		return m.show(ctx, owner, s, menuAction{kind: "confirm-redeliver", taskID: task.ID})
	case "继续会话":
		if task.ThreadID == "" {
			return fmt.Errorf("该请求没有已建立的会话，请从菜单新建。")
		}
		return m.execute(ctx, owner, s, menuAction{kind: "select-thread", workspaceID: task.ProjectID, threadID: task.ThreadID, value: task.Summary})
	}
	return nil
}

func (h *Handler) showUnavailable(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, intent target.Intent) error {
	if h.menus == nil {
		return SendTextReply(ctx, client, msg.FromUserID, "原会话暂不可访问，本条未提交。请发送“菜单”刷新、切换或新建会话；不会自动换目标。", msg.ContextToken, NewClientID())
	}
	m := h.menus
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.session(msg.FromUserID)
	if err := m.show(ctx, msg.FromUserID, s, menuAction{kind: "unavailable", workspaceID: intent.WorkspaceID, targetID: intent.ID, threadID: intent.ThreadID}); err != nil {
		return err
	}
	s.expires = m.now().Add(menuLifetime)
	return m.emit(ctx, client, msg, s)
}
