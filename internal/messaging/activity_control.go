package messaging

import (
	"fmt"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/session"
	"github.com/huixiangyang/codex-link-clawbot/internal/taskqueue"
)

func (h *Handler) openActivities(userID string, pageNumber int) string {
	if h.tasks == nil {
		return "codex-link-clawbot 请求队列\n\n持久请求队列当前不可用。"
	}
	tasks := h.tasks.List(userID)
	status := h.tasks.Status(userID)
	if len(tasks) == 0 {
		queueAction := controlOption{Label: "暂停队列", Action: actionQueuePause}
		if status.Paused {
			queueAction = controlOption{Label: "继续队列", Action: actionQueueResume}
		}
		options := []controlOption{queueAction}
		prompt := "codex-link-clawbot 请求队列\n\n还没有执行记录。直接发送文字、图片或文件即可可靠入队。\n\n" + renderControlOptions(options)
		if !h.storeChoice(userID, viewTaskCenter, options, actionMain) {
			return controlStateFailureResult().Text
		}
		return prompt + "\n\n回复数字操作，0 返回。"
	}
	if pageNumber <= 0 {
		pageNumber = 1
	}
	totalPages := (len(tasks) + controlSessionPageSize - 1) / controlSessionPageSize
	if pageNumber > totalPages {
		return fmt.Sprintf("请求队列页面不存在：%d / %d。发送“请求队列”刷新。", pageNumber, totalPages)
	}
	start := (pageNumber - 1) * controlSessionPageSize
	end := start + controlSessionPageSize
	if end > len(tasks) {
		end = len(tasks)
	}
	options := make([]controlOption, 0, controlSessionPageSize+2)
	for _, task := range tasks[start:end] {
		options = append(options, controlOption{
			Label:  normalizeSessionLine(task.Summary, 34) + " · " + taskStateText(task.State),
			Action: actionActivityDetail, Value: task.ID, Page: pageNumber,
		})
	}
	if pageNumber > 1 {
		options = append(options, controlOption{
			Label: fmt.Sprintf("上一页 · %d/%d", pageNumber-1, totalPages), Action: actionActivityPage, Page: pageNumber - 1,
		})
	}
	if pageNumber < totalPages {
		options = append(options, controlOption{
			Label: fmt.Sprintf("下一页 · %d/%d", pageNumber+1, totalPages), Action: actionActivityPage, Page: pageNumber + 1,
		})
	}
	if status.Paused {
		options = append(options, controlOption{Label: "继续队列", Action: actionQueueResume})
	} else {
		options = append(options, controlOption{Label: "暂停队列", Action: actionQueuePause})
	}
	if status.Queued > 0 {
		options = append(options, controlOption{Label: "清空等待请求", Action: actionConfirmQueueClear})
	}
	paused := "否"
	if status.Paused {
		paused = "是"
	}
	prompt := strings.Join([]string{
		"codex-link-clawbot 请求队列",
		"",
		fmt.Sprintf("页码：%d / %d", pageNumber, totalPages),
		fmt.Sprintf("等待：%d", status.Queued),
		fmt.Sprintf("执行：%d", status.Running+status.Delivering),
		"已暂停：" + paused,
		"",
		renderControlOptions(options),
	}, "\n")
	if !h.storeChoice(userID, viewTaskCenter, options, actionMain) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字查看详情，或说“下一页”“上一页”；0 返回。"
}

