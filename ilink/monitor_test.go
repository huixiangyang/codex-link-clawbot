package ilink

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type monitorObserverRecorder struct {
	cancel  context.CancelFunc
	healthy []bool
}

func (*monitorObserverRecorder) SetRunning(bool)      {}
func (*monitorObserverRecorder) SetBatchPending(bool) {}
func (observer *monitorObserverRecorder) SetHealthy(healthy bool) {
	observer.healthy = append(observer.healthy, healthy)
	if observer.cancel != nil {
		observer.cancel()
	}
}

func TestMonitorTreatsExpiredSessionAsUnhealthy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"ret":1,"errcode":-14,"errmsg":"expired"}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	observer := &monitorObserverRecorder{cancel: cancel}
	client := NewClient(&Credentials{ILinkBotID: "bot-test", BotToken: "token", BaseURL: server.URL})
	monitor, err := NewMonitor(client, func(context.Context, *Client, WeixinMessage) error { return nil }, observer)
	if err != nil {
		t.Fatal(err)
	}
	if err := monitor.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if len(observer.healthy) != 1 || observer.healthy[0] {
		t.Fatalf("health observations = %#v", observer.healthy)
	}
}

func TestMonitorTreatsAnyBusinessErrorAsUnhealthy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"ret":7,"errmsg":"rejected"}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	observer := &monitorObserverRecorder{cancel: cancel}
	client := NewClient(&Credentials{ILinkBotID: "bot-test", BotToken: "token", BaseURL: server.URL})
	monitor, err := NewMonitor(client, func(context.Context, *Client, WeixinMessage) error { return nil }, observer)
	if err != nil {
		t.Fatal(err)
	}
	if err := monitor.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if len(observer.healthy) != 1 || observer.healthy[0] {
		t.Fatalf("health observations = %#v", observer.healthy)
	}
}

func TestMonitorMessageHoldDoesNotConsumeOrAdvanceCursor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret":0,"msgs":[{"seq":7,"message_type":1,"message_state":2}],"get_updates_buf":"cursor-next"}`))
	}))
	defer server.Close()
	client := NewClient(&Credentials{ILinkBotID: "bot-test", BotToken: "token", BaseURL: server.URL})
	handlerCalls := 0
	monitor, err := NewMonitor(client, func(context.Context, *Client, WeixinMessage) error {
		handlerCalls++
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	monitor.SetMessageHold(func() bool { return true })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	if err := monitor.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if handlerCalls != 0 || monitor.getUpdatesBuf != "" {
		t.Fatalf("held monitor consumed=%d cursor=%q", handlerCalls, monitor.getUpdatesBuf)
	}
	if _, err := os.Stat(monitor.bufPath); !os.IsNotExist(err) {
		t.Fatalf("held monitor persisted sync state: %v", err)
	}
}

func TestMonitorAdvancesCursorOnlyAfterWholeBatchSucceeds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := NewClient(&Credentials{ILinkBotID: "bot-test"})
	calls := 0
	failSecond := true
	monitor, err := NewMonitor(client, func(_ context.Context, _ *Client, msg WeixinMessage) error {
		calls++
		if msg.Seq == 2 && failSecond {
			return errors.New("disk unavailable")
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	messages := []WeixinMessage{{Seq: 1}, {Seq: 2}}
	if err := monitor.processBatch(context.Background(), messages, "cursor-next"); err == nil {
		t.Fatal("failed batch unexpectedly committed")
	}
	if monitor.getUpdatesBuf != "" {
		t.Fatalf("cursor advanced after failed batch: %q", monitor.getUpdatesBuf)
	}
	if _, err := os.Stat(monitor.bufPath); err != nil {
		t.Fatalf("partial batch receipt was not persisted: %v", err)
	}

	failSecond = false
	if err := monitor.processBatch(context.Background(), messages, "cursor-next"); err != nil {
		t.Fatal(err)
	}
	if monitor.getUpdatesBuf != "cursor-next" || calls != 3 {
		t.Fatalf("committed cursor=%q handler calls=%d", monitor.getUpdatesBuf, calls)
	}
	reloaded, err := NewMonitor(client, func(context.Context, *Client, WeixinMessage) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.getUpdatesBuf != "cursor-next" {
		t.Fatalf("reloaded cursor = %q", reloaded.getUpdatesBuf)
	}
}

func TestMonitorRejectsUnknownCursorFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := NewClient(&Credentials{ILinkBotID: "bot-test"})
	monitor, err := NewMonitor(client, func(context.Context, *Client, WeixinMessage) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(monitor.bufPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(monitor.bufPath, []byte(`{"version":1,"get_updates_buf":"cursor","extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMonitor(client, func(context.Context, *Client, WeixinMessage) error { return nil }, nil); err == nil {
		t.Fatal("monitor accepted an unknown cursor field")
	}
}
