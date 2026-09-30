package request

import "errors"

var ErrSessionBusy = errors.New("本会话正在执行，本条指令未提交，也不会稍后执行")
var ErrRejected = errors.New("这条指令已经被拒绝，请重新发送新消息")

func (store *Store) FindRejection(source string) (Rejection, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	r, ok := store.state.Rejected[source]
	return r, ok
}

// Reject 在回复之前保存拒绝回执，微信重投也不能把被拒指令变成新工作。
func (store *Store) Reject(r Rejection) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.rejectLocked(r)
}

func (store *Store) rejectLocked(r Rejection) error {
	if _, ok := store.state.Rejected[r.Source]; ok {
		return nil
	}
	previous := store.state.Rejected
	next := make(map[string]Rejection)
	for source, receipt := range previous {
		if receipt.At > store.now().Add(-historyRetention).Unix() {
			next[source] = receipt
		}
	}
	r.At = store.now().Unix()
	next[r.Source] = r
	store.state.Rejected = next
	if err := store.saveLocked(); err != nil {
		store.state.Rejected = previous
		return err
	}
	return nil
}

func (store *Store) Active(owner, target, thread string) (Task, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	for _, task := range store.state.Owners[owner].Tasks {
		if !task.State.Terminal() && ((target != "" && task.TargetID == target) || (thread != "" && task.ThreadID == thread)) {
			return task, true
		}
	}
	return Task{}, false
}
