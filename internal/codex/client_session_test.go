package codex

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

var _ ThreadClient = (*Codex)(nil)
var _ AdvancedThreadClient = (*Codex)(nil)
var _ CapabilityClient = (*Codex)(nil)
var _ ProgressClient = (*Codex)(nil)
var _ GlobalControlClient = (*Codex)(nil)

func newCodexSessionTestAgent(call func(context.Context, string, interface{}) (json.RawMessage, error)) *Codex {
	return &Codex{
		started:       true,
		cwd:           "/workspace",
		model:         "gpt-test",
		loadedThreads: make(map[string]bool),
		threadStatus:  make(map[string]ThreadStatus),
		threadUsage:   make(map[string]ThreadUsage),
		activeTurns:   make(map[string]string),
		instructions:  make(map[string][]string),
		turnCh:        make(map[string]chan *codexTurnEvent),
		rpcCall:       call,
	}
}

func TestCodexAdvancedThreadRPCs(t *testing.T) {
	const sourceID = "thread-source"
	const forkedID = "thread-forked"
	var methods []string
	a := newCodexSessionTestAgent(func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		methods = append(methods, method)
		switch method {
		case "thread/fork":
			if got := params.(map[string]string); got["threadId"] != sourceID {
				t.Fatalf("thread/fork params = %#v", got)
			}
			return json.RawMessage(`{"thread":{"id":"thread-forked","forkedFromId":"thread-source","status":{"type":"idle"}},"instructionSources":["/workspace/AGENTS.md"]}`), nil
		case "thread/metadata/update":
			got := params.(map[string]interface{})
			if got["threadId"] != forkedID || got["isPinned"] != true {
				t.Fatalf("thread/metadata/update params = %#v", got)
			}
			return json.RawMessage(`{"thread":{"id":"thread-forked","isPinned":true,"status":{"type":"idle"}}}`), nil
		case "thread/compact/start":
			if got := params.(map[string]string); got["threadId"] != forkedID {
				t.Fatalf("thread/compact/start params = %#v", got)
			}
			return json.RawMessage(`{}`), nil
		case "thread/delete":
			if got := params.(map[string]string); got["threadId"] != forkedID {
				t.Fatalf("thread/delete params = %#v", got)
			}
			return json.RawMessage(`{}`), nil
		default:
			t.Fatalf("unexpected rpc method %q", method)
			return nil, nil
		}
	})

	forked, err := a.ForkThread(context.Background(), sourceID)
	if err != nil || forked.ID != forkedID || forked.ForkedFromID != sourceID ||
		!reflect.DeepEqual(forked.InstructionSources, []string{"/workspace/AGENTS.md"}) {
		t.Fatalf("ForkThread() = %#v, %v", forked, err)
	}
	pinned, err := a.SetThreadPinned(context.Background(), forkedID, true)
	if err != nil || !pinned.IsPinned {
		t.Fatalf("SetThreadPinned() = %#v, %v", pinned, err)
	}
	if err := a.CompactThread(context.Background(), forkedID); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteThread(context.Background(), forkedID); err != nil {
		t.Fatal(err)
	}
	want := []string{"thread/fork", "thread/metadata/update", "thread/compact/start", "thread/delete"}
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("methods = %#v, want %#v", methods, want)
	}
}

