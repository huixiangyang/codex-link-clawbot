package messaging

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huixiangyang/weclaw/ilink"
)

func TestExtractImageURLs(t *testing.T) {
	text := "check ![img](https://example.com/a.png) and ![](https://example.com/b.jpg)"
	urls := ExtractImageURLs(text)
	if len(urls) != 2 {
		t.Fatalf("expected 2 urls, got %d", len(urls))
	}
	if urls[0] != "https://example.com/a.png" {
		t.Errorf("urls[0] = %q", urls[0])
	}
	if urls[1] != "https://example.com/b.jpg" {
		t.Errorf("urls[1] = %q", urls[1])
	}
}

func TestExtractImageURLs_NoImages(t *testing.T) {
	urls := ExtractImageURLs("just plain text")
	if len(urls) != 0 {
		t.Errorf("expected 0 urls, got %d", len(urls))
	}
}

func TestExtractImageURLs_RelativeURL(t *testing.T) {
	text := "![img](./local.png)"
	urls := ExtractImageURLs(text)
	if len(urls) != 0 {
		t.Errorf("expected 0 urls for relative path, got %d", len(urls))
	}
}

func TestFilenameFromURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://example.com/photo.png", "photo.png"},
		{"https://example.com/path/to/report.pdf", "report.pdf"},
		{"https://example.com/file", "file"},
	}
	for _, tt := range tests {
		got := filenameFromURL(tt.url)
		if got != tt.want {
			t.Errorf("filenameFromURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestFilenameFromURL_WithQuery(t *testing.T) {
	got := filenameFromURL("https://example.com/photo.png?token=abc")
	if got != "photo.png" {
		t.Errorf("got %q, want %q", got, "photo.png")
	}
}

func TestStripQuery(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://example.com/a?b=c", "https://example.com/a"},
		{"https://example.com/a", "https://example.com/a"},
		{"https://example.com/?x=1&y=2", "https://example.com/"},
	}
	for _, tt := range tests {
		got := stripQuery(tt.input)
		if got != tt.want {
			t.Errorf("stripQuery(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestReadAllLimitedRejectsOversizedBody(t *testing.T) {
	_, err := readAllLimited(strings.NewReader("12345"), 4)
	if err == nil {
		t.Fatal("readAllLimited() error = nil, want size limit error")
	}
}

func TestSendMediaBatchDoesNotExposePartialBatchWhenStagingFails(t *testing.T) {
	stagingCalls := 0
	sendCalls := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ilink/bot/getuploadurl":
			stagingCalls++
			if stagingCalls == 2 {
				_ = json.NewEncoder(response).Encode(map[string]any{"ret": 1, "errmsg": "second upload rejected"})
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"ret": 0, "upload_full_url": server.URL + "/upload"})
		case "/upload":
			response.Header().Set("X-Encrypted-Param", "download-token")
			response.WriteHeader(http.StatusOK)
		case "/ilink/bot/sendmessage":
			sendCalls++
			_ = json.NewEncoder(response).Encode(map[string]any{"ret": 0})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	err := sendMediaBatch(context.Background(), client, "owner-1", "context", []outboundMediaPayload{
		{FileName: "card.png", Source: "card.png", Data: []byte("image"), ContentType: "image/png"},
		{FileName: "voice.mp3", Source: "voice.mp3", Data: []byte("audio"), ContentType: "audio/mpeg"},
	})
	if err == nil || !strings.Contains(err.Error(), "stage media 2/2") {
		t.Fatalf("error = %v, want second staging failure", err)
	}
	if sendCalls != 0 {
		t.Fatalf("send calls = %d, want no visible partial batch", sendCalls)
	}
}
