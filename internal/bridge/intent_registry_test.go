package bridge

import (
	"github.com/huixiangyang/codex-link-clawbot/internal/control"
	"strings"
	"testing"
)

func TestDefaultIntentRegistryResolvesUniqueActions(t *testing.T) {
	registry, err := control.NewRegistry(control.DefaultDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		text     string
		wantID   control.ID
		argument string
	}{
		{text: "状态？", wantID: control.IntentTaskStatus},
		{text: "请求队列", wantID: control.IntentTaskCenter},
		{text: "切换项目：codex-link-clawbot", wantID: control.IntentProjectSelect, argument: "：codex-link-clawbot"},
		{text: "切换线程 登录排障", wantID: control.IntentSessionSelect, argument: "登录排障"},
		{text: "搜索线程", wantID: control.IntentSessionSearch},
		{text: "恢复线程 登录", wantID: control.IntentSessionRestore, argument: "登录"},
		{text: "视觉风格 简洁", wantID: control.IntentVisualStyle, argument: "简洁"},
		{text: "发语音", wantID: control.IntentVoiceBriefing},
		{text: "codex信息", wantID: control.IntentRuntime},
		{text: "为什么没回复？", wantID: control.IntentNoReplyDiagnostic},
	}
	for _, item := range tests {
		resolved, ok := registry.Resolve(item.text)
		if !ok || resolved.Definition.ID != item.wantID || resolved.Argument != item.argument {
			t.Fatalf("Resolve(%q) = %#v, %v", item.text, resolved, ok)
		}
	}
	if _, ok := registry.Resolve("请检查项目测试"); ok {
		t.Fatal("ordinary Codex prompt was captured by the control registry")
	}
	if _, ok := registry.Resolve("轮次队列"); ok {
		t.Fatal("legacy queue phrase must not remain as a compatibility alias")
	}
}

func TestIntentRegistryRejectsPhraseAndPrefixConflicts(t *testing.T) {
	base := control.Definition{ID: "system.one", Domain: control.DomainSystem, ExactPhrases: []string{"状态"}, AuditEvent: "one"}
	tests := []struct {
		name        string
		definitions []control.Definition
		want        string
	}{
		{
			name: "normalized exact", want: "belongs to both",
			definitions: []control.Definition{base, {ID: "system.two", Domain: control.DomainSystem, ExactPhrases: []string{"状态？"}, AuditEvent: "two"}},
		},
		{
			name: "prefix captures exact", want: "captured by prefix",
			definitions: []control.Definition{base, {ID: "system.two", Domain: control.DomainSystem, ArgumentPrefixes: []string{"状"}, AuditEvent: "two"}},
		},
		{
			name: "overlapping prefixes", want: "overlapping",
			definitions: []control.Definition{
				{ID: "system.one", Domain: control.DomainSystem, ArgumentPrefixes: []string{"切换"}, AuditEvent: "one"},
				{ID: "system.two", Domain: control.DomainSystem, ArgumentPrefixes: []string{"切换项目"}, AuditEvent: "two"},
			},
		},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			if _, err := control.NewRegistry(item.definitions); err == nil || !strings.Contains(err.Error(), item.want) {
				t.Fatalf("registry error = %v", err)
			}
		})
	}
}

func TestIntentMetadataDeclaresSafetyBoundaries(t *testing.T) {
	registry := mustDefaultIntentRegistry()
	queue, ok := registry.Definition(control.IntentTaskCenter)
	if !ok || queue.Domain != control.DomainQueue {
		t.Fatalf("queue intent metadata = %#v", queue)
	}
	turnSteer, ok := registry.Definition(control.IntentTurnSteer)
	if !ok || turnSteer.Domain != control.DomainSession || !strings.HasPrefix(string(turnSteer.ID), "turn.") {
		t.Fatalf("Codex turn intent metadata = %#v", turnSteer)
	}
	sessionSelect, ok := registry.Definition(control.IntentSessionSelect)
	if !ok || !sessionSelect.MutatesState || !sessionSelect.RequiresReceipt || sessionSelect.AllowDuringTask || !sessionSelect.AllowDuringDrain {
		t.Fatalf("session select metadata = %#v", sessionSelect)
	}
	voice, ok := registry.Definition(control.IntentVoiceBriefing)
	if !ok || !voice.RequiresContextToken || voice.MutatesState {
		t.Fatalf("voice metadata = %#v", voice)
	}
	lock, ok := registry.Definition(control.IntentRemoteLock)
	if !ok || !lock.MutatesState || !lock.AllowDuringTask {
		t.Fatalf("remote lock metadata = %#v", lock)
	}
}

func TestIntentRegistryRejectsMutationWithoutReceipt(t *testing.T) {
	_, err := control.NewRegistry([]control.Definition{{
		ID: "system.unsafe", Domain: control.DomainSystem, ExactPhrases: []string{"危险操作"},
		MutatesState: true, AuditEvent: "unsafe",
	}})
	if err == nil || !strings.Contains(err.Error(), "idempotency receipt") {
		t.Fatalf("registry error = %v", err)
	}
}

func TestReceiptIdentityCoversEveryDeclaredIntent(t *testing.T) {
	for _, definition := range control.DefaultDefinitions() {
		if definition.RequiresReceipt && !validControlReceiptIdentity(string(definition.ID), definition.Domain) {
			t.Fatalf("receipt identity missing for %s", definition.ID)
		}
	}
}