func TestCodexThreadGoalAndModelDirectoryRPCs(t *testing.T) {
	const threadID = "thread-goal"
	var goalExists = true
	a := newCodexSessionTestAgent(func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/goal/set":
			got := params.(map[string]interface{})
			if got["threadId"] != threadID {
				t.Fatalf("thread/goal/set params = %#v", got)
			}
			if objective, exists := got["objective"]; exists {
				if objective != "完成中文控制面" || got["status"] != "active" {
					t.Fatalf("thread/goal/set objective params = %#v", got)
				}
				return json.RawMessage(`{"goal":{"threadId":"thread-goal","objective":"完成中文控制面","status":"active","tokensUsed":0,"timeUsedSeconds":0}}`), nil
			}
			if len(got) != 2 || got["status"] != "paused" {
				t.Fatalf("thread/goal/set status params = %#v", got)
			}
			return json.RawMessage(`{"goal":{"threadId":"thread-goal","objective":"完成中文控制面","status":"paused","tokensUsed":12,"timeUsedSeconds":3}}`), nil
		case "thread/goal/get":
			if got := params.(map[string]string); got["threadId"] != threadID {
				t.Fatalf("thread/goal/get params = %#v", got)
			}
			if !goalExists {
				return json.RawMessage(`{"goal":null}`), nil
			}
			return json.RawMessage(`{"goal":{"threadId":"thread-goal","objective":"完成中文控制面","status":"active","tokensUsed":12,"timeUsedSeconds":3}}`), nil
		case "thread/goal/clear":
			goalExists = false
			return json.RawMessage(`{}`), nil
		case "model/list":
			got := params.(map[string]interface{})
			if got["limit"] != 100 || got["includeHidden"] != false {
				t.Fatalf("model/list params = %#v", got)
			}
			return json.RawMessage(`{"data":[{"id":"gpt-test","model":"gpt-test","displayName":"测试模型","defaultReasoningEffort":"medium","supportedReasoningEfforts":[{"reasoningEffort":"medium","description":"均衡"}],"inputModalities":["text","image"],"isDefault":true}]}`), nil
		default:
			t.Fatalf("unexpected rpc method %q", method)
			return nil, nil
		}
	})

	goal, err := a.SetThreadGoal(context.Background(), threadID, " 完成中文控制面 ", nil)
	if err != nil || goal.Objective != "完成中文控制面" {
		t.Fatalf("SetThreadGoal() = %#v, %v", goal, err)
	}
	goal, err = a.UpdateThreadGoalStatus(context.Background(), threadID, "paused")
	if err != nil || goal.Status != "paused" {
		t.Fatalf("UpdateThreadGoalStatus() = %#v, %v", goal, err)
	}
	if _, err := a.UpdateThreadGoalStatus(context.Background(), threadID, "complete"); err == nil {
		t.Fatal("UpdateThreadGoalStatus() accepted unsupported complete status")
	}
	goal, exists, err := a.GetThreadGoal(context.Background(), threadID)
	if err != nil || !exists || goal.TokensUsed != 12 {
		t.Fatalf("GetThreadGoal() = %#v, %v, %v", goal, exists, err)
	}
	if err := a.ClearThreadGoal(context.Background(), threadID); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := a.GetThreadGoal(context.Background(), threadID); err != nil || exists {
		t.Fatalf("cleared GetThreadGoal() exists=%v err=%v", exists, err)
	}
	models, err := a.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].DisplayName != "测试模型" || models[0].SupportedReasoningEfforts[0].Effort != "medium" {
		t.Fatalf("ListModels() = %#v, %v", models, err)
	}
}

func TestCodexInspectsProjectCapabilities(t *testing.T) {
	a := newCodexSessionTestAgent(func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "skills/list":
			got := params.(map[string]interface{})
			if !reflect.DeepEqual(got["cwds"], []string{"/workspace"}) || got["forceReload"] != false {
				t.Fatalf("skills/list params = %#v", got)
			}
			return json.RawMessage(`{"data":[{"cwd":"/workspace","skills":[{"name":"review","description":"review code","enabled":true,"interface":{"displayName":"代码审查"}}],"errors":[]}]}`), nil
		case "mcpServerStatus/list":
			got := params.(map[string]interface{})
			if got["limit"] != 100 || got["detail"] != "toolsAndAuthOnly" {
				t.Fatalf("mcpServerStatus/list params = %#v", got)
			}
			return json.RawMessage(`{"data":[{"name":"ready","authStatus":"unsupported","resourceTemplates":[],"resources":[],"tools":{}},{"name":"login","authStatus":"notLoggedIn","resourceTemplates":[],"resources":[],"tools":{}}]}`), nil
		default:
			t.Fatalf("unexpected rpc method %q", method)
			return nil, nil
		}
	})

	capabilities, err := a.InspectProject(context.Background(), "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if len(capabilities.Skills) != 1 || capabilities.Skills[0].Interface.DisplayName != "代码审查" {
		t.Fatalf("skills = %#v", capabilities.Skills)
	}
	if capabilities.MCPServers != 2 || capabilities.MCPReady != 1 {
		t.Fatalf("external tools = %d/%d", capabilities.MCPReady, capabilities.MCPServers)
	}
}

