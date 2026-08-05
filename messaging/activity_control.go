package messaging

import (
	"fmt"
	"strings"
	"time"

	"github.com/huixiangyang/weclaw/session"
	"github.com/huixiangyang/weclaw/taskqueue"
)

func (h *Handler) openActivities(userID string, pageNumber int) string {
	if h.tasks == nil {
		return "任务中心\n\n任务队列当前不可用。"
	}
	tasks := h.tasks.List(userID)
	status := h.tasks.Status(userID)
	if len(tasks) == 0 {
		return "任务中心\n\n还没有任务。直接发送文字、图片或文件即可可靠入队。"
	}
	if pageNumber <= 0 {
		pageNumber = 1
	}
	totalPages := (len(tasks) + controlSessionPageSize - 1) / controlSessionPageSize
	if pageNumber > totalPages {
		return fmt.Sprintf("任务中心页面不存在：%d / %d。发送“任务中心”刷新。", pageNumber, totalPages)
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
		options = append(options, controlOption{Label: "清空等待任务", Action: actionConfirmQueueClear})
	}
	paused := "否"
	if status.Paused {
		paused = "是"
	}
	prompt := strings.Join([]string{
		"任务中心",
		"",
		fmt.Sprintf("页码：%d / %d", pageNumber, totalPages),
		fmt.Sprintf("等待：%d", status.Queued),
		fmt.Sprintf("执行：%d", status.Running+status.Delivering),
		"已暂停：" + paused,
		"",
		renderControlOptions(options),
	}, "\n")
	h.storeChoice(userID, prompt, options, actionMain)
	return prompt + "\n\n回复数字查看详情，或说“下一页”“上一页”；0 返回。"
}

func (h *Handler) openActivityDetail(userID, id string, pageNumber int) string {
	if h.tasks == nil {
		return "任务详情\n\n任务队列当前不可用。"
	}
	task, ok := h.tasks.Find(userID, id)
	if !ok {
		return "任务状态已经变化。发送“任务中心”刷新列表。"
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
	options := make([]controlOption, 0, 3)
	switch task.State {
	case taskqueue.StateQueued:
		options = append(options,
			controlOption{Label: "移到最前", Action: actionTaskMoveFront, Value: task.ID, Page: pageNumber},
			controlOption{Label: "删除任务", Action: actionTaskDelete, Value: task.ID, Page: pageNumber},
		)
	case taskqueue.StateRunning:
		options = append(options, controlOption{Label: "取消当前任务", Action: actionConfirmCancelTask})
	case taskqueue.StateFailed, taskqueue.StateInterrupted:
		if h.hasFrozenDelivery(task) {
			options = append(options, controlOption{Label: "取回冻结文字", Action: actionTaskFrozenText, Value: task.ID, Page: pageNumber})
		} else {
			options = append(options, controlOption{Label: "重试任务", Action: actionTaskRetry, Value: task.ID, Page: pageNumber})
		}
		options = append(options, controlOption{Label: "删除任务", Action: actionTaskDelete, Value: task.ID, Page: pageNumber})
	case taskqueue.StateCancelled:
		options = append(options, controlOption{Label: "删除记录", Action: actionTaskDelete, Value: task.ID, Page: pageNumber})
	}
	options = append(options, controlOption{Label: "返回任务中心", Action: actionActivityPage, Page: pageNumber})
	prompt := strings.Join([]string{
		"任务详情",
		"",
		"编号：" + shortTaskID(task.ID),
		"摘要：" + task.Summary,
		"状态：" + taskStateText(task.State),
		"阶段：" + task.Stage,
		"创建：" + formatSessionTime(task.CreatedAt),
		"结束：" + finishedAt,
		"用时：" + formatUptime(duration),
		"项目：" + task.ProjectID,
	}, "\n")
	if task.ThreadID != "" {
		prompt += "\n会话：" + session.ShortCode(task.ThreadID)
	}
	if task.TotalTokens > 0 {
		prompt += fmt.Sprintf("\n用量：输入 %d · 输出 %d · 合计 %d tokens", task.InputTokens, task.OutputTokens, task.TotalTokens)
	}
	prompt += "\n\n" + renderControlOptions(options)
	h.storeChoiceWithBack(userID, prompt, options, controlOption{Action: actionActivityPage, Page: pageNumber})
	return prompt + "\n\n回复数字继续，0 返回原列表。"
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
		return "任务队列当前不可用。"
	}
	if !paused && h.remoteLock != nil && h.remoteLock.IsLocked(userID) {
		return "WeClaw 仍处于远程锁定，不能继续任务队列。"
	}
	if err := h.tasks.SetPaused(userID, paused); err != nil {
		return fmt.Sprintf("更新任务队列失败：%v", err)
	}
	if paused {
		return "任务队列已暂停。已落盘任务会保留，其他绑定者的任务不受影响。"
	}
	if h.coordinator != nil {
		h.coordinator.Wake()
	}
	return "任务队列已继续，将按原顺序执行。"
}

