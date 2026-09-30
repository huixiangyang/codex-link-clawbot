package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
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
	activeTurns   map[string]string
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

// --- JSON-RPC types ---

type rpcRequest struct {
	ID     int64       `json:"id"`
	Method string      `json:"method"`
	Params interface{} `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type codexTurnStartParams struct {
	ThreadID       string           `json:"threadId"`
	ApprovalPolicy string           `json:"approvalPolicy,omitempty"`
	Input          []codexUserInput `json:"input"`
	SandboxPolicy  interface{}      `json:"sandboxPolicy,omitempty"`
	Model          string           `json:"model,omitempty"`
	Effort         string           `json:"effort,omitempty"`
	Cwd            string           `json:"cwd,omitempty"`
}

type codexUserInput struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Path string `json:"path,omitempty"`
}

type codexTurnEvent struct {
	Kind      string
	TurnID    string
	Delta     string
	Text      string
	ItemID    string
	Phase     string
	Completed int
	Total     int
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
		activeTurns:   make(map[string]string),
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
	result, err := a.rpc(initCtx, "initialize", map[string]interface{}{
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

	log.Printf("[codex] initialized (pid=%d): %s", pid, string(result))
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
	a.activeTurns = make(map[string]string)
	a.instructions = make(map[string][]string)
	a.mu.Unlock()
	a.doneOnce.Do(func() { close(a.done) })
	if err != nil {
		log.Printf("[codex] app-server exited: %v", err)
	} else {
		log.Printf("[codex] app-server exited")
	}
}

// StartThread 创建一个持久化 Client 线程，归属关系由上层会话管理器保存。
func (a *Client) StartThread(ctx context.Context, workspaceRoot string) (codex.ThreadInfo, error) {
	if err := a.ensureCodexReady(ctx); err != nil {
		return codex.ThreadInfo{}, err
	}
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return codex.ThreadInfo{}, fmt.Errorf("workspace root is required")
	}
	model := a.defaultModel()
	params := map[string]interface{}{
		"approvalPolicy": "never",
		"cwd":            workspaceRoot,
		"sandbox":        "danger-full-access",
		"serviceName":    "codex-link-clawbot",
	}
	if model != "" {
		params["model"] = model
	}
	result, err := a.rpc(ctx, "thread/start", params)
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	thread, instructions, err := decodeOpenedThread(result, "thread/start")
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	a.mu.Lock()
	if a.instructions == nil {
		a.instructions = make(map[string][]string)
	}
	a.loadedThreads[thread.ID] = true
	a.threadStatus[thread.ID] = thread.Status
	a.instructions[thread.ID] = append([]string(nil), instructions...)
	a.mu.Unlock()
	thread.InstructionSources = instructions
	return thread, nil
}

// ResumeThread 从磁盘加载线程并订阅其事件。
func (a *Client) ResumeThread(ctx context.Context, threadID, workspaceRoot string) (codex.ThreadInfo, error) {
	if err := a.ensureCodexReady(ctx); err != nil {
		return codex.ThreadInfo{}, err
	}
	if strings.TrimSpace(threadID) == "" {
		return codex.ThreadInfo{}, fmt.Errorf("thread id is required")
	}
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return codex.ThreadInfo{}, fmt.Errorf("workspace root is required")
	}
	model := a.defaultModel()
	params := map[string]interface{}{
		"threadId":       threadID,
		"approvalPolicy": "never",
		"cwd":            workspaceRoot,
		"sandbox":        "danger-full-access",
		"serviceName":    "codex-link-clawbot",
	}
	if model != "" {
		params["model"] = model
	}
	result, err := a.rpc(ctx, "thread/resume", params)
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	thread, instructions, err := decodeOpenedThread(result, "thread/resume")
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	a.mu.Lock()
	if a.instructions == nil {
		a.instructions = make(map[string][]string)
	}
	a.loadedThreads[thread.ID] = true
	a.threadStatus[thread.ID] = thread.Status
	a.instructions[thread.ID] = append([]string(nil), instructions...)
	a.mu.Unlock()
	thread.InstructionSources = instructions
	return thread, nil
}

// ReadThread 只读取线程摘要，不加载完整轮次历史。
func (a *Client) ReadThread(ctx context.Context, threadID string) (codex.ThreadInfo, error) {
	if err := a.ensureCodexReady(ctx); err != nil {
		return codex.ThreadInfo{}, err
	}
	result, err := a.rpc(ctx, "thread/read", map[string]interface{}{
		"threadId":     threadID,
		"includeTurns": false,
	})
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	thread, err := decodeCodexThread(result, "thread/read")
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	a.mu.Lock()
	// 线程读取已返回服务端当前状态，覆盖可能过期的通知缓存。
	a.threadStatus[threadID] = thread.Status
	if a.instructions != nil {
		thread.InstructionSources = append([]string(nil), a.instructions[threadID]...)
	}
	a.mu.Unlock()
	return thread, nil
}

// ListThreads 查询 Client 全局线程页；上层必须按受信任工作空间过滤工作目录。
func (a *Client) ListThreads(ctx context.Context, options codex.ThreadListOptions) (codex.ThreadPage, error) {
	if err := a.ensureCodexReady(ctx); err != nil {
		return codex.ThreadPage{}, err
	}
	params := map[string]interface{}{
		"archived":      options.Archived,
		"sortKey":       "recency_at",
		"sortDirection": "desc",
	}
	if options.Cursor != "" {
		params["cursor"] = options.Cursor
	}
	if options.Limit > 0 {
		params["limit"] = options.Limit
	}
	if len(options.SourceKinds) > 0 {
		params["sourceKinds"] = options.SourceKinds
	}
	if options.Cwd != "" {
		params["cwd"] = options.Cwd
	}
	if options.SearchTerm != "" {
		params["searchTerm"] = options.SearchTerm
	}
	if options.Pinned != nil {
		params["isPinned"] = *options.Pinned
	}
	result, err := a.rpc(ctx, "thread/list", params)
	if err != nil {
		return codex.ThreadPage{}, err
	}
	var response struct {
		Data       []codex.ThreadInfo `json:"data"`
		NextCursor *string            `json:"nextCursor"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return codex.ThreadPage{}, fmt.Errorf("parse thread/list result: %w", err)
	}
	page := codex.ThreadPage{Threads: response.Data}
	if response.NextCursor != nil {
		page.NextCursor = *response.NextCursor
	}
	return page, nil
}

