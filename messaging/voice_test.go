package messaging

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

	voice := newVoiceBriefingWithClient(VoiceBriefingConfig{
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
	voice := newVoiceBriefingWithClient(VoiceBriefingConfig{
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
	voice := newVoiceBriefingWithClient(VoiceBriefingConfig{
		BaseURL: server.URL, APIKey: "test-key", Model: "mimo-v2.5-tts", Voice: "茉莉",
	}, server.Client())

	if _, err := voice.Generate(context.Background(), "测试简报"); err == nil || !strings.Contains(err.Error(), "无效的 MP3") {
		t.Fatalf("error = %v", err)
	}
}

func TestVoiceBriefingRejectsOversizeTextBeforeRequest(t *testing.T) {
	voice := NewVoiceBriefing(VoiceBriefingConfig{})
	if _, err := voice.Generate(context.Background(), strings.Repeat("字", maxVoiceTextRunes+1)); err == nil || !strings.Contains(err.Error(), "2500") {
		t.Fatalf("error = %v", err)
	}
}
