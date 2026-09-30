package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/huixiangyang/codex-link-clawbot/internal/request"
)

var ErrDuplicateSource = errors.New("这条消息正在接收，不会重复执行")

type Executor interface {
	CanExecute(ownerID string) bool
	Execute(context.Context, request.Task, func() bool)
}

// Coordinator 只管理正在发生的工作，不扫描历史记录，也不保存等待执行的任务。
type Coordinator struct {
	tasks    *request.Store
	executor Executor
	mu       sync.Mutex
	active   map[*Admission]bool
	draining bool
	ctx      context.Context
	cancel   context.CancelFunc
	workers  sync.WaitGroup
	effects  int
}

type Admission struct {
	coordinator                           *Coordinator
	owner, target, thread, source         string
	task                                  request.Task
	ctx                                   context.Context
	cancel                                context.CancelFunc
	cancelRequested, finalizing, launched bool
}

func NewCoordinator(tasks *request.Store, executor Executor) (*Coordinator, error) {
	if tasks == nil || executor == nil {
		return nil, fmt.Errorf("execution dependencies incomplete")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Coordinator{tasks: tasks, executor: executor, active: make(map[*Admission]bool), ctx: ctx, cancel: cancel}, nil
}

// Begin 在下载附件之前占用会话；忙碌时立即返回，不阻塞等待前一轮结束。
func (c *Coordinator) Begin(owner, target, thread, source string) (*Admission, error) {
	return c.begin(owner, target, thread, source, true)
}

// WithIdleSession 保护归档等会话变更；不占用其他会话，也不依赖微信出口。
func (c *Coordinator) WithIdleSession(owner, thread string, action func() error) error {
	admission, err := c.begin(owner, "", thread, "", false)
	if err != nil {
		return err
	}
	defer admission.Release()
	return action()
}

func (c *Coordinator) begin(owner, target, thread, source string, work bool) (*Admission, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if work && c.draining || c.ctx.Err() != nil {
		return nil, fmt.Errorf("实例正在维护，本条指令未提交，请稍后重新发送")
	}
	if work && !c.executor.CanExecute(owner) {
		return nil, fmt.Errorf("微信入口暂不可用，本条指令未提交")
	}
	for active := range c.active {
		if source != "" && active.owner == owner && active.source == source {
			return nil, ErrDuplicateSource
		}
		if (target != "" && active.owner == owner && active.target == target) || (thread != "" && active.thread == thread) {
			return nil, request.ErrSessionBusy
		}
		// 新会话建成后，以已持久化的线程 ID 识别来自其他入口的新意图。
		if active.task.ID != "" && thread != "" {
			if task, ok := c.tasks.Find(active.owner, active.task.ID); ok && task.ThreadID == thread {
				return nil, request.ErrSessionBusy
			}
		}
	}
	if _, busy := c.tasks.Active(owner, target, thread); busy {
		return nil, request.ErrSessionBusy
	}
	ctx, cancel := context.WithCancel(c.ctx)
	a := &Admission{coordinator: c, owner: owner, target: target, thread: thread, source: source, ctx: ctx, cancel: cancel}
	c.active[a] = true
	c.workers.Add(1)
	return a, nil
}

func (a *Admission) Context() context.Context { return a.ctx }

// Launch 在已保存的执行记录上立即启动，不依赖后续消息或后台轮询。
func (a *Admission) Launch(task request.Task) {
	c := a.coordinator
	c.mu.Lock()
	if !c.active[a] || a.launched {
		c.mu.Unlock()
		return
	}
	a.task, a.launched = task, true
	c.mu.Unlock()
	go func() {
		defer a.release()
		c.executor.Execute(a.ctx, task, func() bool {
			c.mu.Lock()
			defer c.mu.Unlock()
			a.finalizing = true
			return a.cancelRequested
		})
	}()
}

// Release 只释放尚未启动的准入，执行中的会话由执行器退出后释放。
func (a *Admission) Release() {
	c := a.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	if !a.launched {
		a.releaseLocked()
	}
}
func (a *Admission) release() {
	c := a.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	a.releaseLocked()
}
func (a *Admission) releaseLocked() {
	if a.coordinator.active[a] {
		delete(a.coordinator.active, a)
		a.cancel()
		a.coordinator.workers.Done()
	}
}

func (c *Coordinator) Cancel(owner, taskID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for a := range c.active {
		if a.task.ID == "" {
			if task, ok := c.tasks.Active(a.owner, a.target, a.thread); ok {
				a.task = task
			}
		}
		if a.owner == owner && a.task.ID == taskID && !a.cancelRequested && !a.finalizing {
			a.cancelRequested = true
			_ = c.tasks.UpdateStage(owner, taskID, "已请求打断，正在确认停止")
			a.cancel()
			return true
		}
	}
	return false
}

func (c *Coordinator) SetDraining(draining bool) { c.mu.Lock(); c.draining = draining; c.mu.Unlock() }

func (c *Coordinator) Run(ctx context.Context) error {
	<-ctx.Done()
	c.mu.Lock()
	c.draining = true
	c.cancel()
	c.mu.Unlock()
	c.workers.Wait()
	return nil
}

// Effect 立即启动渠道副作用，不占用会话准入、不形成工作队列；关闭时统一等待。
func (c *Coordinator) Effect(fn func(context.Context)) bool {
	c.mu.Lock()
	if c.ctx.Err() != nil || c.effects >= 32 {
		c.mu.Unlock()
		return false
	}
	c.effects++
	c.workers.Add(1)
	c.mu.Unlock()
	go func() { defer func() { c.mu.Lock(); c.effects--; c.mu.Unlock(); c.workers.Done() }(); fn(c.ctx) }()
	return true
}
func (c *Coordinator) EffectsActive() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.effects > 0 }
