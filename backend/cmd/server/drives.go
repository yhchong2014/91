package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/video-site/backend/internal/api"
	"github.com/video-site/backend/internal/catalog"
	"github.com/video-site/backend/internal/drives"
	"github.com/video-site/backend/internal/drives/googledrive"
	"github.com/video-site/backend/internal/drives/guangyapan"
	"github.com/video-site/backend/internal/drives/localstorage"
	"github.com/video-site/backend/internal/drives/localupload"
	"github.com/video-site/backend/internal/drives/onedrive"
	"github.com/video-site/backend/internal/drives/p115"
	"github.com/video-site/backend/internal/drives/p123"
	"github.com/video-site/backend/internal/drives/pikpak"
	"github.com/video-site/backend/internal/drives/quark"
	"github.com/video-site/backend/internal/drives/scriptcrawler"
	"github.com/video-site/backend/internal/drives/webdav"
	"github.com/video-site/backend/internal/drives/wopan"
	"github.com/video-site/backend/internal/fingerprint"
	"github.com/video-site/backend/internal/preview"
	"github.com/video-site/backend/internal/scanner"
)

// guangYaPanLegacyRootPath keeps existing path-based mounts working until the
// user saves a directory ID through the unified root directory field.
func guangYaPanLegacyRootPath(rootID string, credentials map[string]string) string {
	if strings.TrimSpace(rootID) != "" {
		return ""
	}
	return strings.TrimSpace(credentials["root_path"])
}

func (a *App) attachDrive(ctx context.Context, d *catalog.Drive) error {
	a.driveAttachMu.Lock()
	defer a.driveAttachMu.Unlock()
	return a.attachDriveUnlocked(ctx, d)
}

func (a *App) ensureDriveAttached(ctx context.Context, driveID string) error {
	if _, ok := a.registry.Get(driveID); ok {
		return nil
	}
	a.driveAttachMu.Lock()
	defer a.driveAttachMu.Unlock()
	if _, ok := a.registry.Get(driveID); ok {
		return nil
	}
	d, err := a.cat.GetDrive(ctx, driveID)
	if err != nil {
		return err
	}
	return a.attachDriveUnlocked(ctx, d)
}

func (a *App) attachExistingDrives(ctx context.Context) {
	existing, err := a.cat.ListDrives(ctx)
	if err != nil {
		log.Printf("[drive] list existing drives: %v", err)
		return
	}
	log.Printf("[drive] attaching %d configured drive(s) in background", len(existing))
	for _, d := range existing {
		if err := ctx.Err(); err != nil {
			log.Printf("[drive] background attach stopped: %v", err)
			return
		}
		if err := a.attachDrive(ctx, d); err != nil {
			log.Printf("[drive %s] attach failed: %v", d.ID, err)
		}
	}
	log.Printf("[drive] background attach complete")
}

