package wechat

import (
	"context"
	"log"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/voice"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/access"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/conversation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/delivery"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/runtimecontrol"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/workspace"
)

// Handler processes incoming WeChat messages and dispatches replies.
type Handler struct {
	noticeSending       atomic.Bool
	sessionClient       codex.ThreadClient
	conversations       *conversation.Service
	targets             *target.Store
	projects            *workspace.Manager
	sessions            *thread.Manager
	visual              VisualRenderer
	preferences         *preference.Store
	visualReplyEnabled  bool
	visualReplyMinRunes int
	tasks               *request.Store
	coordinator         *execution.Coordinator
	runner              *execution.Runner
	clients             *clientRegistry
	lifecycle           Lifecycle
	pendingNotices      *delivery.NoticeStore
	remoteLock          *access.RemoteLock
	voice               *voice.Briefing
	managementURL       string
	menus               *numberMenus
	draftMu             sync.Mutex
}

type Lifecycle interface {
	BeginIngress()
	EndIngress()
	Snapshot() runtimecontrol.Snapshot
}

// HandleMessage processes a single incoming message.
func (h *Handler) HandleMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) error {
	// Only process user messages that are finished
	if msg.MessageType != ilink.MessageTypeUser {
		return nil
	}
	if msg.MessageState != ilink.MessageStateFinish {
		return nil
	}
	userLabel := ilink.LogLabel(msg.FromUserID)

	// 只接受扫码绑定账号发来的消息，避免群聊或其他联系人驱动本机 Codex。
	if ownerUserID := client.OwnerUserID(); ownerUserID != "" && msg.FromUserID != ownerUserID {
		log.Printf("[handler] rejected message from non-owner user %s", userLabel)
		return nil
	}
	if h.coordinator != nil {
		h.clients.register(msg.FromUserID, client)
		h.clients.updateContext(msg.FromUserID, msg.ContextToken)

	}

	// Extract text from item list (text message or voice transcription)
	text := extractText(msg)
	if text == "" {
		if voiceText := extractVoiceText(msg); voiceText != "" {
			text = voiceText
			log.Printf("[handler] received voice transcription from %s (chars=%d)", userLabel, len([]rune(text)))
		}
	}
	images := extractImages(msg)
	files := extractFiles(msg)
	if text == "" && len(images) == 0 && len(files) == 0 {
		notice := "暂不支持这类消息，请发送文字、图片或文件。回复 # 打开菜单。"
		for _, item := range msg.ItemList {
			if item.Type == ilink.ItemTypeVoice {
				notice = "未取得语音文字，请重新发送文字或使用微信转文字。回复 # 打开菜单。"
				break
			}
		}
		return h.sendBridgeNotice(ctx, client, msg.FromUserID, notice, msg.ContextToken, NewClientID())
	}

	if len(images) > 0 || len(files) > 0 {
		log.Printf("[handler] received from %s (chars=%d images=%d files=%d)", userLabel, len([]rune(text)), len(images), len(files))
	} else {
		log.Printf("[handler] received from %s (chars=%d)", userLabel, len([]rune(text)))
	}

	clientID := NewClientID()
	if h.remoteLock == nil || !h.remoteLock.IsLocked(msg.FromUserID) {
		if h.coordinator != nil {
			h.coordinator.Effect(func(life context.Context) { h.flushPendingNotices(life, client, msg.FromUserID, msg.ContextToken) })
		} else {
			h.flushPendingNotices(ctx, client, msg.FromUserID, msg.ContextToken)
		}
	}
	if h.tasks != nil {
		if source, err := sourceMessageKey(client, msg); err == nil {
			if h.tasks.WasCleared(source) {
				return h.sendBridgeNotice(ctx, client, msg.FromUserID, request.ErrCleared.Error(), msg.ContextToken, clientID)
			}
			if existing, found := h.tasks.FindBySource(source); found {
				return h.sendBridgeNotice(ctx, client, msg.FromUserID, taskAcknowledgement(existing), msg.ContextToken, clientID)
			}
			if rejection, found := h.tasks.FindRejection(source); found {
				return h.showRejected(ctx, client, msg, rejection)
			}
		}
	}
	if len(images) == 0 && len(files) == 0 && h.menus != nil {
		if handled, err := h.menus.handle(ctx, client, msg, strings.TrimSpace(text)); handled {
			return err
		}
	}
	if h.remoteLock != nil && h.remoteLock.IsLocked(msg.FromUserID) {
		if err := SendTextReply(ctx, client, msg.FromUserID, "入口已锁定。回复 # 打开菜单并解锁。", msg.ContextToken, clientID); err != nil {
			log.Printf("[security] failed to send locked-state reply to %s: %v", userLabel, err)
		}
		return nil
	}
	if h.menus != nil {
		h.menus.leave(msg.FromUserID)
	}
	if handled, err := h.handleDraft(ctx, client, msg, text, images, files); handled {
		return err
	}

	return h.startCodexTask(ctx, client, msg, text, images, files, clientID, "")
}

func extractText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeText && item.TextItem != nil {
			return item.TextItem.Text
		}
	}
	return ""
}

func extractImages(msg ilink.WeixinMessage) []*ilink.ImageItem {
	var images []*ilink.ImageItem
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeImage && item.ImageItem != nil {
			images = append(images, item.ImageItem)
		}
	}
	return images
}

func extractFiles(msg ilink.WeixinMessage) []*ilink.FileItem {
	var files []*ilink.FileItem
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeFile && item.FileItem != nil {
			files = append(files, item.FileItem)
		}
	}
	return files
}

func extractVoiceText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeVoice && item.VoiceItem != nil && item.VoiceItem.Text != "" {
			return item.VoiceItem.Text
		}
	}
	return ""
}
