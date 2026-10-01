package cli

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
)

func TestStartupFailureIsLoggedWithoutConnectingAndReleasesLease(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".codex-link-clawbot")
	mustWriteTestFile(t, filepath.Join(root, "config.json"), "{}", 0o600)
	previous := log.Writer()
	if err := runStart(nil, nil); err == nil {
		t.Fatal("legacy state must fail before login or Codex startup")
	}
	if log.Writer() != previous {
		t.Fatal("service logger was not restored")
	}
	data, err := os.ReadFile(filepath.Join(root, "logs", "service.log"))
	if err != nil || !strings.Contains(string(data), "[service] starting") || !strings.Contains(string(data), "stopped with error") || !strings.Contains(string(data), "migrate-state") {
		t.Fatalf("missing startup diagnostics: %q %v", data, err)
	}
	lease, err := statefile.Acquire(root, statefile.LeaseRuntime)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
}
