package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/huixiangyang/weclaw/internal/api"
	"github.com/huixiangyang/weclaw/internal/config"
	"github.com/spf13/cobra"
)

var (
	sendTokenCaller string
	sendTokenScopes []string
)

var sendTokenCallerPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func init() {
	sendTokenCmd.Flags().StringVar(&sendTokenCaller, "caller", "", "stable caller ID")
	sendTokenCmd.Flags().StringSliceVar(&sendTokenScopes, "scope", []string{api.ScopeSendText, api.ScopeSendMedia}, "send:text and/or send:media")
	_ = sendTokenCmd.MarkFlagRequired("caller")
	rootCmd.AddCommand(sendTokenCmd)
}

var sendTokenCmd = &cobra.Command{
	Use:   "send-token",
	Short: "Generate an offline proactive-send token",
	Args:  cobra.NoArgs,
	RunE:  runSendToken,
}

type sendTokenOutput struct {
	Token  string                    `json:"token"`
	Config config.SendAPITokenConfig `json:"config"`
}

func runSendToken(cmd *cobra.Command, _ []string) error {
	if !sendTokenCallerPattern.MatchString(sendTokenCaller) {
		return fmt.Errorf("caller must start with a letter and contain only lowercase letters, digits, underscore, or hyphen")
	}
	seen := make(map[string]bool, len(sendTokenScopes))
	for _, scope := range sendTokenScopes {
		if scope != api.ScopeSendText && scope != api.ScopeSendMedia || seen[scope] {
			return fmt.Errorf("scope must contain unique send:text and/or send:media values")
		}
		seen[scope] = true
	}
	if len(seen) == 0 {
		return fmt.Errorf("at least one scope is required")
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return fmt.Errorf("generate send token: %w", err)
	}
	plaintext := base64.RawURLEncoding.EncodeToString(random)
	digest := sha256.Sum256([]byte(plaintext))
	output := sendTokenOutput{
		Token: plaintext,
		Config: config.SendAPITokenConfig{
			CallerID: sendTokenCaller, TokenSHA256: hex.EncodeToString(digest[:]), Scopes: append([]string(nil), sendTokenScopes...),
		},
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}
