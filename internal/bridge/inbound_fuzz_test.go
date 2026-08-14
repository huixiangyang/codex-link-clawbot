package bridge

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func FuzzValidateInboundFile(f *testing.F) {
	f.Add("report.pdf", []byte("%PDF-1.7\n"))
	f.Add("script.sh", []byte("#!/bin/sh\necho ok\n"))
	f.Add("program.txt", []byte{'M', 'Z', 0, 0})
	f.Add("../../notes.md", []byte("safe text"))

	f.Fuzz(func(t *testing.T, rawName string, data []byte) {
		if len(rawName) > 4096 || len(data) > 1<<20 {
			t.Skip()
		}
		name, _, err := validateInboundFile(rawName, data)
		if err != nil {
			return
		}
		if name == "" || strings.ContainsAny(name, "/\\:") || filepath.Base(name) != name {
			t.Fatalf("unsafe sanitized name %q", name)
		}
		if supportedInboundExtension(name) == "" {
			t.Fatalf("accepted unsupported extension in %q", name)
		}
		if hasExecutableMagic(data) {
			t.Fatal("accepted executable magic")
		}
	})
}

func FuzzValidatedImageExtension(f *testing.F) {
	f.Add(append([]byte(nil), []byte("\x89PNG\r\n\x1a\n")...))
	f.Add([]byte("GIF89a"))
	f.Add([]byte{0xff, 0xd8, 0xff, 0xe0})
	f.Add([]byte("not an image"))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		ext, err := validatedImageExtension(data)
		if err != nil {
			return
		}
		allowed := []string{".jpg", ".png", ".gif", ".webp"}
		if !bytes.Contains([]byte(strings.Join(allowed, "\n")), []byte(ext)) {
			t.Fatalf("unexpected accepted image extension %q", ext)
		}
	})
}
