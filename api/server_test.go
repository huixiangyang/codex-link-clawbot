package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/runtimecontrol"
	"github.com/huixiangyang/weclaw/taskqueue"
)

func TestServerReadyClosesOnlyAfterListenSucceeds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := NewServer(nil, "127.0.0.1:0", nil)
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

func TestStructuredHealthAndLoopbackDrain(t *testing.T) {
	tasks, err := taskqueue.NewStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	controller := runtimecontrol.New("v2.5-test", tasks, nil)
	controller.SetCodexReady(true)
	controller.SetReady()
	server := NewServer(nil, "127.0.0.1:0", controller)

	healthRequest := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthResponse := httptest.NewRecorder()
	server.handleHealth(healthResponse, healthRequest)
	if healthResponse.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", healthResponse.Code, healthResponse.Body.String())
	}
	var snapshot runtimecontrol.Snapshot
	if err := json.NewDecoder(healthResponse.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != runtimecontrol.StateReady || snapshot.Version != "v2.5-test" || !snapshot.Codex.Ready {
		t.Fatalf("health snapshot = %#v", snapshot)
	}

	drainRequest := httptest.NewRequest(http.MethodPost, "/admin/drain", nil)
	drainRequest.RemoteAddr = "127.0.0.1:42000"
	drainResponse := httptest.NewRecorder()
	server.handleDrain(drainResponse, drainRequest)
	if drainResponse.Code != http.StatusOK || !controller.Snapshot().Draining {
		t.Fatalf("drain status=%d snapshot=%#v", drainResponse.Code, controller.Snapshot())
	}
	remoteRequest := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	remoteRequest.RemoteAddr = "203.0.113.8:42000"
	remoteResponse := httptest.NewRecorder()
	server.handleResume(remoteResponse, remoteRequest)
	if remoteResponse.Code != http.StatusForbidden {
		t.Fatalf("remote admin status=%d", remoteResponse.Code)
	}
}

func TestServerDoesNotBecomeReadyWhenAddressIsOccupied(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	defer listener.Close()

	server := NewServer(nil, listener.Addr().String(), nil)
	if err := server.Run(context.Background()); err == nil {
		t.Fatal("Run() error = nil, want bind failure")
	}
	select {
	case <-server.Ready():
		t.Fatal("ready closed after bind failure")
	default:
	}
}
