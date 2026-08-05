package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/huixiangyang/weclaw/api"
	"github.com/huixiangyang/weclaw/codex"
	"github.com/huixiangyang/weclaw/config"
	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/messaging"
	"github.com/huixiangyang/weclaw/project"
	"github.com/huixiangyang/weclaw/reporting"
	"github.com/huixiangyang/weclaw/session"
	"github.com/huixiangyang/weclaw/visual"
	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
)

var (
	foregroundFlag bool
	apiAddrFlag    string
)

func init() {
	startCmd.Flags().BoolVarP(&foregroundFlag, "foreground", "f", false, "Run in foreground (default is background)")
	startCmd.Flags().StringVar(&apiAddrFlag, "api-addr", "", "API server listen address (default 127.0.0.1:18011)")
	rootCmd.AddCommand(startCmd)
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the WeChat message bridge (auto-login if needed)",
	RunE:  runStart,
}

func runStart(cmd *cobra.Command, args []string) error {
	if !foregroundFlag {
		// Check if login is needed — if so, do it in foreground first, then daemon
		accounts, _ := ilink.LoadAllCredentials()
		if len(accounts) == 0 {
			fmt.Println("No WeChat accounts found, starting login...")
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			_, err := doLogin(ctx)
			cancel()
			if err != nil {
				return fmt.Errorf("login failed: %w", err)
			}
		}
		return runDaemon()
	}

	cleanupPID, err := registerForegroundPID()
	if err != nil {
		return err
	}
	defer cleanupPID()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Load all accounts
	accounts, err := ilink.LoadAllCredentials()
	if err != nil {
		return fmt.Errorf("failed to load credentials: %w", err)
	}

	// No accounts — trigger login
	if len(accounts) == 0 {
		log.Println("No WeChat accounts found, starting login...")
		creds, err := doLogin(ctx)
		if err != nil {
			return fmt.Errorf("login failed: %w", err)
		}
		accounts = append(accounts, creds)
	}

	// 配置只允许单一 Codex App Server，旧 agents/default_agent 字段会被严格拒绝。
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if len(cfg.Projects) == 1 && cfg.Projects[0].ID == "workspace" {
		if err := os.MkdirAll(cfg.Projects[0].Root, 0o700); err != nil {
			return fmt.Errorf("create default project root: %w", err)
		}
	}
	projectManager, err := project.NewManager(cfg.Projects, "")
	if err != nil {
		return fmt.Errorf("initialize project manager: %w", err)
	}
	initialProject := projectManager.List()[0]
	codex := codex.NewCodex(codex.CodexConfig{
		Command: cfg.Codex.Command,
		Cwd:     initialProject.Root,
		Env:     cfg.Codex.Env,
		Model:   cfg.Codex.Model,
	})
	log.Printf("Initializing Codex App Server (command=%s, project=%s, cwd=%s, model=%s)...", cfg.Codex.Command, initialProject.ID, initialProject.Root, cfg.Codex.Model)
	if err := codex.Start(ctx); err != nil {
		return fmt.Errorf("initialize codex: %w", err)
	}
	defer codex.Stop()
	handler := messaging.NewHandler(codex)
	handler.SetProjectManager(projectManager)
	if cfg.Visual.Enabled {
		visualRenderer, visualErr := visual.NewRenderer(visual.Config{BrowserCommand: cfg.Visual.BrowserCommand})
		if visualErr != nil {
			return fmt.Errorf("initialize visual control cards: %w", visualErr)
		}
		styleStore, styleErr := visual.NewStyleStore("")
		if styleErr != nil {
			return fmt.Errorf("initialize visual style preferences: %w", styleErr)
		}
		handler.SetVisualRenderer(visualRenderer)
		handler.SetVisualStyleStore(styleStore)
		handler.SetVisualReplyConfig(cfg.Visual.LongReplies, cfg.Visual.LongReplyMinRunes)
		log.Printf("Visual control cards enabled (browser=%s)", visualRenderer.BrowserCommand())
	}
	sessionManager, err := session.NewManager("")
	if err != nil {
		return fmt.Errorf("initialize session manager: %w", err)
	}
	handler.SetSessionManager(sessionManager)
	activityStore, err := messaging.NewActivityStore("")
	if err != nil {
		return fmt.Errorf("initialize task history: %w", err)
	}
	handler.SetActivityStore(activityStore)
	libraryStore, err := messaging.NewLibraryStore("")
	if err != nil {
		return fmt.Errorf("initialize material and delivery library: %w", err)
	}
	handler.SetLibraryStore(libraryStore)
	remoteLock, err := messaging.NewRemoteLock("", cfg.Security.RemoteLockCode)
	if err != nil {
		return fmt.Errorf("initialize remote lock: %w", err)
	}
	handler.SetRemoteLock(remoteLock)
	if cfg.Voice.Enabled {
		providers := make([]messaging.VoiceProviderEntry, 0, len(cfg.Voice.Providers))
		providerIDs := make([]string, 0, len(cfg.Voice.Providers))
		for _, providerConfig := range cfg.Voice.Providers {
			var provider messaging.VoiceProvider
			switch providerConfig.Type {
			case "piper":
				provider = messaging.NewPiperVoiceProvider(providerConfig.ID, messaging.PiperVoiceProviderConfig{
					Command:       providerConfig.Piper.Command,
					Model:         providerConfig.Piper.Model,
					ModelConfig:   providerConfig.Piper.ModelConfig,
					FFmpegCommand: providerConfig.Piper.FFmpegCommand,
					LengthScale:   providerConfig.Piper.LengthScale,
				})
			case "mimo":
				provider = messaging.NewMiMoVoiceProvider(providerConfig.ID, messaging.MiMoVoiceProviderConfig{
					BaseURL:     providerConfig.MiMo.BaseURL,
					APIKey:      providerConfig.MiMo.APIKey,
					Model:       providerConfig.MiMo.Model,
					Voice:       providerConfig.MiMo.Voice,
					StylePrompt: providerConfig.MiMo.StylePrompt,
				})
			}
			providers = append(providers, messaging.VoiceProviderEntry{
				Provider: provider,
				Timeout:  time.Duration(providerConfig.TimeoutSeconds) * time.Second,
			})
			providerIDs = append(providerIDs, providerConfig.ID)
		}
		handler.SetVoiceBriefing(messaging.NewVoiceBriefing(providers))
		log.Printf("Voice briefing enabled (providers=%s)", strings.Join(providerIDs, ","))
	}

	handler.SetProgressConfig(messaging.ProgressConfig{
		Enabled:           cfg.Progress.Enabled,
		TypingInterval:    time.Duration(cfg.Progress.TypingIntervalSeconds) * time.Second,
		FirstMessageDelay: time.Duration(cfg.Progress.FirstMessageDelaySeconds) * time.Second,
		MessageInterval:   time.Duration(cfg.Progress.MessageIntervalSeconds) * time.Second,
	})

	// 可选的 Linkhoard 网页归档目录，与 turn 附件沙箱无关。
	if cfg.SaveDir != "" {
		handler.SetSaveDir(cfg.SaveDir)
		log.Printf("Linkhoard archive directory: %s", cfg.SaveDir)
	}

	// Start HTTP API server for sending messages
	var clients []*ilink.Client
	for _, c := range accounts {
		clients = append(clients, ilink.NewClient(c))
	}
	// Resolve API addr: flag > env/config > default
	apiAddr := cfg.APIAddr // already includes env override from loadEnv
	if apiAddrFlag != "" {
		apiAddr = apiAddrFlag
	}
	handler.SetBridgeInfo(Version, apiAddr)
	apiServer := api.NewServer(clients, apiAddr)
	apiErr := make(chan error, 1)
	go func() {
		apiErr <- apiServer.Run(ctx)
	}()
	select {
	case <-apiServer.Ready():
	case err := <-apiErr:
		return fmt.Errorf("start API server: %w", err)
	case <-ctx.Done():
		return ctx.Err()
	}
	go func() {
		if err := <-apiErr; err != nil {
			log.Printf("API server error: %v", err)
		}
	}()

	// 确定性自动化复用已登录账号主动通知，状态按计划和绑定者隔离。
	reportScheduler, err := reporting.NewScheduler(cfg.Automations, cfg.Projects, clients)
	if err != nil {
		return fmt.Errorf("initialize scheduled reports: %w", err)
	}
	handler.SetAutomationProvider(reportScheduler)
	go reportScheduler.Run(ctx)

	// Codex 就绪后才启动消息轮询。
	log.Printf("Starting message bridge for %d account(s)...", len(accounts))

	var wg sync.WaitGroup
	for _, creds := range accounts {
		wg.Add(1)
		go func(c *ilink.Credentials) {
			defer wg.Done()
			runMonitorWithRestart(ctx, c, handler)
		}(creds)
	}

	monitorsDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(monitorsDone)
	}()

	select {
	case <-monitorsDone:
		log.Println("All monitors stopped")
		return nil
	case <-codex.Done():
		if ctx.Err() != nil {
			<-monitorsDone
			return nil
		}
		if exitErr := codex.ExitError(); exitErr != nil {
			return fmt.Errorf("codex app-server exited: %w", exitErr)
		}
		return fmt.Errorf("codex app-server exited unexpectedly")
	case <-ctx.Done():
		<-monitorsDone
		return nil
	}
}

