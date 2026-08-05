package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testBearerToken = "test-token-with-at-least-256-bits-of-randomness"

type fakeDelivery struct {
	mu         sync.Mutex
	owners     map[string]bool
	textCount  int
	mediaCount int
	fail       error
}

func (d *fakeDelivery) HasOwner(owner string) bool { return d.owners[owner] }

func (d *fakeDelivery) SendText(_ context.Context, _, _, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.textCount++
	return d.fail
}

func (d *fakeDelivery) SendMedia(_ context.Context, _, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mediaCount++
	return d.fail
}

func TestServerReadyClosesOnlyAfterListenSucceeds(t *testing.T) {
	server, _ := newTestSendServer(t, []string{ScopeSendText, ScopeSendMedia}, false)
	server.addr = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- server.Run(ctx) }()

	select {
	case <-server.Ready():
	case err := <-errCh:
		t.Fatalf("server exited before ready: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not become ready")
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server shutdown error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestServerRejectsUnsafeListenerConfiguration(t *testing.T) {
	digest := sha256.Sum256([]byte(testBearerToken))
	delivery := &fakeDelivery{owners: map[string]bool{"owner@wechat": true}}
	receipts, err := NewSendReceiptStore(filepath.Join(t.TempDir(), "send-api-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	token := []AccessToken{{CallerID: "dashboard", TokenSHA256: hex.EncodeToString(digest[:]), Scopes: []string{ScopeSendText}}}
	for _, config := range []ServerConfig{
		{ListenAddr: "0.0.0.0:18012", Tokens: token},
		{ListenAddr: "0.0.0.0:18012", ProxyMode: true, TrustedProxyCIDRs: []string{"0.0.0.0/0"}, Tokens: token},
	} {
		if _, err := NewServer(delivery, config, receipts); err == nil {
			t.Fatalf("unsafe server config accepted: %#v", config)
		}
	}
}

func TestTCPServerDoesNotExposeManagementRoutes(t *testing.T) {
	server, _ := newTestSendServer(t, []string{ScopeSendText}, false)
	for _, route := range []string{"/health", "/admin/drain", "/admin/resume", "/admin/deployment-notification"} {
		request := httptest.NewRequest(http.MethodPost, route, nil)
		response := httptest.NewRecorder()
		server.handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("route %s status=%d, want 404", route, response.Code)
		}
	}
}

func TestSendAPIRequiresAuthenticationScopeAndBoundOwner(t *testing.T) {
	server, _ := newTestSendServer(t, []string{ScopeSendText}, false)
	body := `{"caller_id":"dashboard","target_owner":"owner@wechat","text":"hello"}`

	request := sendTestRequest(body, "")
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", response.Code)
	}

	request = sendTestRequest(body, "wrong-token")
	response = httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status=%d", response.Code)
	}

	callerMismatch := `{"caller_id":"other","target_owner":"owner@wechat","text":"hello"}`
	request = sendTestRequest(callerMismatch, testBearerToken)
	response = httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("caller mismatch status=%d", response.Code)
	}

	mediaBody := `{"caller_id":"dashboard","target_owner":"owner@wechat","media_url":"https://example.com/image.png"}`
	request = sendTestRequest(mediaBody, testBearerToken)
	response = httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong scope status=%d body=%s", response.Code, response.Body.String())
	}

	unknownOwner := `{"caller_id":"dashboard","target_owner":"other@wechat","text":"hello"}`
	request = sendTestRequest(unknownOwner, testBearerToken)
	response = httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unbound owner status=%d", response.Code)
	}
}

func TestSendAPIIsAtMostOnceAndRejectsIdempotencyConflict(t *testing.T) {
	server, delivery := newTestSendServer(t, []string{ScopeSendText}, false)
	body := `{"caller_id":"dashboard","target_owner":"owner@wechat","text":"same body"}`
	for attempt := 0; attempt < 2; attempt++ {
		request := sendTestRequest(body, testBearerToken)
		response := httptest.NewRecorder()
		server.handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("attempt %d status=%d body=%s", attempt, response.Code, response.Body.String())
		}
	}
	if delivery.textCount != 1 {
		t.Fatalf("text deliveries=%d, want 1", delivery.textCount)
	}

	conflict := `{"caller_id":"dashboard","target_owner":"owner@wechat","text":"different body"}`
	request := sendTestRequest(conflict, testBearerToken)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", response.Code, response.Body.String())
	}
	if delivery.textCount != 1 {
		t.Fatalf("conflicting request was delivered")
	}
}

