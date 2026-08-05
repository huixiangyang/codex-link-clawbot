package messaging

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/huixiangyang/weclaw/ilink"
)

// VoiceBriefing 调用管理员显式配置的本机 TTS 包装器。
// 命令契约固定为：command <text> <output.mp3>。
type VoiceBriefing struct {
	command string
}

func NewVoiceBriefing(command string) *VoiceBriefing {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	return &VoiceBriefing{command: command}
}

func (v *VoiceBriefing) Generate(ctx context.Context, text string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "weclaw-voice-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := os.Chmod(dir, 0o700); err != nil {
		cleanup()
		return "", func() {}, err
	}
	output := filepath.Join(dir, "briefing.mp3")
	commandCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if raw, err := exec.CommandContext(commandCtx, v.command, text, output).CombinedOutput(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("TTS command failed: %s", normalizeSessionLine(string(raw), 160))
	}
	info, err := os.Stat(output)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 20<<20 {
		cleanup()
		return "", func() {}, fmt.Errorf("TTS command did not create a valid MP3")
	}
	if err := os.Chmod(output, 0o600); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return output, cleanup, nil
}

func SendVoiceFromPath(ctx context.Context, client *ilink.Client, userID, path, contextToken string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	uploaded, err := UploadFileToCDN(ctx, client, data, userID, ilink.CDNMediaTypeFile)
	if err != nil {
		return fmt.Errorf("upload voice: %w", err)
	}
	item := ilink.MessageItem{Type: ilink.ItemTypeVoice, VoiceItem: &ilink.VoiceItem{
		Media:     &ilink.MediaInfo{EncryptQueryParam: uploaded.DownloadParam, AESKey: AESKeyToBase64(uploaded.AESKeyHex), EncryptType: 1},
		VoiceSize: uploaded.CipherSize, EncodeType: 7,
	}}
	response, err := client.SendMessage(ctx, &ilink.SendMessageRequest{
		Msg: ilink.SendMsg{
			FromUserID: client.BotID(), ToUserID: userID, ClientID: NewClientID(),
			MessageType: ilink.MessageTypeBot, MessageState: ilink.MessageStateFinish,
			ItemList: []ilink.MessageItem{item}, ContextToken: contextToken,
		},
	})
	if err != nil {
		return err
	}
	if response.Ret != 0 {
		return fmt.Errorf("send voice failed: ret=%d errmsg=%s", response.Ret, response.ErrMsg)
	}
	return nil
}

func (h *Handler) requestVoiceBriefing(userID string) string {
	if h.voice == nil {
		return "语音简报未启用。需要配置 voice.command 本机 TTS 包装器。"
	}
	h.controlVoice.Store(userID, true)
	return "语音简报已生成并发送。"
}

func (h *Handler) sendVoiceBriefing(ctx context.Context, client *ilink.Client, userID, contextToken string) error {
	projectName := "未配置项目"
	if h.projects != nil {
		projectName = h.projects.Current(userID).Name
	}
	parts := []string{"WeClaw 工作简报。当前项目：" + projectName + "。"}
	if h.activities == nil || len(h.activities.List(userID)) == 0 {
		parts = append(parts, "目前还没有任务记录。")
	} else {
		records := h.activities.List(userID)
		if len(records) > 3 {
			records = records[:3]
		}
		parts = append(parts, fmt.Sprintf("最近有 %d 项任务。", len(records)))
		for index, record := range records {
			parts = append(parts, fmt.Sprintf("第 %d 项，%s，状态%s。", index+1, record.Summary, formatActivityStatus(record.Status)))
		}
	}
	path, cleanup, err := h.voice.Generate(ctx, strings.Join(parts, ""))
	if err != nil {
		return err
	}
	defer cleanup()
	return SendVoiceFromPath(ctx, client, userID, path, contextToken)
}
