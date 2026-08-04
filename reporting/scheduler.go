package reporting

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/weclaw/config"
	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/messaging"
)

type recipient struct {
	client *ilink.Client
	userID string
}

type reportSender func(context.Context, *ilink.Client, string, string) error
type reportCollector func(context.Context, config.ScheduledReportConfig, time.Time) string

type schedulerState struct {
	LastSent map[string]string `json:"last_sent"`
}

// Scheduler 每分钟检查一次每日计划，并按报告和接收者持久化去重状态。
type Scheduler struct {
	reports    []config.ScheduledReportConfig
	recipients []recipient
	statePath  string
	now        func() time.Time
	collect    reportCollector
	send       reportSender

	mu    sync.Mutex
	state schedulerState
}

func NewScheduler(reports []config.ScheduledReportConfig, clients []*ilink.Client) (*Scheduler, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("获取定时汇报状态目录: %w", err)
	}
	collector := NewCollector()
	scheduler := &Scheduler{
		reports:   append([]config.ScheduledReportConfig(nil), reports...),
		statePath: filepath.Join(home, ".weclaw", "scheduled-reports-state.json"),
		now:       time.Now,
		collect:   collector.Build,
		send: func(ctx context.Context, client *ilink.Client, userID, text string) error {
			return messaging.SendTextReply(ctx, client, userID, text, "", "")
		},
		state: schedulerState{LastSent: make(map[string]string)},
	}
	for _, client := range clients {
		if userID := strings.TrimSpace(client.OwnerUserID()); userID != "" {
			scheduler.recipients = append(scheduler.recipients, recipient{client: client, userID: userID})
		}
	}
	if err := scheduler.loadState(); err != nil {
		return nil, err
	}
	return scheduler, nil
}

func (s *Scheduler) Run(ctx context.Context) {
	if len(s.reports) == 0 {
		return
	}
	log.Printf("[reporting] scheduler started with %d report(s) and %d recipient(s)", len(s.reports), len(s.recipients))
	s.runDue(ctx, s.now())
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runDue(ctx, now)
		}
	}
}

func (s *Scheduler) runDue(ctx context.Context, now time.Time) {
	for _, report := range s.reports {
		location, _ := time.LoadLocation(report.Timezone)
		localNow := now.In(location)
		target, _ := time.ParseInLocation("15:04", report.DailyAt, location)
		targetToday := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), target.Hour(), target.Minute(), 0, 0, location)
		if localNow.Before(targetToday) {
			continue
		}
		date := localNow.Format("2006-01-02")
		var body string
		for _, recipient := range s.recipients {
			stateKey := reportStateKey(report.Name, recipient.userID)
			if s.wasSent(stateKey, date) {
				continue
			}
			if body == "" {
				body = s.collect(ctx, report, now)
			}
			if err := s.send(ctx, recipient.client, recipient.userID, body); err != nil {
				log.Printf("[reporting] send report %q to %s failed: %v", report.Name, recipient.userID, err)
				continue
			}
			if err := s.markSent(stateKey, date); err != nil {
				log.Printf("[reporting] persist report state failed: %v", err)
			}
			log.Printf("[reporting] sent report %q to %s", report.Name, recipient.userID)
		}
	}
}

// ScheduledReportStatuses 为微信管理界面提供只读快照，不触发采集或发送。
func (s *Scheduler) ScheduledReportStatuses(userID string) []messaging.ScheduledReportStatus {
	now := s.now()
	s.mu.Lock()
	lastSent := make(map[string]string, len(s.state.LastSent))
	for key, value := range s.state.LastSent {
		lastSent[key] = value
	}
	s.mu.Unlock()

	statuses := make([]messaging.ScheduledReportStatus, 0, len(s.reports))
	for _, report := range s.reports {
		location, err := time.LoadLocation(report.Timezone)
		if err != nil {
			continue
		}
		localNow := now.In(location)
		target, err := time.ParseInLocation("15:04", report.DailyAt, location)
		if err != nil {
			continue
		}
		targetToday := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), target.Hour(), target.Minute(), 0, 0, location)
		today := localNow.Format("2006-01-02")
		last := lastSent[reportStateKey(report.Name, userID)]
		state := "等待发送"
		nextRun := targetToday
		if !localNow.Before(targetToday) {
			nextRun = targetToday.AddDate(0, 0, 1)
			if last == today {
				state = "今日已发送"
			} else {
				state = "等待重试"
			}
		}
		statuses = append(statuses, messaging.ScheduledReportStatus{
			Name: report.Name, State: state,
			Schedule: "每天 " + report.DailyAt, Timezone: report.Timezone,
			NextRun: nextRun.Format("2006-01-02 15:04"), LastSent: last,
			ProjectDir: report.ProjectDir, Service: report.ServiceName, HealthURL: report.HealthURL,
		})
	}
	return statuses
}

func reportStateKey(reportName, userID string) string {
	return fmt.Sprintf("%d:%s%s", len(reportName), reportName, userID)
}

func (s *Scheduler) wasSent(key, date string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.LastSent[key] == date
}

func (s *Scheduler) markSent(key, date string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.LastSent[key] = date
	return s.saveStateLocked()
}

func (s *Scheduler) loadState() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取定时汇报状态: %w", err)
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return fmt.Errorf("解析定时汇报状态: %w", err)
	}
	if s.state.LastSent == nil {
		s.state.LastSent = make(map[string]string)
	}
	return nil
}

func (s *Scheduler) saveStateLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(s.statePath), ".scheduled-reports-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, s.statePath)
}
