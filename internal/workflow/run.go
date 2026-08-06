package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

type RunStatus struct {
	ProjectID    string
	WorkflowID   string
	WorkflowName string
	Slot         Slot
	Position     int
	Total        int
}

type Submission struct {
	Duplicate bool
	Completed bool
	Prompt    string
	Next      RunStatus
	rollback  *SubmissionRollback
}

// SubmissionRollback 只在最终提示持久入队失败时交还给 Store；字段不对展示层开放。
type SubmissionRollback struct {
	ownerID    string
	receiptKey string
	run        pendingRun
}

func (store *Store) StartRun(ownerID, projectID, workflowID string) (RunStatus, error) {
	ownerID, projectID, workflowID = strings.TrimSpace(ownerID), strings.TrimSpace(projectID), strings.TrimSpace(workflowID)
	if err := store.validateOwnerProject(ownerID, projectID); err != nil {
		return RunStatus{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	definition, exists := findDefinitionState(store.state, ownerID, projectID, workflowID)
	if !exists {
		return RunStatus{}, fmt.Errorf("workflow not found")
	}
	if len(definition.Slots) == 0 {
		return RunStatus{}, fmt.Errorf("workflow does not require parameters")
	}
	now := store.now()
	next := cloneState(store.state)
	next.Runs[ownerID] = pendingRun{
		ProjectID: projectID, WorkflowID: workflowID, Values: make(map[string]string),
		StartedAt: normalizedUnix(now), ExpiresAt: now.Add(runTTL).Unix(),
	}
	if err := store.saveLocked(next); err != nil {
		return RunStatus{}, err
	}
	store.state = next
	return runStatus(definition, 0), nil
}

func (store *Store) PendingRun(ownerID string) (RunStatus, bool, error) {
	ownerID = strings.TrimSpace(ownerID)
	store.mu.Lock()
	defer store.mu.Unlock()
	run, exists := store.state.Runs[ownerID]
	if !exists {
		return RunStatus{}, false, nil
	}
	if run.ExpiresAt <= store.now().Unix() {
		next := cloneState(store.state)
		delete(next.Runs, ownerID)
		if err := store.saveLocked(next); err != nil {
			return RunStatus{}, false, err
		}
		store.state = next
		return RunStatus{}, false, nil
	}
	definition, exists := findDefinitionState(store.state, ownerID, run.ProjectID, run.WorkflowID)
	if !exists || len(run.Values) >= len(definition.Slots) {
		return RunStatus{}, false, fmt.Errorf("pending workflow is inconsistent")
	}
	return runStatus(definition, len(run.Values)), true, nil
}

func (store *Store) SubmitRunValue(ownerID, sourceKey, value string) (Submission, error) {
	ownerID, sourceKey, value = strings.TrimSpace(ownerID), strings.TrimSpace(sourceKey), strings.TrimSpace(value)
	if ownerID == "" || sourceKey == "" || len(sourceKey) > 160 || strings.ContainsAny(sourceKey, "\r\n\x00") {
		return Submission{}, fmt.Errorf("workflow run source is invalid")
	}
	if !validRunValue(value) {
		return Submission{}, fmt.Errorf("workflow parameter value is invalid")
	}
	receiptKey := runReceiptKey(ownerID, sourceKey)
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now()
	if receipt, exists := store.state.Receipts[receiptKey]; exists && receipt.ExpiresAt > now.Unix() {
		return Submission{Duplicate: true}, nil
	}
	run, exists := store.state.Runs[ownerID]
	if !exists || run.ExpiresAt <= now.Unix() {
		return Submission{}, fmt.Errorf("pending workflow run is unavailable")
	}
	definition, exists := findDefinitionState(store.state, ownerID, run.ProjectID, run.WorkflowID)
	if !exists || len(run.Values) >= len(definition.Slots) {
		return Submission{}, fmt.Errorf("pending workflow definition is unavailable")
	}
	previous := run
	previous.Values = cloneValues(run.Values)
	next := cloneState(store.state)
	for key, receipt := range next.Receipts {
		if receipt.ExpiresAt <= now.Unix() {
			delete(next.Receipts, key)
		}
	}
	if len(next.Receipts) >= maxRunReceipts {
		return Submission{}, fmt.Errorf("workflow run receipt capacity is exhausted")
	}
	run = next.Runs[ownerID]
	slot := definition.Slots[len(run.Values)]
	run.Values[slot.Key] = value
	next.Receipts[receiptKey] = runReceipt{CreatedAt: normalizedUnix(now), ExpiresAt: now.Add(runReceiptTTL).Unix()}
	if len(run.Values) < len(definition.Slots) {
		next.Runs[ownerID] = run
		if err := store.saveLocked(next); err != nil {
			return Submission{}, err
		}
		store.state = next
		return Submission{Next: runStatus(definition, len(run.Values))}, nil
	}
	prompt, err := Render(definition, run.Values)
	if err != nil {
		return Submission{}, err
	}
	delete(next.Runs, ownerID)
	if err := store.saveLocked(next); err != nil {
		return Submission{}, err
	}
	store.state = next
	return Submission{
		Completed: true, Prompt: prompt,
		rollback: &SubmissionRollback{ownerID: ownerID, receiptKey: receiptKey, run: previous},
	}, nil
}

func (store *Store) RollbackSubmission(rollback *SubmissionRollback) error {
	if rollback == nil || rollback.ownerID == "" || rollback.receiptKey == "" {
		return fmt.Errorf("workflow submission rollback is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.state.Receipts[rollback.receiptKey]; !exists {
		return fmt.Errorf("workflow submission receipt is missing")
	}
	if _, exists := store.state.Runs[rollback.ownerID]; exists {
		return fmt.Errorf("workflow run changed before rollback")
	}
	next := cloneState(store.state)
	delete(next.Receipts, rollback.receiptKey)
	run := rollback.run
	run.Values = cloneValues(run.Values)
	next.Runs[rollback.ownerID] = run
	if err := store.saveLocked(next); err != nil {
		return err
	}
	store.state = next
	return nil
}

func (store *Store) CancelRun(ownerID string) error {
	ownerID = strings.TrimSpace(ownerID)
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.state.Runs[ownerID]; !exists {
		return nil
	}
	next := cloneState(store.state)
	delete(next.Runs, ownerID)
	if err := store.saveLocked(next); err != nil {
		return err
	}
	store.state = next
	return nil
}

func (store *Store) HasRunReceipt(ownerID, sourceKey string) bool {
	key := runReceiptKey(strings.TrimSpace(ownerID), strings.TrimSpace(sourceKey))
	store.mu.RLock()
	receipt, exists := store.state.Receipts[key]
	now := store.now().Unix()
	store.mu.RUnlock()
	return exists && receipt.ExpiresAt > now
}

func runStatus(definition Definition, position int) RunStatus {
	return RunStatus{
		ProjectID: definition.ProjectID, WorkflowID: definition.ID, WorkflowName: definition.Name,
		Slot: definition.Slots[position], Position: position + 1, Total: len(definition.Slots),
	}
}

func runReceiptKey(ownerID, sourceKey string) string {
	digest := sha256.Sum256([]byte(ownerID + "\x00" + sourceKey))
	return hex.EncodeToString(digest[:])
}

func cloneValues(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (submission Submission) Rollback() *SubmissionRollback {
	return submission.rollback
}
