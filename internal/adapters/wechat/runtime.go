package wechat

import (
	"context"
	"fmt"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/visual"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/voice"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/access"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/conversation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/delivery"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/workspace"
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
	Voice               *voice.Briefing
	Progress            execution.ProgressConfig
	VisualReplyEnabled  bool
	VisualReplyMinRunes int
	ManagementURL       string
}

type Runtime struct {
	Handler       *Handler
	Coordinator   *execution.Coordinator
	Conversations *conversation.Service
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
	threadClient, ok := dependencies.Codex.(codex.ThreadClient)
	if !ok {
		return nil, fmt.Errorf("Codex thread runtime is required")
	}
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
	if err := handler.expireDrafts(); err != nil {
		return nil, fmt.Errorf("initialize conversation drafts: %w", err)
	}
	channel := &executionChannel{
		sendBusy: handler.showRejected, tasks: dependencies.Requests, targets: dependencies.Targets,
		pendingNotices: dependencies.PendingNotices, remoteLock: dependencies.RemoteLock,
		clients: clients, sendReply: handler.sendCompletedTask,
	}
	executor, err := execution.NewRunner(execution.RunnerDependencies{Targets: dependencies.Targets, Codex: dependencies.Codex, Progress: dependencies.Progress, Workspaces: dependencies.Workspaces, Threads: dependencies.Threads, Requests: dependencies.Requests, Channel: channel})
	if err != nil {
		return nil, err
	}
	handler.runner = executor
	coordinator, err := execution.NewCoordinator(dependencies.Requests, executor)
	if err != nil {
		return nil, err
	}
	handler.coordinator = coordinator
	handler.clients = clients
	conversations, err := conversation.New(conversation.Dependencies{Workspaces: dependencies.Workspaces, Threads: dependencies.Threads, Targets: dependencies.Targets, Requests: dependencies.Requests, Client: threadClient, Control: coordinator})
	if err != nil {
		return nil, err
	}
	handler.conversations = conversations
	runtime := &Runtime{Handler: handler, Coordinator: coordinator, Conversations: conversations}
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

// VisualRenderer 只提供在用的菜单图和阅读图，不再保留旧通用卡片入口。
type VisualRenderer interface {
	RenderMenu(context.Context, visual.Menu) (*visual.Artifact, error)
	RenderDocument(context.Context, visual.Document) (*visual.Artifact, error)
}
