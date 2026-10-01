package request

import (
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"strconv"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request/attachmentref"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

type clearedRow struct {
	Source string
	ClearedReceipt
}
type inputRow struct {
	RequestID        string `json:"request_id"`
	SourceMessageKey string `json:"source_message_key"`
	Text             string
	ContextToken     string `json:"context_token"`
}
type artifactRow struct {
	RequestID string `json:"request_id"`
	Role      string
	Position  int
	Attachment
}
type completionRow struct {
	RequestID string `json:"request_id"`
	Completion
}
type resultRow struct {
	RequestID    string `json:"request_id"`
	Version      int
	Reply        string
	ResponseMode presentation.ResponseMode `json:"response_mode"`
	VisualStyle  presentation.Style        `json:"visual_style"`
	FrozenAt     int64                     `json:"frozen_at"`
}
type urlRow struct {
	RequestID string `json:"request_id"`
	Position  int
	URL       string `json:"url"`
}
type receiptRow struct {
	RequestID string `json:"request_id"`
	Position  int
	DeliveryReceipt
}

func (s *Store) loadState() error {
	err := storage.View(s.stateRoot, func(tx *sql.Tx) error {
		tasks, err := storage.Rows[Task](tx, `SELECT * FROM requests ORDER BY "order"`)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			o := s.state.Owners[task.OwnerID]
			o.Tasks = append(o.Tasks, task)
			s.state.Owners[task.OwnerID] = o
		}
		var next string
		if err := tx.QueryRow("SELECT value FROM settings WHERE key='requests.next_order'").Scan(&next); err == nil {
			s.state.NextOrder, err = strconv.ParseInt(next, 10, 64)
			if err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		rejected, err := storage.Rows[Rejection](tx, "SELECT * FROM rejected_sources")
		if err != nil {
			return err
		}
		for _, row := range rejected {
			s.state.Rejected[row.Source] = row
		}
		cleared, err := storage.Rows[clearedRow](tx, "SELECT * FROM cleared_sources")
		if err != nil {
			return err
		}
		for _, row := range cleared {
			s.state.Cleared[row.Source] = row.ClearedReceipt
		}
		return validateIndex(s.state)
	})
	if err == nil {
		s.persisted = snapshotIndex(s.state)
	}
	return err
}

// 已提交快照只用于生成关系行差量；事务成功前不移动基线。
type persistedIndex struct {
	tasks     map[string]Task
	rejected  map[string]Rejection
	cleared   map[string]ClearedReceipt
	nextOrder int64
}

func snapshotIndex(state indexFile) persistedIndex {
	index := persistedIndex{tasks: make(map[string]Task), rejected: maps.Clone(state.Rejected), cleared: maps.Clone(state.Cleared), nextOrder: state.NextOrder}
	for _, owner := range state.Owners {
		for _, task := range owner.Tasks {
			index.tasks[task.ID] = task
		}
	}
	return index
}

func (s *Store) saveLocked(extra ...func(*sql.Tx) error) error {
	if err := validateIndex(s.state); err != nil {
		return err
	}
	next := snapshotIndex(s.state)
	err := storage.Update(s.stateRoot, func(tx *sql.Tx) error {
		for id := range s.persisted.tasks {
			if _, ok := next.tasks[id]; !ok {
				if _, err := tx.Exec("DELETE FROM requests WHERE id=?", id); err != nil {
					return err
				}
			}
		}
		for id, task := range next.tasks {
			if old, exists := s.persisted.tasks[id]; !exists || old != task {
				if err := storage.Put(tx, "requests", task); err != nil {
					return err
				}
			}
		}
		if next.nextOrder != s.persisted.nextOrder {
			if err := storage.Put(tx, "settings", storage.Setting{Key: "requests.next_order", Value: strconv.FormatInt(next.nextOrder, 10)}); err != nil {
				return err
			}
		}
		for source := range s.persisted.rejected {
			if _, ok := next.rejected[source]; !ok {
				if _, err := tx.Exec("DELETE FROM rejected_sources WHERE source=?", source); err != nil {
					return err
				}
			}
		}
		for source, row := range next.rejected {
			if old, ok := s.persisted.rejected[source]; !ok || old != row {
				if err := storage.Put(tx, "rejected_sources", row); err != nil {
					return err
				}
			}
		}
		for source := range s.persisted.cleared {
			if _, ok := next.cleared[source]; !ok {
				if _, err := tx.Exec("DELETE FROM cleared_sources WHERE source=?", source); err != nil {
					return err
				}
			}
		}
		for source, row := range next.cleared {
			if old, ok := s.persisted.cleared[source]; !ok || old != row {
				if err := storage.Put(tx, "cleared_sources", clearedRow{source, row}); err != nil {
					return err
				}
			}
		}
		for _, fn := range extra {
			if err := fn(tx); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		s.persisted = next
	}
	return err
}

func saveInput(tx *sql.Tx, id string, r Request) error {
	if err := validateRequest(r); err != nil {
		return err
	}
	if err := storage.Put(tx, "request_inputs", inputRow{id, r.SourceMessageKey, r.Text, r.ContextToken}); err != nil {
		return err
	}
	if err := attachmentref.Save(tx, "request", id, r.SourceData); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM artifacts WHERE request_id=? AND role<>'output'", id); err != nil {
		return err
	}
	for role, items := range map[string][]Attachment{"input_image": r.Images, "input_file": r.Files} {
		for i, a := range items {
			if err := storage.Put(tx, "artifacts", artifactRow{id, role, i, a}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) readInput(id string) (Request, error) {
	r := Request{Version: requestVersion, Images: []Attachment{}, Files: []Attachment{}}
	err := storage.View(s.stateRoot, func(tx *sql.Tx) error {
		rows, err := storage.Rows[inputRow](tx, "SELECT * FROM request_inputs WHERE request_id=?", id)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return fmt.Errorf("request input is unavailable")
		}
		row := rows[0]
		r.SourceMessageKey, r.Text, r.ContextToken = row.SourceMessageKey, row.Text, row.ContextToken
		r.SourceData, err = attachmentref.Load(tx, "request", id)
		if err != nil {
			return err
		}
		items, err := storage.Rows[artifactRow](tx, "SELECT * FROM artifacts WHERE request_id=? AND role<>'output' ORDER BY position", id)
		if err != nil {
			return err
		}
		for _, a := range items {
			if a.Role == "input_image" {
				r.Images = append(r.Images, a.Attachment)
			} else {
				r.Files = append(r.Files, a.Attachment)
			}
		}
		return validateRequest(r)
	})
	return r, err
}

func saveResult(tx *sql.Tx, id string, r Result) error {
	if err := storage.Put(tx, "results", resultRow{id, r.Version, r.Reply, r.ResponseMode, r.VisualStyle, r.FrozenAt}); err != nil {
		return err
	}
	for _, table := range []string{"result_urls", "delivery_receipts"} {
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE request_id=?", id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("DELETE FROM artifacts WHERE request_id=? AND role='output'", id); err != nil {
		return err
	}
	for i, a := range r.Artifacts {
		if err := storage.Put(tx, "artifacts", artifactRow{id, "output", i, Attachment{Name: a.Name, Path: a.Path, ContentType: "application/octet-stream", Size: a.Size, SHA256: a.SHA256}}); err != nil {
			return err
		}
	}
	for i, url := range r.ImageURLs {
		if err := storage.Put(tx, "result_urls", urlRow{id, i, url}); err != nil {
			return err
		}
	}
	return saveReceipts(tx, id, r)
}

func saveReceipts(tx *sql.Tx, id string, r Result) error {
	if err := storage.Put(tx, "delivery_receipts", receiptRow{id, -1, r.Receipt}); err != nil {
		return err
	}
	for i, receipt := range r.Attempts {
		if err := storage.Put(tx, "delivery_receipts", receiptRow{id, i, receipt}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) readResultRows(id string) (Result, error) {
	r := Result{Artifacts: []ResultArtifact{}, ImageURLs: []string{}}
	err := storage.View(s.stateRoot, func(tx *sql.Tx) error {
		rows, err := storage.Rows[resultRow](tx, "SELECT * FROM results WHERE request_id=?", id)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return sql.ErrNoRows
		}
		row := rows[0]
		r.Version, r.Reply, r.ResponseMode, r.VisualStyle, r.FrozenAt = row.Version, row.Reply, row.ResponseMode, row.VisualStyle, row.FrozenAt
		artifacts, err := storage.Rows[artifactRow](tx, "SELECT * FROM artifacts WHERE request_id=? AND role='output' ORDER BY position", id)
		if err != nil {
			return err
		}
		for _, a := range artifacts {
			r.Artifacts = append(r.Artifacts, ResultArtifact{Name: a.Name, Path: a.Path, Size: a.Size, SHA256: a.SHA256})
		}
		urls, err := storage.Rows[urlRow](tx, "SELECT * FROM result_urls WHERE request_id=? ORDER BY position", id)
		if err != nil {
			return err
		}
		for _, u := range urls {
			r.ImageURLs = append(r.ImageURLs, u.URL)
		}
		receipts, err := storage.Rows[receiptRow](tx, "SELECT * FROM delivery_receipts WHERE request_id=? ORDER BY position", id)
		if err != nil {
			return err
		}
		for _, receipt := range receipts {
			if receipt.Position == -1 {
				r.Receipt = receipt.DeliveryReceipt
			} else {
				r.Attempts = append(r.Attempts, receipt.DeliveryReceipt)
			}
		}
		return nil
	})
	return r, err
}

func (s *Store) saveResult(id string, r Result) error {
	return storage.Update(s.stateRoot, func(tx *sql.Tx) error { return saveResult(tx, id, r) })
}

func (s *Store) saveReceipts(id string, r Result) error {
	return storage.Update(s.stateRoot, func(tx *sql.Tx) error { return saveReceipts(tx, id, r) })
}

func (s *Store) readCompletion(id string) (Completion, bool, error) {
	var c Completion
	found := false
	err := storage.View(s.stateRoot, func(tx *sql.Tx) error {
		rows, err := storage.Rows[completionRow](tx, "SELECT * FROM completions WHERE request_id=?", id)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			c, found = rows[0].Completion, true
		}
		return nil
	})
	return c, found, err
}

func (s *Store) readReceipt(id string) (DeliveryReceipt, error) {
	var receipt DeliveryReceipt
	err := storage.View(s.stateRoot, func(tx *sql.Tx) error {
		rows, err := storage.Rows[receiptRow](tx, "SELECT * FROM delivery_receipts WHERE request_id=? AND position=-1", id)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return sql.ErrNoRows
		}
		receipt = rows[0].DeliveryReceipt
		return nil
	})
	return receipt, err
}
