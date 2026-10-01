package wechat

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
)

type textSendError struct {
	MayBeVisible bool
	Err          error
}

func (err *textSendError) Error() string { return err.Err.Error() }
func (err *textSendError) Unwrap() error { return err.Err }

func outboundMayBeVisible(err error) bool {
	if mediaBatchMayBeVisible(err) {
		return true
	}
	var textErr *textSendError
	return errors.As(err, &textErr) && textErr.MayBeVisible
}

// NewClientID generates a new unique client ID for message correlation.
func NewClientID() string {
	return uuid.New().String()
}

// SendTypingState sends a typing indicator to a user via the iLink sendtyping API.
// It first fetches a typing_ticket via getconfig, then sends the typing status.
func SendTypingState(ctx context.Context, client *ilink.Client, userID, contextToken string) error {
	// Get typing ticket
	configResp, err := client.GetConfig(ctx, userID, contextToken)
	if err != nil {
		return fmt.Errorf("get config for typing: %w", err)
	}
	if configResp.TypingTicket == "" {
		return fmt.Errorf("no typing_ticket returned from getconfig")
	}

	// Send typing
	if err := client.SendTyping(ctx, userID, configResp.TypingTicket, ilink.TypingStatusTyping); err != nil {
		return fmt.Errorf("send typing: %w", err)
	}

	log.Printf("[sender] sent typing indicator to %s", ilink.LogLabel(userID))
	return nil
}

// SendTextReply sends a text reply to a user through the iLink API.
// If clientID is empty, a new one is generated.
func SendTextReply(ctx context.Context, client *ilink.Client, toUserID, text, contextToken, clientID string) error {
	return sendPlainTextReply(ctx, client, toUserID, MarkdownToPlainText(text), contextToken, clientID)
}

// sendPlainTextReply 用于已经排版的数字菜单与文字分页，避免二次 Markdown 转换损坏内容。
func sendPlainTextReply(ctx context.Context, client *ilink.Client, toUserID, plainText, contextToken, clientID string) error {
	if clientID == "" {
		clientID = NewClientID()
	}

	req := &ilink.SendMessageRequest{
		Msg: ilink.SendMsg{
			FromUserID:   client.BotID(),
			ToUserID:     toUserID,
			ClientID:     clientID,
			MessageType:  ilink.MessageTypeBot,
			MessageState: ilink.MessageStateFinish,
			ItemList: []ilink.MessageItem{
				{
					Type: ilink.ItemTypeText,
					TextItem: &ilink.TextItem{
						Text: plainText,
					},
				},
			},
			ContextToken: contextToken,
		},
		BaseInfo: ilink.BaseInfo{},
	}

	resp, err := client.SendMessage(ctx, req)
	if err != nil {
		return &textSendError{MayBeVisible: true, Err: fmt.Errorf("send message: %w", err)}
	}

	if resp.Ret != 0 {
		return &textSendError{Err: fmt.Errorf("send message failed: ret=%d errmsg=%s", resp.Ret, resp.ErrMsg)}
	}

	log.Printf("[sender] sent text reply to %s (chars=%d)", ilink.LogLabel(toUserID), len([]rune(plainText)))
	return nil
}
