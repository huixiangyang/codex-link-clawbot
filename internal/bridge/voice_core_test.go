package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type stubVoiceProvider struct {
	id    string
	audio VoiceAudio
	err   error
}

func (p stubVoiceProvider) ID() string { return p.id }
func (p stubVoiceProvider) Generate(context.Context, string) (VoiceAudio, error) {
	return p.audio, p.err
}

func TestVoiceProviderFallbackKeepsConfiguredOrder(t *testing.T) {
	briefing := NewVoiceBriefing("ffmpeg", []VoiceProviderEntry{
		{Provider: stubVoiceProvider{id: "first", err: errors.New("unavailable")}, Timeout: time.Second},
		{Provider: stubVoiceProvider{id: "second", audio: VoiceAudio{Data: []byte("ID3audio"), Format: VoiceAudioMP3}}, Timeout: time.Second},
	})
	result, err := briefing.Generate(context.Background(), "测试")
	if err != nil {
		t.Fatal(err)
	}
	if result.ProviderID != "second" {
		t.Fatalf("provider = %q", result.ProviderID)
	}
}

func TestVoiceProviderFailureIsBoundedAndIdentified(t *testing.T) {
	briefing := NewVoiceBriefing("ffmpeg", []VoiceProviderEntry{{
		Provider: stubVoiceProvider{id: "broken", err: errors.New(strings.Repeat("x", 300))}, Timeout: time.Second,
	}})
	_, err := briefing.Generate(context.Background(), "测试")
	if err == nil || !strings.Contains(err.Error(), "broken") || len([]rune(err.Error())) > 220 {
		t.Fatalf("unexpected error: %v", err)
	}
}
