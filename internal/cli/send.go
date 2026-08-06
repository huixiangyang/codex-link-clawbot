package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/huixiangyang/weclaw/internal/api"
	"github.com/huixiangyang/weclaw/internal/config"
	"github.com/spf13/cobra"
)

var (
	sendTo             string
	sendText           string
	sendMediaURL       string
	sendCaller         string
	sendEndpoint       string
	sendIdempotencyKey string
)

func init() {
	sendCmd.Flags().StringVar(&sendTo, "to", "", "bound owner ID")
	sendCmd.Flags().StringVar(&sendText, "text", "", "message text")
	sendCmd.Flags().StringVar(&sendMediaURL, "media", "", "HTTPS media URL")
	sendCmd.Flags().StringVar(&sendCaller, "caller", "", "configured send API caller ID")
	sendCmd.Flags().StringVar(&sendEndpoint, "endpoint", "", "send API HTTPS or loopback HTTP origin")
	sendCmd.Flags().StringVar(&sendIdempotencyKey, "idempotency-key", "", "stable retry key; generated when omitted")
	_ = sendCmd.MarkFlagRequired("to")
	_ = sendCmd.MarkFlagRequired("caller")
	rootCmd.AddCommand(sendCmd)
}

var sendCmd = &cobra.Command{
	Use:   "send",
	Short: "Send through the authenticated proactive send API",
	Example: `  weclaw send --caller local-cli --to 'owner-id' --text 'Hello'
  weclaw send --caller local-cli --to 'owner-id' --media 'https://example.com/image.png'`,
	RunE: runSend,
}

func runSend(cmd *cobra.Command, _ []string) error {
	if sendText == "" && sendMediaURL == "" {
		return fmt.Errorf("at least one of --text or --media is required")
	}
	token := os.Getenv("WECLAW_SEND_TOKEN")
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return fmt.Errorf("WECLAW_SEND_TOKEN is required and must be a single line")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.SendAPI.Enabled {
		return fmt.Errorf("send API is disabled")
	}
	endpoint := strings.TrimSpace(sendEndpoint)
	if endpoint == "" {
		if cfg.SendAPI.ProxyMode {
			return fmt.Errorf("--endpoint is required when send API proxy mode is enabled")
		}
		endpoint = "http://" + cfg.SendAPI.ListenAddr
	}
	parsedEndpoint, err := validateSendEndpoint(endpoint)
	if err != nil {
		return err
	}
	idempotencyKey := strings.TrimSpace(sendIdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey, err = newSendIdempotencyKey()
		if err != nil {
			return err
		}
	}
	payload := api.SendRequest{CallerID: sendCaller, TargetOwner: sendTo, Text: sendText, MediaURL: sendMediaURL}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, parsedEndpoint.String()+"/api/send", &body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 75 * time.Second,
		},
		Timeout: 90 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("send API request failed; retry only with --idempotency-key %s: %w", idempotencyKey, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<10))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("send API returned HTTP %d; inspect status before retrying with --idempotency-key %s", response.StatusCode, idempotencyKey)
	}
	var result struct {
		Status    string `json:"status"`
		ReceiptID string `json:"receipt_id"`
		Outcome   string `json:"outcome,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return fmt.Errorf("decode send API response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode send API response: trailing data")
	}
	if result.ReceiptID == "" || result.Status != "sent" && result.Status != "duplicate" {
		return fmt.Errorf("send API returned an invalid response")
	}
	if result.Status == "duplicate" {
		if result.Outcome != "reserved" && result.Outcome != "succeeded" && result.Outcome != "failed" {
			return fmt.Errorf("send API returned an invalid duplicate outcome")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Send status: duplicate. Outcome: %s. Receipt: %s. Idempotency key: %s\n", result.Outcome, result.ReceiptID, idempotencyKey)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Send status: sent. Receipt: %s. Idempotency key: %s\n", result.ReceiptID, idempotencyKey)
	return nil
}

func validateSendEndpoint(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("send endpoint must be a plain HTTPS or loopback HTTP origin")
	}
	if parsed.Scheme == "https" {
		return parsed, nil
	}
	if parsed.Scheme != "http" {
		return nil, fmt.Errorf("send endpoint must use HTTPS or loopback HTTP")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsLoopback() || parsed.Port() == "" {
		return nil, fmt.Errorf("plaintext send endpoint must use a loopback IP and explicit port")
	}
	return parsed, nil
}

func newSendIdempotencyKey() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate idempotency key: %w", err)
	}
	return "cli-" + hex.EncodeToString(data), nil
}
