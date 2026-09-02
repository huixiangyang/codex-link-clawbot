package management

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/access"
	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/delivery"
	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/runtimecontrol"
	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/workspace"
)

const ConsoleTokenName = "management-token"

//go:embed web/*
var consoleFiles embed.FS

type QueueController interface {
	Wake()
	Cancel(ownerID string) bool
	TryRuntimeControl(action func()) bool
}

type ConsoleDependencies struct {
	Runtime      *runtimecontrol.Controller
	Workspaces   *workspace.Manager
	Threads      *thread.Manager
	Requests     *request.Store
	Deliveries   *delivery.Store
	Preferences  *preference.Store
	RemoteLock   *access.RemoteLock
	Codex        codex.ThreadClient
	Queue        QueueController
	OwnerID      string
	AccountCount int
	PublicURL    string
}

type ConsoleServer struct {
	listen string
	token  string
	deps   ConsoleDependencies
	ready  chan struct{}
	once   sync.Once
}

func NewConsoleServer(listen, token string, dependencies ConsoleDependencies) (*ConsoleServer, error) {
	listen = strings.TrimSpace(listen)
	token = strings.TrimSpace(token)
	if listen == "" || len(token) != 64 || dependencies.Runtime == nil || dependencies.Workspaces == nil ||
		dependencies.Threads == nil || dependencies.Requests == nil || dependencies.Deliveries == nil ||
		dependencies.Preferences == nil || dependencies.RemoteLock == nil || dependencies.Codex == nil ||
		dependencies.Queue == nil || strings.TrimSpace(dependencies.OwnerID) == "" {
		return nil, fmt.Errorf("management console dependencies are incomplete")
	}
	if _, err := hex.DecodeString(token); err != nil {
		return nil, fmt.Errorf("management token is invalid")
	}
	return &ConsoleServer{listen: listen, token: token, deps: dependencies, ready: make(chan struct{})}, nil
}

func (s *ConsoleServer) Ready() <-chan struct{} { return s.ready }

func (s *ConsoleServer) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.listen)
	if err != nil {
		return fmt.Errorf("listen management console: %w", err)
	}
	server := &http.Server{
		Handler:           s.securityHeaders(s.handler()),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	s.once.Do(func() { close(s.ready) })
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve management console: %w", err)
	}
	return nil
}

func (s *ConsoleServer) handler() http.Handler {
	webRoot, err := fs.Sub(consoleFiles, "web")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(webRoot)))
	mux.Handle("GET /api/snapshot", s.authorize(http.HandlerFunc(s.handleSnapshot)))
	mux.Handle("POST /api/workspaces/select", s.authorize(http.HandlerFunc(s.handleWorkspaceSelect)))
	mux.Handle("POST /api/threads/new", s.authorize(http.HandlerFunc(s.handleThreadNew)))
	mux.Handle("POST /api/threads/target", s.authorize(http.HandlerFunc(s.handleThreadTarget)))
	mux.Handle("POST /api/queue/action", s.authorize(http.HandlerFunc(s.handleQueueAction)))
	mux.Handle("POST /api/preferences", s.authorize(http.HandlerFunc(s.handlePreferences)))
	mux.Handle("POST /api/runtime/action", s.authorize(http.HandlerFunc(s.handleRuntimeAction)))
	mux.Handle("POST /api/security/lock", s.authorize(http.HandlerFunc(s.handleRemoteLock)))
	return mux
}

func (s *ConsoleServer) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimSpace(r.Header.Get("X-Codex-Link-Token"))
		if len(provided) != len(s.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "管理令牌无效")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *ConsoleServer) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; manifest-src 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

type consoleSnapshot struct {
	Runtime     runtimecontrol.Snapshot `json:"runtime"`
	Connection  connectionSnapshot      `json:"connection"`
	Workspaces  []workspaceSnapshot     `json:"workspaces"`
	Threads     []threadSnapshot        `json:"threads"`
	Queue       queueSnapshot           `json:"queue"`
	Deliveries  []deliverySnapshot      `json:"deliveries"`
	Preferences preferenceSnapshot      `json:"preferences"`
	Options     optionSnapshot          `json:"options"`
	Warning     string                  `json:"warning,omitempty"`
}

