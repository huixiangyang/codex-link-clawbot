package wechat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
)

func TestHomeActivePreviewMergesSortsAndKeepsControls(t *testing.T) {
	f := newNumberMenuFixture(t)
	f.input(t, "#")
	if f.h.menus.session("owner").page.ActiveNotice != "暂无活跃会话" {
		t.Fatal("missing empty state")
	}
	local := f.enqueue(t, "本地请求摘要")
	now := local.StartedAt
	f.h.menus.now = func() time.Time { return time.Unix(now+300, 0) }
	root := f.h.projects.List()[0].Root
	ids := []string{}
	for i, title := range []string{"原生最新", "本地会话名称", "", "较早会话", "空闲会话", "归档会话", "越界会话"} {
		item, err := f.agent.StartThread(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		item.Name, item.Status.Type = title, "active"
		item.UpdatedAt = now - int64(i+1)*60
		if i == 0 {
			recent := now + 60
			item.RecencyAt = &recent
		}
		if i == 4 {
			item.Status.Type, item.UpdatedAt = "idle", now+120
		}
		if i == 6 {
			item.Cwd, item.UpdatedAt = t.TempDir(), now+180
		}
		f.agent.mu.Lock()
		f.agent.threads[item.ID] = item
		f.agent.archived[item.ID] = i == 5
		f.agent.mu.Unlock()
		ids = append(ids, item.ID)
	}
	if err := f.h.tasks.AttachThread("owner", local.ID, ids[1]); err != nil {
		t.Fatal(err)
	}
	if err := f.h.targets.Bind("owner", local.TargetID, ids[1]); err != nil {
		t.Fatal(err)
	}
	f.h.sessions.Invalidate()
	current := f.h.targets.Current("owner")
	f.input(t, "#")
	page := f.h.menus.session("owner").page
	if page.ActiveTitle != "活跃会话 · 4" || len(page.Active) != 3 || page.ActiveNotice != "另有 1 个 · #4 会话状态" {
		t.Fatalf("bad active preview: %+v", page)
	}
	for i, title := range []string{"原生最新", "本地会话名称", "测试线程 3"} {
		if page.Active[i].Title != title {
			t.Fatalf("preview is not sorted by merged activity time: %+v", page.Active)
		}
	}
	if page.Active[0].Time != "4 分钟前" || !page.Active[1].Current || !strings.Contains(page.Active[1].Detail, local.Stage) {
		t.Fatalf("lost activity time, target or local stage: %+v", page.Active)
	}
	if len(page.Options) != 6 || page.actions[1].kind != "new" || page.actions[4].kind != "running" || f.h.targets.Current("owner") != current {
		t.Fatal("preview changed controls or input target")
	}
	fallback := numberedMenuText(page.Menu)
	if strings.Index(fallback, "原生最新") > strings.Index(fallback, "#1 新建对话") || !strings.Contains(fallback, page.ActiveNotice) {
		t.Fatalf("text fallback lost preview order: %s", fallback)
	}
	f.input(t, "#4")
	page = f.h.menus.session("owner").page
	if len(page.Options) != 4 || page.actions[1].threadID != ids[0] || page.actions[2].threadID != ids[1] {
		t.Fatalf("running menu order differs from preview: %+v", page)
	}
}

type unavailableMenuCatalog struct{ codex.ThreadClient }

func (unavailableMenuCatalog) ListThreads(context.Context, codex.ThreadListOptions) (codex.ThreadPage, error) {
	return codex.ThreadPage{}, errors.New("offline")
}

func TestHomeActivePreviewHandlesUnavailableAndLockedStates(t *testing.T) {
	f := newNumberMenuFixture(t)
	local := f.enqueue(t, "本地准备任务")
	f.h.menus.codex = unavailableMenuCatalog{ThreadClient: f.agent}
	f.input(t, "#")
	page := f.h.menus.session("owner").page
	if len(page.Active) != 1 || page.Active[0].Title != local.Summary || !strings.Contains(page.ActiveTitle, "不完整") {
		t.Fatalf("local activity lost when catalog failed: %+v", page)
	}
	if _, err := f.h.tasks.Finish("owner", local.ID, request.StateCancelled, request.ReasonUserCancelled); err != nil {
		t.Fatal(err)
	}
	f.input(t, "#")
	page = f.h.menus.session("owner").page
	if len(page.Active) != 0 || strings.Contains(page.ActiveNotice, "暂无") || !strings.Contains(page.ActiveNotice, "暂不可查询") {
		t.Fatalf("unavailable catalog was presented as empty: %+v", page)
	}
	if err := f.h.remoteLock.Lock("owner"); err != nil {
		t.Fatal(err)
	}
	f.input(t, "#")
	page = f.h.menus.session("owner").page
	if page.ActiveTitle != "" || len(page.Active) != 0 || page.Title != "入口已锁定" {
		t.Fatalf("locked menu exposed activity: %+v", page)
	}
}
