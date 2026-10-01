package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDeploymentSnapshotRestoresManagedStateAndLeavesWorkspace(t *testing.T) {
	base := t.TempDir()
	stateRoot := filepath.Join(base, "state")
	deploymentDir := filepath.Join(stateRoot, "deployments", "tx")
	binaryPath := filepath.Join(base, "bin", "codex-link-clawbot")
	unitPath := filepath.Join(base, "units", "codex-link-clawbot.service")
	mustWriteTestFile(t, filepath.Join(stateRoot, "config.json"), "old-config", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "notes.txt"), "user-notes", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "codex-link-clawbot.log"), "old-log", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "accounts", "owner.sync.json"), "old-sync", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "tasks", "index.json"), "old-queue", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "workspace", "user.txt"), "user-work", 0o600)
	mustWriteTestFile(t, binaryPath, "old-binary", 0o755)
	mustWriteTestFile(t, unitPath, "old-unit", 0o644)

	snapshot, err := createDeploymentSnapshot(deploymentDir, stateRoot, binaryPath, unitPath)
	if err != nil {
		t.Fatalf("createDeploymentSnapshot() error = %v", err)
	}
	mustWriteTestFile(t, filepath.Join(stateRoot, "config.json"), "new-config", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "preferences.json"), "new-state", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "notes.txt"), "updated-user-notes", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "new-user-file.txt"), "new-user-work", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "codex-link-clawbot.log"), "new-log", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "workspace", "user.txt"), "new-user-work", 0o600)
	mustWriteTestFile(t, binaryPath, "new-binary", 0o755)
	mustWriteTestFile(t, unitPath, "new-unit", 0o644)

	if err := restoreDeploymentSnapshot(snapshot, stateRoot, binaryPath, unitPath); err != nil {
		t.Fatalf("restoreDeploymentSnapshot() error = %v", err)
	}
	assertTestFile(t, filepath.Join(stateRoot, "config.json"), "old-config")
	assertTestFile(t, filepath.Join(stateRoot, "accounts", "owner.sync.json"), "old-sync")
	assertTestFile(t, filepath.Join(stateRoot, "tasks", "index.json"), "old-queue")
	assertTestFile(t, filepath.Join(stateRoot, "codex-link-clawbot.log"), "new-log")
	assertTestFile(t, filepath.Join(stateRoot, "workspace", "user.txt"), "new-user-work")
	assertTestFile(t, filepath.Join(stateRoot, "notes.txt"), "updated-user-notes")
	assertTestFile(t, filepath.Join(stateRoot, "new-user-file.txt"), "new-user-work")
	assertTestFile(t, binaryPath, "old-binary")
	assertTestFile(t, unitPath, "old-unit")
	if _, err := os.Stat(filepath.Join(stateRoot, "preferences.json")); !os.IsNotExist(err) {
		t.Fatalf("new managed state survived rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(deploymentDir, "data", "clawbot.db")); err != nil {
		t.Fatalf("SQLite snapshot catalogue missing: %v", err)
	}
}

func TestDeploymentSnapshotRejectsManagedRootSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "data")); err != nil {
		t.Fatal(err)
	}
	if err := snapshotState(root, t.TempDir(), &snapshotManifest{}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("managed root symlink accepted: %v", err)
	}
}

func TestDeploymentSnapshotRejectsTampering(t *testing.T) {
	base := t.TempDir()
	stateRoot := filepath.Join(base, "state")
	deploymentDir := filepath.Join(stateRoot, "deployments", "tx")
	binaryPath := filepath.Join(base, "codex-link-clawbot")
	unitPath := filepath.Join(base, "codex-link-clawbot.service")
	mustWriteTestFile(t, filepath.Join(stateRoot, "config.json"), "config", 0o600)
	mustWriteTestFile(t, binaryPath, "binary", 0o755)
	mustWriteTestFile(t, unitPath, "unit", 0o644)
	snapshot, err := createDeploymentSnapshot(deploymentDir, stateRoot, binaryPath, unitPath)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteTestFile(t, filepath.Join(snapshot.StatePath, "config.json"), "tampered", 0o600)
	if err := restoreDeploymentSnapshot(snapshot, stateRoot, binaryPath, unitPath); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("restoreDeploymentSnapshot() error = %v", err)
	}
}

func TestRewriteSystemdUnitRemovesLegacyForeground(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-link-clawbot.service")
	content := "[Service]\nExecStart=/old/codex-link-clawbot start --foreground --api-addr 127.0.0.1:18011\nRestart=always\n"
	mustWriteTestFile(t, path, content, 0o644)
	if err := rewriteSystemdUnit(path, "/new/codex-link-clawbot", true); err != nil {
		t.Fatalf("rewriteSystemdUnit() error = %v", err)
	}
	assertTestFile(t, path, "[Service]\nExecStart=/new/codex-link-clawbot start --draining\nRestart=always\n")
	if err := rewriteSystemdUnit(path, "/new/codex-link-clawbot", false); err != nil {
		t.Fatalf("rewriteSystemdUnit(normal) error = %v", err)
	}
	assertTestFile(t, path, "[Service]\nExecStart=/new/codex-link-clawbot start\nRestart=always\n")
}

