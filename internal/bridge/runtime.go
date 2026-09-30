package bridge

import (
	"fmt"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/access"
	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/delivery"
	"github.com/huixiangyang/codex-link-clawbot/internal/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/workspace"
)

type Dependencies struct {
	Targets             *target.Store
	Codex               codex.Runtime
	Workspaces          *workspace.Manager
	Threads             *thread.Manager
	Visual              VisualRenderer
	Preferences         *preference.Store
	Requests            *request.Store
	Lifecycle           Lifecycle
	PendingNotices      *delivery.NoticeStore
	RemoteLock          *access.RemoteLock
	Voice               *VoiceBriefing
	Progress            execution.ProgressConfig
	VisualReplyEnabled  bool
	VisualReplyMinRunes int
	ManagementURL       string
}

type Runtime struct {
	Handler     *Handler
	Coordinator *execution.Coordinator
}

// NewRuntime 原子构造消息入口和按会话互斥的执行服务，不允许运行期补注入依赖。
func NewRuntime(dependencies Dependencies) (*Runtime, error) {
	if dependencies.Targets == nil || dependencies.Codex == nil || dependencies.Workspaces == nil || dependencies.Threads == nil || dependencies.Preferences == nil ||
		dependencies.Requests == nil || dependencies.Lifecycle == nil ||
		dependencies.PendingNotices == nil || dependencies.RemoteLock == nil {
		return nil, fmt.Errorf("bridge dependencies are incomplete")
	}
	if err := dependencies.Progress.Validate(); err != nil {
		return nil, err
	}
	minimumRunes := dependencies.VisualReplyMinRunes
	if minimumRunes <= 0 {
		minimumRunes = 900
	}
	handler := &Handler{
		targets:  dependencies.Targets,
		projects: dependencies.Workspaces, sessions: dependencies.Threads,
		visual: dependencies.Visual, preferences: dependencies.Preferences, tasks: dependencies.Requests,
		lifecycle: dependencies.Lifecycle, pendingNotices: dependencies.PendingNotices,
		remoteLock: dependencies.RemoteLock, voice: dependencies.Voice, managementURL: strings.TrimRight(strings.TrimSpace(dependencies.ManagementURL), "/"),
		visualReplyEnabled: dependencies.VisualReplyEnabled, visualReplyMinRunes: minimumRunes,
	}
	clients := &clientRegistry{}
	executor := &taskExecutor{
		sendBusy: handler.showRejected,
		targets:  dependencies.Targets,
		codex:    dependencies.Codex, progress: dependencies.Progress,
		projects: dependencies.Workspaces, sessions: dependencies.Threads, tasks: dependencies.Requests,
		pendingNotices: dependencies.PendingNotices, remoteLock: dependencies.RemoteLock,
		clients: clients, sendReply: handler.sendCompletedTask,
	}
	coordinator, err := execution.NewCoordinator(dependencies.Requests, executor)
	if err != nil {
		return nil, err
	}
	handler.coordinator = coordinator
	handler.clients = clients
	runtime := &Runtime{Handler: handler, Coordinator: coordinator}
	threadClient, _ := dependencies.Codex.(codex.ThreadClient)
	handler.sessionClient = threadClient
	handler.menus = newNumberMenus(handler, threadClient, runtime)
	return runtime, nil
}

// RegisterClient 注册启动时可用的微信出口。
func (r *Runtime) RegisterClient(client *ilink.Client) {
	if client != nil {
		r.Handler.clients.register(client.OwnerUserID(), client)

	}
}
