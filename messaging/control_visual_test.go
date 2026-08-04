package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/visual"
)

type fakeControlVisualRenderer struct {
	path                string
	err                 error
	documentErr         error
	card                visual.Card
	documents           []visual.Document
	cleanedUp           bool
	renderCalls         int
	documentRenderCalls int
}

func (r *fakeControlVisualRenderer) RenderDocument(_ context.Context, document visual.Document) (*visual.Artifact, error) {
	r.documentRenderCalls++
	r.documents = append(r.documents, document)
	if r.documentErr != nil {
		return nil, r.documentErr
	}
	return &visual.Artifact{Path: r.path, Width: 1080, Height: document.Height, Cleanup: func() { r.cleanedUp = true }}, nil
}

func (r *fakeControlVisualRenderer) Render(_ context.Context, card visual.Card) (*visual.Artifact, error) {
	r.renderCalls++
	r.card = card
	if r.err != nil {
		return nil, r.err
	}
	return &visual.Artifact{Path: r.path, Width: 1080, Height: 1200, Cleanup: func() { r.cleanedUp = true }}, nil
}

func TestControlCardFromMainMenu(t *testing.T) {
	card := controlCardFromText("WeClaw\n\n版本：v1.4.0-runtime.1\n会话：视觉交互开发\n状态：空闲\n\n1  会话\n2  任务状态\n3  运行中心\n\n回复数字即可，0 退出。")
	if card.Variant != visual.VariantHome || card.Title != "WeClaw" {
		t.Fatalf("main card identity = %#v", card)
	}
	if len(card.Facts) != 3 || card.Facts[0].Label != "版本" || card.Facts[1].Label != "会话" || card.Facts[1].Value != "视觉交互开发" {
		t.Fatalf("main card facts = %#v", card.Facts)
	}
	if len(card.Options) != 3 || card.Options[2].Label != "运行中心" {
		t.Fatalf("main card options = %#v", card.Options)
	}
	if card.Footer != "回复数字即可，0 退出。" {
		t.Fatalf("main card footer = %q", card.Footer)
	}
}

func TestControlCardFromRuntimeCenter(t *testing.T) {
	reply := "运行中心\nWeClaw：运行中\n版本：v1.4.0-runtime.1\n已运行：2 小时 5 分\n本地接口：127.0.0.1:18011\nCodex：运行中\n协议：App Server\n模型：使用 Codex 默认配置\n工作目录：/workspace\nCodex PID：4242\n\n1  工作目录\n2  刷新运行中心\n\n回复数字操作，0 返回。"
	card := controlCardFromText(reply)
	if card.Variant != visual.VariantSystem || card.Title != "运行中心" {
		t.Fatalf("runtime card identity = %#v", card)
	}
	if len(card.Facts) != 9 || card.Facts[0].Label != "WeClaw" || card.Facts[8].Label != "Codex PID" {
		t.Fatalf("runtime card facts = %#v", card.Facts)
	}
	if len(card.Options) != 2 || card.Options[1].Label != "刷新运行中心" {
		t.Fatalf("runtime card options = %#v", card.Options)
	}
}

func TestControlCardUsesWarningSemanticsForArchiveConfirmation(t *testing.T) {
	reply := "准备归档会话：视觉交互开发\n\n1  确认归档\n\n回复 1 确认，0 返回。"
	card := controlCardFromText(reply)
	if card.Variant != visual.VariantWarning || card.Title != "归档确认" || card.Subtitle != "视觉交互开发" {
		t.Fatalf("archive card = %#v", card)
	}
	if got := controlCaption(reply, card); got != "回复 1 确认，0 返回。" {
		t.Fatalf("archive caption = %q", got)
	}
}

func TestControlCardFromScheduledReportDetail(t *testing.T) {
	reply := "巡检详情：项目日报\n\n状态：今日已发送\n计划：每天 09:00\n时区：Asia/Shanghai\n\n1  刷新巡检详情\n2  返回巡检列表\n\n回复数字操作，0 返回巡检列表。"
	card := controlCardFromText(reply)
	if card.Variant != visual.VariantSystem || card.Title != "巡检详情" || card.Subtitle != "项目日报" {
		t.Fatalf("report card identity = %#v", card)
	}
	if len(card.Facts) != 3 || card.Facts[0].Label != "状态" || card.Facts[0].Value != "今日已发送" {
		t.Fatalf("report card facts = %#v", card.Facts)
	}
	if len(card.Options) != 2 || card.Options[1].Label != "返回巡检列表" {
		t.Fatalf("report card options = %#v", card.Options)
	}
}

