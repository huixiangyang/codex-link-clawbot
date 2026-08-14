package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/management"
	"github.com/huixiangyang/codex-link-clawbot/internal/runtimecontrol"
	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
)

const (
	defaultServiceName = "codex-link-clawbot.service"
	systemctlPath      = "/usr/bin/systemctl"
)

var serviceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.@-]+\.service$`)

func validateServiceName(service string) error {
	if !serviceNamePattern.MatchString(service) {
		return fmt.Errorf("invalid systemd user service name")
	}
	return nil
}

func defaultManagementSocketPath() (string, error) {
	root, err := statefile.DefaultRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, management.ManagementSocketName), nil
}

func newManagementHTTPClient(socketPath string) (*http.Client, error) {
	if err := management.ValidateManagementSocket(socketPath); err != nil {
		return nil, err
	}
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			// 每次拨号前重新校验，避免路径在初检后被替换。
			if err := management.ValidateManagementSocket(socketPath); err != nil {
				return nil, err
			}
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func fetchHealth(ctx context.Context, socketPath string) (runtimecontrol.Snapshot, error) {
	client, err := newManagementHTTPClient(socketPath)
	if err != nil {
		return runtimecontrol.Snapshot{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://codex-link-clawbot.local/health", nil)
	if err != nil {
		return runtimecontrol.Snapshot{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return runtimecontrol.Snapshot{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return runtimecontrol.Snapshot{}, err
	}
	var snapshot runtimecontrol.Snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return runtimecontrol.Snapshot{}, fmt.Errorf("decode structured health: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return runtimecontrol.Snapshot{}, fmt.Errorf("decode structured health: trailing data")
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusServiceUnavailable {
		return runtimecontrol.Snapshot{}, fmt.Errorf("health endpoint returned HTTP %d", response.StatusCode)
	}
	return snapshot, nil
}

func requestAdmin(ctx context.Context, socketPath, action string) (runtimecontrol.Snapshot, error) {
	if action != "drain" && action != "resume" {
		return runtimecontrol.Snapshot{}, fmt.Errorf("invalid runtime admin action")
	}
	client, err := newManagementHTTPClient(socketPath)
	if err != nil {
		return runtimecontrol.Snapshot{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://codex-link-clawbot.local/admin/"+action, http.NoBody)
	if err != nil {
		return runtimecontrol.Snapshot{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return runtimecontrol.Snapshot{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return runtimecontrol.Snapshot{}, fmt.Errorf("admin %s returned HTTP %d", action, response.StatusCode)
	}
	var snapshot runtimecontrol.Snapshot
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return runtimecontrol.Snapshot{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return runtimecontrol.Snapshot{}, fmt.Errorf("decode admin response: trailing data")
	}
	return snapshot, nil
}

func requestDeploymentNotification(ctx context.Context, socketPath string, notice management.DeploymentNotice) (string, error) {
	client, err := newManagementHTTPClient(socketPath)
	if err != nil {
		return "", err
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(notice); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://codex-link-clawbot.local/admin/deployment-notification", &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return "", fmt.Errorf("deployment notification returned HTTP %d", response.StatusCode)
	}
	var result management.DeploymentNotificationResult
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("decode deployment notification: trailing data")
	}
	if result.Status != management.DeploymentNotificationSent && result.Status != management.DeploymentNotificationDeferred {
		return "", fmt.Errorf("deployment notification returned invalid status %q", result.Status)
	}
	return result.Status, nil
}

func waitForDrain(ctx context.Context, socketPath string, timeout time.Duration) (runtimecontrol.Snapshot, error) {
	deadline := time.Now().Add(timeout)
	for {
		snapshot, err := fetchHealth(ctx, socketPath)
		if err == nil && snapshot.Status == runtimecontrol.StateDraining && snapshot.DrainComplete {
			return snapshot, nil
		}
		if time.Now().After(deadline) {
			return runtimecontrol.Snapshot{}, fmt.Errorf("drain did not complete within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return runtimecontrol.Snapshot{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func waitForReady(ctx context.Context, socketPath, expectedVersion string, timeout time.Duration) (runtimecontrol.Snapshot, error) {
	deadline := time.Now().Add(timeout)
	for {
		snapshot, err := fetchHealth(ctx, socketPath)
		if err == nil && snapshot.Status == runtimecontrol.StateReady && snapshot.Codex.Ready && snapshot.WeChat.Monitors > 0 && snapshot.WeChat.Healthy == snapshot.WeChat.Monitors && (expectedVersion == "" || snapshot.Version == expectedVersion) {
			return snapshot, nil
		}
		if time.Now().After(deadline) {
			return runtimecontrol.Snapshot{}, fmt.Errorf("service did not become ready within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return runtimecontrol.Snapshot{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func waitForHealthyDrain(ctx context.Context, socketPath, expectedVersion string, timeout time.Duration) (runtimecontrol.Snapshot, error) {
	deadline := time.Now().Add(timeout)
	for {
		snapshot, err := fetchHealth(ctx, socketPath)
		if err == nil && snapshot.Status == runtimecontrol.StateDraining && snapshot.Draining && snapshot.Tasks.Running == 0 && snapshot.Tasks.Delivering == 0 && snapshot.Codex.Ready && snapshot.WeChat.Monitors > 0 && snapshot.WeChat.Healthy == snapshot.WeChat.Monitors && snapshot.Version == expectedVersion {
			return snapshot, nil
		}
		if time.Now().After(deadline) {
			return runtimecontrol.Snapshot{}, fmt.Errorf("draining service did not become healthy within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return runtimecontrol.Snapshot{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func runSystemctl(ctx context.Context, service string, action ...string) error {
	if err := validateServiceName(service); err != nil {
		return err
	}
	if info, err := os.Stat(systemctlPath); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("systemctl is unavailable at %s", systemctlPath)
	}
	arguments := append([]string{"--user"}, action...)
	if len(action) > 0 && action[len(action)-1] != "daemon-reload" {
		arguments = append(arguments, service)
	}
	command := exec.CommandContext(ctx, systemctlPath, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s failed: %w: %s", strings.Join(action, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func defaultBinaryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "bin", "codex-link-clawbot"), nil
}

func userUnitPath(service string) (string, error) {
	if err := validateServiceName(service); err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", service), nil
}
