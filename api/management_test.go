package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/runtimecontrol"
	"github.com/huixiangyang/weclaw/taskqueue"
)

func TestManagementServerUsesPrivateUnixSocket(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tasks, err := taskqueue.NewStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	controller := runtimecontrol.New("v2.6-test", tasks, nil)
	controller.SetCodexReady(true)
	controller.SetReady()
	socketPath := filepath.Join(root, ManagementSocketName)
	server := NewManagementServer(controller, socketPath, nil)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- server.Run(ctx) }()

	select {
	case <-server.Ready():
	case err := <-errCh:
		t.Fatalf("management server exited before ready: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("management server did not become ready")
	}
	if err := ValidateManagementSocket(socketPath); err != nil {
		t.Fatalf("validate socket: %v", err)
	}
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket permissions=%#o", info.Mode().Perm())
	}

	client := unixHTTPClient(socketPath)
	response, err := client.Get("http://weclaw.local/health")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot runtimecontrol.Snapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || snapshot.Version != "v2.6-test" {
		t.Fatalf("health status=%d snapshot=%#v", response.StatusCode, snapshot)
	}

	request, err := http.NewRequest(http.MethodPost, "http://weclaw.local/admin/drain", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !controller.Snapshot().Draining {
		t.Fatalf("drain status=%d snapshot=%#v", response.StatusCode, controller.Snapshot())
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("management shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("management server did not stop")
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket still exists after shutdown: %v", err)
	}
}

func TestManagementSocketRejectsUnsafePath(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ManagementSocketName)
	if err := ValidateManagementSocket(path); err == nil {
		t.Fatal("ValidateManagementSocket accepted a non-private parent directory")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := NewManagementServer(nil, path, nil)
	if err := server.Run(context.Background()); err == nil {
		t.Fatal("management server accepted a regular file at the socket path")
	}
}

func TestManagementSocketRejectsBroadPermissions(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ManagementSocketName)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagementSocket(path); err == nil {
		t.Fatal("ValidateManagementSocket accepted broad permissions")
	}
}

func TestDeploymentNotificationIsTypedAndLocal(t *testing.T) {
	var received DeploymentNotice
	server := NewManagementServer(nil, filepath.Join(t.TempDir(), ManagementSocketName), func(_ context.Context, notice DeploymentNotice) error {
		received = notice
		return nil
	})
	valid := `{"from_version":"v2.5","to_version":"v2.6","service":"weclaw.service"}`
	request := httptest.NewRequest(http.MethodPost, "/admin/deployment-notification", strings.NewReader(valid))
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || received.ToVersion != "v2.6" {
		t.Fatalf("notification status=%d notice=%#v", response.Code, received)
	}

	request = httptest.NewRequest(http.MethodPost, "/admin/deployment-notification", strings.NewReader(`{"message":"arbitrary text"}`))
	response = httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("arbitrary notification status=%d", response.Code)
	}
}

func unixHTTPClient(socketPath string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &http.Client{Transport: transport, Timeout: 2 * time.Second}
}
