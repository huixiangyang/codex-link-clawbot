package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/ilink"
)

// ProgressConfig 使用 duration 表达内部节奏，配置层负责从秒数转换。
type ProgressConfig struct {
	Enabled           bool
	TypingInterval    time.Duration
	FirstMessageDelay time.Duration
	MessageInterval   time.Duration
}

func DefaultProgressConfig() ProgressConfig {
	return ProgressConfig{
		Enabled:           true,
		TypingInterval:    8 * time.Second,
		FirstMessageDelay: 15 * time.Second,
		MessageInterval:   45 * time.Second,
	}
}

type activeTask struct {
	started  time.Time
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.RWMutex
	status   string
	stopping bool
	finished bool
}

func newActiveTask(parent context.Context) *activeTask {
	ctx, cancel := context.WithCancel(parent)
	return &activeTask{
		started: time.Now(),
		ctx:     ctx,
		cancel:  cancel,
		status:  "任务已接收，正在分析",
	}
}

func (t *activeTask) setStatus(status string) {
	status = strings.TrimSpace(status)
	if status == "" {
		return
	}
	t.mu.Lock()
	if t.stopping || t.finished {
		t.mu.Unlock()
		return
	}
	t.status = status
	t.mu.Unlock()
}

func (t *activeTask) context() context.Context {
	return t.ctx
}

func (t *activeTask) requestCancel() bool {
	t.mu.Lock()
	if t.stopping || t.finished {
		t.mu.Unlock()
		return false
	}
	t.stopping = true
	t.status = "正在取消任务"
	cancel := t.cancel
	t.mu.Unlock()

	cancel()
	return true
}

func (t *activeTask) cancelRequested() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.stopping
}

// finish 原子地结束任务，并返回最终结果是否仍允许发送。
// 取消先拿到锁时结果必须丢弃；完成先拿到锁时后续取消不再被接受。
func (t *activeTask) finish() bool {
	t.mu.Lock()
	if t.finished {
		t.mu.Unlock()
		return false
	}
	t.finished = true
	deliver := !t.stopping
	cancel := t.cancel
	t.mu.Unlock()
	cancel()
	return deliver
}

func (t *activeTask) statusSummary() string {
	t.mu.RLock()
	status := t.status
	stopping := t.stopping
	t.mu.RUnlock()
	taskState := "运行中"
	if stopping {
		taskState = "正在取消"
	}
	return fmt.Sprintf("任务状态：%s\n已运行：%s\n当前阶段：%s", taskState, formatElapsed(time.Since(t.started)), status)
}

func (t *activeTask) busySummary() string {
	t.mu.RLock()
	status := t.status
	t.mu.RUnlock()
	return fmt.Sprintf("上一项任务仍在执行，已运行 %s。\n当前状态：%s\n为避免覆盖任务事件，本条消息未交给 Codex；任务完成后请重新发送。", formatElapsed(time.Since(t.started)), status)
}

type progressReporter struct {
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	client       *ilink.Client
	userID       string
	contextToken string
	config       ProgressConfig
	task         *activeTask
	events       chan agent.ProgressEvent
	closeOnce    sync.Once
}

func newProgressReporter(ctx context.Context, client *ilink.Client, userID, contextToken string, config ProgressConfig, task *activeTask) *progressReporter {
	reporterCtx, cancel := context.WithCancel(ctx)
	r := &progressReporter{
		ctx:          reporterCtx,
		cancel:       cancel,
		done:         make(chan struct{}),
		client:       client,
		userID:       userID,
		contextToken: contextToken,
		config:       config,
		task:         task,
		events:       make(chan agent.ProgressEvent, 32),
	}
	go r.run()
	return r
}

func (r *progressReporter) Report(event agent.ProgressEvent) {
	select {
	case r.events <- event:
	default:
		// 高频命令输出只承担活动保活作用，满载时丢弃不会影响最终答案。
	}
}

func (r *progressReporter) Close() {
	r.closeOnce.Do(func() {
		r.cancel()
		<-r.done
	})
}

func (r *progressReporter) run() {
	defer close(r.done)
	if !r.config.Enabled {
		return
	}

	typingTicker := time.NewTicker(r.config.TypingInterval)
	messageTimer := time.NewTimer(r.config.FirstMessageDelay)
	defer typingTicker.Stop()
	defer messageTimer.Stop()

	r.sendTyping()
	latest := "任务已接收，正在分析"
	latestKind := agent.ProgressKind("")
	sentMessages := make(map[string]struct{})

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-typingTicker.C:
			r.sendTyping()
		case event := <-r.events:
			if status := formatProgressEvent(event); status != "" {
				// 命令活动是最低优先级状态，不能覆盖更有信息量的阶段说明或计划。
				if event.Kind == agent.ProgressActivity && latestKind != "" && latestKind != agent.ProgressActivity {
					continue
				}
				latest = status
				latestKind = event.Kind
				r.task.setStatus(status)
			}
		case <-messageTimer.C:
			if message, ok := unsentProgress(latest, sentMessages); ok {
				if err := SendTextReply(r.ctx, r.client, r.userID, message, r.contextToken, NewClientID()); err != nil {
					log.Printf("[progress] failed to send progress to %s: %v", r.userID, err)
				} else {
					sentMessages[message] = struct{}{}
				}
			}
			messageTimer.Reset(r.config.MessageInterval)
		}
	}
}

// unsentProgress 保证同一次任务中的文字详情只发送一次。
// 发送失败时由调用方保留未发送状态，以便下一个周期重试。
func unsentProgress(latest string, sentMessages map[string]struct{}) (string, bool) {
	latest = strings.TrimSpace(latest)
	if latest == "" {
		return "", false
	}
	if _, sent := sentMessages[latest]; sent {
		return "", false
	}
	return latest, true
}

func (r *progressReporter) sendTyping() {
	// 网络请求异步执行，避免一次 typing 超时阻塞阶段事件和文字进度调度。
	go func() {
		if err := SendTypingState(r.ctx, r.client, r.userID, r.contextToken); err != nil && r.ctx.Err() == nil {
			log.Printf("[progress] failed to refresh typing for %s: %v", r.userID, err)
		}
	}()
}

func formatProgressEvent(event agent.ProgressEvent) string {
	text := truncateRunes(strings.TrimSpace(event.Text), 420)
	switch event.Kind {
	case agent.ProgressCommentary:
		if text == "" {
			return ""
		}
		return "进度：" + text
	case agent.ProgressPlan:
		if event.Total <= 0 {
			return ""
		}
		if text == "" {
			return fmt.Sprintf("进度：已完成 %d/%d 个计划步骤", event.Completed, event.Total)
		}
		return fmt.Sprintf("进度：已完成 %d/%d 个计划步骤\n当前：%s", event.Completed, event.Total, text)
	case agent.ProgressActivity:
		if text == "" {
			return "进度：正在执行本机操作"
		}
		return "进度：" + text
	default:
		return ""
	}
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func formatElapsed(elapsed time.Duration) string {
	if elapsed < time.Minute {
		seconds := int(elapsed.Round(time.Second).Seconds())
		if seconds < 1 {
			seconds = 1
		}
		return fmt.Sprintf("%d 秒", seconds)
	}
	minutes := int(elapsed / time.Minute)
	seconds := int((elapsed % time.Minute).Round(time.Second).Seconds())
	if seconds == 60 {
		minutes++
		seconds = 0
	}
	if seconds == 0 {
		return fmt.Sprintf("%d 分钟", minutes)
	}
	return fmt.Sprintf("%d 分 %d 秒", minutes, seconds)
}
