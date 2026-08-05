package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/huixiangyang/weclaw/api"
)

func TestRunSendTokenPrintsPlaintextOnceAndOnlyHashesConfig(t *testing.T) {
	oldCaller, oldScopes := sendTokenCaller, sendTokenScopes
	t.Cleanup(func() { sendTokenCaller, sendTokenScopes = oldCaller, oldScopes })
	sendTokenCaller = "dashboard"
	sendTokenScopes = []string{api.ScopeSendText}
	var output bytes.Buffer
	command := *sendTokenCmd
	command.SetOut(&output)
	if err := runSendToken(&command, nil); err != nil {
		t.Fatal(err)
	}
	var generated sendTokenOutput
	if err := json.Unmarshal(output.Bytes(), &generated); err != nil {
		t.Fatal(err)
	}
	if generated.Token == "" || generated.Config.CallerID != "dashboard" {
		t.Fatalf("generated output=%#v", generated)
	}
	if bytes.Count(output.Bytes(), []byte(generated.Token)) != 1 {
		t.Fatal("plaintext token was not shown exactly once")
	}
	digest := sha256.Sum256([]byte(generated.Token))
	if generated.Config.TokenSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("config token hash does not match plaintext")
	}
	configJSON, err := json.Marshal(generated.Config)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(configJSON, []byte(generated.Token)) {
		t.Fatal("plaintext token leaked into config entry")
	}
}
