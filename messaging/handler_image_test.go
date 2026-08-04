package messaging

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/ilink"
)

type imageCaptureAgent struct {
	request   agent.ChatRequest
	imageData []byte
}

func (a *imageCaptureAgent) Chat(_ context.Context, _ string, request agent.ChatRequest) (string, error) {
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

func (a *imageCaptureAgent) Info() agent.AgentInfo {
	return agent.AgentInfo{Name: "capture", Type: "test"}
}

func (a *imageCaptureAgent) SetCwd(string) {}

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
	capture := &imageCaptureAgent{}
	handler := NewHandler(nil, nil)
	handler.SetProgressConfig(ProgressConfig{Enabled: false})
	handler.SetDefaultAgent("capture", capture)

	handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID:    1,
		FromUserID:   "user-1",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "context-1",
		ItemList: []ilink.MessageItem{{
			Type:      ilink.ItemTypeImage,
			ImageItem: &ilink.ImageItem{URL: server.URL + "/image.png"},
		}},
	})

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
