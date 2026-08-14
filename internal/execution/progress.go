package execution

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
)

// ProgressConfig 定义真实轮次阶段的移动端呈现节奏。
type ProgressConfig struct {
	Enabled           bool
	TypingInterval    time.Duration
	FirstMessageDelay time.Duration
}

func DefaultProgressConfig() ProgressConfig {
	return ProgressConfig{Enabled: true, TypingInterval: 8 * time.Second, FirstMessageDelay: 15 * time.Second}
}

// Validate 在消息入口启动前校验完整的阶段呈现节奏。
func (config ProgressConfig) Validate() error {
	if config.Enabled && (config.TypingInterval <= 0 || config.FirstMessageDelay < 0) {
		return fmt.Errorf("progress delivery configuration is invalid")
	}
	return nil
}

// ProgressCallbacks 把阶段状态机与具体消息协议、日志和持久化隔离。
type ProgressCallbacks struct {
	Persist    func(string)
	SendTyping func(context.Context) error
	SendPhase  func(context.Context, string) error
	OnError    func(string, error)
}

type phaseUpdate struct {
	signature string
	message   string
}

// ProgressReporter 只接受 Codex 的结构化阶段事件，同一真实阶段只呈现一次。
type ProgressReporter struct {
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	config    ProgressConfig
	callbacks ProgressCallbacks
	events    chan phaseUpdate

	mu           sync.Mutex
	lastAccepted string
	turnID       string
	frozen       bool
	closeOnce    sync.Once
}

func NewProgressReporter(ctx context.Context, config ProgressConfig, callbacks ProgressCallbacks) (*ProgressReporter, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.Enabled && callbacks.SendPhase == nil {
		return nil, fmt.Errorf("progress delivery configuration is invalid")
	}
	reporterContext, cancel := context.WithCancel(ctx)
	reporter := &ProgressReporter{
		ctx: reporterContext, cancel: cancel, done: make(chan struct{}), config: config,
		callbacks: callbacks, events: make(chan phaseUpdate, 32),
	}
	go reporter.run()
	return reporter, nil
}

func (reporter *ProgressReporter) Report(event codex.TurnPhaseEvent) {
	update, accepted := reporter.accept(event)
	if !accepted {
		return
	}
	if reporter.callbacks.Persist != nil {
		reporter.callbacks.Persist(update.message)
	}
	if terminalTurnPhase(event.Phase) {
		reporter.cancel()
		return
	}
	if !reporter.config.Enabled {
		return
	}
	select {
	case reporter.events <- update:
	default:
		// 微信侧拥塞只丢弃中间气泡，已经持久化的真实阶段不受影响。
	}
}

func (reporter *ProgressReporter) Close() {
	reporter.closeOnce.Do(func() {
		reporter.mu.Lock()
		reporter.frozen = true
		reporter.mu.Unlock()
		reporter.cancel()
		<-reporter.done
	})
}

func (reporter *ProgressReporter) accept(event codex.TurnPhaseEvent) (phaseUpdate, bool) {
	update, valid := preparePhaseUpdate(event)
	if !valid {
		return phaseUpdate{}, false
	}
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if reporter.frozen || reporter.turnID != "" && reporter.turnID != event.TurnID || reporter.lastAccepted == update.signature {
		return phaseUpdate{}, false
	}
	if reporter.turnID == "" {
		reporter.turnID = event.TurnID
	}
	reporter.lastAccepted = update.signature
	if terminalTurnPhase(event.Phase) {
		reporter.frozen = true
	}
	return update, true
}

func (reporter *ProgressReporter) run() {
	defer close(reporter.done)
	if !reporter.config.Enabled {
		return
	}
	typingTicker := time.NewTicker(reporter.config.TypingInterval)
	messageTimer := time.NewTimer(reporter.config.FirstMessageDelay)
	defer typingTicker.Stop()
	defer messageTimer.Stop()

	reporter.sendTyping()
	var latest *phaseUpdate
	visible := false
	for {
		select {
		case <-reporter.ctx.Done():
			return
		case <-typingTicker.C:
			reporter.sendTyping()
		case update := <-reporter.events:
			latest = &update
			if visible {
				reporter.sendPhase(update.message)
			}
		case <-messageTimer.C:
			visible = true
			if latest != nil {
				reporter.sendPhase(latest.message)
			}
		}
	}
}

func (reporter *ProgressReporter) sendTyping() {
	if reporter.callbacks.SendTyping == nil {
		return
	}
	go func() {
		if err := reporter.callbacks.SendTyping(reporter.ctx); err != nil && reporter.ctx.Err() == nil {
			reporter.reportError("typing", err)
		}
	}()
}

func (reporter *ProgressReporter) sendPhase(message string) {
	if err := reporter.callbacks.SendPhase(reporter.ctx, message); err != nil {
		reporter.reportError("phase", err)
	}
}

func (reporter *ProgressReporter) reportError(operation string, err error) {
	if reporter.callbacks.OnError != nil {
		reporter.callbacks.OnError(operation, err)
	}
}

func preparePhaseUpdate(event codex.TurnPhaseEvent) (phaseUpdate, bool) {
	if strings.TrimSpace(event.TurnID) == "" {
		return phaseUpdate{}, false
	}
	update := phaseUpdate{signature: string(event.Phase)}
	switch event.Phase {
	case codex.TurnPhaseStarted:
		update.message = "Codex 阶段：轮次已开始"
	case codex.TurnPhaseReasoning:
		update.message = "Codex 阶段：正在推理"
	case codex.TurnPhasePlanning:
		if event.Total < 1 || event.Complete < 0 || event.Complete > event.Total {
			return phaseUpdate{}, false
		}
		step := presentation.Truncate(presentation.SanitizeActivity(event.Step), 72)
		update.signature = fmt.Sprintf("%s:%d:%d:%s", event.Phase, event.Complete, event.Total, step)
		update.message = fmt.Sprintf("Codex 阶段：计划 %d/%d", event.Complete, event.Total)
		if step != "" {
			update.message += " · " + step
		}
	case codex.TurnPhaseWorking:
		update.message = "Codex 阶段：正在执行工作项"
	case codex.TurnPhaseFinalizing:
		update.message = "Codex 阶段：正在生成最终回答"
	case codex.TurnPhaseCompleted:
		update.message = "Codex 阶段：轮次已完成"
	case codex.TurnPhaseFailed:
		update.message = "Codex 阶段：轮次执行失败"
	case codex.TurnPhaseInterrupted:
		update.message = "Codex 阶段：轮次已中断"
	default:
		return phaseUpdate{}, false
	}
	return update, true
}

func terminalTurnPhase(phase codex.TurnPhase) bool {
	return phase == codex.TurnPhaseCompleted || phase == codex.TurnPhaseFailed || phase == codex.TurnPhaseInterrupted
}
