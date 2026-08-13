package taskqueue

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

const (
	indexVersion               = 1
	requestVersion             = 1
	MaxQueuedPerOwner          = 20
	MaxTerminalPerOwner        = 20
	MaxImageBytes        int64 = 20 << 20
	MaxFileBytes         int64 = 50 << 20
	MaxTaskBytes         int64 = 100 << 20
	MaxQueueBytes        int64 = 500 << 20
	maxRequestTextBytes        = 1 << 20
	maxContextTokenBytes       = 64 << 10
)

type State string

const (
	StateQueued      State = "queued"
	StateRunning     State = "running"
	StateDelivering  State = "delivering"
	StateSucceeded   State = "succeeded"
	StateFailed      State = "failed"
	StateInterrupted State = "interrupted"
	StateCancelled   State = "cancelled"
)

func (state State) Valid() bool {
	switch state {
	case StateQueued, StateRunning, StateDelivering, StateSucceeded, StateFailed, StateInterrupted, StateCancelled:
		return true
	default:
		return false
	}
}

func (state State) Terminal() bool {
	switch state {
	case StateSucceeded, StateFailed, StateInterrupted, StateCancelled:
		return true
	default:
		return false
	}
}

type Task struct {
	ID                      string                  `json:"id"`
	SourceMessageKey        string                  `json:"source_message_key"`
	OwnerID                 string                  `json:"owner_id"`
	ProjectID               string                  `json:"project_id"`
	ThreadID                string                  `json:"thread_id,omitempty"`
	Summary                 string                  `json:"summary"`
	State                   State                   `json:"state"`
	Stage                   string                  `json:"stage"`
	Reason                  string                  `json:"reason,omitempty"`
	AwaitingAcknowledgement bool                    `json:"awaiting_acknowledgement,omitempty"`
	ResponseMode            preference.ResponseMode `json:"response_mode"`
	VisualStyle             visual.Style            `json:"visual_style"`
	Order                   int64                   `json:"order"`
	CreatedAt               int64                   `json:"created_at"`
	StartedAt               int64                   `json:"started_at,omitempty"`
	FinishedAt              int64                   `json:"finished_at,omitempty"`
	PayloadExpiresAt        int64                   `json:"payload_expires_at,omitempty"`
	RetryOf                 string                  `json:"retry_of,omitempty"`
	ImageCount              int                     `json:"image_count,omitempty"`
	FileCount               int                     `json:"file_count,omitempty"`
	PayloadBytes            int64                   `json:"payload_bytes,omitempty"`
	InputTokens             int64                   `json:"input_tokens,omitempty"`
	OutputTokens            int64                   `json:"output_tokens,omitempty"`
	TotalTokens             int64                   `json:"total_tokens,omitempty"`
}

type OwnerQueue struct {
	Paused bool   `json:"paused"`
	Tasks  []Task `json:"tasks"`
}

type indexFile struct {
	Version   int                   `json:"version"`
	NextOrder int64                 `json:"next_order"`
	Owners    map[string]OwnerQueue `json:"owners"`
}

type Attachment struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
}

type Request struct {
	Version          int          `json:"version"`
	SourceMessageKey string       `json:"source_message_key"`
	Text             string       `json:"text,omitempty"`
	ContextToken     string       `json:"context_token,omitempty"`
	Images           []Attachment `json:"images"`
	Files            []Attachment `json:"files"`
}

type LoadedAttachment struct {
	Attachment
	AbsolutePath string
}

type LoadedRequest struct {
	SourceMessageKey string
	Text             string
	ContextToken     string
	Images           []LoadedAttachment
	Files            []LoadedAttachment
}

type InputAttachment struct {
	Name        string
	ContentType string
	Data        []byte
}

type EnqueueInput struct {
	SourceMessageKey       string
	OwnerID                string
	ProjectID              string
	ThreadID               string
	Summary                string
	Text                   string
	ContextToken           string
	ResponseMode           preference.ResponseMode
	VisualStyle            visual.Style
	RetryOf                string
	RequireAcknowledgement bool
	Images                 []InputAttachment
	Files                  []InputAttachment
}

type OwnerStatus struct {
	Paused      bool
	Queued      int
	Running     int
	Delivering  int
	Succeeded   int
	Failed      int
	Interrupted int
	Cancelled   int
}

type QueueStatus struct {
	Queued     int
	Running    int
	Delivering int
}

