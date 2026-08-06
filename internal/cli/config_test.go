package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"
)

func TestConfigStatusRedactsSecretsAndReportsEffectiveFeatures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WECLAW_MIMO_API_KEY", "")
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
	if status.Status != "valid" || status.SchemaVersion != 2 || len(status.WeClaw.ProjectEntries) != 1 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if !status.WeClaw.Reply.Visual || !status.WeClaw.Reply.Progress || status.WeClaw.Reply.Voice {
		t.Fatalf("unexpected reply status: %#v", status.WeClaw.Reply)
	}
	if bytes.Contains(output.Bytes(), []byte("token_sha256")) || bytes.Contains(output.Bytes(), []byte("remote_lock_code")) || bytes.Contains(output.Bytes(), []byte("api_key")) {
		t.Fatalf("configuration status exposed secret fields: %s", output.String())
	}
}
