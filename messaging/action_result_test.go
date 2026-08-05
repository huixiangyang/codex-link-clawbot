package messaging

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huixiangyang/weclaw/ilink"
)

func TestActionResultValidationRejectsAmbiguousOutput(t *testing.T) {
	tests := []struct {
		name   string
		result ActionResult
		valid  bool
	}{
		{name: "text", result: newActionResult("system.menu", DomainSystem, "菜单"), valid: true},
		{name: "prompt effect", result: effectActionResult("project.quick", DomainProject, "", EffectEnqueuePrompt, "检查改动"), valid: true},
		{name: "media effect", result: effectActionResult("library.resend", DomainLibrary, "已发送", EffectSendMedia, "/tmp/result.png"), valid: true},
		{name: "missing identity", result: newActionResult("", DomainSystem, "菜单")},
		{name: "unknown domain", result: newActionResult("system.menu", "unknown", "菜单")},
		{name: "empty text", result: newActionResult("system.menu", DomainSystem, "")},
		{name: "empty effect value", result: effectActionResult("project.quick", DomainProject, "", EffectEnqueuePrompt, "")},
		{name: "media without receipt", result: effectActionResult("library.resend", DomainLibrary, "", EffectSendMedia, "/tmp/result.png")},
		{name: "voice with hidden value", result: effectActionResult("automation.voice", DomainAutomation, "生成中", EffectVoiceBriefing, "unexpected")},
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
	handler := NewHandler(nil)
	message := ilink.WeixinMessage{FromUserID: "owner-1", ContextToken: "context"}
	if err := handler.presentActionResult(context.Background(), client, message, newActionResult("system.guide", DomainSystem, "使用说明"), "client-1"); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("text delivery requests = %d", requests)
	}

	err := handler.presentActionResult(context.Background(), client, message, ActionResult{ActionID: "broken", Domain: DomainSystem}, "client-2")
	if err == nil || !strings.Contains(err.Error(), "invalid action result") {
		t.Fatalf("invalid result error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("invalid result reached WeChat: requests=%d", requests)
	}
}

func TestAttachmentIntentRequiresExplicitPermission(t *testing.T) {
	handler := NewHandler(nil)
	if result, handled := handler.handleControlInput(context.Background(), "owner-1", "/", true); handled || result != (ActionResult{}) {
		t.Fatalf("default attachment intent = %#v, handled=%v", result, handled)
	}

	definition, ok := handler.intents.Definition(IntentMenu)
	if !ok {
		t.Fatal("menu intent is missing")
	}
	definition.AllowAttachments = true
	registry, err := NewIntentRegistry([]IntentDefinition{definition})
	if err != nil {
		t.Fatal(err)
	}
	handler.intents = registry
	result, handled := handler.handleControlInput(context.Background(), "owner-1", "/", true)
	if !handled || result.ActionID != string(IntentMenu) {
		t.Fatalf("allowed attachment intent = %#v, handled=%v", result, handled)
	}
}