type connectionSnapshot struct {
	AccountCount      int    `json:"account_count"`
	WeChatConnected   bool   `json:"wechat_connected"`
	CodexReady        bool   `json:"codex_ready"`
	CurrentWorkspace  string `json:"current_workspace"`
	CurrentThreadID   string `json:"current_thread_id,omitempty"`
	CurrentThreadName string `json:"current_thread_name,omitempty"`
	PublicURL         string `json:"public_url,omitempty"`
}

type workspaceSnapshot struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Root    string `json:"root"`
	Current bool   `json:"current"`
}

type threadSnapshot struct {
	ID            string `json:"id"`
	ShortID       string `json:"short_id"`
	Title         string `json:"title"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	Path          string `json:"path"`
	Status        string `json:"status"`
	UpdatedAt     int64  `json:"updated_at"`
	Current       bool   `json:"current"`
}

type queueSnapshot struct {
	Paused bool           `json:"paused"`
	Tasks  []taskSnapshot `json:"tasks"`
}

type taskSnapshot struct {
	ID        string        `json:"id"`
	ShortID   string        `json:"short_id"`
	Summary   string        `json:"summary"`
	State     request.State `json:"state"`
	Stage     string        `json:"stage"`
	ProjectID string        `json:"project_id"`
	ThreadID  string        `json:"thread_id,omitempty"`
	CreatedAt int64         `json:"created_at"`
}

type deliverySnapshot struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"created_at"`
	Available bool   `json:"available"`
}

type preferenceSnapshot struct {
	ResponseMode      presentation.ResponseMode `json:"response_mode"`
	Style             presentation.Style        `json:"style"`
	RemoteLocked      bool                      `json:"remote_locked"`
	RemoteLockEnabled bool                      `json:"remote_lock_enabled"`
}

type optionSnapshot struct {
	ResponseModes []selectOption `json:"response_modes"`
	Styles        []selectOption `json:"styles"`
}

type selectOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *ConsoleServer) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	runtimeSnapshot := s.deps.Runtime.Snapshot()
	currentWorkspace := s.deps.Workspaces.Current(s.deps.OwnerID)
	result := consoleSnapshot{
		Runtime: runtimeSnapshot,
		Connection: connectionSnapshot{
			AccountCount: s.deps.AccountCount, WeChatConnected: runtimeSnapshot.WeChat.Healthy > 0,
			CodexReady: runtimeSnapshot.Codex.Ready, CurrentWorkspace: currentWorkspace.Name,
			PublicURL: strings.TrimRight(s.deps.PublicURL, "/"),
		},
		Preferences: preferenceSnapshot{
			ResponseMode:      s.deps.Preferences.Get(s.deps.OwnerID).ResponseMode,
			Style:             s.deps.Preferences.Get(s.deps.OwnerID).Style,
			RemoteLocked:      s.deps.RemoteLock.IsLocked(s.deps.OwnerID),
			RemoteLockEnabled: s.deps.RemoteLock.Enabled(),
		},
		Options: optionSnapshot{ResponseModes: responseModeOptions(), Styles: styleOptions()},
	}
	for _, item := range s.deps.Workspaces.List() {
		result.Workspaces = append(result.Workspaces, workspaceSnapshot{
			ID: item.ID, Name: item.Name, Root: item.Root, Current: item.ID == currentWorkspace.ID,
		})
	}
	workspaces := threadWorkspaces(s.deps.Workspaces.List())
	page, err := s.deps.Threads.GlobalList(r.Context(), s.deps.OwnerID, s.deps.Codex, workspaces, false, false, "", 1, 12)
	if err != nil {
		result.Warning = "Codex 线程目录暂时不可用"
	} else {
		for _, item := range page.Items {
			title := strings.TrimSpace(item.Info.Name)
			if title == "" {
				title = strings.TrimSpace(item.Info.Preview)
			}
			if title == "" {
				title = "未命名线程"
			}
			result.Threads = append(result.Threads, threadSnapshot{
				ID: item.Info.ID, ShortID: thread.ShortCode(item.Info.ID), Title: title,
				WorkspaceID: item.WorkspaceID, WorkspaceName: item.WorkspaceName, Path: item.Info.Cwd,
				Status: item.Info.Status.Type, UpdatedAt: item.Info.UpdatedAt, Current: item.Current,
			})
			if item.Current {
				result.Connection.CurrentThreadID = item.Info.ID
				result.Connection.CurrentThreadName = title
			}
		}
	}
	ownerStatus := s.deps.Requests.Status(s.deps.OwnerID)
	result.Queue.Paused = ownerStatus.Paused
	for _, task := range s.deps.Requests.List(s.deps.OwnerID) {
		result.Queue.Tasks = append(result.Queue.Tasks, taskSnapshot{
			ID: task.ID, ShortID: shortID(task.ID), Summary: task.Summary, State: task.State,
			Stage: task.Stage, ProjectID: task.ProjectID, ThreadID: task.ThreadID, CreatedAt: task.CreatedAt,
		})
	}
	for _, record := range s.deps.Deliveries.List(s.deps.OwnerID) {
		result.Deliveries = append(result.Deliveries, deliverySnapshot{
			ID: record.ID, Title: record.Title, Size: record.Size, CreatedAt: record.CreatedAt,
			Available: s.deps.Deliveries.Availability(record) == delivery.Available,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *ConsoleServer) handleWorkspaceSelect(w http.ResponseWriter, r *http.Request) {
	var input struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if _, err := s.deps.Workspaces.Select(s.deps.OwnerID, input.WorkspaceID); err != nil {
		writeError(w, http.StatusBadRequest, "无法切换工作空间")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *ConsoleServer) handleThreadNew(w http.ResponseWriter, r *http.Request) {
	var input struct {
		WorkspaceID string `json:"workspace_id"`
		Name        string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var actionErr error
	if !s.deps.Queue.TryRuntimeControl(func() {
		if _, actionErr = s.deps.Workspaces.Select(s.deps.OwnerID, input.WorkspaceID); actionErr != nil {
			return
		}
		_, actionErr = s.deps.Threads.New(r.Context(), s.deps.OwnerID, s.deps.Codex, input.Name)
	}) {
		writeError(w, http.StatusConflict, "Codex 正在执行任务，暂时不能新建线程")
		return
	}
	if actionErr != nil {
		writeError(w, http.StatusBadRequest, "无法新建 Codex 线程")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}

func (s *ConsoleServer) handleThreadTarget(w http.ResponseWriter, r *http.Request) {
	var input struct {
		WorkspaceID string `json:"workspace_id"`
		ThreadID    string `json:"thread_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	definition, exists := s.deps.Workspaces.Get(strings.TrimSpace(input.WorkspaceID))
	if !exists {
		writeError(w, http.StatusBadRequest, "工作空间不存在")
		return
	}
	var actionErr error
	if !s.deps.Queue.TryRuntimeControl(func() {
		if _, actionErr = s.deps.Workspaces.Select(s.deps.OwnerID, definition.ID); actionErr != nil {
			return
		}
		_, actionErr = s.deps.Threads.UseGlobalThread(r.Context(), s.deps.OwnerID, thread.Workspace{
			ID: definition.ID, Name: definition.Name, Root: definition.Root,
		}, input.ThreadID, s.deps.Codex)
	}) {
		writeError(w, http.StatusConflict, "Codex 正在执行任务，暂时不能切换目标")
		return
	}
	if actionErr != nil {
		writeError(w, http.StatusBadRequest, "无法切换目标线程")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *ConsoleServer) handleQueueAction(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Action string `json:"action"`
		TaskID string `json:"task_id,omitempty"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var err error
	switch input.Action {
	case "pause":
		err = s.deps.Requests.SetPaused(s.deps.OwnerID, true)
	case "resume":
		err = s.deps.Requests.SetPaused(s.deps.OwnerID, false)
		s.deps.Queue.Wake()
	case "move_front":
		_, err = s.deps.Requests.MoveToFront(s.deps.OwnerID, input.TaskID)
		s.deps.Queue.Wake()
	case "delete":
		_, err = s.deps.Requests.DeleteQueued(s.deps.OwnerID, input.TaskID)
	case "clear":
		_, err = s.deps.Requests.ClearQueued(s.deps.OwnerID)
	case "cancel":
		if !s.deps.Queue.Cancel(s.deps.OwnerID) {
			writeError(w, http.StatusConflict, "当前没有可取消的执行")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "未知队列操作")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "队列操作失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *ConsoleServer) handlePreferences(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ResponseMode presentation.ResponseMode `json:"response_mode"`
		Style        presentation.Style        `json:"style"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.deps.Preferences.SetResponseMode(s.deps.OwnerID, input.ResponseMode); err != nil {
		writeError(w, http.StatusBadRequest, "回答方式无效")
		return
	}
	if err := s.deps.Preferences.SetStyle(s.deps.OwnerID, input.Style); err != nil {
		writeError(w, http.StatusBadRequest, "视觉风格无效")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *ConsoleServer) handleRuntimeAction(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Action string `json:"action"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var snapshot runtimecontrol.Snapshot
	switch input.Action {
	case "drain":
		snapshot = s.deps.Runtime.Drain()
	case "resume":
		snapshot = s.deps.Runtime.Resume()
	default:
		writeError(w, http.StatusBadRequest, "未知运行时操作")
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *ConsoleServer) handleRemoteLock(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Locked bool   `json:"locked"`
		Code   string `json:"code,omitempty"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var err error
	if input.Locked {
		err = s.deps.RemoteLock.Lock(s.deps.OwnerID)
	} else {
		err = s.deps.RemoteLock.Unlock(s.deps.OwnerID, input.Code)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "远程锁定操作失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	reader := http.MaxBytesReader(w, r.Body, 32<<10)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "请求包含多余内容")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func threadWorkspaces(items []workspace.Definition) []thread.Workspace {
	result := make([]thread.Workspace, 0, len(items))
	for _, item := range items {
		result = append(result, thread.Workspace{ID: item.ID, Name: item.Name, Root: item.Root})
	}
	return result
}

func responseModeOptions() []selectOption {
	definitions := presentation.ResponseModes()
	result := make([]selectOption, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, selectOption{ID: string(definition.ID), Name: definition.Name, Description: definition.Description})
	}
	return result
}

func styleOptions() []selectOption {
	definitions := presentation.Styles()
	result := make([]selectOption, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, selectOption{ID: string(definition.ID), Name: definition.Name, Description: definition.Description})
	}
	return result
}

func shortID(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[len(value)-8:]
}

func ConsoleTokenPath(stateRoot string) string {
	return filepath.Join(filepath.Clean(stateRoot), ConsoleTokenName)
}

func EnsureConsoleToken(stateRoot string) (string, error) {
	if err := statefile.EnsurePrivateDirectory(stateRoot); err != nil {
		return "", err
	}
	path := ConsoleTokenPath(stateRoot)
	if token, err := LoadConsoleToken(stateRoot); err == nil {
		return token, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate management token: %w", err)
	}
	token := hex.EncodeToString(raw)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadConsoleToken(stateRoot)
		}
		return "", fmt.Errorf("create management token: %w", err)
	}
	if _, err := io.WriteString(file, token+"\n"); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write management token: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("sync management token: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close management token: %w", err)
	}
	return token, nil
}

func LoadConsoleToken(stateRoot string) (string, error) {
	path := ConsoleTokenPath(stateRoot)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || info.Size() > 256 {
		return "", fmt.Errorf("management token file is not a protected regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return "", fmt.Errorf("management token is invalid")
	}
	return token, nil
}
