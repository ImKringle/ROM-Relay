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
	"runtime"
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
	"romrelay/internal/pokedex"
	"romrelay/internal/pokemon"
	"romrelay/internal/pokemonvault"
	"romrelay/internal/presentation"
	"romrelay/internal/randomizer"
	remotelib "romrelay/internal/remote"
	"romrelay/internal/savevault"
	"romrelay/internal/threedscrypt"
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

// healthProbe queries the local health endpoint and returns nil when the server
// is responding. Any HTTP status below 500 counts as healthy, a token-protected
// deployment answers /healthz with 401, which still proves the process is up.
// Designed for a container HEALTHCHECK where no shell or curl is available.
func healthProbe(cfg config.Config) error {
	host := cfg.Server.ListenAddress
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	url := fmt.Sprintf("http://%s/healthz", net.JoinHostPort(host, fmt.Sprint(cfg.Server.Port)))
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("health endpoint returned %d", resp.StatusCode)
	}
	return nil
}

func run() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	configDir := flag.String("config-dir", filepath.Dir(exe), "directory containing config.ini and .env")
	healthcheck := flag.Bool("healthcheck", false, "probe the local health endpoint and exit 0/1 (for container HEALTHCHECK)")
	flag.Parse()
	cfg, err := config.Load(*configDir)
	if err != nil {
		return err
	}
	if *healthcheck {
		return healthProbe(cfg)
	}
	theme := cfg.UITheme
	var operator *dashboard.Dashboard
	var present *presentation.Presenter
	if dashboard.TerminalAvailable(os.Stdin, os.Stdout) {
		operator = dashboard.New(theme)
		operator.Start()
		present = presentation.New(theme, operator, operator)
		present.SetLiveSink(operator)
	} else {
		present = presentation.New(theme, os.Stdout, os.Stderr)
	}
	activePresenter = present
	for _, warning := range cfg.Warnings {
		present.Warning("%s", "%s", warning)
	}
	runErr := runV2(cfg, present, filepath.Join(filepath.Dir(exe), "internal", "drivers"), operator, *configDir)
	if operator != nil {
		runErr = errors.Join(runErr, operator.Stop())
		activePresenter = presentation.New(theme, os.Stdout, os.Stderr)
	}
	return runErr
}

