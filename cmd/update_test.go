package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyReleaseChecksum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "weclaw_linux_amd64")
	content := []byte("verified release binary")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	manifest := []byte(fmt.Sprintf("%x  weclaw_linux_amd64\n", sum))

	if err := verifyReleaseChecksum(path, "weclaw_linux_amd64", manifest); err != nil {
		t.Fatalf("verify checksum: %v", err)
	}
	if err := verifyReleaseChecksum(path, "weclaw_silk_encoder_linux_amd64", manifest); err == nil || !strings.Contains(err.Error(), "missing or invalid") {
		t.Fatalf("missing checksum error = %v", err)
	}

	badManifest := []byte(strings.Repeat("0", sha256.Size*2) + "  weclaw_linux_amd64\n")
	if err := verifyReleaseChecksum(path, "weclaw_linux_amd64", badManifest); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("checksum mismatch error = %v", err)
	}
}