func (a *App) attachDriveUnlocked(ctx context.Context, d *catalog.Drive) error {
	if d == nil {
		return errors.New("nil drive")
	}
	var drv drives.Drive
	switch d.Kind {
	case "quark":
		drv = quark.New(quark.Config{
			ID:     d.ID,
			Cookie: d.Credentials["cookie"],
			RootID: d.RootID,
			OnCookieUpdate: func(cookie string) {
				d.Credentials["cookie"] = cookie
				_ = a.cat.UpsertDrive(ctx, d)
			},
		})
	case "p115":
		drv = p115.New(p115.Config{
			ID:            d.ID,
			Cookie:        d.Credentials["cookie"],
			RootID:        d.RootID,
			UploadTempDir: a.uploadWorkDir("p115"),
		})
	case p123.Kind:
		drv = p123.New(p123.Config{
			ID:            d.ID,
			Username:      d.Credentials["username"],
			Password:      d.Credentials["password"],
			AccessToken:   d.Credentials["access_token"],
			Platform:      d.Credentials["platform"],
			RootID:        d.RootID,
			UploadTempDir: a.uploadWorkDir(p123.Kind),
			OnTokenUpdate: func(access string) {
				if d.Credentials == nil {
					d.Credentials = make(map[string]string)
				}
				d.Credentials["access_token"] = access
				_ = a.cat.UpsertDrive(ctx, d)
			},
		})
	case "pikpak":
		drv = pikpak.New(pikpak.Config{
			ID:               d.ID,
			Username:         d.Credentials["username"],
			Password:         d.Credentials["password"],
			Platform:         d.Credentials["platform"],
			RefreshToken:     d.Credentials["refresh_token"],
			AccessToken:      d.Credentials["access_token"],
			CaptchaToken:     d.Credentials["captcha_token"],
			DeviceID:         d.Credentials["device_id"],
			RootID:           d.RootID,
			DisableMediaLink: pikpak.ParseBoolDefault(d.Credentials["disable_media_link"], true),
			UploadTempDir:    a.uploadWorkDir("pikpak"),
			OnTokenUpdate: func(access, refresh, captcha, deviceID string) {
				d.Credentials["access_token"] = access
				d.Credentials["refresh_token"] = refresh
				d.Credentials["captcha_token"] = captcha
				d.Credentials["device_id"] = deviceID
				_ = a.cat.UpsertDrive(ctx, d)
			},
		})
	case "wopan":
		drv = wopan.New(wopan.Config{
			ID:            d.ID,
			AccessToken:   d.Credentials["access_token"],
			RefreshToken:  d.Credentials["refresh_token"],
			FamilyID:      d.Credentials["family_id"],
			RootID:        d.RootID,
			UploadTempDir: a.uploadWorkDir("wopan"),
			OnTokenUpdate: func(access, refresh string) {
				d.Credentials["access_token"] = access
				d.Credentials["refresh_token"] = refresh
				_ = a.cat.UpsertDrive(ctx, d)
			},
		})
	case guangyapan.Kind:
		drv = guangyapan.New(guangyapan.Config{
			ID:             d.ID,
			RootID:         d.RootID,
			RootPath:       guangYaPanLegacyRootPath(d.RootID, d.Credentials),
			PhoneNumber:    d.Credentials["phone_number"],
			CaptchaToken:   d.Credentials["captcha_token"],
			SendCode:       parseBoolDefault(strings.TrimSpace(d.Credentials["send_code"]), false),
			VerifyCode:     d.Credentials["verify_code"],
			VerificationID: d.Credentials["verification_id"],
			AccessToken:    d.Credentials["access_token"],
			RefreshToken:   d.Credentials["refresh_token"],
			ClientID:       d.Credentials["client_id"],
			DeviceID:       d.Credentials["device_id"],
			PageSize:       parseIntDefault(strings.TrimSpace(d.Credentials["page_size"]), 100),
			OrderBy:        parseIntDefault(strings.TrimSpace(d.Credentials["order_by"]), 3),
			SortType:       parseIntDefault(strings.TrimSpace(d.Credentials["sort_type"]), 1),
			AccountBaseURL: d.Credentials["account_base_url"],
			APIBaseURL:     d.Credentials["api_base_url"],
			OnCredentialsUpdate: func(updated map[string]string) {
				if d.Credentials == nil {
					d.Credentials = make(map[string]string)
				}
				for k, v := range updated {
					d.Credentials[k] = v
				}
				_ = a.cat.UpsertDrive(ctx, d)
			},
		})
	case "onedrive":
		drv = onedrive.New(onedrive.Config{
			ID:           d.ID,
			RootID:       d.RootID,
			Region:       d.Credentials["region"],
			AccessToken:  d.Credentials["access_token"],
			RefreshToken: d.Credentials["refresh_token"],
			IsSharePoint: parseBoolDefault(d.Credentials["is_sharepoint"], false),
			SiteID:       d.Credentials["site_id"],
			RenewAPIURL:  d.Credentials["api_url_address"],
			OnTokenUpdate: func(access, refresh string) {
				if d.Credentials == nil {
					d.Credentials = make(map[string]string)
				}
				d.Credentials["access_token"] = access
				d.Credentials["refresh_token"] = refresh
				_ = a.cat.UpsertDrive(ctx, d)
			},
		})
	case googledrive.Kind:
		drv = googledrive.New(googledrive.Config{
			ID:           d.ID,
			RootID:       d.RootID,
			AccessToken:  d.Credentials["access_token"],
			RefreshToken: d.Credentials["refresh_token"],
			ClientID:     d.Credentials["client_id"],
			ClientSecret: d.Credentials["client_secret"],
			OAuthURL:     d.Credentials["oauth_url"],
			APIBaseURL:   d.Credentials["api_base_url"],
			OnTokenUpdate: func(access, refresh string) {
				if d.Credentials == nil {
					d.Credentials = make(map[string]string)
				}
				d.Credentials["access_token"] = access
				d.Credentials["refresh_token"] = refresh
				_ = a.cat.UpsertDrive(ctx, d)
			},
		})
	case webdav.Kind:
		address := strings.TrimSpace(d.Credentials["address"])
		if address == "" {
			return fmt.Errorf("webdav drive %s: address is required", d.ID)
		}
		drv = webdav.New(webdav.Config{
			ID:                    d.ID,
			Address:               address,
			Username:              strings.TrimSpace(d.Credentials["username"]),
			Password:              d.Credentials["password"],
			RootPath:              strings.TrimSpace(d.Credentials["root_path"]),
			TLSInsecureSkipVerify: parseBoolDefault(strings.TrimSpace(d.Credentials["tls_insecure_skip_verify"]), false),
		})
	case localstorage.Kind:
		drv = localstorage.New(localstorage.Config{
			ID:                   d.ID,
			RootPath:             d.Credentials["path"],
			STRMAllowOutsideRoot: parseBoolDefault(strings.TrimSpace(d.Credentials["strm_allow_outside_root"]), false),
		})
	case scriptcrawler.Kind:
		drv = scriptcrawler.New(scriptcrawler.Config{
			ID:      d.ID,
			RootDir: a.scriptCrawlerDriveDir(d.ID),
		})
	default:
		return fmt.Errorf("unknown drive kind: %s", d.Kind)
	}

	if err := drv.Init(ctx); err != nil {
		d.Status = "error"
		d.LastError = err.Error()
		_ = a.cat.UpsertDrive(ctx, d)
		return err
	}

	d.Status = "ok"
	d.LastError = ""
	_ = a.cat.UpsertDrive(ctx, d)

	a.registry.Set(d.ID, drv)

	a.startDriveGenerationWorkers(ctx, d.ID, drv, true)

	if sd, ok := drv.(*scriptcrawler.Driver); ok {
		a.attachScriptCrawler(d, sd)
	}

	return nil
}

func (a *App) attachLocalUpload(ctx context.Context) error {
	drv := localupload.New(a.localUploadDir())
	if err := drv.Init(ctx); err != nil {
		return err
	}
	a.registry.Set(drv.ID(), drv)

	a.startDriveGenerationWorkers(ctx, drv.ID(), drv, true)
	return nil
}

func (a *App) newDriveGenerationWorkers(drv drives.Drive) (*preview.Worker, *preview.ThumbWorker, *fingerprint.Worker) {
	previewCfg := preview.Config{}
	if a.cfg != nil {
		previewCfg = preview.Config{
			FFmpegPath:      a.cfg.Preview.FFmpegPath,
			FFprobePath:     a.cfg.Preview.FFprobePath,
			DurationSeconds: a.cfg.Preview.DurationSeconds,
			Width:           a.cfg.Preview.Width,
			Segments:        a.cfg.Preview.Segments,
			LocalDir:        a.cfg.Storage.LocalPreviewDir,
		}
	}
	gen := preview.New(previewCfg)
	previewWorker := preview.NewWorker(gen, a.cat, drv)
	thumbWorker := preview.NewThumbWorker(gen, a.cat, drv)
	if cooldown := generationCooldownForDrive(drv); cooldown > 0 {
		previewWorker.RateLimitCooldown = cooldown
		thumbWorker.RateLimitCooldown = cooldown
	}
	return previewWorker, thumbWorker, fingerprint.NewWorker(a.cat, drv, fingerprintConfigForDrive(drv))
}

