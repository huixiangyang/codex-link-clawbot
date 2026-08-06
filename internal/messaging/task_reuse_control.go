package messaging

import (
	"context"
	"log"
	"strings"

	"github.com/huixiangyang/weclaw/internal/ilink"
	"github.com/huixiangyang/weclaw/internal/taskqueue"
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
		return newActionResult(string(actionTaskRerun), DomainTask, "任务队列当前不可用。")
	}
	task, exists := h.tasks.Find(userID, taskID)
	if !exists || task.State != taskqueue.StateSucceeded || task.ThreadID == "" {
		return newActionResult(string(actionTaskRerun), DomainTask, "这个任务已经不能继续。发送“任务中心”刷新。")
	}
	prompt, err := h.tasks.LoadReusablePrompt(userID, task.ID)
	if err != nil {
		return newActionResult(string(actionTaskRerun), DomainTask, "原始请求已过期，或任务包含附件，不能再次执行。")
	}
	result := effectActionResult(string(actionTaskRerun), DomainTask, "正在创建后续任务。", EffectEnqueuePrompt, prompt).
		withProjectID(task.ProjectID)
	if newThread {
		return result.withNewThread()
	}
	return result.withThreadID(task.ThreadID)
}

func (h *Handler) continueTaskSession(ctx context.Context, userID, taskID string, page int) string {
	if h.tasks == nil || h.projects == nil || h.sessions == nil {
		return "任务会话当前不可用。"
	}
	task, exists := h.tasks.Find(userID, taskID)
	if !exists || task.State != taskqueue.StateSucceeded || task.ThreadID == "" {
		return "这个任务已经不能继续。发送“任务中心”刷新。"
	}
	if _, exists := h.projects.Get(task.ProjectID); !exists {
		return "任务所属项目已经不可用，未切换到其他项目。"
	}
	if page <= 0 {
		page = 1
	}
	return h.withRuntimeMutation(func() string {
		threadAgent, err := h.sessionContext()
		if err != nil {
			return "Codex 会话运行时当前不可用。"
		}
		previousProject := h.projects.Current(userID)
		projectChanged := previousProject.ID != task.ProjectID
		if projectChanged {
			if _, err := h.projects.Select(userID, task.ProjectID); err != nil {
				return "任务所属项目切换失败，当前项目未改变。"
			}
		}
		thread, err := h.sessions.Use(ctx, userID, threadAgent, task.ThreadID)
		if err != nil {
			if projectChanged {
				if _, rollbackErr := h.projects.Select(userID, previousProject.ID); rollbackErr != nil {
					log.Printf("[task] failed to restore project after session error for %s", ilink.LogLabel(userID))
					return "任务会话无法恢复，项目状态也未能自动还原。请发送“项目”检查当前状态。"
				}
			}
			return "任务会话已经不可用，项目状态未改变。"
		}
		log.Printf("[task] continued task=%s project=%s for %s", shortTaskID(task.ID), task.ProjectID, ilink.LogLabel(userID))
		options := []controlOption{
			{Label: "查看当前会话", Action: actionCurrentSession},
			{Label: "返回任务详情", Action: actionActivityDetail, Value: task.ID, Page: page},
			{Label: "任务中心", Action: actionActivityPage, Page: page},
		}
		prompt := strings.Join([]string{
			"已回到任务会话",
			"",
			"项目：" + task.ProjectID,
			"会话：" + threadTitle(thread),
			"",
			"下一条普通消息会继续这个会话。",
			"",
			renderControlOptions(options),
		}, "\n")
		if !h.storeChoiceWithBack(userID, viewTaskResult, options, controlOption{Action: actionActivityDetail, Value: task.ID, Page: page}) {
			return controlStateFailureResult().Text
		}
		return prompt + "\n\n回复数字继续，0 返回任务详情。"
	})
}
