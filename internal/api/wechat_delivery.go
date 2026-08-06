package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/huixiangyang/weclaw/internal/ilink"
	"github.com/huixiangyang/weclaw/internal/messaging"
)

type WeChatDelivery struct {
	targets map[string]*ilink.Client
}

func NewWeChatDelivery(targets map[string]*ilink.Client) (*WeChatDelivery, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("send API requires at least one bound owner")
	}
	copyTargets := make(map[string]*ilink.Client, len(targets))
	for owner, client := range targets {
		if strings.TrimSpace(owner) == "" || client == nil {
			return nil, fmt.Errorf("send API target is invalid")
		}
		copyTargets[owner] = client
	}
	return &WeChatDelivery{targets: copyTargets}, nil
}

func (d *WeChatDelivery) HasOwner(owner string) bool {
	_, found := d.targets[owner]
	return found
}

func (d *WeChatDelivery) SendText(ctx context.Context, owner, text, deliveryID string) error {
	client, found := d.targets[owner]
	if !found {
		return fmt.Errorf("target owner is unavailable")
	}
	return messaging.SendTextReply(ctx, client, owner, text, "", deliveryID)
}

func (d *WeChatDelivery) SendMedia(ctx context.Context, owner, mediaURL string) error {
	client, found := d.targets[owner]
	if !found {
		return fmt.Errorf("target owner is unavailable")
	}
	return messaging.SendMediaFromPublicURL(ctx, client, owner, mediaURL)
}
