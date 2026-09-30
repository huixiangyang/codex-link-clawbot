package bridge

import (
	"context"
	"errors"
	"fmt"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/thread"
)

func (m *numberMenus) execute(ctx context.Context, owner string, s *menuSession, action menuAction) error {
	if strings.HasPrefix(action.kind, "confirm-") {
		return m.show(ctx, owner, s, action)
	}
	switch action.kind {
	case "home", "running", "threads", "workspaces", "session", "results", "task", "settings", "styles", "unlock", "read":
		return m.show(ctx, owner, s, action)
	case "new", "select-workspace", "select-thread":
		if m.h.targets == nil || m.h.projects == nil {
			return fmt.Errorf("目标服务暂不可用")
		}
		definition, ok := m.h.projects.Get(action.workspaceID)
		if !ok {
			return fmt.Errorf("工作空间已不可用，请回复 0 重新选择")
		}
		var err error
		message := "已切换到「" + definition.Name + "」。"
		switch action.kind {
		case "new":
			_, err = m.h.targets.NewConversation(owner, definition.ID)
			message = "已在「" + definition.Name + "」准备新对话。"
		case "select-workspace":
			_, err = m.h.targets.Select(owner, definition.ID, nil)
		case "select-thread":
			if m.codex == nil || m.h.coordinator == nil {
				return fmt.Errorf("会话服务暂不可用")
			}
			_, err = m.h.targets.Select(owner, definition.ID, func() (string, error) {
				info, readErr := m.h.sessions.UseGlobalThread(ctx, owner, thread.Workspace{ID: definition.ID, Name: definition.Name, Root: definition.Root}, action.threadID, m.codex)
				return info.ID, readErr
			})
			message += "会话「" + action.value + "」(" + thread.ShortCode(action.threadID) + ")。"
		}
		if err != nil {
			return fmt.Errorf("目标切换失败，原目标保持不变，请重试")
		}
		s.closed, s.text = true, message+"直接发送工作内容即可。回复 0 打开菜单。"
		return nil
	case "cancel":
		if m.recovery == nil {
			return fmt.Errorf("会话服务暂不可用")
		}
		if action.taskID == "" {
			if err := m.background(owner, func(life context.Context) error {
				return m.recovery.Interrupt(life, owner, action.targetID, action.threadID, "", action.turnID)
			}, "本次执行已确认结束。回复 0，再选 4 查看会话。"); err != nil {
				return err
			}
			s.page = newNumberedPage("正在确认打断", thread.ShortCode(action.threadID))
			s.page.Notice = "正在确认本次轮次是否停止，可以继续切换或管理会话。"
			s.page.add(1, "刷新会话状态", "", menuAction{kind: "session", targetID: action.targetID, threadID: action.threadID}, false)
			s.page.add(3, "切换会话", "", menuAction{kind: "threads"}, false)
			return nil
		}
		if err := m.recovery.Interrupt(ctx, owner, action.targetID, action.threadID, action.taskID, action.turnID); err != nil {
			return err
		}
		if err := m.show(ctx, owner, s, menuAction{kind: "session", targetID: action.targetID, threadID: action.threadID}); err != nil {
			return err
		}
		s.page.Notice = "已发出打断请求。结束后请重新发送工作内容。"
		return nil
	case "retry":
		if m.recovery == nil {
			return fmt.Errorf("重新执行服务暂不可用")
		}
		retried, err := m.recovery.Retry(ctx, owner, action.taskID, action.operationID)
		if err != nil {
			if errors.Is(err, request.ErrSessionBusy) || errors.Is(err, request.ErrRejected) {
				task, _ := m.h.tasks.Find(owner, action.taskID)
				_ = m.show(ctx, owner, s, menuAction{kind: "session", targetID: task.TargetID, threadID: task.ThreadID})
				s.page.Title = "提交失败"
			} else {
				_ = m.show(ctx, owner, s, menuAction{kind: "task", taskID: action.taskID})
			}
			return fmt.Errorf("未能重新执行：%w", err)
		}
		if err := m.show(ctx, owner, s, menuAction{kind: "session", targetID: retried.TargetID, threadID: retried.ThreadID}); err != nil {
			return err
		}
		s.page.Notice = "已开始重新执行。"
		return nil
	case "redeliver":
		if m.recovery == nil {
			return fmt.Errorf("发送服务暂不可用")
		}
		if !m.h.coordinator.Effect(func(life context.Context) {
			err := m.recovery.Redeliver(life, owner, action.taskID, action.operationID)
			message := "结果已重新发送。回复 0，再选 5 查看回执。"
			if err != nil {
				message = "重发未确认成功。回复 0，再选 5 查看回执；没有重新执行。"
			}
			client, ok := m.h.clients.load(owner)
			token, tokenErr := m.h.clients.context(owner)
			if ok && tokenErr == nil {
				_ = SendTextReply(life, client, owner, message, token, NewClientID())
			}
		}) {
			return fmt.Errorf("发送繁忙，请稍后重新选择")
		}
		if err := m.show(ctx, owner, s, menuAction{kind: "task", taskID: action.taskID}); err != nil {
			return err
		}
		s.page.Notice = "正在重新发送，仍可管理或切换会话。"
		return nil
	case "restore-result":
		if err := m.background(owner, func(life context.Context) error { return m.recovery.RestoreResult(life, owner, action.taskID) }, "结果已恢复保存。回复 0，再选 5 取回。"); err != nil {
			return err
		}
		s.page.Notice = "正在恢复保存，不会再次执行。稍后刷新详情。"
		return nil
	case "release":
		if err := m.h.tasks.Release(owner, action.taskID); err != nil {
			return err
		}
		if err := m.show(ctx, owner, s, menuAction{kind: "results"}); err != nil {
			return err
		}
		s.page.Notice = "记录与内容已清理。执行来源回执保留，不会重复执行。"
		return nil

	case "mode":
		mode := presentation.ResponseMode(action.value)
		if mode == presentation.ResponseReading && m.h.visual == nil || mode == presentation.ResponseVoice && m.h.voice == nil {
			return fmt.Errorf("当前回复方式不可用，请重新选择")
		}
		if err := m.h.preferences.SetResponseMode(owner, mode); err != nil {
			return fmt.Errorf("回复设置保存失败，请重试")
		}
		if err := m.show(ctx, owner, s, menuAction{kind: "settings"}); err != nil {
			return err
		}
		s.page.Notice = "已保存，之后提交的请求使用新设置。"
		return nil
	case "style":
		if err := m.h.preferences.SetStyle(owner, presentation.Style(action.value)); err != nil {
			return fmt.Errorf("图片风格保存失败，请重试")
		}
		if err := m.show(ctx, owner, s, menuAction{kind: "styles"}); err != nil {
			return err
		}
		s.page.Notice = "已保存回答图片风格。数字菜单保持大字排版。"
		return nil
	case "lock":
		if m.h.remoteLock == nil || m.h.remoteLock.Lock(owner) != nil {
			return fmt.Errorf("入口未能锁定，请检查锁定配置")
		}
		return m.show(ctx, owner, s, menuAction{kind: "home"})
	case "console":
		address := strings.TrimSpace(m.h.managementURL)
		if address == "" {
			return fmt.Errorf("尚未配置网页地址；常用操作可直接在数字菜单完成")
		}
		s.text = "网页工作台：\n" + address + "\n如果地址是 localhost 或 127.0.0.1，请在部署电脑打开。"
		return nil
	}
	return fmt.Errorf("操作不可用，请回复 0 返回首页")
}

// 慢速外部操作独立执行，数字菜单仍能实时接收；不保存待执行指令。
func (m *numberMenus) background(owner string, run func(context.Context) error, success string) error {
	if !m.h.coordinator.Effect(func(life context.Context) {
		ctx, cancel := context.WithTimeout(life, 30*time.Second)
		defer cancel()
		message := success
		if err := run(ctx); err != nil {
			message = "操作未完成：" + err.Error() + "。回复 0 查看状态。"
		}
		client, ok := m.h.clients.load(owner)
		token, err := m.h.clients.context(owner)
		if ok && err == nil {
			_ = SendTextReply(ctx, client, owner, message, token, NewClientID())
		}
	}) {
		return fmt.Errorf("操作繁忙，请稍后重新选择")
	}
	return nil
}
