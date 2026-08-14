package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/management"
	"github.com/spf13/cobra"
)

const deploymentReceiptVersion = 1

var deployVersionPattern = regexp.MustCompile(`^[vV]?[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)

type deploymentReceipt struct {
	Version            int    `json:"version"`
	ID                 string `json:"id"`
	Status             string `json:"status"`
	Phase              string `json:"phase"`
	Service            string `json:"service"`
	FromVersion        string `json:"from_version"`
	ToVersion          string `json:"to_version"`
	BinarySHA256       string `json:"binary_sha256"`
	StartedAt          int64  `json:"started_at"`
	FinishedAt         int64  `json:"finished_at,omitempty"`
	NotificationStatus string `json:"notification_status,omitempty"`
	Failure            string `json:"failure,omitempty"`
}

type deployOptions struct {
	ReleaseVersion string
	Binary         string
	Expected       string
	Service        string
	Timeout        time.Duration
	TargetBinary   string
	StateRoot      string
}

type deploymentCandidate struct {
	Path    string
	Version string
	SHA256  string
	cleanup func()
}

var (
	deployBinary       string
	deployExpected     string
	deployService      string
	deployTimeout      time.Duration
	deployTargetBinary string
	deployStateRoot    string
)

func init() {
	deployCmd.Flags().StringVar(&deployBinary, "binary", "", "absolute local candidate binary")
	deployCmd.Flags().StringVar(&deployExpected, "expect-version", "", "required version for a local candidate")
	deployCmd.Flags().StringVar(&deployService, "service", defaultServiceName, "systemd user service name")
	deployCmd.Flags().DurationVar(&deployTimeout, "timeout", 10*time.Minute, "maximum drain, rollback and readiness wait")
	deployCmd.Flags().StringVar(&deployTargetBinary, "target", "", "installed binary path")
	deployCmd.Flags().StringVar(&deployStateRoot, "state-root", "", "codex-link-clawbot state root")
	rootCmd.AddCommand(deployCmd)
}

var deployCmd = &cobra.Command{
	Use:   "deploy [version]",
	Short: "Transactionally deploy an immutable codex-link-clawbot version",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target, err := resolvedDeployBinaryPath(deployTargetBinary)
		if err != nil {
			return err
		}
		stateRoot, err := resolvedStateRoot(deployStateRoot)
		if err != nil {
			return err
		}
		releaseVersion := ""
		if len(args) == 1 {
			releaseVersion = args[0]
		}
		return runDeploy(cmd.Context(), deployOptions{
			ReleaseVersion: releaseVersion, Binary: deployBinary, Expected: deployExpected,
			Service: deployService, Timeout: deployTimeout,
			TargetBinary: target, StateRoot: stateRoot,
		})
	},
}

func runDeploy(ctx context.Context, options deployOptions) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return fmt.Errorf("deployment supports only linux/amd64 and linux/arm64")
	}
	if err := validateDeployOptions(options); err != nil {
		return err
	}
	candidate, err := prepareDeploymentCandidate(ctx, options)
	if err != nil {
		return err
	}
	defer candidate.cleanup()
	controlSocket := filepath.Join(options.StateRoot, management.ManagementSocketName)

	current, err := fetchHealth(ctx, controlSocket)
	if err != nil {
		return fmt.Errorf("preflight requires the local structured health protocol: %w", err)
	}
	if current.Status != "ready" || current.Draining {
		return fmt.Errorf("service must be ready before deployment (status=%s)", current.Status)
	}
	if current.Version == candidate.Version {
		return fmt.Errorf("version %s is already running", candidate.Version)
	}
	if _, err := requestAdmin(ctx, controlSocket, "drain"); err != nil {
		return fmt.Errorf("request service drain: %w", err)
	}
	resumeNeeded := true
	defer func() {
		if resumeNeeded {
			_, _ = requestAdmin(context.Background(), controlSocket, "resume")
		}
	}()
	if _, err := waitForDrain(ctx, controlSocket, options.Timeout); err != nil {
		return err
	}

	unitPath, err := userUnitPath(options.Service)
	if err != nil {
		return err
	}
	deploymentDir, receipt, err := newDeploymentDirectory(options, current.Version, candidate)
	if err != nil {
		return err
	}
	receiptPath := filepath.Join(deploymentDir, "receipt.json")
	writeReceipt := func() error { return writePrivateJSONAtomic(receiptPath, receipt) }
	if err := writeReceipt(); err != nil {
		return err
	}
	receipt.Phase = "stopping"
	if err := writeReceipt(); err != nil {
		return err
	}
	if stopErr := runSystemctl(ctx, options.Service, "stop"); stopErr != nil {
		receipt.Status, receipt.Phase, receipt.Failure, receipt.FinishedAt = "failed", "stopping", safeFailure(stopErr), time.Now().Unix()
		_ = writeReceipt()
		return fmt.Errorf("stop old service: %w", stopErr)
	}
	resumeNeeded = false
	// 服务完全停下后再复制状态，避免微信入站在快照过程中改写队列或同步游标。
	snapshot, err := createDeploymentSnapshot(deploymentDir, options.StateRoot, options.TargetBinary, unitPath)
	if err != nil {
		_ = os.RemoveAll(filepath.Join(deploymentDir, "state"))
		restartErr := runSystemctl(context.Background(), options.Service, "start")
		var readyErr error
		if restartErr == nil {
			_, readyErr = waitForReady(context.Background(), controlSocket, current.Version, options.Timeout)
		}
		receipt.Status, receipt.Phase, receipt.Failure, receipt.FinishedAt = "failed", "snapshot", safeFailure(errors.Join(err, restartErr, readyErr)), time.Now().Unix()
		_ = writeReceipt()
		return fmt.Errorf("create deployment snapshot after drain: %w", errors.Join(err, restartErr, readyErr))
	}

	receipt.Phase = "migrating"
	_ = writeReceipt()
	if err := runOfflineMigration(ctx, candidate.Path, options.StateRoot); err != nil {
		return failAndRollback(options, current.Version, snapshot, unitPath, &receipt, receiptPath, fmt.Errorf("offline migration: %w", err))
	}
	if err := rewriteSystemdUnit(unitPath, options.TargetBinary, true); err != nil {
		return failAndRollback(options, current.Version, snapshot, unitPath, &receipt, receiptPath, fmt.Errorf("rewrite systemd unit: %w", err))
	}
	receipt.Phase = "installing"
	_ = writeReceipt()
	if err := copyRegularFile(candidate.Path, options.TargetBinary, 0o755); err != nil {
		return failAndRollback(options, current.Version, snapshot, unitPath, &receipt, receiptPath, fmt.Errorf("install candidate: %w", err))
	}
	if err := runSystemctl(ctx, options.Service, "daemon-reload"); err != nil {
		return failAndRollback(options, current.Version, snapshot, unitPath, &receipt, receiptPath, err)
	}
	if err := runSystemctl(ctx, options.Service, "start"); err != nil {
		return failAndRollback(options, current.Version, snapshot, unitPath, &receipt, receiptPath, err)
	}
	receipt.Phase = "verifying"
	_ = writeReceipt()
	if _, err := waitForHealthyDrain(ctx, controlSocket, candidate.Version, options.Timeout); err != nil {
		return failAndRollback(options, current.Version, snapshot, unitPath, &receipt, receiptPath, err)
	}
	// 当前进程继续保持排空；先把下次启动单元恢复为正常模式，再显式放行队列。
	if err := rewriteSystemdUnit(unitPath, options.TargetBinary, false); err != nil {
		return failAndRollback(options, current.Version, snapshot, unitPath, &receipt, receiptPath, err)
	}
	if err := runSystemctl(ctx, options.Service, "daemon-reload"); err != nil {
		return failAndRollback(options, current.Version, snapshot, unitPath, &receipt, receiptPath, err)
	}
	// 单元文件和 systemd 缓存都已指向正常启动模式，此处是不可逆提交点。
	// 提交后的响应若不确定，只能前向确认，不能用旧快照覆盖可能已经消费的新消息。
	if err := completeDeploymentCommit(ctx, options, candidate.Version); err != nil {
		receipt.Status, receipt.Phase, receipt.Failure, receipt.FinishedAt = "commit_uncertain", "commit", safeFailure(err), time.Now().Unix()
		_ = writeReceipt()
		return fmt.Errorf("candidate passed drain-mode health checks but deployment commit is uncertain; state was not rolled back: %w", err)
	}

	receipt.Status, receipt.Phase, receipt.FinishedAt = "succeeded", "ready", time.Now().Unix()
	notificationStatus, notifyErr := requestDeploymentNotification(ctx, controlSocket, management.DeploymentNotice{
		FromVersion: current.Version,
		ToVersion:   candidate.Version,
		Service:     options.Service,
	})
	if notifyErr != nil {
		receipt.NotificationStatus = "failed"
		fmt.Fprintf(os.Stderr, "warning: deployment notification failed: %v\n", notifyErr)
	} else {
		receipt.NotificationStatus = notificationStatus
	}
	if err := writeReceipt(); err != nil {
		return fmt.Errorf("persist successful deployment receipt: %w", err)
	}
	// 新版本已经通过健康验收，立即销毁包含正文、附件和令牌的状态副本。
	if err := removeSnapshotState(snapshot); err != nil {
		return fmt.Errorf("remove sensitive deployment snapshot: %w", err)
	}
	fmt.Printf("codex-link-clawbot %s deployed and ready. Receipt: %s\n", candidate.Version, receiptPath)
	return nil
}

func completeDeploymentCommit(ctx context.Context, options deployOptions, expectedVersion string) error {
	deadline := time.Now().Add(options.Timeout)
	controlSocket := filepath.Join(options.StateRoot, management.ManagementSocketName)
	var lastErr error
	for {
		snapshot, err := requestAdmin(ctx, controlSocket, "resume")
		if err == nil && snapshot.Status == "ready" && snapshot.Version == expectedVersion && snapshot.Codex.Ready && snapshot.WeChat.Monitors > 0 && snapshot.WeChat.Healthy == snapshot.WeChat.Monitors {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("resume returned status %s for version %s", snapshot.Status, snapshot.Version)
		}
		health, healthErr := fetchHealth(ctx, controlSocket)
		if healthErr == nil && health.Status == "ready" && health.Version == expectedVersion && health.Codex.Ready && health.WeChat.Monitors > 0 && health.WeChat.Healthy == health.WeChat.Monitors {
			return nil
		}
		if healthErr != nil {
			lastErr = errors.Join(lastErr, healthErr)
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func validateDeployOptions(options deployOptions) error {
	if err := validateServiceName(options.Service); err != nil {
		return err
	}
	if options.Timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	local := strings.TrimSpace(options.Binary) != ""
	if local {
		if options.ReleaseVersion != "" || !validDeployVersion(options.Expected) {
			return fmt.Errorf("local deployment requires --binary and --expect-version, without a release argument")
		}
		if !filepath.IsAbs(options.Binary) {
			return fmt.Errorf("local candidate path must be absolute")
		}
	} else if !validDeployVersion(options.ReleaseVersion) || options.Expected != "" {
		return fmt.Errorf("release deployment requires exactly one version argument")
	}
	if !filepath.IsAbs(options.TargetBinary) || !filepath.IsAbs(options.StateRoot) {
		return fmt.Errorf("deployment paths must be absolute")
	}
	return nil
}

func validDeployVersion(version string) bool {
	return deployVersionPattern.MatchString(strings.TrimSpace(version))
}

func prepareDeploymentCandidate(ctx context.Context, options deployOptions) (deploymentCandidate, error) {
	expected := options.ReleaseVersion
	path := options.Binary
	cleanup := func() {}
	if path == "" {
		filename := fmt.Sprintf("codex-link-clawbot_linux_%s", runtime.GOARCH)
		baseURL := fmt.Sprintf("https://github.com/%s/releases/download/%s", githubRepo, options.ReleaseVersion)
		checksumPath, err := downloadReleaseFile(baseURL+"/checksums.txt", maxChecksumBytes, false)
		if err != nil {
			return deploymentCandidate{}, fmt.Errorf("download release checksums: %w", err)
		}
		binaryPath, err := downloadReleaseFile(baseURL+"/"+filename, maxReleaseBinaryBytes, true)
		if err != nil {
			_ = os.Remove(checksumPath)
			return deploymentCandidate{}, fmt.Errorf("download release binary: %w", err)
		}
		cleanup = func() { _ = os.Remove(checksumPath); _ = os.Remove(binaryPath) }
		manifest, err := os.ReadFile(checksumPath)
		if err != nil {
			cleanup()
			return deploymentCandidate{}, err
		}
		hash, err := verifyReleaseChecksum(binaryPath, filename, manifest)
		if err != nil {
			cleanup()
			return deploymentCandidate{}, err
		}
		path = binaryPath
		metadata, err := inspectCandidateVersion(ctx, path)
		if err != nil {
			cleanup()
			return deploymentCandidate{}, err
		}
		if metadata.Version != expected {
			cleanup()
			return deploymentCandidate{}, fmt.Errorf("release binary reports version %s, expected %s", metadata.Version, expected)
		}
		return deploymentCandidate{Path: path, Version: expected, SHA256: hash, cleanup: cleanup}, nil
	}
	expected = options.Expected
	if _, err := checkedRegularFile(path); err != nil {
		return deploymentCandidate{}, err
	}
	metadata, err := inspectCandidateVersion(ctx, path)
	if err != nil {
		return deploymentCandidate{}, err
	}
	if metadata.Version != expected {
		return deploymentCandidate{}, fmt.Errorf("local binary reports version %s, expected %s", metadata.Version, expected)
	}
	hash, err := fileSHA256(path)
	if err != nil {
		return deploymentCandidate{}, err
	}
	return deploymentCandidate{Path: path, Version: expected, SHA256: hash, cleanup: cleanup}, nil
}

func inspectCandidateVersion(ctx context.Context, path string) (versionOutput, error) {
	command := exec.CommandContext(ctx, path, "version", "--json")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return versionOutput{}, fmt.Errorf("candidate version probe failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() > 4096 {
		return versionOutput{}, fmt.Errorf("candidate version output is too large")
	}
	var metadata versionOutput
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return versionOutput{}, fmt.Errorf("decode candidate version: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return versionOutput{}, fmt.Errorf("decode candidate version: trailing data")
	}
	if !validDeployVersion(metadata.Version) || metadata.GOOS != runtime.GOOS || metadata.GOARCH != runtime.GOARCH {
		return versionOutput{}, fmt.Errorf("candidate platform or version metadata is invalid")
	}
	return metadata, nil
}

func runOfflineMigration(ctx context.Context, candidate, stateRoot string) error {
	command := exec.CommandContext(ctx, candidate, "migrate-state", "--root", stateRoot)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("candidate migration failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func newDeploymentDirectory(options deployOptions, fromVersion string, candidate deploymentCandidate) (string, deploymentReceipt, error) {
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return "", deploymentReceipt{}, err
	}
	id := fmt.Sprintf("%d-%s", time.Now().Unix(), hex.EncodeToString(idBytes))
	directory := filepath.Join(options.StateRoot, "deployments", id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", deploymentReceipt{}, err
	}
	if err := os.Chmod(filepath.Dir(directory), 0o700); err != nil {
		return "", deploymentReceipt{}, err
	}
	return directory, deploymentReceipt{
		Version: deploymentReceiptVersion, ID: id, Status: "running", Phase: "snapshot",
		Service: options.Service, FromVersion: fromVersion, ToVersion: candidate.Version,
		BinarySHA256: candidate.SHA256, StartedAt: time.Now().Unix(),
	}, nil
}

func failAndRollback(options deployOptions, oldVersion string, snapshot deploymentSnapshot, unitPath string, receipt *deploymentReceipt, receiptPath string, cause error) error {
	receipt.Status, receipt.Phase, receipt.Failure = "rolling_back", "rollback", safeFailure(cause)
	_ = writePrivateJSONAtomic(receiptPath, receipt)
	rollbackCtx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	defer cancel()
	stopErr := runSystemctl(rollbackCtx, options.Service, "stop")
	restoreErr := restoreDeploymentSnapshot(snapshot, options.StateRoot, options.TargetBinary, unitPath)
	var reloadErr, startErr, readyErr error
	if restoreErr == nil {
		reloadErr = runSystemctl(rollbackCtx, options.Service, "daemon-reload")
	}
	if restoreErr == nil && reloadErr == nil {
		startErr = runSystemctl(rollbackCtx, options.Service, "start")
	}
	if restoreErr == nil && reloadErr == nil && startErr == nil {
		_, readyErr = waitForReady(rollbackCtx, filepath.Join(options.StateRoot, management.ManagementSocketName), oldVersion, options.Timeout)
	}
	rollbackErr := errors.Join(stopErr, restoreErr, reloadErr, startErr, readyErr)
	receipt.FinishedAt = time.Now().Unix()
	if rollbackErr != nil {
		receipt.Status, receipt.Phase = "rollback_failed", "rollback_failed"
		receipt.Failure = safeFailure(errors.Join(cause, rollbackErr))
		_ = writePrivateJSONAtomic(receiptPath, receipt)
		return fmt.Errorf("deployment failed and rollback did not recover the old service: %w", errors.Join(cause, rollbackErr))
	}
	receipt.Status, receipt.Phase = "rolled_back", "rolled_back"
	if cleanupErr := removeSnapshotState(snapshot); cleanupErr != nil {
		receipt.Status, receipt.Phase, receipt.Failure = "rollback_cleanup_failed", "rollback_cleanup", safeFailure(cleanupErr)
		_ = writePrivateJSONAtomic(receiptPath, receipt)
		return fmt.Errorf("deployment failed and old version %s was restored, but the sensitive snapshot could not be removed: %w", oldVersion, errors.Join(cause, cleanupErr))
	}
	_ = writePrivateJSONAtomic(receiptPath, receipt)
	return fmt.Errorf("deployment failed; old version %s was restored: %w", oldVersion, cause)
}

func rewriteSystemdUnit(path, binaryPath string, draining bool) error {
	info, err := checkedRegularFile(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	found := 0
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "ExecStart=") {
			continue
		}
		found++
		commandLine := strings.TrimSpace(strings.TrimPrefix(trimmed, "ExecStart="))
		if strings.ContainsAny(commandLine, "'\"\\") {
			return fmt.Errorf("quoted or escaped ExecStart is not supported")
		}
		fields := strings.Fields(commandLine)
		if len(fields) < 2 || fields[1] != "start" {
			return fmt.Errorf("systemd unit must run codex-link-clawbot start")
		}
		arguments := []string{binaryPath, "start"}
		for argumentIndex := 2; argumentIndex < len(fields); argumentIndex++ {
			argument := fields[argumentIndex]
			if argument == "--foreground" || argument == "--draining" {
				continue
			}
			if argument == "--api-addr" {
				if argumentIndex+1 >= len(fields) {
					return fmt.Errorf("legacy --api-addr is missing its value")
				}
				argumentIndex++
				continue
			}
			if strings.HasPrefix(argument, "--api-addr=") {
				continue
			}
			arguments = append(arguments, argument)
		}
		if draining {
			arguments = append(arguments, "--draining")
		}
		lines[index] = "ExecStart=" + strings.Join(arguments, " ")
	}
	if found != 1 {
		return fmt.Errorf("systemd unit must contain exactly one ExecStart")
	}
	return writeAtomicFile(path, []byte(strings.Join(lines, "\n")), info.Mode().Perm())
}

func safeFailure(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 1000 {
		message = message[:1000]
	}
	return message
}

func resolvedDeployBinaryPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return defaultBinaryPath()
	}
	value = filepath.Clean(value)
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("target binary path must be absolute")
	}
	return value, nil
}

func resolvedStateRoot(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, ".codex-link-clawbot")
	}
	value = filepath.Clean(value)
	if !filepath.IsAbs(value) || value == string(filepath.Separator) {
		return "", fmt.Errorf("state root must be a specific absolute path")
	}
	return value, nil
}
