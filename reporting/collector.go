package reporting

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/huixiangyang/weclaw/config"
)

type commandRunner func(context.Context, string, ...string) (string, error)

// Collector 从真实 Git、systemd 和 HTTP 端点采集状态，不经过语言模型推断。
type Collector struct {
	run        commandRunner
	httpClient *http.Client
}

func NewCollector() *Collector {
	return &Collector{
		run: runCommand,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

// Build 生成适合微信纯文本展示的每日项目巡检。
func (c *Collector) Build(ctx context.Context, report config.ScheduledReportConfig, now time.Time) string {
	location, _ := time.LoadLocation(report.Timezone)
	localNow := now.In(location)
	lines := []string{
		"项目巡检：" + report.Name,
		"时间：" + localNow.Format("2006-01-02 15:04 MST"),
		"项目：" + report.ProjectDir,
		"",
	}

	lines = append(lines, c.gitSection(ctx, report, localNow)...)
	lines = append(lines, "")
	lines = append(lines, c.serviceSection(ctx, report)...)
	return strings.Join(lines, "\n")
}

func (c *Collector) gitSection(ctx context.Context, report config.ScheduledReportConfig, now time.Time) []string {
	gitArgs := func(args ...string) []string {
		return append([]string{"-C", report.ProjectDir}, args...)
	}
	branch, branchErr := c.run(ctx, "git", gitArgs("branch", "--show-current")...)
	if branchErr != nil {
		return []string{"Git：检查失败（" + compactError(branch, branchErr) + "）"}
	}
	if branch == "" {
		branch, _ = c.run(ctx, "git", gitArgs("rev-parse", "--short", "HEAD")...)
		branch = "detached@" + branch
	}

	status, statusErr := c.run(ctx, "git", gitArgs("status", "--porcelain")...)
	worktree := "干净"
	if statusErr != nil {
		worktree = "检查失败"
	} else if status != "" {
		worktree = fmt.Sprintf("有 %d 项未提交改动", countNonEmptyLines(status))
	}

	upstream := "未配置上游"
	if counts, err := c.run(ctx, "git", gitArgs("rev-list", "--left-right", "--count", "@{upstream}...HEAD")...); err == nil {
		fields := strings.Fields(counts)
		if len(fields) == 2 {
			behind, behindErr := strconv.Atoi(fields[0])
			ahead, aheadErr := strconv.Atoi(fields[1])
			if behindErr == nil && aheadErr == nil {
				upstream = fmt.Sprintf("领先 %d，落后 %d", ahead, behind)
			}
		}
	}

	since := now.Add(-time.Duration(report.CommitLookbackHours) * time.Hour).Format(time.RFC3339)
	commits, commitsErr := c.run(ctx, "git", gitArgs("log", "--since="+since, "--max-count=8", "--pretty=format:%h %s")...)
	lines := []string{
		fmt.Sprintf("Git：%s；工作区%s；%s", branch, worktree, upstream),
		fmt.Sprintf("最近 %d 小时提交：", report.CommitLookbackHours),
	}
	if commitsErr != nil {
		return append(lines, "- 检查失败（"+compactError(commits, commitsErr)+"）")
	}
	if strings.TrimSpace(commits) == "" {
		return append(lines, "- 无新提交")
	}
	for _, commit := range strings.Split(commits, "\n") {
		if commit = strings.TrimSpace(commit); commit != "" {
			lines = append(lines, "- "+commit)
		}
	}
	return lines
}

func (c *Collector) serviceSection(ctx context.Context, report config.ScheduledReportConfig) []string {
	serviceState, serviceErr := c.run(ctx, "systemctl", "--user", "is-active", report.ServiceName)
	if serviceState == "" {
		serviceState = "unknown"
	}
	serviceLine := fmt.Sprintf("服务：%s；%s", report.ServiceName, serviceState)
	if serviceErr != nil {
		serviceLine += "（异常）"
	}

	healthLine := "健康端点：异常"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, report.HealthURL, nil)
	if err == nil {
		resp, requestErr := c.httpClient.Do(req)
		if requestErr == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			resp.Body.Close()
			detail := strings.TrimSpace(string(body))
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				healthLine = fmt.Sprintf("健康端点：正常（HTTP %d", resp.StatusCode)
			} else {
				healthLine = fmt.Sprintf("健康端点：异常（HTTP %d", resp.StatusCode)
			}
			if detail != "" {
				healthLine += "，" + oneLine(detail)
			}
			healthLine += "）"
		} else {
			healthLine = "健康端点：异常（" + compactError("", requestErr) + "）"
		}
	}
	return []string{serviceLine, healthLine}
}

func countNonEmptyLines(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func compactError(output string, err error) string {
	if detail := oneLine(strings.TrimSpace(output)); detail != "" {
		return detail
	}
	if err == nil {
		return "未知错误"
	}
	return oneLine(err.Error())
}

func oneLine(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 120 {
		text = text[:120]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		return text + "..."
	}
	return text
}
