package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func newProgressTestAgent(threadID string) (*ACPAgent, chan *codexTurnEvent) {
	ch := make(chan *codexTurnEvent, 4)
	return &ACPAgent{turnCh: map[string]chan *codexTurnEvent{threadID: ch}}, ch
}

func TestHandleCodexPlanUpdated(t *testing.T) {
	agent, ch := newProgressTestAgent("thread-1")
	agent.handleCodexPlanUpdated(json.RawMessage(`{
		"threadId":"thread-1",
		"plan":[
			{"step":"检查环境","status":"completed"},
			{"step":"替换服务","status":"inProgress"},
			{"step":"验证链路","status":"pending"}
		]
	}`))

	event := <-ch
	if event.Kind != "plan" || event.Completed != 1 || event.Total != 3 {
		t.Fatalf("unexpected plan event: %#v", event)
	}
	if event.Text != "替换服务" {
		t.Fatalf("event.Text = %q, want %q", event.Text, "替换服务")
	}
}

func TestHandleCodexItemCompletedKeepsMessagePhase(t *testing.T) {
	agent, ch := newProgressTestAgent("thread-1")
	agent.handleCodexItemCompleted(json.RawMessage(`{
		"threadId":"thread-1",
		"item":{"id":"msg-1","type":"agentMessage","phase":"commentary","text":"正在检查服务"}
	}`))

	event := <-ch
	if event.Kind != "message_completed" || event.Phase != "commentary" || event.Text != "正在检查服务" {
		t.Fatalf("unexpected message event: %#v", event)
	}
}

func TestHandleCodexActivityDoesNotExposeRawOutput(t *testing.T) {
	agent, ch := newProgressTestAgent("thread-1")
	agent.handleCodexActivity(json.RawMessage(`{
		"threadId":"thread-1",
		"delta":"SECRET_TOKEN=must-not-leak"
	}`), "正在执行本机操作")

	event := <-ch
	if event.Text != "正在执行本机操作" {
		t.Fatalf("activity exposed unexpected text: %#v", event)
	}
}

func TestChatCodexAppServerSeparatesCommentaryAndFinalAnswer(t *testing.T) {
	a := &ACPAgent{
		started:  true,
		protocol: protocolCodexAppServer,
		threads:  map[string]string{"user-1": "thread-1"},
		turnCh:   make(map[string]chan *codexTurnEvent),
	}
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "turn/start" {
			t.Fatalf("unexpected rpc method: %s", method)
		}
		a.handleCodexItemStarted(json.RawMessage(`{
			"threadId":"thread-1","item":{"id":"comment-1","type":"agentMessage","phase":"commentary","text":""}
		}`))
		a.handleCodexItemDelta(json.RawMessage(`{
			"threadId":"thread-1","itemId":"comment-1","delta":"正在检查本机服务"
		}`))
		a.handleCodexItemCompleted(json.RawMessage(`{
			"threadId":"thread-1","item":{"id":"comment-1","type":"agentMessage","phase":"commentary","text":"正在检查本机服务"}
		}`))
		a.handleCodexPlanUpdated(json.RawMessage(`{
			"threadId":"thread-1","plan":[{"step":"验证链路","status":"inProgress"}]
		}`))
		a.handleCodexItemStarted(json.RawMessage(`{
			"threadId":"thread-1","item":{"id":"final-1","type":"agentMessage","phase":"final_answer","text":""}
		}`))
		a.handleCodexItemDelta(json.RawMessage(`{
			"threadId":"thread-1","itemId":"final-1","delta":"改造完成"
		}`))
		a.handleCodexItemCompleted(json.RawMessage(`{
			"threadId":"thread-1","item":{"id":"final-1","type":"agentMessage","phase":"final_answer","text":"改造完成"}
		}`))
		a.handleCodexTurnEvent("turn/completed", json.RawMessage(`{"threadId":"thread-1"}`))
		return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
	}

	var progress []ProgressEvent
	reply, err := a.ChatWithProgress(context.Background(), "user-1", ChatRequest{Text: "开始改造"}, func(event ProgressEvent) {
		progress = append(progress, event)
	})
	if err != nil {
		t.Fatalf("ChatWithProgress() error: %v", err)
	}
	if reply != "改造完成" {
		t.Fatalf("reply = %q, want final answer only", reply)
	}
	if len(progress) != 2 || progress[0].Kind != ProgressCommentary || progress[1].Kind != ProgressPlan {
		t.Fatalf("unexpected progress events: %#v", progress)
	}
}

func TestChatCodexAppServerSendsTextAndLocalImages(t *testing.T) {
	a := &ACPAgent{
		started:  true,
		protocol: protocolCodexAppServer,
		threads:  map[string]string{"user-1": "thread-1"},
		turnCh:   make(map[string]chan *codexTurnEvent),
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
			"threadId":"thread-1","item":{"id":"final-1","type":"agentMessage","phase":"final_answer","text":"图片分析完成"}
		}`))
		a.handleCodexTurnEvent("turn/completed", json.RawMessage(`{"threadId":"thread-1"}`))
		return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
	}

	reply, err := a.Chat(context.Background(), "user-1", ChatRequest{
		Text:        "分析这两张截图",
		LocalImages: []string{"/tmp/one.png", "/tmp/two.jpg"},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if reply != "图片分析完成" {
		t.Fatalf("Chat() reply = %q", reply)
	}
}

func TestChatRequestPromptTextIncludesInboundFilesAndOutboxContract(t *testing.T) {
	request := ChatRequest{
		Text: "检查构建失败原因",
		LocalFiles: []LocalFile{{
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

func TestLegacyACPRejectsLocalImages(t *testing.T) {
	a := &ACPAgent{started: true, protocol: protocolLegacyACP}
	_, err := a.Chat(context.Background(), "user-1", ChatRequest{
		Text:        "分析图片",
		LocalImages: []string{"/tmp/image.png"},
	})
	if err == nil || !strings.Contains(err.Error(), "does not support image input") {
		t.Fatalf("Chat() error = %v, want unsupported image error", err)
	}
}

func TestChatCodexAppServerInterruptsCancelledTurn(t *testing.T) {
	a := &ACPAgent{
		started:  true,
		protocol: protocolCodexAppServer,
		threads:  map[string]string{"user-1": "thread-1"},
		turnCh:   make(map[string]chan *codexTurnEvent),
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
	_, err := a.ChatWithProgress(ctx, "user-1", ChatRequest{Text: "执行任务"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ChatWithProgress() error = %v, want context canceled", err)
	}
	if !interrupted {
		t.Fatal("cancelled turn should call turn/interrupt")
	}
}
