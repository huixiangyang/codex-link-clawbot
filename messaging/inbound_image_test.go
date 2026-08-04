package messaging

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huixiangyang/weclaw/ilink"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
}

func TestPrepareAgentInputDownloadsImageAndCleansTaskDirectory(t *testing.T) {
	imageData := testPNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(imageData)
	}))
	defer server.Close()

	root := filepath.Join(t.TempDir(), "inbox")
	request, cleanup, err := prepareCodexInput(context.Background(), "", []*ilink.ImageItem{{URL: server.URL}}, nil, root)
	if err != nil {
		t.Fatalf("prepareCodexInput() error: %v", err)
	}
	if request.Text != defaultImagePrompt {
		t.Fatalf("request.Text = %q", request.Text)
	}
	if len(request.LocalImages) != 1 {
		t.Fatalf("request.LocalImages = %#v", request.LocalImages)
	}
	imagePath := request.LocalImages[0]
	if filepath.Ext(imagePath) != ".png" {
		t.Fatalf("image path = %q, want png", imagePath)
	}
	if rel, err := filepath.Rel(root, imagePath); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("image path %q is outside root %q", imagePath, root)
	}
	got, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatalf("read prepared image: %v", err)
	}
	if !bytes.Equal(got, imageData) {
		t.Fatal("prepared image content changed")
	}
	info, err := os.Stat(imagePath)
	if err != nil {
		t.Fatalf("stat prepared image: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("image mode = %o, want 600", info.Mode().Perm())
	}
	taskDir := filepath.Dir(imagePath)
	cleanup()
	if _, err := os.Stat(taskDir); !os.IsNotExist(err) {
		t.Fatalf("task directory still exists after cleanup: %v", err)
	}
}

func TestPrepareAgentInputPreservesImagePrompt(t *testing.T) {
	imageData := testPNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(imageData)
	}))
	defer server.Close()

	request, cleanup, err := prepareCodexInput(context.Background(), "  修复截图里的错误  ", []*ilink.ImageItem{{URL: server.URL}}, nil, t.TempDir())
	if err != nil {
		t.Fatalf("prepareCodexInput() error: %v", err)
	}
	defer cleanup()
	if request.Text != "修复截图里的错误" {
		t.Fatalf("request.Text = %q", request.Text)
	}
}

func TestPrepareAgentInputRejectsInvalidImageAndRemovesPartialFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not an image"))
	}))
	defer server.Close()

	root := filepath.Join(t.TempDir(), "inbox")
	_, _, err := prepareCodexInput(context.Background(), "分析", []*ilink.ImageItem{{URL: server.URL}}, nil, root)
	if err == nil || !strings.Contains(err.Error(), "不支持的图片格式") {
		t.Fatalf("prepareCodexInput() error = %v", err)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatalf("read inbox: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("partial task directories were not removed: %v", entries)
	}
}

func TestPrepareAgentInputRejectsTooManyImages(t *testing.T) {
	images := make([]*ilink.ImageItem, maxInboundImages+1)
	for index := range images {
		images[index] = &ilink.ImageItem{URL: "https://example.invalid/image.png"}
	}
	_, _, err := prepareCodexInput(context.Background(), "分析", images, nil, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "最多支持") {
		t.Fatalf("prepareCodexInput() error = %v", err)
	}
}

func TestDownloadInboundImageRejectsOversizedMetadata(t *testing.T) {
	_, err := downloadInboundImage(context.Background(), &ilink.ImageItem{MidSize: int(maxInboundImageBytes + 17)})
	if err == nil || !strings.Contains(err.Error(), "20 MiB") {
		t.Fatalf("downloadInboundImage() error = %v", err)
	}
}

func TestPrepareAgentInputCancellationRemovesTaskDirectory(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	root := filepath.Join(t.TempDir(), "inbox")
	_, _, err := prepareCodexInput(ctx, "分析", []*ilink.ImageItem{{URL: server.URL}}, nil, root)
	if err == nil {
		t.Fatal("prepareCodexInput() error = nil, want cancellation")
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatalf("read inbox: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("cancelled image task left files behind: %v", entries)
	}
}
