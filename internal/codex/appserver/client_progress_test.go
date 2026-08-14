package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
)

func newProgressTestAgent(threadID string) (*Client, chan *codexTurnEvent) {
	ch := make(chan *codexTurnEvent, 4)
	return &Client{turnCh: map[string]chan *codexTurnEvent{threadID: ch}}, ch
}

func TestHandleCodexPlanUpdated(t *testing.T) {
	agent, ch := newProgressTestAgent("thread-1")
	agent.handleCodexPlanUpdated(json.RawMessage(`{
		"threadId":"thread-1","turnId":"turn-1",
		"plan":[
			{"step":"检查环境","status":"completed"},
			{"step":"替换服务","status":"inProgress"},
			{"step":"验证链路","status":"pending"}
		]
	}`))

	event := <-ch
	if event.Kind != "plan" || event.TurnID != "turn-1" || event.Completed != 1 || event.Total != 3 {
		t.Fatalf("unexpected plan event: %#v", event)
	}
	if event.Text != "替换服务" {
		t.Fatalf("event.Text = %q, want %q", event.Text, "替换服务")
	}
}

func TestHandleCodexTurnCompletedMapsTerminalStatus(t *testing.T) {
	for _, test := range []struct {
		status string
		phase  codex.TurnPhase
	}{
		{status: "completed", phase: codex.TurnPhaseCompleted},
		{status: "failed", phase: codex.TurnPhaseFailed},
		{status: "interrupted", phase: codex.TurnPhaseInterrupted},
	} {
		t.Run(test.status, func(t *testing.T) {
			agent, ch := newProgressTestAgent("thread-1")
			agent.handleCodexTurnEvent("turn/completed", json.RawMessage(`{
				"threadId":"thread-1","turn":{"id":"turn-1","status":"`+test.status+`","items":[]}
			}`))

			event := <-ch
			if event.Kind != "terminal" || event.TurnID != "turn-1" || event.Phase != string(test.phase) {
				t.Fatalf("terminal event = %#v, want %s", event, test.phase)
			}
		})
	}
}

func TestHandleCodexItemCompletedKeepsMessagePhase(t *testing.T) {
	agent, ch := newProgressTestAgent("thread-1")
	agent.handleCodexItemCompleted(json.RawMessage(`{
		"threadId":"thread-1","turnId":"turn-1",
		"item":{"id":"msg-1","type":"agentMessage","phase":"commentary","text":"正在检查服务"}
	}`))

	event := <-ch
	if event.Kind != "message_completed" || event.TurnID != "turn-1" || event.Phase != "commentary" || event.Text != "正在检查服务" {
		t.Fatalf("unexpected message event: %#v", event)
	}
}

func TestHandleCodexWorkItemReportsOnlyStructuredPhase(t *testing.T) {
	for _, itemType := range []string{"commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "subAgentActivity"} {
		t.Run(itemType, func(t *testing.T) {
			agent, ch := newProgressTestAgent("thread-1")
			params := json.RawMessage(`{
				"threadId":"thread-1","turnId":"turn-1",
				"item":{"id":"work-1","type":"` + itemType + `","command":"SECRET_TOKEN=must-not-leak","cwd":"/private/path"}
			}`)
			agent.handleCodexItemStarted(params)

			event := <-ch
			if event.Kind != "phase" || event.TurnID != "turn-1" || event.Phase != string(codex.TurnPhaseWorking) || event.Text != "" {
				t.Fatalf("work item exposed unexpected fields: %#v", event)
			}
		})
	}
}

