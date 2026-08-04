package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ACPAgent communicates with ACP-compatible agents (claude-agent-acp, codex-acp, cursor agent, etc.) via stdio JSON-RPC 2.0.
type ACPAgent struct {
	command      string
	args         []string
	model        string
	systemPrompt string
	cwd          string
	env          map[string]string
	protocol     string // "legacy_acp" or "codex_app_server"

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	scanner  *bufio.Scanner
	started  bool
	nextID   atomic.Int64
	sessions map[string]string // conversationID -> sessionID (legacy ACP)
	threads  map[string]string // conversationID -> threadID (codex app-server)

	// pending tracks in-flight JSON-RPC requests
	pendingMu sync.Mutex
	pending   map[int64]chan *rpcResponse

	// notifications channel for session/update events
	notifyMu sync.Mutex
	notifyCh map[string]chan *sessionUpdate // sessionID -> channel
	turnCh   map[string]chan *codexTurnEvent

	stderr *acpStderrWriter // captures stderr for error reporting

	// rpcCall allows tests to stub JSON-RPC interactions without a subprocess.
	rpcCall func(ctx context.Context, method string, params interface{}) (json.RawMessage, error)
}

// ACPAgentConfig holds configuration for the ACP agent.
type ACPAgentConfig struct {
	Command      string   // path to ACP agent binary (claude-agent-acp, codex-acp, cursor agent, etc.)
	Args         []string // extra args for command (e.g. ["acp"] for cursor)
	Model        string
	SystemPrompt string
	Cwd          string            // working directory
	Env          map[string]string // extra environment variables
}

// --- JSON-RPC types ---

type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
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

// --- ACP protocol types ---

type initParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities clientCapabilities `json:"clientCapabilities"`
}

type clientCapabilities struct {
	FS *fsCapabilities `json:"fs,omitempty"`
}

type fsCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type newSessionParams struct {
	Cwd        string        `json:"cwd"`
	McpServers []interface{} `json:"mcpServers"`
}

type newSessionResult struct {
	SessionID string `json:"sessionId"`
}

type promptParams struct {
	SessionID string        `json:"sessionId"`
	Prompt    []promptEntry `json:"prompt"`
}

type promptEntry struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type promptResult struct {
	StopReason string `json:"stopReason"`
}

type sessionUpdateParams struct {
	SessionID string        `json:"sessionId"`
	Update    sessionUpdate `json:"update"`
}

type sessionUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content,omitempty"`
	// For agent_message_chunk
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

type permissionRequestParams struct {
	ToolCall json.RawMessage    `json:"toolCall"`
	Options  []permissionOption `json:"options"`
}

type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// Codex app-server protocol constants and types.
const (
	protocolLegacyACP      = "legacy_acp"
	protocolCodexAppServer = "codex_app_server"
)

type codexTurnStartParams struct {
	ThreadID       string           `json:"threadId"`
	ApprovalPolicy string           `json:"approvalPolicy,omitempty"`
	Input          []codexUserInput `json:"input"`
	SandboxPolicy  interface{}      `json:"sandboxPolicy,omitempty"`
	Model          string           `json:"model,omitempty"`
	Cwd            string           `json:"cwd,omitempty"`
}

type codexUserInput struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Path string `json:"path,omitempty"`
}

type codexTurnEvent struct {
	Kind      string
	Delta     string
	Text      string
	ItemID    string
	Phase     string
	Completed int
	Total     int
}

func detectACPProtocol(command string, args []string) string {
	base := strings.ToLower(filepath.Base(command))
	// codex-acp is a standard ACP wrapper, NOT codex app-server
	// Only `codex app-server` uses the codex-native protocol
	if base == "codex" || base == "codex.exe" {
		for _, arg := range args {
			if arg == "app-server" {
				return protocolCodexAppServer
			}
		}
	}
	return protocolLegacyACP
}