// runMonitorWithRestart runs a monitor with automatic restart on failure.
func runMonitorWithRestart(ctx context.Context, creds *ilink.Credentials, handler *messaging.Handler) {
	const maxRestartDelay = 30 * time.Second
	restartDelay := 3 * time.Second

	for {
		log.Printf("[%s] Starting monitor...", ilink.LogLabel(creds.ILinkBotID))

		client := ilink.NewClient(creds)
		monitor, err := ilink.NewMonitor(client, handler.HandleMessage)
		if err != nil {
			log.Printf("[%s] Failed to create monitor: %v", ilink.LogLabel(creds.ILinkBotID), err)
		} else {
			err = monitor.Run(ctx)
		}

		// If context is cancelled, exit
		if ctx.Err() != nil {
			return
		}

		log.Printf("[%s] Monitor stopped: %v, restarting in %s", ilink.LogLabel(creds.ILinkBotID), err, restartDelay)
		select {
		case <-time.After(restartDelay):
		case <-ctx.Done():
			return
		}

		// Exponential backoff for restarts, capped
		restartDelay *= 2
		if restartDelay > maxRestartDelay {
			restartDelay = maxRestartDelay
		}
	}
}

// doLogin runs the interactive QR login flow and returns credentials.
func doLogin(ctx context.Context) (*ilink.Credentials, error) {
	fmt.Println("Fetching QR code...")
	qr, err := ilink.FetchQRCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch QR code: %w", err)
	}

	fmt.Println("\nScan this QR code with WeChat:")
	fmt.Println()
	qrterminal.GenerateWithConfig(qr.QRCodeImgContent, qrterminal.Config{
		Level:          qrterminal.L,
		Writer:         os.Stdout,
		HalfBlocks:     true,
		BlackChar:      qrterminal.BLACK_BLACK,
		WhiteBlackChar: qrterminal.WHITE_BLACK,
		WhiteChar:      qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE,
		QuietZone:      1,
	})
	fmt.Printf("\nQR URL: %s\n", qr.QRCodeImgContent)
	fmt.Println("\nWaiting for scan...")

	lastStatus := ""
	creds, err := ilink.PollQRStatus(ctx, qr.QRCode, func(status string) {
		if status != lastStatus {
			lastStatus = status
			switch status {
			case "scaned":
				fmt.Println("QR code scanned! Please confirm on your phone.")
			case "confirmed":
				fmt.Println("Login confirmed!")
			case "expired":
				fmt.Println("QR code expired.")
			}
		}
	})
	if err != nil {
		return nil, err
	}

	if err := ilink.SaveCredentials(creds); err != nil {
		return nil, fmt.Errorf("failed to save credentials: %w", err)
	}

	dir, _ := ilink.CredentialsPath()
	fmt.Printf("\nLogin successful! Credentials saved to %s\n", dir)
	fmt.Printf("Bot ID: %s\n\n", creds.ILinkBotID)
	return creds, nil
}

