package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
)

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
