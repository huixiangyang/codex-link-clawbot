package appserver

import (
	"encoding/json"
	"testing"
)

func TestUsageAndRateLimitNotificationsUpdateSnapshots(t *testing.T) {
	client := New(Config{})
	client.handleThreadTokenUsageUpdated(json.RawMessage(`{
  "threadId":"thread-1",
  "turnId":"turn-1",
  "tokenUsage":{"last":{"inputTokens":120,"cachedInputTokens":20,"outputTokens":30,"reasoningOutputTokens":5,"totalTokens":150},"total":{"inputTokens":120,"cachedInputTokens":20,"outputTokens":30,"reasoningOutputTokens":5,"totalTokens":150},"modelContextWindow":200000}
}`))
	usage, ok := client.Usage("thread-1")
	if !ok || usage.Last.InputTokens != 120 || usage.Last.TotalTokens != 150 {
		t.Fatalf("usage = %#v, %v", usage, ok)
	}
	client.handleRateLimitsUpdated(json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":37,"resetsAt":1785900000},"secondary":{"usedPercent":12,"resetsAt":1786000000}}}`))
	limits, ok := client.RateLimits()
	if !ok || limits.Primary == nil || limits.Primary.UsedPercent != 37 || limits.Secondary == nil {
		t.Fatalf("limits = %#v, %v", limits, ok)
	}
}
