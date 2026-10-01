package wechat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request/attachmentref"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

const draftLifetime = 30 * time.Minute

type conversationDraft struct {
	Target       target.Intent       `json:"target"`
	Text         string              `json:"text"`
	Attachments  incomingAttachments `json:"attachments"`
	ExpiresAt    int64               `json:"expires_at"`
	SubmitSource string              `json:"submit_source,omitempty"`
}

type draftState struct {
	Version  int                          `json:"version"`
	Owners   map[string]conversationDraft `json:"owners"`
	Receipts map[string]int64             `json:"receipts"`
}

func (h *Handler) loadDrafts() (draftState, error) {
	s := draftState{Version: 1, Owners: map[string]conversationDraft{}, Receipts: map[string]int64{}}
	err := storage.View(h.tasks.StateRoot(), func(tx *sql.Tx) error {
		rows, err := storage.Rows[draftRow](tx, "SELECT * FROM drafts")
		if err != nil {
			return err
		}
		for _, r := range rows {
			draft := conversationDraft{Target: target.Intent{ID: r.TargetID, WorkspaceID: r.WorkspaceID, ThreadID: r.ThreadID}, Text: r.Text, ExpiresAt: r.ExpiresAt, SubmitSource: r.SubmitSource}
			data, err := attachmentref.Load(tx, "draft", r.OwnerID)
			if err != nil {
				return err
			}
			if len(data) > 0 {
				if err := json.Unmarshal(data, &draft.Attachments); err != nil {
					return err
				}
			}
			s.Owners[r.OwnerID] = draft
		}
		receipts, err := storage.Rows[draftReceiptRow](tx, "SELECT * FROM draft_receipts")
		if err != nil {
			return err
		}
		for _, r := range receipts {
			s.Receipts[r.Source] = r.At
		}
		return nil
	})
	return s, err
}

func (h *Handler) saveDrafts(s draftState) error {
	return saveDraftState(h.tasks.StateRoot(), s)
}

type draftRow struct {
	OwnerID      string `json:"owner_id"`
	TargetID     string `json:"target_id"`
	WorkspaceID  string `json:"workspace_id"`
	ThreadID     string `json:"thread_id"`
	Text         string
	ExpiresAt    int64  `json:"expires_at"`
	SubmitSource string `json:"submit_source"`
}
type draftReceiptRow struct {
	Source string
	At     int64
}

func saveDraftState(root string, s draftState) error {
	if s.Version != 1 || s.Owners == nil || s.Receipts == nil {
		return fmt.Errorf("invalid draft state")
	}
	return storage.Update(root, func(tx *sql.Tx) error {
		for _, table := range []string{"drafts", "draft_receipts"} {
			if _, err := tx.Exec("DELETE FROM " + table); err != nil {
				return err
			}
		}
		for owner, d := range s.Owners {
			if err := storage.Put(tx, "drafts", draftRow{owner, d.Target.ID, d.Target.WorkspaceID, d.Target.ThreadID, d.Text, d.ExpiresAt, d.SubmitSource}); err != nil {
				return err
			}
			data, err := json.Marshal(d.Attachments)
			if err != nil {
				return err
			}
			if err := attachmentref.Save(tx, "draft", owner, data); err != nil {
				return err
			}
		}
		for source, at := range s.Receipts {
			if err := storage.Put(tx, "draft_receipts", draftReceiptRow{source, at}); err != nil {
				return err
			}
		}
		return nil
	})
}

// 草稿只保存输入和附件引用，不占用执行槽，也不会在重启或空闲后自动提交。
func (h *Handler) pruneDrafts(s *draftState) bool {
	changed := false
	now := time.Now().Unix()
	for owner, draft := range s.Owners {
		_, submitted := h.tasks.FindBySource(draft.SubmitSource)
		if draft.ExpiresAt <= now || draft.SubmitSource != "" && (submitted || h.tasks.WasCleared(draft.SubmitSource)) {
			delete(s.Owners, owner)
			changed = true
		}
	}
	for source, at := range s.Receipts {
		if at <= time.Now().Add(-30*24*time.Hour).Unix() {
			delete(s.Receipts, source)
			changed = true
		}
	}
	return changed
}

func (h *Handler) expireDrafts() error {
	h.draftMu.Lock()
	defer h.draftMu.Unlock()
	s, err := h.loadDrafts()
	if err != nil {
		return err
	}
	if h.pruneDrafts(&s) {
		return h.saveDrafts(s)
	}
	return nil
}