func generationCooldownForDrive(drv drives.Drive) time.Duration {
	if drv == nil {
		return 0
	}
	switch strings.ToLower(drv.Kind()) {
	case "wopan", "guangyapan":
		return 10 * time.Minute
	}
	return 0
}

func (a *App) startDriveGenerationWorkers(ctx context.Context, driveID string, drv drives.Drive, enqueue bool) {
	worker, thumbWorker, fingerprintWorker := a.newDriveGenerationWorkers(drv)
	workerCtx, cancel := context.WithCancel(ctx)
	go worker.Run(workerCtx)
	go thumbWorker.Run(workerCtx)
	go fingerprintWorker.Run(workerCtx)

	a.registerPreviewWorkersWithOptions(workerCtx, driveID, worker, thumbWorker, fingerprintWorker, cancel, enqueue)
}

func (a *App) localUploadDir() string {
	return filepath.Join(filepath.Dir(a.cfg.Storage.LocalPreviewDir), "uploads")
}

func (a *App) uploadWorkDir(kind string) string {
	if a == nil || a.cfg == nil || strings.TrimSpace(a.cfg.Storage.LocalPreviewDir) == "" {
		return ""
	}
	kind = strings.Trim(strings.ToLower(strings.TrimSpace(kind)), string(filepath.Separator))
	if kind == "" {
		kind = "generic"
	}
	return filepath.Join(filepath.Dir(a.cfg.Storage.LocalPreviewDir), "upload-tmp", kind)
}

func fingerprintConfigForDrive(drv drives.Drive) fingerprint.Config {
	cfg := fingerprint.Config{RateLimitCooldown: 5 * time.Minute}
	if drv == nil {
		return cfg
	}
	switch strings.ToLower(drv.Kind()) {
	case "p115", "p123", "onedrive", "wopan", "guangyapan":
		cfg.RateLimitCooldown = 10 * time.Minute
	case "pikpak":
		cfg.RateLimitCooldown = 5 * time.Minute
	}
	return cfg
}

// scriptCrawlerRootDir 是所有通用脚本爬虫 drive 共享的根目录。
func (a *App) scriptCrawlerRootDir() string {
	return filepath.Join(filepath.Dir(a.cfg.Storage.LocalPreviewDir), "scriptcrawlers")
}

// scriptCrawlerDriveDir 是单个 scriptcrawler drive 的存储目录：<root>/<driveID>。
func (a *App) scriptCrawlerDriveDir(driveID string) string {
	return filepath.Join(a.scriptCrawlerRootDir(), driveID)
}

// commonThumbsDir 是所有 drive 共享的封面目录，/p/thumb/{videoID} 路由命中这里。
func (a *App) commonThumbsDir() string {
	return filepath.Join(a.cfg.Storage.LocalPreviewDir, "thumbs")
}

// attachScriptCrawler 创建通用脚本爬虫 runner，并注册到 a.scriptCrawlers。
func (a *App) attachScriptCrawler(d *catalog.Drive, drv *scriptcrawler.Driver) {
	pythonPath := strings.TrimSpace(d.Credentials["python_path"])
	if pythonPath == "" {
		pythonPath = "python3"
	}
	scriptPath := strings.TrimSpace(d.Credentials["script_path"])
	proxyURL := strings.TrimSpace(d.Credentials["proxy"])
	configJSON := strings.TrimSpace(d.Credentials["config_json"])
	workDir := ""
	if scriptPath != "" {
		workDir = filepath.Dir(scriptPath)
	}

	driveID := d.ID
	c := scriptcrawler.NewCrawler(scriptcrawler.CrawlerConfig{
		Driver:         drv,
		Catalog:        a.cat,
		CrawlerName:    d.Name,
		PythonPath:     pythonPath,
		FFmpegPath:     a.cfg.Preview.FFmpegPath,
		FFprobePath:    a.cfg.Preview.FFprobePath,
		ScriptPath:     scriptPath,
		WorkDir:        workDir,
		CommonThumbDir: a.commonThumbsDir(),
		ProxyURL:       proxyURL,
		ConfigJSON:     configJSON,
		DisablePreview: !d.TeaserEnabled,
		OnProgress: func(progress scriptcrawler.CrawlProgress) {
			scanned := progress.Checked
			if scanned < progress.TotalEntries {
				scanned = progress.TotalEntries
			}
			added := progress.Emitted
			if added < progress.NewVideos {
				added = progress.NewVideos
			}
			a.updateDriveScanProgress(driveID, scanned, added)
		},
	})

	a.mu.Lock()
	a.scriptCrawlers[driveID] = c
	a.mu.Unlock()

	a.ensureScriptCrawlerNameTag(driveID, d.Name)
}

func (a *App) ensureScriptCrawlerNameTag(driveID, crawlerName string) {
	tagName := strings.TrimSpace(crawlerName)
	if tagName == "" {
		tagName = strings.TrimSpace(driveID)
	}
	if tagName == "" {
		return
	}
	bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go func() {
		defer cancel()
		prefix := scriptcrawler.BuildVideoID(driveID, "")
		if _, err := a.cat.EnsureCrawlerTagForVideoIDPrefix(bgCtx, prefix, tagName); err != nil {
			log.Printf("[scriptcrawler] drive=%s ensure crawler tag %q: %v", driveID, tagName, err)
		}
	}()
}

func (a *App) registerPreviewWorkers(ctx context.Context, driveID string, worker *preview.Worker, thumbWorker *preview.ThumbWorker, fingerprintWorker *fingerprint.Worker, cancel context.CancelFunc) {
	a.registerPreviewWorkersWithOptions(ctx, driveID, worker, thumbWorker, fingerprintWorker, cancel, true)
}

