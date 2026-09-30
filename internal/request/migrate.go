package request

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/target"
)

// MigrateLegacy 只转换离线暂存副本。运行时不读取旧 schema，也不双写旧交付库。
func MigrateLegacy(stageRoot, originalRoot string, targets *target.Store) error {
	root := filepath.Join(stageRoot, "tasks")
	if err := statefile.EnsurePrivateDirectory(root); err != nil {
		return err
	}
	// 旧字段仅在离线迁移器中出现，运行时不保留队列兼容分支。
	var legacy struct {
		Version   int   `json:"version"`
		NextOrder int64 `json:"next_order"`
		Owners    map[string]struct {
			Paused bool `json:"paused"`
			Tasks  []struct {
				Task
				AwaitingAcknowledgement bool `json:"awaiting_acknowledgement,omitempty"`
			} `json:"tasks"`
		} `json:"owners"`
		Rejected map[string]Rejection `json:"rejected"`
	}
	found, err := statefile.ReadJSON(filepath.Join(root, "index.json"), &legacy, statefile.Options{MaxBytes: 16 << 20})
	if err != nil {
		return err
	}
	if found && legacy.Version != 1 && legacy.Version != 2 && legacy.Version != 3 {
		return fmt.Errorf("仅支持请求索引 v1/v2/v3 的离线迁移")
	}
	index := defaultIndex()
	if legacy.Rejected != nil {
		index.Rejected = legacy.Rejected
	}
	if found {
		index.NextOrder = legacy.NextOrder
	}
	for ownerID, records := range legacy.Owners {
		owner := OwnerRecords{}
		for _, item := range records.Tasks {
			owner.Tasks = append(owner.Tasks, item.Task)
		}
		index.Owners[ownerID] = owner
	}
	now := time.Now().Unix()
	hashes := map[string]bool{}
	var total int64
	for ownerID, owner := range index.Owners {
		for j := range owner.Tasks {
			task := &owner.Tasks[j]
			if task.TargetID == "" {
				intent, err := targets.Remember(ownerID, task.ProjectID, task.ThreadID, false)
				if err != nil {
					return err
				}
				task.TargetID = intent.ID
			}
			if task.State == State("queued") {
				task.State, task.Stage, task.Reason = StateCancelled, "旧等待指令已取消，未执行", "removed_pending"
				task.FinishedAt = max(now, task.CreatedAt)
				task.PayloadExpiresAt = task.FinishedAt + int64(payloadRetention.Seconds())
			}
			taskRoot := filepath.Join(root, task.ID)
			// 先校验身份，禁止旧索引把暂存写入引导到目录之外。
			if !taskIDPattern.MatchString(task.ID) {
				return fmt.Errorf("invalid legacy task id")
			}
			var payload Request
			payloadFound, err := statefile.ReadJSON(filepath.Join(taskRoot, "request.json"), &payload, statefile.Options{MaxBytes: 2 << 20})
			if err != nil {
				return err
			}
			if payloadFound {
				payload.Version = requestVersion
				if err := writeJSONAtomic(filepath.Join(taskRoot, "request.json"), payload); err != nil {
					return err
				}
				task.PayloadBytes = int64(len(payload.Text) + len(payload.ContextToken))
				for _, a := range payload.Images {
					task.PayloadBytes += a.Size
				}
				for _, a := range payload.Files {
					task.PayloadBytes += a.Size
				}
			}
			var result Result
			found, err := statefile.ReadJSON(filepath.Join(taskRoot, "result.json"), &result, statefile.Options{MaxBytes: maxResultReplyBytes + 1<<20})
			if err != nil {
				return err
			}
			if found {
				if result.Version != 1 && result.Version != resultVersion {
					return fmt.Errorf("unsupported legacy result")
				}
				result.Version = resultVersion
				if len(result.Attempts) == 0 && result.Receipt.Outcome != DeliveryPending {
					result.Attempts = []DeliveryReceipt{result.Receipt}
				}
				if err := validateResult(result, *task); err != nil {
					return err
				}
				task.ExecutionCompletedAt = result.FrozenAt
				task.ResultExpiresAt = result.FrozenAt + int64(ResultRetention.Seconds())
				task.ResultBytes = resultSize(result)
				if task.State.Terminal() {
					task.State, task.Stage, task.Reason = StateSucceeded, "执行完成", ""
				}
				if err := writeJSONAtomic(filepath.Join(taskRoot, "result.json"), result); err != nil {
					return err
				}
				for _, a := range result.Artifacts {
					hashes[ownerID+":"+task.ID+":"+a.SHA256] = true
				}
				if task.ResultExpiresAt > now {
					total += task.ResultBytes
				}
			} else if task.State == StateSucceeded {
				task.ExecutionCompletedAt = task.FinishedAt
			}
			if task.State.Terminal() {
				task.PayloadExpiresAt = task.FinishedAt + int64(payloadRetention.Seconds())
				if _, err := os.Lstat(filepath.Join(taskRoot, "request.json")); os.IsNotExist(err) {
					task.PayloadExpiresAt = task.FinishedAt
				} else if err != nil {
					return err
				}
			}
		}
		index.Owners[ownerID] = owner
	}
	// 旧成功任务的 outbox 已被清理，独立交付库是其文件的唯一剩余副本。
	var library struct {
		Version int                               `json:"version"`
		Owners  map[string][]legacyDeliveryRecord `json:"owners"`
	}
	found = false
	if legacy.Version < 2 {
		found, err = statefile.ReadJSON(filepath.Join(stageRoot, "library.json"), &library, statefile.Options{})
	}
	if err != nil {
		return err
	}
	if found && library.Version != 3 {
		return fmt.Errorf("交付库必须先迁移到 v3")
	}
	for ownerID, records := range library.Owners {
		for _, record := range records {
			if hashes[ownerID+":"+record.TaskID+":"+record.SHA256] {
				continue
			}
			relative, err := filepath.Rel(filepath.Join(originalRoot, "deliveries"), record.FilePath)
			if err != nil || !filepath.IsLocal(relative) || !validAttachmentName(record.Title) || record.Size <= 0 || record.Size > MaxFileBytes {
				return fmt.Errorf("旧交付文件路径或大小无效")
			}
			source := filepath.Join(stageRoot, "deliveries", relative)
			hash, err := hashRegularFile(source, record.Size)
			if err != nil || hash != record.SHA256 {
				return fmt.Errorf("旧交付文件缺失或校验失败: %s", record.ID)
			}
			id := "task-" + strings.ReplaceAll(uuid.NewString(), "-", "")
			intent, err := targets.Remember(ownerID, record.ProjectID, record.ThreadID, false)
			if err != nil {
				return err
			}
			title := []rune("历史文件：" + record.Title)
			if len(title) > 120 {
				title = title[:120]
			}
			task := Task{ID: id, SourceMessageKey: "legacy-delivery:" + id, OwnerID: ownerID, ProjectID: record.ProjectID, ThreadID: record.ThreadID, TargetID: intent.ID, Summary: string(title), State: StateSucceeded, Stage: "历史文件已导入", ResponseMode: presentation.ResponseAdaptive, VisualStyle: presentation.DefaultStyle, Order: index.NextOrder, CreatedAt: now, StartedAt: now, FinishedAt: now, PayloadExpiresAt: now, ExecutionCompletedAt: now, ResultExpiresAt: now + int64(ResultRetention.Seconds())}
			result := Result{Version: resultVersion, Reply: fmt.Sprintf("迁移保留的历史交付文件。原请求：%s；原交付时间：%s。", record.TaskID, time.Unix(record.CreatedAt, 0).Format(time.RFC3339)), ResponseMode: task.ResponseMode, VisualStyle: task.VisualStyle, FrozenAt: now, Receipt: DeliveryReceipt{Outcome: DeliverySucceeded, AttemptedAt: now, MediaSent: 1}}
			result.Attempts = []DeliveryReceipt{result.Receipt}
			data, err := os.ReadFile(source)
			if err != nil {
				return err
			}
			path := filepath.Join("outbox", record.Title)
			if err := statefile.Write(filepath.Join(root, id, path), data, statefile.Options{MaxBytes: MaxFileBytes}); err != nil {
				return err
			}
			result.Artifacts = []ResultArtifact{{Name: record.Title, Path: path, Size: record.Size, SHA256: record.SHA256}}
			task.ResultBytes = resultSize(result)
			total += task.ResultBytes
			if err := validateResult(result, task); err != nil {
				return err
			}
			if err := writeJSONAtomic(filepath.Join(root, id, "result.json"), result); err != nil {
				return err
			}
			owner := index.Owners[ownerID]
			owner.Tasks = append(owner.Tasks, task)
			index.Owners[ownerID] = owner
			index.NextOrder++
		}
	}
	if total > MaxResultStoreBytes {
		return fmt.Errorf("旧结果超过 1 GiB，请先离线整理；原始状态尚未修改")
	}
	if err := validateIndex(index); err != nil {
		return err
	}
	if err := statefile.WriteJSON(filepath.Join(root, "index.json"), index, statefile.Options{MaxBytes: 16 << 20}); err != nil {
		return err
	}
	migrated, err := NewStore(root)
	if err != nil {
		return err
	}
	// 完整验证保留中的输入和结果，只有验证通过的副本才能发布。
	for ownerID := range index.Owners {
		for _, task := range migrated.List(ownerID) {
			if taskHasPayload(task, now) {
				if _, err := migrated.LoadRequest(ownerID, task.ID); err != nil {
					return err
				}
			}
			if task.ResultExpiresAt > now {
				if _, err := migrated.LoadResult(ownerID, task.ID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// 旧交付结构仅供离线迁移解码，运行时没有第二套文件库。
type legacyDeliveryRecord struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	ThreadID  string `json:"thread_id"`
	TaskID    string `json:"task_id"`
	Title     string `json:"title"`
	FilePath  string `json:"file_path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	CreatedAt int64  `json:"created_at"`
}
