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
	"time"

	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/preference"
)

func TestResponseModeMenuPersistsVoicePreference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := preference.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHandler(t)
	handler.SetPreferenceStore(store)
	handler.SetVisualRenderer(&fakeControlVisualRenderer{})
	handler.SetVoiceBriefing(NewVoiceBriefing("/usr/bin/ffmpeg", nil))

	menu, handled := handler.handleControlInput(context.Background(), "owner-1", "语音模式", false, nextTestControlSource())
	if !handled {
		t.Fatal("response mode menu was not handled")
	}
	for _, want := range []string{"当前：自适应", "1  自适应", "2  阅读", "3  语音", "视觉：构筑"} {
		if !strings.Contains(menu.Text, want) {
			t.Fatalf("response mode menu missing %q: %q", want, menu.Text)
		}
	}
	switched, handled := handler.handleControlInput(context.Background(), "owner-1", "3", false, nextTestControlSource())
	if !handled || !strings.Contains(switched.Text, "回答方式已切换") || !strings.Contains(switched.Text, "当前：语音") {
		t.Fatalf("voice mode switch = %q handled=%v", switched.Text, handled)
	}
	if got := store.Get("owner-1").ResponseMode; got != preference.ResponseVoice {
		t.Fatalf("stored response mode = %q", got)
	}
	reloaded, err := preference.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get("owner-1").ResponseMode; got != preference.ResponseVoice {
		t.Fatalf("reloaded response mode = %q", got)
	}

	direct, handled := handler.handleControlInput(context.Background(), "owner-1", "关闭语音模式", false, nextTestControlSource())
	if !handled || !strings.Contains(direct.Text, "当前：自适应") || store.Get("owner-1").ResponseMode != preference.ResponseAdaptive {
		t.Fatalf("disable voice mode = %q handled=%v", direct.Text, handled)
	}
}

func TestVoiceResponseModeDeliversExactCardThenMP3(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, ffmpeg, `#!/bin/sh
cat >/dev/null
printf 'ID3\004\000\000\000\000\000\000encoded'
`)
	cardPath := filepath.Join(dir, "voice-response.png")
	if err := os.WriteFile(cardPath, []byte("voice response card"), 0o600); err != nil {
		t.Fatal(err)
	}

	var events []string
	var sent []ilink.SendMessageRequest
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ilink/bot/getuploadurl":
			events = append(events, "stage")
			_ = json.NewEncoder(response).Encode(map[string]any{"ret": 0, "upload_full_url": server.URL + "/upload"})
		case "/upload":
			events = append(events, "upload")
			response.Header().Set("X-Encrypted-Param", "download-token")
			response.WriteHeader(http.StatusOK)
		case "/ilink/bot/sendmessage":
			events = append(events, "send")
			var message ilink.SendMessageRequest
			if err := json.NewDecoder(request.Body).Decode(&message); err != nil {
				t.Errorf("decode message: %v", err)
			}
			sent = append(sent, message)
			_ = json.NewEncoder(response).Encode(map[string]any{"ret": 0})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	store, err := preference.NewStore(filepath.Join(dir, "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetResponseMode("owner-1", preference.ResponseVoice); err != nil {
		t.Fatal(err)
	}
	renderer := &fakeControlVisualRenderer{path: cardPath}
	handler := NewHandler(nil)
	handler.SetPreferenceStore(store)
	handler.SetVisualRenderer(renderer)
	provider := &stubVoiceProvider{id: "local", audio: validTestMP3()}
	handler.SetVoiceBriefing(NewVoiceBriefing(ffmpeg, []VoiceProviderEntry{{Provider: provider, Timeout: time.Second}}))
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	reply := "**完成了。**\n\n下一步继续。"
	handler.sendReplyWithMedia(context.Background(), client, ilink.WeixinMessage{FromUserID: "owner-1", ContextToken: "context-voice"}, reply, "", "client-voice")

	if got, want := strings.Join(events, ","), "stage,upload,stage,upload,send,send"; got != want {
		t.Fatalf("delivery events = %q, want %q", got, want)
	}
	if len(sent) != 2 || sent[0].Msg.ItemList[0].Type != ilink.ItemTypeImage || sent[1].Msg.ItemList[0].Type != ilink.ItemTypeFile {
		t.Fatalf("voice response messages = %#v", sent)
	}
	if got := sent[1].Msg.ItemList[0].FileItem.FileName; got != "weclaw-reply.mp3" {
		t.Fatalf("voice response filename = %q", got)
	}
	if renderer.card.Title != "语音回答" || len(renderer.card.Body) != 1 || renderer.card.Body[0] != "完成了。\n\n下一步继续。" {
		t.Fatalf("voice response card = %#v", renderer.card)
	}
	if provider.calls != 1 {
		t.Fatalf("voice provider calls = %d", provider.calls)
	}
	if len(provider.texts) != 1 || provider.texts[0] != renderer.card.Body[0] {
		t.Fatalf("spoken text = %#v, card body = %#v", provider.texts, renderer.card.Body)
	}
}

