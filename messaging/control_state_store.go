package messaging

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/weclaw/statefile"
)

const (
	controlStateVersion  = 1
	controlStateMaxBytes = 256 << 10
	controlStateMaxOwner = 64
	controlStateMaxItems = 16
	controlReceiptLimit  = 512
	controlReceiptTTL    = 24 * time.Hour
)

type controlView string

const (
	viewSystemMain         controlView = "system.main"
	viewSystemRuntime      controlView = "system.runtime"
	viewSystemMore         controlView = "system.more"
	viewSystemGuide        controlView = "system.guide"
	viewTaskStatus         controlView = "task.status"
	viewTaskCancelConfirm  controlView = "task.cancel_confirm"
	viewTaskCenter         controlView = "task.center"
	viewTaskDetail         controlView = "task.detail"
	viewTaskClearConfirm   controlView = "task.clear_confirm"
	viewProjectCenter      controlView = "project.center"
	viewProjectQuickTasks  controlView = "project.quick_tasks"
	viewProjectResult      controlView = "project.result"
	viewSessionCenter      controlView = "session.center"
	viewSessionCurrent     controlView = "session.current"
	viewSessionList        controlView = "session.list"
	viewSessionDetail      controlView = "session.detail"
	viewSessionSearchInput controlView = "session.search_input"
	viewSessionNewInput    controlView = "session.new_input"
	viewSessionRenameInput controlView = "session.rename_input"
	viewSessionArchive     controlView = "session.archive_confirm"
	viewSessionResult      controlView = "session.result"
	viewPreferenceResponse controlView = "preference.response"
	viewPreferenceVisual   controlView = "preference.visual"
	viewLibraryCenter      controlView = "library.center"
	viewLibraryPage        controlView = "library.page"
	viewLibraryDetail      controlView = "library.detail"
	viewLibraryResult      controlView = "library.result"
	viewAutomationCenter   controlView = "automation.center"
	viewAutomationDetail   controlView = "automation.detail"
	viewAutomationResult   controlView = "automation.result"
)

type controlNavigation string

type controlStateStatus uint8

const (
	controlStateMissing controlStateStatus = iota
	controlStateActive
	controlStateExpired
)

const (
	navigationPrevious controlNavigation = "previous"
	navigationNext     controlNavigation = "next"
)

type persistedControlOption struct {
	Action   controlAction     `json:"action"`
	Subject  string            `json:"subject,omitempty"`
	Page     int               `json:"page,omitempty"`
	Archived bool              `json:"archived,omitempty"`
	Filter   string            `json:"filter,omitempty"`
	AutoUse  bool              `json:"auto_use,omitempty"`
	Navigate controlNavigation `json:"navigate,omitempty"`
}

type persistedControlState struct {
	Revision  string                   `json:"revision"`
	View      controlView              `json:"view"`
	Mode      controlMode              `json:"mode"`
	ExpiresAt int64                    `json:"expires_at"`
	Options   []persistedControlOption `json:"options,omitempty"`
	Back      persistedControlOption   `json:"back"`
}

type controlStateFile struct {
	Version  int                                `json:"version"`
	Owners   map[string]persistedControlState   `json:"owners"`
	Receipts map[string]persistedControlReceipt `json:"receipts"`
}

type persistedControlReceipt struct {
	OwnerHash string       `json:"owner_hash"`
	ActionID  string       `json:"action_id"`
	Domain    ActionDomain `json:"domain"`
	CreatedAt int64        `json:"created_at"`
	ExpiresAt int64        `json:"expires_at"`
}

// ControlStateStore 是数字菜单和待输入交互的唯一事实来源。
// 显示正文、标签、路径、提示词与 context token 都不会进入磁盘。
type ControlStateStore struct {
	mu    sync.RWMutex
	path  string
	state controlStateFile
	now   func() time.Time
}

