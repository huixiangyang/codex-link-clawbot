package messaging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huixiangyang/weclaw/internal/preference"
	"github.com/huixiangyang/weclaw/internal/visual"
)

func TestVisualStyleMenuSwitchesPersistsAndIsolatesOwners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := preference.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHandler(t)
	handler.SetVisualRenderer(&fakeControlVisualRenderer{})
	handler.SetPreferenceStore(store)
	mainMenu := handler.openMoreMenu("owner-1")
	if !strings.Contains(mainMenu, "偏好设置") {
		t.Fatalf("main menu missing preferences: %q", mainMenu)
	}

	menu, handled := handler.handleControlInput(context.Background(), "owner-1", "视觉风格", false, nextTestControlSource())
	if !handled {
		t.Fatal("visual style menu was not handled")
	}
	for _, want := range []string{"当前：构筑", "1  刊物", "2  构筑", "3  黑标", "4  可爱", "5  简洁"} {
		if !strings.Contains(menu.Text, want) {
			t.Fatalf("style menu missing %q: %q", want, menu.Text)
		}
	}

	switched, handled := handler.handleControlInput(context.Background(), "owner-1", "4", false, nextTestControlSource())
	if !handled || !strings.Contains(switched.Text, "视觉风格已切换") || !strings.Contains(switched.Text, "当前：可爱") {
		t.Fatalf("style switch = %q, handled=%v", switched.Text, handled)
	}
	if got := store.Get("owner-1").Style; got != visual.StyleCute {
		t.Fatalf("owner-1 style = %q", got)
	}
	if got := store.Get("owner-2").Style; got != visual.DefaultStyle {
		t.Fatalf("owner-2 style = %q", got)
	}

	direct, handled := handler.handleControlInput(context.Background(), "owner-1", "切换风格 简洁", false, nextTestControlSource())
	if !handled || !strings.Contains(direct.Text, "当前：简洁") || store.Get("owner-1").Style != visual.StyleMinimal {
		t.Fatalf("direct style switch = %q, handled=%v style=%q", direct.Text, handled, store.Get("owner-1").Style)
	}
	reloaded, err := preference.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get("owner-1").Style; got != visual.StyleMinimal {
		t.Fatalf("reloaded style = %q", got)
	}

	invalid, handled := handler.handleControlInput(context.Background(), "owner-1", "视觉风格 霓虹", false, nextTestControlSource())
	if !handled || !strings.Contains(invalid.Text, "没有这个视觉风格") || store.Get("owner-1").Style != visual.StyleMinimal {
		t.Fatalf("invalid style = %q, handled=%v", invalid.Text, handled)
	}
}

func TestVisualStyleMenuRequiresVisualRendererAndStore(t *testing.T) {
	handler := newTestHandler(t)
	reply, handled := handler.handleControlInput(context.Background(), "owner-1", "视觉风格", false, nextTestControlSource())
	if !handled || reply.Text != "视觉卡片当前不可用。" {
		t.Fatalf("unavailable style menu = %q, handled=%v", reply.Text, handled)
	}
}