// NewACPAgent creates a new ACP agent.
func NewACPAgent(cfg ACPAgentConfig) *ACPAgent {
	if cfg.Command == "" {
		cfg.Command = "claude-agent-acp"
	}
	if cfg.Cwd == "" {
		cfg.Cwd = defaultWorkspace()
	}
	protocol := detectACPProtocol(cfg.Command, cfg.Args)
	return &ACPAgent{
		command:      cfg.Command,
		args:         cfg.Args,
		model:        cfg.Model,
		systemPrompt: cfg.SystemPrompt,
		cwd:          cfg.Cwd,
		env:          cfg.Env,
		protocol:     protocol,
		sessions:     make(map[string]string),
		threads:      make(map[string]string),
		pending:      make(map[int64]chan *rpcResponse),
		notifyCh:     make(map[string]chan *sessionUpdate),
		turnCh:       make(map[string]chan *codexTurnEvent),
	}
}

// Start launches the claude-agent-acp subprocess and initializes the connection.
func (a *ACPAgent) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return nil
	}

	a.cmd = exec.CommandContext(ctx, a.command, a.args...)
	a.cmd.Dir = a.cwd
	if len(a.env) > 0 {
		cmdEnv, err := mergeEnv(os.Environ(), a.env)
		if err != nil {
			a.mu.Unlock()
			return fmt.Errorf("build acp env: %w", err)
		}
		a.cmd.Env = cmdEnv
	}
	// Capture stderr for debugging and error reporting
	a.stderr = &acpStderrWriter{prefix: "[acp-stderr]"}
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
		return fmt.Errorf("start acp agent %s: %w", a.command, err)
	}

	pid := a.cmd.Process.Pid
	log.Printf("[acp] started subprocess (command=%s, pid=%d)", a.command, pid)

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

	log.Printf("[acp] sending initialize handshake (pid=%d, protocol=%s)...", pid, a.protocol)
	var result json.RawMessage
	if a.protocol == protocolCodexAppServer {
		result, err = a.rpc(initCtx, "initialize", map[string]interface{}{
			"clientInfo": map[string]string{"name": "weclaw", "version": "0.3.0"},
		})
		if err == nil {
			// codex app-server expects an "initialized" notification after initialize response
			err = a.notify("initialized", nil)
		}
	} else {
		result, err = a.rpc(initCtx, "initialize", initParams{
			ProtocolVersion: 1,
			ClientCapabilities: clientCapabilities{
				FS: &fsCapabilities{ReadTextFile: true, WriteTextFile: true},
			},
		})
	}
	if err != nil {
		a.mu.Lock()
		a.started = false
		a.mu.Unlock()
		a.stdin.Close()
		a.cmd.Process.Kill()
		a.cmd.Wait()
		// Use stderr detail if available (e.g. "connect ECONNREFUSED")
		if detail := a.stderr.LastError(); detail != "" {
			return fmt.Errorf("agent startup failed: %s", detail)
		}
		// Provide a helpful hint when the binary looks like a Claude CLI that doesn't support ACP
		base := strings.ToLower(filepath.Base(a.command))
		if base == "claude" || base == "claude.exe" {
			return fmt.Errorf("agent startup failed (pid=%d): %w\n\nHint: the 'claude' CLI does not support ACP protocol directly.\nSet type to \"cli\" in your config, or install claude-agent-acp and set command to \"claude-agent-acp\".", pid, err)
		}
		return fmt.Errorf("agent startup failed (pid=%d): %w", pid, err)
	}

	log.Printf("[acp] initialized (pid=%d): %s", pid, string(result))
	return nil
}

// Stop terminates the subprocess.
func (a *ACPAgent) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.started {
		return
	}
	a.stdin.Close()
	a.cmd.Process.Kill()
	a.cmd.Wait()
	a.started = false
}

// SetCwd changes the working directory for subsequent sessions.
func (a *ACPAgent) SetCwd(cwd string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cwd = cwd
}

