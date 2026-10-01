package appserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
)

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
				log.Printf("[codex] unhandled method: %q", msg.Method)
			}
		}
	}

	if err := a.scanner.Err(); err != nil {
		log.Printf("[codex] read loop error: %v", err)
	}
	log.Println("[codex] read loop ended")
}
