package execution

import (
	"context"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
)

type silentChannel struct{ Channel }

func (silentChannel) Notify(context.Context, request.Task, string, string) {}

func TestExecutorCancellationWinsDuringPreflight(t *testing.T) {
	store, err := request.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := store.Start(request.StartInput{
		SourceMessageKey: "source-preflight-cancel", OwnerID: "owner", ProjectID: "project",
		Summary: "取消测试", Text: "执行", ResponseMode: presentation.ResponseAdaptive, VisualStyle: presentation.StyleEditorial,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{deps: RunnerDependencies{Requests: store, Channel: silentChannel{}}}
	runner.failBefore(context.Background(), task, func() bool { return true }, "不应发送", request.ReasonPayloadInvalid, nil)
	finished, exists := store.Find("owner", task.ID)
	if !exists || finished.State != request.StateCancelled || finished.Reason != request.ReasonUserCancelled {
		t.Fatalf("cancelled task = %#v, exists=%v", finished, exists)
	}
}