func TestChatCodexAppServerSeparatesCommentaryAndFinalAnswer(t *testing.T) {
	a := &Client{
		started:       true,
		loadedThreads: map[string]bool{"thread-1": true},
		threadStatus:  make(map[string]codex.ThreadStatus),
		turnCh:        make(map[string]chan *codexTurnEvent),
	}
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "turn/start" {
			t.Fatalf("unexpected rpc method: %s", method)
		}
		a.handleCodexTurnEvent("turn/started", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"inProgress","items":[]}}`))
		a.handleCodexItemStarted(json.RawMessage(`{
			"threadId":"thread-1","turnId":"turn-1","item":{"id":"comment-1","type":"agentMessage","phase":"commentary","text":""}
		}`))
		a.handleCodexItemDelta(json.RawMessage(`{
			"threadId":"thread-1","turnId":"turn-1","itemId":"comment-1","delta":"正在检查本机服务"
		}`))
		a.handleCodexItemCompleted(json.RawMessage(`{
			"threadId":"thread-1","turnId":"turn-1","item":{"id":"comment-1","type":"agentMessage","phase":"commentary","text":"正在检查本机服务"}
		}`))
		a.handleCodexPlanUpdated(json.RawMessage(`{
			"threadId":"thread-1","turnId":"turn-1","plan":[{"step":"验证链路","status":"inProgress"}]
		}`))
		a.handleCodexItemStarted(json.RawMessage(`{
			"threadId":"thread-1","turnId":"turn-1","item":{"id":"cmd-1","type":"commandExecution","command":"do not expose"}
		}`))
		a.handleCodexItemStarted(json.RawMessage(`{
			"threadId":"thread-1","turnId":"turn-1","item":{"id":"final-1","type":"agentMessage","phase":"final_answer","text":""}
		}`))
		a.handleCodexItemDelta(json.RawMessage(`{
			"threadId":"thread-1","turnId":"turn-1","itemId":"final-1","delta":"改造完成"
		}`))
		a.handleCodexItemCompleted(json.RawMessage(`{
			"threadId":"thread-1","turnId":"turn-1","item":{"id":"final-1","type":"agentMessage","phase":"final_answer","text":"改造完成"}
		}`))
		a.handleCodexTurnEvent("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[]}}`))
		return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
	}

	var phases []codex.TurnPhaseEvent
	reply, err := a.ChatThreadWithProgress(context.Background(), "thread-1", codex.ChatRequest{Text: "开始改造", WorkspaceRoot: "/workspace"}, func(event codex.TurnPhaseEvent) {
		phases = append(phases, event)
	})
	if err != nil {
		t.Fatalf("ChatWithProgress() error: %v", err)
	}
	if reply != "改造完成" {
		t.Fatalf("reply = %q, want final answer only", reply)
	}
	want := []codex.TurnPhase{codex.TurnPhaseStarted, codex.TurnPhasePlanning, codex.TurnPhaseWorking, codex.TurnPhaseFinalizing, codex.TurnPhaseCompleted}
	if len(phases) != len(want) {
		t.Fatalf("phase events = %#v", phases)
	}
	for index := range want {
		if phases[index].TurnID != "turn-1" || phases[index].Phase != want[index] {
			t.Fatalf("phase[%d] = %#v, want %s", index, phases[index], want[index])
		}
	}
}

