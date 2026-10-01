package ilink

import (
	"fmt"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

type bindingSelection struct {
	Version int    `json:"version"`
	BotID   string `json:"bot_id"`
}

func SelectBinding(root, botID string, accounts []*Credentials) error {
	for _, account := range accounts {
		if account.ILinkBotID == botID || NormalizeAccountID(account.ILinkBotID) == botID {
			return storage.SetSetting(root, "binding.bot_id", account.ILinkBotID)
		}
	}
	return fmt.Errorf("binding does not exist")
}

// ActiveBinding 明确选择一个入口；多账号且没有选择时不启动任何监听器。
func ActiveBinding(root string, accounts []*Credentials) (*Credentials, error) {
	botID, found, err := storage.GetSetting(root, "binding.bot_id")
	if err != nil {
		return nil, err
	}
	if !found && len(accounts) == 1 {
		return accounts[0], nil
	}
	if !found {
		return nil, fmt.Errorf("请先运行 codex-link-clawbot binding list，然后 binding select <bot-id> 选择唯一启用的绑定")
	}
	for _, account := range accounts {
		if account.ILinkBotID == botID {
			return account, nil
		}
	}
	return nil, fmt.Errorf("所选绑定已不存在，请重新运行 binding select <bot-id>")
}