func (a *App) registerPreviewWorkersWithOptions(ctx context.Context, driveID string, worker *preview.Worker, thumbWorker *preview.ThumbWorker, fingerprintWorker *fingerprint.Worker, cancel context.CancelFunc, enqueue bool) {
	a.mu.Lock()
	if a.cancels == nil {
		a.cancels = make(map[string]context.CancelFunc)
	}
	if a.workers == nil {
		a.workers = make(map[string]*preview.Worker)
	}
	if a.thumbWorkers == nil {
		a.thumbWorkers = make(map[string]*preview.ThumbWorker)
	}
	if a.fingerprintWorkers == nil {
		a.fingerprintWorkers = make(map[string]*fingerprint.Worker)
	}
	if old, ok := a.cancels[driveID]; ok && old != nil {
		old()
	}
	if worker != nil {
		a.workers[driveID] = worker
	} else {
		delete(a.workers, driveID)
	}
	if thumbWorker != nil {
		a.thumbWorkers[driveID] = thumbWorker
	} else {
		delete(a.thumbWorkers, driveID)
	}
	if fingerprintWorker != nil {
		a.fingerprintWorkers[driveID] = fingerprintWorker
	} else {
		delete(a.fingerprintWorkers, driveID)
	}
	if cancel != nil {
		a.cancels[driveID] = cancel
	} else {
		delete(a.cancels, driveID)
	}
	a.mu.Unlock()

	if !enqueue {
		return
	}
	go a.enqueueDriveGeneration(ctx, driveID, worker, thumbWorker)
	if fingerprintWorker != nil {
		a.scheduleFingerprintBackfill(ctx, driveID, fingerprintWorker)
	}
}

func (a *App) registerDriveTaskContext(ctx context.Context, driveID string) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	taskCtx, cancel := context.WithCancel(ctx)

	a.taskCancelMu.Lock()
	if a.driveTaskCancels == nil {
		a.driveTaskCancels = make(map[string]map[uint64]context.CancelFunc)
	}
	a.driveTaskCancelSeq++
	token := a.driveTaskCancelSeq
	if a.driveTaskCancels[driveID] == nil {
		a.driveTaskCancels[driveID] = make(map[uint64]context.CancelFunc)
	}
	a.driveTaskCancels[driveID][token] = cancel
	a.taskCancelMu.Unlock()

	done := func() {
		cancel()
		a.taskCancelMu.Lock()
		if cancels := a.driveTaskCancels[driveID]; cancels != nil {
			delete(cancels, token)
			if len(cancels) == 0 {
				delete(a.driveTaskCancels, driveID)
			}
		}
		a.taskCancelMu.Unlock()
	}
	return taskCtx, done
}

func (a *App) cancelDriveTaskContexts(driveID string) int {
	a.taskCancelMu.Lock()
	cancelsByToken := a.driveTaskCancels[driveID]
	delete(a.driveTaskCancels, driveID)
	a.taskCancelMu.Unlock()

	for _, cancel := range cancelsByToken {
		if cancel != nil {
			cancel()
		}
	}
	return len(cancelsByToken)
}

func (a *App) cancelAllDriveTaskContexts() map[string]int {
	a.taskCancelMu.Lock()
	all := a.driveTaskCancels
	a.driveTaskCancels = nil
	a.taskCancelMu.Unlock()

	out := make(map[string]int, len(all))
	for driveID, cancelsByToken := range all {
		out[driveID] = len(cancelsByToken)
		for _, cancel := range cancelsByToken {
			if cancel != nil {
				cancel()
			}
		}
	}
	return out
}

func (a *App) clearQueuedDriveTask(driveID string) bool {
	a.scanQueueMu.Lock()
	queued := a.scanQueued[driveID]
	delete(a.scanQueued, driveID)
	delete(a.scanProgress, driveID)
	a.scanQueueMu.Unlock()
	return queued
}

func (a *App) clearAllQueuedDriveTasks() []string {
	a.scanQueueMu.Lock()
	ids := make([]string, 0, len(a.scanQueued))
	for id := range a.scanQueued {
		ids = append(ids, id)
	}
	a.scanQueued = nil
	a.scanProgress = nil
	a.scanQueueMu.Unlock()
	return ids
}

func (a *App) clearFingerprintQueueing(driveID string) bool {
	a.fingerprintQueueMu.Lock()
	queued := a.fingerprintQueueing[driveID]
	delete(a.fingerprintQueueing, driveID)
	a.fingerprintQueueMu.Unlock()
	return queued
}

func (a *App) clearAllFingerprintQueueing() []string {
	a.fingerprintQueueMu.Lock()
	ids := make([]string, 0, len(a.fingerprintQueueing))
	for id := range a.fingerprintQueueing {
		ids = append(ids, id)
	}
	a.fingerprintQueueing = nil
	a.fingerprintQueueMu.Unlock()
	return ids
}

func (a *App) beginDriveScanOrCrawl(driveID string) bool {
	driveID = strings.TrimSpace(driveID)
	if driveID == "" {
		return false
	}
	a.scanQueueMu.Lock()
	defer a.scanQueueMu.Unlock()
	if a.scanQueued == nil {
		a.scanQueued = make(map[string]bool)
	}
	if a.scanQueued[driveID] {
		return false
	}
	a.scanQueued[driveID] = true
	if a.scanProgress == nil {
		a.scanProgress = make(map[string]driveScanProgress)
	}
	a.scanProgress[driveID] = driveScanProgress{}
	return true
}

func (a *App) endDriveScanOrCrawl(driveID string) {
	a.scanQueueMu.Lock()
	delete(a.scanQueued, driveID)
	delete(a.scanProgress, driveID)
	a.scanQueueMu.Unlock()
}

func (a *App) updateDriveScanProgress(driveID string, scanned, added int) {
	driveID = strings.TrimSpace(driveID)
	if driveID == "" {
		return
	}
	a.scanQueueMu.Lock()
	if a.scanQueued[driveID] {
		if a.scanProgress == nil {
			a.scanProgress = make(map[string]driveScanProgress)
		}
		progress := a.scanProgress[driveID]
		progress.Scanned = scanned
		progress.Added = added
		a.scanProgress[driveID] = progress
	}
	a.scanQueueMu.Unlock()
}