func (a *Client) SetThreadName(ctx context.Context, threadID, name string) error {
	if err := a.ensureCodexReady(ctx); err != nil {
		return err
	}
	_, err := a.rpc(ctx, "thread/name/set", map[string]string{"threadId": threadID, "name": name})
	return err
}

func (a *Client) ArchiveThread(ctx context.Context, threadID string) error {
	if err := a.ensureCodexReady(ctx); err != nil {
		return err
	}
	_, err := a.rpc(ctx, "thread/archive", map[string]string{"threadId": threadID})
	if err == nil {
		a.mu.Lock()
		delete(a.loadedThreads, threadID)
		delete(a.threadStatus, threadID)
		a.mu.Unlock()
	}
	return err
}

// ChatThread 在已经过归属校验的显式线程中执行一次轮次。
func (a *Client) ChatThread(ctx context.Context, threadID string, request codex.ChatRequest) (string, error) {
	return a.chatTurn(ctx, threadID, request, nil)
}

func (a *Client) ChatThreadWithProgress(ctx context.Context, threadID string, request codex.ChatRequest, onPhase codex.TurnPhaseHandler) (string, error) {
	return a.chatTurn(ctx, threadID, request, onPhase)
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

func decodeCodexThread(result json.RawMessage, method string) (codex.ThreadInfo, error) {
	var response struct {
		Thread codex.ThreadInfo `json:"thread"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return codex.ThreadInfo{}, fmt.Errorf("parse %s result: %w", method, err)
	}
	if response.Thread.ID == "" {
		return codex.ThreadInfo{}, fmt.Errorf("%s returned empty thread id", method)
	}
	return response.Thread, nil
}

func decodeOpenedThread(result json.RawMessage, method string) (codex.ThreadInfo, []string, error) {
	var response struct {
		Thread             codex.ThreadInfo `json:"thread"`
		InstructionSources []string         `json:"instructionSources"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return codex.ThreadInfo{}, nil, fmt.Errorf("parse %s result: %w", method, err)
	}
	if response.Thread.ID == "" {
		return codex.ThreadInfo{}, nil, fmt.Errorf("%s returned empty thread id", method)
	}
	return response.Thread, response.InstructionSources, nil
}

func (a *Client) ensureThreadLoaded(ctx context.Context, threadID, workspaceRoot string) error {
	a.mu.Lock()
	loaded := a.loadedThreads[threadID]
	a.mu.Unlock()
	if loaded {
		return nil
	}
	_, err := a.ResumeThread(ctx, threadID, workspaceRoot)
	return err
}

func (a *Client) chatTurn(ctx context.Context, threadID string, request codex.ChatRequest, onPhase codex.TurnPhaseHandler) (string, error) {
	if err := a.ensureCodexReady(ctx); err != nil {
		return "", err
	}
	if err := a.ensureThreadLoaded(ctx, threadID, request.WorkspaceRoot); err != nil {
		return "", fmt.Errorf("resume thread: %w", err)
	}

	a.mu.Lock()
	busy := a.threadStatus[threadID].Type == "active"
	a.mu.Unlock()
	if busy {
		return "", codex.ErrThreadBusy
	}

	pid := 0
	a.mu.Lock()
	if a.cmd != nil && a.cmd.Process != nil {
		pid = a.cmd.Process.Pid
	}
	a.mu.Unlock()

	log.Printf("[codex] using explicit thread (pid=%d, thread=%s)", pid, threadID)

	turnCh, release := a.registerTurnChannel(threadID)
	if turnCh == nil {
		return "", codex.ErrThreadBusy
	}
	defer release()

	// 轮次启动会立即返回轮次 ID；取消时必须携带它调用中断接口。
	// 短暂脱离用户取消信号，确保即使用户立刻取消也能拿到轮次 ID 后完成中断。
	input := codexInput(request)
	if len(input) == 0 {
		return "", fmt.Errorf("turn input is empty")
	}
	workspaceRoot := strings.TrimSpace(request.WorkspaceRoot)
	if workspaceRoot == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	model := a.defaultModel()
	if strings.TrimSpace(request.Model) != "" {
		model = strings.TrimSpace(request.Model)
	}

	startCtx, cancelStart := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	startResult, err := a.rpc(startCtx, "turn/start", codexTurnStartParams{
		ThreadID:       threadID,
		ApprovalPolicy: "never",
		Input:          input,
		SandboxPolicy:  map[string]interface{}{"type": "dangerFullAccess"},
		Model:          model,
		Effort:         strings.TrimSpace(request.Effort),
		Cwd:            workspaceRoot,
	})
	cancelStart()
	if err != nil {
		return "", fmt.Errorf("start turn: %w", err)
	}
	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(startResult, &started); err != nil {
		return "", fmt.Errorf("parse turn/start result: %w", err)
	}
	if started.Turn.ID == "" {
		return "", fmt.Errorf("turn/start returned empty turn id")
	}
	turnID := started.Turn.ID
	a.mu.Lock()
	if a.activeTurns == nil {
		a.activeTurns = make(map[string]string)
	}
	a.activeTurns[threadID] = turnID
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.activeTurns[threadID] == turnID {
			delete(a.activeTurns, threadID)
		}
		a.mu.Unlock()
	}()
	return a.collectTurn(ctx, threadID, turnID, turnCh, onPhase)
}

