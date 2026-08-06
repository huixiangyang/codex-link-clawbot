package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/huixiangyang/weclaw/internal/ilink"
)

func TestPrepareQueuedInputDownloadsAndValidatesWithoutWritingTurns(t *testing.T) {
	png := testPNG(t)
	text, images, files, err := prepareQueuedInputWithDownloaders(
		context.Background(), "", []*ilink.ImageItem{{}}, []*ilink.FileItem{{FileName: "notes.md", Len: "5"}},
		inboundDownloaders{
			image: func(context.Context, *ilink.ImageItem) ([]byte, error) { return png, nil },
			file:  func(context.Context, *ilink.FileItem) ([]byte, error) { return []byte("hello"), nil },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "文件") || len(images) != 1 || len(files) != 1 {
		t.Fatalf("queued input text=%q images=%#v files=%#v", text, images, files)
	}
	if images[0].ContentType != "image/png" || files[0].Name != "notes.md" || string(files[0].Data) != "hello" {
		t.Fatalf("queued attachment metadata images=%#v files=%#v", images, files)
	}
}
