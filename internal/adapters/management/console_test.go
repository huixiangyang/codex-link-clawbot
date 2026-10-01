package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestConsoleTokenIsStableAndPrivate(t *testing.T) {
	root := t.TempDir()
	first, err := EnsureConsoleToken(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureConsoleToken(root)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("unexpected token lifecycle: first=%d second=%d equal=%t", len(first), len(second), first == second)
	}
	info, err := os.Stat(ConsoleTokenPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %v", info.Mode().Perm())
	}
}

func TestConsoleRejectsMissingTokenAndProtectsStaticShell(t *testing.T) {
	token := strings.Repeat("a", 64)
	server := &ConsoleServer{token: token}
	handler := server.securityHeaders(server.handler())

	apiRequest := httptest.NewRequest(http.MethodGet, "/api/snapshot", nil)
	apiResponse := httptest.NewRecorder()
	handler.ServeHTTP(apiResponse, apiRequest)
	if apiResponse.Code != http.StatusUnauthorized {
		t.Fatalf("API status = %d", apiResponse.Code)
	}
	if apiResponse.Header().Get("Content-Security-Policy") == "" || apiResponse.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("security headers = %#v", apiResponse.Header())
	}

	pageRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	pageResponse := httptest.NewRecorder()
	handler.ServeHTTP(pageResponse, pageRequest)
	if pageResponse.Code != http.StatusOK || !strings.Contains(pageResponse.Body.String(), "Codex Link") {
		t.Fatalf("static page status=%d body=%q", pageResponse.Code, pageResponse.Body.String())
	}
	if strings.Contains(pageResponse.Body.String(), token) {
		t.Fatal("management token must never be embedded in the page")
	}
}