func (h *Handler) openActivityDetail(userID, id string, pageNumber int) string {
	if h.tasks == nil {
		return "codex-link-clawbot 执行记录\n\n持久请求队列当前不可用。"
	}
	task, ok := h.tasks.Find(userID, id)
	if !ok {
		return "执行记录已经变化。发送“请求队列”刷新列表。"
	}
	if pageNumber <= 0 {
		pageNumber = 1
	}
	finishedAt := "尚未结束"
	end := time.Now()
	if task.FinishedAt > 0 {
		end = time.Unix(task.FinishedAt, 0)
		finishedAt = formatSessionTime(task.FinishedAt)
	}
	startAt := task.CreatedAt
	if task.StartedAt > 0 {
		startAt = task.StartedAt
	}
	duration := end.Sub(time.Unix(startAt, 0))
	if duration < 0 {
		duration = 0
	}
	options := make([]controlOption, 0, 6)
	switch task.State {
	case taskqueue.StateQueued:
		options = append(options,
			controlOption{Label: "移到最前", Action: actionTaskMoveFront, Value: task.ID, Page: pageNumber},
			controlOption{Label: "删除请求", Action: actionTaskDelete, Value: task.ID, Page: pageNumber},
		)
	case taskqueue.StateRunning:
		options = append(options, controlOption{Label: "取消当前执行", Action: actionConfirmCancelTask})
	case taskqueue.StateFailed, taskqueue.StateInterrupted:
		if h.hasFrozenDelivery(task) {
			options = append(options, controlOption{Label: "取回冻结文字", Action: actionTaskFrozenText, Value: task.ID, Page: pageNumber})
		} else {
			options = append(options, controlOption{Label: "重试请求", Action: actionTaskRetry, Value: task.ID, Page: pageNumber})
		}
		options = append(options, controlOption{Label: "删除记录", Action: actionTaskDelete, Value: task.ID, Page: pageNumber})
	case taskqueue.StateCancelled:
		options = append(options, controlOption{Label: "删除记录", Action: actionTaskDelete, Value: task.ID, Page: pageNumber})
	case taskqueue.StateSucceeded:
		if task.ThreadID != "" {
			options = append(options, controlOption{
				Label: "继续这个线程", Action: actionTaskContinueSession, Value: task.ID, Page: pageNumber,
			})
		}
		if _, err := h.tasks.LoadReusablePrompt(userID, task.ID); err == nil {
			options = append(options,
				controlOption{Label: "再次执行", Action: actionTaskRerun, Value: task.ID, Page: pageNumber},
				controlOption{Label: "在新线程执行", Action: actionTaskRerunNewSession, Value: task.ID, Page: pageNumber},
			)
		}
	}
	options = append(options, controlOption{Label: "返回请求队列", Action: actionActivityPage, Page: pageNumber})
	prompt := strings.Join([]string{
		"codex-link-clawbot 执行记录",
		"",
		"编号：" + shortTaskID(task.ID),
		"摘要：" + task.Summary,
		"状态：" + taskStateText(task.State),
		"阶段：" + task.Stage,
		"创建：" + formatSessionTime(task.CreatedAt),
		"结束：" + finishedAt,
		"用时：" + formatUptime(duration),
		"Codex 工作空间：" + task.ProjectID,
		"交付状态：" + taskDeliveryStateText(task),
	}, "\n")
	if task.ThreadID != "" {
		prompt += "\nCodex 线程：" + session.ShortCode(task.ThreadID)
	}
	if task.TotalTokens > 0 {
		prompt += fmt.Sprintf("\nCodex 用量：输入 %d · 输出 %d · 合计 %d 个令牌", task.InputTokens, task.OutputTokens, task.TotalTokens)
	}
	prompt += "\n\n" + renderControlOptions(options)
	if !h.storeChoiceWithBack(userID, viewTaskDetail, options, controlOption{Action: actionActivityPage, Page: pageNumber}) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字继续，0 返回原列表。"
}

func taskDeliveryStateText(task taskqueue.Task) string {
	switch task.State {
	case taskqueue.StateQueued, taskqueue.StateRunning:
		return "尚未开始"
	case taskqueue.StateDelivering:
		return "正在发送"
	case taskqueue.StateSucceeded:
		return "已成功发送"
	case taskqueue.StateInterrupted:
		if task.Reason == taskqueue.ReasonDeliveryAmbiguous || task.Reason == taskqueue.ReasonRestartDelivery {
			return "发送结果不确定 · 可人工取回冻结文字"
		}
		return "未发送"
	case taskqueue.StateFailed:
		if task.Reason == taskqueue.ReasonDeliveryFailed {
			return "微信明确发送失败"
		}
		return "未发送"
	case taskqueue.StateCancelled:
		return "未发送"
	default:
		return "未知"
	}
}

func taskStateText(state taskqueue.State) string {
	switch state {
	case taskqueue.StateQueued:
		return "排队中"
	case taskqueue.StateRunning:
		return "运行中"
	case taskqueue.StateDelivering:
		return "发送中"
	case taskqueue.StateSucceeded:
		return "已完成"
	case taskqueue.StateFailed:
		return "失败"
	case taskqueue.StateCancelled:
		return "已取消"
	case taskqueue.StateInterrupted:
		return "重启中断"
	default:
		return "未知"
	}
}