func NewControlStateStore(path string) (*ControlStateStore, error) {
	if strings.TrimSpace(path) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve control state path: %w", err)
		}
		path = filepath.Join(home, ".weclaw", "control-state.json")
	}
	store := &ControlStateStore{
		path: filepath.Clean(path), now: time.Now,
		state: controlStateFile{
			Version: controlStateVersion, Owners: make(map[string]persistedControlState),
			Receipts: make(map[string]persistedControlReceipt),
		},
	}
	found, err := statefile.ReadJSON(store.path, &store.state, statefile.Options{
		MaxBytes: controlStateMaxBytes,
		Validate: store.validate,
	})
	if err != nil {
		return nil, fmt.Errorf("load control state: %w", err)
	}
	if !found {
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// ReserveReceipt 在执行有副作用动作前持久化来源键，采用保守的 at-most-once 语义。
// 返回 reserved=false 表示同一动作已接收，调用方不得再次执行控制器。
func (store *ControlStateStore) ReserveReceipt(ownerID, sourceKey, actionID string, domain ActionDomain) (bool, error) {
	ownerID = strings.TrimSpace(ownerID)
	sourceKey = strings.TrimSpace(sourceKey)
	actionID = strings.TrimSpace(actionID)
	if ownerID == "" || sourceKey == "" || !validControlReceiptIdentity(actionID, domain) || len(sourceKey) > 160 {
		return false, fmt.Errorf("invalid control receipt identity")
	}
	now := store.now()
	ownerHash := controlOwnerHash(ownerID)
	store.mu.Lock()
	defer store.mu.Unlock()
	if receipt, exists := store.state.Receipts[sourceKey]; exists && receipt.ExpiresAt > now.Unix() {
		if receipt.OwnerHash != ownerHash || receipt.ActionID != actionID || receipt.Domain != domain {
			return false, fmt.Errorf("control receipt source conflicts with another action")
		}
		return false, nil
	}
	previous := cloneControlReceipts(store.state.Receipts)
	for key, receipt := range store.state.Receipts {
		if receipt.ExpiresAt <= now.Unix() {
			delete(store.state.Receipts, key)
		}
	}
	if len(store.state.Receipts) >= controlReceiptLimit {
		store.state.Receipts = previous
		return false, fmt.Errorf("control receipt capacity is exhausted")
	}
	store.state.Receipts[sourceKey] = persistedControlReceipt{
		OwnerHash: ownerHash, ActionID: actionID, Domain: domain,
		CreatedAt: now.Unix(), ExpiresAt: now.Add(controlReceiptTTL).Unix(),
	}
	if err := store.saveLocked(); err != nil {
		store.state.Receipts = previous
		return false, err
	}
	return true, nil
}

// ConsumeAndReserve 将 revision 消费和副作用来源键写入同一原子文件事务。
func (store *ControlStateStore) ConsumeAndReserve(ownerID, revision, sourceKey, actionID string, domain ActionDomain) (consumed, duplicate bool, err error) {
	ownerID = strings.TrimSpace(ownerID)
	revision = strings.TrimSpace(revision)
	sourceKey = strings.TrimSpace(sourceKey)
	actionID = strings.TrimSpace(actionID)
	if ownerID == "" || !validControlRevision(revision) || sourceKey == "" || !validControlReceiptIdentity(actionID, domain) || len(sourceKey) > 160 {
		return false, false, fmt.Errorf("invalid consumed control receipt identity")
	}
	now := store.now()
	ownerHash := controlOwnerHash(ownerID)
	store.mu.Lock()
	defer store.mu.Unlock()
	if receipt, exists := store.state.Receipts[sourceKey]; exists && receipt.ExpiresAt > now.Unix() {
		if receipt.OwnerHash != ownerHash || receipt.ActionID != actionID || receipt.Domain != domain {
			return false, false, fmt.Errorf("control receipt source conflicts with another action")
		}
		return false, true, nil
	}
	state, exists := store.state.Owners[ownerID]
	if !exists || state.Revision != revision || state.ExpiresAt <= now.Unix() {
		return false, false, nil
	}
	previousReceipts := cloneControlReceipts(store.state.Receipts)
	for key, receipt := range store.state.Receipts {
		if receipt.ExpiresAt <= now.Unix() {
			delete(store.state.Receipts, key)
		}
	}
	if len(store.state.Receipts) >= controlReceiptLimit {
		store.state.Receipts = previousReceipts
		return false, false, fmt.Errorf("control receipt capacity is exhausted")
	}
	delete(store.state.Owners, ownerID)
	store.state.Receipts[sourceKey] = persistedControlReceipt{
		OwnerHash: ownerHash, ActionID: actionID, Domain: domain,
		CreatedAt: now.Unix(), ExpiresAt: now.Add(controlReceiptTTL).Unix(),
	}
	if err := store.saveLocked(); err != nil {
		store.state.Owners[ownerID] = state
		store.state.Receipts = previousReceipts
		return false, false, err
	}
	return true, false, nil
}

// RollbackConsumedReceipt 只用于可安全按来源键重试的持久任务副作用。
// 投递失败时恢复原 revision，让微信重投再次进入同一个数字动作。
func (store *ControlStateStore) RollbackConsumedReceipt(ownerID, sourceKey string, state controlState) error {
	ownerID = strings.TrimSpace(ownerID)
	sourceKey = strings.TrimSpace(sourceKey)
	if ownerID == "" || sourceKey == "" || !validControlRevision(state.Revision) || !validControlView(state.View) {
		return fmt.Errorf("invalid control receipt rollback")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	receipt, exists := store.state.Receipts[sourceKey]
	if !exists || receipt.OwnerHash != controlOwnerHash(ownerID) {
		return fmt.Errorf("control receipt rollback target is missing")
	}
	if receipt.ActionID != string(actionRunQuickTask) && receipt.ActionID != string(actionTaskRetry) {
		return fmt.Errorf("control receipt action cannot be rolled back")
	}
	if _, occupied := store.state.Owners[ownerID]; occupied {
		return fmt.Errorf("control state changed before receipt rollback")
	}
	previousReceipts := cloneControlReceipts(store.state.Receipts)
	delete(store.state.Receipts, sourceKey)
	store.state.Owners[ownerID] = encodeControlState(state)
	if err := store.saveLocked(); err != nil {
		delete(store.state.Owners, ownerID)
		store.state.Receipts = previousReceipts
		return err
	}
	return nil
}

func (store *ControlStateStore) FindReceipt(ownerID, sourceKey string) (persistedControlReceipt, bool) {
	ownerHash := controlOwnerHash(strings.TrimSpace(ownerID))
	sourceKey = strings.TrimSpace(sourceKey)
	store.mu.RLock()
	receipt, exists := store.state.Receipts[sourceKey]
	now := store.now().Unix()
	store.mu.RUnlock()
	if !exists || receipt.ExpiresAt <= now || receipt.OwnerHash != ownerHash {
		return persistedControlReceipt{}, false
	}
	return receipt, true
}

func (store *ControlStateStore) Put(ownerID string, state controlState) (*controlState, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, fmt.Errorf("control state owner is required")
	}
	revision, err := newControlRevision()
	if err != nil {
		return nil, fmt.Errorf("create control revision: %w", err)
	}
	state.Revision = revision
	if state.ExpiresAt.IsZero() {
		state.ExpiresAt = store.now().Add(controlStateTTL)
	}
	persisted := encodeControlState(state)

	store.mu.Lock()
	defer store.mu.Unlock()
	previous, existed := store.state.Owners[ownerID]
	store.state.Owners[ownerID] = persisted
	if err := store.saveLocked(); err != nil {
		if existed {
			store.state.Owners[ownerID] = previous
		} else {
			delete(store.state.Owners, ownerID)
		}
		return nil, err
	}
	copy := decodeControlState(persisted)
	return &copy, nil
}

func (store *ControlStateStore) Load(ownerID string) (*controlState, controlStateStatus, error) {
	ownerID = strings.TrimSpace(ownerID)
	store.mu.Lock()
	defer store.mu.Unlock()
	persisted, exists := store.state.Owners[ownerID]
	if !exists {
		return nil, controlStateMissing, nil
	}
	if !store.now().Before(time.Unix(persisted.ExpiresAt, 0)) {
		delete(store.state.Owners, ownerID)
		if err := store.saveLocked(); err != nil {
			store.state.Owners[ownerID] = persisted
			return nil, controlStateMissing, err
		}
		return nil, controlStateExpired, nil
	}
	state := decodeControlState(persisted)
	return &state, controlStateActive, nil
}

func (store *ControlStateStore) Delete(ownerID string) error {
	ownerID = strings.TrimSpace(ownerID)
	store.mu.Lock()
	defer store.mu.Unlock()
	previous, exists := store.state.Owners[ownerID]
	if !exists {
		return nil
	}
	delete(store.state.Owners, ownerID)
	if err := store.saveLocked(); err != nil {
		store.state.Owners[ownerID] = previous
		return err
	}
	return nil
}

func (store *ControlStateStore) CompareAndDelete(ownerID, revision string) (bool, error) {
	ownerID = strings.TrimSpace(ownerID)
	revision = strings.TrimSpace(revision)
	store.mu.Lock()
	defer store.mu.Unlock()
	previous, exists := store.state.Owners[ownerID]
	if !exists || previous.Revision != revision {
		return false, nil
	}
	delete(store.state.Owners, ownerID)
	if err := store.saveLocked(); err != nil {
		store.state.Owners[ownerID] = previous
		return false, err
	}
	return true, nil
}

func (store *ControlStateStore) saveLocked() error {
	return statefile.WriteJSON(store.path, store.state, statefile.Options{
		MaxBytes: controlStateMaxBytes,
		Validate: store.validate,
	})
}

func (store *ControlStateStore) validate() error {
	if store.state.Version != controlStateVersion || store.state.Owners == nil || store.state.Receipts == nil ||
		len(store.state.Owners) > controlStateMaxOwner || len(store.state.Receipts) > controlReceiptLimit {
		return fmt.Errorf("invalid control state schema")
	}
	for ownerID, state := range store.state.Owners {
		if strings.TrimSpace(ownerID) == "" || len(ownerID) > 512 || !validControlRevision(state.Revision) || !validControlView(state.View) {
			return fmt.Errorf("invalid control state owner")
		}
		if state.Mode < controlChoice || state.Mode > controlSessionSearch || state.ExpiresAt <= 0 || len(state.Options) > controlStateMaxItems {
			return fmt.Errorf("invalid control state value")
		}
		if state.Mode == controlChoice && len(state.Options) == 0 || state.Mode != controlChoice && len(state.Options) != 0 {
			return fmt.Errorf("invalid control state options")
		}
		if err := validatePersistedControlOption(state.Back); err != nil {
			return err
		}
		for _, option := range state.Options {
			if err := validatePersistedControlOption(option); err != nil {
				return err
			}
		}
	}
	for sourceKey, receipt := range store.state.Receipts {
		if strings.TrimSpace(sourceKey) == "" || len(sourceKey) > 160 || strings.ContainsAny(sourceKey, "\r\n\x00") ||
			!validControlOwnerHash(receipt.OwnerHash) || !validControlReceiptIdentity(receipt.ActionID, receipt.Domain) ||
			receipt.CreatedAt <= 0 || receipt.ExpiresAt <= receipt.CreatedAt ||
			receipt.ExpiresAt-receipt.CreatedAt > int64(controlReceiptTTL/time.Second)+1 {
			return fmt.Errorf("invalid control receipt")
		}
	}
	return nil
}

func validatePersistedControlOption(option persistedControlOption) error {
	if !option.Action.valid() || len(option.Subject) > 160 || len(option.Filter) > 120 || option.Page < 0 || option.Page > 1_000_000 {
		return fmt.Errorf("invalid control option")
	}
	if strings.ContainsAny(option.Subject, "\r\n\x00") || strings.ContainsAny(option.Filter, "\r\n\x00") {
		return fmt.Errorf("invalid control option text")
	}
	if option.Navigate != "" && option.Navigate != navigationPrevious && option.Navigate != navigationNext {
		return fmt.Errorf("invalid control navigation")
	}
	return nil
}

func encodeControlState(state controlState) persistedControlState {
	options := make([]persistedControlOption, len(state.Options))
	for index, option := range state.Options {
		options[index] = encodeControlOption(option)
	}
	return persistedControlState{
		Revision: state.Revision, View: state.View, Mode: state.Mode,
		ExpiresAt: state.ExpiresAt.Unix(), Options: options, Back: encodeControlOption(state.Back),
	}
}

func decodeControlState(state persistedControlState) controlState {
	options := make([]controlOption, len(state.Options))
	for index, option := range state.Options {
		options[index] = decodeControlOption(option)
	}
	return controlState{
		Revision: state.Revision, View: state.View, Mode: state.Mode,
		ExpiresAt: time.Unix(state.ExpiresAt, 0), Options: options, Back: decodeControlOption(state.Back),
	}
}

func encodeControlOption(option controlOption) persistedControlOption {
	navigation := option.Navigate
	if navigation == "" {
		switch {
		case strings.HasPrefix(option.Label, "上一页"):
			navigation = navigationPrevious
		case strings.HasPrefix(option.Label, "下一页"):
			navigation = navigationNext
		}
	}
	return persistedControlOption{
		Action: option.Action, Subject: option.Value, Page: option.Page, Archived: option.Archived,
		Filter: option.Query, AutoUse: option.AutoUse, Navigate: navigation,
	}
}

func decodeControlOption(option persistedControlOption) controlOption {
	return controlOption{
		Action: option.Action, Value: option.Subject, Page: option.Page, Archived: option.Archived,
		Query: option.Filter, AutoUse: option.AutoUse, Navigate: option.Navigate,
	}
}

func newControlRevision() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func validControlRevision(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validControlView(view controlView) bool {
	value := string(view)
	if value == "" || len(value) > 80 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func controlOwnerHash(ownerID string) string {
	digest := sha256.Sum256([]byte(ownerID))
	return hex.EncodeToString(digest[:16])
}

func validControlOwnerHash(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validControlReceiptIdentity(value string, domain ActionDomain) bool {
	value = strings.TrimSpace(value)
	if action := controlAction(value); action.valid() {
		return controlActionDomain(action) == domain
	}
	switch IntentID(value) {
	case IntentCancel, IntentQueuePause, IntentQueueResume, IntentQueueClear:
		return domain == DomainTask
	case IntentProjectSelect:
		return domain == DomainProject
	case IntentSessionSelect, IntentSessionRestore, IntentSessionNew, IntentSessionRename, IntentSessionArchive:
		return domain == DomainSession
	case IntentResponseVoice, IntentResponseAdaptive, IntentResponseReading, IntentVisualStyle:
		return domain == DomainPreference
	case IntentVoiceBriefing:
		return domain == DomainAutomation
	case IntentRemoteLock:
		return domain == DomainSecurity
	default:
		return false
	}
}

func cloneControlReceipts(source map[string]persistedControlReceipt) map[string]persistedControlReceipt {
	copy := make(map[string]persistedControlReceipt, len(source))
	for key, receipt := range source {
		copy[key] = receipt
	}
	return copy
}

func (action controlAction) valid() bool {
	switch action {
	case actionExit, actionMain, actionSessionMenu, actionCurrentSession, actionPickSession,
		actionBrowseSessions, actionPromptSessionSearch, actionSessionPage, actionSessionDetail,
		actionUseSession, actionPromptNewSession, actionPromptRenameSession, actionConfirmArchive,
		actionArchiveCurrent, actionConfirmArchiveItem, actionArchiveItem, actionPickArchivedSession,
		actionRestoreSession, actionTaskStatus, actionConfirmCancelTask, actionCancelTask,
		actionActivityPage, actionActivityDetail, actionTaskMoveFront, actionTaskDelete,
		actionTaskRetry, actionTaskFrozenText, actionQueuePause, actionQueueResume,
		actionConfirmQueueClear, actionQueueClear, actionRuntimeInfo, actionMore,
		actionProjectCenter, actionSelectProject, actionProjectQuickTasks, actionRunQuickTask,
		actionLibraryCenter, actionLibraryPage, actionLibraryDetail, actionResendDelivery,
		actionRemoteLock, actionVoiceBriefing, actionAutomations, actionAutomation,
		actionRunAutomation, actionVisualStyles, actionSetVisualStyle, actionResponseModes,
		actionSetResponseMode, actionGuide:
		return true
	default:
		return false
	}
}
