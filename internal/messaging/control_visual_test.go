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
	"time"

	"github.com/huixiangyang/weclaw/internal/ilink"
	"github.com/huixiangyang/weclaw/internal/preference"
	"github.com/huixiangyang/weclaw/internal/visual"
)

type fakeControlVisualRenderer struct {
	path                 string
	err                  error
	documentErr          error
	card                 visual.Card
	directory            visual.Directory
	documents            []visual.Document
	cleanedUp            bool
	renderCalls          int
	directoryRenderCalls int
	documentRenderCalls  int
}

func (r *fakeControlVisualRenderer) RenderDirectory(_ context.Context, directory visual.Directory) (*visual.Artifact, error) {
	r.directoryRenderCalls++
	r.directory = directory
	if r.err != nil {
		return nil, r.err
	}
	return &visual.Artifact{Path: r.path, Width: 1080, Height: 2280, Cleanup: func() { r.cleanedUp = true }}, nil
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
	card := controlCardFromText("WeClaw\n\n版本：v1.4.0-runtime.1\nCodex 线程：视觉交互开发\nWeClaw 执行：空闲\n\n1  Codex 线程\n2  WeClaw 执行状态\n3  WeClaw 运行与安全\n\n回复数字即可，0 退出。")
	if card.Variant != visual.VariantHome || card.Title != "WeClaw" {
		t.Fatalf("main card identity = %#v", card)
	}
	if len(card.Facts) != 3 || card.Facts[0].Label != "版本" || card.Facts[1].Label != "Codex 线程" || card.Facts[1].Value != "视觉交互开发" {
		t.Fatalf("main card facts = %#v", card.Facts)
	}
	if len(card.Options) != 3 || card.Options[2].Label != "WeClaw 运行与安全" {
		t.Fatalf("main card options = %#v", card.Options)
	}
	if card.Footer != "回复数字即可，0 退出。" {
		t.Fatalf("main card footer = %q", card.Footer)
	}
}

func TestControlDirectoryFromTextBuildsSixStableSections(t *testing.T) {
	reply := "WeClaw 操作总览\n\n能力边界：Codex 工作能力与 WeClaw 管理能力分层\n版本：v2\nWeClaw 项目入口：主项目\nCodex 线程：移动端开发\nWeClaw 执行：运行中\nWeClaw 回复：自适应\nWeClaw 视觉：构筑\n\n" +
		"[1]  Codex · 工作台\n11  当前线程\n12  新建线程\n\n" +
		"[2]  WeClaw · 请求\n21  执行状态\n\n" +
		"[3]  WeClaw · 回复\n31  回答方式\n\n" +
		"[4]  WeClaw · 功能\n41  提示词模板 · 2 项\n\n" +
		"[5]  WeClaw · 设置\n51  有效配置状态\n\n" +
		"[6]  WeClaw · 诊断\n63  使用说明\n\n回复编号直接操作，0 退出；首页 30 分钟内有效。"
	directory, ok := controlDirectoryFromText(reply)
	if !ok || directory.Subtitle != "Codex 工作能力与 WeClaw 管理能力分层" || len(directory.Facts) != 6 || len(directory.Sections) != 6 {
		t.Fatalf("directory = %#v, ok=%v", directory, ok)
	}
	if directory.Facts[1].Label != "WeClaw 项目入口" || directory.Facts[1].Value != "主项目" {
		t.Fatalf("directory facts = %#v", directory.Facts)
	}
	if first := directory.Sections[0]; first.Code != "1" || first.Icon != "messages-square" || first.Items[0].Code != "11" || first.Items[0].Label != "当前线程" {
		t.Fatalf("first directory section = %#v", first)
	}
	if current := directory.Sections[3].Items[0]; current.Label != "提示词模板" || current.Meta != "2 项" {
		t.Fatalf("directory item metadata = %#v", current)
	}
}

func TestControlCardFromRuntimeCenter(t *testing.T) {
	reply := "WeClaw 运行与安全\nWeClaw：运行中\n版本：v1.4.0-runtime.1\n已运行：2 小时 5 分\n本地接口：127.0.0.1:18011\nCodex：运行中\n协议：Codex 应用服务\n模型：使用 Codex 默认配置\nCodex 工作目录：/workspace\nCodex PID：4242\n\n1  工作目录\n2  刷新 WeClaw 状态\n\n回复数字操作，0 返回。"
	card := controlCardFromText(reply)
	if card.Variant != visual.VariantSystem || card.Title != "WeClaw 运行与安全" {
		t.Fatalf("runtime card identity = %#v", card)
	}
	if len(card.Facts) != 9 || card.Facts[0].Label != "WeClaw" || card.Facts[8].Label != "Codex PID" {
		t.Fatalf("runtime card facts = %#v", card.Facts)
	}
	if len(card.Options) != 2 || card.Options[1].Label != "刷新 WeClaw 状态" {
		t.Fatalf("runtime card options = %#v", card.Options)
	}
}

