package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
)

// Client communicates with Client App Server over stdio JSON-RPC.
type Client struct {
	command string
	model   string
	env     map[string]string

	mu            sync.Mutex
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	scanner       *bufio.Scanner
	started       bool
	nextID        atomic.Int64
	loadedThreads map[string]bool // 当前 app-server 进程已加载的显式线程
	threadStatus  map[string]codex.ThreadStatus
	threadUsage   map[string]codex.ThreadUsage
	instructions  map[string][]string
	rateLimits    codex.RateLimits
	hasRateLimits bool
	done          chan struct{}
	doneOnce      sync.Once
	exitErr       error

	// pending tracks in-flight JSON-RPC requests
	pendingMu sync.Mutex
	pending   map[int64]chan *rpcResponse

	notifyMu sync.Mutex
	turnCh   map[string]chan *codexTurnEvent

	stderr *codexStderrWriter // captures stderr for error reporting

	// rpcCall allows tests to stub JSON-RPC interactions without a subprocess.
	rpcCall func(ctx context.Context, method string, params interface{}) (json.RawMessage, error)
}

// Config 定义唯一支持的 Client App Server 进程配置。
type Config struct {
	Command string
	Model   string
	Env     map[string]string
}

// New 创建一个只使用稳定 App Server API 的 Client 客户端。
func New(cfg Config) *Client {
	if cfg.Command == "" {
		cfg.Command = "codex"
	}
	return &Client{
		command:       cfg.Command,
		model:         cfg.Model,
		env:           cfg.Env,
		loadedThreads: make(map[string]bool),
		threadStatus:  make(map[string]codex.ThreadStatus),
		threadUsage:   make(map[string]codex.ThreadUsage),
		instructions:  make(map[string][]string),
		pending:       make(map[int64]chan *rpcResponse),
		turnCh:        make(map[string]chan *codexTurnEvent),
		done:          make(chan struct{}),
	}
}

// Start 启动唯一的 Client App Server 子进程并完成握手。
func (a *Client) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return nil
	}

	a.cmd = exec.CommandContext(ctx, a.command, "app-server", "--listen", "stdio://")
	if len(a.env) > 0 {
		cmdEnv, err := mergeEnv(os.Environ(), a.env)
		if err != nil {
			a.mu.Unlock()
			return fmt.Errorf("build codex env: %w", err)
		}
		a.cmd.Env = cmdEnv
	}
	// Capture stderr for debugging and error reporting
	a.stderr = &codexStderrWriter{prefix: "[codex-stderr]"}
	a.cmd.Stderr = a.stderr

	var err error
	a.stdin, err = a.cmd.StdinPipe()
	if err != nil {
		a.mu.Unlock()
		return fmt.Errorf("create stdin pipe: %w", err)
	}

	stdout, err := a.cmd.StdoutPipe()
	if err != nil {
		a.mu.Unlock()
		return fmt.Errorf("create stdout pipe: %w", err)
	}

	if err := a.cmd.Start(); err != nil {
		a.mu.Unlock()
		return fmt.Errorf("start codex app-server %s: %w", a.command, err)
	}

	pid := a.cmd.Process.Pid
	log.Printf("[codex] started subprocess (command=%s, pid=%d)", a.command, pid)

	a.scanner = bufio.NewScanner(stdout)
	a.scanner.Buffer(make([]byte, 0, 4*1024*1024), 4*1024*1024) // 4MB
	a.started = true

	// Start reading loop
	go a.readLoop()

	// Release lock before calling initialize — call() needs a.mu to write to stdin
	a.mu.Unlock()

	// Initialize handshake with timeout
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	log.Printf("[codex] sending initialize handshake (pid=%d)...", pid)
	_, err = a.rpc(initCtx, "initialize", map[string]interface{}{
		"clientInfo": map[string]string{
			"name": "codex-link-clawbot", "title": "codex-link-clawbot", "version": "1.0.0",
		},
		"capabilities": map[string]interface{}{
			// 高频原文碎片对移动端阶段呈现没有价值，也不应进入进程内事件流。
			"optOutNotificationMethods": []string{
				"item/commandExecution/outputDelta", "item/commandExecution/terminalInteraction", "turn/diff/updated",
			},
		},
	})
	if err == nil {
		err = a.notify("initialized", map[string]interface{}{})
	}
	if err != nil {
		a.mu.Lock()
		a.started = false
		a.mu.Unlock()
		a.stdin.Close()
		a.cmd.Process.Kill()
		a.cmd.Wait()
		if detail := a.stderr.LastError(); detail != "" {
			return fmt.Errorf("codex startup failed: %s", detail)
		}
		return fmt.Errorf("codex startup failed (pid=%d): %w", pid, err)
	}

	log.Printf("[codex] initialized (pid=%d)", pid)
	go a.waitForExit()
	return nil
}

// Stop terminates the subprocess.
func (a *Client) Stop() {
	a.mu.Lock()
	if !a.started {
		a.mu.Unlock()
		return
	}
	stdin := a.stdin
	process := a.cmd.Process
	a.mu.Unlock()

	_ = stdin.Close()
	_ = process.Kill()
	<-a.done
}

// Done 在 App Server 退出时关闭，主进程必须随即退出并由服务管理器重启。
func (a *Client) Done() <-chan struct{} {
	return a.done
}

// ExitError 返回 App Server 的最终退出原因。
func (a *Client) ExitError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.exitErr
}

func (a *Client) waitForExit() {
	err := a.cmd.Wait()
	a.mu.Lock()
	a.started = false
	a.exitErr = err
	a.loadedThreads = make(map[string]bool)
	a.threadStatus = make(map[string]codex.ThreadStatus)
	a.instructions = make(map[string][]string)
	a.mu.Unlock()
	a.doneOnce.Do(func() { close(a.done) })
	if err != nil {
		log.Printf("[codex] app-server exited: %v", err)
	} else {
		log.Printf("[codex] app-server exited")
	}
}

func (a *Client) ensureCodexReady(ctx context.Context) error {
	a.mu.Lock()
	started := a.started
	a.mu.Unlock()
	if !started {
		return a.Start(ctx)
	}
	return nil
}

// Info 返回当前唯一 Client 运行时摘要。
func (a *Client) Info() codex.RuntimeInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	info := codex.RuntimeInfo{
		Model:   a.model,
		Command: a.command,
	}
	if a.cmd != nil && a.cmd.Process != nil {
		info.PID = a.cmd.Process.Pid
	}
	return info
}

func (a *Client) defaultModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

// codexStderrWriter 转发 stderr，并保留最后一条有效错误用于启动诊断。
type codexStderrWriter struct {
	prefix string
	mu     sync.Mutex
	last   string // last non-empty, non-traceback line
}

func (w *codexStderrWriter) Write(p []byte) (int, error) {
	lines := strings.Split(strings.TrimRight(string(p), "\n"), "\n")
	w.mu.Lock()
	for _, line := range lines {
		if line != "" {
			log.Printf("%s %s", w.prefix, line)
			// Capture lines that look like actual error messages (not traceback frames)
			if !strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "Traceback") && !strings.HasPrefix(line, "...") {
				w.last = line
			}
		}
	}
	w.mu.Unlock()
	return len(p), nil
}

// LastError returns the last captured error line and resets it.
func (w *codexStderrWriter) LastError() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.last
	w.last = ""
	return s
}
