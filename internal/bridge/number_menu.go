package bridge

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

const menuLifetime = 10 * time.Minute
const menuPageSize = 4

type menuAction struct {
	targetID, turnID string
	kind             string
	page             int
	workspaceID      string
	threadID         string
	taskID           string
	value            string
	operationID      string
}

type numberedPage struct {
	visual.Menu
	actions map[int]menuAction
}

func newNumberedPage(title, context string) numberedPage {
	return numberedPage{Menu: visual.Menu{Title: title, Context: context}, actions: make(map[int]menuAction)}
}
func (p *numberedPage) add(number int, label, detail string, action menuAction, current bool) {
	p.Options = append(p.Options, visual.MenuOption{Number: number, Label: label, Detail: detail, Current: current})
	sort.Slice(p.Options, func(i, j int) bool { return p.Options[i].Number < p.Options[j].Number })
	p.actions[number] = action
}

type menuSession struct {
	emitCancel context.CancelFunc
	page       numberedPage
	location   menuAction
	expires    time.Time
	closed     bool
	text       string
	textOnly   bool
}

// numberMenus 串行处理菜单输入。数字绑定展示时的 ID，不能临时按列表下标查询。
// 状态只在本次进程中有效；重启后的旧数字必须先回首页，不会重新执行动作。
type numberMenus struct {
	mu       sync.Mutex
	h        *Handler
	codex    codex.ThreadClient
	recovery *Runtime
	owners   map[string]*menuSession
	receipts *menuReceipts
	now      func() time.Time
}

func newNumberMenus(h *Handler, client codex.ThreadClient, recovery *Runtime) *numberMenus {
	return &numberMenus{h: h, codex: client, recovery: recovery, owners: make(map[string]*menuSession), now: time.Now}
}

func (m *numberMenus) session(owner string) *menuSession {
	s := m.owners[owner]
	if s == nil {
		s = &menuSession{}
		m.owners[owner] = s
	}
	return s
}
func (m *numberMenus) leave(owner string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.session(owner)
	if s.emitCancel != nil {
		s.emitCancel()
	}
	s.closed, s.text = true, "菜单已退出。直接发送内容继续工作，回复 0 打开菜单。"
}

func normalizeMenuNumber(text string) (string, bool) {
	text = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r >= '０' && r <= '９' {
			return r - '０' + '0'
		}
		return r
	}, text))
	if text == "" {
		return text, false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return text, false
		}
	}
	return text, true
}

func (m *numberMenus) handle(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, input string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.session(msg.FromUserID)
	text, numeric := normalizeMenuNumber(input)
	source, _ := sourceMessageKey(client, msg)
	open := isCodexLinkMenu(input) || text == "0"
	unlock := !s.closed && s.location.kind == "unlock"
	if err := m.loadReceipts(); err != nil {
		return true, err
	}
	if source != "" && m.receipts.contains(msg.FromUserID, source) {
		if !s.closed && !m.now().Before(s.expires) {
			if err := m.show(ctx, msg.FromUserID, s, menuAction{kind: "home"}); err != nil {
				return true, err
			}
			s.expires = m.now().Add(menuLifetime)
			s.page.Notice = "菜单已过期，已返回首页。"
		}
		// 重投只展示当前页面，既不重复副作用，也不把旧页面显示成当前编号。
		return true, m.emit(ctx, client, msg, s)
	}
	if !open && !unlock && (!numeric || s.closed) {
		return false, nil
	}
	// 先记录已接收的菜单输入，再执行副作用。崩溃或整批微信重投不能重复操作。
	if source != "" {
		if err := m.recordReceipt(msg.FromUserID, source); err != nil {
			return true, err
		}
	}
	if open {
		if err := m.show(ctx, msg.FromUserID, s, menuAction{kind: "home"}); err != nil {
			return true, err
		}
	} else if s.expires.IsZero() || !m.now().Before(s.expires) {
		if err := m.show(ctx, msg.FromUserID, s, menuAction{kind: "home"}); err != nil {
			return true, err
		}
		s.page.Notice = "菜单已刷新，请按当前页面的数字重新选择。"
	} else if text == "9" {
		s.closed, s.text = true, "菜单已退出。直接发送内容继续工作，回复 0 打开菜单。"
	} else if source == "" {
		s.page.Notice = "这条消息缺少编号，请重新发送 0 打开菜单。"
	} else if unlock {
		if err := m.h.remoteLock.Unlock(msg.FromUserID, input); err != nil {
			s.page.Notice = "解锁码不正确，请重新输入。0 返回首页，9 退出。"
		} else {
			if err := m.show(ctx, msg.FromUserID, s, menuAction{kind: "home"}); err != nil {
				return true, err
			}
			s.page.Notice = "已解锁，可以继续工作。"
		}
	} else if m.locked(msg.FromUserID) && s.location.kind != "locked" {
		if err := m.show(ctx, msg.FromUserID, s, menuAction{kind: "home"}); err != nil {
			return true, err
		}
	} else {
		number, err := strconv.Atoi(text)
		action, ok := s.page.actions[number]
		if err != nil || len(text) != 1 || !ok {
			s.page.Notice = "请选择本页显示的单个数字。0 首页，9 退出。"
		} else if err := m.execute(ctx, msg.FromUserID, s, action); err != nil {
			s.page.Notice = err.Error()
		}
	}
	s.expires = m.now().Add(menuLifetime)
	return true, m.emit(ctx, client, msg, s)
}

func (m *numberMenus) locked(owner string) bool {
	return m.h.remoteLock != nil && m.h.remoteLock.IsLocked(owner)
}

func (m *numberMenus) emit(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, s *menuSession) error {
	snapshot := *s
	snapshot.page.Options = append([]visual.MenuOption(nil), s.page.Options...)
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
		return fmt.Errorf("菜单发送繁忙，请重新发送 0")
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
	for _, option := range menu.Options {
		line := fmt.Sprintf("%d %s", option.Number, option.Label)
		if option.Current {
			line += "（当前）"
		}
		if option.Detail != "" {
			line += " · " + option.Detail
		}
		lines = append(lines, line)
	}
	if menu.Previous {
		lines = append(lines, "7 上一页")
	}
	if menu.Next {
		lines = append(lines, "8 下一页")
	}
	return strings.Join(append(lines, "0 首页 · 9 退出", "输入一个数字；直接发工作内容会退出菜单并提交。"), "\n")
}
