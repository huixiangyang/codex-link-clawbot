package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/app"
	"github.com/huixiangyang/codex-link-clawbot/internal/app/config"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/logging"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
)

var startDrainingFlag bool

func init() {
	startCmd.Flags().BoolVar(&startDrainingFlag, "draining", false, "start without accepting new work")
	_ = startCmd.Flags().MarkHidden("draining")
	rootCmd.AddCommand(startCmd)
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the WeChat message bridge (auto-login if needed)",
	RunE:  runStart,
}

func runStart(_ *cobra.Command, _ []string) (runErr error) {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	stateRoot, err := statefile.DefaultRoot()
	if err != nil {
		return fmt.Errorf("resolve state root: %w", err)
	}
	lease, err := statefile.Acquire(stateRoot, statefile.LeaseRuntime)
	if err != nil {
		return fmt.Errorf("acquire runtime state lease: %w", err)
	}
	defer func() {
		if closeErr := lease.Close(); closeErr != nil {
			log.Printf("release runtime state lease: %v", closeErr)
		}
	}()
	writer, err := logging.Open(stateRoot, os.Stderr)
	if err != nil {
		return fmt.Errorf("initialize service log: %w", err)
	}
	previousOutput, previousFlags, previousPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(writer)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC)
	log.SetPrefix(fmt.Sprintf("[pid=%d] ", os.Getpid()))
	defer func() {
		if runErr != nil {
			log.Printf("[service] stopped with error: %v", runErr)
		} else {
			log.Print("[service] stopped")
		}
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
		runErr = writer.SanitizeError(errors.Join(runErr, writer.Close()))
	}()
	log.Printf("[service] starting version=%s", Version)

	accounts, err := ilink.LoadAllCredentials()
	if err != nil {
		return fmt.Errorf("failed to load credentials: %w", err)
	}
	if len(accounts) == 0 {
		log.Println("No WeChat accounts found, starting login...")
		credentials, loginErr := doLogin(ctx)
		if loginErr != nil {
			return fmt.Errorf("login failed: %w", loginErr)
		}
		accounts = append(accounts, credentials)
	}
	for _, account := range accounts {
		writer.Protect(account.BotToken)
	}
	account, err := ilink.ActiveBinding(stateRoot, accounts)
	if err != nil {
		return err
	}
	accounts = []*ilink.Credentials{account}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	writer.Protect(cfg.Clawbot.Security.RemoteLockCode)
	for _, provider := range cfg.Clawbot.Reply.Voice.Providers {
		if provider.MiMo != nil {
			writer.Protect(provider.MiMo.APIKey)
		}
	}
	for key, value := range cfg.Codex.Env {
		if sensitiveEnvironmentKey(key) {
			writer.Protect(value)
		}
	}
	for _, item := range os.Environ() {
		key, value, _ := strings.Cut(item, "=")
		if sensitiveEnvironmentKey(key) {
			writer.Protect(value)
		}
	}
	return app.Run(ctx, cfg, accounts, app.Options{Version: Version, StateRoot: stateRoot, Draining: startDrainingFlag})
}

func sensitiveEnvironmentKey(key string) bool {
	key = strings.ToUpper(key)
	return strings.Contains(key, "TOKEN") || strings.Contains(key, "KEY") || strings.Contains(key, "SECRET") || strings.Contains(key, "PASSWORD")
}

// doLogin 运行交互式二维码登录，只负责 CLI 输入输出。
func doLogin(ctx context.Context) (*ilink.Credentials, error) {
	fmt.Println("Fetching QR code...")
	qr, err := ilink.FetchQRCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch QR code: %w", err)
	}

	fmt.Println("\nScan this QR code with WeChat:")
	fmt.Println()
	qrterminal.GenerateWithConfig(qr.QRCodeImgContent, qrterminal.Config{
		Level: qrterminal.L, Writer: os.Stdout, HalfBlocks: true,
		BlackChar: qrterminal.BLACK_BLACK, WhiteBlackChar: qrterminal.WHITE_BLACK,
		WhiteChar: qrterminal.WHITE_WHITE, BlackWhiteChar: qrterminal.BLACK_WHITE, QuietZone: 1,
	})
	fmt.Printf("\nQR URL: %s\n", qr.QRCodeImgContent)
	fmt.Println("\nWaiting for scan...")

	lastStatus := ""
	credentials, err := ilink.PollQRStatus(ctx, qr.QRCode, func(status string) {
		if status == lastStatus {
			return
		}
		lastStatus = status
		switch status {
		case "scaned":
			fmt.Println("QR code scanned! Please confirm on your phone.")
		case "confirmed":
			fmt.Println("Login confirmed!")
		case "expired":
			fmt.Println("QR code expired.")
		}
	})
	if err != nil {
		return nil, err
	}
	if err := ilink.SaveCredentials(credentials); err != nil {
		return nil, fmt.Errorf("failed to save credentials: %w", err)
	}
	directory, _ := ilink.CredentialsPath()
	fmt.Printf("\nLogin successful! Credentials saved to %s\n", directory)
	fmt.Printf("Bot ID: %s\n\n", credentials.ILinkBotID)
	return credentials, nil
}
