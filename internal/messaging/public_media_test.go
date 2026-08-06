package messaging

import (
	"net"
	"testing"
)

func TestPublicMediaIPRejectsInternalNetworks(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.8", "172.16.1.2", "192.168.1.2", "169.254.169.254", "::1", "fe80::1"} {
		if isPublicMediaIP(net.ParseIP(raw)) {
			t.Fatalf("internal address %s was accepted", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "2606:4700:4700::1111"} {
		if !isPublicMediaIP(net.ParseIP(raw)) {
			t.Fatalf("public address %s was rejected", raw)
		}
	}
}

func TestPublicMediaURLRequiresHTTPS(t *testing.T) {
	for _, raw := range []string{"http://example.com/image.png", "https://user:secret@example.com/a", "https://example.com/a#fragment"} {
		if err := validatePublicMediaURL(raw); err == nil {
			t.Fatalf("unsafe URL %q was accepted", raw)
		}
	}
	if err := validatePublicMediaURL("https://example.com/image.png?size=large"); err != nil {
		t.Fatalf("valid URL rejected: %v", err)
	}
}
