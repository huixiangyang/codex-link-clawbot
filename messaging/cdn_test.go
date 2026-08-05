package messaging

import (
	"testing"

	"github.com/huixiangyang/weclaw/ilink"
)

func TestSelectCDNDownloadParamUsesVoiceUploadTokenFallback(t *testing.T) {
	if got := selectCDNDownloadParam(ilink.CDNMediaTypeVoice, "upload", "", ""); got != "upload" {
		t.Fatalf("voice download token = %q", got)
	}
	if got := selectCDNDownloadParam(ilink.CDNMediaTypeVoice, "upload", "query", "short"); got != "upload" {
		t.Fatalf("voice upload token precedence = %q", got)
	}
}

func TestSelectCDNDownloadParamKeepsMediaSpecificPrecedence(t *testing.T) {
	if got := selectCDNDownloadParam(ilink.CDNMediaTypeImage, "upload", "query", "short"); got != "short" {
		t.Fatalf("image download token = %q", got)
	}
	if got := selectCDNDownloadParam(ilink.CDNMediaTypeFile, "upload", "query", "short"); got != "query" {
		t.Fatalf("file download token = %q", got)
	}
}
