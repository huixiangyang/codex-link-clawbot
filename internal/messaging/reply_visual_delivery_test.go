package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/taskqueue"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

func multiPageVisualReply(t *testing.T) string {
	t.Helper()
	paragraphs := make([]string, 9)
	for index := range paragraphs {
		paragraphs[index] = strings.Repeat("这是用于验证移动端多页阅读批次的正文。", 14)
	}
	reply := "# 交付检查\n\n" + strings.Join(paragraphs, "\n\n")
	if pages := visual.PaginateMarkdown(reply); len(pages) < 2 {
		t.Fatalf("test reply produced %d page", len(pages))
	}
	return reply
}

func TestReadingPagesStageAsOneBatchBeforeVisibility(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "document.png")
	if err := os.WriteFile(imagePath, []byte("rendered-page"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer := &fakeControlVisualRenderer{path: imagePath}
	stagingCalls := 0
	var sent []ilink.SendMessageRequest
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ilink/bot/getuploadurl":
			stagingCalls++
			if stagingCalls == 2 {
				_ = json.NewEncoder(response).Encode(map[string]any{"ret": 1, "errmsg": "second page rejected"})
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"ret": 0, "upload_full_url": server.URL + "/upload"})
		case "/upload":
			response.Header().Set("X-Encrypted-Param", "download-token")
			response.WriteHeader(http.StatusOK)
		case "/ilink/bot/sendmessage":
			var message ilink.SendMessageRequest
			_ = json.NewDecoder(request.Body).Decode(&message)
			sent = append(sent, message)
			_ = json.NewEncoder(response).Encode(map[string]any{"ret": 0})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	handler := NewHandler(nil)
	handler.SetVisualRenderer(renderer)
	previous := &cachedVisualReply{Text: "此前成功发送的原文"}
	handler.visualReplies.Store("owner", previous)
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot", ILinkUserID: "owner", BaseURL: server.URL})
	reply := multiPageVisualReply(t)
	report := handler.deliverReplyPlan(
		context.Background(), client, ilink.WeixinMessage{FromUserID: "owner", ContextToken: "context"},
		reply, nil, nil, nil, "client", DeliverySource{}, preference.ResponseReading, visual.StyleEditorial, "Project",
	)
	if renderer.documentRenderCalls < 2 || stagingCalls != 2 {
		t.Fatalf("render calls=%d staging calls=%d", renderer.documentRenderCalls, stagingCalls)
	}
	if len(sent) != 1 || sent[0].Msg.ItemList[0].Type != ilink.ItemTypeText || !strings.Contains(sent[0].Msg.ItemList[0].TextItem.Text, "移动端多页阅读批次") {
		t.Fatalf("visible messages after staging failure = %#v", sent)
	}
	if report.Outcome != taskqueue.DeliverySucceeded || report.MediaSent != 0 || !report.TextSent {
		t.Fatalf("delivery report = %#v", report)
	}
	if cached, exists := handler.visualReplies.Load("owner"); !exists || cached != previous {
		t.Fatal("staging failure must preserve the previously delivered copyable reply")
	}
}

func TestReadingBatchPartialSendNeverDuplicatesFullText(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "document.png")
	if err := os.WriteFile(imagePath, []byte("rendered-page"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer := &fakeControlVisualRenderer{path: imagePath}
	var sent []ilink.SendMessageRequest
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ilink/bot/getuploadurl":
			_, _ = fmt.Fprintf(response, `{"ret":0,"upload_full_url":%q}`, server.URL+"/upload")
		case "/upload":
			response.Header().Set("X-Encrypted-Param", "download-token")
			response.WriteHeader(http.StatusOK)
		case "/ilink/bot/sendmessage":
			var message ilink.SendMessageRequest
			_ = json.NewDecoder(request.Body).Decode(&message)
			sent = append(sent, message)
			if len(sent) == 2 {
				_ = json.NewEncoder(response).Encode(map[string]any{"ret": 7, "errmsg": "second page rejected"})
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"ret": 0})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	handler := NewHandler(nil)
	handler.SetVisualRenderer(renderer)
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot", ILinkUserID: "owner", BaseURL: server.URL})
	report := handler.deliverReplyPlan(
		context.Background(), client, ilink.WeixinMessage{FromUserID: "owner", ContextToken: "context"},
		multiPageVisualReply(t), nil, nil, nil, "client", DeliverySource{}, preference.ResponseReading, visual.StyleEditorial, "Project",
	)
	if len(sent) != 2 || sent[0].Msg.ItemList[0].Type != ilink.ItemTypeImage || sent[1].Msg.ItemList[0].Type != ilink.ItemTypeImage {
		t.Fatalf("partial send messages = %#v", sent)
	}
	if report.Outcome != taskqueue.DeliveryAmbiguous || report.MediaSent != 1 || report.TextSent {
		t.Fatalf("partial delivery report = %#v", report)
	}
	if _, exists := handler.visualReplies.Load("owner"); !exists {
		t.Fatal("partial image delivery must retain the copyable original")
	}
}