func (a *App) updateDriveScanCooldown(driveID string, until time.Time) {
	driveID = strings.TrimSpace(driveID)
	if driveID == "" {
		return
	}
	a.scanQueueMu.Lock()
	if a.scanQueued[driveID] {
		if a.scanProgress == nil {
			a.scanProgress = make(map[string]driveScanProgress)
		}
		progress := a.scanProgress[driveID]
		progress.CooldownUntil = until
		a.scanProgress[driveID] = progress
	}
	a.scanQueueMu.Unlock()
}

func (a *App) pauseDriveScanForRateLimit(ctx context.Context, driveID string, drv drives.Drive, err error) bool {
	wait, ok := drives.RateLimitRetryAfter(err)
	if !ok {
		return false
	}
	if wait <= 0 {
		wait = scanCooldownForDrive(drv)
	}
	if wait <= 0 {
		wait = 5 * time.Minute
	}
	until := time.Now().Add(wait)
	a.updateDriveScanCooldown(driveID, until)
	log.Printf("[scan] drive=%s rate limited; cooling until=%s wait=%s: %v", driveID, until.Format(time.RFC3339), wait, err)
	if !sleepDriveScanCooldown(ctx, wait) {
		log.Printf("[scan] drive=%s cooldown canceled: %v", driveID, ctx.Err())
	}
	return true
}

func scanCooldownForDrive(drv drives.Drive) time.Duration {
	if drv == nil {
		return 5 * time.Minute
	}
	switch strings.ToLower(drv.Kind()) {
	case "guangyapan":
		return 10 * time.Minute
	default:
		return 5 * time.Minute
	}
}

func sleepDriveScanCooldown(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (a *App) driveHasActiveWork(driveID string) bool {
	driveID = strings.TrimSpace(driveID)
	if driveID == "" {
		return true
	}

	a.scanQueueMu.Lock()
	scanning := a.scanQueued[driveID]
	a.scanQueueMu.Unlock()
	if scanning {
		return true
	}

	a.taskCancelMu.Lock()
	taskContexts := len(a.driveTaskCancels[driveID])
	a.taskCancelMu.Unlock()
	if taskContexts > 0 {
		return true
	}

	a.fingerprintQueueMu.Lock()
	fingerprintQueueing := a.fingerprintQueueing[driveID]
	a.fingerprintQueueMu.Unlock()
	if fingerprintQueueing {
		return true
	}

	a.uploadProgressMu.Lock()
	uploading := a.uploadProgress[driveID].State != ""
	a.uploadProgressMu.Unlock()
	if uploading {
		return true
	}

	a.mu.Lock()
	previewWorker := a.workers[driveID]
	thumbWorker := a.thumbWorkers[driveID]
	fingerprintWorker := a.fingerprintWorkers[driveID]
	a.mu.Unlock()

	if previewTaskBusy(thumbWorker.Status()) {
		return true
	}
	if previewTaskBusy(previewWorker.Status()) {
		return true
	}
	if fingerprintTaskBusy(fingerprintWorker.Status()) {
		return true
	}
	return false
}

func previewTaskBusy(status preview.TaskStatus) bool {
	return status.State != "" && status.State != "idle"
}

func fingerprintTaskBusy(status fingerprint.TaskStatus) bool {
	return status.State != "" && status.State != "idle"
}

func (a *App) resetDriveGenerationWorkers(ctx context.Context, driveID string) bool {
	var drv drives.Drive
	var attached bool
	if a.registry != nil {
		drv, attached = a.registry.Get(driveID)
	}

	a.mu.Lock()
	hadWorkers := a.workers[driveID] != nil ||
		a.thumbWorkers[driveID] != nil ||
		a.fingerprintWorkers[driveID] != nil ||
		a.cancels[driveID] != nil
	oldCancel := a.cancels[driveID]
	a.mu.Unlock()

	if attached && drv != nil {
		a.startDriveGenerationWorkers(ctx, driveID, drv, false)
		return hadWorkers
	}

	if oldCancel != nil {
		oldCancel()
	}
	a.mu.Lock()
	delete(a.workers, driveID)
	delete(a.thumbWorkers, driveID)
	delete(a.fingerprintWorkers, driveID)
	delete(a.cancels, driveID)
	a.mu.Unlock()
	return hadWorkers
}

func (a *App) resetAllDriveGenerationWorkers(ctx context.Context) []string {
	seen := make(map[string]struct{})
	if a.registry != nil {
		for _, drv := range a.registry.All() {
			if drv == nil {
				continue
			}
			driveID := drv.ID()
			seen[driveID] = struct{}{}
			a.startDriveGenerationWorkers(ctx, driveID, drv, false)
		}
	}

	a.mu.Lock()
	stale := make([]string, 0)
	for id := range a.cancels {
		if _, ok := seen[id]; !ok {
			stale = append(stale, id)
		}
	}
	for id := range a.workers {
		if _, ok := seen[id]; !ok {
			stale = append(stale, id)
		}
	}
	for id := range a.thumbWorkers {
		if _, ok := seen[id]; !ok {
			stale = append(stale, id)
		}
	}
	for id := range a.fingerprintWorkers {
		if _, ok := seen[id]; !ok {
			stale = append(stale, id)
		}
	}
	a.mu.Unlock()

	for _, id := range stale {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		a.resetDriveGenerationWorkers(ctx, id)
	}

	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
}

func (a *App) stopDriveTasks(ctx context.Context, driveID string) bool {
	driveID = strings.TrimSpace(driveID)
	if driveID == "" {
		return false
	}

	canceled := a.cancelDriveTaskContexts(driveID)
	queued := a.clearQueuedDriveTask(driveID)
	fingerprintQueued := a.clearFingerprintQueueing(driveID)
	uploading := a.clearCrawlerUploadProgress(driveID)
	transcoding := a.stopDriveTranscode(driveID)
	hadWorkers := a.resetDriveGenerationWorkers(ctx, driveID)
	stopped := canceled > 0 || queued || fingerprintQueued || uploading || transcoding || hadWorkers
	log.Printf("[tasks] stop drive=%s stopped=%v canceled_tasks=%d queued=%v fingerprint_queue=%v uploading=%v transcoding=%v workers=%v",
		driveID, stopped, canceled, queued, fingerprintQueued, uploading, transcoding, hadWorkers)
	return stopped
}

func (a *App) stopAllDriveTasks(ctx context.Context) int {
	stoppedIDs := make(map[string]struct{})
	if a.nightlyRunner != nil && a.nightlyRunner.StopCurrent() {
		log.Printf("[tasks] requested nightly pipeline stop")
	}
	for id := range a.cancelAllDriveTaskContexts() {
		stoppedIDs[id] = struct{}{}
	}
	for _, id := range a.clearAllQueuedDriveTasks() {
		stoppedIDs[id] = struct{}{}
	}
	for _, id := range a.clearAllFingerprintQueueing() {
		stoppedIDs[id] = struct{}{}
	}
	for _, id := range a.clearAllCrawlerUploadProgress() {
		stoppedIDs[id] = struct{}{}
	}
	for _, id := range a.stopAllDriveTranscodes() {
		stoppedIDs[id] = struct{}{}
	}
	for _, id := range a.resetAllDriveGenerationWorkers(ctx) {
		stoppedIDs[id] = struct{}{}
	}
	log.Printf("[tasks] stop all drive tasks drives=%d", len(stoppedIDs))
	return len(stoppedIDs)
}

func (a *App) enqueuePending(ctx context.Context, driveID string, w *preview.Worker) {
	pending, err := a.cat.ListVideosByPreviewStatus(ctx, driveID, "pending", 0)
	if err != nil {
		log.Printf("[preview] list pending %s: %v", driveID, err)
		return
	}
	if len(pending) == 0 {
		return
	}
	log.Printf("[preview] enqueue %d pending videos for drive=%s", len(pending), driveID)
	for _, v := range pending {
		if !w.EnqueueBlocking(ctx, v) {
			log.Printf("[preview] enqueue pending canceled for drive=%s", driveID)
			return
		}
	}
}

func (a *App) enqueueDriveGeneration(ctx context.Context, driveID string, worker *preview.Worker, thumbWorker *preview.ThumbWorker) {
	// 封面 worker 始终入队（与早期"全局 preview.enabled=false 时仍然生成封面"
	// 的行为一致）；预览视频 worker 仅在该 drive 的 TeaserEnabled 为 true 时入队。
	// 两条队列互不等待，避免封面批量生成拖住预览视频生成。
	if thumbWorker != nil {
		a.enqueueThumbnails(ctx, driveID, thumbWorker)
	}
	if worker == nil || !a.teaserEnabledForDrive(ctx, driveID) {
		return
	}
	a.enqueuePending(ctx, driveID, worker)
}

func (a *App) enqueueThumbnails(ctx context.Context, driveID string, w *preview.ThumbWorker) {
	pending, err := a.cat.ListVideosNeedingThumbnail(ctx, driveID, 0)
	if err != nil {
		log.Printf("[thumb] list pending %s: %v", driveID, err)
		return
	}
	if len(pending) == 0 {
		return
	}
	log.Printf("[thumb] enqueue %d thumbnail/duration tasks for drive=%s", len(pending), driveID)
	for _, v := range pending {
		if !w.EnqueueBlocking(ctx, v) {
			log.Printf("[thumb] enqueue thumbnail/duration tasks canceled for drive=%s", driveID)
			return
		}
	}
}

func (a *App) runFingerprintReconciler(ctx context.Context) {
	ticker := time.NewTicker(fingerprintReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.enqueueAllPendingFingerprints(ctx)
		}
	}
}