func TestReadingModeForcesShortReplyCard(t *testing.T) {
	dir := t.TempDir()
	cardPath := filepath.Join(dir, "reading.png")
	if err := os.WriteFile(cardPath, []byte("reading card"), 0o600); err != nil {
		t.Fatal(err)
	}
	var sent []ilink.SendMessageRequest
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ilink/bot/getuploadurl":
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

	store, err := preference.NewStore(filepath.Join(dir, "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetResponseMode("owner-1", preference.ResponseReading); err != nil {
		t.Fatal(err)
	}
	renderer := &fakeControlVisualRenderer{path: cardPath}
	handler := NewHandler(nil)
	handler.SetPreferenceStore(store)
	handler.SetVisualRenderer(renderer)
	handler.SetVisualReplyConfig(false, 5000)
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	handler.sendReplyWithMedia(context.Background(), client, ilink.WeixinMessage{FromUserID: "owner-1", ContextToken: "context-reading"}, "短回答。", "", "client-reading")

	if renderer.documentRenderCalls != 1 || renderer.renderCalls != 0 {
		t.Fatalf("reading renderer calls = document:%d card:%d", renderer.documentRenderCalls, renderer.renderCalls)
	}
	if len(sent) != 1 || sent[0].Msg.ItemList[0].Type != ilink.ItemTypeImage {
		t.Fatalf("reading mode messages = %#v", sent)
	}
}

func TestVoiceModeFallsBackToCompleteReadingCardBeforeVisibility(t *testing.T) {
	dir := t.TempDir()
	cardPath := filepath.Join(dir, "fallback.png")
	if err := os.WriteFile(cardPath, []byte("fallback card"), 0o600); err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ilink/bot/getuploadurl":
			_ = json.NewEncoder(response).Encode(map[string]any{"ret": 0, "upload_full_url": server.URL + "/upload"})
		case "/upload":
			response.Header().Set("X-Encrypted-Param", "download-token")
			response.WriteHeader(http.StatusOK)
		case "/ilink/bot/sendmessage":
			_ = json.NewEncoder(response).Encode(map[string]any{"ret": 0})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	store, err := preference.NewStore(filepath.Join(dir, "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = store.SetResponseMode("owner-1", preference.ResponseVoice)
	renderer := &fakeControlVisualRenderer{path: cardPath}
	handler := NewHandler(nil)
	handler.SetPreferenceStore(store)
	handler.SetVisualRenderer(renderer)
	handler.SetVoiceBriefing(NewVoiceBriefing("/usr/bin/ffmpeg", []VoiceProviderEntry{{
		Provider: &stubVoiceProvider{id: "broken", err: fmt.Errorf("TTS unavailable")}, Timeout: time.Second,
	}}))
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	handler.sendReplyWithMedia(context.Background(), client, ilink.WeixinMessage{FromUserID: "owner-1", ContextToken: "context"}, "失败时仍然必须完整展示这条回答。", "", "client")
	if renderer.renderCalls != 0 || renderer.documentRenderCalls != 1 {
		t.Fatalf("fallback renderer calls = card:%d document:%d", renderer.renderCalls, renderer.documentRenderCalls)
	}
}

func TestVoiceModeDeliversArtifactCompleteLongCardsThenExcerptMP3(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, ffmpeg, `#!/bin/sh
cat >/dev/null
printf 'ID3\004\000\000\000\000\000\000encoded'
`)
	cardPath := filepath.Join(dir, "voice-long.png")
	if err := os.WriteFile(cardPath, []byte("voice long card"), 0o600); err != nil {
		t.Fatal(err)
	}
	outbox := filepath.Join(dir, "outbox")
	if err := os.Mkdir(outbox, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outbox, "result.txt"), []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}

	var sent []ilink.SendMessageRequest
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ilink/bot/getuploadurl":
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

	store, err := preference.NewStore(filepath.Join(dir, "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetResponseMode("owner-1", preference.ResponseVoice); err != nil {
		t.Fatal(err)
	}
	renderer := &fakeControlVisualRenderer{path: cardPath}
	provider := &stubVoiceProvider{id: "local", audio: validTestMP3()}
	handler := NewHandler(nil)
	handler.SetPreferenceStore(store)
	handler.SetVisualRenderer(renderer)
	handler.SetVoiceBriefing(NewVoiceBriefing(ffmpeg, []VoiceProviderEntry{{Provider: provider, Timeout: time.Second}}))
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	reply := strings.Repeat("这是用于验证完整长回答交付顺序的正文。", 180)
	handler.sendReplyWithMedia(context.Background(), client, ilink.WeixinMessage{FromUserID: "owner-1", ContextToken: "context-long"}, reply, outbox, "client-long")

	if renderer.documentRenderCalls < 1 || renderer.renderCalls != 1 {
		t.Fatalf("long voice renderer calls = document:%d card:%d", renderer.documentRenderCalls, renderer.renderCalls)
	}
	if len(sent) != renderer.documentRenderCalls+3 {
		t.Fatalf("sent messages = %d, want %d", len(sent), renderer.documentRenderCalls+3)
	}
	if first := sent[0].Msg.ItemList[0]; first.Type != ilink.ItemTypeFile || first.FileItem.FileName != "result.txt" {
		t.Fatalf("first delivery = %#v", first)
	}
	for index := 1; index < len(sent)-1; index++ {
		if sent[index].Msg.ItemList[0].Type != ilink.ItemTypeImage {
			t.Fatalf("delivery %d is not an image: %#v", index, sent[index])
		}
	}
	if last := sent[len(sent)-1].Msg.ItemList[0]; last.Type != ilink.ItemTypeFile || last.FileItem.FileName != "weclaw-reply.mp3" {
		t.Fatalf("last delivery = %#v", last)
	}
	if len(provider.texts) != 1 || provider.texts[0] != renderer.card.Body[0] || !strings.HasSuffix(provider.texts[0], "回答内容较长，完整内容已放在前面的阅读卡中。") {
		t.Fatalf("long spoken/card text = provider:%#v card:%#v", provider.texts, renderer.card.Body)
	}
}

func TestBuildVoiceReplyScriptMakesLongAnswerExplicit(t *testing.T) {
	script, summarized := buildVoiceReplyScript(strings.Repeat("这是完整回答的一部分。", 400))
	if !summarized || !strings.HasSuffix(script, "回答内容较长，完整内容已放在前面的阅读卡中。") {
		t.Fatalf("long voice script = summarized:%v text:%q", summarized, script)
	}
	if len([]rune(script)) > maxVoiceReplyScriptRunes {
		t.Fatalf("long voice script runes = %d", len([]rune(script)))
	}
}
