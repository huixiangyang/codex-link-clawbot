// Package app 是进程唯一组合根，负责构造领域服务、适配器和运行循环。
package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/appserver"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/management"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/visual"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/voice"
	"github.com/huixiangyang/codex-link-clawbot/internal/app/config"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/access"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/delivery"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/runtimecontrol"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/workspace"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

type Options struct {
	Version   string
	StateRoot string
	Draining  bool
}

func Run(ctx context.Context, cfg *config.Config, accounts []*ilink.Credentials, options Options) error {
	if cfg == nil || len(accounts) != 1 || strings.TrimSpace(options.Version) == "" || !filepath.IsAbs(options.StateRoot) {
		return fmt.Errorf("application bootstrap input is invalid")
	}
	for _, credentials := range accounts {
		if credentials == nil || strings.TrimSpace(credentials.ILinkUserID) == "" {
			return fmt.Errorf("account owner is missing")
		}
	}
	if err := storage.CheckReady(options.StateRoot); err != nil {
		return err
	}
	entries := cfg.Clawbot.ProjectEntries
	replyConfig := cfg.Clawbot.Reply
	if len(entries) == 1 && entries[0].ID == "workspace" {
		if err := os.MkdirAll(entries[0].Root, 0o700); err != nil {
			return fmt.Errorf("create default workspace root: %w", err)
		}
	}
	definitions := make([]workspace.Definition, 0, len(entries))
	for _, entry := range entries {
		definitions = append(definitions, workspace.Definition{ID: entry.ID, Name: entry.Name, Root: entry.Root})
	}
	workspaces, err := workspace.NewManager(definitions)
	if err != nil {
		return fmt.Errorf("initialize workspace manager: %w", err)
	}
	layout, err := storage.NewLayout(options.StateRoot)
	if err != nil {
		return err
	}
	if err := layout.CleanupTemporary(); err != nil {
		return err
	}
	targets, err := target.Open(options.StateRoot, workspaces.List()[0].ID)
	if err != nil {
		return fmt.Errorf("initialize conversation targets: %w", err)
	}
	initialWorkspace := workspaces.List()[0]
	codexClient := appserver.New(appserver.Config{Command: cfg.Codex.Command, Env: cfg.Codex.Env, Model: cfg.Codex.Model})
	log.Printf("Initializing Codex App Server (command=%s, workspace=%s, cwd=%s, model=%s)...", cfg.Codex.Command, initialWorkspace.ID, initialWorkspace.Root, cfg.Codex.Model)
	if err := codexClient.Start(ctx); err != nil {
		return fmt.Errorf("initialize codex: %w", err)
	}
	defer codexClient.Stop()

	preferences, err := preference.NewStore(options.StateRoot)
	if err != nil {
		return fmt.Errorf("initialize owner preferences: %w", err)
	}
	var visualRenderer wechat.VisualRenderer
	if replyConfig.Visual.Enabled {
		renderer, renderErr := visual.NewRenderer(visual.Config{BrowserCommand: replyConfig.Visual.BrowserCommand, RootDir: layout.Temporary("render")})
		if renderErr != nil {
			return fmt.Errorf("initialize visual control cards: %w", renderErr)
		}
		visualRenderer = renderer
		log.Printf("Visual control cards enabled (browser=%s)", renderer.BrowserCommand())
	}
	threads, err := thread.NewManager()
	if err != nil {
		return fmt.Errorf("initialize thread manager: %w", err)
	}
	requests, err := request.NewStore(options.StateRoot)
	if err != nil {
		return fmt.Errorf("initialize execution records: %w", err)
	}
	notices, err := delivery.OpenNoticeStore(options.StateRoot, time.Now)
	if err != nil {
		return fmt.Errorf("initialize pending notice store: %w", err)
	}

	var messageHold atomic.Bool
	messageHold.Store(options.Draining)
	drainer := &runtimeDrainer{messageHold: &messageHold}
	runtimeController := runtimecontrol.New(options.Version, requests, drainer)
	runtimeController.SetCodexReady(true)
	defer runtimeController.SetStopping()

	remoteLock, err := access.NewRemoteLock(options.StateRoot, cfg.Clawbot.Security.RemoteLockCode)
	if err != nil {
		return fmt.Errorf("initialize remote lock: %w", err)
	}
	managementURL := strings.TrimRight(cfg.Clawbot.Management.PublicURL, "/")
	if managementURL == "" {
		managementURL = "http://" + cfg.Clawbot.Management.Listen
	}
	wechatRuntime, err := wechat.NewRuntime(wechat.Dependencies{
		Targets:    targets,
		Codex:      codexClient,
		Workspaces: workspaces, Threads: threads, Visual: visualRenderer, Preferences: preferences,
		Requests: requests, Lifecycle: runtimeController, PendingNotices: notices,
		RemoteLock: remoteLock, Voice: buildVoice(replyConfig, layout.Temporary("voice")), ManagementURL: managementURL,
		VisualReplyEnabled: replyConfig.Visual.LongReplies, VisualReplyMinRunes: replyConfig.Visual.LongReplyMinRunes,
		Progress: execution.ProgressConfig{
			Enabled:           replyConfig.Progress.Enabled,
			TypingInterval:    time.Duration(replyConfig.Progress.TypingIntervalSeconds) * time.Second,
			FirstMessageDelay: time.Duration(replyConfig.Progress.FirstMessageDelaySeconds) * time.Second,
		},
	})
	if err != nil {
		return fmt.Errorf("initialize message bridge: %w", err)
	}
	handler := wechatRuntime.Handler
	coordinator := wechatRuntime.Coordinator
	drainer.coordinator = coordinator
	if options.Draining {
		runtimeController.Drain()
	}

	for _, credentials := range accounts {
		wechatRuntime.RegisterClient(ilink.NewClient(credentials))
	}
	consoleToken, err := management.EnsureConsoleToken(options.StateRoot)
	if err != nil {
		return fmt.Errorf("initialize management console token: %w", err)
	}
	consoleServer, err := management.NewConsoleServer(cfg.Clawbot.Management.Listen, consoleToken, management.ConsoleDependencies{
		Targets: targets, Recovery: wechatRuntime, Conversations: wechatRuntime.Conversations, VisualEnabled: replyConfig.Visual.Enabled, VoiceEnabled: replyConfig.Voice.Enabled,
		Runtime: runtimeController, Workspaces: workspaces, Threads: threads, Requests: requests,
		Preferences: preferences, RemoteLock: remoteLock, Codex: codexClient,
		OwnerID: accounts[0].ILinkUserID, PublicURL: cfg.Clawbot.Management.PublicURL,
	})
	if err != nil {
		return fmt.Errorf("initialize management console: %w", err)
	}
	managementServer := management.NewManagementServer(
		runtimeController,
		filepath.Join(options.StateRoot, management.ManagementSocketName),
	)
	services := newServiceGroup(ctx)
	defer services.Stop()
	services.Go("codex app-server", func(ctx context.Context) error {
		select {
		case <-codexClient.Done():
			return codexClient.ExitError()
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	services.Go("local management server", managementServer.Run)
	services.Go("management console", consoleServer.Run)
	if err := services.WaitReady(managementServer.Ready()); err != nil {
		return err
	}
	if err := services.WaitReady(consoleServer.Ready()); err != nil {
		return err
	}
	log.Printf("Management console listening on %s", cfg.Clawbot.Management.Listen)

	services.Go("request coordinator", coordinator.Run)
	services.Go("retention cleanup", func(ctx context.Context) error {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				wechatRuntime.ExpireMessageContexts()
				if err := requests.CleanupExpired(); err != nil {
					return fmt.Errorf("清理到期请求数据: %w", err)
				}
			}
		}
	})
	log.Printf("Starting message bridge for %d account(s)...", len(accounts))
	probes := make([]ilink.MonitorObserver, 0, len(accounts))
	for range accounts {
		probes = append(probes, runtimeController.NewMonitorProbe())
	}
	runtimeController.SetReady()
	services.Go("message monitors", func(ctx context.Context) error {
		<-runMonitors(ctx, options.StateRoot, accounts, handler, probes, &messageHold)
		return ctx.Err()
	})
	return services.Wait()
}

func buildVoice(reply config.ReplyConfig, temporaryRoot string) *voice.Briefing {
	if !reply.Voice.Enabled {
		return nil
	}
	providers := make([]voice.Entry, 0, len(reply.Voice.Providers))
	providerIDs := make([]string, 0, len(reply.Voice.Providers))
	for _, providerConfig := range reply.Voice.Providers {
		var provider voice.Provider
		switch providerConfig.Type {
		case "piper":
			provider = voice.NewPiper(providerConfig.ID, voice.PiperConfig{
				TemporaryRoot: temporaryRoot,
				Command:       providerConfig.Piper.Command, Model: providerConfig.Piper.Model,
				ModelConfig: providerConfig.Piper.ModelConfig, LengthScale: providerConfig.Piper.LengthScale,
			})
		case "mimo":
			provider = voice.NewMiMo(providerConfig.ID, voice.MiMoConfig{
				BaseURL: providerConfig.MiMo.BaseURL, APIKey: providerConfig.MiMo.APIKey, Model: providerConfig.MiMo.Model,
				Voice: providerConfig.MiMo.Voice, StylePrompt: providerConfig.MiMo.StylePrompt,
			})
		}
		providers = append(providers, voice.Entry{Provider: provider, Timeout: time.Duration(providerConfig.TimeoutSeconds) * time.Second})
		providerIDs = append(providerIDs, providerConfig.ID)
	}
	log.Printf("Voice briefing enabled (providers=%s, delivery=mp3-file)", strings.Join(providerIDs, ","))
	return voice.NewBriefing(reply.Voice.FFmpegCommand, providers)
}

func runMonitors(ctx context.Context, root string, accounts []*ilink.Credentials, handler *wechat.Handler, probes []ilink.MonitorObserver, messageHold *atomic.Bool) <-chan struct{} {
	var waitGroup sync.WaitGroup
	for index, credentials := range accounts {
		waitGroup.Add(1)
		go func(account *ilink.Credentials, observer ilink.MonitorObserver) {
			defer waitGroup.Done()
			runMonitorWithRestart(ctx, root, account, handler, observer, messageHold)
		}(credentials, probes[index])
	}
	done := make(chan struct{})
	go func() {
		waitGroup.Wait()
		close(done)
	}()
	return done
}

func runMonitorWithRestart(ctx context.Context, root string, credentials *ilink.Credentials, handler *wechat.Handler, observer ilink.MonitorObserver, messageHold *atomic.Bool) {
	const maxRestartDelay = 30 * time.Second
	restartDelay := 3 * time.Second
	for {
		log.Printf("[%s] Starting monitor...", ilink.LogLabel(credentials.ILinkBotID))
		client := ilink.NewClient(credentials)
		monitor, err := ilink.NewMonitor(root, client, handler.HandleMessage, observer)
		if err != nil {
			log.Printf("[%s] Failed to create monitor: %v", ilink.LogLabel(credentials.ILinkBotID), err)
		} else {
			monitor.SetMessageHold(messageHold.Load)
			err = monitor.Run(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		log.Printf("[%s] Monitor stopped: %v, restarting in %s", ilink.LogLabel(credentials.ILinkBotID), err, restartDelay)
		select {
		case <-time.After(restartDelay):
		case <-ctx.Done():
			return
		}
		restartDelay *= 2
		if restartDelay > maxRestartDelay {
			restartDelay = maxRestartDelay
		}
	}
}

type runtimeDrainer struct {
	coordinator *execution.Coordinator
	messageHold *atomic.Bool
}

func (drainer *runtimeDrainer) SetDraining(draining bool) {
	if drainer.coordinator != nil {
		drainer.coordinator.SetDraining(draining)
	}
	if !draining {
		drainer.messageHold.Store(false)
	}
}
