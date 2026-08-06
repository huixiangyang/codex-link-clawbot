package messaging

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/weclaw/internal/statefile"
)

const (
	libraryVersion = 1
	libraryLimit   = 50
)

type LibraryKind string

const (
	LibraryLink     LibraryKind = "link"
	LibraryDelivery LibraryKind = "delivery"
)

type LibraryRecord struct {
	ID        string      `json:"id"`
	Kind      LibraryKind `json:"kind"`
	ProjectID string      `json:"project_id,omitempty"`
	Title     string      `json:"title"`
	URL       string      `json:"url,omitempty"`
	FilePath  string      `json:"file_path,omitempty"`
	Size      int64       `json:"size,omitempty"`
	CreatedAt int64       `json:"created_at"`
}

type libraryFile struct {
	Version int                        `json:"version"`
	Owners  map[string][]LibraryRecord `json:"owners"`
}

// LibraryStore 保存素材与交付物元数据；交付文件复制到私有持久目录以支持再次发送。
type LibraryStore struct {
	mu      sync.RWMutex
	path    string
	fileDir string
	state   libraryFile
	now     func() time.Time
}

func NewLibraryStore(path string) (*LibraryStore, error) {
	if strings.TrimSpace(path) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve library path: %w", err)
		}
		path = filepath.Join(home, ".weclaw", "library.json")
	}
	store := &LibraryStore{
		path: filepath.Clean(path), fileDir: filepath.Join(filepath.Dir(path), "deliveries"), now: time.Now,
		state: libraryFile{Version: libraryVersion, Owners: make(map[string][]LibraryRecord)},
	}
	if err := statefile.EnsurePrivateDirectory(store.fileDir); err != nil {
		return nil, fmt.Errorf("create delivery archive: %w", err)
	}
	found, err := statefile.ReadJSON(store.path, &store.state, statefile.Options{
		Validate: store.validate,
	})
	if err != nil {
		return nil, fmt.Errorf("load library: %w", err)
	}
	if !found {
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
		return store, nil
	}
	return store, nil
}

func (s *LibraryStore) RecordLink(ownerID, projectID, title, rawURL string) (LibraryRecord, error) {
	record, err := newLibraryRecord(LibraryLink, projectID, title, s.now())
	if err != nil {
		return LibraryRecord{}, err
	}
	record.URL = strings.TrimSpace(rawURL)
	parsedURL, parseErr := url.Parse(record.URL)
	if record.URL == "" || parseErr != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return LibraryRecord{}, fmt.Errorf("link URL is required")
	}
	return record, s.append(ownerID, record)
}

func (s *LibraryStore) RecordDelivery(ownerID, projectID, sourcePath string) (LibraryRecord, error) {
	info, err := os.Stat(sourcePath)
	if err != nil || !info.Mode().IsRegular() {
		return LibraryRecord{}, fmt.Errorf("inspect delivery: %w", err)
	}
	record, err := newLibraryRecord(LibraryDelivery, projectID, filepath.Base(sourcePath), s.now())
	if err != nil {
		return LibraryRecord{}, err
	}
	record.Size = info.Size()
	ownerHash := fmt.Sprintf("%x", sha256.Sum256([]byte(ownerID)))[:16]
	dir := filepath.Join(s.fileDir, ownerHash)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return LibraryRecord{}, err
	}
	target := filepath.Join(dir, record.ID+filepath.Ext(record.Title))
	if err := copyPrivateFile(sourcePath, target); err != nil {
		return LibraryRecord{}, err
	}
	record.FilePath = target
	if err := s.append(ownerID, record); err != nil {
		_ = os.Remove(target)
		return LibraryRecord{}, err
	}
	return record, nil
}

func (s *LibraryStore) List(ownerID string, kind LibraryKind) []LibraryRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []LibraryRecord
	for _, record := range s.state.Owners[ownerID] {
		if kind == "" || record.Kind == kind {
			result = append(result, record)
		}
	}
	return result
}

func (s *LibraryStore) Find(ownerID, id string) (LibraryRecord, bool) {
	for _, record := range s.List(ownerID, "") {
		if record.ID == id {
			return record, true
		}
	}
	return LibraryRecord{}, false
}

func (s *LibraryStore) append(ownerID string, record LibraryRecord) error {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return fmt.Errorf("library owner is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := append([]LibraryRecord(nil), s.state.Owners[ownerID]...)
	records := append([]LibraryRecord{record}, previous...)
	if len(records) > libraryLimit {
		records = records[:libraryLimit]
	}
	s.state.Owners[ownerID] = records
	if err := s.saveLocked(); err != nil {
		s.state.Owners[ownerID] = previous
		return err
	}
	return nil
}

func (s *LibraryStore) validate() error {
	if s.state.Version != libraryVersion || s.state.Owners == nil {
		return fmt.Errorf("invalid library schema")
	}
	for ownerID, records := range s.state.Owners {
		if strings.TrimSpace(ownerID) == "" || len(records) > libraryLimit {
			return fmt.Errorf("invalid library owner")
		}
		for _, record := range records {
			if record.ID == "" || record.Title == "" || record.CreatedAt <= 0 || (record.Kind != LibraryLink && record.Kind != LibraryDelivery) {
				return fmt.Errorf("invalid library record")
			}
			if record.Kind == LibraryLink && record.URL == "" {
				return fmt.Errorf("invalid link record")
			}
			if record.Kind == LibraryDelivery && !filepath.IsAbs(record.FilePath) {
				return fmt.Errorf("invalid delivery record")
			}
			if record.Kind == LibraryDelivery {
				relative, err := filepath.Rel(s.fileDir, record.FilePath)
				if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					return fmt.Errorf("delivery record escapes private archive")
				}
				info, err := os.Lstat(record.FilePath)
				if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("delivery archive file is missing")
				}
			}
		}
	}
	return nil
}

func (s *LibraryStore) saveLocked() error {
	return statefile.WriteJSON(s.path, s.state, statefile.Options{Validate: s.validate})
}

func newLibraryRecord(kind LibraryKind, projectID, title string, now time.Time) (LibraryRecord, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return LibraryRecord{}, err
	}
	title = normalizeSessionLine(title, 100)
	if title == "" {
		title = "未命名素材"
	}
	return LibraryRecord{
		ID: hex.EncodeToString(random), Kind: kind, ProjectID: projectID,
		Title: title, CreatedAt: now.Unix(),
	}, nil
}

func copyPrivateFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		_ = os.Remove(target)
		return err
	}
	return output.Close()
}
