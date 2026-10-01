package management

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/access"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/conversation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/runtimecontrol"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/workspace"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

const ConsoleTokenName = "management-token"

//go:embed web/*
var consoleFiles embed.FS

type Recovery interface {
	Retry(context.Context, string, string, string) (request.Task, error)
	Redeliver(context.Context, string, string, string) error
	RestoreResult(context.Context, string, string) error
}
type ConsoleDependencies struct {
	Runtime                     *runtimecontrol.Controller
	Workspaces                  *workspace.Manager
	Threads                     *thread.Manager
	Targets                     *target.Store
	Requests                    *request.Store
	Preferences                 *preference.Store
	RemoteLock                  *access.RemoteLock
	Codex                       codex.ThreadClient
	Conversations               *conversation.Service
	Recovery                    Recovery
	OwnerID, PublicURL          string
	VisualEnabled, VoiceEnabled bool
}
type ConsoleServer struct {
	listen, token string
	deps          ConsoleDependencies
	ready         chan struct{}
	once          sync.Once
}

func NewConsoleServer(listen, token string, deps ConsoleDependencies) (*ConsoleServer, error) {
	if listen == "" || len(token) != 64 || deps.Runtime == nil || deps.Workspaces == nil || deps.Threads == nil || deps.Targets == nil || deps.Requests == nil || deps.Preferences == nil || deps.RemoteLock == nil || deps.Codex == nil || deps.Conversations == nil || deps.Recovery == nil || strings.TrimSpace(deps.OwnerID) == "" {
		return nil, fmt.Errorf("management dependencies incomplete")
	}
	if _, err := hex.DecodeString(token); err != nil {
		return nil, fmt.Errorf("invalid management token")
	}
	return &ConsoleServer{listen: listen, token: token, deps: deps, ready: make(chan struct{})}, nil
}
func (s *ConsoleServer) Ready() <-chan struct{} { return s.ready }
func (s *ConsoleServer) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: s.securityHeaders(s.handler()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second}
	s.once.Do(func() { close(s.ready) })
	return serveHTTP(ctx, server, listener)
}
func (s *ConsoleServer) handler() http.Handler {
	root, err := fs.Sub(consoleFiles, "web")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(root)))
	routes := map[string]http.HandlerFunc{
		"GET /api/snapshot":                        s.handleSnapshot,
		"GET /api/requests":                        s.handleRequests,
		"GET /api/artifacts":                       s.handleArtifacts,
		"GET /api/requests/{id}":                   s.handleRequestDetail,
		"GET /api/requests/{id}/artifacts/{index}": s.handleArtifact,
		"POST /api/requests/{id}/actions":          s.handleRequestAction,
		"GET /api/conversations/{id}":              s.handleSession,
		"GET /api/conversations":                   s.handleConversations,
		"POST /api/conversations":                  s.handleConversationNew,
		"POST /api/conversations/{id}/actions":     s.handleConversationAction,
		"PUT /api/target":                          s.handleTarget,
		"PUT /api/preferences":                     s.handlePreferences,
		"POST /api/runtime":                        s.handleRuntime,
		"POST /api/lock":                           s.handleLock,
	}
	for pattern, handler := range routes {
		mux.Handle(pattern, s.authorize(handler))
	}
	return mux
}
func (s *ConsoleServer) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		token := strings.TrimSpace(r.Header.Get("X-Codex-Link-Token"))
		if len(token) != len(s.token) || subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
			writeError(w, 401, "管理令牌无效")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *ConsoleServer) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, 400, "请求格式无效")
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeError(w, 400, "请求必须是一个 JSON 对象")
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

