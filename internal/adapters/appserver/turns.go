package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
)

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

// ChatThread 在已经过归属校验的显式线程中执行一次轮次。
func (a *Client) ChatThread(ctx context.Context, threadID string, request codex.ChatRequest) (string, error) {
	return a.chatTurn(ctx, threadID, request, nil)
}

func (a *Client) ChatThreadWithProgress(ctx context.Context, threadID string, request codex.ChatRequest, onPhase codex.TurnPhaseHandler) (string, error) {
	return a.chatTurn(ctx, threadID, request, onPhase)
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
