package ilink

import (
	"strings"
	"testing"
)

func TestLogLabelIsStableAndDoesNotExposeIdentifier(t *testing.T) {
	raw := "private-user@im.wechat"
	first := LogLabel(raw)
	if first != LogLabel(raw) || first == LogLabel("another-user@im.wechat") {
		t.Fatalf("LogLabel() is not stable or distinct: %q", first)
	}
	if strings.Contains(first, "private") || strings.Contains(first, "wechat") || len(first) != 11 {
		t.Fatalf("LogLabel() exposed identifier: %q", first)
	}
	if LogLabel("") != "id:none" {
		t.Fatalf("empty LogLabel() = %q", LogLabel(""))
	}
}
