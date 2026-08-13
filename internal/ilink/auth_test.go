package ilink

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialsUseStrictVersionedPrivateState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	credentials := &Credentials{
		BotToken: "secret-token", ILinkBotID: "bot@example", BaseURL: "https://ilinkai.weixin.qq.com", ILinkUserID: "owner@example",
	}
	if err := SaveCredentials(credentials); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".codex-link-clawbot", "accounts", NormalizeAccountID(credentials.ILinkBotID)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": 1`) {
		t.Fatalf("credentials are not versioned: %s", data)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %v, err=%v", info, err)
	}
	loaded, err := LoadAllCredentials()
	if err != nil || len(loaded) != 1 || loaded[0].Version != credentialsVersion || loaded[0].BotToken != credentials.BotToken {
		t.Fatalf("loaded credentials = %#v, err=%v", loaded, err)
	}
}

func TestCredentialsRejectUnknownFieldsAndWrongFilename(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	directory := filepath.Join(home, ".codex-link-clawbot", "accounts")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	unknown := `{"version":1,"bot_token":"secret","ilink_bot_id":"bot","baseurl":"https://ilinkai.weixin.qq.com","ilink_user_id":"owner","extra":true}`
	if err := os.WriteFile(filepath.Join(directory, "bot.json"), []byte(unknown), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAllCredentials(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown credential field error = %v", err)
	}
	valid := `{"version":1,"bot_token":"secret","ilink_bot_id":"different","baseurl":"https://ilinkai.weixin.qq.com","ilink_user_id":"owner"}`
	if err := os.WriteFile(filepath.Join(directory, "bot.json"), []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAllCredentials(); err == nil || !strings.Contains(err.Error(), "filename") {
		t.Fatalf("credential filename error = %v", err)
	}
}
