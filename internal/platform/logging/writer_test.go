package logging

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestWriterRotationRetentionPermissionsAndMirror(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "logs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 8; i++ {
		name := "service-" + time.Now().Add(-time.Duration(i)*24*time.Hour).UTC().Format("2006-01-02T15-04-05.000") + ".log"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	var mirror bytes.Buffer
	w, err := Open(root, &mirror)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	w.file.MaxSize = 1
	w.Protect("known-secret", "known-secret-longer")
	message := "test known-secret-longer api_key=hidden https://user:password@example.test/api?token=signed#private\ninjected\n"
	if n, err := w.Write([]byte(message)); err != nil || n != len(message) {
		t.Fatalf("write: n=%d err=%v", n, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != mirror.String() || !strings.Contains(string(data), `\ninjected`) {
		t.Fatalf("file and mirror differ: %q %v", data, err)
	}
	for _, secret := range []string{"known-secret", "longer", "hidden", "user:password", "signed", "#private"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("log leaked %q", secret)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) <= 6 {
			for _, entry := range entries {
				info, err := entry.Info()
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("unsafe log mode: %s %v", entry.Name(), err)
				}
				if entry.Name() != FileName {
					stamp := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "service-"), ".log")
					at, err := time.Parse("2006-01-02T15-04-05.000", stamp)
					if err != nil || time.Since(at) > 7*24*time.Hour {
						t.Fatalf("expired log remains: %s", entry.Name())
					}
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("archive cleanup did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0o700 {
		t.Fatal("log directory is not private")
	}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = fmt.Fprintf(w, "worker=%d\n", i) }()
	}
	wg.Wait()
	w.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	if _, err := w.Write([]byte("next day\n")); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "next day\n" {
		t.Fatalf("daily rotation failed: %q %v", data, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("after close")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
}

func TestWriterRejectsSymlinkAndPreservesStderrOnFileFailure(t *testing.T) {
	root := t.TempDir()
	w, err := Open(root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "logs", FileName)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, io.Discard); err == nil {
		t.Fatal("accepted symlink log")
	}
	var mirror bytes.Buffer
	w, err = Open(t.TempDir(), &mirror)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	_ = w.file.Close()
	dir := filepath.Dir(w.file.Filename)
	if err := os.Rename(dir, dir+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("still visible\n")); err == nil {
		t.Fatal("expected file error")
	}
	if !strings.Contains(mirror.String(), "still visible") || !strings.Contains(mirror.String(), "file output failed") {
		t.Fatal("file failure hid stderr diagnostics")
	}
}

func TestSanitizeCredentialsAndBoundedSingleLine(t *testing.T) {
	message := `Authorization: Bearer bearer-secret {"client_secret":"json-secret","context_token":"context-secret"} password='quoted secret'`
	safe := Sanitize(message)
	for _, secret := range []string{"bearer-secret", "json-secret", "context-secret", "quoted secret"} {
		if strings.Contains(safe, secret) {
			t.Fatalf("leaked credential: %s", safe)
		}
	}
	if got := Sanitize(strings.Repeat("中", maxEntryBytes)); !utf8.ValidString(got) || len(got) > maxEntryBytes+20 || !strings.HasSuffix(got, "[truncated]") {
		t.Fatal("unbounded or invalid UTF-8 log entry")
	}
	if got := Sanitize("first\n\x1b[2Jsecond\r"); strings.ContainsAny(got, "\n\r\x1b") {
		t.Fatal("control characters escaped into log")
	}
	w, err := Open(t.TempDir(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Protect("bare-secret")
	cause := errors.New("bare-secret")
	err = w.SanitizeError(cause)
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "bare-secret") {
		t.Fatal("error redaction lost the cause or exposed the secret")
	}
}
