package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

var _ ThreadAgent = (*ACPAgent)(nil)
var _ ThreadProgressAgent = (*ACPAgent)(nil)

func newCodexSessionTestAgent(call func(context.Context, string, interface{}) (json.RawMessage, error)) *ACPAgent {
	return &ACPAgent{
		started:       true,
		protocol:      protocolCodexAppServer,
		cwd:           "/workspace",
		model:         "gpt-test",
		loadedThreads: make(map[string]bool),
		threadStatus:  make(map[string]ThreadStatus),
		turnCh:        make(map[string]chan *codexTurnEvent),
		rpcCall:       call,
	}
}

func threadResult(id, status string) json.RawMessage {
	return json.RawMessage(`{"thread":{"id":"` + id + `","sessionId":"session-1","name":null,"preview":"检查项目","cwd":"/workspace","createdAt":100,"updatedAt":200,"recencyAt":201,"modelProvider":"openai","isPinned":false,"status":{"type":"` + status + `"}}}`)
}

func TestACPAgentCodexThreadLifecycleRPCs(t *testing.T) {
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
		case "thread/unsubscribe":
			return json.RawMessage(`{"status":"unsubscribed"}`), nil
		case "thread/archive":
			return json.RawMessage(`{}`), nil
		case "thread/unarchive":
			return threadResult(threadID, "notLoaded"), nil
		default:
			t.Fatalf("unexpected rpc method %q", method)
			return nil, nil
		}
	})

	started, err := a.StartThread(context.Background())
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
	if err := a.UnsubscribeThread(context.Background(), threadID); err != nil {
		t.Fatal(err)
	}
	if a.loadedThreads[threadID] {
		t.Fatal("unsubscribed thread should not remain marked loaded")
	}
	if err := a.ArchiveThread(context.Background(), threadID); err != nil {
		t.Fatal(err)
	}
	restored, err := a.UnarchiveThread(context.Background(), threadID)
	if err != nil || restored.ID != threadID {
		t.Fatalf("UnarchiveThread() = %#v, %v", restored, err)
	}
	wantMethods := []string{
		"thread/start", "thread/read", "thread/name/set", "thread/unsubscribe", "thread/archive", "thread/unarchive",
	}
	if !reflect.DeepEqual(methods, wantMethods) {
		t.Fatalf("methods = %#v, want %#v", methods, wantMethods)
	}
}

func TestACPAgentResumeAndListThreads(t *testing.T) {
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
	resumed, err := a.ResumeThread(context.Background(), threadID)
	if err != nil || resumed.ID != threadID || !a.loadedThreads[threadID] {
		t.Fatalf("ResumeThread() = %#v, %v", resumed, err)
	}
	page, err := a.ListThreads(context.Background(), ThreadListOptions{
		Archived: true, Limit: 6, Cursor: "next-1", SourceKinds: []string{"vscode", "appServer"},
	})
	if err != nil || len(page.Threads) != 1 || page.NextCursor != "next-2" {
		t.Fatalf("ListThreads() = %#v, %v", page, err)
	}
}

func TestACPAgentTracksThreadStatusNotifications(t *testing.T) {
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

func TestACPAgentRejectsImplicitCodexConversation(t *testing.T) {
	a := newCodexSessionTestAgent(nil)
	_, err := a.Chat(context.Background(), "wechat-user", ChatRequest{Text: "检查项目"})
	if err == nil || !strings.Contains(err.Error(), "explicit thread id") {
		t.Fatalf("Chat() error = %v", err)
	}
}
