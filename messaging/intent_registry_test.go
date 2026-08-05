package messaging

import (
	"strings"
	"testing"
)

func TestDefaultIntentRegistryResolvesUniqueActions(t *testing.T) {
	registry, err := NewIntentRegistry(defaultIntentDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		text     string
		wantID   IntentID
		argument string
	}{
		{text: "状态？", wantID: IntentTaskStatus},
		{text: "任务中心", wantID: IntentTaskCenter},
		{text: "切换项目：weclaw", wantID: IntentProjectSelect, argument: "：weclaw"},
		{text: "切换会话 登录排障", wantID: IntentSessionSelect, argument: "登录排障"},
		{text: "搜索会话", wantID: IntentSessionSearch},
		{text: "恢复会话 登录", wantID: IntentSessionRestore, argument: "登录"},
		{text: "视觉风格 简洁", wantID: IntentVisualStyle, argument: "简洁"},
		{text: "发语音", wantID: IntentVoiceBriefing},
		{text: "codex信息", wantID: IntentRuntime},
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
}

func TestIntentRegistryRejectsPhraseAndPrefixConflicts(t *testing.T) {
	base := IntentDefinition{ID: "system.one", Domain: DomainSystem, ExactPhrases: []string{"状态"}, AuditEvent: "one"}
	tests := []struct {
		name        string
		definitions []IntentDefinition
		want        string
	}{
		{
			name: "normalized exact", want: "belongs to both",
			definitions: []IntentDefinition{base, {ID: "system.two", Domain: DomainSystem, ExactPhrases: []string{"状态？"}, AuditEvent: "two"}},
		},
		{
			name: "prefix captures exact", want: "captured by prefix",
			definitions: []IntentDefinition{base, {ID: "system.two", Domain: DomainSystem, ArgumentPrefixes: []string{"状"}, AuditEvent: "two"}},
		},
		{
			name: "overlapping prefixes", want: "overlapping",
			definitions: []IntentDefinition{
				{ID: "system.one", Domain: DomainSystem, ArgumentPrefixes: []string{"切换"}, AuditEvent: "one"},
				{ID: "system.two", Domain: DomainSystem, ArgumentPrefixes: []string{"切换项目"}, AuditEvent: "two"},
			},
		},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			if _, err := NewIntentRegistry(item.definitions); err == nil || !strings.Contains(err.Error(), item.want) {
				t.Fatalf("registry error = %v", err)
			}
		})
	}
}

func TestIntentMetadataDeclaresSafetyBoundaries(t *testing.T) {
	registry := mustDefaultIntentRegistry()
	sessionSelect, ok := registry.Definition(IntentSessionSelect)
	if !ok || !sessionSelect.MutatesState || sessionSelect.AllowDuringTask || !sessionSelect.AllowDuringDrain {
		t.Fatalf("session select metadata = %#v", sessionSelect)
	}
	voice, ok := registry.Definition(IntentVoiceBriefing)
	if !ok || !voice.RequiresContextToken || voice.MutatesState {
		t.Fatalf("voice metadata = %#v", voice)
	}
	lock, ok := registry.Definition(IntentRemoteLock)
	if !ok || !lock.MutatesState || !lock.AllowDuringTask {
		t.Fatalf("remote lock metadata = %#v", lock)
	}
}