func TestCodexSteersAndReviewsActiveTurn(t *testing.T) {
	const threadID = "thread-active"
	const turnID = "turn-active"
	var a *Codex
	a = newCodexSessionTestAgent(func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "turn/steer":
			got := params.(map[string]interface{})
			if got["threadId"] != threadID || got["expectedTurnId"] != turnID {
				t.Fatalf("turn/steer params = %#v", got)
			}
			input := got["input"].([]codexUserInput)
			if len(input) != 1 || input[0].Text != "先修复失败测试" {
				t.Fatalf("turn/steer input = %#v", input)
			}
			return json.RawMessage(`{"turnId":"turn-active"}`), nil
		case "review/start":
			got := params.(map[string]interface{})
			if got["threadId"] != threadID || got["delivery"] != "inline" || got["target"].(ReviewTarget).Type != "uncommittedChanges" {
				t.Fatalf("review/start params = %#v", got)
			}
			a.turnCh[threadID] <- &codexTurnEvent{Kind: "message_completed", ItemID: "review-message", Text: "发现一处风险"}
			a.turnCh[threadID] <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"turn":{"id":"turn-review"},"reviewThreadId":"thread-active"}`), nil
		default:
			t.Fatalf("unexpected rpc method %q", method)
			return nil, nil
		}
	})
	a.loadedThreads[threadID] = true
	a.activeTurns[threadID] = turnID
	if err := a.SteerThread(context.Background(), threadID, ChatRequest{Text: "先修复失败测试"}); err != nil {
		t.Fatal(err)
	}
	delete(a.activeTurns, threadID)
	review, err := a.ReviewThread(context.Background(), threadID, ReviewTarget{Type: "uncommittedChanges"}, nil)
	if err != nil || review != "发现一处风险" {
		t.Fatalf("ReviewThread() = %q, %v", review, err)
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

func TestCodexReadsGlobalControlState(t *testing.T) {
	var methods []string
	a := newCodexSessionTestAgent(func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		methods = append(methods, method)
		switch method {
		case "thread/loaded/list":
			if params != nil {
				t.Fatalf("thread/loaded/list params = %#v, want nil", params)
			}
			return json.RawMessage(`{"data":["thread-1","thread-2"]}`), nil
		case "account/read":
			if got := params.(map[string]bool); got["refreshToken"] {
				t.Fatalf("account/read params = %#v", got)
			}
			return json.RawMessage(`{"account":{"type":"chatgpt","email":"owner@example.com","planType":"pro"},"requiresOpenaiAuth":true}`), nil
		default:
			t.Fatalf("unexpected rpc method %q", method)
			return nil, nil
		}
	})
	ids, err := a.ListLoadedThreadIDs(context.Background())
	if err != nil || !reflect.DeepEqual(ids, []string{"thread-1", "thread-2"}) {
		t.Fatalf("ListLoadedThreadIDs() = %#v, %v", ids, err)
	}
	account, err := a.ReadAccount(context.Background())
	if err != nil || account.Type != "chatgpt" || account.Email != "owner@example.com" || account.PlanType != "pro" || !account.RequiresOpenAIAuth {
		t.Fatalf("ReadAccount() = %#v, %v", account, err)
	}
	if !reflect.DeepEqual(methods, []string{"thread/loaded/list", "account/read"}) {
		t.Fatalf("methods = %#v", methods)
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
