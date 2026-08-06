package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/internal/statefile"
)

func TestStoreCRUDPersistsAndIsolatesOwnerAndProject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflows.json")
	clock := time.Unix(1_800_000_000, 0)
	store, err := newStore(path, []string{"alpha", "beta"}, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(CreateInput{
		OwnerID: "owner-a", ProjectID: "alpha", Name: "发布检查",
		PromptTemplate: "检查 {{branch}} 分支并输出 {{format}} 报告",
		Slots:          []Slot{{Key: "branch", Label: "分支"}, {Key: "format", Label: "格式"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.List("owner-a", "alpha")) != 1 || len(store.List("owner-a", "beta")) != 0 || len(store.List("owner-b", "alpha")) != 0 {
		t.Fatalf("workflow isolation failed: %#v", store.state)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("workflow state permissions = %v, %v", info, err)
	}
	clock = clock.Add(time.Minute)
	updated, err := store.Update("owner-a", "alpha", created.ID, UpdateInput{
		Name: "发布前检查", PromptTemplate: "检查 {{branch}} 分支", Slots: []Slot{{Key: "branch", Label: "分支"}},
	})
	if err != nil || updated.UpdatedAt <= created.UpdatedAt {
		t.Fatalf("Update() = %#v, %v", updated, err)
	}
	reopened, err := NewStore(path, []string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if found, ok := reopened.Find("owner-a", "alpha", created.ID); !ok || found.Name != "发布前检查" {
		t.Fatalf("Find() = %#v, %v", found, ok)
	}
	if err := reopened.Delete("owner-a", "alpha", created.ID); err != nil || len(reopened.List("owner-a", "alpha")) != 0 {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestStoreWriteFailureKeepsOldMemoryAndDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflows.json")
	store, err := NewStore(path, []string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(CreateInput{
		OwnerID: "owner", ProjectID: "alpha", Name: "原名称", PromptTemplate: "原内容", Slots: []Slot{},
	})
	if err != nil {
		t.Fatal(err)
	}
	store.fault = func(point statefile.FaultPoint) error {
		if point == statefile.FaultRename {
			return errors.New("injected rename failure")
		}
		return nil
	}
	if _, err := store.Update("owner", "alpha", created.ID, UpdateInput{Name: "新名称", PromptTemplate: "新内容", Slots: []Slot{}}); err == nil {
		t.Fatal("injected update failure was ignored")
	}
	if err := store.Delete("owner", "alpha", created.ID); err == nil {
		t.Fatal("injected delete failure was ignored")
	}
	found, ok := store.Find("owner", "alpha", created.ID)
	if !ok || found.Name != "原名称" || found.PromptTemplate != "原内容" {
		t.Fatalf("memory changed after failed write: %#v, %v", found, ok)
	}
	store.fault = nil
	reopened, err := NewStore(path, []string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	found, ok = reopened.Find("owner", "alpha", created.ID)
	if !ok || found.Name != "原名称" || found.PromptTemplate != "原内容" {
		t.Fatalf("disk changed after failed write: %#v, %v", found, ok)
	}
}

func TestRenderRequiresExactDeclaredSlots(t *testing.T) {
	definition := Definition{
		ID: "workflow-0123456789abcdef0123456789abcdef", ProjectID: "alpha", Name: "检查",
		PromptTemplate: "检查 {{branch}}，输出 {{format}}", CreatedAt: 1, UpdatedAt: 1,
		Slots: []Slot{{Key: "branch", Label: "分支"}, {Key: "format", Label: "格式"}},
	}
	got, err := Render(definition, map[string]string{"branch": "main", "format": "简报"})
	if err != nil || got != "检查 main，输出 简报" {
		t.Fatalf("Render() = %q, %v", got, err)
	}
	if _, err := Render(definition, map[string]string{"branch": "main"}); err == nil {
		t.Fatal("missing slot value was accepted")
	}
	definition.PromptTemplate = "检查 {{unknown}}"
	if _, err := Render(definition, map[string]string{"branch": "main", "format": "简报"}); err == nil {
		t.Fatal("undeclared placeholder was accepted")
	}
}

func TestStoreRejectsUnknownSchemaAndProject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflows.json")
	store, err := NewStore(path, []string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Create(CreateInput{
		OwnerID: "owner", ProjectID: "beta", Name: "错误项目", PromptTemplate: "检查", Slots: []Slot{},
	})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unknown project error = %v", err)
	}
	unknownPath := filepath.Join(t.TempDir(), "unknown.json")
	if err := os.WriteFile(unknownPath, []byte(`{"version":1,"owners":{},"extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(unknownPath, []string{"alpha"}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown workflow schema error = %v", err)
	}
}

func TestImportIsIdempotentAndNeverOverwrites(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "workflows.json"), []string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	definition := Definition{
		ID: StableImportID("alpha", "review"), ProjectID: "alpha", Name: "审查改动",
		PromptTemplate: "审查当前改动", Slots: []Slot{}, CreatedAt: 1, UpdatedAt: 1,
	}
	if imported, err := store.Import("owner", definition); err != nil || !imported {
		t.Fatalf("first Import() = %v, %v", imported, err)
	}
	if imported, err := store.Import("owner", definition); err != nil || imported {
		t.Fatalf("second Import() = %v, %v", imported, err)
	}
	definition.PromptTemplate = "覆盖用户内容"
	if _, err := store.Import("owner", definition); err == nil {
		t.Fatal("conflicting import overwrote workflow")
	}
}