// --- Daemon mode ---

func weclawDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".weclaw")
}

func pidFile() string {
	return filepath.Join(weclawDir(), "weclaw.pid")
}

func logFile() string {
	return filepath.Join(weclawDir(), "weclaw.log")
}

// registerForegroundPID 让 systemd 前台模式也拥有准确的状态文件。
// 清理时核对 PID，避免旧进程误删新实例写入的状态。
func registerForegroundPID() (func(), error) {
	if err := os.MkdirAll(weclawDir(), 0o700); err != nil {
		return nil, fmt.Errorf("create weclaw dir: %w", err)
	}
	pid := os.Getpid()
	pidText := fmt.Sprintf("%d", pid)
	if err := os.WriteFile(pidFile(), []byte(pidText), 0o600); err != nil {
		return nil, fmt.Errorf("write pid file: %w", err)
	}

	return func() {
		data, err := os.ReadFile(pidFile())
		if err == nil && string(data) == pidText {
			_ = os.Remove(pidFile())
		}
	}, nil
}

// runDaemon spawns weclaw start (without --daemon) as a background process.
func runDaemon() error {
	// Kill any existing weclaw processes before starting a new one
	stopAllWeclaw()

	// Ensure log directory exists
	if err := os.MkdirAll(weclawDir(), 0o700); err != nil {
		return fmt.Errorf("create weclaw dir: %w", err)
	}

	// Open log file
	lf, err := os.OpenFile(logFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	// Re-exec ourselves without --daemon
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}

	cmd := exec.Command(exe, "start", "-f")
	cmd.Stdout = lf
	cmd.Stderr = lf
	setSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		lf.Close()
		return fmt.Errorf("start daemon: %w", err)
	}

	// Save PID
	pid := cmd.Process.Pid
	os.WriteFile(pidFile(), []byte(fmt.Sprintf("%d", pid)), 0o600)

	// Detach — don't wait
	cmd.Process.Release()
	lf.Close()

	fmt.Printf("weclaw started in background (pid=%d)\n", pid)
	fmt.Printf("Log: %s\n", logFile())
	fmt.Printf("Stop: weclaw stop\n")
	return nil
}

func readPid() (int, error) {
	data, err := os.ReadFile(pidFile())
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		return 0, err
	}
	return pid, nil
}

func processExists(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks if process exists without killing it
	return p.Signal(syscall.Signal(0)) == nil
}

// stopAllWeclaw kills all running weclaw processes (by PID file and by process scan).
func stopAllWeclaw() {
	// 1. Kill by PID file
	if pid, err := readPid(); err == nil && processExists(pid) {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
	}
	os.Remove(pidFile())

	// 2. Kill any remaining weclaw processes by scanning
	exe, err := os.Executable()
	if err != nil {
		return
	}
	// Use pkill to kill all processes matching the executable path
	_ = exec.Command("pkill", "-f", exe+" start").Run()
	time.Sleep(500 * time.Millisecond)
}