var (
	taskIDPattern   = regexp.MustCompile(`^task-[a-f0-9]{32}$`)
	safeIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	sha256Pattern   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	safeExtensionRE = regexp.MustCompile(`^\.[a-z0-9]{1,10}$`)
	reasonPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

func defaultIndex() indexFile {
	return indexFile{Version: indexVersion, NextOrder: 1, Owners: make(map[string]OwnerQueue)}
}

func validateEnqueueInput(input EnqueueInput) error {
	if !validSingleLine(input.SourceMessageKey, 512) {
		return fmt.Errorf("task source message key is invalid")
	}
	if strings.TrimSpace(input.OwnerID) == "" || len(input.OwnerID) > 512 || strings.ContainsAny(input.OwnerID, "\r\n") {
		return fmt.Errorf("task owner is invalid")
	}
	if !safeIDPattern.MatchString(input.ProjectID) {
		return fmt.Errorf("task project is invalid")
	}
	if input.ThreadID != "" && !validSingleLine(input.ThreadID, 512) {
		return fmt.Errorf("task thread is invalid")
	}
	if !validSingleLine(input.Summary, 120) {
		return fmt.Errorf("task summary is invalid")
	}
	if !utf8.ValidString(input.Text) || strings.ContainsRune(input.Text, '\x00') ||
		len([]byte(input.Text)) > maxRequestTextBytes || len([]byte(input.ContextToken)) > maxContextTokenBytes {
		return fmt.Errorf("task text or context token exceeds the limit")
	}
	if strings.TrimSpace(input.Text) == "" && len(input.Images) == 0 && len(input.Files) == 0 {
		return fmt.Errorf("task request is empty")
	}
	if !input.ResponseMode.Valid() || !input.VisualStyle.Valid() {
		return fmt.Errorf("task response preferences are invalid")
	}
	if input.RetryOf != "" && !taskIDPattern.MatchString(input.RetryOf) {
		return fmt.Errorf("task retry source is invalid")
	}
	if len(input.Images) > 4 || len(input.Files) > 8 {
		return fmt.Errorf("task attachment count exceeds the limit")
	}
	var total int64
	for _, attachment := range input.Images {
		if err := validateInputAttachment(attachment, MaxImageBytes); err != nil {
			return fmt.Errorf("invalid image attachment: %w", err)
		}
		total += int64(len(attachment.Data))
	}
	for _, attachment := range input.Files {
		if err := validateInputAttachment(attachment, MaxFileBytes); err != nil {
			return fmt.Errorf("invalid file attachment: %w", err)
		}
		total += int64(len(attachment.Data))
	}
	if total > MaxTaskBytes {
		return fmt.Errorf("task attachments exceed the total size limit")
	}
	return nil
}

func validateInputAttachment(attachment InputAttachment, maxBytes int64) error {
	if !validAttachmentName(attachment.Name) {
		return fmt.Errorf("attachment name is invalid")
	}
	if !validSingleLine(attachment.ContentType, 128) {
		return fmt.Errorf("attachment content type is invalid")
	}
	if len(attachment.Data) == 0 || int64(len(attachment.Data)) > maxBytes {
		return fmt.Errorf("attachment size is invalid")
	}
	return nil
}

func validateIndex(state indexFile) error {
	if state.Version != indexVersion || state.NextOrder < 1 || state.Owners == nil {
		return fmt.Errorf("invalid task index schema")
	}
	ids := make(map[string]struct{})
	sources := make(map[string]struct{})
	active := 0
	for ownerID, owner := range state.Owners {
		if strings.TrimSpace(ownerID) == "" || len(ownerID) > 512 || strings.ContainsAny(ownerID, "\r\n") {
			return fmt.Errorf("invalid task owner")
		}
		queued := 0
		terminal := 0
		for _, task := range owner.Tasks {
			if err := validateTask(task, ownerID); err != nil {
				return err
			}
			if _, exists := ids[task.ID]; exists {
				return fmt.Errorf("duplicated task id")
			}
			ids[task.ID] = struct{}{}
			if _, exists := sources[task.SourceMessageKey]; exists {
				return fmt.Errorf("duplicated task source")
			}
			sources[task.SourceMessageKey] = struct{}{}
			if task.State == StateQueued {
				queued++
			}
			if task.State == StateRunning || task.State == StateDelivering {
				active++
			}
			if task.State.Terminal() {
				terminal++
			}
		}
		if queued > MaxQueuedPerOwner || terminal > MaxTerminalPerOwner {
			return fmt.Errorf("task owner limits are invalid")
		}
	}
	if active > 1 {
		return fmt.Errorf("multiple active tasks are not allowed")
	}
	return nil
}

func validateTask(task Task, ownerID string) error {
	if !taskIDPattern.MatchString(task.ID) || task.OwnerID != ownerID || !validSingleLine(task.SourceMessageKey, 512) {
		return fmt.Errorf("invalid task identity")
	}
	if !safeIDPattern.MatchString(task.ProjectID) || task.ThreadID != "" && !validSingleLine(task.ThreadID, 512) {
		return fmt.Errorf("invalid task project or thread")
	}
	if !validSingleLine(task.Summary, 120) || !validSingleLine(task.Stage, 120) || task.Reason != "" && !reasonPattern.MatchString(task.Reason) {
		return fmt.Errorf("invalid task display metadata")
	}
	if !task.State.Valid() || !task.ResponseMode.Valid() || !task.VisualStyle.Valid() {
		return fmt.Errorf("invalid task state or preferences")
	}
	if task.CreatedAt <= 0 || task.StartedAt < 0 || task.FinishedAt < 0 || task.PayloadExpiresAt < 0 {
		return fmt.Errorf("invalid task timestamps")
	}
	if task.RetryOf != "" && !taskIDPattern.MatchString(task.RetryOf) {
		return fmt.Errorf("invalid task retry source")
	}
	if task.ImageCount < 0 || task.ImageCount > 4 || task.FileCount < 0 || task.FileCount > 8 || task.PayloadBytes < 0 || task.PayloadBytes > MaxTaskBytes {
		return fmt.Errorf("invalid task attachment metadata")
	}
	if task.InputTokens < 0 || task.OutputTokens < 0 || task.TotalTokens < 0 {
		return fmt.Errorf("invalid task token usage")
	}
	switch task.State {
	case StateQueued:
		if task.StartedAt != 0 || task.FinishedAt != 0 || task.PayloadExpiresAt != 0 {
			return fmt.Errorf("invalid queued task timestamps")
		}
	case StateRunning, StateDelivering:
		if task.AwaitingAcknowledgement {
			return fmt.Errorf("active task still awaits acknowledgement")
		}
		if task.StartedAt < task.CreatedAt || task.FinishedAt != 0 || task.PayloadExpiresAt != 0 {
			return fmt.Errorf("invalid active task timestamps")
		}
	case StateSucceeded:
		if task.AwaitingAcknowledgement {
			return fmt.Errorf("successful task still awaits acknowledgement")
		}
		if task.StartedAt < task.CreatedAt || task.FinishedAt < task.StartedAt || task.PayloadExpiresAt != 0 {
			return fmt.Errorf("invalid successful task timestamps")
		}
	case StateCancelled:
		if task.AwaitingAcknowledgement {
			return fmt.Errorf("cancelled task still awaits acknowledgement")
		}
		if task.FinishedAt < task.CreatedAt || task.PayloadExpiresAt != 0 {
			return fmt.Errorf("invalid cancelled task timestamps")
		}
	case StateFailed, StateInterrupted:
		if task.AwaitingAcknowledgement {
			return fmt.Errorf("failed task still awaits acknowledgement")
		}
		if task.StartedAt < task.CreatedAt || task.FinishedAt < task.StartedAt || task.PayloadExpiresAt < task.FinishedAt {
			return fmt.Errorf("invalid retained task timestamps")
		}
	}
	return nil
}

func validateRequest(request Request) error {
	if request.Version != requestVersion || !validSingleLine(request.SourceMessageKey, 512) {
		return fmt.Errorf("invalid task request schema")
	}
	if !utf8.ValidString(request.Text) || strings.ContainsRune(request.Text, '\x00') ||
		len([]byte(request.Text)) > maxRequestTextBytes || len([]byte(request.ContextToken)) > maxContextTokenBytes {
		return fmt.Errorf("task request exceeds the limit")
	}
	if strings.TrimSpace(request.Text) == "" && len(request.Images) == 0 && len(request.Files) == 0 {
		return fmt.Errorf("task request is empty")
	}
	if len(request.Images) > 4 || len(request.Files) > 8 {
		return fmt.Errorf("task request attachment count is invalid")
	}
	paths := make(map[string]struct{})
	for _, attachment := range request.Images {
		if err := validateStoredAttachment(attachment, MaxImageBytes); err != nil {
			return err
		}
		if _, exists := paths[attachment.Path]; exists {
			return fmt.Errorf("duplicated task attachment path")
		}
		paths[attachment.Path] = struct{}{}
	}
	for _, attachment := range request.Files {
		if err := validateStoredAttachment(attachment, MaxFileBytes); err != nil {
			return err
		}
		if _, exists := paths[attachment.Path]; exists {
			return fmt.Errorf("duplicated task attachment path")
		}
		paths[attachment.Path] = struct{}{}
	}
	return nil
}

func validateStoredAttachment(attachment Attachment, maxBytes int64) error {
	if !validAttachmentName(attachment.Name) || !validSingleLine(attachment.ContentType, 128) {
		return fmt.Errorf("invalid stored attachment metadata")
	}
	if attachment.Size <= 0 || attachment.Size > maxBytes || !sha256Pattern.MatchString(attachment.SHA256) {
		return fmt.Errorf("invalid stored attachment integrity")
	}
	if !filepath.IsLocal(attachment.Path) || filepath.Clean(attachment.Path) != attachment.Path || !strings.HasPrefix(attachment.Path, "inbox"+string(filepath.Separator)) {
		return fmt.Errorf("invalid stored attachment path")
	}
	return nil
}

func validSingleLine(value string, maxRunes int) bool {
	value = strings.TrimSpace(value)
	return value != "" && utf8.RuneCountInString(value) <= maxRunes && !strings.ContainsAny(value, "\r\n")
}

func validAttachmentName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && len([]byte(name)) <= 180 && filepath.Base(name) == name && name != "." && !strings.ContainsAny(name, "\r\n/\\:")
}

func storedExtension(name string) string {
	extension := strings.ToLower(filepath.Ext(name))
	if safeExtensionRE.MatchString(extension) {
		return extension
	}
	return ""
}
