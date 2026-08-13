package ilink

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
)

const (
	maxConsecutiveFailures = 5
	initialBackoff         = 3 * time.Second
	maxBackoff             = 60 * time.Second
	sessionExpiredBackoff  = 5 * time.Second
	errCodeSessionExpired  = -14
)

// MessageHandler is called for each received message.
type MessageHandler func(ctx context.Context, client *Client, msg WeixinMessage) error

type MonitorObserver interface {
	SetRunning(bool)
	SetHealthy(bool)
	SetBatchPending(bool)
}

// Monitor manages the long-poll loop for receiving messages.
type Monitor struct {
	client        *Client
	handler       MessageHandler
	observer      MonitorObserver
	getUpdatesBuf string
	pendingCursor string
	consumed      map[string]bool
	bufPath       string
	failures      int
	lastActivity  time.Time
	messageHold   func() bool
}

// NewMonitor creates a new long-poll monitor.
func NewMonitor(client *Client, handler MessageHandler, observer MonitorObserver) (*Monitor, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	accountID := NormalizeAccountID(client.BotID())
	bufPath := filepath.Join(home, ".codex-link-clawbot", "accounts", accountID+".sync.json")

	m := &Monitor{
		client:       client,
		handler:      handler,
		observer:     observer,
		bufPath:      bufPath,
		consumed:     make(map[string]bool),
		lastActivity: time.Now(),
	}
	if err := m.loadBuf(); err != nil {
		return nil, err
	}
	return m, nil
}

// SetMessageHold 让部署探测进程只验证长轮询连通性，不消费消息或推进游标。
func (m *Monitor) SetMessageHold(hold func() bool) {
	m.messageHold = hold
}

