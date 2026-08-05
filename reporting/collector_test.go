package reporting

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/config"
)

func TestCollectorBuildsGitServiceAndHealthReport(t *testing.T) {
	project := t.TempDir()
	runGitTestCommand(t, project, "init", "-b", "main")
	runGitTestCommand(t, project, "config", "user.name", "Test")
	runGitTestCommand(t, project, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGitTestCommand(t, project, "add", "README.md")
	runGitTestCommand(t, project, "commit", "-m", "initial report")
	if err := os.WriteFile(filepath.Join(project, "dirty.log"), []byte("pending\n"), 0o600); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer health.Close()

	collector := NewCollector()
	collector.run = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "systemctl" {
			return "active", nil
		}
		return runCommand(ctx, name, args...)
	}
	report := collector.Build(context.Background(), config.AutomationConfig{
		ID: "daily", Name: "项目日报", DailyAt: "09:00", Timezone: "Asia/Shanghai",
		Checks: []string{"git", "service", "health"}, CommitLookbackHours: 24,
	}, config.ProjectConfig{ID: "project", Name: "Project", Root: project, ServiceName: "weclaw.service", HealthURL: health.URL}, time.Now())

	for _, want := range []string{"自动化检查：项目日报", "Git：main", "有 1 项未提交改动", "initial report", "weclaw.service；active", "健康端点：正常（HTTP 200，ok）"} {
		if !strings.Contains(report.Text, want) {
			t.Fatalf("report missing %q:\n%s", want, report.Text)
		}
	}
	if !report.Anomaly || report.Fingerprint == "" {
		t.Fatalf("result metadata = %#v", report)
	}
}

func runGitTestCommand(t *testing.T, project string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", project}, args...)
	if output, err := exec.Command("git", commandArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