func (h *Handler) setQueuePaused(userID string, paused bool) string {
	if h.tasks == nil {
		return "codex-link-clawbot 请求队列当前不可用。"
	}
	if !paused && h.remoteLock != nil && h.remoteLock.IsLocked(userID) {
		return "codex-link-clawbot 仍处于远程锁定，不能继续请求队列。"
	}
	if err := h.tasks.SetPaused(userID, paused); err != nil {
		return fmt.Sprintf("更新请求队列失败：%v", err)
	}
	if paused {
		return "codex-link-clawbot 请求队列已暂停。已落盘请求会保留，其他绑定者不受影响。"
	}
	if h.coordinator != nil {
		h.coordinator.Wake()
	}
	return "codex-link-clawbot 请求队列已继续，将按原顺序执行。"
}

func (h *Handler) moveTaskToFront(userID, taskID string, pageNumber int) string {
	if h.tasks == nil {
		return "codex-link-clawbot 请求队列当前不可用。"
	}
	if _, err := h.tasks.MoveToFront(userID, taskID); err != nil {
		return fmt.Sprintf("调整请求顺序失败：%v", err)
	}
	if h.coordinator != nil {
		h.coordinator.Wake()
	}
	return "请求已移到等待区最前，不会抢占当前正在执行或发送的请求。\n\n" + h.openActivityDetail(userID, taskID, pageNumber)
}

func (h *Handler) deleteTask(userID, taskID string, pageNumber int) string {
	if h.tasks == nil {
		return "codex-link-clawbot 请求队列当前不可用。"
	}
	if err := h.tasks.Delete(userID, taskID); err != nil {
		return fmt.Sprintf("删除请求失败：%v", err)
	}
	return "请求记录已删除。\n\n" + h.openActivities(userID, pageNumber)
}

func (h *Handler) requestTaskRetry(userID, taskID string) ActionResult {
	if h.tasks == nil {
		return newActionResult(string(actionTaskRetry), DomainQueue, "codex-link-clawbot 请求队列当前不可用。")
	}
	if task, ok := h.tasks.Find(userID, taskID); !ok || task.State != taskqueue.StateFailed && task.State != taskqueue.StateInterrupted || h.hasFrozenDelivery(task) {
		return newActionResult(string(actionTaskRetry), DomainQueue, "这条执行记录已经不能重试。发送“请求队列”刷新。")
	}
	return effectActionResult(string(actionTaskRetry), DomainQueue, "正在创建重试请求。", EffectRetryTask, taskID)
}

func (h *Handler) requestFrozenTaskText(userID, taskID string) ActionResult {
	if h.tasks == nil {
		return newActionResult(string(actionTaskFrozenText), DomainQueue, "codex-link-clawbot 请求队列当前不可用。")
	}
	task, ok := h.tasks.Find(userID, taskID)
	if !ok || !h.hasFrozenDelivery(task) {
		return newActionResult(string(actionTaskFrozenText), DomainQueue, "这条执行记录没有可人工恢复的冻结结果。")
	}
	if _, err := h.tasks.LoadResult(userID, taskID); err != nil {
		return newActionResult(string(actionTaskFrozenText), DomainQueue, "冻结结果已过期或损坏，无法取回。")
	}
	return effectActionResult(string(actionTaskFrozenText), DomainQueue, "正在取回冻结文字。", EffectFrozenText, taskID)
}

func (h *Handler) hasFrozenDelivery(task taskqueue.Task) bool {
	switch task.Reason {
	case taskqueue.ReasonRestartDelivery, taskqueue.ReasonDeliveryFailed, taskqueue.ReasonDeliveryAmbiguous:
		if h.tasks == nil {
			return false
		}
		_, err := h.tasks.LoadResult(task.OwnerID, task.ID)
		return err == nil
	default:
		return false
	}
}

func (h *Handler) confirmClearQueue(userID string) string {
	if h.tasks == nil || h.tasks.Status(userID).Queued == 0 {
		return "当前没有等待中的请求。"
	}
	options := []controlOption{{Label: "确认清空等待请求", Action: actionQueueClear}}
	prompt := "准备清空 codex-link-clawbot 请求队列\n\n只会删除当前绑定者的等待请求；执行中请求和其他绑定者不受影响。\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewTaskClearConfirm, options, actionActivityPage) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复 1 确认，0 返回请求队列。"
}

func (h *Handler) clearQueue(userID string) string {
	if h.tasks == nil {
		return "codex-link-clawbot 请求队列当前不可用。"
	}
	count, err := h.tasks.ClearQueued(userID)
	if err != nil {
		return fmt.Sprintf("清空请求队列失败：%v", err)
	}
	return fmt.Sprintf("已清空 %d 项等待请求。\n\n%s", count, h.openActivities(userID, 1))
}
