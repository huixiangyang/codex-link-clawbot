package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/weclaw/internal/statefile"
)

const (
	sendReceiptStateVersion = 1
	maxSendReceipts         = 4096
	sendReceiptTTL          = 24 * time.Hour
	maxSendReceiptBytes     = 2 << 20
)

type ReceiptOutcome string

const (
	ReceiptReserved  ReceiptOutcome = "reserved"
	ReceiptSucceeded ReceiptOutcome = "succeeded"
	ReceiptFailed    ReceiptOutcome = "failed"
)

var (
	ErrIdempotencyConflict = errors.New("idempotency key conflict")
	receiptHashPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type sendReceipt struct {
	CallerID    string         `json:"caller_id"`
	RequestHash string         `json:"request_hash"`
	Outcome     ReceiptOutcome `json:"outcome"`
	CreatedAt   int64          `json:"created_at"`
	UpdatedAt   int64          `json:"updated_at"`
}

type sendReceiptState struct {
	Version  int                    `json:"version"`
	Receipts map[string]sendReceipt `json:"receipts"`
}

type ReceiptReservation struct {
	ID        string
	Outcome   string
	Duplicate bool
}

// SendReceiptStore 只保存请求指纹和投递结果，不保存绑定者、正文、URL 或 token。
type SendReceiptStore struct {
	path string
	mu   sync.Mutex
	now  func() time.Time
}

func NewSendReceiptStore(path string) (*SendReceiptStore, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return nil, fmt.Errorf("send receipt path must be a specific absolute path")
	}
	store := &SendReceiptStore{path: path, now: time.Now}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, err := store.loadLocked()
	if err != nil {
		return nil, err
	}
	if pruneSendReceipts(&state, store.now()) {
		if err := store.writeLocked(state); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *SendReceiptStore) Reserve(callerID, idempotencyKey, requestHash string) (ReceiptReservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return ReceiptReservation{}, err
	}
	now := s.now()
	pruned := pruneSendReceipts(&state, now)
	id := receiptID(callerID, idempotencyKey)
	if existing, found := state.Receipts[id]; found {
		if existing.RequestHash != requestHash {
			if pruned {
				_ = s.writeLocked(state)
			}
			return ReceiptReservation{}, ErrIdempotencyConflict
		}
		if pruned {
			if err := s.writeLocked(state); err != nil {
				return ReceiptReservation{}, err
			}
		}
		return ReceiptReservation{ID: id, Outcome: string(existing.Outcome), Duplicate: true}, nil
	}
	if len(state.Receipts) >= maxSendReceipts {
		return ReceiptReservation{}, fmt.Errorf("send receipt capacity reached")
	}
	unixNow := now.Unix()
	state.Receipts[id] = sendReceipt{
		CallerID: callerID, RequestHash: requestHash, Outcome: ReceiptReserved,
		CreatedAt: unixNow, UpdatedAt: unixNow,
	}
	if err := s.writeLocked(state); err != nil {
		return ReceiptReservation{}, err
	}
	return ReceiptReservation{ID: id, Outcome: string(ReceiptReserved)}, nil
}

func (s *SendReceiptStore) Complete(id string, outcome ReceiptOutcome) error {
	if outcome != ReceiptSucceeded && outcome != ReceiptFailed {
		return fmt.Errorf("invalid receipt outcome")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return err
	}
	receipt, found := state.Receipts[id]
	if !found {
		return fmt.Errorf("send receipt does not exist")
	}
	if receipt.Outcome == ReceiptSucceeded || receipt.Outcome == ReceiptFailed {
		if receipt.Outcome != outcome {
			return fmt.Errorf("send receipt outcome is already final")
		}
		return nil
	}
	receipt.Outcome = outcome
	receipt.UpdatedAt = s.now().Unix()
	state.Receipts[id] = receipt
	return s.writeLocked(state)
}

func (s *SendReceiptStore) loadLocked() (sendReceiptState, error) {
	state := sendReceiptState{Version: sendReceiptStateVersion, Receipts: make(map[string]sendReceipt)}
	found, err := statefile.ReadJSON(s.path, &state, statefile.Options{
		MaxBytes: maxSendReceiptBytes,
		Validate: state.validate,
	})
	if err != nil {
		return sendReceiptState{}, err
	}
	if !found {
		return state, nil
	}
	return state, nil
}

func (s *SendReceiptStore) writeLocked(state sendReceiptState) error {
	return statefile.WriteJSON(s.path, state, statefile.Options{
		MaxBytes: maxSendReceiptBytes,
		Validate: state.validate,
	})
}

func (s sendReceiptState) validate() error {
	if s.Version != sendReceiptStateVersion || s.Receipts == nil || len(s.Receipts) > maxSendReceipts {
		return fmt.Errorf("invalid v1 send receipt state")
	}
	for id, receipt := range s.Receipts {
		if !receiptHashPattern.MatchString(id) || !receiptHashPattern.MatchString(receipt.RequestHash) || !callerIDPattern.MatchString(receipt.CallerID) {
			return fmt.Errorf("invalid v1 send receipt identity")
		}
		if receipt.Outcome != ReceiptReserved && receipt.Outcome != ReceiptSucceeded && receipt.Outcome != ReceiptFailed {
			return fmt.Errorf("invalid v1 send receipt outcome")
		}
		if receipt.CreatedAt <= 0 || receipt.UpdatedAt < receipt.CreatedAt {
			return fmt.Errorf("invalid v1 send receipt timestamp")
		}
	}
	return nil
}

func pruneSendReceipts(state *sendReceiptState, now time.Time) bool {
	cutoff := now.Add(-sendReceiptTTL).Unix()
	changed := false
	for id, receipt := range state.Receipts {
		if receipt.CreatedAt <= cutoff {
			delete(state.Receipts, id)
			changed = true
		}
	}
	return changed
}

func receiptID(callerID, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(callerID + "\x00" + idempotencyKey))
	return hex.EncodeToString(digest[:])
}
