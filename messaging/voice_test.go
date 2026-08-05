package messaging

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/ilink"
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
	voice := NewVoiceBriefing("/usr/bin/ffmpeg", "/usr/bin/weclaw-silk-encoder", []VoiceProviderEntry{{Provider: &stubVoiceProvider{id: "local", audio: validTestMP3()}, Timeout: time.Second}})
	if _, err := voice.Generate(context.Background(), strings.Repeat("字", maxVoiceTextRunes+1)); err == nil || !strings.Contains(err.Error(), "2500") {
		t.Fatalf("error = %v", err)
	}
}

func TestVoiceBriefingFallsBackInConfiguredOrder(t *testing.T) {
	first := &stubVoiceProvider{id: "local", waitForContext: true}
	second := &stubVoiceProvider{id: "mimo", audio: validTestMP3()}
	voice := NewVoiceBriefing("/usr/bin/ffmpeg", "/usr/bin/weclaw-silk-encoder", []VoiceProviderEntry{
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
	voice := NewVoiceBriefing("/usr/bin/ffmpeg", "/usr/bin/weclaw-silk-encoder", []VoiceProviderEntry{
		{Provider: &stubVoiceProvider{id: "local", err: context.DeadlineExceeded}, Timeout: time.Second},
		{Provider: &stubVoiceProvider{id: "mimo", err: context.Canceled}, Timeout: time.Second},
	})

	_, err := voice.Generate(context.Background(), "测试失败")
	if err == nil || !strings.Contains(err.Error(), "local") || !strings.Contains(err.Error(), "mimo") {
		t.Fatalf("error = %v", err)
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
	silkCommand := os.Getenv("WECLAW_TEST_SILK_COMMAND")
	if command == "" || model == "" || modelConfig == "" || ffmpeg == "" || silkCommand == "" {
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
	wechatVoice, err := EncodeWeChatVoice(ctx, ffmpeg, silkCommand, audio)
	if err != nil {
		t.Fatal(err)
	}
	if wechatVoice.PlaytimeMS <= 0 || !bytes.HasPrefix(wechatVoice.Data, []byte("\x02#!SILK_V3")) {
		t.Fatalf("unexpected WeChat voice: playtime=%d bytes=%d", wechatVoice.PlaytimeMS, len(wechatVoice.Data))
	}
}

func TestUploadWeChatVoiceLive(t *testing.T) {
	if os.Getenv("WECLAW_TEST_UPLOAD_VOICE") != "1" {
		t.Skip("未启用微信语音 CDN 实测")
	}
	command := os.Getenv("WECLAW_TEST_PIPER_COMMAND")
	model := os.Getenv("WECLAW_TEST_PIPER_MODEL")
	modelConfig := os.Getenv("WECLAW_TEST_PIPER_MODEL_CONFIG")
	ffmpeg := os.Getenv("WECLAW_TEST_FFMPEG_COMMAND")
	silkCommand := os.Getenv("WECLAW_TEST_SILK_COMMAND")
	if command == "" || model == "" || modelConfig == "" || ffmpeg == "" || silkCommand == "" {
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
	voice, err := EncodeWeChatVoice(ctx, ffmpeg, silkCommand, audio)
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := UploadFileToCDN(ctx, client, voice.Data, client.OwnerUserID(), ilink.CDNMediaTypeVoice)
	if err != nil {
		t.Fatal(err)
	}
	if uploaded.DownloadParam == "" || uploaded.FileSize != len(voice.Data) {
		t.Fatalf("uploaded voice = %#v", uploaded)
	}
}

func TestEncodeWeChatVoiceCreatesTencentSILK(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, ffmpeg, `#!/bin/sh
head -c 32000 /dev/zero
`)
	silkCommand := filepath.Join(dir, "silk")
	writeExecutable(t, silkCommand, `#!/bin/sh
cat >/dev/null
printf '\002#!SILK_V3encoded'
`)
	voice, err := EncodeWeChatVoice(context.Background(), ffmpeg, silkCommand, validTestMP3())
	if err != nil {
		t.Fatal(err)
	}
	if voice.PlaytimeMS != 1000 || !strings.HasPrefix(string(voice.Data), "\x02#!SILK_V3") {
		t.Fatalf("voice = playtime:%d bytes:%d", voice.PlaytimeMS, len(voice.Data))
	}
}

func TestSendVoiceRejectsMissingContextToken(t *testing.T) {
	voice := WeChatVoice{Data: append([]byte("\x02#!SILK_V3"), 1), PlaytimeMS: 20}
	if err := SendVoice(context.Background(), nil, "owner", voice, ""); err == nil || !strings.Contains(err.Error(), "context token") {
		t.Fatalf("error = %v", err)
	}
}

func TestVoiceMessageItemUsesNativeSilkProtocol(t *testing.T) {
	item := newVoiceMessageItem(&UploadedFile{
		DownloadParam: "download-token",
		AESKeyHex:     "00112233445566778899aabbccddeeff",
	}, 1234)
	if item.Type != ilink.ItemTypeVoice || item.VoiceItem == nil {
		t.Fatalf("item = %#v", item)
	}
	voice := item.VoiceItem
	if voice.EncodeType != 6 || voice.BitsPerSample != 16 || voice.SampleRate != 16000 || voice.Playtime != 1234 {
		t.Fatalf("voice metadata = %#v", voice)
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "voice_size") || !strings.Contains(string(data), "download-token") {
		t.Fatalf("voice JSON = %s", data)
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
