package wechat

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/visual"
)

func (m *numberMenus) emit(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, s *menuSession) error {
	snapshot := *s
	snapshot.page.Options = append([]visual.MenuOption(nil), s.page.Options...)
	snapshot.page.Active = append([]visual.MenuPreview(nil), s.page.Active...)
	if s.text != "" && !s.closed && !s.textOnly {
		s.text = ""
	}
	if s.emitCancel != nil {
		s.emitCancel()
	}
	sendCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.emitCancel = cancel
	if m.h.coordinator == nil {
		defer cancel()
		return m.emitSnapshot(ctx, client, msg, &snapshot)
	}
	if !m.h.coordinator.Effect(func(life context.Context) {
		defer cancel()
		stop := context.AfterFunc(life, cancel)
		defer stop()
		if err := m.emitSnapshot(sendCtx, client, msg, &snapshot); err != nil && sendCtx.Err() == nil {
			log.Printf("[menu] output failed: %v", err)
		}
	}) {
		cancel()
		return fmt.Errorf("菜单发送繁忙，请重新发送 # 或 /")
	}
	return nil
}

func (m *numberMenus) emitSnapshot(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, s *menuSession) error {
	if s.text != "" {
		text := s.text
		if s.textOnly && s.page.Notice != "" {
			text = s.page.Notice + "\n\n" + text
		}
		if err := sendPlainTextReply(ctx, client, msg.FromUserID, text, msg.ContextToken, NewClientID()); err != nil {
			return err
		}
		if s.closed || s.textOnly {
			return nil
		}
		s.text = ""
	}
	// 正常只发一张图。渲染或投递失败时才发送同编号文字，不用两份菜单占屏。
	if m.h.visual != nil {
		artifact, err := m.h.visual.RenderMenu(ctx, s.page.Menu)
		if artifact != nil && artifact.Cleanup != nil {
			defer artifact.Cleanup()
		}
		if err == nil && artifact != nil && artifact.Path != "" {
			err = SendMediaFromPath(ctx, client, msg.FromUserID, artifact.Path, msg.ContextToken)
			if err == nil {
				return nil
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("[menu] falling back to numbered text: %v", err)
	}
	return sendPlainTextReply(ctx, client, msg.FromUserID, numberedMenuText(s.page.Menu), msg.ContextToken, NewClientID())
}

func numberedMenuText(menu visual.Menu) string {
	lines := []string{menu.Title, menu.Context}
	if menu.Page != "" {
		lines = append(lines, menu.Page)
	}
	if menu.Notice != "" {
		lines = append(lines, menu.Notice)
	}
	if menu.ActiveTitle != "" {
		lines = append(lines, "", menu.ActiveTitle)
		for _, preview := range menu.Active {
			title := preview.Title
			if preview.Current {
				title += "（当前）"
			}
			lines = append(lines, title+" · "+preview.Time, preview.Detail)
		}
		if menu.ActiveNotice != "" {
			lines = append(lines, menu.ActiveNotice)
		}
		lines = append(lines, "")
	}
	for _, option := range menu.Options {
		line := fmt.Sprintf("#%d %s", option.Number, option.Label)
		if option.Current {
			line += "（当前）"
		}
		if option.Detail != "" {
			line += " · " + option.Detail
		}
		lines = append(lines, line)
	}
	if menu.Previous {
		lines = append(lines, "#7 上一页")
	}
	if menu.Next {
		lines = append(lines, "#8 下一页")
	}
	return strings.Join(append(lines, "# 或 / 首页 · #9 退出", "输入 # 加数字；直接发工作内容会退出菜单并提交。"), "\n")
}
