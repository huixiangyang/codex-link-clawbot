package request

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

type Summary struct {
	Task            Task
	Receipt         DeliveryReceipt
	ResultAvailable bool
}

// ListSummaries 在同一状态快照中批量读取当前回执，不加载回答正文或附件。
func (store *Store) ListSummaries(ownerID string) ([]Summary, error) {
	ownerID = strings.TrimSpace(ownerID)
	store.mu.RLock()
	defer store.mu.RUnlock()
	tasks := append([]Task(nil), store.state.Owners[ownerID].Tasks...)
	sortTasksForDisplay(tasks)
	items := make([]Summary, 0, len(tasks))
	if len(tasks) == 0 {
		return items, nil
	}
	now := store.now().Unix()
	receipts := make(map[string]DeliveryReceipt)
	err := storage.View(store.stateRoot, func(tx *sql.Tx) error {
		type row struct {
			receiptRow
			FrozenAt int64 `json:"frozen_at"`
		}
		rows, err := storage.Rows[row](tx, `SELECT d.*,s.frozen_at FROM delivery_receipts d
		JOIN results s ON s.request_id=d.request_id JOIN requests r ON r.id=d.request_id
		WHERE r.owner_id=? AND r.execution_completed_at>0 AND r.result_expires_at>? AND d.position=-1`, ownerID, now)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := validateReceipt(row.DeliveryReceipt, row.FrozenAt); err != nil {
				return fmt.Errorf("invalid receipt for %s: %w", row.RequestID, err)
			}
			receipts[row.RequestID] = row.DeliveryReceipt
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, task := range tasks {
		receipt, found := receipts[task.ID]
		items = append(items, Summary{Task: task, Receipt: receipt, ResultAvailable: found && task.ExecutionCompletedAt > 0 && task.ResultExpiresAt > now})
	}
	return items, nil
}