func codexInput(request codex.ChatRequest) []codexUserInput {
	input := make([]codexUserInput, 0, 1+len(request.LocalImages))
	if text := request.PromptText(); text != "" {
		input = append(input, codexUserInput{Type: "text", Text: text})
	}
	for _, imagePath := range request.LocalImages {
		if imagePath = strings.TrimSpace(imagePath); imagePath != "" {
			input = append(input, codexUserInput{Type: "localImage", Path: imagePath})
		}
	}
	return input
}

func (a *Client) registerTurnChannel(threadID string) (chan *codexTurnEvent, func()) {
	turnCh := make(chan *codexTurnEvent, 256)
	a.notifyMu.Lock()
	if a.turnCh[threadID] != nil {
		a.notifyMu.Unlock()
		return nil, func() {}
	}
	a.turnCh[threadID] = turnCh
	a.notifyMu.Unlock()
	return turnCh, func() {
		a.notifyMu.Lock()
		if a.turnCh[threadID] == turnCh {
			delete(a.turnCh, threadID)
		}
		a.notifyMu.Unlock()
	}
}

func (a *Client) collectTurn(ctx context.Context, threadID, turnID string, turnCh <-chan *codexTurnEvent, onPhase codex.TurnPhaseHandler) (string, error) {

	// Client 会把阶段说明和最终答案都作为 agentMessage 发出。
	// 必须按 phase 分流，否则微信端会把所有中间说明拼进最终回复。
	type messageState struct {
		phase string
		text  strings.Builder
	}
	messages := make(map[string]*messageState)
	var messageOrder []string
	var finalParts []string

	getMessage := func(itemID string) *messageState {
		state, ok := messages[itemID]
		if ok {
			return state
		}
		state = &messageState{}
		messages[itemID] = state
		messageOrder = append(messageOrder, itemID)
		return state
	}
	startedReported := false
	report := func(event codex.TurnPhaseEvent) {
		if event.Phase == codex.TurnPhaseStarted {
			if startedReported {
				return
			}
			startedReported = true
		}
		if onPhase != nil {
			onPhase(event)
		}
	}

	// turn/start 的响应即确定本轮编号，无需等待可能缺失的 started 通知。
	report(codex.TurnPhaseEvent{TurnID: turnID, Phase: codex.TurnPhaseStarted})
	for {
		select {
		case <-ctx.Done():
			outcome, err := a.interruptCodexTurn(threadID, turnID)
			if err != nil {
				return "", err
			}
			if outcome.Status == "completed" {
				return outcome.Reply, nil
			}
			if outcome.Status == "interrupted" {
				return "", errors.Join(codex.ErrTurnInterrupted, context.Canceled)
			}
			return "", fmt.Errorf("codex turn failed")
		case evt := <-turnCh:
			// 同一线程可能被其他 Client 客户端继续使用；只消费本次显式轮次的事件。
			if evt == nil || evt.TurnID != turnID {
				continue
			}
			if evt.Kind == "error" {
				return "", fmt.Errorf("turn error: %s", evt.Text)
			}

			switch evt.Kind {
			case "message_started":
				state := getMessage(evt.ItemID)
				state.phase = evt.Phase
				if evt.Text != "" {
					state.text.WriteString(evt.Text)
				}
			case "message_delta":
				getMessage(evt.ItemID).text.WriteString(evt.Delta)
			case "message_completed":
				state := getMessage(evt.ItemID)
				if evt.Phase != "" {
					state.phase = evt.Phase
				}
				text := strings.TrimSpace(evt.Text)
				if text == "" {
					text = strings.TrimSpace(state.text.String())
				}
				if text == "" {
					break
				}
				if state.phase != "commentary" {
					finalParts = append(finalParts, text)
				}
			case "phase":
				report(codex.TurnPhaseEvent{TurnID: turnID, Phase: codex.TurnPhase(evt.Phase)})
			case "plan":
				report(codex.TurnPhaseEvent{
					TurnID: turnID, Phase: codex.TurnPhasePlanning, Step: evt.Text,
					Complete: evt.Completed, Total: evt.Total,
				})
			case "terminal":
				terminal := codex.TurnPhase(evt.Phase)
				report(codex.TurnPhaseEvent{TurnID: turnID, Phase: terminal})
				if terminal == codex.TurnPhaseFailed {
					if strings.TrimSpace(evt.Text) == "" {
						return "", fmt.Errorf("codex turn failed")
					}
					return "", fmt.Errorf("codex turn failed: %s", evt.Text)
				}
				if terminal == codex.TurnPhaseInterrupted {
					return "", codex.ErrTurnInterrupted
				}
				if terminal != codex.TurnPhaseCompleted {
					return "", fmt.Errorf("codex returned invalid terminal turn status %q", terminal)
				}
				result := strings.TrimSpace(strings.Join(finalParts, "\n\n"))
				if result == "" {
					// phase 缺失时仅选择最后一条非 commentary 消息作为最终答案。
					for i := len(messageOrder) - 1; i >= 0; i-- {
						state := messages[messageOrder[i]]
						if state.phase == "commentary" {
							continue
						}
						result = strings.TrimSpace(state.text.String())
						if result != "" {
							break
						}
					}
				}
				if result == "" {
					return "", fmt.Errorf("codex returned empty response")
				}
				return result, nil
			}
		}
	}
}

