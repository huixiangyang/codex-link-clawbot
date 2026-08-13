package cli

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	githubRepo            = "huixiangyang/codex-link-clawbot"
	maxReleaseBinaryBytes = 100 << 20
	maxChecksumBytes      = 1 << 20
)

var deployHTTPClient = &http.Client{Timeout: 2 * time.Minute}

func downloadReleaseFile(url string, maxBytes int64, executable bool) (string, error) {
	response, err := deployHTTPClient.Get(url)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", response.StatusCode)
	}
	temporary, err := os.CreateTemp("", "codex-link-clawbot-deploy-download-*")
	if err != nil {
		return "", err
	}
	path := temporary.Name()
	remove := true
	defer func() {
		_ = temporary.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	written, err := io.Copy(temporary, io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return "", err
	}
	if written > maxBytes {
		return "", fmt.Errorf("response exceeds %d bytes", maxBytes)
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	mode := os.FileMode(0o600)
	if executable {
		mode = 0o700
	}
	if err := os.Chmod(path, mode); err != nil {
		return "", err
	}
	remove = false
	return path, nil
}

func verifyReleaseChecksum(path, filename string, manifest []byte) (string, error) {
	want := ""
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != filename {
			continue
		}
		want = strings.ToLower(fields[0])
		break
	}
	if len(want) != sha256.Size*2 {
		return "", fmt.Errorf("checksum for %s is missing or invalid", filename)
	}
	got, err := fileSHA256(path)
	if err != nil {
		return "", err
	}
	if got != want {
		return "", fmt.Errorf("checksum mismatch for %s", filename)
	}
	return got, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
