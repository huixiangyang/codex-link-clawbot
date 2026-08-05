package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/huixiangyang/weclaw/api"
	"github.com/huixiangyang/weclaw/codex"
	"github.com/huixiangyang/weclaw/config"
	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/messaging"
	"github.com/huixiangyang/weclaw/preference"
	"github.com/huixiangyang/weclaw/project"
	"github.com/huixiangyang/weclaw/reporting"
	"github.com/huixiangyang/weclaw/runtimecontrol"
	"github.com/huixiangyang/weclaw/session"
	"github.com/huixiangyang/weclaw/statefile"
	"github.com/huixiangyang/weclaw/taskqueue"
	"github.com/huixiangyang/weclaw/visual"
	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
)

var (
	apiAddrFlag       string
	startDrainingFlag bool
)

func init() {
	startCmd.Flags().StringVar(&apiAddrFlag, "api-addr", "", "API server listen address (default 127.0.0.1:18011)")
	startCmd.Flags().BoolVar(&startDrainingFlag, "draining", false, "start without claiming queued tasks")
	_ = startCmd.Flags().MarkHidden("draining")
	rootCmd.AddCommand(startCmd)
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the WeChat message bridge (auto-login if needed)",
	RunE:  runStart,
}

func runStart(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	stateRoot, err := statefile.DefaultRoot()
	if err != nil {
		return fmt.Errorf("resolve state root: %w", err)
	}
	stateLease, err := statefile.Acquire(stateRoot, statefile.LeaseRuntime)
	if err != nil {
		return fmt.Errorf("acquire runtime state lease: %w", err)
	}
	defer func() {
		if err := stateLease.Close(); err != nil {
			log.Printf("release runtime state lease: %v", err)
		}
	}()

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
	controlStateStore, controlStateErr := messaging.NewControlStateStore("")
	if controlStateErr != nil {
		return fmt.Errorf("initialize persistent control state: %w", controlStateErr)
	}
	handler.SetControlStateStore(controlStateStore)
	handler.SetProjectManager(projectManager)
	preferenceStore, preferenceErr := preference.NewStore("")
	if preferenceErr != nil {
		return fmt.Errorf("initialize owner preferences: %w", preferenceErr)
	}
	handler.SetPreferenceStore(preferenceStore)
	if cfg.Visual.Enabled {
		visualRenderer, visualErr := visual.NewRenderer(visual.Config{BrowserCommand: cfg.Visual.BrowserCommand})
		if visualErr != nil {
			return fmt.Errorf("initialize visual control cards: %w", visualErr)
		}
		handler.SetVisualRenderer(visualRenderer)
		handler.SetVisualReplyConfig(cfg.Visual.LongReplies, cfg.Visual.LongReplyMinRunes)
		log.Printf("Visual control cards enabled (browser=%s)", visualRenderer.BrowserCommand())
	}
	sessionManager, err := session.NewManager("")
	if err != nil {
		return fmt.Errorf("initialize session manager: %w", err)
	}
	handler.SetSessionManager(sessionManager)
	taskStore, err := taskqueue.NewStore("")
	if err != nil {
		return fmt.Errorf("initialize persistent task queue: %w", err)
	}
	coordinator, err := messaging.NewCoordinator(handler, taskStore)
	if err != nil {
		return fmt.Errorf("initialize task coordinator: %w", err)
	}
	handler.SetTaskQueue(taskStore, coordinator)
	var deploymentMessageHold atomic.Bool
	deploymentMessageHold.Store(startDrainingFlag)
	runtimeDrainer := &startupRuntimeDrainer{coordinator: coordinator, messageHold: &deploymentMessageHold}
	runtimeController := runtimecontrol.New(Version, taskStore, runtimeDrainer)
	runtimeController.SetCodexReady(true)
	if startDrainingFlag {
		// 部署验收期间禁止新版本提前领取旧队列，确认健康后再由部署事务恢复。
		runtimeController.Drain()
	}
	handler.SetRuntimeLifecycle(runtimeController)
	defer runtimeController.SetStopping()
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
					Command:     providerConfig.Piper.Command,
					Model:       providerConfig.Piper.Model,
					ModelConfig: providerConfig.Piper.ModelConfig,
					LengthScale: providerConfig.Piper.LengthScale,
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
		handler.SetVoiceBriefing(messaging.NewVoiceBriefing(cfg.Voice.FFmpegCommand, providers))
		log.Printf("Voice briefing enabled (providers=%s, delivery=mp3-file)", strings.Join(providerIDs, ","))
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
		client := ilink.NewClient(c)
		clients = append(clients, client)
		coordinator.RegisterClient(client)
	}
	managementSocket := filepath.Join(stateRoot, api.ManagementSocketName)
	managementServer := api.NewManagementServer(runtimeController, managementSocket, func(notifyContext context.Context, notice api.DeploymentNotice) error {
		message := fmt.Sprintf("WeClaw 已完成部署并恢复服务。\n版本：%s → %s\n服务：%s\n状态：已就绪", notice.FromVersion, notice.ToVersion, notice.Service)
		var failures []error
		for index, credentials := range accounts {
			if strings.TrimSpace(credentials.ILinkUserID) == "" {
				failures = append(failures, fmt.Errorf("account owner is missing"))
				continue
			}
			if err := messaging.SendTextReply(notifyContext, clients[index], credentials.ILinkUserID, message, "", ""); err != nil {
				failures = append(failures, err)
			}
		}
		return errors.Join(failures...)
	})
	managementErr := make(chan error, 1)
	go func() {
		managementErr <- managementServer.Run(ctx)
	}()
	select {
	case <-managementServer.Ready():
	case err := <-managementErr:
		return fmt.Errorf("start local management server: %w", err)
	case <-ctx.Done():
		return ctx.Err()
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
	coordinatorErr := make(chan error, 1)
	go func() {
		coordinatorErr <- coordinator.Run(ctx)
	}()

	// Codex 就绪后才启动消息轮询。
	log.Printf("Starting message bridge for %d account(s)...", len(accounts))
	monitorProbes := make([]ilink.MonitorObserver, 0, len(accounts))
	for range accounts {
		monitorProbes = append(monitorProbes, runtimeController.NewMonitorProbe())
	}
	runtimeController.SetReady()

	var wg sync.WaitGroup
	for index, creds := range accounts {
		wg.Add(1)
		go func(c *ilink.Credentials, monitorProbe ilink.MonitorObserver) {
			defer wg.Done()
			runMonitorWithRestart(ctx, c, handler, monitorProbe, &deploymentMessageHold)
		}(creds, monitorProbes[index])
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
	case err := <-coordinatorErr:
		if ctx.Err() != nil {
			<-monitorsDone
			return nil
		}
		return fmt.Errorf("task coordinator stopped: %w", err)
	case err := <-managementErr:
		if ctx.Err() != nil {
			<-monitorsDone
			return nil
		}
		return fmt.Errorf("local management server stopped: %w", err)
	case <-ctx.Done():
		<-monitorsDone
		return nil
	}
}

// runMonitorWithRestart runs a monitor with automatic restart on failure.
func runMonitorWithRestart(ctx context.Context, creds *ilink.Credentials, handler *messaging.Handler, observer ilink.MonitorObserver, messageHold *atomic.Bool) {
	const maxRestartDelay = 30 * time.Second
	restartDelay := 3 * time.Second

	for {
		log.Printf("[%s] Starting monitor...", ilink.LogLabel(creds.ILinkBotID))

		client := ilink.NewClient(creds)
		monitor, err := ilink.NewMonitor(client, handler.HandleMessage, observer)
		if err != nil {
			log.Printf("[%s] Failed to create monitor: %v", ilink.LogLabel(creds.ILinkBotID), err)
		} else {
			if messageHold != nil {
				monitor.SetMessageHold(messageHold.Load)
			}
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

type startupRuntimeDrainer struct {
	coordinator *messaging.Coordinator
	messageHold *atomic.Bool
}

func (drainer *startupRuntimeDrainer) SetDraining(draining bool) {
	if drainer.coordinator != nil {
		drainer.coordinator.SetDraining(draining)
	}
	if !draining && drainer.messageHold != nil {
		drainer.messageHold.Store(false)
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
