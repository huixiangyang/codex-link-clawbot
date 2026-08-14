package appserver

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
)

func TestReadThreadVerificationFactsUsesLatestVerificationTurnWithoutReturningCommands(t *testing.T) {
	agent := newCodexSessionTestAgent(func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "thread/read" {
			t.Fatalf("method = %q", method)
		}
		got := params.(map[string]interface{})
		if got["threadId"] != "thread-facts" || got["includeTurns"] != true {
			t.Fatalf("params = %#v", got)
		}
		return json.RawMessage(`{"thread":{"id":"thread-facts","turns":[
			{"id":"turn-old","status":"completed","completedAt":10,"items":[{"id":"cmd-1","type":"commandExecution","command":"go test ./... && go vet ./...","status":"completed","exitCode":0}]},
			{"id":"turn-latest-verification","status":"failed","completedAt":20,"items":[{"id":"cmd-2","type":"commandExecution","command":"pytest -q","status":"failed","exitCode":1},{"id":"cmd-3","type":"commandExecution","command":"echo go test ./...","status":"completed","exitCode":0}]},
			{"id":"turn-chat","status":"completed","completedAt":30,"items":[{"id":"message","type":"agentMessage","text":"done"}]}
		]}}`), nil
	})

	facts, err := agent.ReadThreadVerificationFacts(context.Background(), "thread-facts")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Available || facts.TurnID != "turn-latest-verification" || facts.CompletedAt != 20 || facts.Total != 1 || facts.Passed != 0 || facts.Failed != 1 || facts.Incomplete != 0 {
		t.Fatalf("facts = %#v", facts)
	}
	if !reflect.DeepEqual(facts.Kinds, []codex.VerificationKind{codex.VerificationTest}) {
		t.Fatalf("kinds = %#v", facts.Kinds)
	}
}

func TestVerificationKindsClassifiesChecksAndBuildsConservatively(t *testing.T) {
	kinds := verificationKinds("set -e\nmake check && go build ./cmd/codex-link-clawbot")
	if !reflect.DeepEqual(kinds, []codex.VerificationKind{codex.VerificationBuild, codex.VerificationCheck}) {
		t.Fatalf("kinds = %#v", kinds)
	}
	if got := verificationKinds("printf 'please run go test ./...'\nrg 'pytest'"); len(got) != 0 {
		t.Fatalf("non-execution text was classified: %#v", got)
	}
}
