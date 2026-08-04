package reporting

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/config"
	"github.com/huixiangyang/weclaw/ilink"
)

func TestSchedulerSendsOnceAndPersistsDailyState(t *testing.T) {
	report := config.ScheduledReportConfig{
		Name: "项目日报", DailyAt: "09:00", Timezone: "Asia/Shanghai",
		ProjectDir: "/srv/project", ServiceName: "weclaw.service",
		HealthURL: "http://127.0.0.1:18011/health", CommitLookbackHours: 24,
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 8, 4, 9, 5, 0, 0, time.FixedZone("CST", 8*60*60))
	sends := 0
	collects := 0
	newTestScheduler := func() *Scheduler {
		return &Scheduler{
			reports:    []config.ScheduledReportConfig{report},
			recipients: []recipient{{client: &ilink.Client{}, userID: "owner"}},
			statePath:  statePath,
			collect: func(context.Context, config.ScheduledReportConfig, time.Time) string {
				collects++
				return "report"
			},
			send: func(context.Context, *ilink.Client, string, string) error {
				sends++
				return nil
			},
			state: schedulerState{LastSent: make(map[string]string)},
		}
	}

	first := newTestScheduler()
	first.runDue(context.Background(), now)
	first.runDue(context.Background(), now.Add(time.Hour))
	if sends != 1 || collects != 1 {
		t.Fatalf("first scheduler sends=%d collects=%d", sends, collects)
	}

	second := newTestScheduler()
	if err := second.loadState(); err != nil {
		t.Fatalf("loadState() error: %v", err)
	}
	second.runDue(context.Background(), now.Add(2*time.Hour))
	if sends != 1 || collects != 1 {
		t.Fatalf("persisted state did not deduplicate: sends=%d collects=%d", sends, collects)
	}
}

func TestSchedulerDoesNotSendBeforeDailyTime(t *testing.T) {
	sends := 0
	scheduler := &Scheduler{
		reports:    []config.ScheduledReportConfig{{Name: "日报", DailyAt: "09:00", Timezone: "Asia/Shanghai"}},
		recipients: []recipient{{client: &ilink.Client{}, userID: "owner"}},
		statePath:  filepath.Join(t.TempDir(), "state.json"),
		collect:    func(context.Context, config.ScheduledReportConfig, time.Time) string { return "report" },
		send: func(context.Context, *ilink.Client, string, string) error {
			sends++
			return nil
		},
		state: schedulerState{LastSent: make(map[string]string)},
	}
	now := time.Date(2026, 8, 4, 8, 59, 0, 0, time.FixedZone("CST", 8*60*60))
	scheduler.runDue(context.Background(), now)
	if sends != 0 {
		t.Fatalf("sends before schedule = %d", sends)
	}
}
