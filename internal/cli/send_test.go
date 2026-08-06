package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huixiangyang/weclaw/internal/api"
	"github.com/huixiangyang/weclaw/internal/config"
	"github.com/spf13/cobra"
)

func TestValidateSendEndpointRejectsPlaintextNetworkAddress(t *testing.T) {
	for _, raw := range []string{
		"http://0.0.0.0:18011",
		"http://192.168.1.20:18011",
		"http://example.com:18011",
		"ftp://127.0.0.1:18011",
		"https://user:secret@example.com",
	} {
		if _, err := validateSendEndpoint(raw); err == nil {
			t.Fatalf("unsafe endpoint %q was accepted", raw)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:18011", "https://send.example.com"} {
		if _, err := validateSendEndpoint(raw); err != nil {
			t.Fatalf("valid endpoint %q rejected: %v", raw, err)
		}
	}
}

func TestRunSendUsesAuthenticatedAPIWithoutCredentialFiles(t *testing.T) {
	const token = "client-test-token"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("Idempotency-Key") == "" {
			http.Error(response, "missing authentication", http.StatusUnauthorized)
			return
		}
		var payload api.SendRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.CallerID != "local-cli" || payload.TargetOwner != "owner-id" {
			http.Error(response, "invalid payload", http.StatusBadRequest)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"status":"sent","receipt_id":"receipt-123"}`))
	}))
	defer server.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WECLAW_SEND_TOKEN", token)
	cfg := config.DefaultConfig()
	cfg.SendAPI = config.SendAPIConfig{
		Enabled: true, ListenAddr: "127.0.0.1:18011",
		Tokens: []config.SendAPITokenConfig{{CallerID: "local-cli", TokenSHA256: strings.Repeat("a", 64), Scopes: []string{api.ScopeSendText}}},
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	oldTo, oldText, oldMedia := sendTo, sendText, sendMediaURL
	oldCaller, oldEndpoint, oldKey := sendCaller, sendEndpoint, sendIdempotencyKey
	t.Cleanup(func() {
		sendTo, sendText, sendMediaURL = oldTo, oldText, oldMedia
		sendCaller, sendEndpoint, sendIdempotencyKey = oldCaller, oldEndpoint, oldKey
	})
	sendTo, sendText, sendMediaURL = "owner-id", "hello", ""
	sendCaller, sendEndpoint, sendIdempotencyKey = "local-cli", server.URL, "client-request-0001"
	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&output)
	command.SetContext(context.Background())
	if err := runSend(command, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "receipt-123") || !strings.Contains(output.String(), "client-request-0001") {
		t.Fatalf("send output=%q", output.String())
	}
}