// ResetSession clears the existing session for the given conversationID and
// immediately creates a new one, returning the new session ID.
func (a *ACPAgent) ResetSession(ctx context.Context, conversationID string) (string, error) {
	if a.protocol == protocolCodexAppServer {
		a.mu.Lock()
		delete(a.threads, conversationID)
		a.mu.Unlock()
		log.Printf("[acp] thread reset (conversation=%s), creating new thread", conversationID)

		threadID, _, err := a.getOrCreateThread(ctx, conversationID)
		if err != nil {
			return "", fmt.Errorf("create new thread: %w", err)
		}
		return threadID, nil
	}

	a.mu.Lock()
	delete(a.sessions, conversationID)
	a.mu.Unlock()
	log.Printf("[acp] session reset (conversation=%s), creating new session", conversationID)

	sessionID, _, err := a.getOrCreateSession(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("create new session: %w", err)
	}
	return sessionID, nil
}

// Chat sends structured input and returns the final response.
func (a *ACPAgent) Chat(ctx context.Context, conversationID string, request ChatRequest) (string, error) {
	return a.chat(ctx, conversationID, request, nil)
}

// ChatWithProgress sends structured input and exposes safe progress updates.
func (a *ACPAgent) ChatWithProgress(ctx context.Context, conversationID string, request ChatRequest, onProgress ProgressHandler) (string, error) {
	return a.chat(ctx, conversationID, request, onProgress)
}

func (a *ACPAgent) chat(ctx context.Context, conversationID string, request ChatRequest, onProgress ProgressHandler) (string, error) {
	if !a.started {
		if err := a.Start(ctx); err != nil {
			return "", err
		}
	}

	// Route to codex app-server protocol if applicable
	if a.protocol == protocolCodexAppServer {
		return a.chatCodexAppServer(ctx, conversationID, request, onProgress)
	}
	if len(request.LocalImages) > 0 {
		return "", fmt.Errorf("ACP protocol %q does not support image input", a.protocol)
	}
	promptText := request.PromptText()
	if promptText == "" {
		return "", fmt.Errorf("prompt input is empty")
	}

	// Get or create session
	sessionID, isNew, err := a.getOrCreateSession(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("session error: %w", err)
	}

	pid := a.cmd.Process.Pid
	if isNew {
		log.Printf("[acp] new session created (pid=%d, session=%s, conversation=%s)", pid, sessionID, conversationID)
	} else {
		log.Printf("[acp] reusing session (pid=%d, session=%s, conversation=%s)", pid, sessionID, conversationID)
	}

	// Register notification channel for this session
	notifyCh := make(chan *sessionUpdate, 256)
	a.notifyMu.Lock()
	a.notifyCh[sessionID] = notifyCh
	a.notifyMu.Unlock()

	defer func() {
		a.notifyMu.Lock()
		delete(a.notifyCh, sessionID)
		a.notifyMu.Unlock()
	}()

	// Send prompt (this blocks until the prompt completes)
	type promptDoneMsg struct {
		result json.RawMessage
		err    error
	}
	promptDone := make(chan promptDoneMsg, 1)
	go func() {
		result, err := a.rpc(ctx, "session/prompt", promptParams{
			SessionID: sessionID,
			Prompt:    []promptEntry{{Type: "text", Text: promptText}},
		})
		if result != nil {
			log.Printf("[acp] prompt result (session=%s): %s", sessionID, string(result))
		}
		promptDone <- promptDoneMsg{result: result, err: err}
	}()

	// Collect text chunks from notifications
	var textParts []string

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case update := <-notifyCh:
			if update.SessionUpdate == "agent_message_chunk" {
				text := extractChunkText(update)
				if text != "" {
					textParts = append(textParts, text)
				}
			}
		case done := <-promptDone:
			// Drain remaining notifications
			for {
				select {
				case update := <-notifyCh:
					if update.SessionUpdate == "agent_message_chunk" {
						text := extractChunkText(update)
						if text != "" {
							textParts = append(textParts, text)
						}
					}
				default:
					goto drained
				}
			}
		drained:
			if done.err != nil {
				return "", fmt.Errorf("prompt error: %w", done.err)
			}
			result := strings.TrimSpace(strings.Join(textParts, ""))
			if result == "" {
				// Try extracting from prompt result (some agents return content here)
				result = extractPromptResultText(done.result)
			}
			if result == "" {
				return "", fmt.Errorf("agent returned empty response")
			}
			return result, nil
		}
	}
}

