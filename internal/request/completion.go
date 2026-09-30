package request

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
)

type Completion struct {
	Version int    `json:"version"`
	Reply   string `json:"reply"`
	At      int64  `json:"at"`
}

// CompleteExecution 独立保存执行事实；归档错误不能把成功工作变为可重跑失败。
func (s *Store) CompleteExecution(owner, id, reply string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.findTaskLocked(owner, id)
	if !ok || task.State != StateRunning {
		return fmt.Errorf("执行不可完成")
	}
	at := max(s.now().Unix(), task.StartedAt)
	checkpointErr := statefile.WriteJSON(filepath.Join(s.taskPath(id), "completion.json"), Completion{Version: 1, Reply: reply, At: at}, statefile.Options{MaxBytes: maxResultReplyBytes + (1 << 20)})
	completed := task
	completed.ExecutionCompletedAt, completed.ResultExpiresAt = at, at+int64(ResultRetention.Seconds())
	completed.ArchiveFailed, completed.ResultBytes, completed.Stage = true, MaxTaskBytes+int64(len(reply)), "执行完成，结果待保存"
	stateErr := s.updateTaskLocked(owner, id, func(t *Task) error {
		t.ExecutionCompletedAt = at
		t.ResultExpiresAt = at + int64(ResultRetention.Seconds())
		t.ArchiveFailed = true
		t.ResultBytes = MaxTaskBytes + int64(len(reply))
		t.Stage = "执行完成，正在保存结果"
		return nil
	})
	if stateErr != nil {
		// 磁盘暂不可写时仍保留进程内的完成事实，后续收尾绝不能变成可重跑失败。
		records := s.state.Owners[owner]
		for i := range records.Tasks {
			if records.Tasks[i].ID == id {
				records.Tasks[i] = completed
			}
		}
		s.state.Owners[owner] = records
	}
	return errors.Join(checkpointErr, stateErr)
}

func (s *Store) LoadCompletion(owner, id string) (Completion, error) {
	task, ok := s.Find(owner, id)
	if !ok || task.ResultExpiresAt <= s.now().Unix() {
		return Completion{}, fmt.Errorf("结果已过期")
	}
	var c Completion
	found, err := statefile.ReadJSON(filepath.Join(s.taskPath(id), "completion.json"), &c, statefile.Options{MaxBytes: maxResultReplyBytes + (1 << 20), Validate: func() error {
		if c.Version != 1 || c.At < task.StartedAt {
			return fmt.Errorf("完成记录无效")
		}
		return nil
	}})
	if err != nil {
		return c, err
	}
	if !found {
		return c, fmt.Errorf("完成记录不可用")
	}
	return c, nil
}

// ClearedReceipt 保留最小执行事实与去重来源，不占用用户历史记录名额。
type ClearedReceipt struct {
	OwnerID              string `json:"owner_id"`
	TaskID               string `json:"task_id"`
	State                State  `json:"state"`
	ExecutionCompletedAt int64  `json:"execution_completed_at,omitempty"`
	At                   int64  `json:"at"`
}

var ErrCleared = errors.New("该消息已经处理且已清理，不会重复执行")

func (s *Store) WasCleared(source string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.state.Cleared[source]
	return ok
}

// Release 删除用户明确选中的已结束请求，先保存来源回执再删除文件。
func (s *Store) Release(owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.findTaskLocked(owner, id)
	if !ok || !task.State.Terminal() {
		return fmt.Errorf("只能清理已结束的请求")
	}
	previous := s.state.Owners[owner]
	next := OwnerRecords{Tasks: make([]Task, 0, len(previous.Tasks)-1)}
	for _, t := range previous.Tasks {
		if t.ID != id {
			next.Tasks = append(next.Tasks, t)
		}
	}
	if s.state.Cleared == nil {
		s.state.Cleared = make(map[string]ClearedReceipt)
	}
	s.state.Cleared[task.SourceMessageKey] = ClearedReceipt{OwnerID: owner, TaskID: id, State: task.State, ExecutionCompletedAt: task.ExecutionCompletedAt, At: task.FinishedAt}
	s.state.Owners[owner] = next
	if err := s.saveLocked(); err != nil {
		s.state.Owners[owner] = previous
		delete(s.state.Cleared, task.SourceMessageKey)
		return err
	}
	s.resultSummaries.Delete(id)
	return os.RemoveAll(s.taskPath(id))
}

type Capacity struct {
	InputBytes  int64 `json:"input_bytes"`
	ResultBytes int64 `json:"result_bytes"`
	Records     int   `json:"records"`
	InputLimit  int64 `json:"input_limit"`
	ResultLimit int64 `json:"result_limit"`
	RecordLimit int   `json:"record_limit"`
	NextExpiry  int64 `json:"next_expiry"`
}

func (s *Store) Capacity(owner string) Capacity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := Capacity{InputBytes: s.payloadBytesLocked(), ResultBytes: s.resultBytesLocked(), Records: len(s.state.Owners[owner].Tasks), InputLimit: MaxInputStoreBytes, ResultLimit: MaxResultStoreBytes, RecordLimit: MaxRecordsPerOwner}
	for _, r := range s.state.Owners {
		for _, t := range r.Tasks {
			for _, at := range []int64{t.PayloadExpiresAt, t.ResultExpiresAt, t.FinishedAt + int64(historyRetention.Seconds())} {
				if at > s.now().Unix() && (c.NextExpiry == 0 || at < c.NextExpiry) {
					c.NextExpiry = at
				}
			}
		}
	}
	return c
}