// ActiveTurn 读取原生轮次 ID，供实时状态和精确打断使用。
func (a *Client) ActiveTurn(ctx context.Context, threadID string) (string, error) {
	if err := a.ensureCodexReady(ctx); err != nil {
		return "", err
	}
	result, err := a.rpc(ctx, "thread/read", map[string]interface{}{"threadId": threadID, "includeTurns": true})
	if err != nil {
		return "", err
	}
	var response struct {
		Thread struct {
			Turns []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return "", err
	}
	for _, turn := range response.Thread.Turns {
		if turn.Status == "inProgress" {
			return turn.ID, nil
		}
	}
	return "", nil
}

func (a *Client) InterruptTurn(ctx context.Context, threadID, turnID string) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	current, err := a.ActiveTurn(ctx, threadID)
	if err != nil {
		return err
	}
	if current == "" || current != turnID {
		return fmt.Errorf("这次执行已结束，未打断其他轮次")
	}
	_, err = a.interruptAndConfirm(ctx, threadID, turnID)
	return err
}

func (a *Client) interruptCodexTurn(threadID, turnID string) (codex.TurnResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	return a.interruptAndConfirm(ctx, threadID, turnID)
}

// RPC 接受打断不等于轮次已停止，必须再读指定轮次的终态。
func (a *Client) interruptAndConfirm(ctx context.Context, threadID, turnID string) (codex.TurnResult, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, sendErr := a.rpc(rpcCtx, "turn/interrupt", map[string]string{"threadId": threadID, "turnId": turnID})
	cancel()
	for {
		result, err := a.ReadTurn(ctx, threadID, turnID)
		if err == nil && (result.Status == "interrupted" || result.Status == "completed" || result.Status == "failed") {
			return result, nil
		}
		if sendErr != nil || err != nil {
			return result, fmt.Errorf("%w: %v", codex.ErrInterruptUnconfirmed, errors.Join(sendErr, err))
		}
		select {
		case <-ctx.Done():
			return result, fmt.Errorf("%w: %v", codex.ErrInterruptUnconfirmed, ctx.Err())
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func (a *Client) ReadTurn(ctx context.Context, threadID, turnID string) (codex.TurnResult, error) {
	raw, err := a.rpc(ctx, "thread/read", map[string]interface{}{"threadId": threadID, "includeTurns": true})
	if err != nil {
		return codex.TurnResult{}, err
	}
	var response struct {
		Thread struct {
			Turns []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Items  []struct {
					Type  string `json:"type"`
					Phase string `json:"phase"`
					Text  string `json:"text"`
				} `json:"items"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		return codex.TurnResult{}, err
	}
	for _, turn := range response.Thread.Turns {
		if turn.ID == turnID {
			var reply []string
			for _, item := range turn.Items {
				if item.Type == "agentMessage" && item.Phase != "commentary" {
					reply = append(reply, item.Text)
				}
			}
			return codex.TurnResult{ID: turn.ID, Status: turn.Status, Reply: strings.Join(reply, "\n\n")}, nil
		}
	}
	return codex.TurnResult{}, fmt.Errorf("指定轮次不可用")
}

func (a *Client) rpc(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if a.rpcCall != nil {
		return a.rpcCall(ctx, method, params)
	}
	return a.call(ctx, method, params)
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (a *Client) notify(method string, params interface{}) error {
	msg := struct {
		Method string      `json:"method"`
		Params interface{} `json:"params,omitempty"`
	}{
		Method: method,
		Params: params,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	a.mu.Lock()
	_, err = fmt.Fprintf(a.stdin, "%s\n", data)
	a.mu.Unlock()
	return err
}

// call sends a JSON-RPC request and waits for the response.
func (a *Client) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	id := a.nextID.Add(1)

	ch := make(chan *rpcResponse, 1)
	a.pendingMu.Lock()
	a.pending[id] = ch
	a.pendingMu.Unlock()

	defer func() {
		a.pendingMu.Lock()
		delete(a.pending, id)
		a.pendingMu.Unlock()
	}()

	req := rpcRequest{
		ID:     id,
		Method: method,
		Params: params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	a.mu.Lock()
	_, err = fmt.Fprintf(a.stdin, "%s\n", data)
	a.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write to stdin: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			msg := resp.Error.Message
			// Enrich with stderr context if available
			if a.stderr != nil {
				if detail := a.stderr.LastError(); detail != "" {
					msg = detail
				}
			}
			return nil, fmt.Errorf("codex error: %s", msg)
		}
		return resp.Result, nil
	}
}

// readLoop reads NDJSON lines from stdout and dispatches to pending requests or notification channels.
func (a *Client) readLoop() {
	for a.scanner.Scan() {
		line := a.scanner.Text()
		if line == "" {
			continue
		}

		var msg rpcResponse
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			log.Printf("[codex] failed to parse message: %v", err)
			continue
		}

		// Response to a request we made (has id, no method)
		if msg.ID != nil && msg.Method == "" {
			a.pendingMu.Lock()
			ch, ok := a.pending[*msg.ID]
			a.pendingMu.Unlock()
			if ok {
				ch <- &msg
			}
			continue
		}

		// 只处理 Client App Server 当前稳定事件。
		switch msg.Method {
		case "item/agentMessage/delta":
			a.handleCodexItemDelta(msg.Params)
		case "item/started":
			a.handleCodexItemStarted(msg.Params)
		case "item/completed":
			a.handleCodexItemCompleted(msg.Params)
		case "turn/plan/updated":
			a.handleCodexPlanUpdated(msg.Params)
		case "item/commandExecution/outputDelta", "item/commandExecution/terminalInteraction", "turn/diff/updated":
			// 原始终端片段和 diff 不属于轮次阶段，永远不能驱动微信进度。
		case "turn/started", "turn/completed":
			a.handleCodexTurnEvent(msg.Method, msg.Params)
		case "thread/status/changed":
			a.handleThreadStatusChanged(msg.Params)
		case "thread/tokenUsage/updated":
			a.handleThreadTokenUsageUpdated(msg.Params)
		case "account/rateLimits/updated":
			a.handleRateLimitsUpdated(msg.Params)
		case "thread/started", "thread/archived", "thread/unarchived", "thread/closed",
			"thread/name/updated", "thread/goal/updated", "thread/goal/cleared",
			"serverRequest/resolved", "remoteControl/status/changed":
			// 已知但无需转发到微信的稳定事件。

		default:
			if msg.Method != "" {
				log.Printf("[codex] unhandled method: %s (raw: %.200s)", msg.Method, line)
			}
		}
	}

	if err := a.scanner.Err(); err != nil {
		log.Printf("[codex] read loop error: %v", err)
	}
	log.Println("[codex] read loop ended")
}

func (a *Client) handleThreadTokenUsageUpdated(params json.RawMessage) {
	var update struct {
		ThreadID   string            `json:"threadId"`
		TokenUsage codex.ThreadUsage `json:"tokenUsage"`
	}
	if err := json.Unmarshal(params, &update); err != nil || update.ThreadID == "" {
		log.Printf("[codex] failed to parse thread/tokenUsage/updated: %v", err)
		return
	}
	a.mu.Lock()
	a.threadUsage[update.ThreadID] = update.TokenUsage
	a.mu.Unlock()
}

func (a *Client) handleRateLimitsUpdated(params json.RawMessage) {
	var update struct {
		RateLimits codex.RateLimits `json:"rateLimits"`
	}
	if err := json.Unmarshal(params, &update); err != nil {
		log.Printf("[codex] failed to parse account/rateLimits/updated: %v", err)
		return
	}
	a.mu.Lock()
	a.rateLimits = update.RateLimits
	a.hasRateLimits = true
	a.mu.Unlock()
}

func (a *Client) Usage(threadID string) (codex.ThreadUsage, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	usage, ok := a.threadUsage[threadID]
	return usage, ok
}

func (a *Client) RateLimits() (codex.RateLimits, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rateLimits, a.hasRateLimits
}

func (a *Client) handleThreadStatusChanged(params json.RawMessage) {
	var update struct {
		ThreadID string             `json:"threadId"`
		Status   codex.ThreadStatus `json:"status"`
	}
	if err := json.Unmarshal(params, &update); err != nil || update.ThreadID == "" {
		log.Printf("[codex] failed to parse thread/status/changed: %v", err)
		return
	}
	a.mu.Lock()
	a.threadStatus[update.ThreadID] = update.Status
	if update.Status.Type == "notLoaded" {
		delete(a.loadedThreads, update.ThreadID)
	}
	a.mu.Unlock()
}

// handleCodexItemDelta handles "item/agentMessage/delta" events.
// These contain incremental text deltas for the agent's response.
func (a *Client) handleCodexItemDelta(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		ItemID   string `json:"itemId"`
		Delta    string `json:"delta"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	if p.ThreadID == "" || p.TurnID == "" || p.ItemID == "" || p.Delta == "" {
		return
	}

	a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "message_delta", TurnID: p.TurnID, ItemID: p.ItemID, Delta: p.Delta})
}

// handleCodexItemStarted handles "item/started" events.
func (a *Client) handleCodexItemStarted(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Item     struct {
			ID    string `json:"id"`
			Type  string `json:"type"`
			Text  string `json:"text"`
			Phase string `json:"phase"`
		} `json:"item"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	if p.ThreadID == "" || p.TurnID == "" || p.Item.ID == "" {
		return
	}

	switch p.Item.Type {
	case "agentMessage":
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{
			Kind: "message_started", TurnID: p.TurnID, ItemID: p.Item.ID, Phase: p.Item.Phase, Text: p.Item.Text,
		})
		if p.Item.Phase == "final_answer" {
			a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "phase", TurnID: p.TurnID, Phase: string(codex.TurnPhaseFinalizing)})
		}
	case "reasoning":
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "phase", TurnID: p.TurnID, Phase: string(codex.TurnPhaseReasoning)})
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "subAgentActivity",
		"webSearch", "imageView", "sleep", "imageGeneration", "enteredReviewMode", "exitedReviewMode", "contextCompaction":
		// 只使用 item 类型表达“执行”，不读取命令、参数、路径、输出或 diff。
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "phase", TurnID: p.TurnID, Phase: string(codex.TurnPhaseWorking)})
	}
}

// handleCodexItemCompleted 使用完整 item 文本结束消息，避免依赖碎片拼接。
func (a *Client) handleCodexItemCompleted(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Item     struct {
			ID    string `json:"id"`
			Type  string `json:"type"`
			Text  string `json:"text"`
			Phase string `json:"phase"`
		} `json:"item"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	if p.ThreadID == "" || p.TurnID == "" || p.Item.ID == "" {
		return
	}

	if p.Item.Type == "agentMessage" {
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{
			Kind: "message_completed", TurnID: p.TurnID, ItemID: p.Item.ID, Phase: p.Item.Phase, Text: p.Item.Text,
		})
	}
}

// handleCodexPlanUpdated 将计划压缩成适合微信展示的一行阶段状态。
func (a *Client) handleCodexPlanUpdated(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Plan     []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	if p.ThreadID == "" || p.TurnID == "" || len(p.Plan) == 0 {
		return
	}

	completed := 0
	current := ""
	for _, step := range p.Plan {
		switch step.Status {
		case "completed":
			completed++
		case "inProgress":
			current = strings.TrimSpace(step.Step)
		case "pending":
		default:
			return
		}
	}
	a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{
		Kind: "plan", TurnID: p.TurnID, Text: current, Completed: completed, Total: len(p.Plan),
	})
}

// handleCodexTurnEvent 处理轮次开始与完成通知。
func (a *Client) handleCodexTurnEvent(method string, params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	if p.ThreadID == "" || p.Turn.ID == "" {
		return
	}
	if method == "turn/started" && p.Turn.Status == "inProgress" {
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "phase", TurnID: p.Turn.ID, Phase: string(codex.TurnPhaseStarted)})
		return
	}
	if method != "turn/completed" {
		return
	}
	terminal := codex.TurnPhase("")
	switch p.Turn.Status {
	case "completed":
		terminal = codex.TurnPhaseCompleted
	case "failed":
		terminal = codex.TurnPhaseFailed
	case "interrupted":
		terminal = codex.TurnPhaseInterrupted
	default:
		return
	}
	detail := ""
	if p.Turn.Error != nil {
		detail = p.Turn.Error.Message
	}
	a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "terminal", TurnID: p.Turn.ID, Phase: string(terminal), Text: detail})
}

// dispatchToTurnCh 把事件发送到指定线程的轮次通道。
func (a *Client) dispatchToTurnCh(threadID string, evt *codexTurnEvent) {
	a.notifyMu.Lock()
	ch, ok := a.turnCh[threadID]
	a.notifyMu.Unlock()

	if ok {
		select {
		case ch <- evt:
		default:
		}
	}
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
