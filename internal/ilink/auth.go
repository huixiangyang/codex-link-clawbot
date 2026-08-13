package ilink

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
)

const (
	qrCodeURL          = "https://ilinkai.weixin.qq.com/ilink/bot/get_bot_qrcode?bot_type=3"
	qrStatusURL        = "https://ilinkai.weixin.qq.com/ilink/bot/get_qrcode_status?qrcode="
	statusWait         = "wait"
	statusScanned      = "scaned"
	statusConfirmed    = "confirmed"
	statusExpired      = "expired"
	credentialsVersion = 1
)

// FetchQRCode retrieves a new QR code for login.
func FetchQRCode(ctx context.Context) (*QRCodeResponse, error) {
	c := NewUnauthenticatedClient()
	var resp QRCodeResponse
	if err := c.doGet(ctx, qrCodeURL, &resp); err != nil {
		return nil, fmt.Errorf("fetch QR code: %w", err)
	}
	return &resp, nil
}

// PollQRStatus polls for QR code scan status until confirmed or expired.
// It calls onStatus for each status change so the caller can display progress.
func PollQRStatus(ctx context.Context, qrcode string, onStatus func(status string)) (*Credentials, error) {
	c := NewUnauthenticatedClient()
	url := qrStatusURL + qrcode

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		pollCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
		var resp QRStatusResponse
		err := c.doGet(pollCtx, url, &resp)
		cancel()

		if err != nil {
			// Timeout is normal for long-poll, retry
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}

		if onStatus != nil {
			onStatus(resp.Status)
		}

		switch resp.Status {
		case statusConfirmed:
			creds := &Credentials{
				Version:     credentialsVersion,
				BotToken:    resp.BotToken,
				ILinkBotID:  resp.ILinkBotID,
				BaseURL:     resp.BaseURL,
				ILinkUserID: resp.ILinkUserID,
			}
			return creds, nil
		case statusExpired:
			return nil, fmt.Errorf("QR code expired")
		case statusWait, statusScanned:
			// Continue polling
		default:
			// Unknown status, continue
		}
	}
}

// AccountsDir returns the directory where account credentials are stored.
func AccountsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex-link-clawbot", "accounts"), nil
}

// NormalizeAccountID converts raw bot ID to filesystem-safe format.
func NormalizeAccountID(raw string) string {
	s := raw
	for _, ch := range []string{"@", ".", ":"} {
		s = filepath.Clean(s)
		s = replaceAll(s, ch, "-")
	}
	return s
}

func replaceAll(s, old, new string) string {
	for {
		i := indexOf(s, old)
		if i < 0 {
			return s
		}
		s = s[:i] + new + s[i+len(old):]
	}
}

func indexOf(s, sub string) int {
	for i := range s {
		if i+len(sub) <= len(s) && s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// SaveCredentials saves credentials to disk under ~/.codex-link-clawbot/accounts/{id}.json.
func SaveCredentials(creds *Credentials) error {
	if creds == nil {
		return fmt.Errorf("credentials are required")
	}
	dir, err := AccountsDir()
	if err != nil {
		return err
	}
	if err := statefile.EnsurePrivateDirectory(dir); err != nil {
		return fmt.Errorf("create accounts dir: %w", err)
	}

	stored := *creds
	stored.Version = credentialsVersion
	if err := validateCredentials(stored); err != nil {
		return err
	}
	id := NormalizeAccountID(stored.ILinkBotID)
	path := filepath.Join(dir, id+".json")
	if err := statefile.WriteJSON(path, stored, statefile.Options{
		Validate: func() error { return validateCredentials(stored) },
	}); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}

// LoadAllCredentials loads all saved account credentials.
func LoadAllCredentials() ([]*Credentials, error) {
	dir, err := AccountsDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read accounts dir: %w", err)
	}

	var result []*Credentials
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sync.json") {
			continue
		}
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		var creds Credentials
		found, err := statefile.ReadJSON(path, &creds, statefile.Options{
			Validate: func() error { return validateCredentials(creds) },
		})
		if err != nil {
			return nil, fmt.Errorf("load credentials %s: %w", e.Name(), err)
		}
		if !found {
			return nil, fmt.Errorf("credentials %s disappeared during load", e.Name())
		}
		if e.Name() != NormalizeAccountID(creds.ILinkBotID)+".json" {
			return nil, fmt.Errorf("credentials filename does not match its bot id")
		}
		result = append(result, &creds)
	}
	return result, nil
}

func validateCredentials(creds Credentials) error {
	if creds.Version != credentialsVersion {
		return fmt.Errorf("invalid credentials version")
	}
	values := []struct {
		name  string
		value string
		max   int
	}{
		{name: "bot_token", value: creds.BotToken, max: 16 << 10},
		{name: "ilink_bot_id", value: creds.ILinkBotID, max: 512},
		{name: "ilink_user_id", value: creds.ILinkUserID, max: 512},
	}
	for _, item := range values {
		if strings.TrimSpace(item.value) == "" || len(item.value) > item.max || strings.ContainsAny(item.value, "\r\n") {
			return fmt.Errorf("invalid credentials %s", item.name)
		}
	}
	baseURL, err := url.Parse(strings.TrimSpace(creds.BaseURL))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return fmt.Errorf("invalid credentials baseurl")
	}
	if baseURL.Scheme != "https" && !(baseURL.Scheme == "http" && isLoopbackCredentialHost(baseURL.Hostname())) {
		return fmt.Errorf("credentials baseurl must use HTTPS")
	}
	return nil
}

func isLoopbackCredentialHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CredentialsPath returns the path for display purposes.
func CredentialsPath() (string, error) {
	return AccountsDir()
}