func TestSendAPIDoesNotRepeatFailedOrAmbiguousDelivery(t *testing.T) {
	server, delivery := newTestSendServer(t, []string{ScopeSendText}, false)
	delivery.fail = fmt.Errorf("ambiguous upstream failure")
	body := `{"caller_id":"dashboard","target_owner":"owner@wechat","text":"send once"}`
	request := sendTestRequest(body, testBearerToken)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("first failure status=%d", response.Code)
	}

	delivery.fail = nil
	request = sendTestRequest(body, testBearerToken)
	response = httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("duplicate failure status=%d body=%s", response.Code, response.Body.String())
	}
	var result sendResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || result.Status != "duplicate" || result.Outcome != string(ReceiptFailed) {
		t.Fatalf("duplicate failure result=%#v err=%v", result, err)
	}
	if delivery.textCount != 1 {
		t.Fatalf("failed delivery repeated %d times", delivery.textCount)
	}
}

func TestSendAPIRejectsOversizeAndUntrustedProxy(t *testing.T) {
	server, _ := newTestSendServer(t, []string{ScopeSendText}, false)
	tooMuchText := fmt.Sprintf(`{"caller_id":"dashboard","target_owner":"owner@wechat","text":%q}`, strings.Repeat("x", maxTextRunes+1))
	request := sendTestRequest(tooMuchText, testBearerToken)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("text limit status=%d body=%s", response.Code, response.Body.String())
	}

	oversize := fmt.Sprintf(`{"caller_id":"dashboard","target_owner":"owner@wechat","text":%q}`, strings.Repeat("x", maxSendBody))
	request = sendTestRequest(oversize, testBearerToken)
	response = httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status=%d body=%s", response.Code, response.Body.String())
	}

	proxyServer, _ := newTestSendServer(t, []string{ScopeSendText}, true)
	request = sendTestRequest(`{"caller_id":"dashboard","target_owner":"owner@wechat","text":"hello"}`, testBearerToken)
	request.RemoteAddr = "203.0.113.20:41000"
	response = httptest.NewRecorder()
	proxyServer.handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("untrusted proxy status=%d", response.Code)
	}

	request = sendTestRequest(`{"caller_id":"dashboard","target_owner":"owner@wechat","text":"hello"}`, testBearerToken)
	request.RemoteAddr = "10.20.30.40:41000"
	response = httptest.NewRecorder()
	proxyServer.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("trusted proxy status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSendReceiptStateDoesNotPersistPayloadOrSecrets(t *testing.T) {
	server, _ := newTestSendServer(t, []string{ScopeSendText}, false)
	body := `{"caller_id":"dashboard","target_owner":"owner@wechat","text":"private-body-marker"}`
	request := sendTestRequest(body, testBearerToken)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("send status=%d body=%s", response.Code, response.Body.String())
	}
	data, err := os.ReadFile(server.receipts.path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(server.receipts.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt permissions=%v", info.Mode().Perm())
	}
	for _, forbidden := range []string{"private-body-marker", "owner@wechat", testBearerToken, "request-key-0001"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("receipt state leaked %q: %s", forbidden, data)
		}
	}
	var state sendReceiptState
	if err := json.Unmarshal(data, &state); err != nil || len(state.Receipts) != 1 {
		t.Fatalf("receipt state=%#v err=%v", state, err)
	}
}

func TestServerDoesNotBecomeReadyWhenAddressIsOccupied(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	defer listener.Close()

	server, _ := newTestSendServer(t, []string{ScopeSendText}, false)
	server.addr = listener.Addr().String()
	if err := server.Run(context.Background()); err == nil {
		t.Fatal("Run() error = nil, want bind failure")
	}
	select {
	case <-server.Ready():
		t.Fatal("ready closed after bind failure")
	default:
	}
}

func newTestSendServer(t *testing.T, scopes []string, proxyMode bool) (*Server, *fakeDelivery) {
	t.Helper()
	digest := sha256.Sum256([]byte(testBearerToken))
	delivery := &fakeDelivery{owners: map[string]bool{"owner@wechat": true}}
	receipts, err := NewSendReceiptStore(filepath.Join(t.TempDir(), "send-api-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := ServerConfig{
		ListenAddr: "127.0.0.1:18012",
		Tokens:     []AccessToken{{CallerID: "dashboard", TokenSHA256: hex.EncodeToString(digest[:]), Scopes: scopes}},
		ProxyMode:  proxyMode,
	}
	if proxyMode {
		config.TrustedProxyCIDRs = []string{"10.0.0.0/8"}
	}
	server, err := NewServer(delivery, config, receipts)
	if err != nil {
		t.Fatal(err)
	}
	return server, delivery
}

func sendTestRequest(body, token string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:41000"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "request-key-0001")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request
}
