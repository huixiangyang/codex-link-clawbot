package messaging

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/huixiangyang/weclaw/internal/ilink"
	"github.com/huixiangyang/weclaw/internal/taskqueue"
)

// prepareQueuedInput 在确认入队前完成所有微信附件的下载与内容校验。
// 返回值只在内存中短暂存在，随后由 taskqueue 原子写入私有任务目录。
func prepareQueuedInput(ctx context.Context, text string, images []*ilink.ImageItem, files []*ilink.FileItem) (string, []taskqueue.InputAttachment, []taskqueue.InputAttachment, error) {
	return prepareQueuedInputWithDownloaders(ctx, text, images, files, inboundDownloaders{
		image: downloadInboundImage,
		file:  downloadInboundFile,
	})
}

func prepareQueuedInputWithDownloaders(ctx context.Context, text string, images []*ilink.ImageItem, files []*ilink.FileItem, downloaders inboundDownloaders) (string, []taskqueue.InputAttachment, []taskqueue.InputAttachment, error) {
	text = strings.TrimSpace(text)
	if len(images) > maxInboundImages {
		return "", nil, nil, fmt.Errorf("单条消息最多支持 %d 张图片", maxInboundImages)
	}
	if len(files) > maxInboundFiles {
		return "", nil, nil, fmt.Errorf("单条消息最多支持 %d 个文件", maxInboundFiles)
	}
	if text == "" {
		switch {
		case len(images) > 0 && len(files) > 0:
			text = defaultFilePrompt + "同时结合随附图片中的信息。"
		case len(images) > 0:
			text = defaultImagePrompt
		case len(files) > 0:
			text = defaultFilePrompt
		}
	}

	queuedImages := make([]taskqueue.InputAttachment, 0, len(images))
	queuedFiles := make([]taskqueue.InputAttachment, 0, len(files))
	var totalBytes int64
	for index, image := range images {
		data, err := downloaders.image(ctx, image)
		if err != nil {
			return "", nil, nil, fmt.Errorf("接收第 %d 张图片: %w", index+1, err)
		}
		totalBytes += int64(len(data))
		if totalBytes > maxInboundTotalBytes {
			return "", nil, nil, fmt.Errorf("单条消息的附件总大小超过 100 MiB")
		}
		extension, err := validatedImageExtension(data)
		if err != nil {
			return "", nil, nil, fmt.Errorf("校验第 %d 张图片: %w", index+1, err)
		}
		queuedImages = append(queuedImages, taskqueue.InputAttachment{
			Name:        fmt.Sprintf("image-%02d%s", index+1, extension),
			ContentType: http.DetectContentType(data),
			Data:        data,
		})
	}

	for index, file := range files {
		if err := validateInboundFileMetadata(file); err != nil {
			return "", nil, nil, fmt.Errorf("校验第 %d 个文件: %w", index+1, err)
		}
		data, err := downloaders.file(ctx, file)
		if err != nil {
			return "", nil, nil, fmt.Errorf("接收第 %d 个文件: %w", index+1, err)
		}
		totalBytes += int64(len(data))
		if totalBytes > maxInboundTotalBytes {
			return "", nil, nil, fmt.Errorf("单条消息的附件总大小超过 100 MiB")
		}
		name, contentType, err := validateInboundFile(file.FileName, data)
		if err != nil {
			return "", nil, nil, fmt.Errorf("校验第 %d 个文件: %w", index+1, err)
		}
		queuedFiles = append(queuedFiles, taskqueue.InputAttachment{
			Name: name, ContentType: contentType, Data: data,
		})
	}
	return text, queuedImages, queuedFiles, nil
}
