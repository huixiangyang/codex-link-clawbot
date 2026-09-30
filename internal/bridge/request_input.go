package bridge

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
)

// incomingAttachments 只在私有请求输入中保存，下载在已登记且可取消的执行内发生。
type incomingAttachments struct {
	Images []*ilink.ImageItem `json:"images"`
	Files  []*ilink.FileItem  `json:"files"`
}

// prepareRequestInput 在确认执行前完成所有微信附件的下载与内容校验。
func prepareRequestInput(ctx context.Context, text string, images []*ilink.ImageItem, files []*ilink.FileItem) (string, []request.InputAttachment, []request.InputAttachment, error) {
	return prepareRequestInputWithDownloaders(ctx, text, images, files, inboundDownloaders{
		image: downloadInboundImage,
		file:  downloadInboundFile,
	})
}

func prepareRequestInputWithDownloaders(ctx context.Context, text string, images []*ilink.ImageItem, files []*ilink.FileItem, downloaders inboundDownloaders) (string, []request.InputAttachment, []request.InputAttachment, error) {
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

	inputImages := make([]request.InputAttachment, 0, len(images))
	inputFiles := make([]request.InputAttachment, 0, len(files))
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
		inputImages = append(inputImages, request.InputAttachment{
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
		inputFiles = append(inputFiles, request.InputAttachment{
			Name: name, ContentType: contentType, Data: data,
		})
	}
	return text, inputImages, inputFiles, nil
}
