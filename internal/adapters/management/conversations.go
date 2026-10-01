package management

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/execution"
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
	page, size := pagination(r)
	found, err := s.deps.Threads.GlobalList(r.Context(), s.deps.OwnerID, s.deps.Codex, s.deps.Workspaces.List(), false, false, r.URL.Query().Get("q"), page, size)
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
	if _, ok := s.deps.Workspaces.Get(input.WorkspaceID); !ok {
		writeError(w, 400, "工作空间不存在")
		return
	}
	intent, err := s.deps.Conversations.Select(r.Context(), s.deps.OwnerID, input.WorkspaceID, input.ThreadID)
	if err != nil {
		writeError(w, 409, "会话切换失败，请刷新当前目标后重试")
		return
	}
	writeJSON(w, 200, intent)
}

func (s *ConsoleServer) handleConversationNew(w http.ResponseWriter, r *http.Request) {
	var input struct {
		WorkspaceID string `json:"workspace_id"`
		Name        string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if _, ok := s.deps.Workspaces.Get(input.WorkspaceID); !ok {
		writeError(w, 400, "工作空间不存在")
		return
	}
	intent, err := s.deps.Conversations.Create(r.Context(), s.deps.OwnerID, input.WorkspaceID, input.Name)
	if err != nil {
		writeError(w, 409, "新建会话失败，请刷新当前目标后重试")
		return
	}
	writeJSON(w, 201, intent)
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
	var err error
	switch input.Action {
	case "interrupt":
		err = s.deps.Conversations.Interrupt(r.Context(), s.deps.OwnerID, "", id, input.TaskID, input.TurnID)
	case "rename":
		err = s.deps.Conversations.Rename(r.Context(), id, input.Name)
	case "archive":
		err = s.deps.Conversations.Archive(r.Context(), s.deps.OwnerID, id)
	default:
		err = fmt.Errorf("未知会话操作")
	}
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
