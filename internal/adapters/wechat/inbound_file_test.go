package wechat

import (
	"os"

	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
)

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

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
