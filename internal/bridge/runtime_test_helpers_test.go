package bridge

import (
	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/execution"
)

func newBareHandler(runtime codex.Runtime) *Handler {
	return &Handler{
		codex: runtime, progress: execution.DefaultProgressConfig(),
		visualReplyEnabled: true, visualReplyMinRunes: 900,
	}
}
