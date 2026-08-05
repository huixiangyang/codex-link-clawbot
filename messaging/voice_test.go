package messaging

import (
	"context"
	"encoding/base64"
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
	"github.com/huixiangyang/weclaw/visual"
)

func TestVoiceBriefingCallsMiMoTTSContract(t *testing.T) {
	wantAudio := []byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 0}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		var payload mimoTTSRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model != "mimo-v2.5-tts" || payload.Audio.Format != "mp3" || payload.Audio.Voice != "茉莉" {
			t.Errorf("unexpected payload: %#v", payload)
		}
		if len(payload.Messages) != 2 || payload.Messages[0].Role != "user" || payload.Messages[0].Content != "自然播报" || payload.Messages[1].Role != "assistant" || payload.Messages[1].Content != "测试简报" {
			t.Errorf("messages = %#v", payload.Messages)
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"audio": map[string]any{
				"data": base64.StdEncoding.EncodeToString(wantAudio), "format": "mp3",
			}}}},
		})
	}))
	defer server.Close()

	voice := newMiMoVoiceProviderWithClient("mimo", MiMoVoiceProviderConfig{
		BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "mimo-v2.5-tts", Voice: "茉莉", StylePrompt: "自然播报",
	}, server.Client())
	audio, err := voice.Generate(context.Background(), "测试简报")
	if err != nil {
		t.Fatal(err)
	}
	if audio.Format != VoiceAudioMP3 || string(audio.Data) != string(wantAudio) {
		t.Fatalf("audio = %v, want %v", audio, wantAudio)
	}
}

func TestVoiceBriefingSurfacesMiMoGatewayError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(response).Encode(map[string]any{"error": map[string]any{"message": "没有可用的内网节点 test-key"}})
	}))
	defer server.Close()
	voice := newMiMoVoiceProviderWithClient("mimo", MiMoVoiceProviderConfig{
		BaseURL: server.URL, APIKey: "test-key", Model: "mimo-v2.5-tts", Voice: "茉莉",
	}, server.Client())

	_, err := voice.Generate(context.Background(), "测试简报")
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") || !strings.Contains(err.Error(), "没有可用的内网节点") || strings.Contains(err.Error(), "test-key") {
		t.Fatalf("error = %v", err)
	}
}

func TestVoiceBriefingRejectsInvalidAudio(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"audio": map[string]any{
				"data": base64.StdEncoding.EncodeToString([]byte("not-mp3")), "format": "mp3",
			}}}},
		})
	}))
	defer server.Close()
	voice := newMiMoVoiceProviderWithClient("mimo", MiMoVoiceProviderConfig{
		BaseURL: server.URL, APIKey: "test-key", Model: "mimo-v2.5-tts", Voice: "茉莉",
	}, server.Client())

	if _, err := voice.Generate(context.Background(), "测试简报"); err == nil || !strings.Contains(err.Error(), "无效的 MP3") {
		t.Fatalf("error = %v", err)
	}
}

func TestVoiceBriefingRejectsOversizeTextBeforeRequest(t *testing.T) {
	voice := NewVoiceBriefing("/usr/bin/ffmpeg", []VoiceProviderEntry{{Provider: &stubVoiceProvider{id: "local", audio: validTestMP3()}, Timeout: time.Second}})
	if _, err := voice.Generate(context.Background(), strings.Repeat("字", maxVoiceTextRunes+1)); err == nil || !strings.Contains(err.Error(), "2500") {
		t.Fatalf("error = %v", err)
	}
}

func TestVoiceBriefingFallsBackInConfiguredOrder(t *testing.T) {
	first := &stubVoiceProvider{id: "local", waitForContext: true}
	second := &stubVoiceProvider{id: "mimo", audio: validTestMP3()}
	voice := NewVoiceBriefing("/usr/bin/ffmpeg", []VoiceProviderEntry{
		{Provider: first, Timeout: 10 * time.Millisecond},
		{Provider: second, Timeout: time.Second},
	})

	result, err := voice.Generate(context.Background(), "测试回退")
	if err != nil {
		t.Fatal(err)
	}
	if result.ProviderID != "mimo" || first.calls != 1 || second.calls != 1 {
		t.Fatalf("result = %#v, calls = %d/%d", result, first.calls, second.calls)
	}
}

