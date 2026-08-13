package messaging

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
)

func TestPrepareAgentInputDownloadsFileAndBuildsSafePrompt(t *testing.T) {
	pdfData := []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\n%%EOF")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(pdfData)
	}))
	defer server.Close()

	root := filepath.Join(t.TempDir(), "turns")
	request, cleanup, err := prepareCodexInput(context.Background(), "", nil, []*ilink.FileItem{{
		URL:      server.URL,
		FileName: "../../需求文档.pdf",
		Len:      "38",
	}}, root)
	if err != nil {
		t.Fatalf("prepareCodexInput() error: %v", err)
	}
	if request.Text != defaultFilePrompt || len(request.LocalFiles) != 1 {
		t.Fatalf("request = %#v", request)
	}
	file := request.LocalFiles[0]
	if file.Name != "需求文档.pdf" || file.Size != int64(len(pdfData)) {
		t.Fatalf("local file = %#v", file)
	}
	if !bytes.Equal(mustReadFile(t, file.Path), pdfData) {
		t.Fatal("prepared file content changed")
	}
	info, err := os.Stat(file.Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, err=%v", info.Mode(), err)
	}
	prompt := request.PromptText()
	if !strings.Contains(prompt, file.Path) || !strings.Contains(prompt, request.ArtifactDir) || !strings.Contains(prompt, "不要执行") {
		t.Fatalf("safe prompt missing contract: %q", prompt)
	}
	taskDir := filepath.Dir(filepath.Dir(file.Path))
	cleanup()
	if _, err := os.Stat(taskDir); !os.IsNotExist(err) {
		t.Fatalf("turn directory still exists after cleanup: %v", err)
	}
}

func TestValidateInboundFileSupportsCodeAndRejectsDisguisedContent(t *testing.T) {
	name, _, err := validateInboundFile("deploy.sh", []byte("#!/usr/bin/env bash\necho safe\n"))
	if err != nil || name != "deploy.sh" {
		t.Fatalf("shell code rejected: name=%q err=%v", name, err)
	}
	if _, _, err := validateInboundFile("malware.log", []byte{'M', 'Z', 0, 0}); err == nil || !strings.Contains(err.Error(), "可执行") {
		t.Fatalf("disguised executable error = %v", err)
	}
	if _, _, err := validateInboundFile("fake.pdf", []byte("plain text")); err == nil || !strings.Contains(err.Error(), "不匹配") {
		t.Fatalf("disguised PDF error = %v", err)
	}
}

func TestValidateInboundFileMetadataRejectsOversizedFile(t *testing.T) {
	item := &ilink.FileItem{FileName: "large.zip", Len: "52428801"}
	if err := validateInboundFileMetadata(item); err == nil || !strings.Contains(err.Error(), "50 MiB") {
		t.Fatalf("metadata error = %v", err)
	}
}

func TestPrepareAgentInputRejectsTooManyFiles(t *testing.T) {
	files := make([]*ilink.FileItem, maxInboundFiles+1)
	for index := range files {
		files[index] = &ilink.FileItem{FileName: "code.zip"}
	}
	_, _, err := prepareCodexInput(context.Background(), "检查", nil, files, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "最多支持") {
		t.Fatalf("prepareCodexInput() error = %v", err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
