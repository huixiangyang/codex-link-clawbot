package wechat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
)

type fileCaptureAgent struct {
	*handlerThreadClient
	request codex.ChatRequest
	data    []byte
}

func (a *fileCaptureAgent) ChatThread(_ context.Context, _ string, request codex.ChatRequest) (string, error) {
	a.request = request
	if len(request.LocalFiles) > 0 {
		data, err := os.ReadFile(request.LocalFiles[0].Path)
		if err != nil {
			return "", err
		}
		a.data = data
	}
	return "文件检查完成", nil
}

func TestHandleMessagePassesWechatFileToAgent(t *testing.T) {
	fileData := []byte("error: build failed\nline 42\n")
	var sentMu sync.Mutex
	var sent []ilink.SendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/build.log":
			_, _ = w.Write(fileData)
		case "/ilink/bot/sendmessage":
			var request ilink.SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode send message: %v", err)
			}
			sentMu.Lock()
			sent = append(sent, request)
			sentMu.Unlock()
			_, _ = w.Write([]byte(`{"ret":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "user-1", BaseURL: server.URL})
	capture := &fileCaptureAgent{handlerThreadClient: newHandlerThreadClient()}
	handler := newBareHandler(capture)
	attachTestSessionManager(t, handler)
	handler.progress = execution.ProgressConfig{Enabled: false}
	store, stop := attachTestExecution(t, handler, client, "user-1")
	defer stop()

	if err := handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 2, FromUserID: "user-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context-1",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeFile, FileItem: &ilink.FileItem{
			URL: server.URL + "/build.log", FileName: "build.log", Len: "28",
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	submitTestDraft(t, handler, client, "user-1", 80, "检查文件")
	waitForTerminalTask(t, store, "user-1")

	if capture.request.Text != "检查文件" || len(capture.request.LocalFiles) != 1 {
		t.Fatalf("agent request = %#v", capture.request)
	}
	if string(capture.data) != string(fileData) {
		t.Fatalf("agent file data = %q", capture.data)
	}
	if _, err := os.Stat(capture.request.LocalFiles[0].Path); err != nil {
		t.Fatalf("inbound file was not retained after turn: %v", err)
	}
	sentMu.Lock()
	defer sentMu.Unlock()
	if len(sent) != 3 {
		t.Fatalf("sent messages = %d, want two draft receipts and final reply", len(sent))
	}
	if item := sent[2].Msg.ItemList[0]; item.TextItem == nil || item.TextItem.Text != "文件检查完成" {
		t.Fatalf("second message is not final reply: %#v", item)
	}
}

type artifactAgent struct {
	*handlerThreadClient
	artifactDir string
}

func (a *artifactAgent) ChatThread(_ context.Context, _ string, request codex.ChatRequest) (string, error) {
	a.artifactDir = request.ArtifactDir
	path := filepath.Join(request.ArtifactDir, "changes.patch")
	if err := os.WriteFile(path, []byte("diff --git a/a b/a\n"), 0o600); err != nil {
		return "", err
	}
	return "补丁已经生成。", nil
}

func TestCompletedArtifactsAreRetrievedOnDemand(t *testing.T) {
	var mu sync.Mutex
	var sent []ilink.SendMessageRequest
	var encryptedUpload []byte
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getuploadurl":
			_, _ = w.Write([]byte(`{"ret":0,"upload_full_url":"` + server.URL + `/upload"}`))
		case "/upload":
			encryptedUpload, _ = io.ReadAll(r.Body)
			w.Header().Set("X-Encrypted-Param", "download-token")
			_, _ = w.Write([]byte("ok"))
		case "/ilink/bot/sendmessage":
			var request ilink.SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode send message: %v", err)
			}
			mu.Lock()
			sent = append(sent, request)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ret":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "user-1", BaseURL: server.URL})
	ag := &artifactAgent{handlerThreadClient: newHandlerThreadClient()}
	handler := newBareHandler(ag)
	attachTestSessionManager(t, handler)
	handler.progress = execution.ProgressConfig{Enabled: false}
	store, stop := attachTestExecution(t, handler, client, "user-1")
	defer stop()
	if err := handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 3, FromUserID: "user-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ItemList:     []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "生成补丁"}}},
	}); err != nil {
		t.Fatal(err)
	}
	task := waitForTerminalTask(t, store, "user-1")
	mu.Lock()
	if len(encryptedUpload) != 0 || len(sent) != 1 {
		t.Fatalf("completion should send final reply only: uploads=%d messages=%d", len(encryptedUpload), len(sent))
	}
	sent = nil
	mu.Unlock()
	result, err := store.LoadResult("user-1", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	report := handler.sendReplyWithMediaForTask(context.Background(), client, ilink.WeixinMessage{FromUserID: "user-1", ContextToken: "fresh"}, task, result, NewClientID())
	if report.Outcome != request.DeliverySucceeded {
		t.Fatalf("explicit retrieval: %#v", report)
	}

	if len(encryptedUpload) == 0 {
		t.Fatal("artifact was not uploaded")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("sent messages = %d, want explicitly requested file and text", len(sent))
	}
	if item := sent[0].Msg.ItemList[0]; item.FileItem == nil || item.FileItem.FileName != "changes.patch" {
		t.Fatalf("first message is not patch attachment: %#v", item)
	}
	if item := sent[1].Msg.ItemList[0]; item.TextItem == nil || !strings.Contains(item.TextItem.Text, "已发送附件：changes.patch") {
		t.Fatalf("second message missing artifact summary: %#v", item)
	}
	if _, err := os.Stat(ag.artifactDir); err != nil {
		t.Fatalf("artifact directory was not retained: %v", err)
	}
}
