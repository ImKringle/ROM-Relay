package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"romrelay/internal/catalog"
	"romrelay/internal/config"
	"romrelay/internal/converter"
	"romrelay/internal/dashboard"
	"romrelay/internal/decryptor"
	"romrelay/internal/metadata"
	patcher "romrelay/internal/patch"
	"romrelay/internal/presentation"
	remotelib "romrelay/internal/remote"
	webui "romrelay/internal/web"
)

var activePresenter *presentation.Presenter

func main() {
	if err := run(); err != nil {
		if activePresenter != nil {
			activePresenter.Error("ROM Relay stopped: %v", "The corn stand tipped over: %v", err)
		} else {
			fmt.Fprintln(os.Stderr, "ROM Relay:", err)
		}
		os.Exit(1)
	}
}
func run() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	defaultConfig := filepath.Join(filepath.Dir(exe), "config.json")
	configPath := flag.String("config", defaultConfig, "path to JSON configuration")
	flag.Parse()
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	theme := cfg.UITheme
	if cfg.Legacy {
		theme = "corn"
	}
	var operator *dashboard.Dashboard
	out, errOut := os.Stdout, os.Stderr
	var present *presentation.Presenter
	if !cfg.Legacy && dashboard.TerminalAvailable(os.Stdin, os.Stdout) {
		operator = dashboard.New(theme)
		operator.Start()
		present = presentation.New(theme, operator, operator)
		present.SetLiveSink(operator)
	} else {
		present = presentation.New(theme, out, errOut)
	}
	activePresenter = present
	if !cfg.Legacy {
		runErr := runV2(cfg, present, filepath.Join(filepath.Dir(exe), "internal", "drivers"), operator)
		if operator != nil {
			runErr = errors.Join(runErr, operator.Stop())
			activePresenter = presentation.New(theme, os.Stdout, os.Stderr)
		}
		return runErr
	}
	for _, warning := range cfg.Warnings {
		present.Warning("%s", "Old recipe detected: %s", warning)
	}
	conv, err := converter.New(cfg.Boot9Path)
	if err != nil {
		return err
	}
	stage := filepath.Join(cfg.ServeDirectory, ".corntool-staging")
	if err = os.MkdirAll(stage, 0700); err != nil {
		return fmt.Errorf("create private staging directory: %w", err)
	}
	entries, err := os.ReadDir(cfg.InputDirectory)
	if err != nil {
		return err
	}
	found := 0
	failed := 0
	skipped := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".3ds" && ext != ".cci" {
			continue
		}
		found++
		src := filepath.Join(cfg.InputDirectory, e.Name())
		dst := filepath.Join(cfg.ServeDirectory, strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))+".cia")
		if _, statErr := os.Stat(dst); statErr == nil {
			present.Info("Skipping %s: output already exists", "Skipping ear %s: cob already exists", e.Name())
			skipped++
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			present.Error("Failed %s: inspect output: %v", "Spoiled ear %s: inspect cob: %v", e.Name(), statErr)
			failed++
			continue
		}
		tmp, err := os.CreateTemp(stage, "harvest-*.tmp")
		if err != nil {
			return err
		}
		tmpPath := tmp.Name()
		tmp.Close()
		os.Remove(tmpPath)
		present.Info("Converting %s", "Shucking %s", e.Name())
		conversionProgress := present.NewConversion()
		err = conv.Convert(context.Background(), src, tmpPath, func(part string, done, total int64) {
			conversionProgress.Update(part, done, total)
		})
		conversionProgress.Stop(err != nil)
		if err != nil {
			os.Remove(tmpPath)
			present.Error("Conversion failed for %s: %v", "Ear %s fell off the wagon: %v", e.Name(), err)
			failed++
			continue
		}
		if err = os.Rename(tmpPath, dst); err != nil {
			os.Remove(tmpPath)
			present.Error("Failed to publish %s: %v", "Could not stack cob %s: %v", e.Name(), err)
			failed++
			continue
		}
		present.Success("Ready: %s", "Fresh cob ready: %s", filepath.Base(dst))
	}
	if found == 0 {
		present.Info("No .3ds or .cci inputs found", "No .3ds or .cci ears found in the field")
	}
	present.Summary("Conversion report", "Harvest report", presentation.Row{Label: "Inputs", Value: fmt.Sprint(found)}, presentation.Row{Label: "Failed", Value: fmt.Sprint(failed)}, presentation.Row{Label: "Skipped", Value: fmt.Sprint(skipped)})
	ip, err := webui.AdvertiseIP(cfg.AdvertiseIP, cfg.ListenAddress)
	if err != nil {
		return err
	}
	base := fmt.Sprintf("http://%s:%d", ip, cfg.AdvertisePort)
	token := cfg.RemoteToken
	parsedIP := net.ParseIP(ip)
	publicEndpoint := parsedIP != nil && !parsedIP.IsPrivate() && !parsedIP.IsLoopback()
	if publicEndpoint && token == "" {
		token, err = generateToken()
		if err != nil {
			return fmt.Errorf("generate remote access token: %w", err)
		}
	}
	files, err := webui.Discover(cfg.ServeDirectory, base, token)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("empty husk: no validated existing or newly converted CIA is available")
	}
	for _, f := range files {
		if f.Large {
			present.Warning("%s is at least 4 GiB; FBI may reject its 32-bit size", "%s is a giant cob (at least 4 GiB); FBI may reject it", f.Name)
		}
	}
	ui, err := webui.New(files, base, token)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", cfg.ListenAddress, cfg.Port), Handler: ui.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	serverErr := make(chan error, 1)
	go func() { serverErr <- srv.ListenAndServe() }()
	accessURL := base
	if token != "" {
		accessURL += "/?token=" + url.QueryEscape(token)
	}
	present.Summary("ROM Relay server", "Corn stand is open", presentation.Row{Label: "Catalog", Value: accessURL}, presentation.Row{Label: "FBI", Value: "Scan an individual or batch QR"}, presentation.Row{Label: "Stop", Value: "Ctrl+C"})
	if publicEndpoint {
		present.Warning("Public endpoint enabled; expose only this port and protect the token", "Corn stand faces the public road; guard the gate and secret token")
	}
	select {
	case <-stop:
		present.Info("Shutting down ROM Relay", "Packing up the corn stand")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func runV2(cfg config.Config, present *presentation.Presenter, driverDirectory string, operator *dashboard.Dashboard) (retErr error) {
	store, err := catalog.Open(cfg.DatabasePath, cfg.LibraryDirectory)
	if err != nil {
		return fmt.Errorf("open catalog: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, store.Close()) }()
	store.AddArtifactRoot(cfg.Patching.OutputDirectory)
	externalRoot := filepath.Join(cfg.LibraryDirectory, "external", "antisocial-expert")
	store.AddExternalArtifactRoot(externalRoot)
	ip, err := webui.AdvertiseIP(cfg.Server.AdvertiseIP, cfg.Server.ListenAddress)
	if err != nil {
		return err
	}
	base := strings.TrimRight(cfg.Server.PublicBaseURL, "/")
	if base == "" {
		base = fmt.Sprintf("http://%s:%d", ip, cfg.Server.AdvertisePort)
	}
	viewerToken := cfg.Server.RemoteAccessToken
	boot9Path := fixedBoot9(filepath.Join(driverDirectory, "boot9-3ds"))
	adminToken := cfg.Patching.AdminAccessToken
	if cfg.Patching.Enabled && adminToken == "" {
		adminToken, err = generateToken()
		if err != nil {
			return err
		}
	}
	ui := webui.NewCatalog(store, base, viewerToken, cfg.UITheme, adminToken, cfg.Patching.PatchDirectory, time.Duration(cfg.Patching.RetentionHours)*time.Hour)
	ui.SetReady(false, "Scanning library")
	ui.SetDownloadObserver(func(event webui.DownloadEvent) {
		switch event.Status {
		case "started":
			present.Info("Download started: %s to %s", "Corn delivery started: %s to %s", event.Name, event.RemoteIP)
		case "completed":
			present.Success("Download completed: %s to %s (%d bytes)", "Corn delivered: %s to %s (%d kernels)", event.Name, event.RemoteIP, event.Written)
		default:
			present.Warning("Download stopped: %s to %s (%d/%d bytes)", "Corn delivery stopped: %s to %s (%d/%d kernels)", event.Name, event.RemoteIP, event.Written, event.Expected)
		}
	})
	ui.SetIGDBEnabled(cfg.Metadata.IGDB.Enabled)
	ui.SetMediaDirectory(cfg.Metadata.Libretro.ThumbnailDirectory)
	store.SetArtworkDirectory(cfg.Metadata.Libretro.ThumbnailDirectory)
	var metadataDisplayMu sync.RWMutex
	var metadataDisplay *presentation.Metadata
	metadataStats := map[string]map[string]int{}
	reportROMMetadata := func(p metadata.ROMProgress) {
		metadataDisplayMu.RLock()
		display := metadataDisplay
		metadataDisplayMu.RUnlock()
		if display != nil {
			display.Update(p.Provider, p.GameID, p.Title, p.Status, p.Done, p.Total)
		}
		ui.SetBackground("metadata", webui.BackgroundJob{Active: true, Message: p.Provider + " · " + p.Title + " · " + p.Status, Done: int64(p.Done), Total: int64(p.Total)})
		romDone := int64(0)
		if p.Status != "querying" {
			romDone = 1
		}
		ui.SetBackground("ROM · "+p.Provider+" · "+p.GameID, webui.BackgroundJob{Active: true, Message: p.Title + " · " + p.Status, Done: romDone, Total: 1})
		if p.Status != "querying" {
			metadataDisplayMu.Lock()
			if metadataStats[p.Provider] == nil {
				metadataStats[p.Provider] = map[string]int{}
			}
			metadataStats[p.Provider][p.Status]++
			metadataDisplayMu.Unlock()
		}
	}
	var remoteManager *remotelib.Manager
	if cfg.RemoteSearch.Enabled {
		if err = os.MkdirAll(externalRoot, 0700); err != nil {
			return err
		}
		remoteManager = remotelib.New(store, remotelib.Options{Host: cfg.RemoteSearch.Host, Port: cfg.RemoteSearch.Port, Username: cfg.RemoteSearch.Username, Password: cfg.RemoteSearch.Password, Root: cfg.RemoteSearch.RootDirectory, HostKeySHA256: cfg.RemoteSearch.HostKeySHA256, MaxResults: cfg.RemoteSearch.MaxResults, MaxFileBytes: int64(cfg.RemoteSearch.MaxFileSizeMB) << 20, PlatformDirectories: cfg.RemoteSearch.PlatformDirectories, LibraryRoot: cfg.LibraryDirectory, ExternalRoot: externalRoot, Retention: 24 * time.Hour})
		ui.SetRemoteManager(remoteManager)
	}
	var libretroProvider *metadata.Libretro
	if cfg.Metadata.Libretro.Enabled {
		libretroProvider = metadata.NewLibretro(store, metadata.LibretroOptions{DatabaseDirectory: cfg.Metadata.Libretro.DatabaseDirectory, ThumbnailDirectory: cfg.Metadata.Libretro.ThumbnailDirectory, AutoUpdate: cfg.Metadata.Libretro.AutoUpdate, DownloadThumbnails: cfg.Metadata.Libretro.DownloadThumbnails, ROMProgress: reportROMMetadata, Progress: func(stage string, done, total int) {
			ui.SetBackground("metadata", webui.BackgroundJob{Active: true, Message: "Libretro " + stage, Done: int64(done), Total: int64(total)})
		}})
	}
	var igdbProvider *metadata.IGDB
	if cfg.Metadata.IGDB.Enabled {
		igdbProvider = metadata.NewIGDB(store, metadata.IGDBOptions{ClientID: cfg.Metadata.IGDB.ClientID, ClientSecret: cfg.Metadata.IGDB.ClientSecret, ThumbnailDirectory: cfg.Metadata.Libretro.ThumbnailDirectory, ROMProgress: reportROMMetadata})
	}
	decryptWork := filepath.Join(cfg.Patching.OutputDirectory, ".decrypt-jobs")
	if err = os.MkdirAll(decryptWork, 0700); err != nil {
		return fmt.Errorf("create CIA decrypt staging: %w", err)
	}
	ciaDecryptor := decryptor.New(store, filepath.Join(driverDirectory, "cia-3ds-decryptor"), cfg.LibraryDirectory, decryptWork, 10*time.Minute)
	ui.SetDecryptor(ciaDecryptor)
	var patchManager *patcher.Manager
	if cfg.Patching.Enabled {
		patchManager = patcher.NewManager(store, patcher.ManagerConfig{PatchDirectory: cfg.Patching.PatchDirectory, OutputDirectory: cfg.Patching.OutputDirectory, ArtworkDirectory: cfg.Metadata.Libretro.ThumbnailDirectory, LibraryDirectory: cfg.LibraryDirectory, Retention: time.Duration(cfg.Patching.RetentionHours) * time.Hour, MaxROMBytes: int64(cfg.Patching.MaxROMSizeMB) << 20, MaxPatchBytes: int64(cfg.Patching.MaxPatchSizeMB) << 20, Timeout: time.Duration(cfg.Patching.TimeoutSeconds) * time.Second, Progress: func(job patcher.Job, message string, done, total int64) {
			active := job.Status == "queued" || job.Status == "running"
			background := webui.BackgroundJob{Active: active, Message: message, Done: done, Total: total}
			if job.Status == "failed" || job.Status == "awaiting_confirmation" {
				background.Error = message
			}
			ui.SetBackground("Patch · "+job.ID, background)
			switch job.Status {
			case "failed":
				present.Error("Patch job %s failed: %s", "Corn patch %s failed: %s", job.ID, job.Error)
			case "completed":
				present.Success("Patch job %s completed: %s", "Corn patch %s finished: %s", job.ID, job.OutputName)
			case "awaiting_confirmation":
				present.Warning("Patch job %s needs checksum confirmation", "Corn patch %s needs checksum confirmation", job.ID)
			}
		}})
		ui.SetPatchManager(patchManager)
	}
	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", cfg.Server.ListenAddress, cfg.Server.Port), Handler: ui.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ui.SetConverter(func(_ context.Context, artifactID string, progress func(string, int64, int64)) error {
		conversionProgress := present.NewConversion()
		err := convertCatalogArtifact(ctx, cfg, boot9Path, store, artifactID, func(part string, done, total int64) {
			conversionProgress.Update(part, done, total)
			if progress != nil {
				progress(part, done, total)
			}
		})
		conversionProgress.Stop(err != nil)
		return err
	})
	srv.BaseContext = func(net.Listener) context.Context { return ctx }
	var workers sync.WaitGroup
	startWorker := func(fn func()) {
		workers.Add(1)
		go func() { defer workers.Done(); fn() }()
	}
	shutdownRequest := make(chan struct{}, 1)
	ui.SetShutdown(func() {
		select {
		case shutdownRequest <- struct{}{}:
		default:
		}
	})
	if operator != nil {
		operator.SetQuit(func() {
			select {
			case shutdownRequest <- struct{}{}:
			default:
			}
		})
	}
	cleanup := func() {
		deleted, deferred, failures := store.CleanupExpired(ctx)
		if deleted > 0 || deferred > 0 {
			present.Info("Retention cleanup: %d deleted, %d deferred", "Corn crib cleanup: %d stale cobs removed, %d still being carried", deleted, deferred)
		}
		for _, failure := range failures {
			present.Warning("Retention cleanup will retry: %v", "Corn crib cleanup will try this cob again: %v", failure)
		}
		if patchManager != nil {
			removed, patchFailures := patchManager.CleanupTemporaryPatches(time.Now())
			if removed > 0 {
				present.Info("Temporary patch cleanup: %d removed", "Corn patch cleanup: %d old kernels removed", removed)
			}
			for _, failure := range patchFailures {
				present.Warning("Temporary patch cleanup will retry: %v", "A temporary corn patch resisted cleanup: %v", failure)
			}
		}
	}
	cleanup()
	startWorker(func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanup()
			}
		}
	})
	var scanMu sync.Mutex
	var metadataMu sync.Mutex
	refreshMetadata := func() {
		metadataMu.Lock()
		defer metadataMu.Unlock()
		if ctx.Err() != nil {
			return
		}
		ui.SetBackground("metadata", webui.BackgroundJob{Active: true, Message: "starting metadata sweep"})
		display := present.NewMetadata()
		metadataDisplayMu.Lock()
		metadataDisplay = display
		metadataStats = map[string]map[string]int{}
		metadataDisplayMu.Unlock()
		present.Info("Metadata sweep started", "Metadata harvest started")
		defer func() {
			display.Stop(ctx.Err() != nil)
			metadataDisplayMu.Lock()
			metadataDisplay = nil
			stats := metadataStats
			metadataDisplayMu.Unlock()
			for _, provider := range []string{"Libretro", "IGDB"} {
				counts := stats[provider]
				if counts == nil {
					continue
				}
				total := 0
				failed := 0
				for status, n := range counts {
					total += n
					if strings.HasPrefix(status, "failed:") {
						failed += n
					}
				}
				present.Info("%s results: %d ROMs — %d matched, %d no exact match, %d rate limited, %d failed", "%s harvest: %d ears — %d named, %d unnamed, %d throttled, %d failed", provider, total, counts["matched"], counts["no exact match"], counts["rate limited"], failed)
			}
			ui.ClearBackgroundPrefix("ROM · ")
			ui.SetBackground("metadata", webui.BackgroundJob{Active: false, Message: "metadata sweep complete"})
		}()
		staleBefore := time.Now().Add(-time.Duration(cfg.MetadataRefreshDays) * 24 * time.Hour)
		enrichBefore := staleBefore
		_, _, nameMatchStateErr := store.SchedulerState(ctx, "libretro_exact_name_match_v1")
		if nameMatchStateErr != nil {
			enrichBefore = time.Now().Add(time.Second)
		}
		if libretroProvider != nil {
			shouldSync := !cfg.Metadata.Libretro.AutoUpdate
			if cfg.Metadata.Libretro.AutoUpdate {
				last, providerStatus, stateErr := store.SchedulerState(ctx, "libretro_update")
				shouldSync = stateErr != nil || last.Before(staleBefore) || strings.Contains(providerStatus, ";")
			}
			if shouldSync {
				platforms, records, syncErr := libretroProvider.Sync(ctx)
				if syncErr != nil && ctx.Err() == nil {
					present.Warning("Libretro update partially available: %v", "Libretro almanac partly wilted: %v", syncErr)
				}
				if platforms > 0 {
					// Games checked before the new index existed need one immediate
					// second chance; otherwise their no-match timestamp suppresses art.
					enrichBefore = time.Now().Add(time.Second)
					present.Success("Libretro index: %d platforms, %d records", "Libretro almanac: %d fields, %d records", platforms, records)
				}
			}
			matched, enrichErr := libretroProvider.Enrich(ctx, enrichBefore)
			if enrichErr != nil && ctx.Err() == nil {
				present.Warning("Libretro enrichment will retry: %v", "Libretro labels will sprout again: %v", enrichErr)
			} else if matched > 0 {
				present.Success("Libretro matched %d games", "Libretro named %d ears", matched)
			}
			if enrichErr == nil {
				_ = store.SetSchedulerState(context.Background(), "libretro_exact_name_match_v1", time.Now(), "complete")
			}
		}
		if igdbProvider != nil {
			if ctx.Err() != nil {
				return
			}
			updated, enrichErr := igdbProvider.Enrich(ctx, staleBefore)
			if enrichErr != nil && ctx.Err() == nil {
				present.Warning("IGDB enrichment will retry: %v", "IGDB labels will sprout again: %v", enrichErr)
			} else if updated > 0 {
				present.Success("IGDB enriched %d games", "IGDB dressed %d ears", updated)
			}
		}
		present.Success("Metadata sweep complete", "Metadata harvest complete")
	}
	reconcile := func() {
		scanMu.Lock()
		defer scanMu.Unlock()
		ui.SetScanState(true, nil)
		e := store.Reconcile(ctx)
		if errors.Is(e, context.Canceled) {
			ui.SetScanState(false, nil)
			return
		}
		ui.SetScanState(false, e)
		if e != nil {
			present.Error("Catalog reconciliation failed: %v", "Corn crib count failed: %v", e)
		} else if games, e := store.Games(ctx); e == nil {
			present.Success("Catalog ready: %d games", "Corn crib stocked: %d games", len(games))
		}
	}
	initialCatalog := make(chan struct{})
	startWorker(func() {
		reconcile()
		close(initialCatalog)
	})
	initialConversion := make(chan struct{})
	startWorker(func() {
		defer close(initialConversion)
		select {
		case <-initialCatalog:
		case <-ctx.Done():
			return
		}
		ui.SetReady(false, "Checking 3DS conversion queue")
		ui.SetBackground("conversion", webui.BackgroundJob{Active: true, Message: "checking 3DS conversion queue"})
		err := convertV2(ctx, cfg, boot9Path, present, func(name, part string, done, total int64) {
			message := name
			if part != "" {
				message += " · " + part
			}
			ui.SetBackground("conversion", webui.BackgroundJob{Active: true, Message: message, Done: done, Total: total})
		}, func(string) { reconcile() })
		if err != nil {
			if ctx.Err() == nil {
				present.Error("3DS conversion failed: %v", "3DS harvest failed: %v", err)
			}
		}
		ui.SetBackground("conversion", webui.BackgroundJob{Active: false, Message: "conversion queue complete"})
	})
	if libretroProvider != nil || igdbProvider != nil {
		startWorker(func() {
			select {
			case <-initialCatalog:
			case <-ctx.Done():
				return
			}
			refreshMetadata()
		})
	}
	if libretroProvider != nil || igdbProvider != nil {
		startWorker(func() {
			// The refresh function performs its own staleness checks. An hourly
			// wake-up lets interrupted/offline providers recover promptly without
			// re-fetching healthy indexes before metadata_refresh_days.
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					refreshMetadata()
				}
			}
		})
	}
	if cfg.LibraryScanIntervalHours > 0 {
		startWorker(func() {
			ticker := time.NewTicker(time.Duration(cfg.LibraryScanIntervalHours) * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					reconcile()
				}
			}
		})
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- srv.ListenAndServe() }()
	initialPatchIndex := make(chan struct{})
	if patchManager != nil {
		startWorker(func() {
			defer close(initialPatchIndex)
			ui.SetBackground("patch_index", webui.BackgroundJob{Active: true, Message: "indexing reusable patches"})
			present.Info("Patch recipe indexing started", "Patch field indexing started")
			if scanErr := patchManager.ScanRecipes(ctx); scanErr != nil && ctx.Err() == nil {
				ui.SetBackground("patch_index", webui.BackgroundJob{Error: scanErr.Error(), Message: "patch indexing failed"})
				present.Warning("Patch recipe index will retry: %v", "Patch recipe field will be replanted: %v", scanErr)
			} else {
				ui.SetBackground("patch_index", webui.BackgroundJob{Active: false, Message: "patch index ready"})
				present.Success("Patch recipe index ready", "Patch field index ready")
			}
		})
	} else {
		close(initialPatchIndex)
	}
	viewerURL := base + "/"
	if viewerToken != "" {
		viewerURL += "?token=" + url.QueryEscape(viewerToken)
	}
	adminURL := base + "/admin/patches?admin_token=" + url.QueryEscape(adminToken)
	startWorker(func() {
		for _, stage := range []struct {
			wait  <-chan struct{}
			label string
		}{{initialCatalog, "Preparing catalog"}, {initialConversion, "Finishing 3DS conversions"}, {initialPatchIndex, "Indexing patch recipes"}} {
			ui.SetReady(false, stage.label)
			select {
			case <-stage.wait:
			case <-ctx.Done():
				return
			}
		}
		ui.SetReady(true, "ready")
		present.Summary("ROM Relay ready", "ROM Relay corn stand", presentation.Row{Label: "Theme", Value: cfg.UITheme}, presentation.Row{Label: "Catalog", Value: viewerURL}, presentation.Row{Label: "Library", Value: cfg.LibraryDirectory})
		if cfg.Patching.Enabled {
			present.Warning("Private admin URL (do not share): %s", "Secret silo door (do not share): %s", adminURL)
		}
	})
	if operator != nil {
		operator.SetSource(func(statsCtx context.Context) dashboard.Snapshot {
			catalogStats, statsErr := store.RuntimeStats(statsCtx)
			httpStats := ui.HTTPMetrics()
			state := ui.OperatorState()
			snapshot := dashboard.Snapshot{
				Status: "setup in progress · " + state.Setup, LastError: state.Error,
				Games: catalogStats.Games, Files: catalogStats.Files, ActiveDownloads: catalogStats.ActiveDownloads,
				HTTPRequests: httpStats.Requests, HTTPBytes: httpStats.ResponseBytes, HTTPActive: httpStats.Active,
				DBOpen: catalogStats.DB.OpenConnections, DBInUse: catalogStats.DB.InUse, DBIdle: catalogStats.DB.Idle,
				DBWait: catalogStats.DB.WaitCount, WAL: catalogStats.WAL,
			}
			if state.Ready {
				snapshot.PublicURL, snapshot.AdminURL, snapshot.Status = viewerURL, adminURL, "serving"
			}
			if state.Scanning {
				snapshot.Status = "scanning library"
			}
			if statsErr != nil {
				snapshot.LastError = statsErr.Error()
			}
			for _, row := range catalogStats.Platforms {
				snapshot.Platforms = append(snapshot.Platforms, dashboard.PlatformRow{Platform: row.Platform, Games: row.Games, Base: row.Base, Patched: row.Patched, Files: row.Files, EncryptedCIA: row.EncryptedCIA, UnencryptedCIA: row.UnencryptedCIA})
			}
			keys := make([]string, 0, len(state.Jobs))
			for key, job := range state.Jobs {
				if job.Active || job.Recent || job.Error != "" {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			for _, key := range keys {
				job := state.Jobs[key]
				line := job.Message
				if line == "" {
					line = key
				}
				snapshot.Jobs = append(snapshot.Jobs, dashboard.JobProgress{Message: line, Done: job.Done, Total: job.Total, Error: job.Error})
			}
			return snapshot
		})
	}
	boot9 := "not found (only needed for Original-NCCH encrypted .3ds/.cci)"
	if boot9Path != "" {
		boot9 = boot9Path + " (validated when conversion starts)"
	}
	decryptStatus := "not installed (encrypted downloads remain available)"
	if ok, _ := ciaDecryptor.Available(); ok {
		decryptStatus = "Redux-compatible driver ready"
	}
	present.Info("Setup in progress. URLs will appear when the catalog is ready.", "The corn crib is being prepared. URLs will sprout when it is ready.")
	present.Info("boot9: %s; CIA decryptor: %s", "boot9: %s; CIA decryptor: %s", boot9, decryptStatus)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	var runErr error
	select {
	case <-stop:
		present.Info("Shutting down ROM Relay", "Packing up the corn stand")
	case <-shutdownRequest:
		present.Info("Admin requested a clean shutdown", "The silo door requested a clean shutdown")
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = err
		}
	}
	cancel()
	shutdown, c := context.WithTimeout(context.Background(), 10*time.Second)
	shutdownErr := srv.Shutdown(shutdown)
	c()
	if shutdownErr != nil {
		_ = srv.Close()
	}
	if patchManager != nil {
		patchManager.Shutdown()
	}
	if remoteManager != nil {
		remoteManager.Shutdown()
	}
	workers.Wait()
	present.Success("Shutdown complete; HTTP, workers, and SQLite are closed", "Corn stand packed; HTTP, workers, and SQLite are closed")
	return errors.Join(runErr, shutdownErr)
}

