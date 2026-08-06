package runtimecontrol

import (
	"sync"
	"time"

	"github.com/huixiangyang/weclaw/internal/taskqueue"
)

type State string

const (
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateDraining State = "draining"
	StateStopping State = "stopping"
	StateDegraded State = "degraded"
)

type Drainer interface {
	SetDraining(bool)
}

type ComponentStatus struct {
	Ready bool `json:"ready"`
}

type WeChatStatus struct {
	Monitors              int   `json:"monitors"`
	Healthy               int   `json:"healthy"`
	PendingBatches        int   `json:"pending_batches"`
	OldestPendingSeconds  int64 `json:"oldest_pending_seconds"`
	LastSuccessSecondsAgo int64 `json:"last_success_seconds_ago"`
}

type TaskStatus struct {
	Running    int `json:"running"`
	Queued     int `json:"queued"`
	Delivering int `json:"delivering"`
}

type Snapshot struct {
	Status        State           `json:"status"`
	Version       string          `json:"version"`
	UptimeSeconds int64           `json:"uptime_seconds"`
	Codex         ComponentStatus `json:"codex"`
	WeChat        WeChatStatus    `json:"wechat"`
	Tasks         TaskStatus      `json:"tasks"`
	Draining      bool            `json:"draining"`
	DrainComplete bool            `json:"drain_complete"`
}

type Controller struct {
	mu                sync.Mutex
	version           string
	startedAt         time.Time
	state             State
	codexReady        bool
	draining          bool
	initialized       bool
	staging           int
	monitors          int
	healthy           int
	pendingBatches    map[*MonitorProbe]time.Time
	lastWeChatSuccess time.Time
	tasks             *taskqueue.Store
	drainer           Drainer
}

func New(version string, tasks *taskqueue.Store, drainer Drainer) *Controller {
	if version == "" {
		version = "dev"
	}
	return &Controller{
		version: version, startedAt: time.Now(), state: StateStarting,
		tasks: tasks, drainer: drainer, pendingBatches: make(map[*MonitorProbe]time.Time),
	}
}

func (controller *Controller) SetCodexReady(ready bool) {
	controller.mu.Lock()
	controller.codexReady = ready
	controller.recalculateStateLocked()
	controller.mu.Unlock()
}

func (controller *Controller) SetReady() {
	controller.mu.Lock()
	controller.initialized = true
	controller.recalculateStateLocked()
	controller.mu.Unlock()
}

func (controller *Controller) SetStopping() {
	controller.mu.Lock()
	controller.state = StateStopping
	controller.mu.Unlock()
}

func (controller *Controller) BeginIngress() {
	controller.mu.Lock()
	controller.staging++
	controller.mu.Unlock()
}

func (controller *Controller) EndIngress() {
	controller.mu.Lock()
	if controller.staging > 0 {
		controller.staging--
	}
	controller.mu.Unlock()
}

func (controller *Controller) Drain() Snapshot {
	controller.mu.Lock()
	controller.draining = true
	controller.recalculateStateLocked()
	controller.mu.Unlock()
	if controller.drainer != nil {
		controller.drainer.SetDraining(true)
	}
	return controller.Snapshot()
}

func (controller *Controller) Resume() Snapshot {
	controller.mu.Lock()
	controller.draining = false
	controller.recalculateStateLocked()
	controller.mu.Unlock()
	if controller.drainer != nil {
		controller.drainer.SetDraining(false)
	}
	return controller.Snapshot()
}