func TestCollectTurnIgnoresOtherTurnEventsOnSameThread(t *testing.T) {
	a := &Client{started: true, loadedThreads: map[string]bool{"thread-1": true}, threadStatus: make(map[string]codex.ThreadStatus), turnCh: make(map[string]chan *codexTurnEvent)}
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "turn/start" {
			t.Fatalf("unexpected rpc method: %s", method)
		}
		a.handleCodexPlanUpdated(json.RawMessage(`{"threadId":"thread-1","turnId":"turn-other","plan":[{"step":"不属于当前请求","status":"inProgress"}]}`))
		a.handleCodexItemCompleted(json.RawMessage(`{"threadId":"thread-1","turnId":"turn-other","item":{"id":"other","type":"agentMessage","phase":"final_answer","text":"错误答案"}}`))
		a.handleCodexItemCompleted(json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","item":{"id":"final","type":"agentMessage","phase":"final_answer","text":"正确答案"}}`))
		a.handleCodexTurnEvent("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[]}}`))
		return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
	}
	var phases []codex.TurnPhaseEvent
	reply, err := a.ChatThreadWithProgress(context.Background(), "thread-1", codex.ChatRequest{Text: "执行", WorkspaceRoot: "/workspace"}, func(event codex.TurnPhaseEvent) { phases = append(phases, event) })
	if err != nil || reply != "正确答案" || len(phases) != 1 || phases[0].Phase != codex.TurnPhaseCompleted {
		t.Fatalf("reply=%q phases=%#v err=%v", reply, phases, err)
	}
}

func TestChatCodexAppServerSendsTextAndLocalImages(t *testing.T) {
	a := &Client{
		started:       true,
		loadedThreads: map[string]bool{"thread-1": true},
		threadStatus:  make(map[string]codex.ThreadStatus),
		turnCh:        make(map[string]chan *codexTurnEvent),
	}
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "turn/start" {
			t.Fatalf("unexpected rpc method: %s", method)
		}
		got, ok := params.(codexTurnStartParams)
		if !ok {
			t.Fatalf("turn/start params type = %T", params)
		}
		if len(got.Input) != 3 {
			t.Fatalf("turn/start input = %#v", got.Input)
		}
		if got.Input[0].Type != "text" || got.Input[0].Text != "分析这两张截图" {
			t.Fatalf("unexpected text input: %#v", got.Input[0])
		}
		if got.Input[1].Type != "localImage" || got.Input[1].Path != "/tmp/one.png" {
			t.Fatalf("unexpected first image input: %#v", got.Input[1])
		}
		if got.Input[2].Type != "localImage" || got.Input[2].Path != "/tmp/two.jpg" {
			t.Fatalf("unexpected second image input: %#v", got.Input[2])
		}

		a.handleCodexItemCompleted(json.RawMessage(`{
			"threadId":"thread-1","turnId":"turn-1","item":{"id":"final-1","type":"agentMessage","phase":"final_answer","text":"图片分析完成"}
		}`))
		a.handleCodexTurnEvent("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[]}}`))
		return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
	}

	reply, err := a.ChatThread(context.Background(), "thread-1", codex.ChatRequest{
		Text:          "分析这两张截图",
		LocalImages:   []string{"/tmp/one.png", "/tmp/two.jpg"},
		WorkspaceRoot: "/workspace",
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if reply != "图片分析完成" {
		t.Fatalf("Chat() reply = %q", reply)
	}
}

func TestChatRequestPromptTextIncludesInboundFilesAndOutboxContract(t *testing.T) {
	request := codex.ChatRequest{
		Text: "检查构建失败原因",
		LocalFiles: []codex.LocalFile{{
			Path: "/tmp/turn/inbox/build.log", Name: "build.log",
			ContentType: "text/plain", Size: 42,
		}},
		ArtifactDir: "/tmp/turn/outbox",
	}
	prompt := request.PromptText()
	for _, want := range []string{"检查构建失败原因", "build.log", "/tmp/turn/inbox/build.log", "不要执行", "/tmp/turn/outbox", "自动发送"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("PromptText() missing %q: %q", want, prompt)
		}
	}
}

func TestChatCodexAppServerInterruptsCancelledTurn(t *testing.T) {
	a := &Client{
		started:       true,
		loadedThreads: map[string]bool{"thread-1": true},
		threadStatus:  make(map[string]codex.ThreadStatus),
		turnCh:        make(map[string]chan *codexTurnEvent),
	}
	interrupted := false
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "turn/start":
			return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
		case "turn/interrupt":
			got, ok := params.(map[string]string)
			if !ok || got["threadId"] != "thread-1" || got["turnId"] != "turn-1" {
				t.Fatalf("unexpected interrupt params: %#v", params)
			}
			interrupted = true
			return json.RawMessage(`{}`), nil
		default:
			t.Fatalf("unexpected rpc method: %s", method)
			return nil, nil
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.ChatThreadWithProgress(ctx, "thread-1", codex.ChatRequest{Text: "执行任务", WorkspaceRoot: "/workspace"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ChatWithProgress() error = %v, want context canceled", err)
	}
	if !interrupted {
		t.Fatal("cancelled turn should call turn/interrupt")
	}
}
