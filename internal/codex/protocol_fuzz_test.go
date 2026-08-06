package codex

import "testing"

func FuzzCodexEventDecoders(f *testing.F) {
	f.Add([]byte(`{"thread":{"id":"thread-1"}}`))
	f.Add([]byte(`{"threadId":"thread-1","item":{"id":"item-1","type":"agentMessage","text":"done","phase":"final"}}`))
	f.Add([]byte(`{"threadId":"thread-1","plan":[{"step":"检查","status":"inProgress"}]}`))
	f.Add([]byte(`{"threadId":"thread-1","turn":{"status":"completed"}}`))
	f.Add([]byte(`not-json`))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = decodeCodexThread(data, "fuzz")
		client := &Codex{turnCh: make(map[string]chan *codexTurnEvent)}
		// 这些处理器都接收 App Server 的不可信 JSON 通知，必须做到任意输入不崩溃。
		client.handleCodexItemDelta(data)
		client.handleCodexItemStarted(data)
		client.handleCodexItemCompleted(data)
		client.handleCodexPlanUpdated(data)
		client.handleCodexActivity(data, "测试活动")
		client.handleCodexTurnEvent("turn/completed", data)
	})
}
