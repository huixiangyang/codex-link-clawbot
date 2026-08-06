package reporting

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/internal/config"
	"github.com/huixiangyang/weclaw/internal/ilink"
)

func testAutomation() config.AutomationConfig {
	return config.AutomationConfig{
		ID: "daily", Name: "项目日报", ProjectID: "project", DailyAt: "09:00",
		Timezone: "Asia/Shanghai", NotifyOn: "always", Checks: []string{"git"}, CommitLookbackHours: 24,
	}
}

func TestSchedulerRunsDailySlotOnceAndPersistsState(t *testing.T) {
	automation := testAutomation()
	statePath := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 8, 4, 9, 5, 0, 0, time.FixedZone("CST", 8*60*60))
	sends := 0
	collects := 0
	newScheduler := func() *Scheduler {
		return &Scheduler{
			automations: []config.AutomationConfig{automation},
			projects:    map[string]config.ProjectConfig{"project": {ID: "project", Name: "Project", Root: "/srv/project"}},
			recipients:  []recipient{{client: &ilink.Client{}, userID: "owner"}}, statePath: statePath,
			collect: func(context.Context, config.AutomationConfig, config.ProjectConfig, time.Time) Result {
				collects++
				return Result{Text: "report", Fingerprint: "stable"}
			},
			send:  func(context.Context, *ilink.Client, string, string) error { sends++; return nil },
			state: schedulerState{Version: schedulerStateVersion, Runs: make(map[string]automationRunState), LastSent: make(map[string]string)},
		}
	}
	first := newScheduler()
	first.runDue(context.Background(), now)
	first.runDue(context.Background(), now.Add(time.Hour))
	if sends != 1 || collects != 1 {
		t.Fatalf("first scheduler sends=%d collects=%d", sends, collects)
	}
	second := newScheduler()
	if err := second.loadState(); err != nil {
		t.Fatal(err)
	}
	second.runDue(context.Background(), now.Add(2*time.Hour))
	if sends != 1 || collects != 1 {
		t.Fatalf("persisted state did not deduplicate: sends=%d collects=%d", sends, collects)
	}
}

func TestSchedulerNotificationPolicies(t *testing.T) {
	for _, test := range []struct {
		policy           string
		anomaly, changed bool
		want             bool
	}{
		{policy: "always", want: true},
		{policy: "anomaly", anomaly: true, want: true},
		{policy: "anomaly", changed: true, want: false},
		{policy: "change", changed: true, want: true},
		{policy: "anomaly_or_change", anomaly: true, want: true},
	} {
		if got := shouldNotify(test.policy, test.anomaly, test.changed); got != test.want {
			t.Fatalf("shouldNotify(%q) = %v", test.policy, got)
		}
	}
}

func TestAutomationStatusesAndManualRun(t *testing.T) {
	automation := testAutomation()
	now := time.Date(2026, 8, 4, 8, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	scheduler := &Scheduler{
		automations: []config.AutomationConfig{automation},
		projects:    map[string]config.ProjectConfig{"project": {ID: "project", Name: "Project", Root: "/srv/project"}},
		now:         func() time.Time { return now }, statePath: filepath.Join(t.TempDir(), "state.json"),
		collect: func(context.Context, config.AutomationConfig, config.ProjectConfig, time.Time) Result {
			return Result{Text: "manual result", Fingerprint: "one", Anomaly: true}
		},
		state: schedulerState{Version: schedulerStateVersion, Runs: make(map[string]automationRunState), LastSent: make(map[string]string)},
	}
	statuses := scheduler.AutomationStatuses("owner")
	if len(statuses) != 1 || statuses[0].State != "等待首次运行" || statuses[0].NextRun != "2026-08-04 09:00" {
		t.Fatalf("statuses = %#v", statuses)
	}
	result, err := scheduler.RunAutomation(context.Background(), "owner", "daily")
	if err != nil || result != "manual result" {
		t.Fatalf("RunAutomation() = %q, %v", result, err)
	}
	if got := scheduler.AutomationStatuses("owner")[0].State; got != "异常" {
		t.Fatalf("manual outcome = %q", got)
	}
}