func (h *Handler) handleDraft(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text string, images []*ilink.ImageItem, files []*ilink.FileItem) (bool, error) {
	if h.tasks == nil || h.targets == nil {
		return false, nil
	}
	h.draftMu.Lock()
	defer h.draftMu.Unlock()
	reply := func(body string) (bool, error) {
		return true, SendTextReply(ctx, client, msg.FromUserID, body, msg.ContextToken, NewClientID())
	}
	s, err := h.loadDrafts()
	if err != nil {
		return true, err
	}
	if h.pruneDrafts(&s) {
		if err := h.saveDrafts(s); err != nil {
			return true, err
		}
	}
	draft, exists := s.Owners[msg.FromUserID]
	text = strings.TrimSpace(text)
	command := len(images) == 0 && len(files) == 0
	source, err := sourceMessageKey(client, msg)
	if err != nil {
		return reply("未保存输入：消息缺少来源编号，请重新发送。")
	}
	if _, duplicate := s.Receipts[source]; duplicate {
		return reply("这条草稿输入已处理，不会重复添加或执行。发送“草稿”查看当前内容。")
	}
	if !exists && len(images) == 0 && len(files) == 0 && text != "提交" && text != "丢弃草稿" && text != "草稿" {
		return false, nil
	}
	if command && text == "丢弃草稿" {
		delete(s.Owners, msg.FromUserID)
		s.Receipts[source] = time.Now().Unix()
		if err := h.saveDrafts(s); err != nil {
			return true, err
		}
		return reply("草稿已丢弃，没有执行任何工作。")
	}
	if command && (text == "草稿" || text == "提交") && !exists {
		return reply("没有待提交草稿，草稿可能已提交或超过 30 分钟失效。请重新发送附件和说明。")
	}
	if command && text == "草稿" {
		return reply(h.draftSummary(draft))
	}
	current, err := h.targets.Capture(msg.FromUserID)
	if err != nil {
		return true, err
	}
	if exists && current.ID != draft.Target.ID {
		return reply("草稿仍绑定原会话，未发送到当前会话。请切回原会话后提交，或发送“丢弃草稿”。\n" + h.draftSummary(draft))
	}
	if command && text == "提交" {
		if draft.Text == "" {
			return reply("请先发送这批附件的处理要求，再发送“提交”。附件不会自动执行。")
		}
		// 先保存提交来源；执行已登记但清理前崩溃时，可据此收敛，防止重复提交。
		draft.SubmitSource = source
		s.Owners[msg.FromUserID] = draft
		if err := h.saveDrafts(s); err != nil {
			return true, err
		}
		err := h.startCodexTask(ctx, client, msg, draft.Text, draft.Attachments.Images, draft.Attachments.Files, NewClientID(), draft.Target.ID)
		if _, accepted := h.tasks.FindBySource(source); accepted {
			delete(s.Owners, msg.FromUserID)
			s.Receipts[source] = time.Now().Unix()
			if saveErr := h.saveDrafts(s); saveErr != nil {
				return true, saveErr
			}
		}
		return true, err
	}
	for _, file := range files {
		if err := validateInboundFileMetadata(file); err != nil {
			return reply("附件未加入草稿：" + err.Error())
		}
		if supportedInboundExtension(file.FileName) == "" {
			return reply("不支持文件格式 " + filepath.Ext(file.FileName) + "。请转为 PDF、文本或代码文件；Office 文档请先导出为 PDF。该文件未加入草稿，不要重复发送同一格式。")
		}
	}
	for _, image := range images {
		if image == nil || int64(image.MidSize) > maxInboundImageBytes+16 {
			return reply("图片未加入草稿：内容为空或超过 20 MiB，请压缩后重新发送。")
		}
	}
	if len(draft.Attachments.Images)+len(images) > maxInboundImages || len(draft.Attachments.Files)+len(files) > maxInboundFiles {
		return reply("草稿最多容纳 4 张图片和 8 个文件，本条附件未加入。请先提交或丢弃已有草稿。")
	}
	if !exists {
		draft.Target = current
	}
	if text != "" {
		if len(draft.Text)+len(text)+2 > 1<<20 {
			return reply("草稿说明过长，本条未加入。请精简后重新发送。")
		}
		draft.Text = strings.TrimSpace(draft.Text + "\n\n" + text)
	}
	draft.Attachments.Images = append(draft.Attachments.Images, images...)
	draft.Attachments.Files = append(draft.Attachments.Files, files...)
	encoded, err := json.Marshal(draft.Attachments)
	if err != nil || len(encoded) > 128<<10 {
		return reply("附件引用过大，本条未加入草稿。请减少附件后重新发送。")
	}
	draft.ExpiresAt = time.Now().Add(draftLifetime).Unix()
	s.Owners[msg.FromUserID] = draft
	s.Receipts[source] = time.Now().Unix()
	if err := h.saveDrafts(s); err != nil {
		return true, err
	}
	return reply(h.draftSummary(draft))
}

func (h *Handler) draftSummary(draft conversationDraft) string {
	definition, _ := h.projects.Get(draft.Target.WorkspaceID)
	threadLabel := draft.Target.ThreadID
	if threadLabel == "" {
		threadLabel = "待建立的新会话"
	}
	return fmt.Sprintf("草稿已暂存，尚未执行\n%s · %s\n%d 张图片 · %d 个文件\n说明：%s\n\n可继续发送附件或说明。发送“提交”开始；“丢弃草稿”清空。30 分钟未补充将失效。", definition.Name, threadLabel, len(draft.Attachments.Images), len(draft.Attachments.Files), presentation.NormalizeLine(draft.Text, 160))
}