// Run starts the long-poll loop. It blocks until ctx is cancelled.
// Automatically recovers from errors with exponential backoff.
func (m *Monitor) Run(ctx context.Context) error {
	log.Println("[monitor] starting long-poll loop")
	if m.observer != nil {
		m.observer.SetRunning(true)
		defer m.observer.SetRunning(false)
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("[monitor] shutting down")
			return ctx.Err()
		default:
		}

		resp, err := m.client.GetUpdates(ctx, m.getUpdatesBuf)
		if err != nil {
			if m.observer != nil {
				m.observer.SetHealthy(false)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			m.failures++
			backoff := m.calcBackoff()
			log.Printf("[monitor] GetUpdates error (%d/%d, backoff=%s): %v",
				m.failures, maxConsecutiveFailures, backoff, err)
			if m.failures == maxConsecutiveFailures {
				log.Printf("[monitor] WARNING: %d consecutive failures. If this persists, run `codex-link-clawbot login` to re-authenticate.", maxConsecutiveFailures)
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		// Session expired — reset sync buf and reconnect silently
		if resp.ErrCode == errCodeSessionExpired {
			if m.observer != nil {
				m.observer.SetHealthy(false)
			}
			if m.getUpdatesBuf != "" {
				log.Printf("[monitor] session expired, resetting sync buf")
				if err := m.saveBuf(""); err != nil {
					return fmt.Errorf("reset expired sync cursor: %w", err)
				}
				if m.observer != nil {
					m.observer.SetBatchPending(false)
				}
			} else {
				// Sync buf already empty but still getting session expired:
				// the bot token itself has expired. The user needs to re-login.
				log.Printf("[monitor] WARNING: WeChat session expired and cannot be auto-recovered. Run `codex-link-clawbot login` to re-authenticate.")
			}
			select {
			case <-time.After(sessionExpiredBackoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		// Other server errors
		if resp.Ret != 0 || resp.ErrCode != 0 {
			if m.observer != nil {
				m.observer.SetHealthy(false)
			}
			m.failures++
			backoff := m.calcBackoff()
			log.Printf("[monitor] server error: ret=%d errcode=%d errmsg=%s backoff=%s", resp.Ret, resp.ErrCode, resp.ErrMsg, backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		// 只有业务层成功响应才刷新健康状态和最近成功轮询时间。
		m.lastActivity = time.Now()
		if m.observer != nil {
			m.observer.SetHealthy(true)
			m.observer.SetBatchPending(len(resp.Msgs) > 0 || m.pendingCursor != "")
		}

		if m.messageHold != nil && m.messageHold() {
			// 保持服务端游标不变；部署提交或正常重启后，同一批消息仍会重新投递。
			m.failures = 0
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		// 同步处理整批消息；任何可重试错误都必须保留旧游标，让微信重新投递。
		if err := m.processBatch(ctx, resp.Msgs, resp.GetUpdatesBuf); err != nil {
			m.failures++
			backoff := m.calcBackoff()
			log.Printf("[monitor] message batch not committed (backoff=%s): %v", backoff, err)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		if m.observer != nil {
			m.observer.SetBatchPending(false)
		}
		m.failures = 0
	}
}

func (m *Monitor) processBatch(ctx context.Context, messages []WeixinMessage, nextCursor string) error {
	if m.pendingCursor != "" && m.pendingCursor != nextCursor {
		return fmt.Errorf("pending sync batch cursor changed")
	}
	if m.pendingCursor == "" && nextCursor != "" {
		m.pendingCursor = nextCursor
	}
	for _, msg := range messages {
		messageKey := monitorMessageKey(msg)
		if messageKey != "" && m.consumed[messageKey] {
			continue
		}
		if err := m.handler(ctx, m.client, msg); err != nil {
			return err
		}
		if messageKey != "" && m.pendingCursor != "" {
			m.consumed[messageKey] = true
			if err := m.saveState(syncData{
				Version: syncVersion, GetUpdatesBuf: m.getUpdatesBuf,
				PendingCursor: m.pendingCursor, Consumed: sortedConsumed(m.consumed),
			}); err != nil {
				return fmt.Errorf("persist partial sync receipt: %w", err)
			}
		}
	}
	if m.pendingCursor != "" {
		if err := m.saveBuf(nextCursor); err != nil {
			return fmt.Errorf("persist committed sync cursor: %w", err)
		}
	}
	return nil
}

func monitorMessageKey(msg WeixinMessage) string {
	if msg.MessageID != 0 {
		return fmt.Sprintf("message:%d", msg.MessageID)
	}
	if msg.Seq != 0 {
		return fmt.Sprintf("seq:%d", msg.Seq)
	}
	return ""
}

// calcBackoff returns an exponential backoff duration capped at maxBackoff.
func (m *Monitor) calcBackoff() time.Duration {
	d := initialBackoff
	for i := 1; i < m.failures; i++ {
		d *= 2
		if d > maxBackoff {
			return maxBackoff
		}
	}
	return d
}

type syncData struct {
	Version       int      `json:"version"`
	GetUpdatesBuf string   `json:"get_updates_buf"`
	PendingCursor string   `json:"pending_cursor,omitempty"`
	Consumed      []string `json:"consumed,omitempty"`
}

const syncVersion = 1

func (m *Monitor) loadBuf() error {
	var s syncData
	found, err := statefile.ReadJSON(m.bufPath, &s, statefile.Options{
		Validate: func() error { return validateSyncData(s) },
	})
	if err != nil {
		return fmt.Errorf("load sync cursor: %w", err)
	}
	if !found {
		return nil
	}
	for _, key := range s.Consumed {
		if key == "" || m.consumed[key] {
			return fmt.Errorf("invalid sync cursor receipt")
		}
		m.consumed[key] = true
	}
	m.pendingCursor = s.PendingCursor
	if s.GetUpdatesBuf != "" {
		m.getUpdatesBuf = s.GetUpdatesBuf
		log.Printf("[monitor] loaded sync buf from %s", m.bufPath)
	}
	return nil
}

func (m *Monitor) saveBuf(next string) error {
	return m.saveState(syncData{Version: syncVersion, GetUpdatesBuf: next})
}

func (m *Monitor) saveState(state syncData) error {
	if err := statefile.WriteJSON(m.bufPath, state, statefile.Options{
		Validate: func() error { return validateSyncData(state) },
	}); err != nil {
		return err
	}
	m.getUpdatesBuf = state.GetUpdatesBuf
	m.pendingCursor = state.PendingCursor
	m.consumed = make(map[string]bool, len(state.Consumed))
	for _, key := range state.Consumed {
		m.consumed[key] = true
	}
	return nil
}

func validateSyncData(state syncData) error {
	if state.Version != syncVersion || state.PendingCursor == "" && len(state.Consumed) > 0 || len(state.Consumed) > 512 {
		return fmt.Errorf("invalid sync cursor schema")
	}
	seen := make(map[string]bool, len(state.Consumed))
	for _, key := range state.Consumed {
		if key == "" || seen[key] {
			return fmt.Errorf("invalid sync cursor receipt")
		}
		seen[key] = true
	}
	return nil
}

func sortedConsumed(consumed map[string]bool) []string {
	keys := make([]string, 0, len(consumed))
	for key := range consumed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// FormatMessageSummary returns a short description of a message for logging.
func FormatMessageSummary(msg WeixinMessage) string {
	text := ""
	for _, item := range msg.ItemList {
		if item.Type == ItemTypeText && item.TextItem != nil {
			text = item.TextItem.Text
			break
		}
	}
	if len(text) > 50 {
		text = text[:50] + "..."
	}
	return fmt.Sprintf("from=%s type=%d state=%d text=%q", msg.FromUserID, msg.MessageType, msg.MessageState, text)
}