func convertV2(ctx context.Context, cfg config.Config, boot9Path string, present *presentation.Presenter, status func(name, part string, done, total int64), ready func(path string)) error {
	conv, err := converter.New(boot9Path)
	if err != nil {
		return err
	}
	sourceRoot := filepath.Join(cfg.LibraryDirectory, "3ds", "3ds")
	ciaRoot := filepath.Join(cfg.LibraryDirectory, "3ds", "cia")
	outputRoot := filepath.Join(ciaRoot, "encrypted")
	stage := filepath.Join(outputRoot, ".romrelay-staging")
	if err = os.MkdirAll(stage, 0700); err != nil {
		return err
	}
	// A hard power loss or OOM cannot run cancellation cleanup. Staging is
	// private and never served, so discard only its known temporary files before
	// resuming the serial queue.
	entries, readErr := os.ReadDir(stage)
	if readErr != nil {
		return readErr
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "convert-") && strings.HasSuffix(entry.Name(), ".tmp") {
			if removeErr := os.Remove(filepath.Join(stage, entry.Name())); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				present.Warning("Could not clear interrupted conversion %s: %v", "Could not clear interrupted cob %s: %v", entry.Name(), removeErr)
			}
		}
	}
	return filepath.WalkDir(sourceRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".3ds" && ext != ".cci" {
			return nil
		}
		dst := filepath.Join(outputRoot, strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))+".cia")
		legacyDst := filepath.Join(ciaRoot, filepath.Base(dst))
		if _, err := os.Stat(dst); err == nil {
			present.Info("Skipping %s: CIA already exists", "Skipping ear %s: CIA cob already exists", d.Name())
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := os.Stat(legacyDst); err == nil {
			present.Info("Skipping %s: legacy encrypted CIA already exists", "Skipping ear %s: old encrypted cob already exists", d.Name())
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		tmp, err := os.CreateTemp(stage, "convert-*.tmp")
		if err != nil {
			return err
		}
		tmpPath := tmp.Name()
		tmp.Close()
		os.Remove(tmpPath)
		present.Info("Converting %s to encrypted CIA", "Shucking %s into an encrypted CIA cob", d.Name())
		if status != nil {
			status(d.Name(), "preparing", 0, 0)
		}
		conversionProgress := present.NewConversion()
		if err = conv.Convert(ctx, path, tmpPath, func(part string, done, total int64) {
			conversionProgress.Update(part, done, total)
			if status != nil {
				status(d.Name(), part, done, total)
			}
		}); err != nil {
			conversionProgress.Stop(true)
			os.Remove(tmpPath)
			present.Error("Conversion failed for %s: %v", "Ear %s spoiled during shucking: %v", d.Name(), err)
			return nil
		}
		conversionProgress.Stop(false)
		if err = converter.Validate(tmpPath); err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("validate generated %s: %w", d.Name(), err)
		}
		if err = os.Rename(tmpPath, dst); err != nil {
			os.Remove(tmpPath)
			return err
		}
		present.Success("Ready: %s", "Fresh encrypted cob ready: %s", filepath.Base(dst))
		if ready != nil {
			ready(dst)
		}
		return nil
	})
}

