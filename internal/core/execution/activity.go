package execution

import (
	"context"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
)

// Activity 是微信与网页共用的会话状态；查询失败不能显示为空闲。
type Activity struct {
	TargetID     string `json:"target_id"`
	ThreadID     string `json:"thread_id"`
	TaskID       string `json:"task_id,omitempty"`
	TurnID       string `json:"turn_id,omitempty"`
	Stage        string `json:"stage"`
	Busy         bool   `json:"busy"`
	CanInterrupt bool   `json:"can_interrupt"`
}

func ReadActivity(ctx context.Context, store *request.Store, client codex.ThreadClient, owner, targetID, threadID string) (Activity, error) {
	activity := CatalogActivity(store, owner, targetID, threadID, codex.ThreadStatus{})
	if activity.TaskID != "" || threadID == "" {
		return activity, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	info, err := client.ReadThread(ctx, threadID)
	if err != nil {
		activity.Busy, activity.Stage = true, "状态暂不可用，请刷新"
		return activity, err
	}
	activity = CatalogActivity(store, owner, targetID, threadID, info.Status)
	if activity.Busy {
		if control, ok := client.(codex.SessionControl); ok {
			activity.TurnID, err = control.ActiveTurn(ctx, threadID)
			activity.CanInterrupt = err == nil && activity.TurnID != ""
		}
	}
	return activity, err
}

// 列表复用同一份原生目录快照，详情页再读取可打断的精确轮次。
func CatalogActivity(store *request.Store, owner, targetID, threadID string, status codex.ThreadStatus) Activity {
	value := Activity{TargetID: targetID, ThreadID: threadID, Stage: "空闲，可发送工作内容"}
	if task, ok := store.Active(owner, targetID, threadID); ok {
		value.Busy, value.Stage, value.TaskID = true, task.Stage, task.ID
		if task.ThreadID != "" {
			value.ThreadID = task.ThreadID
		}
		value.CanInterrupt = task.State == request.StateRunning && task.ExecutionCompletedAt == 0
	} else if status.Type == "active" {
		value.Busy, value.Stage = true, "会话正在执行"
	}
	return value
}
