package reporting

import (
	"context"
	"crypto/sha256"
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

type Result struct {
	Text        string
	Fingerprint string
	Anomaly     bool
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

// Build 仅执行配置声明的确定性检查，并生成可用于变化检测的稳定指纹。
func (c *Collector) Build(ctx context.Context, automation config.AutomationConfig, project config.ProjectConfig, now time.Time) Result {
	location, _ := time.LoadLocation(automation.Timezone)
	localNow := now.In(location)
	lines := []string{
		"自动化检查：" + automation.Name,
		"时间：" + localNow.Format("2006-01-02 15:04 MST"),
		"项目：" + project.Name,
		"",
	}
	var stable []string
	anomaly := false
	for _, check := range automation.Checks {
		var section []string
		var failed bool
		switch check {
		case "git":
			section, failed = c.gitSection(ctx, project.Root, automation.CommitLookbackHours, localNow)
		case "service":
			section, failed = c.serviceSection(ctx, project.ServiceName)
		case "health":
			section, failed = c.healthSection(ctx, project.HealthURL)
		}
		if len(section) == 0 {
			continue
		}
		if len(stable) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, section...)
		stable = append(stable, section...)
		anomaly = anomaly || failed
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(stable, "\n"))))
	return Result{Text: strings.Join(lines, "\n"), Fingerprint: fingerprint, Anomaly: anomaly}
}

func (c *Collector) gitSection(ctx context.Context, projectRoot string, lookbackHours int, now time.Time) ([]string, bool) {
	gitArgs := func(args ...string) []string {
		return append([]string{"-C", projectRoot}, args...)
	}
	branch, branchErr := c.run(ctx, "git", gitArgs("branch", "--show-current")...)
	if branchErr != nil {
		return []string{"Git：检查失败（" + compactError(branch, branchErr) + "）"}, true
	}
	if branch == "" {
		branch, _ = c.run(ctx, "git", gitArgs("rev-parse", "--short", "HEAD")...)
		branch = "detached@" + branch
	}

	status, statusErr := c.run(ctx, "git", gitArgs("status", "--porcelain")...)
	worktree := "干净"
	anomaly := false
	if statusErr != nil {
		worktree = "检查失败"
		anomaly = true
	} else if status != "" {
		worktree = fmt.Sprintf("有 %d 项未提交改动", countNonEmptyLines(status))
		anomaly = true
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

	since := now.Add(-time.Duration(lookbackHours) * time.Hour).Format(time.RFC3339)
	commits, commitsErr := c.run(ctx, "git", gitArgs("log", "--since="+since, "--max-count=8", "--pretty=format:%h %s")...)
	lines := []string{
		fmt.Sprintf("Git：%s；工作区%s；%s", branch, worktree, upstream),
		fmt.Sprintf("最近 %d 小时提交：", lookbackHours),
	}
	if commitsErr != nil {
		return append(lines, "- 检查失败（"+compactError(commits, commitsErr)+"）"), true
	}
	if strings.TrimSpace(commits) == "" {
		return append(lines, "- 无新提交"), anomaly
	}
	for _, commit := range strings.Split(commits, "\n") {
		if commit = strings.TrimSpace(commit); commit != "" {
			lines = append(lines, "- "+commit)
		}
	}
	return lines, anomaly
}

func (c *Collector) serviceSection(ctx context.Context, serviceName string) ([]string, bool) {
	serviceState, serviceErr := c.run(ctx, "systemctl", "--user", "is-active", serviceName)
	if serviceState == "" {
		serviceState = "unknown"
	}
	serviceLine := fmt.Sprintf("服务：%s；%s", serviceName, serviceState)
	if serviceErr != nil {
		serviceLine += "（异常）"
	}

	return []string{serviceLine}, serviceErr != nil || serviceState != "active"
}

func (c *Collector) healthSection(ctx context.Context, healthURL string) ([]string, bool) {
	healthLine := "健康端点：异常"
	anomaly := true
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err == nil {
		resp, requestErr := c.httpClient.Do(req)
		if requestErr == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			resp.Body.Close()
			detail := strings.TrimSpace(string(body))
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				healthLine = fmt.Sprintf("健康端点：正常（HTTP %d", resp.StatusCode)
				anomaly = false
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
	return []string{healthLine}, anomaly
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
