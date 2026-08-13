package messaging

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
)

type imageCaptureAgent struct {
	*handlerThreadClient
	request   codex.ChatRequest
	imageData []byte
}

func TestRepeatedWechatSourceDoesNotRedownloadAttachment(t *testing.T) {
	imageData := testPNG(t)
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/image.png":
			downloads.Add(1)
			_, _ = w.Write(imageData)
		case "/ilink/bot/sendmessage":
			_, _ = w.Write([]byte(`{"ret":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := ilink.NewClient(&ilink.Credentials{
		BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "user-1", BaseURL: server.URL,
	})
	handler := NewHandler(&imageCaptureAgent{handlerThreadClient: newHandlerThreadClient()})
	attachTestSessionManager(t, handler)
	store, stop := attachTestTaskQueue(t, handler, client, "user-1")
	defer stop()
	handler.coordinator.SetDraining(true)
	message := ilink.WeixinMessage{
		MessageID: 77, FromUserID: "user-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeImage, ImageItem: &ilink.ImageItem{URL: server.URL + "/image.png"}}},
	}
	for range 2 {
		if err := handler.HandleMessage(context.Background(), client, message); err != nil {
			t.Fatal(err)
		}
	}
	if downloads.Load() != 1 || len(store.List("user-1")) != 1 {
		t.Fatalf("downloads=%d tasks=%d", downloads.Load(), len(store.List("user-1")))
	}
}

func (a *imageCaptureAgent) ChatThread(_ context.Context, _ string, request codex.ChatRequest) (string, error) {
	a.request = request
	if len(request.LocalImages) > 0 {
		data, err := os.ReadFile(request.LocalImages[0])
		if err != nil {
			return "", err
		}
		a.imageData = data
	}
	return "已收到图片", nil
}

func TestHandleMessagePassesWechatImageToAgent(t *testing.T) {
	imageData := testPNG(t)
	var sentReply ilink.SendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/image.png":
			_, _ = w.Write(imageData)
		case "/ilink/bot/sendmessage":
			if err := json.NewDecoder(r.Body).Decode(&sentReply); err != nil {
				t.Errorf("decode send message: %v", err)
			}
			_, _ = w.Write([]byte(`{"ret":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := ilink.NewClient(&ilink.Credentials{
		BotToken:    "token",
		ILinkBotID:  "bot-1",
		ILinkUserID: "user-1",
		BaseURL:     server.URL,
	})
	capture := &imageCaptureAgent{handlerThreadClient: newHandlerThreadClient()}
	handler := NewHandler(capture)
	attachTestSessionManager(t, handler)
	handler.SetProgressConfig(ProgressConfig{Enabled: false})
	store, stop := attachTestTaskQueue(t, handler, client, "user-1")
	defer stop()

	if err := handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID:    1,
		FromUserID:   "user-1",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "context-1",
		ItemList: []ilink.MessageItem{{
			Type:      ilink.ItemTypeImage,
			ImageItem: &ilink.ImageItem{URL: server.URL + "/image.png"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	terminal := waitForTerminalTask(t, store, "user-1")
	if terminal.State != "succeeded" {
		t.Fatalf("terminal task state = %s", terminal.State)
	}

	if capture.request.Text != defaultImagePrompt || len(capture.request.LocalImages) != 1 {
		t.Fatalf("agent request = %#v", capture.request)
	}
	if len(capture.imageData) != len(imageData) {
		t.Fatalf("agent image bytes = %d, want %d", len(capture.imageData), len(imageData))
	}
	if _, err := os.Stat(capture.request.LocalImages[0]); !os.IsNotExist(err) {
		t.Fatalf("inbound image was not cleaned after turn: %v", err)
	}
	if len(sentReply.Msg.ItemList) != 1 || sentReply.Msg.ItemList[0].TextItem == nil || sentReply.Msg.ItemList[0].TextItem.Text != "已收到图片" {
		t.Fatalf("sent reply = %#v", sentReply.Msg.ItemList)
	}
}