func TestControlCardUsesWarningSemanticsForArchiveConfirmation(t *testing.T) {
	reply := "准备归档线程：视觉交互开发\n\n1  确认归档\n\n回复 1 确认，0 返回。"
	card := controlCardFromText(reply)
	if card.Variant != visual.VariantWarning || card.Title != "归档确认" || card.Subtitle != "视觉交互开发" {
		t.Fatalf("archive card = %#v", card)
	}
	if got := controlCaption(reply, card); got != "回复 1 确认，0 返回。" {
		t.Fatalf("archive caption = %q", got)
	}
}

func TestControlCardFromAutomationDetail(t *testing.T) {
	reply := "自动化详情：项目日报\n\n状态：正常\n计划：每天 09:00\n时区：Asia/Shanghai\n\n1  立即检查\n2  返回自动化中心\n\n回复数字操作，0 返回自动化中心。"
	card := controlCardFromText(reply)
	if card.Variant != visual.VariantSystem || card.Title != "自动化详情" || card.Subtitle != "项目日报" {
		t.Fatalf("report card identity = %#v", card)
	}
	if len(card.Facts) != 3 || card.Facts[0].Label != "状态" || card.Facts[0].Value != "正常" {
		t.Fatalf("report card facts = %#v", card.Facts)
	}
	if len(card.Options) != 2 || card.Options[1].Label != "返回自动化中心" {
		t.Fatalf("report card options = %#v", card.Options)
	}
}

func TestControlCardFromBrowsableSessionDetail(t *testing.T) {
	reply := "线程详情\n名称：登录排障\n短编号：00000001\n状态：空闲\n位置：可用\n目录：/workspace\n摘要：检查微信登录失败原因\n\n1  切换到这个线程\n2  归档这个线程\n3  返回线程列表\n\n回复数字管理，0 返回原列表。"
	card := controlCardFromText(reply)
	if card.Variant != visual.VariantSession || card.Title != "线程详情" {
		t.Fatalf("session detail card identity = %#v", card)
	}
	if len(card.Facts) != 6 || card.Facts[3].Label != "位置" || card.Facts[5].Label != "摘要" {
		t.Fatalf("session detail card facts = %#v", card.Facts)
	}
	if len(card.Options) != 3 || card.Options[0].Label != "切换到这个线程" {
		t.Fatalf("session detail card options = %#v", card.Options)
	}
}

func TestControlCardFromTaskHistory(t *testing.T) {
	reply := "WeClaw 请求队列\n\n页码：1 / 2\n等待：2\n执行：1\n已暂停：否\n\n1  检查发布流程 · 已完成\n2  分析失败原因 · 失败\n7  下一页 · 2/2\n\n回复数字查看详情，或说“下一页”“上一页”；0 返回。"
	card := controlCardFromText(reply)
	if card.Variant != visual.VariantProgress || card.Title != "WeClaw 请求队列" {
		t.Fatalf("activity card identity = %#v", card)
	}
	if len(card.Facts) != 4 || card.Facts[3].Label != "已暂停" {
		t.Fatalf("activity card facts = %#v", card.Facts)
	}
	if len(card.Options) != 3 || card.Options[2].Label != "下一页 · 2/2" {
		t.Fatalf("activity card options = %#v", card.Options)
	}
}