func TestInspectCandidateVersionRequiresExactPlatformMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '{\"version\":\"v2.5.0-test.1\",\"goos\":\"%s\",\"goarch\":\"%s\"}'\n", runtime.GOOS, runtime.GOARCH)
	mustWriteTestFile(t, path, script, 0o700)
	metadata, err := inspectCandidateVersion(context.Background(), path)
	if err != nil || metadata.Version != "v2.5.0-test.1" {
		t.Fatalf("inspectCandidateVersion() = %#v, %v", metadata, err)
	}
	mustWriteTestFile(t, path, "#!/bin/sh\nprintf '%s\\n' '{\"version\":\"v2.5.0-test.1\",\"goos\":\"other\",\"goarch\":\"other\"}'\n", 0o700)
	if _, err := inspectCandidateVersion(context.Background(), path); err == nil || !strings.Contains(err.Error(), "platform") {
		t.Fatalf("inspectCandidateVersion() platform error = %v", err)
	}
}

func TestVerifyReleaseChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), releaseBinaryName)
	content := []byte("verified release")
	mustWriteTestFile(t, path, string(content), 0o600)
	sum := sha256.Sum256(content)
	manifest := []byte(fmt.Sprintf("%x  %s\n", sum, releaseBinaryName))
	if got, err := verifyReleaseChecksum(path, manifest); err != nil || got != fmt.Sprintf("%x", sum) {
		t.Fatalf("verifyReleaseChecksum() = %q, %v", got, err)
	}
	if _, err := verifyReleaseChecksum(path, []byte(fmt.Sprintf("%x  another-file\n", sum))); err == nil {
		t.Fatal("verifyReleaseChecksum() accepted a missing release artifact")
	}
}

type releaseTransport func(*http.Request) (*http.Response, error)

func (fn releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestReleaseCandidateUsesOnlyTheFixedArtifact(t *testing.T) {
	const version = "v3.0.0-rc.1"
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '{\"version\":\"%s\",\"goos\":\"%s\",\"goarch\":\"%s\"}'\n", version, runtime.GOOS, runtime.GOARCH)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(script)))
	base := "https://github.com/huixiangyang/codex-link-clawbot/releases/download/" + version
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing=%t", missing), func(t *testing.T) {
			var urls []string
			previous := deployHTTPClient
			t.Cleanup(func() { deployHTTPClient = previous })
			deployHTTPClient = &http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
				url := r.URL.String()
				urls = append(urls, url)
				status, body := http.StatusOK, ""
				switch url {
				case base + "/checksums.txt":
					body = hash + "  codex-link-clawbot\n"
				case base + "/codex-link-clawbot":
					body = script
					if missing {
						status = http.StatusNotFound
					}
				default:
					return nil, fmt.Errorf("unexpected release URL: %s", url)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}
			root := t.TempDir()
			candidate, err := prepareDeploymentCandidate(context.Background(), deployOptions{StateRoot: root, ReleaseVersion: version})
			if missing {
				if err == nil {
					candidate.cleanup()
					t.Fatal("missing fixed artifact accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer candidate.cleanup()
				if candidate.Version != version || candidate.SHA256 != hash {
					t.Fatalf("candidate = %+v", candidate)
				}
				candidate.cleanup()
			}
			if !slices.Equal(urls, []string{base + "/checksums.txt", base + "/codex-link-clawbot"}) {
				t.Fatalf("release lookup selected or retried another artifact: %v", urls)
			}
			files, err := os.ReadDir(filepath.Join(root, "tmp", "download"))
			if err != nil || len(files) != 0 {
				t.Fatalf("candidate download cleanup: %v, %v", files, err)
			}
		})
	}
}

func TestValidateDeployOptionsSeparatesReleaseAndLocalModes(t *testing.T) {
	base := deployOptions{Service: defaultServiceName, Timeout: time.Minute, TargetBinary: "/tmp/codex-link-clawbot", StateRoot: "/tmp/state"}
	release := base
	release.ReleaseVersion = "v2.5.0-test.1"
	if err := validateDeployOptions(release); err != nil {
		t.Fatalf("release options rejected: %v", err)
	}
	local := base
	local.Binary, local.Expected = "/tmp/candidate", "v2.5.0-test.1"
	if err := validateDeployOptions(local); err != nil {
		t.Fatalf("local options rejected: %v", err)
	}
	local.ReleaseVersion = "v2.5.0-test.1"
	if err := validateDeployOptions(local); err == nil {
		t.Fatal("mixed release and local options accepted")
	}
}

func mustWriteTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
