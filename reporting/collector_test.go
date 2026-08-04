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

	"github.com/fastclaw-ai/weclaw/config"
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
	report := collector.Build(context.Background(), config.ScheduledReportConfig{
		Name: "项目日报", DailyAt: "09:00", Timezone: "Asia/Shanghai",
		ProjectDir: project, ServiceName: "weclaw.service", HealthURL: health.URL,
		CommitLookbackHours: 24,
	}, time.Now())

	for _, want := range []string{"项目巡检：项目日报", "Git：main", "有 1 项未提交改动", "initial report", "weclaw.service；active", "健康端点：正常（HTTP 200，ok）"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func runGitTestCommand(t *testing.T, project string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", project}, args...)
	if output, err := exec.Command("git", commandArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