func (a *ACPAgent) getOrCreateSession(ctx context.Context, conversationID string) (string, bool, error) {
	a.mu.Lock()
	sid, exists := a.sessions[conversationID]
	a.mu.Unlock()

	if exists {
		return sid, false, nil
	}

	result, err := a.rpc(ctx, "session/new", newSessionParams{
		Cwd:        a.cwd,
		McpServers: []interface{}{},
	})
	if err != nil {
		return "", false, err
	}

	var sessionResult newSessionResult
	if err := json.Unmarshal(result, &sessionResult); err != nil {
		return "", false, fmt.Errorf("parse session result: %w", err)
	}

	a.mu.Lock()
	a.sessions[conversationID] = sessionResult.SessionID
	a.mu.Unlock()

	return sessionResult.SessionID, true, nil
}

// --- Codex app-server protocol ---

func (a *ACPAgent) getOrCreateThread(ctx context.Context, conversationID string) (string, bool, error) {
	a.mu.Lock()
	tid, exists := a.threads[conversationID]
	a.mu.Unlock()

	if exists {
		return tid, false, nil
	}

	params := map[string]interface{}{
		"approvalPolicy": "never",
		"cwd":            a.cwd,
		"sandbox":        "danger-full-access",
	}
	if a.model != "" {
		params["model"] = a.model
	}
	result, err := a.rpc(ctx, "thread/start", params)
	if err != nil {
		return "", false, err
	}

	var threadResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &threadResult); err != nil {
		return "", false, fmt.Errorf("parse thread/start result: %w", err)
	}
	if threadResult.Thread.ID == "" {
		return "", false, fmt.Errorf("thread/start returned empty thread id")
	}

	a.mu.Lock()
	a.threads[conversationID] = threadResult.Thread.ID
	a.mu.Unlock()

	return threadResult.Thread.ID, true, nil
}

func (a *ACPAgent) chatCodexAppServer(ctx context.Context, conversationID string, request ChatRequest, onProgress ProgressHandler) (string, error) {
	threadID, isNew, err := a.getOrCreateThread(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("thread error: %w", err)
	}

	pid := 0
	a.mu.Lock()
	if a.cmd != nil && a.cmd.Process != nil {
		pid = a.cmd.Process.Pid
	}
	a.mu.Unlock()

	if isNew {
		log.Printf("[acp] new thread created (pid=%d, thread=%s, conversation=%s)", pid, threadID, conversationID)
	} else {
		log.Printf("[acp] reusing thread (pid=%d, thread=%s, conversation=%s)", pid, threadID, conversationID)
	}

	// Register turn event channel
	turnCh := make(chan *codexTurnEvent, 256)
	a.notifyMu.Lock()
	a.turnCh[threadID] = turnCh
	a.notifyMu.Unlock()

	defer func() {
		a.notifyMu.Lock()
		delete(a.turnCh, threadID)
		a.notifyMu.Unlock()
	}()

	// turn/start 会立即返回 turn ID；取消时必须携带它调用 turn/interrupt。
	// 短暂脱离任务取消信号，确保即使用户立刻取消也能拿到 turn ID 后完成中断。
	input := make([]codexUserInput, 0, 1+len(request.LocalImages))
	if text := request.PromptText(); text != "" {
		input = append(input, codexUserInput{Type: "text", Text: text})
	}
	for _, imagePath := range request.LocalImages {
		imagePath = strings.TrimSpace(imagePath)
		if imagePath != "" {
			input = append(input, codexUserInput{Type: "localImage", Path: imagePath})
		}
	}
	if len(input) == 0 {
		return "", fmt.Errorf("turn input is empty")
	}

	startCtx, cancelStart := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	startResult, err := a.rpc(startCtx, "turn/start", codexTurnStartParams{
		ThreadID:       threadID,
		ApprovalPolicy: "never",
		Input:          input,
		SandboxPolicy:  map[string]interface{}{"type": "dangerFullAccess"},
		Model:          a.model,
		Cwd:            a.cwd,
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

	// Codex 会把阶段说明和最终答案都作为 agentMessage 发出。
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
	report := func(event ProgressEvent) {
		if onProgress != nil {
			onProgress(event)
		}
	}

	for {
		select {
		case <-ctx.Done():
			a.interruptCodexTurn(threadID, turnID)
			return "", ctx.Err()
		case evt := <-turnCh:
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
				if state.phase == "commentary" {
					report(ProgressEvent{Kind: ProgressCommentary, Text: text})
				} else {
					finalParts = append(finalParts, text)
				}
			case "plan":
				report(ProgressEvent{
					Kind:      ProgressPlan,
					Text:      evt.Text,
					Completed: evt.Completed,
					Total:     evt.Total,
				})
			case "activity":
				report(ProgressEvent{Kind: ProgressActivity, Text: evt.Text})
			case "completed":
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
					return "", fmt.Errorf("agent returned empty response")
				}
				return result, nil
			}
		}
	}
}

