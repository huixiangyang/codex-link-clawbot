package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/huixiangyang/weclaw/runtimecontrol"
)

const (
	defaultServiceName = "weclaw.service"
	defaultAPIBaseURL  = "http://127.0.0.1:18011"
	systemctlPath      = "/usr/bin/systemctl"
)

var serviceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.@-]+\.service$`)

var serviceHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func validateServiceName(service string) error {
	if !serviceNamePattern.MatchString(service) {
		return fmt.Errorf("invalid systemd user service name")
	}
	return nil
}

func validateLoopbackAPI(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("API address must be a plain loopback HTTP origin")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || parsed.Port() == "" {
		return nil, fmt.Errorf("API address must use a loopback IP and explicit port")
	}
	return parsed, nil
}

func fetchHealth(ctx context.Context, apiBase string) (runtimecontrol.Snapshot, error) {
	parsed, err := validateLoopbackAPI(apiBase)
	if err != nil {
		return runtimecontrol.Snapshot{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String()+"/health", nil)
	if err != nil {
		return runtimecontrol.Snapshot{}, err
	}
	response, err := serviceHTTPClient.Do(request)
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

func requestAdmin(ctx context.Context, apiBase, action string) (runtimecontrol.Snapshot, error) {
	parsed, err := validateLoopbackAPI(apiBase)
	if err != nil {
		return runtimecontrol.Snapshot{}, err
	}
	if action != "drain" && action != "resume" {
		return runtimecontrol.Snapshot{}, fmt.Errorf("invalid runtime admin action")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String()+"/admin/"+action, http.NoBody)
	if err != nil {
		return runtimecontrol.Snapshot{}, err
	}
	response, err := serviceHTTPClient.Do(request)
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

func waitForDrain(ctx context.Context, apiBase string, timeout time.Duration) (runtimecontrol.Snapshot, error) {
	deadline := time.Now().Add(timeout)
	for {
		snapshot, err := fetchHealth(ctx, apiBase)
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

func waitForReady(ctx context.Context, apiBase, expectedVersion string, timeout time.Duration) (runtimecontrol.Snapshot, error) {
	deadline := time.Now().Add(timeout)
	for {
		snapshot, err := fetchHealth(ctx, apiBase)
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

func waitForHealthyDrain(ctx context.Context, apiBase, expectedVersion string, timeout time.Duration) (runtimecontrol.Snapshot, error) {
	deadline := time.Now().Add(timeout)
	for {
		snapshot, err := fetchHealth(ctx, apiBase)
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
	return filepath.Join(home, ".local", "bin", "weclaw"), nil
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
