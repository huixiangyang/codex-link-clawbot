package appserver

import (
	"encoding/json"
	"log"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
)

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
