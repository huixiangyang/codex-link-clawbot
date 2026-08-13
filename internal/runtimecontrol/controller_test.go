package runtimecontrol

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/taskqueue"
)

type testDrainer struct{ draining bool }

func (drainer *testDrainer) SetDraining(draining bool) { drainer.draining = draining }

func TestControllerDrainIncludesIngressAndCursorCommit(t *testing.T) {
	store, err := taskqueue.NewStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	drainer := &testDrainer{}
	controller := New("v2.5-test", store, drainer)
	controller.SetCodexReady(true)
	controller.SetReady()
	probe := controller.NewMonitorProbe()
	probe.SetRunning(true)
	probe.SetHealthy(true)
	controller.BeginIngress()
	probe.SetBatchPending(true)
	if snapshot := controller.Drain(); snapshot.Status != StateDraining || snapshot.DrainComplete || snapshot.WeChat.PendingBatches != 1 || snapshot.WeChat.LastSuccessSecondsAgo < 0 || !drainer.draining {
		t.Fatalf("draining snapshot = %#v drainer=%v", snapshot, drainer.draining)
	}
	controller.EndIngress()
	probe.SetBatchPending(false)
	if snapshot := controller.Snapshot(); !snapshot.DrainComplete || snapshot.WeChat.Healthy != 1 {
		t.Fatalf("completed drain snapshot = %#v", snapshot)
	}
	if snapshot := controller.Resume(); snapshot.Status != StateReady || snapshot.Draining || drainer.draining {
		t.Fatalf("resumed snapshot = %#v drainer=%v", snapshot, drainer.draining)
	}
}

func TestControllerReportsOldestPendingBatchAge(t *testing.T) {
	controller := New("v2.6.0", nil, nil)
	probe := controller.NewMonitorProbe()
	probe.SetRunning(true)
	probe.SetBatchPending(true)
	controller.mu.Lock()
	controller.pendingBatches[probe] = time.Now().Add(-45 * time.Second)
	controller.mu.Unlock()

	snapshot := controller.Snapshot()
	if snapshot.WeChat.PendingBatches != 1 || snapshot.WeChat.OldestPendingSeconds < 44 {
		t.Fatalf("pending snapshot = %#v", snapshot.WeChat)
	}
	probe.SetBatchPending(false)
	if snapshot := controller.Snapshot(); snapshot.WeChat.PendingBatches != 0 || snapshot.WeChat.OldestPendingSeconds != -1 {
		t.Fatalf("cleared pending snapshot = %#v", snapshot.WeChat)
	}
}

func TestControllerUsesOldestActivePendingProbe(t *testing.T) {
	controller := New("v2.6.0", nil, nil)
	older := controller.NewMonitorProbe()
	newer := controller.NewMonitorProbe()
	older.SetRunning(true)
	newer.SetRunning(true)
	older.SetBatchPending(true)
	newer.SetBatchPending(true)
	controller.mu.Lock()
	controller.pendingBatches[older] = time.Now().Add(-45 * time.Second)
	controller.pendingBatches[newer] = time.Now()
	controller.mu.Unlock()
	older.SetBatchPending(false)

	snapshot := controller.Snapshot()
	if snapshot.WeChat.PendingBatches != 1 || snapshot.WeChat.OldestPendingSeconds > 2 {
		t.Fatalf("active pending snapshot = %#v", snapshot.WeChat)
	}
}
