package bridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/delivery"
)

func TestInspectWorkspaceChangeFactsNeverReturnsPaths(t *testing.T) {
	root := t.TempDir()
	runGitTestCommand(t, root, "init", "-q")
	tracked := filepath.Join(root, "private-name.txt")
	deleted := filepath.Join(root, "remove-me.txt")
	if err := os.WriteFile(tracked, []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deleted, []byte("delete\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, root, "add", "--", "private-name.txt", "remove-me.txt")
	runGitTestCommand(t, root, "-c", "user.name=codex-link-clawbot Test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
	if err := os.WriteFile(tracked, []byte("base\nchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(deleted); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret-new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	facts := inspectWorkspaceChangeFacts(context.Background(), root)
	if !facts.Available || facts.Files != 3 || facts.New != 1 || facts.Modified != 1 || facts.Deleted != 1 || !facts.HasLineStats || facts.AddedLines != 1 || facts.DeletedLines != 1 {
		t.Fatalf("facts = %#v", facts)
	}
	formatted := formatReviewChangeFact(facts)
	for _, private := range []string{"private-name", "remove-me", "secret-new", root} {
		if strings.Contains(formatted, private) {
			t.Fatalf("formatted facts leaked %q: %q", private, formatted)
		}
	}
}

func TestMobileReviewFactsExposeOnlyAggregates(t *testing.T) {
	facts := mobileReviewFacts(mobileReviewEvidence{
		Changes: workspaceChangeFacts{Available: true, Files: 4, AddedLines: 12, DeletedLines: 3, HasLineStats: true},
		Verification: codex.ThreadVerificationFacts{
			Available: true, Total: 2, Passed: 1, Failed: 1, Kinds: []codex.VerificationKind{codex.VerificationTest, codex.VerificationCheck},
		},
		Deliveries: delivery.ThreadSummary{Available: true, Total: 2, Resendable: 1, Unavailable: 1},
	})
	if len(facts) != 3 || facts[0].Value != "4 个文件 · +12 / −3" || !strings.Contains(facts[1].Value, "1 失败") || facts[2].Value != "1 项可再次发送 · 1 项已失效" {
		t.Fatalf("facts = %#v", facts)
	}
}

func runGitTestCommand(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}