func (h *Handler) moveTaskToFront(userID, taskID string, pageNumber int) string {
	if h.tasks == nil {
		return "任务队列当前不可用。"
	}
	if _, err := h.tasks.MoveToFront(userID, taskID); err != nil {
		return fmt.Sprintf("调整任务顺序失败：%v", err)
	}
	if h.coordinator != nil {
		h.coordinator.Wake()
	}
	return "任务已移到等待区最前，不会抢占当前正在执行或发送的任务。\n\n" + h.openActivityDetail(userID, taskID, pageNumber)
}

func (h *Handler) deleteTask(userID, taskID string, pageNumber int) string {
	if h.tasks == nil {
		return "任务队列当前不可用。"
	}
	if err := h.tasks.Delete(userID, taskID); err != nil {
		return fmt.Sprintf("删除任务失败：%v", err)
	}
	return "任务已删除。\n\n" + h.openActivities(userID, pageNumber)
}

func (h *Handler) requestTaskRetry(userID, taskID string) string {
	if h.tasks == nil {
		return "任务队列当前不可用。"
	}
	if task, ok := h.tasks.Find(userID, taskID); !ok || task.State != taskqueue.StateFailed && task.State != taskqueue.StateInterrupted || h.hasFrozenDelivery(task) {
		return "这个任务已经不能重试。发送“任务中心”刷新。"
	}
	h.controlRetries.Store(userID, taskID)
	return "正在创建重试任务。"
}

func (h *Handler) requestFrozenTaskText(userID, taskID string) string {
	if h.tasks == nil {
		return "任务队列当前不可用。"
	}
	task, ok := h.tasks.Find(userID, taskID)
	if !ok || !h.hasFrozenDelivery(task) {
		return "这个任务没有可人工恢复的冻结结果。"
	}
	if _, err := h.tasks.LoadResult(userID, taskID); err != nil {
		return "冻结结果已过期或损坏，无法取回。"
	}
	h.controlFrozenTexts.Store(userID, taskID)
	return "正在取回冻结文字。"
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
		return "当前没有等待中的任务。"
	}
	options := []controlOption{{Label: "确认清空等待任务", Action: actionQueueClear}}
	prompt := "准备清空队列\n\n只会删除当前绑定者的等待任务；执行中任务和其他绑定者不受影响。\n\n" + renderControlOptions(options)
	h.storeChoice(userID, prompt, options, actionActivityPage)
	return prompt + "\n\n回复 1 确认，0 返回任务中心。"
}

func (h *Handler) clearQueue(userID string) string {
	if h.tasks == nil {
		return "任务队列当前不可用。"
	}
	count, err := h.tasks.ClearQueued(userID)
	if err != nil {
		return fmt.Sprintf("清空任务队列失败：%v", err)
	}
	return fmt.Sprintf("已清空 %d 项等待任务。\n\n%s", count, h.openActivities(userID, 1))
}