func TestHandleMessageSendsSingleVisualCommandDirectory(t *testing.T) {
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
	styleStore, err := preference.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := styleStore.SetStyle("owner-1", visual.StyleNoir); err != nil {
		t.Fatal(err)
	}
	handler.SetPreferenceStore(styleStore)
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 9201, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context-visual",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "/"}}},
	})

	if runtime.chatThreadID != "" {
		t.Fatalf("visual control unexpectedly started Codex thread %s", runtime.chatThreadID)
	}
	if renderer.directoryRenderCalls != 1 || renderer.renderCalls != 0 || renderer.directory.Style != visual.StyleNoir || len(renderer.directory.Sections) != 6 || !renderer.cleanedUp {
		t.Fatalf("renderer state = directory calls:%d card calls:%d directory:%#v cleanup:%v", renderer.directoryRenderCalls, renderer.renderCalls, renderer.directory, renderer.cleanedUp)
	}
	if len(uploaded) == 0 {
		t.Fatal("visual card was not uploaded")
	}
	if len(sent) != 1 {
		t.Fatalf("sent messages = %d, want exactly one directory image", len(sent))
	}
	if item := sent[0].Msg.ItemList[0]; item.Type != ilink.ItemTypeImage || item.ImageItem == nil {
		t.Fatalf("directory message = %#v", item)
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
	if len(sent.Msg.ItemList) != 1 || sent.Msg.ItemList[0].TextItem == nil || !strings.Contains(sent.Msg.ItemList[0].TextItem.Text, "11  当前线程") {
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
	if len(sent.Msg.ItemList) != 1 || sent.Msg.ItemList[0].TextItem == nil || !strings.Contains(sent.Msg.ItemList[0].TextItem.Text, "回复编号直接操作") {
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
	styleStore, err := preference.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := styleStore.SetStyle("owner-1", visual.StyleEditorial); err != nil {
		t.Fatal(err)
	}
	handler.SetPreferenceStore(styleStore)
	handler.SetVisualReplyConfig(true, 20)
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	reply := "# 移动端结果\n\n" + strings.Repeat("这是一段适合手机阅读并且需要保留可复制原文的 Codex 回复。", 16)
	handler.sendReplyWithMedia(context.Background(), client, ilink.WeixinMessage{FromUserID: "owner-1", ContextToken: "context-long"}, reply, "", "client-long")

	if renderer.documentRenderCalls == 0 || len(renderer.documents) == 0 || !renderer.cleanedUp {
		t.Fatalf("document renderer state = calls:%d documents:%d cleanup:%v", renderer.documentRenderCalls, len(renderer.documents), renderer.cleanedUp)
	}
	for _, document := range renderer.documents {
		if document.Style != visual.StyleEditorial {
			t.Fatalf("reading document style = %q", document.Style)
		}
	}
	if len(sent) != renderer.documentRenderCalls {
		t.Fatalf("sent messages = %d, want %d reading pages without a redundant caption", len(sent), renderer.documentRenderCalls)
	}
	if item := sent[0].Msg.ItemList[0]; item.Type != ilink.ItemTypeImage || item.ImageItem == nil {
		t.Fatalf("reading page message = %#v", item)
	}
	_ = controlReply(t, handler, "owner-1", "/")

	handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 9301, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context-copy",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "文字版"}}},
	})
	if runtime.chatThreadID != "" {
		t.Fatalf("copyable text request unexpectedly reached Codex thread %s", runtime.chatThreadID)
	}
	if _, status, err := handler.controlStates.Load("owner-1"); err != nil || status != controlStateMissing {
		t.Fatal("copyable text retrieval should clear the previous menu state")
	}
	if len(sent) != renderer.documentRenderCalls+1 {
		t.Fatalf("sent messages after text retrieval = %d", len(sent))
	}
	copyItem := sent[len(sent)-1].Msg.ItemList[0].TextItem
	if copyItem == nil || !strings.Contains(copyItem.Text, "移动端结果") || !strings.Contains(copyItem.Text, "可复制原文") {
		t.Fatalf("copyable reply = %#v", copyItem)
	}
}

func TestExpiredCopyableTextRequestNeverStartsCodexTurn(t *testing.T) {
	var sent ilink.SendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode expired copy notice: %v", err)
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	handler, runtime := newSessionHandler(t)
	handler.visualReplies.Store("owner-1", &cachedVisualReply{
		Text: "已经过期的原文", ExpiresAt: time.Now().Add(-time.Second),
	})
	_ = controlReply(t, handler, "owner-1", "/")
	client := ilink.NewClient(&ilink.Credentials{
		BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL,
	})
	handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 9302, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context-expired",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "文字版"}}},
	})
	if runtime.chatThreadID != "" {
		t.Fatalf("expired copy request unexpectedly reached Codex thread %s", runtime.chatThreadID)
	}
	if _, status, err := handler.controlStates.Load("owner-1"); err != nil || status != controlStateMissing {
		t.Fatal("expired copy request should clear the previous menu state")
	}
	item := sent.Msg.ItemList[0].TextItem
	if item == nil || !strings.Contains(item.Text, "已过期或不存在") || !strings.Contains(item.Text, "30 分钟") {
		t.Fatalf("expired copy notice = %#v", item)
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
