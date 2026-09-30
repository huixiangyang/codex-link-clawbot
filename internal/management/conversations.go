package management

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/workspace"
)

func threadTitle(info codex.ThreadInfo) string {
	if name := strings.TrimSpace(info.Name); name != "" {
		return name
	}
	if preview := strings.TrimSpace(info.Preview); preview != "" {
		return preview
	}
	return "未命名会话"
}
func (s *ConsoleServer) handleConversations(w http.ResponseWriter, r *http.Request) {
	workspaces := []thread.Workspace{}
	for _, item := range s.deps.Workspaces.List() {
		workspaces = append(workspaces, thread.Workspace{ID: item.ID, Name: item.Name, Root: item.Root})
	}
	page, size := pagination(r)
	found, err := s.deps.Threads.GlobalList(r.Context(), s.deps.OwnerID, s.deps.Codex, workspaces, false, false, r.URL.Query().Get("q"), page, size)
	if err != nil {
		writeError(w, 503, "暂时无法读取会话，请刷新重试")
		return
	}
	current := s.deps.Targets.Current(s.deps.OwnerID)
	items := []map[string]any{}
	for _, item := range found.Items {
		items = append(items, map[string]any{"id": item.Info.ID, "title": threadTitle(item.Info), "workspace_id": item.WorkspaceID, "workspace_name": item.WorkspaceName, "updated_at": item.Info.UpdatedAt, "status": item.Info.Status.Type, "activity": execution.CatalogActivity(s.deps.Requests, s.deps.OwnerID, "", item.Info.ID, item.Info.Status), "current": item.Info.ID == current.ThreadID && item.WorkspaceID == current.WorkspaceID})
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": found.Total, "page": found.Number, "pages": found.TotalPages})
}
func (s *ConsoleServer) handleTarget(w http.ResponseWriter, r *http.Request) {
	var input struct {
		WorkspaceID string `json:"workspace_id"`
		ThreadID    string `json:"thread_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	definition, ok := s.deps.Workspaces.Get(input.WorkspaceID)
	if !ok {
		writeError(w, 400, "工作空间不存在")
		return
	}
	if input.ThreadID == "" {
		intent, err := s.deps.Targets.Select(s.deps.OwnerID, definition.ID, nil)
		if err != nil {
			writeError(w, 500, "无法保存目标")
			return
		}
		writeJSON(w, 200, intent)
		return
	}
	var actionErr error
	func() {
		_, actionErr = s.deps.Targets.Select(s.deps.OwnerID, definition.ID, func() (string, error) {
			info, err := s.deps.Threads.UseGlobalThread(r.Context(), s.deps.OwnerID, thread.Workspace{ID: definition.ID, Name: definition.Name, Root: definition.Root}, input.ThreadID, s.deps.Codex)
			return info.ID, err
		})
	}()
	if actionErr != nil {
		writeError(w, 409, "会话切换失败，原目标保持不变")
		return
	}
	writeJSON(w, 200, s.deps.Targets.Current(s.deps.OwnerID))
}
func (s *ConsoleServer) handleConversationNew(w http.ResponseWriter, r *http.Request) {
	var input struct {
		WorkspaceID string `json:"workspace_id"`
		Name        string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	definition, ok := s.deps.Workspaces.Get(input.WorkspaceID)
	if !ok {
		writeError(w, 400, "工作空间不存在")
		return
	}
	var actionErr error
	func() {
		created := ""
		_, actionErr = s.deps.Targets.Select(s.deps.OwnerID, definition.ID, func() (string, error) {
			info, err := s.deps.Threads.OpenTaskThread(r.Context(), s.deps.OwnerID, thread.Workspace{ID: definition.ID, Name: definition.Name, Root: definition.Root}, "", s.deps.Codex, input.Name)
			if err == nil {
				created = info.ID
			}
			return info.ID, err
		})
		if actionErr != nil && created != "" {
			_ = s.deps.Codex.ArchiveThread(r.Context(), created)
		}
	}()
	if actionErr != nil {
		writeError(w, 409, "新建会话失败，原目标保持不变")
		return
	}
	writeJSON(w, 201, s.deps.Targets.Current(s.deps.OwnerID))
}
func (s *ConsoleServer) trustedThread(ctx context.Context, id string) (codex.ThreadInfo, workspace.Definition, error) {
	info, err := s.deps.Codex.ReadThread(ctx, id)
	if err != nil {
		return info, workspace.Definition{}, err
	}
	cwd, err := filepath.EvalSymlinks(info.Cwd)
	if err != nil {
		return info, workspace.Definition{}, err
	}
	for _, definition := range s.deps.Workspaces.List() {
		root, err := filepath.EvalSymlinks(definition.Root)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(root, cwd)
		if err == nil && (relative == "." || filepath.IsLocal(relative)) {
			return info, definition, nil
		}
	}
	return info, workspace.Definition{}, fmt.Errorf("会话不在受信任工作空间")
}
func (s *ConsoleServer) handleConversationAction(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Action string `json:"action"`
		TaskID string `json:"task_id"`
		TurnID string `json:"turn_id"`
		Name   string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	id := r.PathValue("id")
	var actionErr error
	func() {
		if _, _, actionErr = s.trustedThread(r.Context(), id); actionErr != nil {
			return
		}
		switch input.Action {
		case "interrupt":
			if input.TaskID != "" {
				task, ok := s.deps.Requests.Find(s.deps.OwnerID, input.TaskID)
				if !ok || task.ThreadID != id || !s.deps.Execution.Cancel(s.deps.OwnerID, input.TaskID) {
					actionErr = fmt.Errorf("这次执行已结束或正在收尾，未打断其他执行")
				}
			} else if client, ok := s.deps.Codex.(codex.SessionControl); ok && input.TurnID != "" {
				actionErr = client.InterruptTurn(r.Context(), id, input.TurnID)
			} else {
				actionErr = fmt.Errorf("请刷新状态后再打断")
			}
		case "rename":
			name := strings.TrimSpace(input.Name)
			if name == "" || len([]rune(name)) > 80 || strings.ContainsAny(name, "\r\n\x00") {
				actionErr = fmt.Errorf("名称应为 1–80 个单行字符")
				return
			}
			actionErr = s.deps.Codex.SetThreadName(r.Context(), id, name)
		case "archive":
			actionErr = s.deps.Execution.WithIdleSession(s.deps.OwnerID, id, func() error {
				info, err := s.deps.Codex.ReadThread(r.Context(), id)
				if err != nil {
					return err
				}
				if info.Status.Type == "active" {
					return fmt.Errorf("会话正在执行，请先打断")
				}
				if err := s.deps.Codex.ArchiveThread(r.Context(), id); err != nil {
					return err
				}
				return s.deps.Targets.ClearThread(s.deps.OwnerID, id)
			})
		default:
			actionErr = fmt.Errorf("未知会话操作")
		}
	}()
	if actionErr != nil {
		writeError(w, 409, actionErr.Error())
		return
	}
	s.deps.Threads.Invalidate()
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func threadWithinWorkspace(cwd, root string) bool {
	canonicalCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return false
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalCwd)
	return err == nil && (relative == "." || filepath.IsLocal(relative))
}
