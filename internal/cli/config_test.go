package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"
)

func TestConfigStatusRedactsSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_LINK_CLAWBOT_MIMO_API_KEY", "")
	command := &cobra.Command{}
	var output bytes.Buffer
	command.SetOut(&output)

	if err := runConfigStatus(command, nil); err != nil {
		t.Fatal(err)
	}
	var status configurationStatus
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatalf("decode config status: %v", err)
	}
	if status.Status != "valid" || status.SchemaVersion != 5 || len(status.Clawbot.ProjectEntries) != 1 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if !status.Clawbot.Reply.Visual || !status.Clawbot.Reply.Progress || status.Clawbot.Reply.Voice {
		t.Fatalf("unexpected reply status: %#v", status.Clawbot.Reply)
	}
	if bytes.Contains(output.Bytes(), []byte("token_sha256")) || bytes.Contains(output.Bytes(), []byte("remote_lock_code")) || bytes.Contains(output.Bytes(), []byte("api_key")) {
		t.Fatalf("configuration status exposed secret fields: %s", output.String())
	}
}
