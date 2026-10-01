// Package conversation 统一各入口的会话操作，不负责渠道协议和页面呈现。
package conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/workspace"
)

type Control interface {
	WithIdleSession(string, string, func() error) error
	Cancel(string, string) bool
}

type Dependencies struct {
	Workspaces *workspace.Manager
	Threads    *thread.Manager
	Targets    *target.Store
	Requests   *request.Store
	Client     codex.ThreadClient
	Control    Control
}

type Service struct{ deps Dependencies }

func New(deps Dependencies) (*Service, error) {
	if deps.Workspaces == nil || deps.Threads == nil || deps.Targets == nil || deps.Requests == nil || deps.Client == nil || deps.Control == nil {
		return nil, fmt.Errorf("conversation dependencies incomplete")
	}
	return &Service{deps: deps}, nil
}

func (s *Service) Select(ctx context.Context, owner, workspaceID, threadID string) (target.Intent, error) {
	definition, ok := s.deps.Workspaces.Get(workspaceID)
	if !ok {
		return target.Intent{}, fmt.Errorf("工作空间不存在")
	}
	if threadID == "" {
		return s.deps.Targets.SelectWorkspace(owner, workspaceID)
	}
	expected := s.deps.Targets.Snapshot(owner)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info, err := s.deps.Threads.UseGlobalThread(ctx, owner, definition, threadID, s.deps.Client)
	if err != nil {
		return target.Intent{}, err
	}
	if err := ctx.Err(); err != nil {
		return target.Intent{}, err
	}
	return s.deps.Targets.SelectThread(owner, workspaceID, info.ID, expected)
}

func (s *Service) Create(ctx context.Context, owner, workspaceID, name string) (target.Intent, error) {
	definition, ok := s.deps.Workspaces.Get(workspaceID)
	if !ok {
		return target.Intent{}, fmt.Errorf("工作空间不存在")
	}
	expected := s.deps.Targets.Snapshot(owner)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info, err := s.deps.Threads.OpenTaskThread(ctx, owner, definition, "", s.deps.Client, name)
	if err != nil {
		return target.Intent{}, err
	}
	var intent target.Intent
	if err = ctx.Err(); err == nil {
		intent, err = s.deps.Targets.SelectThread(owner, workspaceID, info.ID, expected)
	}
	if err != nil {
		// 远端已创建、本地未提交时补偿；取消请求不能让清理无限等待。
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		cleanupErr := s.deps.Client.ArchiveThread(cleanup, info.ID)
		s.deps.Threads.Invalidate()
		return target.Intent{}, errors.Join(err, cleanupErr)
	}
	return intent, nil
}

func (s *Service) Trusted(ctx context.Context, id string) (codex.ThreadInfo, workspace.Definition, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	info, err := s.deps.Client.ReadThread(ctx, id)
	if err != nil {
		return info, workspace.Definition{}, err
	}
	definition, allowed := s.deps.Workspaces.Match(info.Cwd)
	if !allowed || id == "" || info.ID != id {
		return info, workspace.Definition{}, fmt.Errorf("会话不在受信任工作空间")
	}
	return info, definition, nil
}

// Interrupt 始终验证精确请求或轮次，不会退化为打断当前任意工作。
func (s *Service) Interrupt(ctx context.Context, owner, targetID, threadID, taskID, turnID string) error {
	if taskID != "" {
		task, ok := s.deps.Requests.Find(owner, taskID)
		if !ok || threadID != "" && task.ThreadID != threadID || targetID != "" && task.TargetID != targetID || !s.deps.Control.Cancel(owner, taskID) {
			return fmt.Errorf("这次执行已结束或正在收尾，未打断其他执行")
		}
		return nil
	}
	client, ok := s.deps.Client.(codex.SessionControl)
	if !ok || threadID == "" || turnID == "" {
		return fmt.Errorf("请刷新状态后再打断")
	}
	info, _, err := s.Trusted(ctx, threadID)
	if err != nil {
		return err
	}
	if targetID != "" {
		intent, err := s.deps.Targets.Resolve(owner, targetID)
		definition, allowed := s.deps.Workspaces.Get(intent.WorkspaceID)
		if err != nil || intent.ThreadID != threadID || !allowed || !workspace.Contains(info.Cwd, definition.Root) {
			return fmt.Errorf("会话目标已失效")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return client.InterruptTurn(ctx, threadID, turnID)
}

func (s *Service) Rename(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > thread.MaxSessionName || strings.ContainsAny(name, "\r\n\x00") {
		return fmt.Errorf("名称应为 1–80 个单行字符")
	}
	if _, _, err := s.Trusted(ctx, id); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.deps.Client.SetThreadName(ctx, id, name); err != nil {
		return err
	}
	s.deps.Threads.Invalidate()
	return nil
}

func (s *Service) Archive(ctx context.Context, owner, id string) error {
	return s.deps.Control.WithIdleSession(owner, id, func() error {
		info, _, err := s.Trusted(ctx, id)
		if err != nil {
			return err
		}
		if info.Status.Type == "active" {
			return codex.ErrThreadBusy
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := s.deps.Client.ArchiveThread(ctx, id); err != nil {
			return err
		}
		s.deps.Threads.Invalidate()
		return s.deps.Targets.ClearThread(owner, id)
	})
}
