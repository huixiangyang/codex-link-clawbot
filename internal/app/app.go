// Package app 是进程唯一组合根，负责构造领域服务、适配器和运行循环。
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/access"
	"github.com/huixiangyang/codex-link-clawbot/internal/bridge"
	"github.com/huixiangyang/codex-link-clawbot/internal/codex/appserver"
	"github.com/huixiangyang/codex-link-clawbot/internal/config"
	"github.com/huixiangyang/codex-link-clawbot/internal/control"
	"github.com/huixiangyang/codex-link-clawbot/internal/delivery"
	"github.com/huixiangyang/codex-link-clawbot/internal/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/management"
	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/runtimecontrol"
	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
	"github.com/huixiangyang/codex-link-clawbot/internal/workspace"
)

type Options struct {
	Version   string
	StateRoot string
	Draining  bool
}

func Run(ctx context.Context, cfg *config.Config, accounts []*ilink.Credentials, options Options) error {
	if cfg == nil || len(accounts) == 0 || strings.TrimSpace(options.Version) == "" || strings.TrimSpace(options.StateRoot) == "" {
		return fmt.Errorf("application bootstrap input is invalid")
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
	workspaces, err := workspace.NewManager(definitions, "")
	if err != nil {
		return fmt.Errorf("initialize workspace manager: %w", err)
	}
	initialWorkspace := workspaces.List()[0]
	codexClient := appserver.New(appserver.Config{Command: cfg.Codex.Command, Env: cfg.Codex.Env, Model: cfg.Codex.Model})
	log.Printf("Initializing Codex App Server (command=%s, workspace=%s, cwd=%s, model=%s)...", cfg.Codex.Command, initialWorkspace.ID, initialWorkspace.Root, cfg.Codex.Model)
	if err := codexClient.Start(ctx); err != nil {
		return fmt.Errorf("initialize codex: %w", err)
	}
	defer codexClient.Stop()

	controlStates, err := bridge.NewControlStateStore("")
	if err != nil {
		return fmt.Errorf("initialize persistent control state: %w", err)
	}
	preferences, err := preference.NewStore("")
	if err != nil {
		return fmt.Errorf("initialize owner preferences: %w", err)
	}
	var visualRenderer bridge.VisualRenderer
	if replyConfig.Visual.Enabled {
		renderer, renderErr := visual.NewRenderer(visual.Config{BrowserCommand: replyConfig.Visual.BrowserCommand})
		if renderErr != nil {
			return fmt.Errorf("initialize visual control cards: %w", renderErr)
		}
		visualRenderer = renderer
		log.Printf("Visual control cards enabled (browser=%s)", renderer.BrowserCommand())
	}
	threads, err := thread.NewManager("", func(ownerID string) thread.Workspace {
		definition := workspaces.Current(ownerID)
		return thread.Workspace{ID: definition.ID, Name: definition.Name, Root: definition.Root}
	})
	if err != nil {
		return fmt.Errorf("initialize thread manager: %w", err)
	}
	requests, err := request.NewStore("")
	if err != nil {
		return fmt.Errorf("initialize persistent request queue: %w", err)
	}
	notices, err := delivery.OpenNoticeStore("", time.Now)
	if err != nil {
		return fmt.Errorf("initialize pending notice store: %w", err)
	}

	var messageHold atomic.Bool
	messageHold.Store(options.Draining)
	drainer := &runtimeDrainer{messageHold: &messageHold}
	runtimeController := runtimecontrol.New(options.Version, requests, drainer)
	runtimeController.SetCodexReady(true)
	if options.Draining {
		runtimeController.Drain()
	}
	defer runtimeController.SetStopping()

	deliveries, err := delivery.OpenStore("", time.Now)
	if err != nil {
		return fmt.Errorf("initialize delivery store: %w", err)
	}
	remoteLock, err := access.NewRemoteLock("", cfg.Clawbot.Security.RemoteLockCode)
	if err != nil {
		return fmt.Errorf("initialize remote lock: %w", err)
	}
	intents, err := control.DefaultRegistry()
	if err != nil {
		return fmt.Errorf("initialize control intents: %w", err)
	}
	bridgeRuntime, err := bridge.NewRuntime(bridge.Dependencies{
		Codex: codexClient, ControlStates: controlStates, Intents: intents,
		Workspaces: workspaces, Threads: threads, Visual: visualRenderer, Preferences: preferences,
		Requests: requests, Lifecycle: runtimeController, Deliveries: deliveries, PendingNotices: notices,
		RemoteLock: remoteLock, Voice: buildVoice(replyConfig), Version: options.Version,
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
	handler := bridgeRuntime.Handler
	coordinator := bridgeRuntime.Coordinator
	drainer.coordinator = coordinator

	for _, credentials := range accounts {
		if strings.TrimSpace(credentials.ILinkUserID) == "" {
			return fmt.Errorf("account owner is missing")
		}
		coordinator.RegisterClient(ilink.NewClient(credentials))
	}
	managementServer := management.NewManagementServer(
		runtimeController,
		filepath.Join(options.StateRoot, management.ManagementSocketName),
		deploymentNotifier(accounts, notices),
	)
	managementErrors := make(chan error, 1)
	go func() { managementErrors <- managementServer.Run(ctx) }()
	select {
	case <-managementServer.Ready():
	case err := <-managementErrors:
		return fmt.Errorf("start local management server: %w", err)
	case <-ctx.Done():
		return ctx.Err()
	}

	statefile.ClearLastFailure()
	coordinatorErrors := make(chan error, 1)
	go func() { coordinatorErrors <- coordinator.Run(ctx) }()

	log.Printf("Starting message bridge for %d account(s)...", len(accounts))
	probes := make([]ilink.MonitorObserver, 0, len(accounts))
	for range accounts {
		probes = append(probes, runtimeController.NewMonitorProbe())
	}
	runtimeController.SetReady()
	monitorsDone := runMonitors(ctx, accounts, handler, probes, &messageHold)

	select {
	case <-monitorsDone:
		log.Println("All monitors stopped")
		return nil
	case <-codexClient.Done():
		if ctx.Err() != nil {
			<-monitorsDone
			return nil
		}
		if exitErr := codexClient.ExitError(); exitErr != nil {
			return fmt.Errorf("codex app-server exited: %w", exitErr)
		}
		return fmt.Errorf("codex app-server exited unexpectedly")
	case err := <-coordinatorErrors:
		if ctx.Err() != nil {
			<-monitorsDone
			return nil
		}
		return fmt.Errorf("request coordinator stopped: %w", err)
	case err := <-managementErrors:
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

func buildVoice(reply config.ReplyConfig) *bridge.VoiceBriefing {
	if !reply.Voice.Enabled {
		return nil
	}
	providers := make([]bridge.VoiceProviderEntry, 0, len(reply.Voice.Providers))
	providerIDs := make([]string, 0, len(reply.Voice.Providers))
	for _, providerConfig := range reply.Voice.Providers {
		var provider bridge.VoiceProvider
		switch providerConfig.Type {
		case "piper":
			provider = bridge.NewPiperVoiceProvider(providerConfig.ID, bridge.PiperVoiceProviderConfig{
				Command: providerConfig.Piper.Command, Model: providerConfig.Piper.Model,
				ModelConfig: providerConfig.Piper.ModelConfig, LengthScale: providerConfig.Piper.LengthScale,
			})
		case "mimo":
			provider = bridge.NewMiMoVoiceProvider(providerConfig.ID, bridge.MiMoVoiceProviderConfig{
				BaseURL: providerConfig.MiMo.BaseURL, APIKey: providerConfig.MiMo.APIKey, Model: providerConfig.MiMo.Model,
				Voice: providerConfig.MiMo.Voice, StylePrompt: providerConfig.MiMo.StylePrompt,
			})
		}
		providers = append(providers, bridge.VoiceProviderEntry{Provider: provider, Timeout: time.Duration(providerConfig.TimeoutSeconds) * time.Second})
		providerIDs = append(providerIDs, providerConfig.ID)
	}
	log.Printf("Voice briefing enabled (providers=%s, delivery=mp3-file)", strings.Join(providerIDs, ","))
	return bridge.NewVoiceBriefing(reply.Voice.FFmpegCommand, providers)
}

func deploymentNotifier(accounts []*ilink.Credentials, notices *delivery.NoticeStore) management.DeploymentNotifier {
	return func(_ context.Context, notice management.DeploymentNotice) (management.DeploymentNotificationResult, error) {
		message := fmt.Sprintf("codex-link-clawbot 已完成部署并恢复服务。\n版本：%s → %s\n服务：%s\n状态：已就绪", notice.FromVersion, notice.ToVersion, notice.Service)
		var failures []error
		for _, credentials := range accounts {
			ownerID := strings.TrimSpace(credentials.ILinkUserID)
			if ownerID == "" {
				failures = append(failures, fmt.Errorf("account owner is missing"))
				continue
			}
			if _, _, err := notices.Enqueue(ownerID, delivery.NoticeInput{
				Kind: delivery.NoticeDeployment, DedupKey: "deployment:" + notice.ToVersion + ":" + notice.Service,
				Title: "codex-link-clawbot 部署完成", Body: message, TTL: 7 * 24 * time.Hour,
			}); err != nil {
				failures = append(failures, err)
			}
		}
		if err := errors.Join(failures...); err != nil {
			return management.DeploymentNotificationResult{}, err
		}
		return management.DeploymentNotificationResult{Status: management.DeploymentNotificationDeferred}, nil
	}
}

func runMonitors(ctx context.Context, accounts []*ilink.Credentials, handler *bridge.Handler, probes []ilink.MonitorObserver, messageHold *atomic.Bool) <-chan struct{} {
	var waitGroup sync.WaitGroup
	for index, credentials := range accounts {
		waitGroup.Add(1)
		go func(account *ilink.Credentials, observer ilink.MonitorObserver) {
			defer waitGroup.Done()
			runMonitorWithRestart(ctx, account, handler, observer, messageHold)
		}(credentials, probes[index])
	}
	done := make(chan struct{})
	go func() {
		waitGroup.Wait()
		close(done)
	}()
	return done
}

func runMonitorWithRestart(ctx context.Context, credentials *ilink.Credentials, handler *bridge.Handler, observer ilink.MonitorObserver, messageHold *atomic.Bool) {
	const maxRestartDelay = 30 * time.Second
	restartDelay := 3 * time.Second
	for {
		log.Printf("[%s] Starting monitor...", ilink.LogLabel(credentials.ILinkBotID))
		client := ilink.NewClient(credentials)
		monitor, err := ilink.NewMonitor(client, handler.HandleMessage, observer)
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
	coordinator *bridge.Coordinator
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
