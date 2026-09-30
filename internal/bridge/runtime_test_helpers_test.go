package bridge

import (
	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/target"
	"path/filepath"
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
		codex:   runtime, progress: execution.DefaultProgressConfig(),
	}
}

func newTestCoordinator(handler *testHandler, store *request.Store) (*execution.Coordinator, error) {
	handler.clients = &clientRegistry{}
	handler.sessionClient, _ = handler.codex.(codex.ThreadClient)
	if handler.targets == nil {
		var err error
		handler.targets, err = target.Open(filepath.Join(store.Root(), "targets.json"), handler.projects.List()[0].ID)
		if err != nil {
			return nil, err
		}
	}
	executor := &taskExecutor{
		sendBusy: handler.showRejected,
		targets:  handler.targets,
		codex:    handler.codex, progress: handler.progress,
		projects: handler.projects, sessions: handler.sessions, tasks: store,
		remoteLock: handler.remoteLock, pendingNotices: handler.pendingNotices,
		clients: handler.clients, sendReply: handler.sendCompletedTask,
	}
	return execution.NewCoordinator(store, executor)
}
