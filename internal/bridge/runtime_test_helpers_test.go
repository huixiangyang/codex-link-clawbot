package bridge

import (
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/control"
	"github.com/huixiangyang/codex-link-clawbot/internal/execution"
)

func mustDefaultIntentRegistry() *control.Registry {
	registry, err := control.DefaultRegistry()
	if err != nil {
		panic(err)
	}
	return registry
}

func newBareHandler(runtime codex.Runtime) *Handler {
	return &Handler{
		codex: runtime, intents: mustDefaultIntentRegistry(), progress: execution.DefaultProgressConfig(),
		visualReplyEnabled: true, visualReplyMinRunes: 900, bridgeVersion: "dev", startedAt: time.Now(),
	}
}
