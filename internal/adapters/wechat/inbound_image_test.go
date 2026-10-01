package wechat

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"

	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
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

func TestDownloadInboundImageRejectsOversizedMetadata(t *testing.T) {
	_, err := downloadInboundImage(context.Background(), &ilink.ImageItem{MidSize: int(maxInboundImageBytes + 17)})
	if err == nil || !strings.Contains(err.Error(), "20 MiB") {
		t.Fatalf("downloadInboundImage() error = %v", err)
	}
}
