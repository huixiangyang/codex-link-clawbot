package messaging

import (
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/huixiangyang/weclaw/ilink"
)

// reMarkdownImage matches markdown image syntax: ![alt](url)
var reMarkdownImage = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)

// ExtractImageURLs extracts image URLs from markdown text.
func ExtractImageURLs(text string) []string {
	matches := reMarkdownImage.FindAllStringSubmatch(text, -1)
	var urls []string
	for _, m := range matches {
		url := strings.TrimSpace(m[1])
		if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
			urls = append(urls, url)
		}
	}
	return urls
}

// SendMediaFromURL downloads a file from a URL and sends it as a media message.
func SendMediaFromURL(ctx context.Context, client *ilink.Client, toUserID, mediaURL, contextToken string) error {
	data, contentType, err := downloadFile(ctx, mediaURL)
	if err != nil {
		return fmt.Errorf("download %s: %w", mediaURL, err)
	}

	return sendMediaBatch(ctx, client, toUserID, contextToken, []outboundMediaPayload{{
		FileName: filenameFromURL(mediaURL), Source: mediaURL, Data: data, ContentType: contentType,
	}})
}

// SendMediaFromPath reads a local file and sends it as a media message.
func SendMediaFromPath(ctx context.Context, client *ilink.Client, toUserID, path, contextToken string) error {
	payload, err := outboundMediaFromPath(path)
	if err != nil {
		return err
	}
	return sendMediaBatch(ctx, client, toUserID, contextToken, []outboundMediaPayload{payload})
}

