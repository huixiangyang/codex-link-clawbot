package messaging

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/huixiangyang/weclaw/internal/ilink"
)

const maxPublicMediaBytes = 25 << 20

// SendMediaFromPublicURL 为外部发送 API 提供阻断私网、重绑定和代理继承的媒体下载路径。
func SendMediaFromPublicURL(ctx context.Context, client *ilink.Client, toUserID, mediaURL string) error {
	data, contentType, err := downloadPublicFile(ctx, mediaURL)
	if err != nil {
		return fmt.Errorf("download public media: %w", err)
	}
	return sendMediaBatch(ctx, client, toUserID, "", []outboundMediaPayload{{
		FileName: filenameFromURL(mediaURL), Source: mediaURL, Data: data, ContentType: contentType,
	}})
}

func downloadPublicFile(ctx context.Context, rawURL string) ([]byte, string, error) {
	if err := validatePublicMediaURL(rawURL); err != nil {
		return nil, "", err
	}
	transport := &http.Transport{
		Proxy:                 nil,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		DialContext: func(dialContext context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := net.DefaultResolver.LookupIP(dialContext, "ip", host)
			if err != nil {
				return nil, err
			}
			for _, addressIP := range addresses {
				if !isPublicMediaIP(addressIP) {
					return nil, fmt.Errorf("media host resolves to a non-public address")
				}
			}
			if len(addresses) == 0 {
				return nil, fmt.Errorf("media host has no address")
			}
			return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(dialContext, network, net.JoinHostPort(addresses[0].String(), port))
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many media redirects")
			}
			return validatePublicMediaURL(request.URL.String())
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("media host returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxPublicMediaBytes {
		return nil, "", fmt.Errorf("media exceeds size limit")
	}
	data, err := readAllLimited(response.Body, maxPublicMediaBytes)
	if err != nil {
		return nil, "", err
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = inferContentType(rawURL)
	}
	return data, contentType, nil
}

func validatePublicMediaURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("media URL must be public HTTPS without credentials or fragment")
	}
	return nil
}

func isPublicMediaIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}
