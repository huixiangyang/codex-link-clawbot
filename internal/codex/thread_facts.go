package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type verificationCommandPattern struct {
	Kind    VerificationKind
	Pattern *regexp.Regexp
}

var verificationCommandPatterns = []verificationCommandPattern{
	{Kind: VerificationTest, Pattern: regexp.MustCompile(`(?im)(?:^|[\n;&|])\s*(?:(?:[a-z_][a-z0-9_]*=[^\s]+|timeout\s+\S+|env|sudo|command)\s+)*(?:go\s+test|pytest|python(?:3)?\s+-m\s+pytest|npm\s+(?:run\s+)?test|pnpm\s+(?:run\s+)?test|yarn\s+test|cargo\s+test|dotnet\s+test|mvn(?:w)?\s+test|gradle\s+test|\./gradlew\s+test|make\s+test)(?:\s|$)`)},
	{Kind: VerificationCheck, Pattern: regexp.MustCompile(`(?im)(?:^|[\n;&|])\s*(?:(?:[a-z_][a-z0-9_]*=[^\s]+|timeout\s+\S+|env|sudo|command)\s+)*(?:go\s+vet|golangci-lint(?:\s+run)?|staticcheck|ruff\s+check|eslint|npm\s+run\s+lint|pnpm\s+(?:run\s+)?lint|yarn\s+lint|tsc\s+--noemit|make\s+check)(?:\s|$)`)},
	{Kind: VerificationBuild, Pattern: regexp.MustCompile(`(?im)(?:^|[\n;&|])\s*(?:(?:[a-z_][a-z0-9_]*=[^\s]+|timeout\s+\S+|env|sudo|command)\s+)*(?:go\s+build|cargo\s+build|dotnet\s+build|npm\s+run\s+build|pnpm\s+(?:run\s+)?build|yarn\s+build|make\s+build)(?:\s|$)`)},
}

// ReadThreadVerificationFacts 只提取最近一次含验证命令的轮次状态。
// aggregatedOutput 和原始命令均不会进入返回值。
func (a *Codex) ReadThreadVerificationFacts(ctx context.Context, threadID string) (ThreadVerificationFacts, error) {
	if err := a.ensureCodexReady(ctx); err != nil {
		return ThreadVerificationFacts{}, err
	}
	result, err := a.rpc(ctx, "thread/read", map[string]interface{}{
		"threadId":     threadID,
		"includeTurns": true,
	})
	if err != nil {
		return ThreadVerificationFacts{}, err
	}
	var response struct {
		Thread struct {
			ID    string `json:"id"`
			Turns []struct {
				ID          string `json:"id"`
				CompletedAt *int64 `json:"completedAt"`
				Items       []struct {
					Type     string `json:"type"`
					Command  string `json:"command"`
					Status   string `json:"status"`
					ExitCode *int   `json:"exitCode"`
				} `json:"items"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return ThreadVerificationFacts{}, fmt.Errorf("parse thread verification facts: %w", err)
	}
	if response.Thread.ID == "" {
		return ThreadVerificationFacts{}, fmt.Errorf("thread/read returned empty thread id")
	}
	for index := len(response.Thread.Turns) - 1; index >= 0; index-- {
		turn := response.Thread.Turns[index]
		facts := ThreadVerificationFacts{Available: true, TurnID: turn.ID}
		if turn.CompletedAt != nil {
			facts.CompletedAt = *turn.CompletedAt
		}
		kindSet := make(map[VerificationKind]bool)
		for _, item := range turn.Items {
			if item.Type != "commandExecution" {
				continue
			}
			kinds := verificationKinds(item.Command)
			if len(kinds) == 0 {
				continue
			}
			facts.Total++
			for _, kind := range kinds {
				kindSet[kind] = true
			}
			switch {
			case item.Status == "completed" && item.ExitCode != nil && *item.ExitCode == 0:
				facts.Passed++
			case item.Status == "failed" || item.ExitCode != nil && *item.ExitCode != 0:
				facts.Failed++
			default:
				facts.Incomplete++
			}
		}
		if facts.Total == 0 {
			continue
		}
		facts.Kinds = make([]VerificationKind, 0, len(kindSet))
		for kind := range kindSet {
			facts.Kinds = append(facts.Kinds, kind)
		}
		sort.Slice(facts.Kinds, func(i, j int) bool { return facts.Kinds[i] < facts.Kinds[j] })
		return facts, nil
	}
	return ThreadVerificationFacts{Available: true}, nil
}

func verificationKinds(command string) []VerificationKind {
	command = strings.ToLower(strings.ReplaceAll(command, "\r\n", "\n"))
	seen := make(map[VerificationKind]bool)
	for _, candidate := range verificationCommandPatterns {
		if candidate.Pattern.MatchString(command) {
			seen[candidate.Kind] = true
		}
	}
	kinds := make([]VerificationKind, 0, len(seen))
	for kind := range seen {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}
