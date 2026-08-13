package messaging

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
)

const (
	deliveryStoreVersion = 3
	deliveryStoreLimit   = 50
)

// DeliverySource 把交付物固定到产生它的 codex-link-clawbot 请求和 Codex 线程。
type DeliverySource struct {
	ProjectID string `json:"project_id"`
	ThreadID  string `json:"thread_id"`
	TaskID    string `json:"task_id"`
}

type DeliveryRecord struct {
	ID string `json:"id"`
	DeliverySource
	Title     string `json:"title"`
	FilePath  string `json:"file_path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	CreatedAt int64  `json:"created_at"`
}

type DeliveryAvailability string

const (
	DeliveryAvailable   DeliveryAvailability = "available"
	DeliveryUnavailable DeliveryAvailability = "unavailable"
)

type DeliveryThreadSummary struct {
	Available   bool
	Total       int
	Resendable  int
	Unavailable int
}

type deliveryStoreState struct {
	Version int                         `json:"version"`
	Owners  map[string][]DeliveryRecord `json:"owners"`
}

// DeliveryStore 只保存交付物元数据；交付文件复制到私有持久目录以支持再次发送。
type DeliveryStore struct {
	mu      sync.RWMutex
	path    string
	fileDir string
	state   deliveryStoreState
	now     func() time.Time
}

func NewDeliveryStore(path string) (*DeliveryStore, error) {
	if strings.TrimSpace(path) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve delivery store path: %w", err)
		}
		path = filepath.Join(home, ".codex-link-clawbot", "library.json")
	}
	store := &DeliveryStore{
		path: filepath.Clean(path), fileDir: filepath.Join(filepath.Dir(path), "deliveries"), now: time.Now,
		state: deliveryStoreState{Version: deliveryStoreVersion, Owners: make(map[string][]DeliveryRecord)},
	}
	if err := statefile.EnsurePrivateDirectory(store.fileDir); err != nil {
		return nil, fmt.Errorf("create delivery archive: %w", err)
	}
	found, err := statefile.ReadJSON(store.path, &store.state, statefile.Options{
		Validate: store.validate,
	})
	if err != nil {
		return nil, fmt.Errorf("load delivery store: %w", err)
	}
	if !found {
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
		return store, nil
	}
	return store, nil
}

func (s *DeliveryStore) RecordDelivery(ownerID string, source DeliverySource, sourcePath string) (DeliveryRecord, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" || len(ownerID) > 512 || strings.ContainsAny(ownerID, "\r\n") {
		return DeliveryRecord{}, fmt.Errorf("delivery owner is required")
	}
	if err := validateDeliverySource(source); err != nil {
		return DeliveryRecord{}, err
	}
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return DeliveryRecord{}, fmt.Errorf("inspect delivery: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 {
		return DeliveryRecord{}, fmt.Errorf("delivery source must be a non-empty regular file")
	}
	record, err := newDeliveryRecord(source, filepath.Base(sourcePath), s.now())
	if err != nil {
		return DeliveryRecord{}, err
	}
	record.Size = info.Size()
	ownerHash := fmt.Sprintf("%x", sha256.Sum256([]byte(ownerID)))[:16]
	dir := filepath.Join(s.fileDir, ownerHash)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return DeliveryRecord{}, err
	}
	target := filepath.Join(dir, record.ID+filepath.Ext(record.Title))
	record.SHA256, err = copyPrivateFile(sourcePath, target)
	if err != nil {
		return DeliveryRecord{}, err
	}
	copiedInfo, err := os.Lstat(target)
	if err != nil {
		_ = os.Remove(target)
		return DeliveryRecord{}, fmt.Errorf("inspect copied delivery: %w", err)
	}
	if !copiedInfo.Mode().IsRegular() || copiedInfo.Mode()&os.ModeSymlink != 0 || copiedInfo.Size() <= 0 {
		_ = os.Remove(target)
		return DeliveryRecord{}, fmt.Errorf("copied delivery must be a non-empty regular file")
	}
	record.Size = copiedInfo.Size()
	record.FilePath = target
	pruned, err := s.append(ownerID, record)
	if err != nil {
		_ = os.Remove(target)
		return DeliveryRecord{}, err
	}
	for _, stale := range pruned {
		_ = os.Remove(stale.FilePath)
	}
	return record, nil
}

func (s *DeliveryStore) List(ownerID string) []DeliveryRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]DeliveryRecord(nil), s.state.Owners[ownerID]...)
}

func (s *DeliveryStore) Find(ownerID, id string) (DeliveryRecord, bool) {
	for _, record := range s.List(ownerID) {
		if record.ID == id {
			return record, true
		}
	}
	return DeliveryRecord{}, false
}

// Availability 只做低成本元数据检查，供列表和汇总使用。
// 再次发送前必须调用 Verify，重新校验完整 SHA-256。
func (s *DeliveryStore) Availability(record DeliveryRecord) DeliveryAvailability {
	if !s.recordPathAllowed(record.FilePath) {
		return DeliveryUnavailable
	}
	info, err := os.Lstat(record.FilePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != record.Size {
		return DeliveryUnavailable
	}
	return DeliveryAvailable
}

func (s *DeliveryStore) Verify(ownerID, id string) (DeliveryRecord, DeliveryAvailability, bool) {
	record, exists := s.Find(ownerID, id)
	if !exists {
		return DeliveryRecord{}, DeliveryUnavailable, false
	}
	if s.Availability(record) != DeliveryAvailable {
		return record, DeliveryUnavailable, true
	}
	if !s.verifyRecord(record) {
		return record, DeliveryUnavailable, true
	}
	return record, DeliveryAvailable, true
}

func (s *DeliveryStore) SummaryForThread(ownerID, projectID, threadID string) DeliveryThreadSummary {
	summary := DeliveryThreadSummary{Available: true}
	for _, record := range s.List(ownerID) {
		if record.ProjectID != projectID || record.ThreadID != threadID {
			continue
		}
		summary.Total++
		if s.verifyRecord(record) {
			summary.Resendable++
		} else {
			summary.Unavailable++
		}
	}
	return summary
}

func (s *DeliveryStore) append(ownerID string, record DeliveryRecord) ([]DeliveryRecord, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, fmt.Errorf("delivery owner is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := append([]DeliveryRecord(nil), s.state.Owners[ownerID]...)
	records := append([]DeliveryRecord{record}, previous...)
	var pruned []DeliveryRecord
	if len(records) > deliveryStoreLimit {
		pruned = append(pruned, records[deliveryStoreLimit:]...)
		records = records[:deliveryStoreLimit]
	}
	s.state.Owners[ownerID] = records
	if err := s.saveLocked(); err != nil {
		s.state.Owners[ownerID] = previous
		return nil, err
	}
	return pruned, nil
}

func (s *DeliveryStore) validate() error {
	if s.state.Version != deliveryStoreVersion || s.state.Owners == nil {
		return fmt.Errorf("invalid delivery store schema")
	}
	for ownerID, records := range s.state.Owners {
		if strings.TrimSpace(ownerID) == "" || len(records) > deliveryStoreLimit {
			return fmt.Errorf("invalid delivery owner")
		}
		for _, record := range records {
			if record.ID == "" || record.Title == "" || record.CreatedAt <= 0 || record.Size <= 0 ||
				len(record.SHA256) != sha256.Size*2 || !filepath.IsAbs(record.FilePath) || validateDeliverySource(record.DeliverySource) != nil {
				return fmt.Errorf("invalid delivery record")
			}
			if _, err := hex.DecodeString(record.SHA256); err != nil || !s.recordPathAllowed(record.FilePath) {
				return fmt.Errorf("delivery record escapes private archive")
			}
		}
	}
	return nil
}

func (s *DeliveryStore) saveLocked() error {
	return statefile.WriteJSON(s.path, s.state, statefile.Options{Validate: s.validate})
}

func newDeliveryRecord(source DeliverySource, title string, now time.Time) (DeliveryRecord, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return DeliveryRecord{}, err
	}
	title = normalizeSessionLine(title, 100)
	if title == "" {
		title = "未命名交付物"
	}
	return DeliveryRecord{
		ID: hex.EncodeToString(random), DeliverySource: source,
		Title: title, CreatedAt: now.Unix(),
	}, nil
}

func validateDeliverySource(source DeliverySource) error {
	for _, value := range []string{source.ProjectID, source.ThreadID, source.TaskID} {
		if strings.TrimSpace(value) == "" || len(value) > 512 || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("delivery source is incomplete")
		}
	}
	return nil
}

func (s *DeliveryStore) recordPathAllowed(path string) bool {
	relative, err := filepath.Rel(s.fileDir, filepath.Clean(path))
	return err == nil && relative != "." && filepath.IsLocal(relative)
}

func (s *DeliveryStore) verifyRecord(record DeliveryRecord) bool {
	if s.Availability(record) != DeliveryAvailable {
		return false
	}
	hash, err := hashPrivateFile(record.FilePath)
	return err == nil && hash == record.SHA256
}

func copyPrivateFile(source, target string) (string, error) {
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(output, hash), input); err != nil {
		output.Close()
		_ = os.Remove(target)
		return "", err
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(target)
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashPrivateFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
