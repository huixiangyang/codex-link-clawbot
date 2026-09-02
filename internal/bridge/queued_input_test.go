package bridge

import (
	"context"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
)

func TestQueuedInputDownloadsAttachmentsBeforeAcknowledgement(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00}
	text, images, files, err := prepareQueuedInputWithDownloaders(
		context.Background(), "", []*ilink.ImageItem{{URL: "image"}}, []*ilink.FileItem{{FileName: "note.txt"}},
		inboundDownloaders{
			image: func(context.Context, *ilink.ImageItem) ([]byte, error) { return png, nil },
			file:  func(context.Context, *ilink.FileItem) ([]byte, error) { return []byte("hello"), nil },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if text == "" || len(images) != 1 || len(files) != 1 || images[0].Name != "image-01.png" || files[0].Name != "note.txt" {
		t.Fatalf("unexpected queued input: text=%q images=%#v files=%#v", text, images, files)
	}
}