func outboundMediaFromPath(path string) (outboundMediaPayload, error) {
	file, err := os.Open(path)
	if err != nil {
		return outboundMediaPayload{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	data, err := readAllLimited(file, maxOutboundArtifactBytes)
	if err != nil {
		return outboundMediaPayload{}, fmt.Errorf("read %s: %w", path, err)
	}
	return outboundMediaPayload{
		FileName: filepath.Base(path), Source: path, Data: data, ContentType: inferContentType(path),
	}, nil
}

func sendMediaData(ctx context.Context, client *ilink.Client, toUserID, fileName, source string, data []byte, contentType, contextToken string) error {
	return sendMediaBatch(ctx, client, toUserID, contextToken, []outboundMediaPayload{{
		FileName: fileName, Source: source, Data: data, ContentType: contentType,
	}})
}

type outboundMediaPayload struct {
	FileName    string
	Source      string
	Data        []byte
	ContentType string
}

type stagedMediaMessage struct {
	ContentType string
	Item        ilink.MessageItem
}

// sendMediaBatch 先把整批媒体上传到 CDN，再按声明顺序发送，避免后项上传失败时留下孤立的前项消息。
func sendMediaBatch(ctx context.Context, client *ilink.Client, toUserID, contextToken string, payloads []outboundMediaPayload) error {
	if len(payloads) == 0 {
		return fmt.Errorf("media batch must not be empty")
	}
	staged := make([]stagedMediaMessage, 0, len(payloads))
	for index, payload := range payloads {
		message, err := stageMediaMessage(ctx, client, toUserID, payload)
		if err != nil {
			return fmt.Errorf("stage media %d/%d: %w", index+1, len(payloads), err)
		}
		staged = append(staged, message)
	}
	for index, message := range staged {
		if err := sendStagedMediaMessage(ctx, client, toUserID, contextToken, message); err != nil {
			return fmt.Errorf("send media %d/%d: %w", index+1, len(staged), err)
		}
	}
	return nil
}

func stageMediaMessage(ctx context.Context, client *ilink.Client, toUserID string, payload outboundMediaPayload) (stagedMediaMessage, error) {
	fileName := payload.FileName
	if fileName == "" {
		fileName = "file"
	}

	cdnMediaType, itemType := classifyMedia(payload.ContentType, payload.Source)

	log.Printf("[media] staging %s (%d bytes) for %s", payload.ContentType, len(payload.Data), ilink.LogLabel(toUserID))

	uploaded, err := UploadFileToCDN(ctx, client, payload.Data, toUserID, cdnMediaType)
	if err != nil {
		return stagedMediaMessage{}, fmt.Errorf("upload to CDN: %w", err)
	}

	media := &ilink.MediaInfo{
		EncryptQueryParam: uploaded.DownloadParam,
		AESKey:            AESKeyToBase64(uploaded.AESKeyHex),
		EncryptType:       1,
	}

	var item ilink.MessageItem
	switch itemType {
	case ilink.ItemTypeImage:
		item = ilink.MessageItem{
			Type: ilink.ItemTypeImage,
			ImageItem: &ilink.ImageItem{
				Media:   media,
				MidSize: uploaded.CipherSize,
			},
		}
	case ilink.ItemTypeVideo:
		item = ilink.MessageItem{
			Type: ilink.ItemTypeVideo,
			VideoItem: &ilink.VideoItem{
				Media:     media,
				VideoSize: uploaded.CipherSize,
			},
		}
	default:
		item = ilink.MessageItem{
			Type: ilink.ItemTypeFile,
			FileItem: &ilink.FileItem{
				Media:    media,
				FileName: fileName,
				Len:      fmt.Sprintf("%d", uploaded.FileSize),
			},
		}
	}
	return stagedMediaMessage{ContentType: payload.ContentType, Item: item}, nil
}

func sendStagedMediaMessage(ctx context.Context, client *ilink.Client, toUserID, contextToken string, staged stagedMediaMessage) error {
	req := &ilink.SendMessageRequest{
		Msg: ilink.SendMsg{
			FromUserID:   client.BotID(),
			ToUserID:     toUserID,
			ClientID:     NewClientID(),
			MessageType:  ilink.MessageTypeBot,
			MessageState: ilink.MessageStateFinish,
			ItemList:     []ilink.MessageItem{staged.Item},
			ContextToken: contextToken,
		},
		BaseInfo: ilink.BaseInfo{},
	}

	resp, err := client.SendMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("send staged media message: %w", err)
	}
	if resp.Ret != 0 {
		return fmt.Errorf("send staged media failed: ret=%d errmsg=%s", resp.Ret, resp.ErrMsg)
	}

	log.Printf("[media] sent %s to %s", staged.ContentType, ilink.LogLabel(toUserID))
	return nil
}

func downloadFile(ctx context.Context, url string) ([]byte, string, error) {
	return downloadFileLimited(ctx, url, 100<<20)
}

func downloadFileLimited(ctx context.Context, url string, maxBytes int64) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := readAllLimited(resp.Body, maxBytes)
	if err != nil {
		return nil, "", err
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = inferContentType(url)
	}

	return data, contentType, nil
}

func readAllLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("maximum download size must be positive")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file exceeds %d byte limit", maxBytes)
	}
	return data, nil
}

func classifyMedia(contentType, url string) (cdnMediaType int, itemType int) {
	ct := strings.ToLower(contentType)

	if strings.HasPrefix(ct, "image/") || isImageExt(url) {
		return ilink.CDNMediaTypeImage, ilink.ItemTypeImage
	}
	if strings.HasPrefix(ct, "video/") || isVideoExt(url) {
		return ilink.CDNMediaTypeVideo, ilink.ItemTypeVideo
	}
	return ilink.CDNMediaTypeFile, ilink.ItemTypeFile
}

func isImageExt(url string) bool {
	ext := strings.ToLower(filepath.Ext(stripQuery(url)))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

func isVideoExt(url string) bool {
	ext := strings.ToLower(filepath.Ext(stripQuery(url)))
	switch ext {
	case ".mp4", ".mov", ".webm", ".mkv", ".avi":
		return true
	}
	return false
}

func inferContentType(url string) string {
	ext := filepath.Ext(stripQuery(url))
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

func filenameFromURL(rawURL string) string {
	u := stripQuery(rawURL)
	name := filepath.Base(u)
	if name == "" || name == "." || name == "/" {
		return "file"
	}
	return name
}

func stripQuery(rawURL string) string {
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		return rawURL[:i]
	}
	return rawURL
}
