package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

const schedulerStateVersion = 1

type recipient struct {
	client *ilink.Client
	userID string
}

type reportSender func(context.Context, *ilink.Client, string, string) error
type reportCollector func(context.Context, config.AutomationConfig, config.ProjectConfig, time.Time) Result

type automationRunState struct {
	LastSlot    string `json:"last_slot,omitempty"`
	LastRun     int64  `json:"last_run,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Outcome     string `json:"outcome,omitempty"`
}

type schedulerState struct {
	Version  int                           `json:"version"`
	Runs     map[string]automationRunState `json:"runs"`
	LastSent map[string]string             `json:"last_sent"`
}

// Scheduler 运行确定性检查；模型不参与调度、状态判定或通知策略。
type Scheduler struct {
	automations []config.AutomationConfig
	projects    map[string]config.ProjectConfig
	recipients  []recipient
	statePath   string
	now         func() time.Time
	collect     reportCollector
	send        reportSender

	mu    sync.Mutex
	state schedulerState
}

func NewScheduler(automations []config.AutomationConfig, projects []config.ProjectConfig, clients []*ilink.Client) (*Scheduler, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("获取自动化状态目录: %w", err)
	}
	collector := NewCollector()
	scheduler := &Scheduler{
		automations: append([]config.AutomationConfig(nil), automations...),
		projects:    make(map[string]config.ProjectConfig, len(projects)),
		statePath:   filepath.Join(home, ".weclaw", "automation-state.json"),
		now:         time.Now,
		collect:     collector.Build,
		send: func(ctx context.Context, client *ilink.Client, userID, text string) error {
			return messaging.SendTextReply(ctx, client, userID, text, "", "")
		},
		state: schedulerState{Version: schedulerStateVersion, Runs: make(map[string]automationRunState), LastSent: make(map[string]string)},
	}
	for _, project := range projects {
		scheduler.projects[project.ID] = project
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
	if len(s.automations) == 0 {
		return
	}
	log.Printf("[automation] scheduler started with %d automation(s) and %d recipient(s)", len(s.automations), len(s.recipients))
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
	for _, automation := range s.automations {
		slot, _, due := automationSlot(automation, now)
		if !due {
			continue
		}
		s.mu.Lock()
		previous := s.state.Runs[automation.ID]
		s.mu.Unlock()
		if previous.LastSlot == slot {
			continue
		}
		result := s.collect(ctx, automation, s.projects[automation.ProjectID], now)
		changed := previous.Fingerprint == "" || previous.Fingerprint != result.Fingerprint
		outcome := "正常"
		if result.Anomaly {
			outcome = "异常"
		} else if changed && previous.Fingerprint != "" {
			outcome = "发生变化"
		}
		if err := s.recordRun(automation.ID, automationRunState{
			LastSlot: slot, LastRun: now.Unix(), Fingerprint: result.Fingerprint, Outcome: outcome,
		}); err != nil {
			log.Printf("[automation] persist run %q failed: %v", automation.ID, err)
		}
		if !shouldNotify(automation.NotifyOn, result.Anomaly, changed) {
			continue
		}
		for _, recipient := range s.recipients {
			if err := s.send(ctx, recipient.client, recipient.userID, result.Text); err != nil {
				log.Printf("[automation] send %q to %s failed: %v", automation.ID, ilink.LogLabel(recipient.userID), err)
				continue
			}
			if err := s.markSent(automation.ID, recipient.userID, now); err != nil {
				log.Printf("[automation] persist delivery failed: %v", err)
			}
		}
	}
}

func shouldNotify(policy string, anomaly, changed bool) bool {
	switch policy {
	case "always":
		return true
	case "anomaly":
		return anomaly
	case "change":
		return changed
	case "anomaly_or_change":
		return anomaly || changed
	default:
		return false
	}
}

func automationSlot(automation config.AutomationConfig, now time.Time) (slot string, next time.Time, due bool) {
	location, _ := time.LoadLocation(automation.Timezone)
	localNow := now.In(location)
	if automation.EveryMinutes > 0 {
		interval := time.Duration(automation.EveryMinutes) * time.Minute
		start := localNow.Truncate(interval)
		return fmt.Sprintf("interval:%d", start.Unix()), start.Add(interval), true
	}
	target, _ := time.ParseInLocation("15:04", automation.DailyAt, location)
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), target.Hour(), target.Minute(), 0, 0, location)
	if localNow.Before(today) {
		return localNow.Format("2006-01-02"), today, false
	}
	return localNow.Format("2006-01-02"), today.AddDate(0, 0, 1), true
}

func (s *Scheduler) AutomationStatuses(userID string) []messaging.AutomationStatus {
	now := s.now()
	s.mu.Lock()
	runs := make(map[string]automationRunState, len(s.state.Runs))
	for id, run := range s.state.Runs {
		runs[id] = run
	}
	lastSent := make(map[string]string, len(s.state.LastSent))
	for key, value := range s.state.LastSent {
		lastSent[key] = value
	}
	s.mu.Unlock()
	statuses := make([]messaging.AutomationStatus, 0, len(s.automations))
	for _, automation := range s.automations {
		_, next, _ := automationSlot(automation, now)
		project := s.projects[automation.ProjectID]
		run := runs[automation.ID]
		schedule := "每天 " + automation.DailyAt
		if automation.EveryMinutes > 0 {
			schedule = fmt.Sprintf("每 %d 分钟", automation.EveryMinutes)
		}
		state := "等待首次运行"
		if run.Outcome != "" {
			state = run.Outcome
		}
		statuses = append(statuses, messaging.AutomationStatus{
			ID: automation.ID, Name: automation.Name, State: state, Schedule: schedule,
			Timezone: automation.Timezone, NextRun: next.Format("2006-01-02 15:04"),
			LastRun: formatUnix(run.LastRun), LastSent: lastSent[automationStateKey(automation.ID, userID)],
			ProjectID: automation.ProjectID, ProjectName: project.Name,
			Checks: append([]string(nil), automation.Checks...), NotifyOn: automation.NotifyOn,
		})
	}
	return statuses
}

func (s *Scheduler) RunAutomation(ctx context.Context, _ string, automationID string) (string, error) {
	for _, automation := range s.automations {
		if automation.ID != automationID {
			continue
		}
		now := s.now()
		s.mu.Lock()
		previous := s.state.Runs[automation.ID]
		s.mu.Unlock()
		result := s.collect(ctx, automation, s.projects[automation.ProjectID], now)
		outcome := "正常"
		if result.Anomaly {
			outcome = "异常"
		} else if previous.Fingerprint != "" && previous.Fingerprint != result.Fingerprint {
			outcome = "发生变化"
		}
		if err := s.recordRun(automation.ID, automationRunState{
			LastSlot: previous.LastSlot, LastRun: now.Unix(), Fingerprint: result.Fingerprint, Outcome: outcome,
		}); err != nil {
			return "", err
		}
		return result.Text, nil
	}
	return "", fmt.Errorf("automation %q is not configured", automationID)
}

func formatUnix(value int64) string {
	if value <= 0 {
		return ""
	}
	return time.Unix(value, 0).Local().Format("2006-01-02 15:04")
}

func automationStateKey(automationID, userID string) string {
	return fmt.Sprintf("%d:%s%s", len(automationID), automationID, userID)
}

func (s *Scheduler) recordRun(id string, run automationRunState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Runs[id] = run
	return s.saveStateLocked()
}

func (s *Scheduler) markSent(id, userID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.LastSent[automationStateKey(id, userID)] = now.Format("2006-01-02 15:04")
	return s.saveStateLocked()
}

func (s *Scheduler) loadState() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.statePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取自动化状态: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s.state); err != nil {
		return fmt.Errorf("解析自动化状态: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("解析自动化状态: trailing data")
	}
	if s.state.Version != schedulerStateVersion || s.state.Runs == nil || s.state.LastSent == nil {
		return fmt.Errorf("自动化状态 schema 无效")
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
	temp, err := os.CreateTemp(filepath.Dir(s.statePath), ".automation-state-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
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
