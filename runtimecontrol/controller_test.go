package runtimecontrol

import (
	"path/filepath"
	"testing"

	"github.com/huixiangyang/weclaw/taskqueue"
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
	if snapshot := controller.Drain(); snapshot.Status != StateDraining || snapshot.DrainComplete || !drainer.draining {
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