func runV2(cfg config.Config, present *presentation.Presenter, driverDirectory string, operator *dashboard.Dashboard, configDir string) (retErr error) {
	store, err := catalog.Open(cfg.DatabaseDSN(), cfg.LibraryDirectory)
	if err != nil {
		return fmt.Errorf("open catalog: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, store.Close()) }()
	store.AddArtifactRoot(cfg.Patching.OutputDirectory)
	store.AddArtifactRoot(cfg.Randomizer.OutputDirectory)
	externalRoot := filepath.Join(cfg.LibraryDirectory, "external", "antisocial-expert")
	store.AddExternalArtifactRoot(externalRoot)
	// Results from the Game Library's ad-hoc convert/encrypt/decrypt uploads:
	// a normal, temporary (expiring-unless-retained) artifact root, the same
	// shape a patch job's output already is.
	uploadResultsDir := filepath.Join(cfg.Patching.OutputDirectory, ".upload-results")
	if err := os.MkdirAll(uploadResultsDir, 0700); err != nil {
		return fmt.Errorf("create upload-results directory: %w", err)
	}
	store.AddArtifactRoot(uploadResultsDir)
	if err := seedAdmin(context.Background(), store, cfg, present); err != nil {
		return fmt.Errorf("initialize admin account: %w", err)
	}
	// A configured public_base_url takes precedence and makes LAN-adapter
	// detection unnecessary, important in containers, where auto-detection is
	// ambiguous or finds only the compose-network address.
	base := strings.TrimRight(cfg.Server.PublicBaseURL, "/")
	if base == "" {
		ip, err := webui.AdvertiseIP(cfg.Server.AdvertiseIP, cfg.Server.ListenAddress)
		if err != nil {
			return err
		}
		base = fmt.Sprintf("http://%s:%d", ip, cfg.Server.AdvertisePort)
	}
	viewerToken := cfg.Server.RemoteAccessToken
	boot9Path := fixedBoot9(filepath.Join(driverDirectory, "3ds"))
	adminToken := cfg.Patching.AdminAccessToken
	if cfg.Patching.Enabled && adminToken == "" {
		adminToken, err = generateToken()
		if err != nil {
			return err
		}
	}
	ui := webui.NewCatalog(store, base, viewerToken, cfg.UITheme, adminToken, cfg.Patching.PatchDirectory, time.Duration(cfg.Patching.RetentionHours)*time.Hour)
	ui.SetReady(false, "Indexing library")
	store.SetScanProgress(func(files int, _ int64) {
		message := fmt.Sprintf("Indexing library, %d files", files)
		ui.SetScanStage(message)
	})
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
	ui.SetConfig(cfg, configDir)
	ui.SetIGDBEnabled(cfg.Metadata.IGDB.Enabled)
	ui.SetMediaDirectory(cfg.Metadata.Libretro.ThumbnailDirectory)
	ui.SetUploadResultsDir(uploadResultsDir)
	if cfg.PokemonSidecarURL != "" {
		ui.SetPokemonClient(pokemon.NewClient(cfg.PokemonSidecarURL))
		present.Info("Pokémon save ingestion enabled (sidecar %s)", "Poké save shucking enabled (sidecar %s)", cfg.PokemonSidecarURL)
	}
	if cfg.NDecryptSidecarURL != "" {
		ui.SetCryptClient(threedscrypt.NewClient(cfg.NDecryptSidecarURL))
		present.Info("3DS/DS encrypt-decrypt upload enabled (sidecar %s)", "3DS/DS crypt-shucking enabled (sidecar %s)", cfg.NDecryptSidecarURL)
	}
	var randomizerManager *randomizer.Manager
	if cfg.RandomizerSidecarURL != "" {
		randomizerManager = randomizer.NewManager(store, randomizer.NewClient(cfg.RandomizerSidecarURL), randomizer.ManagerConfig{OutputDirectory: cfg.Randomizer.OutputDirectory, ArtworkDirectory: cfg.Metadata.Libretro.ThumbnailDirectory, Retention: time.Duration(cfg.Randomizer.RetentionHours) * time.Hour, MaxROMBytes: int64(cfg.Randomizer.MaxROMSizeMB) << 20, Timeout: time.Duration(cfg.Randomizer.TimeoutSeconds) * time.Second, Progress: func(job randomizer.Job, message string, done, total int64) {
			active := job.Status == "queued" || job.Status == "running"
			background := webui.BackgroundJob{Active: active, Message: message, Done: done, Total: total}
			if job.Status == "failed" {
				background.Error = message
			}
			ui.SetBackground("Randomizer · "+job.ID, background)
			switch job.Status {
			case "failed":
				present.Error("Randomizer job %s failed: %s", "Corn randomizer job %s failed: %s", job.ID, job.Error)
			case "completed":
				present.Success("Randomizer job %s completed: %s", "Corn randomizer job %s finished: %s", job.ID, job.OutputName)
			}
		}})
		ui.SetRandomizerManager(randomizerManager)
		present.Info("ROM Randomizer enabled (sidecar %s)", "Corn randomizer enabled (sidecar %s)", cfg.RandomizerSidecarURL)
	}
	// The save vault works without the sidecar: saves are stored either way, and
	// a recognised Pokémon save additionally feeds the Dex.
	if err := os.MkdirAll(cfg.SaveDirectory, 0o755); err != nil {
		return fmt.Errorf("create save directory: %w", err)
	}
	ui.SetSaveVault(savevault.NewDirectoryVault(cfg.SaveDirectory))
	// Every ingested Pokémon also gets a standalone .pkm file (see the
	// PokeHome design doc), a sibling of the save vault directory rather
	// than its own config key, the same derived-not-configured choice the
	// save vault's own directory made before it.
	pokemonDir := filepath.Join(filepath.Dir(cfg.SaveDirectory), "pokemon")
	if err := os.MkdirAll(pokemonDir, 0o755); err != nil {
		return fmt.Errorf("create pokemon directory: %w", err)
	}
	ui.SetPokemonVault(pokemonvault.NewDirectoryVault(pokemonDir))
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
	// Pokédex reference data: a one-time fill of static species/evolution data.
	// It never blocks the catalog, and the Dex works without it.
	var pokedexIngest *pokedex.Ingest
	if cfg.Metadata.PokeAPI.Enabled {
		// The on-disk api-data checkout is preferred: same JSON, versioned, no
		// network. The live API remains the fallback for checkouts without the
		// submodule.
		var dexClient *pokedex.Client
		source := "PokeAPI"
		if info, statErr := os.Stat(cfg.Metadata.PokeAPI.DataDirectory); statErr == nil && info.IsDir() {
			dexClient = pokedex.NewLocalClient(cfg.Metadata.PokeAPI.DataDirectory)
			source = "local PokeAPI data"
		}
		pokedexIngest = pokedex.New(store, pokedex.Options{
			Client: dexClient,
			Progress: func(done, total int) {
				ui.SetBackground("pokedex_metadata", webui.BackgroundJob{
					Active: done < total, Recent: done >= total,
					Message: "Pokédex data from " + source, Done: int64(done), Total: int64(total),
				})
			},
		})
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
	ui.SetConvertUpload(func(convertCtx context.Context, name string, data []byte, progress func(string, int64, int64)) ([]byte, bool, error) {
		conv, err := converter.New(boot9Path)
		if err != nil {
			return nil, false, err
		}
		in, err := os.CreateTemp(uploadResultsDir, "in-*"+strings.ToLower(filepath.Ext(name)))
		if err != nil {
			return nil, false, err
		}
		inPath := in.Name()
		defer os.Remove(inPath)
		if _, err = in.Write(data); err != nil {
			in.Close()
			return nil, false, err
		}
		if err = in.Close(); err != nil {
			return nil, false, err
		}
		outPath := inPath + ".cia"
		defer os.Remove(outPath)
		encrypted, err := conv.Convert(convertCtx, inPath, outPath, progress)
		if err != nil {
			return nil, false, err
		}
		if err = converter.Validate(outPath); err != nil {
			return nil, false, err
		}
		result, err := os.ReadFile(outPath)
		return result, encrypted, err
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
		// Expired sessions are rows nothing will ever read again; without this
		// they accumulated forever (the method existed but was never called).
		if err := store.CleanupExpiredSessions(ctx); err != nil && ctx.Err() == nil {
			present.Warning("Session cleanup will retry: %v", "Session cleanup will try again: %v", err)
		}
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
				present.Info("%s results: %d ROMs, %d matched, %d no exact match, %d rate limited, %d failed", "%s harvest: %d ears, %d named, %d unnamed, %d throttled, %d failed", provider, total, counts["matched"], counts["no exact match"], counts["rate limited"], failed)
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
		started := time.Now()
		e := store.Reconcile(ctx)
		if errors.Is(e, context.Canceled) {
			ui.SetScanState(false, nil)
			return
		}
		ui.SetScanState(false, e)
		if e != nil {
			present.Error("Catalog reconciliation failed: %v", "Corn crib count failed: %v", e)
		} else if games, e := store.Games(ctx); e == nil {
			present.Success("Catalog ready: %d games (%s)", "Corn crib stocked: %d games (%s)", len(games), time.Since(started).Round(time.Millisecond))
		}
	}
	var hashMu sync.Mutex
	hashPending := func() {
		hashMu.Lock()
		defer hashMu.Unlock()
		started := time.Now()
		var lastHashLog time.Time
		var hashedAny bool
		var lastTotal int
		var lastBytes int64
		err := store.HashPending(ctx, func(done, total int, hashedBytes int64) {
			hashedAny = true
			lastTotal, lastBytes = total, hashedBytes
			message := fmt.Sprintf("Hashing ROMs, %d/%d, %.1f GB", done, total, float64(hashedBytes)/(1<<30))
			ui.SetBackground("hashing", webui.BackgroundJob{Active: true, Message: message, Done: int64(done), Total: int64(total)})
			if time.Since(lastHashLog) >= 5*time.Second {
				lastHashLog = time.Now()
				elapsed := time.Since(started)
				rate := float64(hashedBytes) / elapsed.Seconds() / (1 << 20)
				present.Info("%s (%s elapsed, %.1f MB/s avg)", "%s (%s elapsed, %.1f MB/s avg)", message, elapsed.Round(time.Second), rate)
			}
		})
		ui.SetBackground("hashing", webui.BackgroundJob{Active: false, Message: "library hashing complete"})
		if err != nil && ctx.Err() == nil {
			present.Warning("Library hashing will retry: %v", "Corn tallying will retry: %v", err)
		} else if err == nil && hashedAny {
			present.Success("Library hashing complete: %d files, %.1f GB (%s)", "Corn tally complete: %d ears, %.1f GB (%s)",
				lastTotal, float64(lastBytes)/(1<<30), time.Since(started).Round(time.Millisecond))
		}
	}
	initialCatalog := make(chan struct{})
	startWorker(func() {
		reconcile()
		close(initialCatalog)
	})
	// Content hashing runs after the fast header index, in the background, so the
	// catalog is browsable immediately. Smallest files are hashed first, so
	// metadata for most of a library fills in quickly. Metadata enrichment waits
	// on this because it matches by hash.
	initialHash := make(chan struct{})
	startWorker(func() {
		defer close(initialHash)
		select {
		case <-initialCatalog:
		case <-ctx.Done():
			return
		}
		hashPending()
	})
	initialConversion := make(chan struct{})
	startWorker(func() {
		defer close(initialConversion)
		select {
		case <-initialCatalog:
		case <-ctx.Done():
			return
		}
		// Conversion runs after the catalog is already servable; report it as a
		// background job only. It must not call SetReady(false), that would race
		// with the readiness worker and knock a serving catalog back offline.
		ui.SetBackground("conversion", webui.BackgroundJob{Active: true, Message: "checking 3DS conversion queue"})
		err := convertV2(ctx, cfg, boot9Path, present, func(name, part string, done, total int64) {
			message := name
			if part != "" {
				message += " · " + part
			}
			ui.SetBackground("conversion", webui.BackgroundJob{Active: true, Message: message, Done: done, Total: total})
		}, func(string) { reconcile(); hashPending() })
		if err != nil {
			if ctx.Err() == nil {
				present.Error("3DS conversion failed: %v", "3DS harvest failed: %v", err)
			}
		}
		ui.SetBackground("conversion", webui.BackgroundJob{Active: false, Message: "conversion queue complete"})
	})
	if libretroProvider != nil || igdbProvider != nil {
		startWorker(func() {
			// Metadata matches by content hash, so wait for the background hash
			// pass rather than the header index.
			select {
			case <-initialHash:
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
	if pokedexIngest != nil {
		// The run-once worker and the hourly ticker share this, exactly as the
		// metadata sweep does: a first fill slower than an hour would otherwise
		// have the tick start a second ingest alongside it.
		var pokedexMu sync.Mutex
		refreshPokedex := func() {
			pokedexMu.Lock()
			defer pokedexMu.Unlock()
			started := time.Now()
			stored, ingestErr := pokedexIngest.Run(ctx, pokedex.MaxDex)
			switch {
			case ingestErr != nil && ctx.Err() == nil:
				present.Warning("Pokédex data will retry: %v", "The Pokédex will fill later: %v", ingestErr)
			case ingestErr == nil && stored > 0:
				present.Success("Pokédex data stored for %d species (%s)", "Pokédex data stored for %d ears (%s)", stored, time.Since(started).Round(time.Millisecond))
			}
		}
		startWorker(func() {
			// Pokédex reference data is keyed by dex number, not ROM content hash,
			// and has nothing to do with the ROM library. It only needs the
			// catalog to exist so the web UI is up, not a full hash pass.
			select {
			case <-initialCatalog:
			case <-ctx.Done():
				return
			}
			refreshPokedex()
		})
		startWorker(func() {
			// Same hourly recovery wake-up as the ROM metadata providers, for
			// interrupted/offline runs. Run() is resumable and skips species it
			// already stored, so a healthy re-run is cheap.
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					refreshPokedex()
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
					hashPending()
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
		// The catalog is gated only on the initial library scan. 3DS conversion
		// and patch-recipe indexing can take a long time on a large library
		// (multi-gigabyte 3DS images over a slow bind mount) and continue in the
		// background with progress shown in the catalog header; blocking the whole
		// site on them made a fresh boot with a big library look hung.
		ui.SetReady(false, "Preparing catalog")
		select {
		case <-initialCatalog:
		case <-ctx.Done():
			return
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
	if randomizerManager != nil {
		randomizerManager.Shutdown()
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
	stage := filepath.Join(ciaRoot, ".romrelay-staging")
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

	// Gather the queue first, the same shape catalog.HashPending indexes
	// pending files before hashing them: a cheap directory walk plus a stat
	// per candidate, so what follows is only real conversion work.
	var pending []string
	walkErr := filepath.WalkDir(sourceRoot, func(path string, d os.DirEntry, walkErr error) error {
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
		name := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())) + ".cia"
		if existing, existsErr := ciaAlreadyConverted(ciaRoot, name); existsErr != nil {
			return existsErr
		} else if existing != "" {
			present.Info("Skipping %s: CIA already exists", "Skipping ear %s: CIA cob already exists", d.Name())
			return nil
		}
		pending = append(pending, path)
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	if len(pending) == 0 {
		return nil
	}

	// Conversion is CPU-bound (AES-CTR plus SHA-256 over every byte) and disk
	// I/O bound, the same profile as ROM hashing, so it fans out across the
	// same GOMAXPROCS-bounded worker pool shape as catalog.HashPending and
	// pokedex.Ingest.runParallel (GOMAXPROCS, not NumCPU, so this stays correct
	// under a container CPU quota). converter.Converter carries no mutable
	// state across Convert calls, and each file gets its own temp path and its
	// own *presentation.Conversion tracker, so workers never share write targets.
	workers := runtime.GOMAXPROCS(0)
	if workers > len(pending) {
		workers = len(pending)
	}
	jobs := make(chan string)
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				if err := convertOne(ctx, conv, path, ciaRoot, stage, present, status, ready); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
				}
			}
		}()
	}
feed:
	for _, path := range pending {
		select {
		case jobs <- path:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// convertOne converts a single 3DS/CCI ROM to a CIA, landing in cia/encrypted
// or cia/unencrypted to match the source's own crypt state (never changed by
// conversion itself). It is the per-file body convertV2's worker pool runs
// concurrently: every path it touches (the temp file, the destination, the
// progress tracker) is unique to this one file, so no coordination with
// other in-flight conversions is needed beyond what ready (reconcile + hash)
// already serializes internally.
func convertOne(ctx context.Context, conv *converter.Converter, path, ciaRoot, stage string, present *presentation.Presenter, status func(name, part string, done, total int64), ready func(path string)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := filepath.Base(path)
	ciaName := strings.TrimSuffix(name, filepath.Ext(name)) + ".cia"
	tmp, err := os.CreateTemp(stage, "convert-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	os.Remove(tmpPath)
	present.Info("Converting %s to CIA", "Shucking %s into a CIA cob", name)
	if status != nil {
		status(name, "preparing", 0, 0)
	}
	conversionProgress := present.NewConversion()
	encrypted, err := conv.Convert(ctx, path, tmpPath, func(part string, done, total int64) {
		conversionProgress.Update(part, done, total)
		if status != nil {
			status(name, part, done, total)
		}
	})
	if err != nil {
		conversionProgress.Stop(true)
		os.Remove(tmpPath)
		present.Error("Conversion failed for %s: %v", "Ear %s spoiled during shucking: %v", name, err)
		return nil
	}
	conversionProgress.Stop(false)
	if err = converter.Validate(tmpPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("validate generated %s: %w", name, err)
	}
	outputRoot := filepath.Join(ciaRoot, ciaSubfolder(encrypted))
	if err = os.MkdirAll(outputRoot, 0755); err != nil {
		os.Remove(tmpPath)
		return err
	}
	dst := filepath.Join(outputRoot, ciaName)
	if err = os.Rename(tmpPath, dst); err != nil {
		os.Remove(tmpPath)
		return err
	}
	present.Success("Ready: %s", "Fresh cob ready: %s", filepath.Base(dst))
	if ready != nil {
		ready(dst)
	}
	return nil
}

// ciaSubfolder is the encrypted/unencrypted folder name for a CIA, off the
// same detected-state bool converter.Convert returns.
func ciaSubfolder(encrypted bool) string {
	if encrypted {
		return "encrypted"
	}
	return "unencrypted"
}

// ciaAlreadyConverted reports whether a CIA for this source name exists
// under either destination, regardless of which crypt state it landed in:
// the source's own state isn't known until it's actually read, so both
// possible destinations (plus the pre-split legacy flat layout) are checked.
func ciaAlreadyConverted(ciaRoot, name string) (string, error) {
	candidates := []string{
		filepath.Join(ciaRoot, "encrypted", name),
		filepath.Join(ciaRoot, "unencrypted", name),
		filepath.Join(ciaRoot, name),
	}
	for _, path := range candidates {
		if _, statErr := os.Stat(path); statErr == nil {
			return path, nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
	}
	return "", nil
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
	name := strings.TrimSuffix(a.Name, filepath.Ext(a.Name)) + ".cia"
	if existing, existsErr := ciaAlreadyConverted(ciaRoot, name); existsErr != nil {
		return existsErr
	} else if existing != "" {
		return fmt.Errorf("CIA output already exists: %s", filepath.Base(existing))
	}
	stage := filepath.Join(ciaRoot, ".romrelay-staging")
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
	encrypted, err := conv.Convert(ctx, a.Path, tmpPath, progress)
	if err != nil {
		return err
	}
	if err = converter.Validate(tmpPath); err != nil {
		return fmt.Errorf("converted CIA validation: %w", err)
	}
	outputRoot := filepath.Join(ciaRoot, ciaSubfolder(encrypted))
	if err = os.MkdirAll(outputRoot, 0755); err != nil {
		return err
	}
	final := filepath.Join(outputRoot, name)
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

// seedAdmin ensures the built-in admin account exists and has a password.
// ADMIN_PASSWORD (when set) is applied every start so it can be reset; otherwise
// a password is generated and printed once on first run, and left untouched
// thereafter.
func seedAdmin(ctx context.Context, store *catalog.Store, cfg config.Config, present *presentation.Presenter) error {
	id, err := store.EnsureDefaultUser(ctx)
	if err != nil {
		return err
	}
	admin, err := store.UserByUsername(ctx, "admin")
	if err != nil {
		return err
	}
	switch {
	case cfg.AdminPassword != "":
		hash, err := webui.HashPassword(cfg.AdminPassword)
		if err != nil {
			return err
		}
		if err := store.SetUserPassword(ctx, id, hash, false); err != nil {
			return err
		}
		present.Info("Admin login ready (user: admin), password from ADMIN_PASSWORD", "Silo login ready (user: admin), password from ADMIN_PASSWORD")
	case admin.PasswordHash == "":
		password, err := generateToken()
		if err != nil {
			return err
		}
		hash, err := webui.HashPassword(password)
		if err != nil {
			return err
		}
		if err := store.SetUserPassword(ctx, id, hash, false); err != nil {
			return err
		}
		present.Warning("Generated admin login, user: admin, password: %s (set ADMIN_PASSWORD to choose your own)", "Generated silo login, user: admin, password: %s", password)
	}
	return nil
}

func generateToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
