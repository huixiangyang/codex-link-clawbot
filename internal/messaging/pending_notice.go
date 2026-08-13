package messaging

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
)

const (
	pendingNoticeVersion      = 1
	maxPendingNoticesPerOwner = 32
	maxPendingNoticeBodyRunes = 6000
	maxPendingNoticeTTL       = 30 * 24 * time.Hour
)

type PendingNoticeKind string

const (
	PendingNoticeDeployment   PendingNoticeKind = "deployment"
	PendingNoticeTaskRecovery PendingNoticeKind = "task_recovery"
)

func (kind PendingNoticeKind) valid() bool {
	switch kind {
	case PendingNoticeDeployment, PendingNoticeTaskRecovery:
		return true
	default:
		return false
	}
}

type PendingNotice struct {
	ID          string            `json:"id"`
	Kind        PendingNoticeKind `json:"kind"`
	DedupKey    string            `json:"dedup_key"`
	ReferenceID string            `json:"reference_id,omitempty"`
	Title       string            `json:"title"`
	Body        string            `json:"body"`
	CreatedAt   int64             `json:"created_at"`
	ExpiresAt   int64             `json:"expires_at"`
}

type PendingNoticeInput struct {
	Kind        PendingNoticeKind
	DedupKey    string
	ReferenceID string
	Title       string
	Body        string
	TTL         time.Duration
}

type pendingNoticeState struct {
	Version int                        `json:"version"`
	Owners  map[string][]PendingNotice `json:"owners"`
}

// PendingNoticeStore 保存因微信上下文令牌不可用而暂未送达的受限系统通知。
// 它不保存发送令牌，也不接受外部自由消息。
type PendingNoticeStore struct {
	mu    sync.RWMutex
	path  string
	now   func() time.Time
	state pendingNoticeState
}