func TestVoiceBriefingReportsEveryProviderFailure(t *testing.T) {
	voice := NewVoiceBriefing("/usr/bin/ffmpeg", []VoiceProviderEntry{
		{Provider: &stubVoiceProvider{id: "local", err: context.DeadlineExceeded}, Timeout: time.Second},
		{Provider: &stubVoiceProvider{id: "mimo", err: context.Canceled}, Timeout: time.Second},
	})

	_, err := voice.Generate(context.Background(), "测试失败")
	if err == nil || !strings.Contains(err.Error(), "local") || !strings.Contains(err.Error(), "mimo") {
		t.Fatalf("error = %v", err)
	}
}

func TestVoiceBriefingAcceptsNaturalPhrases(t *testing.T) {
	handler := NewHandler(nil)
	handler.SetVoiceBriefing(NewVoiceBriefing("/usr/bin/ffmpeg", nil))
	for _, phrase := range []string{"语音简报", "发语音", "发个语音", "来段语音", "播报一下", "读给我听"} {
		reply, handled := handler.handleControlInput(context.Background(), "owner-1", phrase, false)
		if !handled || !strings.Contains(reply, "正在生成") {
			t.Fatalf("phrase %q: handled=%v reply=%q", phrase, handled, reply)
		}
		if _, exists := handler.controlVoice.LoadAndDelete("owner-1"); !exists {
			t.Fatalf("phrase %q did not schedule a voice briefing", phrase)
		}
	}
}

