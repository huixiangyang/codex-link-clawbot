package management

import (
	"context"
	"errors"
	"fmt"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func pagination(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if size < 1 || size > 50 {
		size = 12
	}
	return page, size
}
func (s *ConsoleServer) taskView(task request.Task) map[string]any {
	receipt, err := s.deps.Requests.SummarizeResult(task.OwnerID, task.ID)
	delivery := "not_started"
	if err == nil {
		delivery = string(receipt.Outcome)
		if task.State.Terminal() && receipt.Outcome == request.DeliveryPending {
			delivery = string(request.DeliveryAmbiguous)
		}
	} else if task.ArchiveFailed && task.ResultExpiresAt > time.Now().Unix() {
		delivery = "awaiting_archive"
	} else if task.ExecutionCompletedAt > 0 {
		delivery = "unavailable"
	}
	now := time.Now().Unix()
	return map[string]any{
		"id": task.ID, "summary": task.Summary, "state": task.State, "stage": task.Stage, "reason": task.Reason, "execution": task.ExecutionStatus(), "delivery": delivery,
		"workspace_id": task.ProjectID, "thread_id": task.ThreadID, "target_id": task.TargetID, "created_at": task.CreatedAt, "started_at": task.StartedAt, "finished_at": task.FinishedAt,
		"input_expires_at": task.PayloadExpiresAt, "result_expires_at": task.ResultExpiresAt, "result_available": err == nil, "retry_of": task.RetryOf,
		"can_restore": task.ArchiveFailed && task.ResultExpiresAt > now, "archive_failed": task.ArchiveFailed, "can_release": task.State.Terminal(),
		"can_retry":     task.Reason != request.ReasonInterruptUnconfirmed && task.ExecutionCompletedAt == 0 && (task.State == request.StateFailed || task.State == request.StateInterrupted) && task.PayloadExpiresAt > now,
		"can_redeliver": task.State == request.StateSucceeded && err == nil, "can_cancel": task.State == request.StateRunning && task.ExecutionCompletedAt == 0,
	}
}
func (s *ConsoleServer) handleRequests(w http.ResponseWriter, r *http.Request) {
	view := r.URL.Query().Get("view")
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	items := []map[string]any{}
	counts := map[string]int{"active": 0, "attention": 0, "history": 0, "all": 0}
	for _, task := range s.deps.Requests.List(s.deps.OwnerID) {
		item := s.taskView(task)
		category := "history"
		if !task.State.Terminal() {
			category = "active"
		} else if task.ArchiveFailed || task.State == request.StateFailed || task.State == request.StateInterrupted || item["delivery"] == string(request.DeliveryExplicitFailure) || item["delivery"] == string(request.DeliveryAmbiguous) {
			category = "attention"
		}
		counts[category]++
		counts["all"]++
		if view != "" && view != "all" && view != category {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(task.ID+" "+task.Summary+" "+task.ProjectID), query) {
			continue
		}
		items = append(items, item)
	}
	page, size := pagination(r)
	total := len(items)
	pages := max(1, (total+size-1)/size)
	page = min(page, pages)
	start := (page - 1) * size
	end := min(total, start+size)
	writeJSON(w, 200, map[string]any{"items": items[start:end], "total": total, "page": page, "pages": pages, "counts": counts})
}
func (s *ConsoleServer) handleRequestDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, ok := s.deps.Requests.Find(s.deps.OwnerID, id)
	if !ok {
		writeError(w, 404, "请求不存在")
		return
	}
	value := s.taskView(task)
	if input, err := s.deps.Requests.LoadRequest(s.deps.OwnerID, id); err == nil {
		names := []string{}
		for _, a := range input.Images {
			names = append(names, a.Name)
		}
		for _, a := range input.Files {
			names = append(names, a.Name)
		}
		value["input"] = map[string]any{"text": input.Text, "attachments": names}
	} else {
		value["input_unavailable"] = "原始输入已过期或不可用"
	}
	if result, err := s.deps.Requests.InspectResult(s.deps.OwnerID, id); err == nil {
		artifacts := []map[string]any{}
		for index, a := range result.Artifacts {
			artifacts = append(artifacts, map[string]any{"name": a.Name, "size": a.Size, "url": fmt.Sprintf("/api/requests/%s/artifacts/%d", id, index)})
		}
		value["result"] = map[string]any{"reply": result.Reply, "artifacts": artifacts, "receipt": result.Receipt, "attempts": result.Attempts, "frozen_at": result.FrozenAt, "image_urls": result.ImageURLs}
	}
	writeJSON(w, 200, value)
}
func (s *ConsoleServer) handleArtifact(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		writeError(w, 400, "文件编号无效")
		return
	}
	file, artifact, err := s.deps.Requests.OpenArtifact(s.deps.OwnerID, r.PathValue("id"), index)
	if err != nil {
		writeError(w, 404, "文件已过期、损坏或无权访问")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": artifact.Name}))
	http.ServeContent(w, r, artifact.Name, time.Time{}, file)
}
func (s *ConsoleServer) handleRequestAction(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Action      string `json:"action"`
		OperationID string `json:"operation_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	id := r.PathValue("id")
	task, ok := s.deps.Requests.Find(s.deps.OwnerID, id)
	if !ok {
		writeError(w, 404, "请求不存在")
		return
	}
	var err error
	switch input.Action {
	case "release":
		err = s.deps.Requests.Release(s.deps.OwnerID, id)
	case "restore":
		if recovery, ok := s.deps.Recovery.(interface {
			RestoreResult(context.Context, string, string) error
		}); ok {
			err = recovery.RestoreResult(r.Context(), s.deps.OwnerID, id)
		} else {
			err = fmt.Errorf("恢复保存服务不可用")
		}
	case "cancel":
		if !s.deps.Execution.Cancel(s.deps.OwnerID, id) {
			writeError(w, 409, "这条请求已经结束或进入结果收尾，未取消其他请求")
			return
		}
	case "retry":
		var retried request.Task
		retried, err = s.deps.Recovery.Retry(r.Context(), s.deps.OwnerID, id, input.OperationID)
		if err == nil {
			writeJSON(w, 201, map[string]string{"id": retried.ID})
			return
		}
	case "redeliver":
		err = s.deps.Recovery.Redeliver(r.Context(), s.deps.OwnerID, id, input.OperationID)
	default:
		writeError(w, 400, "未知请求操作")
		return
	}
	if errors.Is(err, request.ErrSessionBusy) || errors.Is(err, request.ErrRejected) {
		s.sessionConflict(w, r, task, err.Error())
		return
	}
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
