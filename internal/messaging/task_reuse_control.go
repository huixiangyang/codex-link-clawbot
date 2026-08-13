package messaging

import (
	"context"
	"log"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/taskqueue"
)

func (h *Handler) latestSuccessfulTask(userID string, requireReusable bool) (taskqueue.Task, bool) {
	if h.tasks == nil {
		return taskqueue.Task{}, false
	}
	var latest taskqueue.Task
	for _, task := range h.tasks.List(userID) {
		if task.State != taskqueue.StateSucceeded || task.ThreadID == "" {
			continue
		}
		if requireReusable {
			if _, err := h.tasks.LoadReusablePrompt(userID, task.ID); err != nil {
				continue
			}
		}
		if latest.ID == "" || task.FinishedAt > latest.FinishedAt || task.FinishedAt == latest.FinishedAt && task.CreatedAt > latest.CreatedAt {
			latest = task
		}
	}
	return latest, latest.ID != ""
}

func (h *Handler) requestSuccessfulTaskRerun(userID, taskID string, newThread bool) ActionResult {
	if h.tasks == nil {
		return newActionResult(string(actionTaskRerun), DomainQueue, "codex-link-clawbot 请求队列当前不可用。")
	}
	task, exists := h.tasks.Find(userID, taskID)
	if !exists || task.State != taskqueue.StateSucceeded || task.ThreadID == "" {
		return newActionResult(string(actionTaskRerun), DomainQueue, "这条执行记录已经不能继续。发送“请求队列”刷新。")
	}
	prompt, err := h.tasks.LoadReusablePrompt(userID, task.ID)
	if err != nil {
		return newActionResult(string(actionTaskRerun), DomainQueue, "原始请求已过期，或请求包含附件，不能再次执行。")
	}
	result := effectActionResult(string(actionTaskRerun), DomainQueue, "正在创建后续请求。", EffectEnqueuePrompt, prompt).
		withProjectID(task.ProjectID)
	if newThread {
		return result.withNewThread()
	}
	return result.withThreadID(task.ThreadID)
}

func (h *Handler) continueTaskSession(ctx context.Context, userID, taskID string, page int) string {
	if h.tasks == nil || h.projects == nil || h.sessions == nil {
		return "执行记录关联的 Codex 线程当前不可用。"
	}
	task, exists := h.tasks.Find(userID, taskID)
	if !exists || task.State != taskqueue.StateSucceeded || task.ThreadID == "" {
		return "这条执行记录已经不能继续。发送“请求队列”刷新。"
	}
	if _, exists := h.projects.Get(task.ProjectID); !exists {
		return "执行记录所属的 Codex 工作空间已经不可用，未切换到其他目录。"
	}
	if page <= 0 {
		page = 1
	}
	return h.withRuntimeMutation(func() string {
		threadAgent, err := h.sessionContext()
		if err != nil {
			return "Codex 线程运行时当前不可用。"
		}
		previousProject := h.projects.Current(userID)
		projectChanged := previousProject.ID != task.ProjectID
		if projectChanged {
			if _, err := h.projects.Select(userID, task.ProjectID); err != nil {
				return "执行记录所属的 Codex 工作空间切换失败，当前工作空间未改变。"
			}
		}
		thread, err := h.sessions.Use(ctx, userID, threadAgent, task.ThreadID)
		if err != nil {
			if projectChanged {
				if _, rollbackErr := h.projects.Select(userID, previousProject.ID); rollbackErr != nil {
					log.Printf("[task] failed to restore project after session error for %s", ilink.LogLabel(userID))
					return "执行记录关联的 Codex 线程无法恢复，Codex 工作空间也未能自动还原。请发送“项目”检查当前状态。"
				}
			}
			return "执行记录关联的 Codex 线程已经不可用，Codex 工作空间未改变。"
		}
		log.Printf("[task] continued task=%s project=%s for %s", shortTaskID(task.ID), task.ProjectID, ilink.LogLabel(userID))
		options := []controlOption{
			{Label: "查看当前线程 · /status", Action: actionCurrentSession},
			{Label: "返回执行记录", Action: actionActivityDetail, Value: task.ID, Page: page},
			{Label: "codex-link-clawbot 请求队列", Action: actionActivityPage, Page: page},
		}
		prompt := strings.Join([]string{
			"已回到执行记录关联的 Codex 线程",
			"",
			"Codex 工作空间：" + task.ProjectID,
			"线程：" + threadTitle(thread),
			"",
			"下一条普通消息会继续这个线程。",
			"",
			renderControlOptions(options),
		}, "\n")
		if !h.storeChoiceWithBack(userID, viewTaskResult, options, controlOption{Action: actionActivityDetail, Value: task.ID, Page: page}) {
			return controlStateFailureResult().Text
		}
		return prompt + "\n\n回复数字继续，0 返回执行记录。"
	})
}
