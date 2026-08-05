package messaging

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestVoiceBriefingUsesFixedCommandContract(t *testing.T) {
	script := filepath.Join(t.TempDir(), "tts")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'mp3' > \"$2\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	voice := NewVoiceBriefing(script)
	path, cleanup, err := voice.Generate(context.Background(), "测试简报")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if data, err := os.ReadFile(path); err != nil || string(data) != "mp3" {
		t.Fatalf("generated voice = %q, %v", data, err)
	}
}