func TestControlCardFromBrowsableSessionDetail(t *testing.T) {
	reply := "会话详情\n名称：登录排障\n短编号：00000001\n状态：空闲\n目录：/workspace\n摘要：检查微信登录失败原因\n\n1  切换到这个会话\n2  归档这个会话\n3  返回会话列表\n\n回复数字管理，0 返回原列表。"
	card := controlCardFromText(reply)
	if card.Variant != visual.VariantSession || card.Title != "会话详情" {
		t.Fatalf("session detail card identity = %#v", card)
	}
	if len(card.Facts) != 5 || card.Facts[4].Label != "摘要" {
		t.Fatalf("session detail card facts = %#v", card.Facts)
	}
	if len(card.Options) != 3 || card.Options[0].Label != "切换到这个会话" {
		t.Fatalf("session detail card options = %#v", card.Options)
	}
}

func TestHandleMessageSendsVisualControlCardAndCaption(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "card.png")
	if err := os.WriteFile(imagePath, []byte("test-png"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer := &fakeControlVisualRenderer{path: imagePath}
	var sent []ilink.SendMessageRequest
	var uploaded []byte
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getuploadurl":
			_, _ = fmt.Fprintf(w, `{"ret":0,"upload_full_url":%q}`, server.URL+"/upload")
		case "/upload":
			uploaded, _ = io.ReadAll(r.Body)
			w.Header().Set("X-Encrypted-Param", "download-token")
			_, _ = w.Write([]byte("ok"))
		case "/ilink/bot/sendmessage":
			var request ilink.SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode message: %v", err)
			}
			sent = append(sent, request)
			_, _ = w.Write([]byte(`{"ret":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	handler, runtime := newSessionHandler(t)
	handler.SetVisualRenderer(renderer)
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 9201, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context-visual",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "/"}}},
	})

	if runtime.chatThreadID != "" {
		t.Fatalf("visual control unexpectedly started Codex thread %s", runtime.chatThreadID)
	}
	if renderer.renderCalls != 1 || renderer.card.Variant != visual.VariantHome || !renderer.cleanedUp {
		t.Fatalf("renderer state = calls:%d card:%#v cleanup:%v", renderer.renderCalls, renderer.card, renderer.cleanedUp)
	}
	if len(uploaded) == 0 {
		t.Fatal("visual card was not uploaded")
	}
	if len(sent) != 2 {
		t.Fatalf("sent messages = %d, want image and caption", len(sent))
	}
	if item := sent[0].Msg.ItemList[0]; item.Type != ilink.ItemTypeImage || item.ImageItem == nil {
		t.Fatalf("first visual message = %#v", item)
	}
	if item := sent[1].Msg.ItemList[0]; item.TextItem == nil || !strings.Contains(item.TextItem.Text, "回复数字") {
		t.Fatalf("visual caption = %#v", item)
	}
}

func TestVisualRenderFailureFallsBackToFullText(t *testing.T) {
	renderer := &fakeControlVisualRenderer{err: fmt.Errorf("renderer unavailable")}
	var sent ilink.SendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode fallback: %v", err)
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	handler, _ := newSessionHandler(t)
	handler.SetVisualRenderer(renderer)
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 9202, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ItemList:     []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "/"}}},
	})
	if len(sent.Msg.ItemList) != 1 || sent.Msg.ItemList[0].TextItem == nil || !strings.Contains(sent.Msg.ItemList[0].TextItem.Text, "1  会话") {
		t.Fatalf("fallback message = %#v", sent.Msg.ItemList)
	}
}

func TestVisualUploadFailureFallsBackToFullTextAndCleansArtifact(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "card.png")
	if err := os.WriteFile(imagePath, []byte("test-png"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer := &fakeControlVisualRenderer{path: imagePath}
	var sent ilink.SendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getuploadurl":
			_, _ = w.Write([]byte(`{"ret":7,"errmsg":"upload unavailable"}`))
		case "/ilink/bot/sendmessage":
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Errorf("decode upload fallback: %v", err)
			}
			_, _ = w.Write([]byte(`{"ret":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	handler, _ := newSessionHandler(t)
	handler.SetVisualRenderer(renderer)
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 9203, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ItemList:     []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "/"}}},
	})
	if !renderer.cleanedUp {
		t.Fatal("visual artifact was not cleaned after upload failure")
	}
	if len(sent.Msg.ItemList) != 1 || sent.Msg.ItemList[0].TextItem == nil || !strings.Contains(sent.Msg.ItemList[0].TextItem.Text, "回复数字") {
		t.Fatalf("upload fallback message = %#v", sent.Msg.ItemList)
	}
}