func TestPiperVoiceProviderGeneratesWAV(t *testing.T) {
	dir := t.TempDir()
	piper := filepath.Join(dir, "piper")
	writeExecutable(t, piper, `#!/bin/sh
output=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = '-f' ]; then shift; output="$1"; fi
  shift
done
printf 'RIFFxxxxWAVEdata' > "$output"
`)
	provider := NewPiperVoiceProvider("local", PiperVoiceProviderConfig{
		Command: piper, Model: filepath.Join(dir, "voice.onnx"), ModelConfig: filepath.Join(dir, "voice.onnx.json"),
		LengthScale: 1,
	})

	sentinel := filepath.Join(dir, "injected")
	audio, err := provider.Generate(context.Background(), "本机语音; touch "+sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if audio.Format != VoiceAudioWAV || !isWAV(audio.Data) {
		t.Fatalf("audio = %v", audio)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("voice text was interpreted by a shell: %v", err)
	}
}

func TestPiperVoicePipelineLive(t *testing.T) {
	command := os.Getenv("WECLAW_TEST_PIPER_COMMAND")
	model := os.Getenv("WECLAW_TEST_PIPER_MODEL")
	modelConfig := os.Getenv("WECLAW_TEST_PIPER_MODEL_CONFIG")
	ffmpeg := os.Getenv("WECLAW_TEST_FFMPEG_COMMAND")
	if command == "" || model == "" || modelConfig == "" || ffmpeg == "" {
		t.Skip("未配置本机 Piper 实测环境")
	}
	provider := NewPiperVoiceProvider("local", PiperVoiceProviderConfig{
		Command: command, Model: model, ModelConfig: modelConfig,
		LengthScale: 1,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	audio, err := provider.Generate(ctx, "本机语音服务连接测试成功。")
	if err != nil {
		t.Fatal(err)
	}
	if audio.Format != VoiceAudioWAV || !isWAV(audio.Data) || len(audio.Data) < 1000 {
		t.Fatalf("unexpected live audio: format=%s bytes=%d", audio.Format, len(audio.Data))
	}
	mp3, err := EncodeVoiceMP3(ctx, ffmpeg, audio)
	if err != nil {
		t.Fatal(err)
	}
	if !isMP3(mp3) || len(mp3) < 1000 {
		t.Fatalf("unexpected MP3 audio: bytes=%d", len(mp3))
	}
}

func TestUploadWeChatAudioFileLive(t *testing.T) {
	if os.Getenv("WECLAW_TEST_UPLOAD_AUDIO_FILE") != "1" {
		t.Skip("未启用微信音频文件 CDN 实测")
	}
	command := os.Getenv("WECLAW_TEST_PIPER_COMMAND")
	model := os.Getenv("WECLAW_TEST_PIPER_MODEL")
	modelConfig := os.Getenv("WECLAW_TEST_PIPER_MODEL_CONFIG")
	ffmpeg := os.Getenv("WECLAW_TEST_FFMPEG_COMMAND")
	if command == "" || model == "" || modelConfig == "" || ffmpeg == "" {
		t.Fatal("本机 Piper 实测环境不完整")
	}
	accounts, err := ilink.LoadAllCredentials()
	if err != nil || len(accounts) == 0 {
		t.Fatalf("load WeChat credentials: count=%d err=%v", len(accounts), err)
	}
	client := ilink.NewClient(accounts[0])
	provider := NewPiperVoiceProvider("local", PiperVoiceProviderConfig{
		Command: command, Model: model, ModelConfig: modelConfig, LengthScale: 1,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	audio, err := provider.Generate(ctx, "微信语音上传链路测试。")
	if err != nil {
		t.Fatal(err)
	}
	mp3, err := EncodeVoiceMP3(ctx, ffmpeg, audio)
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := UploadFileToCDN(ctx, client, mp3, client.OwnerUserID(), ilink.CDNMediaTypeFile)
	if err != nil {
		t.Fatal(err)
	}
	if uploaded.DownloadParam == "" || uploaded.FileSize != len(mp3) {
		t.Fatalf("uploaded audio file = %#v", uploaded)
	}
}

func TestEncodeVoiceMP3CreatesValidAudio(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, ffmpeg, `#!/bin/sh
cat >/dev/null
printf 'ID3\004\000\000\000\000\000\000encoded'
`)
	mp3, err := EncodeVoiceMP3(context.Background(), ffmpeg, validTestMP3())
	if err != nil {
		t.Fatal(err)
	}
	if !isMP3(mp3) {
		t.Fatalf("MP3 bytes=%d", len(mp3))
	}
}

func TestSendVoiceBriefingRejectsMissingContextToken(t *testing.T) {
	handler := NewHandler(nil)
	handler.SetVoiceBriefing(NewVoiceBriefing("/usr/bin/ffmpeg", nil))
	if _, err := handler.sendVoiceBriefing(context.Background(), nil, "owner", ""); err == nil || !strings.Contains(err.Error(), "context token") {
		t.Fatalf("error = %v", err)
	}
}

func TestVoiceAudioUsesFileProtocol(t *testing.T) {
	mediaType, itemType := classifyMedia("audio/mpeg", "weclaw-briefing.mp3")
	if mediaType != ilink.CDNMediaTypeFile || itemType != ilink.ItemTypeFile {
		t.Fatalf("audio protocol = media:%d item:%d", mediaType, itemType)
	}
}

func TestVoiceBriefingDeliversCompanionImageBeforeMP3(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, ffmpeg, `#!/bin/sh
cat >/dev/null
printf 'ID3\004\000\000\000\000\000\000encoded'
`)
	cardPath := filepath.Join(dir, "companion.png")
	if err := os.WriteFile(cardPath, []byte("rendered companion"), 0o600); err != nil {
		t.Fatal(err)
	}

	var sent []ilink.SendMessageRequest
	var deliveryEvents []string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ilink/bot/getuploadurl":
			deliveryEvents = append(deliveryEvents, "stage")
			_ = json.NewEncoder(response).Encode(map[string]any{"ret": 0, "upload_full_url": server.URL + "/upload"})
		case "/upload":
			deliveryEvents = append(deliveryEvents, "upload")
			response.Header().Set("X-Encrypted-Param", "download-token")
			response.WriteHeader(http.StatusOK)
		case "/ilink/bot/sendmessage":
			deliveryEvents = append(deliveryEvents, "send")
			var message ilink.SendMessageRequest
			if err := json.NewDecoder(request.Body).Decode(&message); err != nil {
				t.Errorf("decode sent message: %v", err)
			}
			sent = append(sent, message)
			_ = json.NewEncoder(response).Encode(map[string]any{"ret": 0})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	renderer := &fakeControlVisualRenderer{path: cardPath}
	handler := NewHandler(nil)
	handler.SetVisualRenderer(renderer)
	handler.SetVoiceBriefing(NewVoiceBriefing(ffmpeg, []VoiceProviderEntry{{
		Provider: &stubVoiceProvider{id: "local", audio: validTestMP3()}, Timeout: time.Second,
	}}))
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 1001, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context-voice",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "发语音"}}},
	})

	if len(sent) != 2 {
		t.Fatalf("sent messages = %d, want companion image and MP3 only", len(sent))
	}
	if got, want := strings.Join(deliveryEvents, ","), "stage,upload,stage,upload,send,send"; got != want {
		t.Fatalf("delivery events = %q, want %q", got, want)
	}
	if item := sent[0].Msg.ItemList[0]; item.Type != ilink.ItemTypeImage || item.ImageItem == nil {
		t.Fatalf("first message = %#v, want companion image", item)
	}
	fileItem := sent[1].Msg.ItemList[0]
	if fileItem.Type != ilink.ItemTypeFile || fileItem.FileItem == nil || fileItem.FileItem.FileName != "weclaw-briefing.mp3" {
		t.Fatalf("second message = %#v, want MP3 file", fileItem)
	}
	if renderer.renderCalls != 1 || renderer.documentRenderCalls != 0 || len(renderer.documents) != 0 {
		t.Fatalf("renderer = documents:%d cards:%d", renderer.documentRenderCalls, renderer.renderCalls)
	}
	card := renderer.card
	if card.Title != "语音简报" || card.Footer != "配套 MP3 音频文件随后发送" || card.Variant != visual.VariantSystem {
		t.Fatalf("companion card = %#v", card)
	}
	if len(card.Facts) != 2 || card.Facts[0] != (visual.Fact{Label: "当前项目", Value: "未配置项目"}) || card.Facts[1] != (visual.Fact{Label: "音频来源", Value: "local"}) {
		t.Fatalf("companion facts = %#v", card.Facts)
	}
	if len(card.Body) != 1 || !strings.Contains(card.Body[0], "WeClaw 工作简报") {
		t.Fatalf("companion content = %#v", card.Body)
	}
}

func TestVoiceBriefingStopsBeforeAudioWhenCompanionCardFails(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, ffmpeg, `#!/bin/sh
cat >/dev/null
printf 'ID3\004\000\000\000\000\000\000encoded'
`)

	renderer := &fakeControlVisualRenderer{err: fmt.Errorf("renderer unavailable")}
	handler := NewHandler(nil)
	handler.SetVisualRenderer(renderer)
	handler.SetVoiceBriefing(NewVoiceBriefing(ffmpeg, []VoiceProviderEntry{{
		Provider: &stubVoiceProvider{id: "local", audio: validTestMP3()}, Timeout: time.Second,
	}}))

	_, err := handler.sendVoiceBriefing(context.Background(), nil, "owner-1", "context-voice")
	if err == nil || !strings.Contains(err.Error(), "渲染阅读卡") {
		t.Fatalf("error = %v, want companion card failure", err)
	}
	if renderer.renderCalls != 1 {
		t.Fatalf("render calls = %d, want 1", renderer.renderCalls)
	}
}

type stubVoiceProvider struct {
	id             string
	audio          VoiceAudio
	err            error
	waitForContext bool
	calls          int
}

func (p *stubVoiceProvider) ID() string { return p.id }

func (p *stubVoiceProvider) Generate(ctx context.Context, _ string) (VoiceAudio, error) {
	p.calls++
	if p.waitForContext {
		<-ctx.Done()
		return VoiceAudio{}, ctx.Err()
	}
	return p.audio, p.err
}

func validTestMP3() VoiceAudio {
	return VoiceAudio{Data: []byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 0}, Format: VoiceAudioMP3}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}
