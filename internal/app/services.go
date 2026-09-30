package app

import (
	"context"
	"fmt"
	"sync"
)

type serviceExit struct {
	name string
	err  error
}

// serviceGroup 将后台循环绑定到一次 Run 调用；任何启动或运行失败都会在返回前统一回收。
type serviceGroup struct {
	ctx     context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup
	exits   chan serviceExit
}

func newServiceGroup(parent context.Context) *serviceGroup {
	ctx, cancel := context.WithCancel(parent)
	return &serviceGroup{ctx: ctx, cancel: cancel, exits: make(chan serviceExit, 1)}
}

func (g *serviceGroup) Go(name string, run func(context.Context) error) {
	g.workers.Add(1)
	go func() {
		defer g.workers.Done()
		exit := serviceExit{name: name, err: run(g.ctx)}
		// 只保留第一个退出原因，回收阶段的取消不会覆盖原始故障。
		select {
		case g.exits <- exit:
		default:
		}
	}()
}

func (g *serviceGroup) WaitReady(ready <-chan struct{}) error {
	select {
	case <-ready:
		return nil
	case exit := <-g.exits:
		return exit.failure()
	case <-g.ctx.Done():
		return g.ctx.Err()
	}
}

func (g *serviceGroup) Wait() error {
	select {
	case exit := <-g.exits:
		if g.ctx.Err() != nil {
			return nil
		}
		return exit.failure()
	case <-g.ctx.Done():
		return nil
	}
}

func (g *serviceGroup) Stop() {
	g.cancel()
	g.workers.Wait()
}

func (exit serviceExit) failure() error {
	if exit.err != nil {
		return fmt.Errorf("%s stopped: %w", exit.name, exit.err)
	}
	return fmt.Errorf("%s stopped unexpectedly", exit.name)
}
