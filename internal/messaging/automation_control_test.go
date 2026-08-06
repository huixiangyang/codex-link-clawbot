package messaging

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type fakeAutomationProvider []AutomationStatus

func (p fakeAutomationProvider) AutomationStatuses(string) []AutomationStatus {
	return append([]AutomationStatus(nil), p...)
}

func (p fakeAutomationProvider) RunAutomation(context.Context, string, string) (string, error) {
	return "自动化检查：手动\n状态：正常", nil
}

func TestAutomationCenterPaginatesAndRunsManually(t *testing.T) {
	handler, _ := newSessionHandler(t)
	statuses := make(fakeAutomationProvider, 0, 7)
	for index := 1; index <= 7; index++ {
		statuses = append(statuses, AutomationStatus{
			ID: fmt.Sprintf("check-%02d", index), Name: fmt.Sprintf("项目检查 %02d", index), State: "等待首次运行",
			Schedule: "每天 09:00", Timezone: "Asia/Shanghai", NextRun: "2026-08-05 09:00",
			ProjectID: "project", ProjectName: "Project", Checks: []string{"git", "health"}, NotifyOn: "anomaly_or_change",
		})
	}
	handler.SetAutomationProvider(statuses)
	first := controlReply(t, handler, "owner-1", "自动化")
	for _, want := range []string{"页码：1 / 2", "计划：7", "项目检查 01 · 等待首次运行", "下一页 · 2/2"} {
		if !strings.Contains(first, want) {
			t.Fatalf("first automation page missing %q: %q", want, first)
		}
	}
	second := controlReply(t, handler, "owner-1", "下一页")
	if !strings.Contains(second, "项目检查 07") {
		t.Fatalf("second automation page = %q", second)
	}
	detail := controlReply(t, handler, "owner-1", "1")
	for _, want := range []string{"自动化详情：项目检查 07", "检查：git、health", "通知：异常或变化", "1  立即检查"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("automation detail missing %q: %q", want, detail)
		}
	}
	manual := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(manual, "自动化检查：手动") {
		t.Fatalf("manual automation = %q", manual)
	}
}

func TestAutomationCenterEmptyState(t *testing.T) {
	handler, _ := newSessionHandler(t)
	result := controlReply(t, handler, "owner-1", "自动化")
	if !strings.Contains(result, "当前没有配置自动检查") {
		t.Fatalf("empty automation result = %q", result)
	}
}