func (a *ACPAgent) interruptCodexTurn(threadID, turnID string) {
	interruptCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := a.rpc(interruptCtx, "turn/interrupt", map[string]string{
		"threadId": threadID,
		"turnId":   turnID,
	})
	if err != nil {
		log.Printf("[acp] failed to interrupt turn (thread=%s, turn=%s): %v", threadID, turnID, err)
		return
	}
	log.Printf("[acp] interrupted turn (thread=%s, turn=%s)", threadID, turnID)
}

func (a *ACPAgent) rpc(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if a.rpcCall != nil {
		return a.rpcCall(ctx, method, params)
	}
	return a.call(ctx, method, params)
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (a *ACPAgent) notify(method string, params interface{}) error {
	msg := struct {
		JSONRPC string      `json:"jsonrpc"`
		Method  string      `json:"method"`
		Params  interface{} `json:"params,omitempty"`
	}{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
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
func (a *ACPAgent) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
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
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
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
			return nil, fmt.Errorf("agent error: %s", msg)
		}
		return resp.Result, nil
	}
}

// readLoop reads NDJSON lines from stdout and dispatches to pending requests or notification channels.
func (a *ACPAgent) readLoop() {
	for a.scanner.Scan() {
		line := a.scanner.Text()
		if line == "" {
			continue
		}

		var msg rpcResponse
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			log.Printf("[acp] failed to parse message: %v", err)
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

		// Request from agent or notification
		switch msg.Method {
		case "session/update":
			a.handleSessionUpdate(msg.Params)

		case "session/request_permission":
			// Auto-allow all permissions
			a.handlePermissionRequest(line)

		// Codex app-server events (multiple protocol versions)
		case "codex/event/agent_message_delta":
			a.handleCodexDelta(msg.Params)
		case "item/agentMessage/delta":
			a.handleCodexItemDelta(msg.Params)
		case "item/started":
			a.handleCodexItemStarted(msg.Params)
		case "item/completed":
			a.handleCodexItemCompleted(msg.Params)
		case "turn/plan/updated":
			a.handleCodexPlanUpdated(msg.Params)
		case "item/commandExecution/outputDelta", "item/commandExecution/terminalInteraction":
			// 高频终端碎片仅视为已知事件；命令开始事件已经提供安全活动状态。
			// 这里绝不解析或转发原始输出，避免泄漏终端内容并挤占计划更新。
		case "turn/diff/updated":
			a.handleCodexActivity(msg.Params, "正在应用本机变更")
		case "turn/started", "turn/completed":
			a.handleCodexTurnEvent(msg.Method, msg.Params)
		case "codex/event/agent_message", "codex/event/task_complete",
			"codex/event/item_completed", "codex/event/token_count",
			"thread/started", "thread/tokenUsage/updated",
			"account/rateLimits/updated", "thread/status/changed":
			// Known events we don't need to act on
		case "turn/approval/request":
			a.handlePermissionRequest(line)

		default:
			if msg.Method != "" {
				log.Printf("[acp] unhandled method: %s (raw: %.200s)", msg.Method, line)
			}
		}
	}

	if err := a.scanner.Err(); err != nil {
		log.Printf("[acp] read loop error: %v", err)
	}
	log.Println("[acp] read loop ended")
}

