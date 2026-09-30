package request

import (
	"fmt"
	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
	"path/filepath"
)

// AttachInput 先发布完整附件清单，再更新索引缓存；宕机时旧索引仍按预留容量计费。
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
	var original Request
	if _, err := statefile.ReadJSON(filepath.Join(s.taskPath(id), "request.json"), &original, statefile.Options{MaxBytes: 2 << 20, Validate: func() error { return validateRequest(original) }}); err != nil {
		return err
	}
	prepared := Request{Version: requestVersion, SourceMessageKey: task.SourceMessageKey, Text: text, ContextToken: original.ContextToken, Images: []Attachment{}, Files: []Attachment{}}
	for i, item := range images {
		a, err := writeInputAttachment(filepath.Join(s.taskPath(id), "inbox"), "ready-image", i+1, item)
		if err != nil {
			return err
		}
		prepared.Images = append(prepared.Images, a)
	}
	for i, item := range files {
		a, err := writeInputAttachment(filepath.Join(s.taskPath(id), "inbox"), "ready-file", i+1, item)
		if err != nil {
			return err
		}
		prepared.Files = append(prepared.Files, a)
	}
	if err := syncDirectory(filepath.Join(s.taskPath(id), "inbox")); err != nil {
		return err
	}
	if err := writeJSONSync(filepath.Join(s.taskPath(id), "prepared.json"), prepared); err != nil {
		return err
	}
	input.ContextToken = prepared.ContextToken
	return s.updateTaskLocked(owner, id, func(t *Task) error {
		t.InputPending = false
		t.ImageCount = len(images)
		t.FileCount = len(files)
		t.PayloadBytes = inputPayloadBytes(input)
		t.Stage = "准备执行"
		return nil
	})
}
