package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestServiceFailureCancelsAndJoinsSiblings(t *testing.T) {
	for _, startup := range []bool{true, false} {
		name := "running"
		if startup {
			name = "starting"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			services := newServiceGroup(ctx)
			defer services.Stop()
			stopped := make(chan struct{})
			services.Go("sibling", func(ctx context.Context) error {
				<-ctx.Done()
				close(stopped)
				return ctx.Err()
			})
			failure := errors.New("test failure")
			services.Go("failed service", func(context.Context) error { return failure })
			var err error
			if startup {
				err = services.WaitReady(make(chan struct{}))
			} else {
				err = services.Wait()
			}
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), "failed service") {
				t.Fatalf("failure = %v", err)
			}
			services.Stop()
			select {
			case <-stopped:
			default:
				t.Fatal("Stop returned before sibling exited")
			}
		})
	}
}
