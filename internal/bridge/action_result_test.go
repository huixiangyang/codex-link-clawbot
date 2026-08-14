package bridge

import (
	"context"
	"encoding/json"
	"github.com/huixiangyang/codex-link-clawbot/internal/control"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
)

func TestActionResultValidationRejectsAmbiguousOutput(t *testing.T) {
	tests := []struct {
		name   string
		result ActionResult
		valid  bool
	}{
		{name: "text", result: newActionResult("system.menu", control.DomainSystem, "菜单"), valid: true},
		{name: "prompt effect", result: effectActionResult("workspace.quick", control.DomainProject, "", EffectEnqueuePrompt, "检查改动"), valid: true},
		{name: "project prompt effect", result: effectActionResult("workspace.quick", control.DomainProject, "", EffectEnqueuePrompt, "检查改动").withProjectID("alpha"), valid: true},
		{name: "thread prompt effect", result: effectActionResult("queue.rerun", control.DomainQueue, "", EffectEnqueuePrompt, "检查改动").withProjectID("alpha").withThreadID("thread-1"), valid: true},
		{name: "new thread prompt effect", result: effectActionResult("queue.rerun", control.DomainQueue, "", EffectEnqueuePrompt, "检查改动").withProjectID("alpha").withNewThread(), valid: true},
		{name: "media effect", result: effectActionResult("delivery.resend", control.DomainDelivery, "已发送", EffectSendMedia, "/tmp/result.png"), valid: true},
		{name: "missing identity", result: newActionResult("", control.DomainSystem, "菜单")},
		{name: "unknown domain", result: newActionResult("system.menu", "unknown", "菜单")},
		{name: "empty text", result: newActionResult("system.menu", control.DomainSystem, "")},
		{name: "empty effect value", result: effectActionResult("workspace.quick", control.DomainProject, "", EffectEnqueuePrompt, "")},
		{name: "media without receipt", result: effectActionResult("delivery.resend", control.DomainDelivery, "", EffectSendMedia, "/tmp/result.png")},
		{name: "voice with hidden value", result: effectActionResult("result.voice", control.DomainQueue, "生成中", EffectVoiceBriefing, "unexpected")},
		{name: "project on text", result: newActionResult("system.menu", control.DomainSystem, "菜单").withProjectID("alpha")},
		{name: "invalid project", result: effectActionResult("workspace.quick", control.DomainProject, "", EffectEnqueuePrompt, "检查改动").withProjectID("Alpha")},
		{name: "thread on text", result: newActionResult("system.menu", control.DomainSystem, "菜单").withThreadID("thread-1")},
		{name: "thread without project", result: effectActionResult("queue.rerun", control.DomainQueue, "", EffectEnqueuePrompt, "检查改动").withThreadID("thread-1")},
		{name: "thread conflicts with new", result: effectActionResult("queue.rerun", control.DomainQueue, "", EffectEnqueuePrompt, "检查改动").withNewThread().withThreadID("thread-1")},
		{name: "rollback on text", result: ActionResult{ActionID: "system.menu", Domain: control.DomainSystem, Text: "菜单", rollback: &controlReceiptRollback{}}},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			err := item.result.validate()
			if (err == nil) != item.valid {
				t.Fatalf("validate() error = %v, valid=%v", err, item.valid)
			}
		})
	}
}

func TestPresenterIsTheOnlyTextDeliveryBoundary(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(response, request)
			return
		}
		requests++
		_ = json.NewEncoder(response).Encode(map[string]any{"ret": 0})
	}))
	defer server.Close()

	client := ilink.NewClient(&ilink.Credentials{
		BotToken: "token", ILinkBotID: "bot", ILinkUserID: "owner-1", BaseURL: server.URL,
	})
	handler := newBareHandler(nil)
	message := ilink.WeixinMessage{FromUserID: "owner-1", ContextToken: "context"}
	if err := handler.presentActionResult(context.Background(), client, message, newActionResult("system.guide", control.DomainSystem, "使用说明"), "client-1"); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("text delivery requests = %d", requests)
	}

	err := handler.presentActionResult(context.Background(), client, message, ActionResult{ActionID: "broken", Domain: control.DomainSystem}, "client-2")
	if err == nil || !strings.Contains(err.Error(), "invalid action result") {
		t.Fatalf("invalid result error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("invalid result reached WeChat: requests=%d", requests)
	}
}

func TestAttachmentIntentRequiresExplicitPermission(t *testing.T) {
	handler := newBareHandler(nil)
	if result, handled := handler.handleControlInput(context.Background(), "owner-1", "菜单", true, nextTestControlSource()); handled || result != (ActionResult{}) {
		t.Fatalf("default attachment intent = %#v, handled=%v", result, handled)
	}

	definition, ok := handler.intents.Definition(control.IntentMenu)
	if !ok {
		t.Fatal("menu intent is missing")
	}
	definition.AllowAttachments = true
	registry, err := control.NewRegistry([]control.Definition{definition})
	if err != nil {
		t.Fatal(err)
	}
	handler.intents = registry
	result, handled := handler.handleControlInput(context.Background(), "owner-1", "菜单", true, nextTestControlSource())
	if !handled || result.ActionID != string(control.IntentMenu) {
		t.Fatalf("allowed attachment intent = %#v, handled=%v", result, handled)
	}
}