func (a *ACPAgent) handleSessionUpdate(params json.RawMessage) {
	var p sessionUpdateParams
	if err := json.Unmarshal(params, &p); err != nil {
		log.Printf("[acp] failed to parse session/update: %v (raw: %s)", err, string(params))
		return
	}

	// Only log non-streaming events (skip chunks to reduce noise)
	switch p.Update.SessionUpdate {
	case "agent_message_chunk", "agent_thought_chunk":
		// skip — too noisy
	default:
		log.Printf("[acp] session/update (session=%s, type=%s)", p.SessionID, p.Update.SessionUpdate)
	}

	a.notifyMu.Lock()
	ch, ok := a.notifyCh[p.SessionID]
	a.notifyMu.Unlock()

	if ok {
		select {
		case ch <- &p.Update:
		default:
			log.Printf("[acp] notification channel full, dropping update (session=%s)", p.SessionID)
		}
	}
}

func (a *ACPAgent) handleCodexDelta(params json.RawMessage) {
	var p struct {
		Msg struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		} `json:"msg"`
		ConversationID string `json:"conversationId"`
		ThreadID       string `json:"threadId"` // some versions use threadId
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	// Try conversationId first (codex uses this), fallback to threadId
	key := p.ConversationID
	if key == "" {
		key = p.ThreadID
	}

	delta := p.Msg.Delta
	if delta == "" {
		return
	}

	// Find the turn channel by thread ID — we need to match against stored threads
	a.notifyMu.Lock()
	ch, ok := a.turnCh[key]
	if !ok {
		// Try matching by iterating all turn channels (codex uses conversationId, not threadId)
		for _, c := range a.turnCh {
			ch = c
			ok = true
			break
		}
	}
	a.notifyMu.Unlock()

	if ok {
		select {
		case ch <- &codexTurnEvent{Delta: delta}:
		default:
		}
	}
}

