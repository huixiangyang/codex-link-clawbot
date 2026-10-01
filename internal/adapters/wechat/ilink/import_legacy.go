package ilink

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

func ImportLegacy(destination, source string) error {
	entries, err := os.ReadDir(filepath.Join(source, "accounts"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	known := map[string]string{}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sync.json") {
			continue
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return fmt.Errorf("unexpected account entry")
		}
		var creds Credentials
		found, err := statefile.ReadJSON(filepath.Join(source, "accounts", entry.Name()), &creds, statefile.Options{Validate: func() error { return validateCredentials(creds) }})
		if err != nil {
			return err
		}
		if !found || entry.Name() != NormalizeAccountID(creds.ILinkBotID)+".json" {
			return fmt.Errorf("credential identity mismatch")
		}
		if err := SaveCredentialsRoot(destination, &creds); err != nil {
			return err
		}
		known[strings.TrimSuffix(entry.Name(), ".json")] = creds.ILinkBotID
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sync.json") {
			continue
		}
		botID, ok := known[strings.TrimSuffix(entry.Name(), ".sync.json")]
		if !ok {
			return fmt.Errorf("sync cursor has no credentials")
		}
		var state syncData
		if _, err := statefile.ReadJSON(filepath.Join(source, "accounts", entry.Name()), &state, statefile.Options{Validate: func() error { return validateSyncData(state) }}); err != nil {
			return err
		}
		if err := SaveSyncRoot(destination, botID, state); err != nil {
			return err
		}
	}
	var binding bindingSelection
	if found, err := statefile.ReadJSON(filepath.Join(source, "binding.json"), &binding, statefile.Options{}); err != nil {
		return err
	} else if found {
		if binding.Version != 1 || binding.BotID == "" {
			return fmt.Errorf("invalid binding selection")
		}
		if err := storage.SetSetting(destination, "binding.bot_id", binding.BotID); err != nil {
			return err
		}
	}
	return nil
}