func (s *ConsoleServer) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	runtime := s.deps.Runtime.Snapshot()
	current := s.deps.Targets.Current(s.deps.OwnerID)
	definition, valid := s.deps.Workspaces.Get(current.WorkspaceID)
	currentView := map[string]any{"id": current.ID, "workspace_id": current.WorkspaceID, "workspace_name": definition.Name, "thread_id": current.ThreadID, "title": "首次消息时建立会话", "available": valid}
	if current.ThreadID != "" {
		item, err := s.deps.Codex.ReadThread(r.Context(), current.ThreadID)
		if err != nil {
			currentView["title"] = "当前会话暂不可访问"
			currentView["available"] = false
		} else if !workspace.Contains(item.Cwd, definition.Root) {
			currentView["title"] = "当前会话已不在原工作空间"
			currentView["available"] = false
		} else {
			currentView["title"] = threadTitle(item)
		}
	}
	choices := []presentation.ResponseModeDefinition{}
	for _, mode := range presentation.ResponseModes() {
		if s.modeAvailable(mode.ID) {
			choices = append(choices, mode)
		}
	}
	writeJSON(w, 200, map[string]any{
		"runtime": runtime, "target": currentView, "session": s.sessionView(r.Context(), current.ID, current.ThreadID), "workspaces": s.deps.Workspaces.List(), "execution": s.deps.Requests.Status(s.deps.OwnerID),
		"preferences": s.deps.Preferences.Get(s.deps.OwnerID), "modes": choices, "styles": presentation.Styles(),
		"capabilities": map[string]bool{"reading": s.deps.VisualEnabled, "voice": s.deps.VoiceEnabled, "remote_lock": s.deps.RemoteLock.Enabled()},
		"locked":       s.deps.RemoteLock.IsLocked(s.deps.OwnerID), "binding": map[string]any{"count": 1, "owner": s.deps.OwnerID, "public_url": s.deps.PublicURL},
		"capacity":  s.deps.Requests.Capacity(s.deps.OwnerID),
		"retention": map[string]int{"input_hours": 24, "result_days": 7, "history_days": 30},
	})
}
func (s *ConsoleServer) modeAvailable(mode presentation.ResponseMode) bool {
	return mode == presentation.ResponseText || mode == presentation.ResponseAdaptive || mode == presentation.ResponseReading && s.deps.VisualEnabled || mode == presentation.ResponseVoice && s.deps.VoiceEnabled
}
func (s *ConsoleServer) handlePreferences(w http.ResponseWriter, r *http.Request) {
	var input preference.OwnerPreferences
	if !decodeJSON(w, r, &input) {
		return
	}
	if !s.modeAvailable(input.ResponseMode) {
		writeError(w, 409, "当前实例没有启用所选回复能力")
		return
	}
	if err := s.deps.Preferences.Save(s.deps.OwnerID, input); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, input)
}
func (s *ConsoleServer) handleRuntime(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Action string `json:"action"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	switch input.Action {
	case "drain":
		writeJSON(w, 200, s.deps.Runtime.Drain())
	case "resume":
		writeJSON(w, 200, s.deps.Runtime.Resume())
	default:
		writeError(w, 400, "未知运行操作")
	}
}
func (s *ConsoleServer) handleLock(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Locked bool   `json:"locked"`
		Code   string `json:"code"`
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
		writeError(w, 400, "锁定操作失败，请检查配置或解锁码")
		return
	}
	writeJSON(w, 200, map[string]bool{"locked": input.Locked})
}
func ConsoleTokenPath(stateRoot string) string {
	layout, _ := storage.NewLayout(stateRoot)
	return layout.Database()
}

func EnsureConsoleToken(stateRoot string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate management token: %w", err)
	}
	token := hex.EncodeToString(raw)
	err := storage.Update(stateRoot, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO settings(key,value) VALUES('management.token',?) ON CONFLICT(key) DO NOTHING", token)
		return err
	})
	if err != nil {
		return "", err
	}
	return LoadConsoleToken(stateRoot)
}

func LoadConsoleToken(stateRoot string) (string, error) {
	token, found, err := storage.GetSetting(stateRoot, "management.token")
	if err != nil {
		return "", err
	}
	if !found {
		return "", os.ErrNotExist
	}
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return "", fmt.Errorf("management token is invalid")
	}
	return token, nil
}