func NewPendingNoticeStore(path string) (*PendingNoticeStore, error) {
	if strings.TrimSpace(path) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve pending notice path: %w", err)
		}
		path = filepath.Join(home, ".codex-link-clawbot", "pending-notices.json")
	}
	store := &PendingNoticeStore{
		path: filepath.Clean(path), now: time.Now,
		state: pendingNoticeState{Version: pendingNoticeVersion, Owners: make(map[string][]PendingNotice)},
	}
	found, err := statefile.ReadJSON(store.path, &store.state, statefile.Options{Validate: store.validate})
	if err != nil {
		return nil, fmt.Errorf("load pending notices: %w", err)
	}
	if !found {
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *PendingNoticeStore) Enqueue(ownerID string, input PendingNoticeInput) (PendingNotice, bool, error) {
	ownerID = strings.TrimSpace(ownerID)
	input.DedupKey = strings.TrimSpace(input.DedupKey)
	input.ReferenceID = strings.TrimSpace(input.ReferenceID)
	input.Title = strings.TrimSpace(input.Title)
	input.Body = strings.TrimSpace(input.Body)
	if !validNoticeLine(ownerID, 512) || !validNoticeLine(input.DedupKey, 256) || !validOptionalNoticeLine(input.ReferenceID, 256) ||
		!validNoticeLine(input.Title, 120) || !validNoticeBody(input.Body) ||
		!input.Kind.valid() || input.TTL <= 0 || input.TTL > maxPendingNoticeTTL {
		return PendingNotice{}, false, fmt.Errorf("pending notice input is invalid")
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	previousOwners := clonePendingNoticeOwners(s.state.Owners)
	changed := s.removeExpiredLocked(now.Unix())
	for _, notice := range s.state.Owners[ownerID] {
		if notice.DedupKey == input.DedupKey {
			if changed {
				if err := s.saveLocked(); err != nil {
					s.state.Owners = previousOwners
					return PendingNotice{}, false, err
				}
			}
			return notice, true, nil
		}
	}
	if len(s.state.Owners[ownerID]) >= maxPendingNoticesPerOwner {
		if changed {
			if err := s.saveLocked(); err != nil {
				s.state.Owners = previousOwners
				return PendingNotice{}, false, err
			}
		}
		return PendingNotice{}, false, fmt.Errorf("pending notice capacity exceeded")
	}
	id, err := newPendingNoticeID()
	if err != nil {
		return PendingNotice{}, false, err
	}
	notice := PendingNotice{
		ID: id, Kind: input.Kind, DedupKey: input.DedupKey, ReferenceID: input.ReferenceID,
		Title: input.Title, Body: input.Body, CreatedAt: now.Unix(), ExpiresAt: now.Add(input.TTL).Unix(),
	}
	s.state.Owners[ownerID] = append(s.state.Owners[ownerID], notice)
	if err := s.saveLocked(); err != nil {
		s.state.Owners = previousOwners
		return PendingNotice{}, false, err
	}
	return notice, false, nil
}

func (s *PendingNoticeStore) List(ownerID string, limit int) ([]PendingNotice, error) {
	ownerID = strings.TrimSpace(ownerID)
	if !validNoticeLine(ownerID, 512) {
		return nil, fmt.Errorf("pending notice owner is required")
	}
	if limit <= 0 || limit > maxPendingNoticesPerOwner {
		limit = maxPendingNoticesPerOwner
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previousOwners := clonePendingNoticeOwners(s.state.Owners)
	if s.removeExpiredLocked(s.now().Unix()) {
		if err := s.saveLocked(); err != nil {
			s.state.Owners = previousOwners
			return nil, err
		}
	}
	notices := s.state.Owners[ownerID]
	if len(notices) > limit {
		notices = notices[:limit]
	}
	return append([]PendingNotice(nil), notices...), nil
}

func (s *PendingNoticeStore) Complete(ownerID string, ids []string) error {
	ownerID = strings.TrimSpace(ownerID)
	if !validNoticeLine(ownerID, 512) || len(ids) == 0 {
		return fmt.Errorf("pending notice completion is invalid")
	}
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if !validNoticeID(id) {
			return fmt.Errorf("pending notice id is invalid")
		}
		selected[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previousOwners := clonePendingNoticeOwners(s.state.Owners)
	previous := s.state.Owners[ownerID]
	kept := make([]PendingNotice, 0, len(previous))
	for _, notice := range previous {
		if !selected[notice.ID] {
			kept = append(kept, notice)
		}
	}
	if len(kept) == len(previous) {
		return nil
	}
	if len(kept) == 0 {
		delete(s.state.Owners, ownerID)
	} else {
		s.state.Owners[ownerID] = append([]PendingNotice(nil), kept...)
	}
	if err := s.saveLocked(); err != nil {
		s.state.Owners = previousOwners
		return err
	}
	return nil
}

func (s *PendingNoticeStore) removeExpiredLocked(now int64) bool {
	changed := false
	for ownerID, notices := range s.state.Owners {
		kept := notices[:0]
		for _, notice := range notices {
			if notice.ExpiresAt > now {
				kept = append(kept, notice)
			} else {
				changed = true
			}
		}
		if len(kept) == 0 {
			delete(s.state.Owners, ownerID)
		} else {
			s.state.Owners[ownerID] = kept
		}
	}
	return changed
}

func (s *PendingNoticeStore) validate() error {
	if s.state.Version != pendingNoticeVersion || s.state.Owners == nil {
		return fmt.Errorf("pending notice schema is invalid")
	}
	for ownerID, notices := range s.state.Owners {
		if !validNoticeLine(ownerID, 512) || len(notices) > maxPendingNoticesPerOwner {
			return fmt.Errorf("pending notice owner is invalid")
		}
		seenIDs := make(map[string]bool, len(notices))
		seenKeys := make(map[string]bool, len(notices))
		for _, notice := range notices {
			if !validNoticeID(notice.ID) || seenIDs[notice.ID] || !notice.Kind.valid() ||
				!validNoticeLine(notice.DedupKey, 256) || seenKeys[notice.DedupKey] ||
				!validOptionalNoticeLine(notice.ReferenceID, 256) || !validNoticeLine(notice.Title, 120) || !validNoticeBody(notice.Body) ||
				notice.CreatedAt <= 0 || notice.ExpiresAt <= notice.CreatedAt || notice.ExpiresAt-notice.CreatedAt > int64(maxPendingNoticeTTL/time.Second) {
				return fmt.Errorf("pending notice record is invalid")
			}
			seenIDs[notice.ID] = true
			seenKeys[notice.DedupKey] = true
		}
	}
	return nil
}

func (s *PendingNoticeStore) saveLocked() error {
	return statefile.WriteJSON(s.path, s.state, statefile.Options{Validate: s.validate})
}

func clonePendingNoticeOwners(owners map[string][]PendingNotice) map[string][]PendingNotice {
	cloned := make(map[string][]PendingNotice, len(owners))
	for ownerID, notices := range owners {
		cloned[ownerID] = append([]PendingNotice(nil), notices...)
	}
	return cloned
}

func newPendingNoticeID() (string, error) {
	data := make([]byte, 8)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func validNoticeID(value string) bool {
	if len(value) != 16 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validNoticeLine(value string, maxRunes int) bool {
	value = strings.TrimSpace(value)
	return value != "" && utf8.RuneCountInString(value) <= maxRunes && !strings.ContainsAny(value, "\r\n\x00")
}

func validOptionalNoticeLine(value string, maxRunes int) bool {
	return strings.TrimSpace(value) == "" || validNoticeLine(value, maxRunes)
}

func validNoticeBody(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && utf8.RuneCountInString(value) <= maxPendingNoticeBodyRunes && !strings.ContainsRune(value, '\x00')
}