func (a *App) enqueueAllPendingFingerprints(ctx context.Context) {
	a.mu.Lock()
	workers := make(map[string]*fingerprint.Worker, len(a.fingerprintWorkers))
	for id, worker := range a.fingerprintWorkers {
		workers[id] = worker
	}
	a.mu.Unlock()
	for driveID, worker := range workers {
		a.scheduleFingerprintBackfill(ctx, driveID, worker)
	}
}

func (a *App) scheduleFingerprintBackfill(ctx context.Context, driveID string, w *fingerprint.Worker) {
	if w == nil {
		return
	}
	taskCtx, done := a.registerDriveTaskContext(ctx, driveID)
	a.fingerprintQueueMu.Lock()
	if a.fingerprintQueueing == nil {
		a.fingerprintQueueing = make(map[string]bool)
	}
	if a.fingerprintQueueing[driveID] {
		a.fingerprintQueueMu.Unlock()
		done()
		return
	}
	a.fingerprintQueueing[driveID] = true
	a.fingerprintQueueMu.Unlock()

	go func() {
		defer func() {
			done()
			a.fingerprintQueueMu.Lock()
			delete(a.fingerprintQueueing, driveID)
			a.fingerprintQueueMu.Unlock()
		}()
		a.enqueueFingerprints(taskCtx, driveID, w)
	}()
}

func (a *App) enqueueFingerprints(ctx context.Context, driveID string, w *fingerprint.Worker) {
	if w == nil {
		return
	}
	pending, err := a.cat.ListVideosNeedingFingerprint(ctx, driveID, 0)
	if err != nil {
		log.Printf("[fingerprint] list pending %s: %v", driveID, err)
		return
	}
	if len(pending) == 0 {
		return
	}
	log.Printf("[fingerprint] enqueue %d videos for drive=%s", len(pending), driveID)
	for _, v := range pending {
		if !w.EnqueueBlocking(ctx, v) {
			log.Printf("[fingerprint] enqueue canceled for drive=%s", driveID)
			return
		}
	}
}

func (a *App) detachDrive(id string) {
	a.cancelDriveTaskContexts(id)
	a.clearQueuedDriveTask(id)
	a.clearFingerprintQueueing(id)
	a.registry.Remove(id)
	a.mu.Lock()
	if cancel, ok := a.cancels[id]; ok {
		cancel()
		delete(a.cancels, id)
	}
	delete(a.workers, id)
	delete(a.thumbWorkers, id)
	delete(a.fingerprintWorkers, id)
	delete(a.scriptCrawlers, id)
	a.mu.Unlock()
}

