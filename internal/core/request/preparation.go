package request

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// AttachInput 文件同步后，在同一事务提交输入元数据与请求状态。
func (s *Store) AttachInput(owner, id, text string, images, files []InputAttachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.findTaskLocked(owner, id)
	if !ok || task.State != StateRunning || !task.InputPending {
		return fmt.Errorf("附件准备已结束")
	}
	input := StartInput{SourceMessageKey: task.SourceMessageKey, OwnerID: owner, ProjectID: task.ProjectID, Summary: task.Summary, Text: text, Images: images, Files: files, ResponseMode: task.ResponseMode, VisualStyle: task.VisualStyle}
	if err := validateStartInput(input); err != nil {
		return err
	}
	original, err := s.readInput(id)
	if err != nil {
		return err
	}
	prepared := Request{Version: requestVersion, SourceMessageKey: task.SourceMessageKey, Text: text, ContextToken: original.ContextToken, Images: []Attachment{}, Files: []Attachment{}}
	// 未提交文件使用唯一名字；崩溃残留不会妨碍下一次准备，最终随输入到期回收。
	prefix := "ready-" + uuid.NewString()
	committed := false
	defer func() {
		if !committed {
			for _, group := range [][]Attachment{prepared.Images, prepared.Files} {
				for _, attachment := range group {
					_ = os.Remove(filepath.Join(s.taskPath(id), attachment.Path))
				}
			}
		}
	}()
	for i, item := range images {
		a, err := writeInputAttachment(filepath.Join(s.taskPath(id), "input"), prefix+"-image", i+1, item)
		if err != nil {
			return err
		}
		prepared.Images = append(prepared.Images, a)
	}
	for i, item := range files {
		a, err := writeInputAttachment(filepath.Join(s.taskPath(id), "input"), prefix+"-file", i+1, item)
		if err != nil {
			return err
		}
		prepared.Files = append(prepared.Files, a)
	}
	if err := syncDirectory(filepath.Join(s.taskPath(id), "input")); err != nil {
		return err
	}
	input.ContextToken = prepared.ContextToken
	err = s.updateTaskLocked(owner, id, func(t *Task) error {
		t.InputPending = false
		t.ImageCount = len(images)
		t.FileCount = len(files)
		t.PayloadBytes = inputPayloadBytes(input)
		t.Stage = "准备执行"
		return nil
	}, func(tx *sql.Tx) error { return saveInput(tx, id, prepared) })
	committed = err == nil
	return err
}
