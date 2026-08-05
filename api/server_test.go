package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestServerReadyClosesOnlyAfterListenSucceeds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := NewServer(nil, "127.0.0.1:0")
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

func TestTCPServerDoesNotExposeManagementRoutes(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:0")
	for _, route := range []string{"/health", "/admin/drain", "/admin/resume", "/admin/deployment-notification"} {
		request := httptest.NewRequest(http.MethodPost, route, nil)
		response := httptest.NewRecorder()
		server.handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("route %s status=%d, want 404", route, response.Code)
		}
	}
}

func TestServerDoesNotBecomeReadyWhenAddressIsOccupied(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	defer listener.Close()

	server := NewServer(nil, listener.Addr().String())
	if err := server.Run(context.Background()); err == nil {
		t.Fatal("Run() error = nil, want bind failure")
	}
	select {
	case <-server.Ready():
		t.Fatal("ready closed after bind failure")
	default:
	}
}