// listDriveDirChildren 实现 AdminServer.ListDriveDirChildren：
// 列指定 drive 在 parentID 下的直接子目录，仅返回目录条目（IsDir=true），文件忽略。
//
// parentID 为空时使用 drive 实例的 RootID()。用户在"设置跳过目录"弹窗里
// 浏览的是整个网盘逻辑根，方便从根目录起逐层挑跳过点。
//
// 性能优化：p115 的 Driver.List 走 SDK 的 ListWithLimit，会把目录里全部文件 +
// 目录分页拉完才返回；某些 115 根目录累积了几万个视频，单次列目录可能卡几十
// 秒（叠加 driver 的 2s 间隔限频）。所以 p115 走 ListDirsOnly 快路径：单页
// (1150)、按 file_type 排序，扫一遍只挑目录条目，1 次 API 调用搞定。其它网盘
// 走标准 List + IsDir 过滤 —— 它们的根目录通常不会有几万个文件。
//
// drive 未挂载（如凭证错误未通过 Init）时返回 error；前端展示 5xx 给用户。
func (a *App) listDriveDirChildren(ctx context.Context, driveID, parentID string) ([]api.DriveDirEntry, error) {
	drv, ok := a.registry.Get(driveID)
	if !ok {
		return nil, fmt.Errorf("drive %s not attached", driveID)
	}
	if parentID == "" {
		parentID = drv.RootID()
	}
	// p115 快路径：避免拉全部分页文件
	if fast, ok := drv.(interface {
		ListDirsOnly(ctx context.Context, dirID string) ([]drives.Entry, error)
	}); ok {
		entries, err := fast.ListDirsOnly(ctx, parentID)
		if err != nil {
			return nil, fmt.Errorf("list drive %s parent %s dirs-only: %w", driveID, parentID, err)
		}
		out := make([]api.DriveDirEntry, 0, len(entries))
		for _, e := range entries {
			out = append(out, api.DriveDirEntry{ID: e.ID, Name: e.Name})
		}
		return out, nil
	}
	// 通用路径
	entries, err := drv.List(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("list drive %s parent %s: %w", driveID, parentID, err)
	}
	out := make([]api.DriveDirEntry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir {
			continue
		}
		out = append(out, api.DriveDirEntry{ID: e.ID, Name: e.Name})
	}
	return out, nil
}

// scheduleScan 异步触发某个 drive 的扫盘。
//
// 调用立即返回。不同 drive 的扫盘可以并行；同一个 drive 如果已有扫盘、封面、
// 预览视频或指纹任务在跑，本次请求会被拒绝。
func (a *App) scheduleScan(ctx context.Context, driveID string) bool {
	if a.driveHasActiveWork(driveID) {
		log.Printf("[scan] drive=%s has active work, skip duplicate request", driveID)
		return false
	}
	if !a.beginDriveScanOrCrawl(driveID) {
		log.Printf("[scan] drive=%s already queued or running, skip duplicate request", driveID)
		return false
	}
	taskCtx, done := a.registerDriveTaskContext(ctx, driveID)

	go func() {
		defer func() {
			a.endDriveScanOrCrawl(driveID)
			done()
		}()
		a.runScanWithTaskContext(taskCtx, driveID)
	}()
	return true
}

func (a *App) runScan(ctx context.Context, driveID string) {
	if !a.beginDriveScanOrCrawl(driveID) {
		log.Printf("[scan] drive=%s already queued or running, skip direct scan", driveID)
		return
	}
	defer a.endDriveScanOrCrawl(driveID)
	taskCtx, done := a.registerDriveTaskContext(ctx, driveID)
	defer done()
	a.runScanWithTaskContext(taskCtx, driveID)
}

func (a *App) runScanWithTaskContext(ctx context.Context, driveID string) {
	if err := ctx.Err(); err != nil {
		log.Printf("[scan] drive=%s canceled before start: %v", driveID, err)
		return
	}
	if err := a.ensureDriveAttached(ctx, driveID); err != nil {
		log.Printf("[scan] drive %s attach failed: %v", driveID, err)
		return
	}
	drv, ok := a.registry.Get(driveID)
	if !ok {
		log.Printf("[scan] drive %s not attached", driveID)
		return
	}

	a.mu.Lock()
	worker := a.workers[driveID]
	thumbWorker := a.thumbWorkers[driveID]
	fingerprintWorker := a.fingerprintWorkers[driveID]
	a.mu.Unlock()

	onNew := func(v *catalog.Video) {
		if thumbWorker != nil && v.ThumbnailURL == "" {
			thumbWorker.Enqueue(v)
		}
		if fingerprintWorker != nil {
			fingerprintWorker.Enqueue(v)
		}
		// MetaTube: scrape metadata by filename in background.
		if a.metaTubeClient != nil && v.FileName != "" {
			go a.scrapeMetaTube(ctx, v.ID, v.FileName)
		}
	}

	// 扫描入口固定使用 drive 的 root_id；同时把 admin 配置的 SkipDirIDs
	// 传给 scanner（命中即不递归）。
	d, err := a.cat.GetDrive(ctx, driveID)
	if err != nil {
		log.Printf("[scan] get drive %s: %v", driveID, err)
		return
	}
	sc := scanner.New(a.cat, drv, a.cfg.Scanner.VideoExtensions, d.SkipDirIDs, onNew)
	sc.OnProgress = func(stats scanner.Stats) {
		a.updateDriveScanProgress(driveID, stats.Scanned, stats.Added)
	}

	startID := d.RootID

	log.Printf("[scan] drive=%s start=%s skip_dirs=%d", driveID, startID, len(d.SkipDirIDs))
	stats, err := sc.Run(ctx, startID)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("[scan] drive=%s canceled: %v", driveID, err)
		} else if a.pauseDriveScanForRateLimit(ctx, driveID, drv, err) {
			return
		} else {
			log.Printf("[scan] drive=%s error: %v", driveID, err)
		}
		return
	}
	if err := ctx.Err(); err != nil {
		log.Printf("[scan] drive=%s canceled after scan: %v", driveID, err)
		return
	}
	log.Printf("[scan] drive=%s done scanned=%d added=%d errors=%d", driveID, stats.Scanned, stats.Added, stats.Errors)
	// 删除检测：扫描到的 file_ids 是当前云盘上的真实存在；catalog 里这个 drive
	// 名下、且其 parent_id 处在本次扫描走过的目录内（或本次是从根扫的）、却
	// 不在 SeenFileIDs 中的视频 → 视为已被删除。
	//
	// scriptcrawler / localupload 走自己的生命周期管理，不应该参与扫描清理；
	// stats.Errors > 0 时（云盘 API 中途抖动）保守起见跳过这一轮，避免把
	// "暂时列不出来"误认成"被用户删了"。
	if drv.Kind() != scriptcrawler.Kind && drv.ID() != localupload.DriveID {
		if stats.Errors > 0 {
			log.Printf("[cleanup] skip stale cleanup for drive=%s kind=%s: scan had %d directory errors", driveID, drv.Kind(), stats.Errors)
		} else {
			removed, err := a.cleanupMissingDriveVideos(ctx, driveID, stats.SeenFileIDs, stats.VisitedDirIDs, startID == drv.RootID())
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					log.Printf("[cleanup] canceled stale cleanup drive=%s kind=%s: %v", driveID, drv.Kind(), ctxErr)
					return
				}
				log.Printf("[cleanup] stale cleanup drive=%s kind=%s error: %v", driveID, drv.Kind(), err)
			} else if removed > 0 {
				log.Printf("[cleanup] removed %d stale videos for drive=%s kind=%s", removed, driveID, drv.Kind())
			}
		}
	}
	if err := ctx.Err(); err != nil {
		log.Printf("[scan] drive=%s canceled before enqueue generation: %v", driveID, err)
		return
	}
	a.scheduleFingerprintBackfill(ctx, driveID, fingerprintWorker)
	a.enqueueDriveGeneration(ctx, driveID, worker, thumbWorker)
}

