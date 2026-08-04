package messaging

import (
	"fmt"
	"strings"
	"testing"
)

type fakeScheduledReportProvider []ScheduledReportStatus

func (p fakeScheduledReportProvider) ScheduledReportStatuses(string) []ScheduledReportStatus {
	return append([]ScheduledReportStatus(nil), p...)
}

func TestScheduledReportsAppearInMainMenuAndPaginate(t *testing.T) {
	handler, _ := newSessionHandler(t)
	statuses := make(fakeScheduledReportProvider, 0, 7)
	for index := 1; index <= 7; index++ {
		statuses = append(statuses, ScheduledReportStatus{
			Name: fmt.Sprintf("项目巡检 %02d", index), State: "等待发送",
			Schedule: "每天 09:00", Timezone: "Asia/Shanghai",
			NextRun: "2026-08-05 09:00", ProjectDir: fmt.Sprintf("/srv/project-%02d", index),
			Service: "weclaw.service", HealthURL: "http://127.0.0.1:18011/health",
		})
	}
	handler.SetScheduledReportProvider(statuses)

	main := controlReply(t, handler, "owner-1", "/")
	for _, want := range []string{"5  定时巡检", "6  使用说明"} {
		if !strings.Contains(main, want) {
			t.Fatalf("main menu missing %q: %q", want, main)
		}
	}

	first := controlReply(t, handler, "owner-1", "定时巡检")
	for _, want := range []string{"页码：1 / 2", "计划：7", "项目巡检 01 · 等待发送", "下一页 · 2/2"} {
		if !strings.Contains(first, want) {
			t.Fatalf("first report page missing %q: %q", want, first)
		}
	}
	second := controlReply(t, handler, "owner-1", "下一页")
	for _, want := range []string{"页码：2 / 2", "项目巡检 07 · 等待发送", "上一页 · 1/2"} {
		if !strings.Contains(second, want) {
			t.Fatalf("second report page missing %q: %q", want, second)
		}
	}

	detail := controlReply(t, handler, "owner-1", "1")
	for _, want := range []string{
		"巡检详情：项目巡检 07", "状态：等待发送", "计划：每天 09:00", "时区：Asia/Shanghai",
		"项目：/srv/project-07", "服务：weclaw.service", "健康端点：http://127.0.0.1:18011/health",
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("report detail missing %q: %q", want, detail)
		}
	}

	back := controlReply(t, handler, "owner-1", "0")
	if !strings.Contains(back, "页码：2 / 2") {
		t.Fatalf("report detail should return to its source page: %q", back)
	}
}

func TestScheduledReportsStayHiddenWithoutConfiguration(t *testing.T) {
	handler, _ := newSessionHandler(t)
	main := controlReply(t, handler, "owner-1", "/")
	if strings.Contains(main, "定时巡检") || !strings.Contains(main, "5  使用说明") {
		t.Fatalf("empty report menu = %q", main)
	}
	result := controlReply(t, handler, "owner-1", "定时巡检")
	if !strings.Contains(result, "当前没有配置巡检计划") {
		t.Fatalf("empty report result = %q", result)
	}
}
