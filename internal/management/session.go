package management

import (
	"context"
	"net/http"

	"github.com/huixiangyang/codex-link-clawbot/internal/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
)

func (s *ConsoleServer) sessionView(ctx context.Context, targetID, threadID string) execution.Activity {
	value, _ := execution.ReadActivity(ctx, s.deps.Requests, s.deps.Codex, s.deps.OwnerID, targetID, threadID)
	return value
}

func (s *ConsoleServer) sessionConflict(w http.ResponseWriter, r *http.Request, task request.Task, message string) {
	writeJSON(w, 409, map[string]any{"error": "提交失败：" + message, "code": "session_busy", "session": s.sessionView(r.Context(), task.TargetID, task.ThreadID)})
}

func (s *ConsoleServer) handleSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, _, err := s.trustedThread(r.Context(), id); err != nil {
		writeError(w, 404, "会话不可用")
		return
	}
	writeJSON(w, 200, s.sessionView(r.Context(), "", id))
}