func (a *App) cleanupMissingDriveVideos(ctx context.Context, driveID string, liveFileIDs map[string]struct{}, visitedDirIDs map[string]struct{}, fullDriveScan bool) (int, error) {
	items, err := a.cat.ListVideosByDrive(ctx, driveID)
	if err != nil {
		return 0, err
	}

	localDir := ""
	if a.cfg != nil {
		localDir = a.cfg.Storage.LocalPreviewDir
	}
	removed := 0
	for _, v := range items {
		if _, ok := liveFileIDs[v.FileID]; ok {
			continue
		}
		if !fullDriveScan {
			if _, ok := visitedDirIDs[v.ParentID]; !ok {
				continue
			}
		}
		if err := removeLocalVideoAssets(localDir, v); err != nil {
			return removed, fmt.Errorf("remove local assets for %s: %w", v.ID, err)
		}
		if err := a.cat.DeleteVideo(ctx, v.ID); err != nil {
			return removed, fmt.Errorf("delete catalog video %s: %w", v.ID, err)
		}
		removed++
	}
	return removed, nil
}

// scrapeMetaTube queries the MetaTube backend for video metadata (title,
// actors, genres) based on the filename and updates the catalog entry.
// Called asynchronously from the scan onNew callback.
func (a *App) scrapeMetaTube(ctx context.Context, videoID, fileName string) {
	scrapeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	meta, err := a.metaTubeClient.ScrapeByFilename(scrapeCtx, fileName)
	if err != nil {
		log.Printf("[metatube] scrape %q: %v", fileName, err)
		return
	}
	if meta == nil {
		return // no recognizable code in filename
	}

	patch := catalog.VideoMetaPatch{}
	if meta.Title != "" {
		patch.Title = meta.Title
		patch.TitleSet = true
	}
	// Build author from actors list.
	if len(meta.Actors) > 0 {
		names := make([]string, 0, len(meta.Actors))
		for _, actor := range meta.Actors {
			if actor.Name != "" {
				names = append(names, actor.Name)
			}
		}
		if len(names) > 0 {
			patch.Author = strings.Join(names, ", ")
			patch.AuthorSet = true
		}
	}

	if patch.TitleSet || patch.AuthorSet {
		if err := a.cat.UpdateVideoMeta(ctx, videoID, patch); err != nil {
			log.Printf("[metatube] update %q: %v", videoID, err)
		} else {
			log.Printf("[metatube] scraped %q → %q", fileName, meta.Title)
		}
	}

	// Download cover image from MetaTube and set as thumbnail.
	if meta.Provider != "" && meta.ID != "" {
		coverURL := a.metaTubeClient.GetPrimaryImageURL(meta.Provider, meta.ID)
		coverData, contentType, err := a.metaTubeClient.DownloadImage(scrapeCtx, coverURL)
		if err == nil && len(coverData) > 0 {
			thumbPath := a.saveMetaTubeCover(videoID, coverData, contentType)
			if thumbPath != "" {
				_ = a.cat.UpdateVideoMeta(ctx, videoID, catalog.VideoMetaPatch{
					ThumbnailURL:    thumbPath,
					ThumbnailStatus: "ready",
				})
				log.Printf("[metatube] cover saved for %q", fileName)
			}
		}
	}
}

// saveMetaTubeCover writes MetaTube cover image bytes to the local preview
// directory and returns the URL path for the thumbnail.
func (a *App) saveMetaTubeCover(videoID string, data []byte, contentType string) string {
	ext := ".jpg"
	if strings.Contains(contentType, "png") {
		ext = ".png"
	} else if strings.Contains(contentType, "webp") {
		ext = ".webp"
	}
	dir := a.cfg.Storage.LocalPreviewDir
	fileName := videoID + ext
	filePath := filepath.Join(dir, fileName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[metatube] mkdir %s: %v", dir, err)
		return ""
	}
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		log.Printf("[metatube] write cover %s: %v", filePath, err)
		return ""
	}
	return "/p/thumb/" + videoID
}