// handleCodexItemDelta handles "item/agentMessage/delta" events.
// These contain incremental text deltas for the agent's response.
func (a *ACPAgent) handleCodexItemDelta(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		ItemID   string `json:"itemId"`
		Delta    string `json:"delta"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	if p.Delta == "" {
		return
	}

	a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "message_delta", ItemID: p.ItemID, Delta: p.Delta})
}

// handleCodexItemStarted handles "item/started" events.
func (a *ACPAgent) handleCodexItemStarted(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
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

	switch p.Item.Type {
	case "agentMessage":
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{
			Kind: "message_started", ItemID: p.Item.ID, Phase: p.Item.Phase, Text: p.Item.Text,
		})
	case "commandExecution":
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "activity", Text: "正在执行本机操作"})
	case "fileChange":
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "activity", Text: "正在写入本机变更"})
	}
}

// handleCodexItemCompleted 使用完整 item 文本结束消息，避免依赖碎片拼接。
func (a *ACPAgent) handleCodexItemCompleted(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
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

	if p.Item.Type == "agentMessage" {
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{
			Kind: "message_completed", ItemID: p.Item.ID, Phase: p.Item.Phase, Text: p.Item.Text,
		})
	}
}

// handleCodexPlanUpdated 将计划压缩成适合微信展示的一行阶段状态。
func (a *ACPAgent) handleCodexPlanUpdated(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		Plan     []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	completed := 0
	current := ""
	for _, step := range p.Plan {
		if step.Status == "completed" {
			completed++
		}
		if step.Status == "inProgress" {
			current = strings.TrimSpace(step.Step)
		}
	}
	if current == "" && completed == len(p.Plan) && len(p.Plan) > 0 {
		current = "计划步骤已全部完成"
	}
	a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{
		Kind: "plan", Text: current, Completed: completed, Total: len(p.Plan),
	})
}

// handleCodexActivity 只报告安全的活动标签，原始命令输出绝不进入聊天端。
func (a *ACPAgent) handleCodexActivity(params json.RawMessage, text string) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "activity", Text: text})
}

// handleCodexTurnEvent handles "turn/started" and "turn/completed" notifications.
func (a *ACPAgent) handleCodexTurnEvent(method string, params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	if method == "turn/completed" {
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "completed"})
	}
}

// dispatchToTurnCh sends an event to the turn channel for a thread.
func (a *ACPAgent) dispatchToTurnCh(threadID string, evt *codexTurnEvent) {
	a.notifyMu.Lock()
	ch, ok := a.turnCh[threadID]
	if !ok {
		// Fallback: try any active turn channel
		for _, c := range a.turnCh {
			ch = c
			ok = true
			break
		}
	}
	a.notifyMu.Unlock()

	if ok {
		select {
		case ch <- evt:
		default:
		}
	}
}

func (a *ACPAgent) handlePermissionRequest(raw string) {
	// Parse the request to get the ID and auto-allow
	var req struct {
		ID     json.RawMessage         `json:"id"`
		Params permissionRequestParams `json:"params"`
	}
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		log.Printf("[acp] failed to parse permission request: %v", err)
		return
	}

	// Find the "allow" option
	optionID := "allow"
	for _, opt := range req.Params.Options {
		if opt.Kind == "allow" {
			optionID = opt.OptionID
			break
		}
	}

	// Send response
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result": map[string]interface{}{
			"outcome": map[string]interface{}{
				"outcome":  "selected",
				"optionId": optionID,
			},
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("[acp] failed to marshal permission response: %v", err)
		return
	}

	a.mu.Lock()
	fmt.Fprintf(a.stdin, "%s\n", data)
	a.mu.Unlock()

	log.Printf("[acp] auto-allowed permission request")
}

// Info returns metadata about this agent.
func (a *ACPAgent) Info() AgentInfo {
	info := AgentInfo{
		Name:    a.command,
		Type:    "acp",
		Model:   a.model,
		Command: a.command,
	}
	a.mu.Lock()
	if a.cmd != nil && a.cmd.Process != nil {
		info.PID = a.cmd.Process.Pid
	}
	a.mu.Unlock()
	return info
}

func extractChunkText(update *sessionUpdate) string {
	// The content field in agent_message_chunk can be a text content block
	if update.Text != "" {
		return update.Text
	}

	// Try to extract from content JSON
	if update.Content != nil {
		var content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(update.Content, &content); err == nil && content.Text != "" {
			return content.Text
		}
	}

	return ""
}

// extractPromptResultText tries to extract text from the session/prompt response.
// Some ACP agents include response content in the result alongside stopReason.
func extractPromptResultText(result json.RawMessage) string {
	if result == nil {
		return ""
	}

	// Try to extract content array from result
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		// Some agents use a flat text field
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return ""
	}

	if r.Text != "" {
		return r.Text
	}

	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "")
}

// acpStderrWriter forwards the ACP subprocess stderr to the application log
// and captures the last meaningful error line.
type acpStderrWriter struct {
	prefix string
	mu     sync.Mutex
	last   string // last non-empty, non-traceback line
}

func (w *acpStderrWriter) Write(p []byte) (int, error) {
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
func (w *acpStderrWriter) LastError() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.last
	w.last = ""
	return s
}