func (controller *Controller) Snapshot() Snapshot {
	controller.mu.Lock()
	state := controller.state
	version := controller.version
	startedAt := controller.startedAt
	codexReady := controller.codexReady
	draining := controller.draining
	staging := controller.staging
	monitors := controller.monitors
	healthy := controller.healthy
	pendingSync := len(controller.pendingBatches)
	pendingSince := time.Time{}
	for _, startedAt := range controller.pendingBatches {
		if pendingSince.IsZero() || startedAt.Before(pendingSince) {
			pendingSince = startedAt
		}
	}
	lastWeChatSuccess := controller.lastWeChatSuccess
	controller.mu.Unlock()
	queue := taskqueue.QueueStatus{}
	if controller.tasks != nil {
		queue = controller.tasks.QueueStatus()
	}
	uptime := int64(time.Since(startedAt).Seconds())
	if uptime < 0 {
		uptime = 0
	}
	lastSuccessSecondsAgo := int64(-1)
	if !lastWeChatSuccess.IsZero() {
		lastSuccessSecondsAgo = int64(time.Since(lastWeChatSuccess).Seconds())
		if lastSuccessSecondsAgo < 0 {
			lastSuccessSecondsAgo = 0
		}
	}
	oldestPendingSeconds := int64(-1)
	if pendingSync > 0 && !pendingSince.IsZero() {
		oldestPendingSeconds = int64(time.Since(pendingSince).Seconds())
		if oldestPendingSeconds < 0 {
			oldestPendingSeconds = 0
		}
	}
	return Snapshot{
		Status: state, Version: version, UptimeSeconds: uptime,
		Codex: ComponentStatus{Ready: codexReady},
		WeChat: WeChatStatus{
			Monitors: monitors, Healthy: healthy, PendingBatches: pendingSync,
			OldestPendingSeconds: oldestPendingSeconds, LastSuccessSecondsAgo: lastSuccessSecondsAgo,
		},
		Tasks:         TaskStatus{Running: queue.Running, Queued: queue.Queued, Delivering: queue.Delivering},
		Draining:      draining,
		DrainComplete: draining && queue.Running == 0 && queue.Delivering == 0 && staging == 0 && pendingSync == 0,
	}
}

type MonitorProbe struct {
	controller *Controller
	mu         sync.Mutex
	running    bool
	healthy    bool
	pending    bool
}

func (controller *Controller) NewMonitorProbe() *MonitorProbe {
	controller.mu.Lock()
	controller.monitors++
	controller.recalculateStateLocked()
	controller.mu.Unlock()
	return &MonitorProbe{controller: controller}
}

func (probe *MonitorProbe) SetRunning(running bool) {
	probe.mu.Lock()
	if probe.running == running {
		probe.mu.Unlock()
		return
	}
	probe.running = running
	if !running {
		probe.setHealthyLocked(false)
		probe.setPendingLocked(false)
	}
	probe.mu.Unlock()
}

func (probe *MonitorProbe) SetHealthy(healthy bool) {
	probe.mu.Lock()
	if !probe.running {
		healthy = false
	}
	probe.setHealthyLocked(healthy)
	if healthy {
		probe.controller.mu.Lock()
		probe.controller.lastWeChatSuccess = time.Now()
		probe.controller.mu.Unlock()
	}
	probe.mu.Unlock()
}

func (probe *MonitorProbe) SetBatchPending(pending bool) {
	probe.mu.Lock()
	if !probe.running {
		pending = false
	}
	probe.setPendingLocked(pending)
	probe.mu.Unlock()
}

func (probe *MonitorProbe) setHealthyLocked(healthy bool) {
	if probe.healthy == healthy {
		return
	}
	probe.healthy = healthy
	probe.controller.mu.Lock()
	if healthy {
		probe.controller.healthy++
	} else if probe.controller.healthy > 0 {
		probe.controller.healthy--
	}
	probe.controller.recalculateStateLocked()
	probe.controller.mu.Unlock()
}

func (controller *Controller) recalculateStateLocked() {
	if controller.state == StateStopping {
		return
	}
	switch {
	case controller.draining:
		controller.state = StateDraining
	case !controller.initialized:
		controller.state = StateStarting
	case !controller.codexReady || controller.monitors > 0 && controller.healthy < controller.monitors:
		controller.state = StateDegraded
	default:
		controller.state = StateReady
	}
}

func (probe *MonitorProbe) setPendingLocked(pending bool) {
	if probe.pending == pending {
		return
	}
	probe.pending = pending
	probe.controller.mu.Lock()
	if pending {
		probe.controller.pendingBatches[probe] = time.Now()
	} else {
		delete(probe.controller.pendingBatches, probe)
	}
	probe.controller.mu.Unlock()
}