func convertCatalogArtifact(ctx context.Context, cfg config.Config, boot9Path string, store *catalog.Store, artifactID string, progress converter.Progress) (err error) {
	a, err := store.Artifact(ctx, artifactID)
	if err != nil {
		return err
	}
	ext := strings.ToLower(a.Extension)
	if a.Platform != "3ds" || (ext != ".3ds" && ext != ".cci") {
		return errors.New("catalog artifact is not a .3ds or .cci image")
	}
	conv, err := converter.New(boot9Path)
	if err != nil {
		return err
	}
	ciaRoot := filepath.Join(cfg.LibraryDirectory, "3ds", "cia")
	outputRoot := filepath.Join(ciaRoot, "encrypted")
	name := strings.TrimSuffix(a.Name, filepath.Ext(a.Name)) + ".cia"
	final := filepath.Join(outputRoot, name)
	legacy := filepath.Join(ciaRoot, name)
	for _, path := range []string{final, legacy} {
		if _, statErr := os.Stat(path); statErr == nil {
			return fmt.Errorf("CIA output already exists: %s", filepath.Base(path))
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	stage := filepath.Join(outputRoot, ".romrelay-staging")
	if err = os.MkdirAll(stage, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stage, "convert-web-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if closeErr := tmp.Close(); closeErr != nil {
		os.Remove(tmpPath)
		return closeErr
	}
	os.Remove(tmpPath)
	defer os.Remove(tmpPath)
	if err = conv.Convert(ctx, a.Path, tmpPath, progress); err != nil {
		return err
	}
	if err = converter.Validate(tmpPath); err != nil {
		return fmt.Errorf("converted CIA validation: %w", err)
	}
	if err = os.Link(tmpPath, final); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("CIA output already exists: %s", name)
		}
		return fmt.Errorf("publish converted CIA: %w", err)
	}
	if err = store.Reconcile(ctx); err != nil {
		os.Remove(final)
		return err
	}
	return nil
}

func fixedBoot9(directory string) string {
	for _, name := range []string{"boot9.bin", "boot9_prot.bin"} {
		path := filepath.Join(directory, name)
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

func generateToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