func TestLongCodexReplyUsesReadingCardAndKeepsCopyableText(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "document.png")
	if err := os.WriteFile(imagePath, []byte("test-png"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer := &fakeControlVisualRenderer{path: imagePath}
	var sent []ilink.SendMessageRequest
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getuploadurl":
			_, _ = fmt.Fprintf(w, `{"ret":0,"upload_full_url":%q}`, server.URL+"/upload")
		case "/upload":
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("X-Encrypted-Param", "download-token")
			_, _ = w.Write([]byte("ok"))
		case "/ilink/bot/sendmessage":
			var request ilink.SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode long reply message: %v", err)
			}
			sent = append(sent, request)
			_, _ = w.Write([]byte(`{"ret":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	handler, runtime := newSessionHandler(t)
	handler.SetVisualRenderer(renderer)
	handler.SetVisualReplyConfig(true, 20)
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	reply := "# 移动端结果\n\n" + strings.Repeat("这是一段适合手机阅读并且需要保留可复制原文的 Codex 回复。", 16)
	handler.sendReplyWithMedia(context.Background(), client, ilink.WeixinMessage{FromUserID: "owner-1", ContextToken: "context-long"}, reply, "", "client-long")

	if renderer.documentRenderCalls == 0 || len(renderer.documents) == 0 || !renderer.cleanedUp {
		t.Fatalf("document renderer state = calls:%d documents:%d cleanup:%v", renderer.documentRenderCalls, len(renderer.documents), renderer.cleanedUp)
	}
	if len(sent) != renderer.documentRenderCalls+1 {
		t.Fatalf("sent messages = %d, want %d pages and caption", len(sent), renderer.documentRenderCalls)
	}
	if item := sent[0].Msg.ItemList[0]; item.Type != ilink.ItemTypeImage || item.ImageItem == nil {
		t.Fatalf("reading page message = %#v", item)
	}
	caption := sent[len(sent)-1].Msg.ItemList[0].TextItem
	if caption == nil || !strings.Contains(caption.Text, "文字版") {
		t.Fatalf("reading caption = %#v", caption)
	}

	handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 9301, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context-copy",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "文字版"}}},
	})
	if runtime.chatThreadID != "" {
		t.Fatalf("copyable text request unexpectedly reached Codex thread %s", runtime.chatThreadID)
	}
	if len(sent) != renderer.documentRenderCalls+2 {
		t.Fatalf("sent messages after text retrieval = %d", len(sent))
	}
	copyItem := sent[len(sent)-1].Msg.ItemList[0].TextItem
	if copyItem == nil || !strings.Contains(copyItem.Text, "移动端结果") || !strings.Contains(copyItem.Text, "可复制原文") {
		t.Fatalf("copyable reply = %#v", copyItem)
	}
}

func TestLongReplyRenderFailureFallsBackToFullText(t *testing.T) {
	renderer := &fakeControlVisualRenderer{documentErr: fmt.Errorf("document renderer unavailable")}
	var sent ilink.SendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode long reply fallback: %v", err)
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	handler, _ := newSessionHandler(t)
	handler.SetVisualRenderer(renderer)
	handler.SetVisualReplyConfig(true, 20)
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	reply := strings.Repeat("长回复必须在渲染失败时完整退回文字。", 20)
	handler.sendReplyWithMedia(context.Background(), client, ilink.WeixinMessage{FromUserID: "owner-1"}, reply, "", "fallback-long")
	if renderer.documentRenderCalls != 1 {
		t.Fatalf("document render calls = %d", renderer.documentRenderCalls)
	}
	if len(sent.Msg.ItemList) != 1 || sent.Msg.ItemList[0].TextItem == nil || !strings.Contains(sent.Msg.ItemList[0].TextItem.Text, "完整退回文字") {
		t.Fatalf("long reply fallback = %#v", sent.Msg.ItemList)
	}
}
