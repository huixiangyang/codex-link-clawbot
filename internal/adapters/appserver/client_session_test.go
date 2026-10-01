package appserver

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
)

var _ codex.ThreadClient = (*Client)(nil)
var _ codex.TurnProgressClient = (*Client)(nil)

func newCodexSessionTestAgent(call func(context.Context, string, interface{}) (json.RawMessage, error)) *Client {
	return &Client{
		started:       true,
		model:         "gpt-test",
		loadedThreads: make(map[string]bool),
		threadStatus:  make(map[string]codex.ThreadStatus),
		threadUsage:   make(map[string]codex.ThreadUsage),
		instructions:  make(map[string][]string),
		turnCh:        make(map[string]chan *codexTurnEvent),
		rpcCall:       call,
	}
}

func threadResult(id, status string) json.RawMessage {
	return json.RawMessage(`{"thread":{"id":"` + id + `","sessionId":"session-1","name":null,"preview":"检查项目","cwd":"/workspace","createdAt":100,"updatedAt":200,"recencyAt":201,"modelProvider":"openai","isPinned":false,"status":{"type":"` + status + `"}}}`)
}

func TestCodexThreadLifecycleRPCs(t *testing.T) {
	const threadID = "019fcc03-fc8b-7842-a812-a132a87b9898"
	var methods []string
	a := newCodexSessionTestAgent(func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		methods = append(methods, method)
		switch method {
		case "thread/start":
			got := params.(map[string]interface{})
			if got["cwd"] != "/workspace" || got["model"] != "gpt-test" || got["approvalPolicy"] != "never" {
				t.Fatalf("thread/start params = %#v", got)
			}
			return threadResult(threadID, "idle"), nil
		case "thread/read":
			got := params.(map[string]interface{})
			if got["threadId"] != threadID || got["includeTurns"] != false {
				t.Fatalf("thread/read params = %#v", got)
			}
			return threadResult(threadID, "notLoaded"), nil
		case "thread/name/set":
			got := params.(map[string]string)
			if got["threadId"] != threadID || got["name"] != "发布排障" {
				t.Fatalf("thread/name/set params = %#v", got)
			}
			return json.RawMessage(`{}`), nil
		case "thread/archive":
			return json.RawMessage(`{}`), nil
		default:
			t.Fatalf("unexpected rpc method %q", method)
			return nil, nil
		}
	})

	started, err := a.StartThread(context.Background(), "/workspace")
	if err != nil || started.ID != threadID {
		t.Fatalf("StartThread() = %#v, %v", started, err)
	}
	if !a.loadedThreads[threadID] {
		t.Fatal("started thread should be marked loaded")
	}
	read, err := a.ReadThread(context.Background(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	if read.Status.Type != "notLoaded" {
		t.Fatalf("ReadThread() status = %q, want server status", read.Status.Type)
	}
	if err := a.SetThreadName(context.Background(), threadID, "发布排障"); err != nil {
		t.Fatal(err)
	}
	if err := a.ArchiveThread(context.Background(), threadID); err != nil {
		t.Fatal(err)
	}
	wantMethods := []string{
		"thread/start", "thread/read", "thread/name/set", "thread/archive",
	}
	if !reflect.DeepEqual(methods, wantMethods) {
		t.Fatalf("methods = %#v, want %#v", methods, wantMethods)
	}
}

func TestCodexResumeAndListThreads(t *testing.T) {
	const threadID = "019fcc03-fc8b-7842-a812-a132a87b9898"
	a := newCodexSessionTestAgent(func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/resume":
			got := params.(map[string]interface{})
			if got["threadId"] != threadID || got["sandbox"] != "danger-full-access" {
				t.Fatalf("thread/resume params = %#v", got)
			}
			return threadResult(threadID, "idle"), nil
		case "thread/list":
			got := params.(map[string]interface{})
			if got["archived"] != true || got["limit"] != 6 || got["cursor"] != "next-1" {
				t.Fatalf("thread/list params = %#v", got)
			}
			if !reflect.DeepEqual(got["sourceKinds"], []string{"vscode", "appServer"}) {
				t.Fatalf("thread/list sourceKinds = %#v", got["sourceKinds"])
			}
			return json.RawMessage(`{"data":[{"id":"` + threadID + `","preview":"检查项目","cwd":"/workspace","createdAt":100,"updatedAt":200,"status":{"type":"notLoaded"}}],"nextCursor":"next-2"}`), nil
		default:
			t.Fatalf("unexpected rpc method %q", method)
			return nil, nil
		}
	})
	resumed, err := a.ResumeThread(context.Background(), threadID, "/workspace")
	if err != nil || resumed.ID != threadID || !a.loadedThreads[threadID] {
		t.Fatalf("ResumeThread() = %#v, %v", resumed, err)
	}
	page, err := a.ListThreads(context.Background(), codex.ThreadListOptions{
		Archived: true, Limit: 6, Cursor: "next-1", SourceKinds: []string{"vscode", "appServer"},
	})
	if err != nil || len(page.Threads) != 1 || page.NextCursor != "next-2" {
		t.Fatalf("ListThreads() = %#v, %v", page, err)
	}
}

func TestCodexTracksThreadStatusNotifications(t *testing.T) {
	a := newCodexSessionTestAgent(nil)
	a.loadedThreads["thread-1"] = true
	a.handleThreadStatusChanged(json.RawMessage(`{
		"threadId":"thread-1",
		"status":{"type":"active","activeFlags":["waitingOnApproval"]}
	}`))
	status := a.threadStatus["thread-1"]
	if status.Type != "active" || !reflect.DeepEqual(status.ActiveFlags, []string{"waitingOnApproval"}) {
		t.Fatalf("status = %#v", status)
	}
	a.handleThreadStatusChanged(json.RawMessage(`{"threadId":"thread-1","status":{"type":"notLoaded"}}`))
	if a.loadedThreads["thread-1"] {
		t.Fatal("notLoaded notification should clear loaded marker")
	}
}

func TestInterruptUsesObservedTurnAndDoesNotAffectNewTurn(t *testing.T) {
	current := "turn-a"
	interrupted := 0
	client := newCodexSessionTestAgent(func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method == "thread/read" {
			if got := params.(map[string]interface{}); got["threadId"] != "thread" || got["includeTurns"] != true {
				t.Fatalf("read params: %#v", got)
			}
			status := "inProgress"
			if interrupted > 0 {
				status = "interrupted"
			}
			return json.Marshal(map[string]any{"thread": map[string]any{"turns": []map[string]string{{"id": current, "status": status}}}})
		}
		if method == "turn/interrupt" {
			got := params.(map[string]string)
			if got["turnId"] != current || got["threadId"] != "thread" {
				t.Fatalf("interrupt params: %#v", got)
			}
			interrupted++
			return json.RawMessage(`{}`), nil
		}
		t.Fatalf("unexpected call: %s", method)
		return nil, nil
	})
	turn, err := client.ActiveTurn(context.Background(), "thread")
	if err != nil || turn != "turn-a" {
		t.Fatalf("activity: %s %v", turn, err)
	}
	current = "turn-b"
	if client.InterruptTurn(context.Background(), "thread", turn) == nil || interrupted != 0 {
		t.Fatal("stale action interrupted new turn")
	}
	if err := client.InterruptTurn(context.Background(), "thread", current); err != nil || interrupted != 1 {
		t.Fatalf("interrupt: %v count=%d", err, interrupted)
	}
}
