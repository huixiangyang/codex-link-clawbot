package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/runtimecontrol"
)

const ManagementSocketName = "control.sock"

type DeploymentNotice struct {
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	Service     string `json:"service"`
}

const (
	DeploymentNotificationSent     = "sent"
	DeploymentNotificationDeferred = "deferred"
)

type DeploymentNotificationResult struct {
	Status string `json:"status"`
}

type DeploymentNotifier func(context.Context, DeploymentNotice) (DeploymentNotificationResult, error)

// ManagementServer 只通过本机 Unix socket 暴露生命周期控制面。
type ManagementServer struct {
	runtime    *runtimecontrol.Controller
	socketPath string
	notifier   DeploymentNotifier
	ready      chan struct{}
	once       sync.Once
}

func NewManagementServer(runtime *runtimecontrol.Controller, socketPath string, notifier DeploymentNotifier) *ManagementServer {
	return &ManagementServer{
		runtime:    runtime,
		socketPath: filepath.Clean(strings.TrimSpace(socketPath)),
		notifier:   notifier,
		ready:      make(chan struct{}),
	}
}

func (s *ManagementServer) Ready() <-chan struct{} {
	return s.ready
}

func (s *ManagementServer) Run(ctx context.Context) error {
	if err := validateManagementSocketPath(s.socketPath); err != nil {
		return err
	}
	if err := removeStaleManagementSocket(s.socketPath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen management socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(s.socketPath)
	}()
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		return fmt.Errorf("protect management socket: %w", err)
	}
	if err := ValidateManagementSocket(s.socketPath); err != nil {
		return err
	}

	server := &http.Server{Handler: s.handler()}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()

	s.once.Do(func() { close(s.ready) })
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve management socket: %w", err)
	}
	return nil
}

func (s *ManagementServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/admin/drain", s.handleDrain)
	mux.HandleFunc("/admin/resume", s.handleResume)
	mux.HandleFunc("/admin/deployment-notification", s.handleDeploymentNotification)
	return mux
}

func (s *ManagementServer) handleDeploymentNotification(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.notifier == nil {
		http.Error(w, "deployment notifier unavailable", http.StatusServiceUnavailable)
		return
	}
	reader := http.MaxBytesReader(w, request.Body, 4<<10)
	var notice DeploymentNotice
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&notice); err != nil {
		http.Error(w, "invalid deployment notice", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "deployment notice contains trailing data", http.StatusBadRequest)
		return
	}
	if err := validateDeploymentNotice(notice); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.notifier(request.Context(), notice)
	if err != nil {
		http.Error(w, "deployment notification failed", http.StatusBadGateway)
		return
	}
	if result.Status != DeploymentNotificationSent && result.Status != DeploymentNotificationDeferred {
		http.Error(w, "deployment notification returned invalid status", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func validateDeploymentNotice(notice DeploymentNotice) error {
	for field, value := range map[string]string{
		"from_version": notice.FromVersion,
		"to_version":   notice.ToVersion,
		"service":      notice.Service,
	} {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%s is invalid", field)
		}
	}
	return nil
}

func (s *ManagementServer) handleHealth(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	s.writeSnapshot(w)
}

func (s *ManagementServer) handleDrain(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.runtime == nil {
		http.Error(w, "runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.runtime.Drain())
}

func (s *ManagementServer) handleResume(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.runtime == nil {
		http.Error(w, "runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.runtime.Resume())
}

func (s *ManagementServer) writeSnapshot(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	if s.runtime == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(runtimecontrol.Snapshot{Status: runtimecontrol.StateDegraded})
		return
	}
	snapshot := s.runtime.Snapshot()
	if snapshot.Status != runtimecontrol.StateReady && snapshot.Status != runtimecontrol.StateDraining {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(snapshot)
}

func validateManagementSocketPath(path string) error {
	if path == "" || !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return fmt.Errorf("management socket must use a specific absolute path")
	}
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("inspect management socket directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("management socket directory must be a private 0700 directory")
	}
	if err := requireCurrentOwner(info); err != nil {
		return fmt.Errorf("inspect management socket directory: %w", err)
	}
	return nil
}

func removeStaleManagementSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect management socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("management socket path is not a socket")
	}
	if err := requireCurrentOwner(info); err != nil {
		return fmt.Errorf("inspect management socket: %w", err)
	}
	connection, dialErr := net.DialTimeout("unix", path, 150*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return fmt.Errorf("management socket is already active")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale management socket: %w", err)
	}
	return nil
}

// ValidateManagementSocket 在客户端拨号前验证路径、类型、所有者和最小权限。
func ValidateManagementSocket(path string) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if err := validateManagementSocketPath(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect management socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("management socket path is not a socket")
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("management socket must use 0600 permissions")
	}
	if err := requireCurrentOwner(info); err != nil {
		return fmt.Errorf("inspect management socket: %w", err)
	}
	return nil
}

func requireCurrentOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("owner metadata is unavailable")
	}
	if int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("path is not owned by the current user")
	}
	return nil
}
