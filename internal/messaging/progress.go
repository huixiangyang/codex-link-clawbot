package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
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

type progressReporter struct {
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	client       *ilink.Client
	userID       string
	contextToken string
	config       ProgressConfig
	setStatus    func(string)
	events       chan codex.ProgressEvent
	closeOnce    sync.Once
}

func newProgressReporter(ctx context.Context, client *ilink.Client, userID, contextToken string, config ProgressConfig, setStatus func(string)) *progressReporter {
	reporterCtx, cancel := context.WithCancel(ctx)
	r := &progressReporter{
		ctx:          reporterCtx,
		cancel:       cancel,
		done:         make(chan struct{}),
		client:       client,
		userID:       userID,
		contextToken: contextToken,
		config:       config,
		setStatus:    setStatus,
		events:       make(chan codex.ProgressEvent, 32),
	}
	go r.run()
	return r
}

func (r *progressReporter) Report(event codex.ProgressEvent) {
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
	latest := "codex-link-clawbot 请求已接收，正在分析"
	latestKind := codex.ProgressKind("")
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
				if event.Kind == codex.ProgressActivity && latestKind != "" && latestKind != codex.ProgressActivity {
					continue
				}
				latest = status
				latestKind = event.Kind
				if r.setStatus != nil {
					r.setStatus(status)
				}
			}
		case <-messageTimer.C:
			if message, ok := unsentProgress(latest, sentMessages); ok {
				if err := SendTextReply(r.ctx, r.client, r.userID, message, r.contextToken, NewClientID()); err != nil {
					log.Printf("[progress] failed to send progress to %s: %v", ilink.LogLabel(r.userID), err)
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
			log.Printf("[progress] failed to refresh typing for %s: %v", ilink.LogLabel(r.userID), err)
		}
	}()
}

func formatProgressEvent(event codex.ProgressEvent) string {
	text := truncateRunes(strings.TrimSpace(event.Text), 420)
	switch event.Kind {
	case codex.ProgressCommentary:
		if text == "" {
			return ""
		}
		return "进度：" + text
	case codex.ProgressPlan:
		if event.Total <= 0 {
			return ""
		}
		if text == "" {
			return fmt.Sprintf("进度：已完成 %d/%d 个计划步骤", event.Completed, event.Total)
		}
		return fmt.Sprintf("进度：已完成 %d/%d 个计划步骤\n当前：%s", event.Completed, event.Total, text)
	case codex.ProgressActivity:
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
	if limit <= 0 || len(runes) <= limit {
		return text
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
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
