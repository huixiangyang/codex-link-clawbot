package messaging

import (
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
	if string(audio) != string(wantAudio) {
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
	voice := NewVoiceBriefing([]VoiceProviderEntry{{Provider: &stubVoiceProvider{id: "local", audio: validTestMP3()}, Timeout: time.Second}})
	if _, err := voice.Generate(context.Background(), strings.Repeat("字", maxVoiceTextRunes+1)); err == nil || !strings.Contains(err.Error(), "2500") {
		t.Fatalf("error = %v", err)
	}
}

func TestVoiceBriefingFallsBackInConfiguredOrder(t *testing.T) {
	first := &stubVoiceProvider{id: "local", waitForContext: true}
	second := &stubVoiceProvider{id: "mimo", audio: validTestMP3()}
	voice := NewVoiceBriefing([]VoiceProviderEntry{
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
	voice := NewVoiceBriefing([]VoiceProviderEntry{
		{Provider: &stubVoiceProvider{id: "local", err: context.DeadlineExceeded}, Timeout: time.Second},
		{Provider: &stubVoiceProvider{id: "mimo", err: context.Canceled}, Timeout: time.Second},
	})

	_, err := voice.Generate(context.Background(), "测试失败")
	if err == nil || !strings.Contains(err.Error(), "local") || !strings.Contains(err.Error(), "mimo") {
		t.Fatalf("error = %v", err)
	}
}

func TestPiperVoiceProviderGeneratesMP3(t *testing.T) {
	dir := t.TempDir()
	piper := filepath.Join(dir, "piper")
	ffmpeg := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, piper, `#!/bin/sh
output=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = '-f' ]; then shift; output="$1"; fi
  shift
done
printf 'RIFFfake-wave' > "$output"
`)
	writeExecutable(t, ffmpeg, `#!/bin/sh
for output do :; done
printf 'ID3\004\000\000\000\000\000\000' > "$output"
`)
	provider := NewPiperVoiceProvider("local", PiperVoiceProviderConfig{
		Command: piper, Model: filepath.Join(dir, "voice.onnx"), ModelConfig: filepath.Join(dir, "voice.onnx.json"),
		FFmpegCommand: ffmpeg, LengthScale: 1,
	})

	sentinel := filepath.Join(dir, "injected")
	audio, err := provider.Generate(context.Background(), "本机语音; touch "+sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !isMP3(audio) {
		t.Fatalf("audio = %v", audio)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("voice text was interpreted by a shell: %v", err)
	}
}

func TestPiperVoiceProviderLive(t *testing.T) {
	command := os.Getenv("WECLAW_TEST_PIPER_COMMAND")
	model := os.Getenv("WECLAW_TEST_PIPER_MODEL")
	modelConfig := os.Getenv("WECLAW_TEST_PIPER_MODEL_CONFIG")
	ffmpeg := os.Getenv("WECLAW_TEST_FFMPEG_COMMAND")
	if command == "" || model == "" || modelConfig == "" || ffmpeg == "" {
		t.Skip("未配置本机 Piper 实测环境")
	}
	provider := NewPiperVoiceProvider("local", PiperVoiceProviderConfig{
		Command: command, Model: model, ModelConfig: modelConfig,
		FFmpegCommand: ffmpeg, LengthScale: 1,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	audio, err := provider.Generate(ctx, "本机语音服务连接测试成功。")
	if err != nil {
		t.Fatal(err)
	}
	if !isMP3(audio) || len(audio) < 1000 {
		t.Fatalf("unexpected live audio: bytes=%d", len(audio))
	}
}

func TestSendPiperVoiceLive(t *testing.T) {
	if os.Getenv("WECLAW_TEST_SEND_VOICE") != "1" {
		t.Skip("未启用微信语音实发测试")
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
	if client.OwnerUserID() == "" {
		t.Fatal("WeChat owner user ID is empty")
	}
	provider := NewPiperVoiceProvider("local", PiperVoiceProviderConfig{
		Command: command, Model: model, ModelConfig: modelConfig,
		FFmpegCommand: ffmpeg, LengthScale: 1,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	audio, err := provider.Generate(ctx, "本机语音服务已经接入。网络服务不可用时，也可以继续生成语音。")
	if err != nil {
		t.Fatal(err)
	}
	if err := SendVoice(ctx, client, client.OwnerUserID(), audio, ""); err != nil {
		t.Fatal(err)
	}
}

type stubVoiceProvider struct {
	id             string
	audio          []byte
	err            error
	waitForContext bool
	calls          int
}

func (p *stubVoiceProvider) ID() string { return p.id }

func (p *stubVoiceProvider) Generate(ctx context.Context, _ string) ([]byte, error) {
	p.calls++
	if p.waitForContext {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return p.audio, p.err
}

func validTestMP3() []byte {
	return []byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 0}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}
