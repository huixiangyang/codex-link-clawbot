package wechat

import (
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/conversation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
)

// 测试夹具保存可配置的执行依赖，生产 Handler 不再持有 Codex 运行时。
type testHandler struct {
	*Handler
	codex    codex.Runtime
	progress execution.ProgressConfig
}

func newBareHandler(runtime codex.Runtime) *testHandler {
	return &testHandler{
		Handler: &Handler{visualReplyEnabled: true, visualReplyMinRunes: 900},
		codex:   runtime,
		progress: execution.ProgressConfig{
			Enabled: true, TypingInterval: 8 * time.Second,
			FirstMessageDelay: 15 * time.Second,
		},
	}
}

func newTestCoordinator(handler *testHandler, store *request.Store) (*execution.Coordinator, error) {
	handler.clients = &clientRegistry{}
	handler.sessionClient, _ = handler.codex.(codex.ThreadClient)
	if handler.targets == nil {
		var err error
		handler.targets, err = target.Open(store.Root(), handler.projects.List()[0].ID)
		if err != nil {
			return nil, err
		}
	}
	channel := &executionChannel{
		sendBusy:   handler.showRejected,
		tasks:      store,
		targets:    handler.targets,
		remoteLock: handler.remoteLock, pendingNotices: handler.pendingNotices,
		clients: handler.clients, sendReply: handler.sendCompletedTask,
	}
	executor, err := execution.NewRunner(execution.RunnerDependencies{Targets: handler.targets, Codex: handler.codex, Progress: handler.progress, Workspaces: handler.projects, Threads: handler.sessions, Requests: store, Channel: channel})
	if err != nil {
		return nil, err
	}
	handler.runner = executor
	coordinator, err := execution.NewCoordinator(store, executor)
	if err != nil {
		return nil, err
	}
	if handler.sessions != nil && handler.sessionClient != nil {
		handler.conversations, err = conversation.New(conversation.Dependencies{Workspaces: handler.projects, Threads: handler.sessions, Targets: handler.targets, Requests: store, Client: handler.sessionClient, Control: coordinator})
	}
	return coordinator, err
}
