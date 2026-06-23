package main

import (
	"context"
	"database/sql"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/video-site/backend/internal/catalog"
	"github.com/video-site/backend/internal/config"
	"github.com/video-site/backend/internal/drives"
	"github.com/video-site/backend/internal/drives/scriptcrawler"
	"github.com/video-site/backend/internal/fingerprint"
	"github.com/video-site/backend/internal/preview"
	"github.com/video-site/backend/internal/proxy"
)

func TestRegisterPreviewWorkerBackfillsPendingWhenDriveTeaserEnabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	seedDriveWithTeaser(t, cat, "drive-id", true)
	video := &catalog.Video{
		ID:            "video-1",
		DriveID:       "drive-id",
		FileID:        "file-id",
		Title:         "Clip",
		PreviewStatus: "pending",
		PublishedAt:   time.Now(),
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := cat.UpsertVideo(ctx, video); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	app := &App{
		cat:          cat,
		workers:      make(map[string]*preview.Worker),
		thumbWorkers: make(map[string]*preview.ThumbWorker),
	}
	worker := preview.NewWorker(&serverFakeTeaserGenerator{}, cat, &serverFakeDrive{})
	go worker.Run(ctx)

	app.registerPreviewWorkers(ctx, "drive-id", worker, nil, nil, func() {})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := cat.GetVideo(ctx, video.ID)
		if err != nil {
			t.Fatalf("get video: %v", err)
		}
		if got.PreviewStatus == "ready" {
			if got.PreviewLocal != "/tmp/video-1.mp4" {
				t.Fatalf("preview local = %q, want generated local teaser path", got.PreviewLocal)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	got, err := cat.GetVideo(ctx, video.ID)
	if err != nil {
		t.Fatalf("get video after timeout: %v", err)
	}
	t.Fatalf("preview status = %q, want ready", got.PreviewStatus)
}

func TestRegisterPreviewWorkersRunThumbnailsAndPreviewsIndependently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	seedDriveWithTeaser(t, cat, "drive-id", true)
	now := time.Now()
	video := &catalog.Video{
		ID:            "video-1",
		DriveID:       "drive-id",
		FileID:        "file-1",
		Title:         "Clip 1",
		PreviewStatus: "pending",
		PublishedAt:   now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := cat.UpsertVideo(ctx, video); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	app := &App{
		cat:          cat,
		workers:      make(map[string]*preview.Worker),
		thumbWorkers: make(map[string]*preview.ThumbWorker),
	}
	gen := &serverBlockingThumbGenerator{
		started: make(chan string, 1),
		release: make(chan struct{}),
	}
	drv := &serverFakeDrive{}
	worker := preview.NewWorker(gen, cat, drv)
	thumbWorker := preview.NewThumbWorker(gen, cat, drv)
	go worker.Run(ctx)
	go thumbWorker.Run(ctx)

	app.registerPreviewWorkers(ctx, "drive-id", worker, thumbWorker, nil, func() {})

	select {
	case got := <-gen.started:
		if got != video.ID {
			t.Fatalf("thumbnail started for %q, want %q", got, video.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("thumbnail generation did not start")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := cat.GetVideo(ctx, video.ID)
		if err != nil {
			t.Fatalf("get video: %v", err)
		}
		if got.PreviewStatus == "ready" {
			if got.ThumbnailURL != "" {
				t.Fatalf("thumbnail url = %q, want preview ready while thumbnail is still blocked", got.ThumbnailURL)
			}
			close(gen.release)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	got, err := cat.GetVideo(ctx, video.ID)
	if err != nil {
		t.Fatalf("get video after timeout: %v", err)
	}
	t.Fatalf("preview status=%q thumbnail=%q, want preview ready before thumbnail finishes", got.PreviewStatus, got.ThumbnailURL)
}

func TestRegisterPreviewWorkersBackfillsHistoricalFingerprints(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	dataPath := filepath.Join(t.TempDir(), "video.mp4")
	data := []byte("historical video content for fingerprint")
	if err := os.WriteFile(dataPath, data, 0o644); err != nil {
		t.Fatalf("write video data: %v", err)
	}

	now := time.Now()
	video := &catalog.Video{
		ID:                "historical-video",
		DriveID:           "drive-id",
		FileID:            "file-id",
		Title:             "Historical",
		Size:              int64(len(data)),
		FingerprintStatus: "pending",
		PublishedAt:       now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := cat.UpsertVideo(ctx, video); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	app := &App{
		cat:                cat,
		workers:            make(map[string]*preview.Worker),
		thumbWorkers:       make(map[string]*preview.ThumbWorker),
		fingerprintWorkers: make(map[string]*fingerprint.Worker),
	}
	drv := &serverFingerprintFakeDrive{path: dataPath}
	fingerprintWorker := fingerprint.NewWorker(cat, drv, fingerprint.Config{})
	go fingerprintWorker.Run(ctx)

	app.registerPreviewWorkers(ctx, "drive-id", nil, nil, fingerprintWorker, func() {})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := cat.GetVideo(ctx, video.ID)
		if err != nil {
			t.Fatalf("get video: %v", err)
		}
		if got.SampledSHA256 != "" && got.FingerprintStatus == "ready" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, err := cat.GetVideo(ctx, video.ID)
	if err != nil {
		t.Fatalf("get video after timeout: %v", err)
	}
	t.Fatalf("fingerprint status=%q sampled=%q, want ready with hash", got.FingerprintStatus, got.SampledSHA256)
}

func TestUpdateScriptCrawlerRunStatePreservesCurrentTeaserSwitch(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	if err := cat.UpsertDrive(ctx, &catalog.Drive{
		ID:     "crawler-id",
		Kind:   scriptcrawler.Kind,
		Name:   "Crawler",
		RootID: "/",
		Credentials: map[string]string{
			"script_path": "/tmp/crawler.py",
			"target_new":  "10",
		},
		TeaserEnabled: false,
	}); err != nil {
		t.Fatalf("seed crawler drive: %v", err)
	}
	if err := cat.SetDriveTeaserEnabled(ctx, "crawler-id", true); err != nil {
		t.Fatalf("toggle teaser: %v", err)
	}

	app := &App{cat: cat}
	if err := app.updateScriptCrawlerRunState(ctx, "crawler-id", nil); err != nil {
		t.Fatalf("update run state: %v", err)
	}
	got, err := cat.GetDrive(ctx, "crawler-id")
	if err != nil {
		t.Fatalf("get crawler drive: %v", err)
	}
	if !got.TeaserEnabled {
		t.Fatal("teaserEnabled = false after run state update, want preserved true")
	}
	if got.Status != "ok" || got.LastError != "" {
		t.Fatalf("status=%q lastError=%q, want ok with no error", got.Status, got.LastError)
	}
	if got.Credentials["last_crawl_at"] == "" || got.Credentials["target_new"] != "10" {
		t.Fatalf("credentials after run state update = %#v", got.Credentials)
	}
}

func TestStopDriveTasksCancelsQueuedTasksAndReplacesWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	drv := &serverFakeDrive{}
	registry := proxy.NewRegistry()
	registry.Set("drive-id", drv)

	gen := &serverFakeTeaserGenerator{}
	oldWorker := preview.NewWorker(gen, cat, drv)
	oldThumbWorker := preview.NewThumbWorker(gen, cat, drv)
	oldFingerprintWorker := fingerprint.NewWorker(cat, drv, fingerprint.Config{})
	oldCanceled := make(chan struct{})

	app := &App{
		cfg:                &config.Config{},
		cat:                cat,
		registry:           registry,
		workers:            map[string]*preview.Worker{"drive-id": oldWorker},
		thumbWorkers:       map[string]*preview.ThumbWorker{"drive-id": oldThumbWorker},
		fingerprintWorkers: map[string]*fingerprint.Worker{"drive-id": oldFingerprintWorker},
		cancels: map[string]context.CancelFunc{
			"drive-id": func() { close(oldCanceled) },
		},
		scanQueued:          map[string]bool{"drive-id": true},
		scanProgress:        map[string]driveScanProgress{"drive-id": {Scanned: 8, Added: 2}},
		fingerprintQueueing: map[string]bool{"drive-id": true},
	}
	taskCtx, done := app.registerDriveTaskContext(ctx, "drive-id")
	defer done()

	if !app.stopDriveTasks(ctx, "drive-id") {
		t.Fatal("stopDriveTasks returned false, want true")
	}
	select {
	case <-oldCanceled:
	case <-time.After(time.Second):
		t.Fatal("old worker cancel was not called")
	}
	if err := taskCtx.Err(); err == nil {
		t.Fatal("registered drive task context was not canceled")
	}
	if app.scanQueued["drive-id"] {
		t.Fatal("scan queue marker was not cleared")
	}
	if _, ok := app.scanProgress["drive-id"]; ok {
		t.Fatal("scan progress marker was not cleared")
	}
	if app.fingerprintQueueing["drive-id"] {
		t.Fatal("fingerprint queue marker was not cleared")
	}

	app.mu.Lock()
	newWorker := app.workers["drive-id"]
	newThumbWorker := app.thumbWorkers["drive-id"]
	newFingerprintWorker := app.fingerprintWorkers["drive-id"]
	newCancel := app.cancels["drive-id"]
	app.mu.Unlock()
	if newWorker == nil || newWorker == oldWorker {
		t.Fatalf("preview worker was not replaced")
	}
	if newThumbWorker == nil || newThumbWorker == oldThumbWorker {
		t.Fatalf("thumb worker was not replaced")
	}
	if newFingerprintWorker == nil || newFingerprintWorker == oldFingerprintWorker {
		t.Fatalf("fingerprint worker was not replaced")
	}
	if newCancel == nil {
		t.Fatalf("replacement worker cancel was not registered")
	}
	newCancel()
}

func TestScheduleScanRejectsDriveWithActiveGenerationWork(t *testing.T) {
	ctx := context.Background()
	thumbWorker := preview.NewThumbWorker(&serverFakeTeaserGenerator{}, nil, &serverFakeDrive{})
	if !thumbWorker.Enqueue(&catalog.Video{ID: "busy-video", DriveID: "drive-id", Title: "Busy Video"}) {
		t.Fatal("failed to enqueue busy thumbnail task")
	}
	app := &App{
		thumbWorkers: map[string]*preview.ThumbWorker{"drive-id": thumbWorker},
	}

	if app.scheduleScan(ctx, "drive-id") {
		t.Fatal("scheduleScan accepted a drive with active generation work")
	}
}

func TestScheduleScanRunsDifferentDrivesConcurrently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	seedDriveWithTeaser(t, cat, "drive-a", true)
	seedDriveWithTeaser(t, cat, "drive-b", true)

	started := make(chan string, 2)
	release := make(chan struct{})
	registry := proxy.NewRegistry()
	registry.Set("drive-a", &serverBlockingListDrive{id: "drive-a", started: started, release: release})
	registry.Set("drive-b", &serverBlockingListDrive{id: "drive-b", started: started, release: release})

	app := &App{
		cfg: &config.Config{
			Scanner: config.Scanner{VideoExtensions: []string{".mp4"}},
		},
		cat:      cat,
		registry: registry,
	}

	if !app.scheduleScan(ctx, "drive-a") {
		t.Fatal("scheduleScan drive-a was rejected")
	}
	if !app.scheduleScan(ctx, "drive-b") {
		t.Fatal("scheduleScan drive-b was rejected")
	}

	seen := map[string]struct{}{}
	deadline := time.After(time.Second)
	for len(seen) < 2 {
		select {
		case id := <-started:
			seen[id] = struct{}{}
		case <-deadline:
			close(release)
			t.Fatalf("started drives = %#v, want both drives before releasing List", seen)
		}
	}
	close(release)
}

func TestDriveGenerationStatusIncludesScanState(t *testing.T) {
	app := &App{
		scanQueued:   map[string]bool{"drive-id": true},
		scanProgress: map[string]driveScanProgress{"drive-id": {Scanned: 12, Added: 3}},
	}

	status := app.driveGenerationStatuses()["drive-id"].Scan
	if status.State != "scanning" {
		t.Fatalf("scan status = %#v, want scanning", status)
	}
	if status.ScannedCount != 12 || status.AddedCount != 3 {
		t.Fatalf("scan counts = scanned %d added %d, want 12 and 3", status.ScannedCount, status.AddedCount)
	}
}

func TestDriveGenerationStatusIncludesScanCooldown(t *testing.T) {
	until := time.Now().Add(time.Hour).Round(time.Second)
	app := &App{
		scanQueued: map[string]bool{"drive-id": true},
		scanProgress: map[string]driveScanProgress{
			"drive-id": {Scanned: 12, Added: 3, CooldownUntil: until},
		},
	}

	status := app.driveGenerationStatuses()["drive-id"].Scan
	if status.State != "cooling" {
		t.Fatalf("scan status = %#v, want cooling", status)
	}
	if status.CooldownUntil != until.Format(time.RFC3339) {
		t.Fatalf("cooldown until = %q, want %q", status.CooldownUntil, until.Format(time.RFC3339))
	}
}

func TestGuangYaPanGenerationCooldowns(t *testing.T) {
	drv := &serverFakeKindDrive{id: "gy", kind: "guangyapan"}
	if got := generationCooldownForDrive(drv); got != 10*time.Minute {
		t.Fatalf("generation cooldown = %s, want 10m", got)
	}
	if got := fingerprintConfigForDrive(drv).RateLimitCooldown; got != 10*time.Minute {
		t.Fatalf("fingerprint cooldown = %s, want 10m", got)
	}
	if got := scanCooldownForDrive(drv); got != 10*time.Minute {
		t.Fatalf("scan cooldown = %s, want 10m", got)
	}
}

func TestRunCrawlerMigrationAfterManualCrawlRequiresCrawlerUploadTarget(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	if err := cat.UpsertDrive(ctx, &catalog.Drive{
		ID:     "crawler-main",
		Kind:   scriptcrawler.Kind,
		Name:   "Crawler",
		RootID: "/",
		Credentials: map[string]string{
			"script_path": "/tmp/crawler.py",
		},
	}); err != nil {
		t.Fatalf("seed crawler: %v", err)
	}

	registry := proxy.NewRegistry()
	migrator := &serverFakeCrawlerUploadRunner{}
	app := &App{
		cat:                cat,
		registry:           registry,
		crawlerUploader:    migrator,
		workers:            map[string]*preview.Worker{},
		thumbWorkers:       map[string]*preview.ThumbWorker{},
		fingerprintWorkers: map[string]*fingerprint.Worker{},
	}

	app.runCrawlerMigrationAfterManualCrawl(ctx, "crawler-main")
	if migrator.called != 0 {
		t.Fatalf("migration called without upload target")
	}

	d, err := cat.GetDrive(ctx, "crawler-main")
	if err != nil {
		t.Fatalf("get crawler: %v", err)
	}
	d.Credentials["upload_drive_id"] = "pikpak"
	if err := cat.UpsertDrive(ctx, d); err != nil {
		t.Fatalf("set upload target: %v", err)
	}
	app.runCrawlerMigrationAfterManualCrawl(ctx, "crawler-main")
	if migrator.called != 1 {
		t.Fatalf("migration calls = %d, want 1", migrator.called)
	}
}

func TestScheduleCrawlerUploadMigrationRunsForConfiguredCrawler(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	if err := cat.UpsertDrive(ctx, &catalog.Drive{
		ID:     "crawler-truvaze",
		Kind:   scriptcrawler.Kind,
		Name:   "Truvaze",
		RootID: "/",
		Credentials: map[string]string{
			"script_path":     "/tmp/Truvaze.py",
			"upload_drive_id": "pikpak",
		},
	}); err != nil {
		t.Fatalf("seed crawler: %v", err)
	}
	registry := proxy.NewRegistry()
	registry.Set("crawler-truvaze", &serverFakeKindDrive{id: "crawler-truvaze", kind: scriptcrawler.Kind})
	migrator := &serverFakeCrawlerUploadRunner{}
	app := &App{
		cat:                cat,
		registry:           registry,
		crawlerUploader:    migrator,
		workers:            map[string]*preview.Worker{},
		thumbWorkers:       map[string]*preview.ThumbWorker{},
		fingerprintWorkers: map[string]*fingerprint.Worker{},
	}

	if !app.scheduleCrawlerUploadMigration(ctx, "crawler-truvaze") {
		t.Fatal("scheduleCrawlerUploadMigration returned false, want true")
	}
	deadline := time.After(time.Second)
	for migrator.called == 0 {
		select {
		case <-deadline:
			t.Fatalf("migration calls = %d, want 1", migrator.called)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestScheduleCrawlerUploadMigrationSkipsWithoutUploadTarget(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	if err := cat.UpsertDrive(ctx, &catalog.Drive{
		ID:          "crawler-local",
		Kind:        scriptcrawler.Kind,
		Name:        "Local Only",
		RootID:      "/",
		Credentials: map[string]string{"script_path": "/tmp/local.py"},
	}); err != nil {
		t.Fatalf("seed crawler: %v", err)
	}
	migrator := &serverFakeCrawlerUploadRunner{}
	app := &App{cat: cat, registry: proxy.NewRegistry(), crawlerUploader: migrator}

	if app.scheduleCrawlerUploadMigration(ctx, "crawler-local") {
		t.Fatal("scheduleCrawlerUploadMigration returned true without upload target")
	}
	if migrator.called != 0 {
		t.Fatalf("migration calls = %d, want 0", migrator.called)
	}
}

func TestScheduleManualCrawlerUploadMigrationRunsWhenAssetsReady(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	if err := cat.UpsertDrive(ctx, &catalog.Drive{
		ID:            "crawler-ready",
		Kind:          scriptcrawler.Kind,
		Name:          "Ready Crawler",
		RootID:        "/",
		TeaserEnabled: true,
		Credentials: map[string]string{
			"script_path":     "/tmp/ready.py",
			"upload_drive_id": "pikpak-target",
		},
	}); err != nil {
		t.Fatalf("seed crawler: %v", err)
	}
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:                scriptcrawler.BuildVideoID("crawler-ready", "source-1"),
		DriveID:           "crawler-ready",
		FileID:            "source-1.mp4",
		FileName:          "source-1.mp4",
		Title:             "Source 1",
		Size:              123,
		Ext:               "mp4",
		SampledSHA256:     "sampled-source-1",
		FingerprintStatus: "ready",
		PreviewStatus:     "ready",
		PublishedAt:       time.Now(),
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	registry := proxy.NewRegistry()
	registry.Set("crawler-ready", &serverFakeKindDrive{id: "crawler-ready", kind: scriptcrawler.Kind})
	registry.Set("pikpak-target", &serverFakeKindDrive{id: "pikpak-target", kind: "pikpak"})
	migrator := &serverFakeCrawlerUploadRunner{}
	app := &App{
		cat:                cat,
		registry:           registry,
		crawlerUploader:    migrator,
		workers:            map[string]*preview.Worker{},
		thumbWorkers:       map[string]*preview.ThumbWorker{},
		fingerprintWorkers: map[string]*fingerprint.Worker{},
	}

	accepted, message := app.scheduleManualCrawlerUploadMigration(ctx, "crawler-ready")
	if !accepted {
		t.Fatalf("accepted = false, message = %q", message)
	}
	deadline := time.After(time.Second)
	for migrator.called == 0 {
		select {
		case <-deadline:
			t.Fatalf("migration calls = %d, want 1", migrator.called)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestScheduleManualCrawlerUploadMigrationRejectsPendingFingerprint(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	if err := cat.UpsertDrive(ctx, &catalog.Drive{
		ID:            "crawler-pending",
		Kind:          scriptcrawler.Kind,
		Name:          "Pending Crawler",
		RootID:        "/",
		TeaserEnabled: true,
		Credentials: map[string]string{
			"script_path":     "/tmp/pending.py",
			"upload_drive_id": "pikpak-target",
		},
	}); err != nil {
		t.Fatalf("seed crawler: %v", err)
	}
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:            scriptcrawler.BuildVideoID("crawler-pending", "source-1"),
		DriveID:       "crawler-pending",
		FileID:        "source-1.mp4",
		FileName:      "source-1.mp4",
		Title:         "Source 1",
		Size:          123,
		Ext:           "mp4",
		PreviewStatus: "ready",
		PublishedAt:   time.Now(),
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	migrator := &serverFakeCrawlerUploadRunner{}
	app := &App{cat: cat, registry: proxy.NewRegistry(), crawlerUploader: migrator}

	accepted, message := app.scheduleManualCrawlerUploadMigration(ctx, "crawler-pending")
	if accepted {
		t.Fatal("accepted = true, want false")
	}
	if !strings.Contains(message, "指纹") {
		t.Fatalf("message = %q, want fingerprint reason", message)
	}
	if migrator.called != 0 {
		t.Fatalf("migration calls = %d, want 0", migrator.called)
	}
}

func TestDriveGenerationStatusUsesWorkerQueueNotPendingCatalogRows(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:            "pending-thumb",
		DriveID:       "drive-id",
		FileID:        "file-id",
		Title:         "Pending Thumb",
		PreviewStatus: "ready",
		PublishedAt:   now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := cat.UpdateVideoMeta(ctx, "pending-thumb", catalog.VideoMetaPatch{ThumbnailStatus: "pending"}); err != nil {
		t.Fatalf("mark thumbnail pending: %v", err)
	}

	thumbWorker := preview.NewThumbWorker(&serverFakeTeaserGenerator{}, cat, &serverFakeDrive{})
	app := &App{
		cat:                cat,
		workers:            map[string]*preview.Worker{},
		thumbWorkers:       map[string]*preview.ThumbWorker{"drive-id": thumbWorker},
		fingerprintWorkers: map[string]*fingerprint.Worker{},
	}

	status := app.driveGenerationStatuses()["drive-id"].Thumbnail
	if status.State != "idle" || status.QueueLength != 0 {
		t.Fatalf("thumbnail status = %#v, want idle with empty worker queue", status)
	}
}

func TestRegenFailedThumbnailsQueuesPendingRowsAfterStop(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:            "pending-thumb",
		DriveID:       "drive-id",
		FileID:        "file-id",
		Title:         "Pending Thumb",
		PreviewStatus: "ready",
		PublishedAt:   now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := cat.UpdateVideoMeta(ctx, "pending-thumb", catalog.VideoMetaPatch{ThumbnailStatus: "pending"}); err != nil {
		t.Fatalf("mark thumbnail pending: %v", err)
	}

	thumbWorker := preview.NewThumbWorker(&serverFakeTeaserGenerator{}, cat, &serverFakeDrive{})
	app := &App{
		cat:          cat,
		thumbWorkers: map[string]*preview.ThumbWorker{"drive-id": thumbWorker},
	}

	app.regenFailedThumbnails(ctx, "drive-id")

	if got := thumbWorker.Status().QueueLength; got != 1 {
		t.Fatalf("thumb queue length = %d, want pending row re-enqueued", got)
	}
}

func TestRunScanStartsFingerprintBeforeThumbnailAndPreviewDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	seedDriveWithTeaser(t, cat, "drive-id", true)

	dataPath := filepath.Join(t.TempDir(), "scan-video.mp4")
	data := []byte("scan video content for independent fingerprint")
	if err := os.WriteFile(dataPath, data, 0o644); err != nil {
		t.Fatalf("write video data: %v", err)
	}

	drv := &serverScanFingerprintFakeDrive{
		serverFingerprintFakeDrive: serverFingerprintFakeDrive{path: dataPath},
		entries: []drives.Entry{{
			ID:       "file-id",
			Name:     "scan-video.mp4",
			Size:     int64(len(data)),
			ParentID: "root",
		}},
	}
	registry := proxy.NewRegistry()
	registry.Set("drive-id", drv)

	gen := &serverFakeTeaserGenerator{}
	worker := preview.NewWorker(gen, cat, drv)
	thumbWorker := preview.NewThumbWorker(gen, cat, drv)
	fingerprintWorker := fingerprint.NewWorker(cat, drv, fingerprint.Config{})
	go fingerprintWorker.Run(ctx)

	app := &App{
		cfg: &config.Config{
			Scanner: config.Scanner{VideoExtensions: []string{".mp4"}},
		},
		cat:                cat,
		registry:           registry,
		workers:            map[string]*preview.Worker{"drive-id": worker},
		thumbWorkers:       map[string]*preview.ThumbWorker{"drive-id": thumbWorker},
		fingerprintWorkers: map[string]*fingerprint.Worker{"drive-id": fingerprintWorker},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.runScan(ctx, "drive-id")
	}()

	videoID := "fake-drive-id-file-id"
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := cat.GetVideo(ctx, videoID)
		if err == nil && got.SampledSHA256 != "" && got.FingerprintStatus == "ready" {
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("scan did not stop after context cancel")
			}
			if got.ThumbnailURL != "" {
				t.Fatalf("thumbnail url = %q, want fingerprint before thumbnail generation", got.ThumbnailURL)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scan did not stop after context cancel")
	}
	got, err := cat.GetVideo(context.Background(), videoID)
	if err != nil {
		t.Fatalf("get video after timeout: %v", err)
	}
	t.Fatalf("fingerprint status=%q sampled=%q, want ready before thumbnail/preview drain", got.FingerprintStatus, got.SampledSHA256)
}

func TestNightlyTargetsComeFromCatalogBeforeDriveAttach(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	for _, d := range []*catalog.Drive{
		{ID: "115", Kind: "p115", Name: "115", RootID: "0", TeaserEnabled: true},
		{ID: "pikpak", Kind: "pikpak", Name: "PikPak", RootID: "0", TeaserEnabled: true},
		{ID: "crawler-main", Kind: scriptcrawler.Kind, Name: "Crawler", RootID: "/", Credentials: map[string]string{"script_path": "/tmp/crawler.py"}, TeaserEnabled: true},
		{ID: "crawler-deleted", Kind: scriptcrawler.Kind, Name: "Deleted Crawler", RootID: "/", Credentials: map[string]string{}, TeaserEnabled: true},
	} {
		if err := cat.UpsertDrive(ctx, d); err != nil {
			t.Fatalf("seed drive %s: %v", d.ID, err)
		}
	}

	app := &App{cat: cat}
	scanIDs := app.listScanTargetIDs(ctx)
	if len(scanIDs) != 2 || scanIDs[0] != "115" || scanIDs[1] != "pikpak" {
		t.Fatalf("scan target ids = %#v, want 115 and pikpak from catalog", scanIDs)
	}
	crawlerIDs := app.listCrawlerDriveIDs(ctx)
	if len(crawlerIDs) != 1 || crawlerIDs[0] != "crawler-main" {
		t.Fatalf("crawler ids = %#v, want crawler-page script drive", crawlerIDs)
	}
}

func TestAttachDriveRejectsUnknownKind(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	d := &catalog.Drive{
		ID:            "unknown-main",
		Kind:          "unknown",
		Name:          "Unknown",
		RootID:        "/",
		TeaserEnabled: true,
	}
	if err := cat.UpsertDrive(ctx, d); err != nil {
		t.Fatalf("seed drive: %v", err)
	}

	app := &App{cat: cat, registry: proxy.NewRegistry()}
	err = app.attachDrive(ctx, d)
	if err == nil || !strings.Contains(err.Error(), "unknown drive kind: unknown") {
		t.Fatalf("attach err = %v, want unknown kind error", err)
	}
	if _, ok := app.registry.Get(d.ID); ok {
		t.Fatal("unknown drive should not be registered")
	}
}

func TestFailedThumbnailsDoNotBlockPreviewGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	seedDriveWithTeaser(t, cat, "drive-id", true)
	now := time.Now()
	video := &catalog.Video{
		ID:            "video-failed-thumb",
		DriveID:       "drive-id",
		FileID:        "file-1",
		Title:         "Clip With Failed Thumb",
		PreviewStatus: "pending",
		PublishedAt:   now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := cat.UpsertVideo(ctx, video); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := cat.UpdateVideoMeta(ctx, video.ID, catalog.VideoMetaPatch{ThumbnailStatus: "failed"}); err != nil {
		t.Fatalf("mark thumbnail failed: %v", err)
	}
	missing, err := cat.CountVideosNeedingThumbnail(ctx, "drive-id")
	if err != nil {
		t.Fatalf("count missing thumbnails: %v", err)
	}
	if missing != 0 {
		t.Fatalf("missing thumbnails = %d, want failed thumbnails excluded", missing)
	}

	app := &App{
		cat:          cat,
		workers:      make(map[string]*preview.Worker),
		thumbWorkers: make(map[string]*preview.ThumbWorker),
	}
	gen := &serverFakeTeaserGenerator{}
	drv := &serverFakeDrive{}
	worker := preview.NewWorker(gen, cat, drv)
	thumbWorker := preview.NewThumbWorker(gen, cat, drv)
	go worker.Run(ctx)
	go thumbWorker.Run(ctx)

	app.registerPreviewWorkers(ctx, "drive-id", worker, thumbWorker, nil, func() {})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := cat.GetVideo(ctx, video.ID)
		if err != nil {
			t.Fatalf("get video: %v", err)
		}
		if got.PreviewStatus == "ready" {
			events := gen.Events()
			if len(events) != 1 || events[0] != "preview:"+video.ID {
				t.Fatalf("events = %#v, want preview only", events)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	got, err := cat.GetVideo(ctx, video.ID)
	if err != nil {
		t.Fatalf("get video after timeout: %v", err)
	}
	t.Fatalf("preview status = %q, want ready; events=%#v", got.PreviewStatus, gen.Events())
}

func TestRegenFailedPreviewsQueuesOnlyFailedVideosForDrive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	seedDriveWithTeaser(t, cat, "drive-id", true)
	seedDriveWithTeaser(t, cat, "other-drive", true)
	now := time.Now()
	for _, v := range []*catalog.Video{
		{ID: "target-failed", DriveID: "drive-id", FileID: "file-1", Title: "Target Failed", PreviewStatus: "failed"},
		{ID: "target-ready", DriveID: "drive-id", FileID: "file-2", Title: "Target Ready", PreviewStatus: "ready", PreviewLocal: "/tmp/ready.mp4"},
		{ID: "other-failed", DriveID: "other-drive", FileID: "file-3", Title: "Other Failed", PreviewStatus: "failed"},
	} {
		v.PublishedAt = now
		v.CreatedAt = now
		v.UpdatedAt = now
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed video %s: %v", v.ID, err)
		}
	}

	app := &App{
		cat:          cat,
		workers:      make(map[string]*preview.Worker),
		thumbWorkers: make(map[string]*preview.ThumbWorker),
	}
	worker := preview.NewWorker(&serverFakeTeaserGenerator{}, cat, &serverFakeDrive{})
	go worker.Run(ctx)
	app.mu.Lock()
	app.workers["drive-id"] = worker
	app.mu.Unlock()

	app.regenFailedPreviews(ctx, "drive-id")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := cat.GetVideo(ctx, "target-failed")
		if err != nil {
			t.Fatalf("get target failed: %v", err)
		}
		if got.PreviewStatus == "ready" {
			if got.PreviewLocal != "/tmp/target-failed.mp4" {
				t.Fatalf("target preview local = %q, want regenerated local teaser path", got.PreviewLocal)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	target, err := cat.GetVideo(ctx, "target-failed")
	if err != nil {
		t.Fatalf("get regenerated target: %v", err)
	}
	if target.PreviewStatus != "ready" {
		t.Fatalf("target preview status = %q, want ready", target.PreviewStatus)
	}
	ready, err := cat.GetVideo(ctx, "target-ready")
	if err != nil {
		t.Fatalf("get target ready: %v", err)
	}
	if ready.PreviewLocal != "/tmp/ready.mp4" || ready.PreviewStatus != "ready" {
		t.Fatalf("ready video changed: status=%q local=%q", ready.PreviewStatus, ready.PreviewLocal)
	}
	other, err := cat.GetVideo(ctx, "other-failed")
	if err != nil {
		t.Fatalf("get other failed: %v", err)
	}
	if other.PreviewStatus != "failed" {
		t.Fatalf("other drive preview status = %q, want failed", other.PreviewStatus)
	}
}

func TestEnqueueUploadedVideoQueuesLocalGenerationByDefault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	video := &catalog.Video{
		ID:            "local-upload-video",
		DriveID:       "local-upload",
		FileID:        "upload-1.mp4",
		Title:         "Uploaded",
		PreviewStatus: "pending",
		PublishedAt:   time.Now(),
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := cat.UpsertVideo(ctx, video); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	app := &App{
		cat:          cat,
		workers:      make(map[string]*preview.Worker),
		thumbWorkers: make(map[string]*preview.ThumbWorker),
	}
	gen := &serverFakeTeaserGenerator{}
	drv := &serverLocalUploadFakeDrive{}
	worker := preview.NewWorker(gen, cat, drv)
	thumbWorker := preview.NewThumbWorker(gen, cat, drv)
	go worker.Run(ctx)
	go thumbWorker.Run(ctx)
	app.mu.Lock()
	app.workers["local-upload"] = worker
	app.thumbWorkers["local-upload"] = thumbWorker
	app.mu.Unlock()

	app.enqueueUploadedVideo(ctx, video)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := cat.GetVideo(ctx, video.ID)
		if err != nil {
			t.Fatalf("get video: %v", err)
		}
		if got.PreviewStatus == "ready" && got.ThumbnailURL != "" {
			if got.PreviewLocal != "/tmp/local-upload-video.mp4" {
				t.Fatalf("preview local = %q, want generated local teaser path", got.PreviewLocal)
			}
			if got.ThumbnailURL != "/p/thumb/local-upload-video" {
				t.Fatalf("thumbnail url = %q, want generated thumbnail URL", got.ThumbnailURL)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	got, err := cat.GetVideo(ctx, video.ID)
	if err != nil {
		t.Fatalf("get video after timeout: %v", err)
	}
	t.Fatalf("preview status = %q, thumbnail url = %q; want generated local teaser and thumbnail", got.PreviewStatus, got.ThumbnailURL)
}

func TestShouldScanDriveSkipsLocalUpload(t *testing.T) {
	if shouldScanDrive(&serverLocalUploadFakeDrive{}) {
		t.Fatal("local upload drive should not be scanned")
	}
	if !shouldScanDrive(&serverFakeDrive{}) {
		t.Fatal("normal drive should be scanned")
	}
}

func TestCleanupMissingPikPakVideosRemovesDatabaseRowsAndLocalAssets(t *testing.T) {
	ctx := context.Background()
	localDir := t.TempDir()
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	obsoletePreview := filepath.Join(localDir, "obsolete.mp4")
	obsoleteThumb := filepath.Join(localDir, "thumbs", "pikpak-PikPak-obsolete.jpg")
	keptPreview := filepath.Join(localDir, "kept.mp4")
	for _, path := range []string{obsoletePreview, obsoleteThumb, keptPreview} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("asset"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	now := time.Now()
	for _, v := range []*catalog.Video{
		{
			ID:            "pikpak-PikPak-obsolete",
			DriveID:       "PikPak",
			FileID:        "obsolete",
			Title:         "Obsolete",
			PreviewStatus: "ready",
			PreviewLocal:  obsoletePreview,
		},
		{
			ID:            "pikpak-PikPak-kept",
			DriveID:       "PikPak",
			FileID:        "kept",
			Title:         "Kept",
			PreviewStatus: "ready",
			PreviewLocal:  keptPreview,
		},
		{
			ID:            "onedrive-OneDrive-obsolete",
			DriveID:       "OneDrive",
			FileID:        "obsolete",
			Title:         "Other Drive",
			PreviewStatus: "ready",
		},
	} {
		v.PublishedAt = now
		v.CreatedAt = now
		v.UpdatedAt = now
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed %s: %v", v.ID, err)
		}
	}

	app := &App{
		cfg: &config.Config{Storage: config.Storage{LocalPreviewDir: localDir}},
		cat: cat,
	}
	removed, err := app.cleanupMissingDriveVideos(ctx, "PikPak", map[string]struct{}{"kept": {}}, nil, true)
	if err != nil {
		t.Fatalf("cleanup missing videos: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := cat.GetVideo(ctx, "pikpak-PikPak-obsolete"); err != sql.ErrNoRows {
		t.Fatalf("obsolete video lookup error = %v, want sql.ErrNoRows", err)
	}
	if _, err := cat.GetVideo(ctx, "pikpak-PikPak-kept"); err != nil {
		t.Fatalf("kept video missing after cleanup: %v", err)
	}
	if _, err := cat.GetVideo(ctx, "onedrive-OneDrive-obsolete"); err != nil {
		t.Fatalf("other drive video missing after cleanup: %v", err)
	}
	for _, path := range []string{obsoletePreview, obsoleteThumb} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("obsolete asset %s still exists, stat err=%v", path, err)
		}
	}
	if _, err := os.Stat(keptPreview); err != nil {
		t.Fatalf("kept preview missing: %v", err)
	}
}

func TestCleanupDriveVideosForDeleteRemovesRowsAndGeneratedAssetsOnly(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	localDir := filepath.Join(root, "previews")
	originalDir := filepath.Join(root, "local-videos")
	originalVideo := filepath.Join(originalDir, "clip.mp4")
	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	for _, path := range []string{originalVideo} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	if err := cat.UpsertDrive(ctx, &catalog.Drive{
		ID:            "local-main",
		Kind:          "localstorage",
		Name:          "Local",
		RootID:        "/",
		Credentials:   map[string]string{"path": originalDir},
		TeaserEnabled: true,
	}); err != nil {
		t.Fatalf("seed drive: %v", err)
	}

	previewPath := filepath.Join(localDir, "localstorage-local-main-file.mp4")
	thumbPath := filepath.Join(localDir, "thumbs", "localstorage-local-main-file.jpg")
	for _, path := range []string{previewPath, thumbPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("generated"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	now := time.Now()
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:            "localstorage-local-main-file",
		DriveID:       "local-main",
		FileID:        "encoded-local-file",
		Title:         "Local File",
		PreviewLocal:  previewPath,
		PreviewStatus: "ready",
		ThumbnailURL:  "/p/thumb/localstorage-local-main-file",
		PublishedAt:   now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed local video: %v", err)
	}
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:            "pikpak-other",
		DriveID:       "PikPak",
		FileID:        "other",
		Title:         "Other",
		PreviewStatus: "ready",
		PublishedAt:   now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed other video: %v", err)
	}

	app := &App{
		cfg:                &config.Config{Storage: config.Storage{LocalPreviewDir: localDir}},
		cat:                cat,
		registry:           proxy.NewRegistry(),
		workers:            make(map[string]*preview.Worker),
		thumbWorkers:       make(map[string]*preview.ThumbWorker),
		fingerprintWorkers: make(map[string]*fingerprint.Worker),
	}
	removed, err := app.cleanupDriveVideosForDelete(ctx, "local-main")
	if err != nil {
		t.Fatalf("cleanup drive videos: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := cat.GetVideo(ctx, "localstorage-local-main-file"); err != sql.ErrNoRows {
		t.Fatalf("deleted video lookup error = %v, want sql.ErrNoRows", err)
	}
	if _, err := cat.GetVideo(ctx, "pikpak-other"); err != nil {
		t.Fatalf("other drive video missing: %v", err)
	}
	for _, path := range []string{previewPath, thumbPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("generated asset %s still exists, stat err=%v", path, err)
		}
	}
	if _, err := os.Stat(originalVideo); err != nil {
		t.Fatalf("original local video should remain, stat err=%v", err)
	}
}

func TestDeleteVideoRemovesGeneratedAssetsKeepsLocalOriginalAndTombstones(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	localDir := filepath.Join(root, "previews")
	originalDir := filepath.Join(root, "local-videos")
	originalVideo := filepath.Join(originalDir, "clip.mp4")
	if err := os.MkdirAll(originalDir, 0o755); err != nil {
		t.Fatalf("mkdir original dir: %v", err)
	}
	if err := os.WriteFile(originalVideo, []byte("original"), 0o644); err != nil {
		t.Fatalf("write original: %v", err)
	}

	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.UpsertDrive(ctx, &catalog.Drive{
		ID:            "local-main",
		Kind:          "localstorage",
		Name:          "Local",
		RootID:        "/",
		Credentials:   map[string]string{"path": originalDir},
		TeaserEnabled: true,
	}); err != nil {
		t.Fatalf("seed drive: %v", err)
	}

	previewPath := filepath.Join(localDir, "localstorage-local-main-file.mp4")
	thumbPath := filepath.Join(localDir, "thumbs", "localstorage-local-main-file.jpg")
	for _, path := range []string{previewPath, thumbPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("generated"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	now := time.Now()
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:                "localstorage-local-main-file",
		DriveID:           "local-main",
		FileID:            "file",
		FileName:          "clip.mp4",
		SampledSHA256:     "sampled",
		FingerprintStatus: "ready",
		Title:             "Local File",
		PreviewLocal:      previewPath,
		PreviewStatus:     "ready",
		ThumbnailURL:      "/p/thumb/localstorage-local-main-file",
		Size:              123,
		PublishedAt:       now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	app := &App{
		cfg: &config.Config{Storage: config.Storage{LocalPreviewDir: localDir}},
		cat: cat,
	}
	result, err := app.deleteVideo(ctx, "localstorage-local-main-file", false)
	if err != nil {
		t.Fatalf("delete video: %v", err)
	}
	if !result.OK || result.DeletedSource {
		t.Fatalf("delete result = %#v, want ok without source deletion", result)
	}
	if _, err := cat.GetVideo(ctx, "localstorage-local-main-file"); err != sql.ErrNoRows {
		t.Fatalf("deleted video lookup error = %v, want sql.ErrNoRows", err)
	}
	deleted, err := cat.IsDeletedVideoCandidate(ctx, "localstorage-local-main-file", "local-main", "file", "", "clip.mp4", 123)
	if err != nil {
		t.Fatalf("check tombstone: %v", err)
	}
	if !deleted {
		t.Fatal("deleted video tombstone missing")
	}
	for _, path := range []string{previewPath, thumbPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("generated asset %s still exists, stat err=%v", path, err)
		}
	}
	if _, err := os.Stat(originalVideo); err != nil {
		t.Fatalf("original local video was removed: %v", err)
	}
}

func TestDeleteVideoRemovesSourceFileWhenRequested(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	localDir := filepath.Join(root, "previews")
	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = cat.Close() })

	previewPath := filepath.Join(localDir, "video-with-source.mp4")
	thumbPath := filepath.Join(localDir, "thumbs", "video-with-source.jpg")
	for _, path := range []string{previewPath, thumbPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("file"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	now := time.Now()
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:            "video-with-source",
		DriveID:       "source-drive",
		FileID:        "source-file",
		FileName:      "clip.mp4",
		Title:         "Source File",
		PreviewLocal:  previewPath,
		PreviewStatus: "ready",
		ThumbnailURL:  "/p/thumb/video-with-source",
		Size:          123,
		PublishedAt:   now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	registry := proxy.NewRegistry()
	drv := &serverRemovableFakeDrive{id: "source-drive"}
	registry.Set(drv.ID(), drv)
	app := &App{
		cfg:      &config.Config{Storage: config.Storage{LocalPreviewDir: localDir}},
		cat:      cat,
		registry: registry,
	}
	result, err := app.deleteVideo(ctx, "video-with-source", true)
	if err != nil {
		t.Fatalf("delete video: %v", err)
	}
	if !result.OK || !result.DeletedSource {
		t.Fatalf("delete result = %#v, want source deleted", result)
	}
	if got, want := drv.removedFileID, "source-file"; got != want {
		t.Fatalf("removed source fileID = %q, want %q", got, want)
	}
	if _, err := cat.GetVideo(ctx, "video-with-source"); err != sql.ErrNoRows {
		t.Fatalf("deleted video lookup error = %v, want sql.ErrNoRows", err)
	}
	for _, path := range []string{previewPath, thumbPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("generated asset %s still exists, stat err=%v", path, err)
		}
	}
}

func TestDeleteVideoUsesSourceRemoverWithCatalogMetadata(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = cat.Close() })

	now := time.Now()
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:          "video-with-rich-source",
		DriveID:     "source-drive",
		FileID:      "source-fid",
		ParentID:    "parent-dir",
		FileName:    "clip.mp4",
		Title:       "Source File",
		Size:        123,
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	registry := proxy.NewRegistry()
	drv := &serverSourceRemovableFakeDrive{id: "source-drive"}
	registry.Set(drv.ID(), drv)
	app := &App{
		cfg:      &config.Config{Storage: config.Storage{LocalPreviewDir: filepath.Join(t.TempDir(), "previews")}},
		cat:      cat,
		registry: registry,
	}
	result, err := app.deleteVideo(ctx, "video-with-rich-source", true)
	if err != nil {
		t.Fatalf("delete video: %v", err)
	}
	if !result.OK || !result.DeletedSource {
		t.Fatalf("delete result = %#v, want source deleted", result)
	}
	if drv.fallbackRemoveCalled {
		t.Fatal("fallback Remove was called, want SourceRemover")
	}
	want := drives.SourceFile{
		FileID:   "source-fid",
		ParentID: "parent-dir",
		Name:     "clip.mp4",
		Size:     123,
	}
	if drv.removedSource != want {
		t.Fatalf("removed source = %#v, want %#v", drv.removedSource, want)
	}
}

func TestDeleteVideoRemovesScriptCrawlerSourceFile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	localDir := filepath.Join(root, "previews")
	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = cat.Close() })

	if err := cat.UpsertDrive(ctx, &catalog.Drive{
		ID:            "crawler-main",
		Kind:          scriptcrawler.Kind,
		Name:          "Crawler",
		RootID:        "/",
		TeaserEnabled: true,
	}); err != nil {
		t.Fatalf("seed drive: %v", err)
	}
	app := &App{
		cfg:      &config.Config{Storage: config.Storage{LocalPreviewDir: localDir}},
		cat:      cat,
		registry: proxy.NewRegistry(),
	}
	sourceDir := app.scriptCrawlerDriveDir("crawler-main")
	app.registry.Set("crawler-main", scriptcrawler.New(scriptcrawler.Config{
		ID:      "crawler-main",
		RootDir: sourceDir,
	}))
	sourceVideo := filepath.Join(sourceDir, "videos", "source.mp4")
	sourceThumb := filepath.Join(sourceDir, "thumbs", "source.jpg")
	previewPath := filepath.Join(localDir, "scriptcrawler-crawler-main-source.mp4")
	commonThumb := filepath.Join(localDir, "thumbs", "scriptcrawler-crawler-main-source.jpg")
	for _, path := range []string{sourceVideo, sourceThumb, previewPath, commonThumb} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("file"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	now := time.Now()
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:            "scriptcrawler-crawler-main-source",
		DriveID:       "crawler-main",
		FileID:        "source.mp4",
		FileName:      "source.mp4",
		Ext:           "mp4",
		Title:         "Crawler Source",
		PreviewLocal:  previewPath,
		PreviewStatus: "ready",
		ThumbnailURL:  "/p/thumb/scriptcrawler-crawler-main-source",
		Size:          456,
		PublishedAt:   now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	result, err := app.deleteVideo(ctx, "scriptcrawler-crawler-main-source", true)
	if err != nil {
		t.Fatalf("delete crawler video: %v", err)
	}
	if !result.OK || !result.DeletedSource {
		t.Fatalf("delete result = %#v, want source deleted", result)
	}
	for _, path := range []string{sourceVideo, sourceThumb, previewPath, commonThumb} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("deleted file %s still exists, stat err=%v", path, err)
		}
	}
	if _, err := cat.GetVideo(ctx, "scriptcrawler-crawler-main-source"); err != sql.ErrNoRows {
		t.Fatalf("deleted video lookup error = %v, want sql.ErrNoRows", err)
	}
	deleted, err := cat.IsVideoDeleted(ctx, "scriptcrawler-crawler-main-source")
	if err != nil {
		t.Fatalf("check tombstone: %v", err)
	}
	if !deleted {
		t.Fatal("deleted crawler video tombstone missing")
	}
}

func TestCleanupDriveVideosForDeleteScriptCrawlerRemovesOnlyLocalRows(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	localDir := filepath.Join(root, "previews")
	driveID := "crawler-main"
	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	if err := cat.UpsertDrive(ctx, &catalog.Drive{
		ID:            driveID,
		Kind:          scriptcrawler.Kind,
		Name:          "Crawler",
		RootID:        "/",
		TeaserEnabled: true,
	}); err != nil {
		t.Fatalf("seed crawler drive: %v", err)
	}

	localPreview := filepath.Join(localDir, "scriptcrawler-crawler-main-source.mp4")
	localThumb := filepath.Join(localDir, "thumbs", "scriptcrawler-crawler-main-source.jpg")
	migratedPreview := filepath.Join(localDir, "scriptcrawler-crawler-main-migrated.mp4")
	migratedThumb := filepath.Join(localDir, "thumbs", "scriptcrawler-crawler-main-migrated.jpg")
	for _, path := range []string{localPreview, localThumb, migratedPreview, migratedThumb} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("asset"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	now := time.Now()
	for _, v := range []*catalog.Video{
		{
			ID:            "scriptcrawler-crawler-main-source",
			DriveID:       driveID,
			FileID:        "source.mp4",
			Title:         "Source",
			PreviewLocal:  localPreview,
			PreviewStatus: "ready",
			ThumbnailURL:  "/p/thumb/scriptcrawler-crawler-main-source",
		},
		{
			ID:            "scriptcrawler-crawler-main-migrated",
			DriveID:       "PikPak",
			FileID:        "pikpak-file-id",
			Title:         "Migrated",
			PreviewLocal:  migratedPreview,
			PreviewStatus: "ready",
			ThumbnailURL:  "/p/thumb/scriptcrawler-crawler-main-migrated",
		},
		{
			ID:            "pikpak-PikPak-other",
			DriveID:       "PikPak",
			FileID:        "other",
			Title:         "Other",
			PreviewStatus: "ready",
		},
	} {
		v.PublishedAt = now
		v.CreatedAt = now
		v.UpdatedAt = now
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed video %s: %v", v.ID, err)
		}
	}

	app := &App{
		cfg:                &config.Config{Storage: config.Storage{LocalPreviewDir: localDir}},
		cat:                cat,
		registry:           proxy.NewRegistry(),
		workers:            make(map[string]*preview.Worker),
		thumbWorkers:       make(map[string]*preview.ThumbWorker),
		fingerprintWorkers: make(map[string]*fingerprint.Worker),
	}
	removed, err := app.cleanupDriveVideosForDelete(ctx, driveID)
	if err != nil {
		t.Fatalf("cleanup crawler videos: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := cat.GetVideo(ctx, "scriptcrawler-crawler-main-source"); err != sql.ErrNoRows {
		t.Fatalf("local crawler video lookup error = %v, want sql.ErrNoRows", err)
	}
	if _, err := cat.GetVideo(ctx, "scriptcrawler-crawler-main-migrated"); err != nil {
		t.Fatalf("migrated crawler video missing: %v", err)
	}
	if _, err := cat.GetVideo(ctx, "pikpak-PikPak-other"); err != nil {
		t.Fatalf("unrelated pikpak video missing: %v", err)
	}
	for _, path := range []string{localPreview, localThumb} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists, stat err=%v", path, err)
		}
	}
	for _, path := range []string{migratedPreview, migratedThumb} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s missing, stat err=%v", path, err)
		}
	}
}

func TestCleanupOrphanDriveVideosRemovesRowsAndGeneratedAssets(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	localDir := filepath.Join(root, "previews")
	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	if err := cat.UpsertDrive(ctx, &catalog.Drive{
		ID:            "active-drive",
		Kind:          "pikpak",
		Name:          "Active",
		RootID:        "root",
		TeaserEnabled: true,
	}); err != nil {
		t.Fatalf("seed active drive: %v", err)
	}

	previewPath := filepath.Join(localDir, "p123-123-orphan.mp4")
	thumbPath := filepath.Join(localDir, "thumbs", "p123-123-orphan.jpg")
	for _, path := range []string{previewPath, thumbPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("generated"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	now := time.Now()
	for _, v := range []*catalog.Video{
		{
			ID:            "p123-123-orphan",
			DriveID:       "123",
			FileID:        "orphan-file",
			Title:         "Orphan",
			PreviewLocal:  previewPath,
			PreviewStatus: "ready",
			ThumbnailURL:  "/p/thumb/p123-123-orphan",
			PublishedAt:   now,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		{
			ID:          "pikpak-active",
			DriveID:     "active-drive",
			FileID:      "active-file",
			Title:       "Active",
			PublishedAt: now,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	} {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed video %s: %v", v.ID, err)
		}
	}

	app := &App{
		cfg: &config.Config{Storage: config.Storage{LocalPreviewDir: localDir}},
		cat: cat,
	}
	removed, err := app.cleanupOrphanDriveVideos(ctx)
	if err != nil {
		t.Fatalf("cleanup orphan videos: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := cat.GetVideo(ctx, "p123-123-orphan"); err != sql.ErrNoRows {
		t.Fatalf("orphan video lookup error = %v, want sql.ErrNoRows", err)
	}
	if _, err := cat.GetVideo(ctx, "pikpak-active"); err != nil {
		t.Fatalf("active video missing: %v", err)
	}
	for _, path := range []string{previewPath, thumbPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("orphan asset %s still exists, stat err=%v", path, err)
		}
	}
}

func TestCleanupDuplicateVideoAssetsDeletesExactDuplicateRows(t *testing.T) {
	ctx := context.Background()
	localDir := t.TempDir()
	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	canonicalPreview := filepath.Join(localDir, "canonical.mp4")
	duplicatePreview := filepath.Join(localDir, "duplicate.mp4")
	canonicalThumb := filepath.Join(localDir, "thumbs", "canonical-video.jpg")
	duplicateThumb := filepath.Join(localDir, "thumbs", "duplicate-video.jpg")
	for _, path := range []string{canonicalPreview, duplicatePreview, canonicalThumb, duplicateThumb} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("asset"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	for _, v := range []*catalog.Video{
		{
			ID:            "canonical-video",
			DriveID:       "115",
			FileID:        "file-a",
			Title:         "Canonical",
			Size:          2048,
			ThumbnailURL:  "/p/thumb/canonical-video",
			PreviewLocal:  canonicalPreview,
			PreviewStatus: "ready",
			PublishedAt:   now,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		{
			ID:            "duplicate-video",
			DriveID:       "onedrive",
			FileID:        "file-b",
			Title:         "Duplicate",
			Size:          2048,
			ThumbnailURL:  "/p/thumb/duplicate-video",
			PreviewLocal:  duplicatePreview,
			PreviewStatus: "ready",
			PublishedAt:   now.Add(time.Second),
			CreatedAt:     now.Add(time.Second),
			UpdatedAt:     now.Add(time.Second),
		},
	} {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed %s: %v", v.ID, err)
		}
		if err := cat.UpdateVideoFingerprint(ctx, v.ID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "ready", ""); err != nil {
			t.Fatalf("fingerprint %s: %v", v.ID, err)
		}
	}

	app := &App{
		cfg: &config.Config{Storage: config.Storage{LocalPreviewDir: localDir}},
		cat: cat,
	}
	if err := app.cleanupDuplicateVideoAssets(ctx); err != nil {
		t.Fatalf("cleanup duplicate video assets: %v", err)
	}

	for _, path := range []string{canonicalPreview, canonicalThumb} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("canonical asset %s missing: %v", path, err)
		}
	}
	for _, path := range []string{duplicatePreview, duplicateThumb} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("duplicate asset %s still exists, stat err=%v", path, err)
		}
	}
	if _, err := cat.GetVideo(ctx, "duplicate-video"); err != sql.ErrNoRows {
		t.Fatalf("duplicate lookup error = %v, want sql.ErrNoRows", err)
	}
	deleted, err := cat.IsVideoDeleted(ctx, "duplicate-video")
	if err != nil {
		t.Fatalf("check duplicate tombstone: %v", err)
	}
	if !deleted {
		t.Fatalf("duplicate tombstone missing")
	}
	deletedItems, _, err := cat.ListDeletedVideos(ctx, catalog.ListParams{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("list deleted videos: %v", err)
	}
	if len(deletedItems) != 1 || deletedItems[0].ID != "duplicate-video" || deletedItems[0].Reason != catalog.DeletedVideoReasonDuplicate {
		t.Fatalf("duplicate tombstone = %#v, want reason %q", deletedItems, catalog.DeletedVideoReasonDuplicate)
	}
	canon, err := cat.GetVideo(ctx, "canonical-video")
	if err != nil {
		t.Fatalf("get canonical: %v", err)
	}
	if canon.PreviewLocal != canonicalPreview || canon.ThumbnailURL != "/p/thumb/canonical-video" {
		t.Fatalf("canonical changed: preview=%q thumb=%q", canon.PreviewLocal, canon.ThumbnailURL)
	}
}

func TestCleanupDuplicateVideoAssetsDeletesNearDuplicateRowsKeepingLargest(t *testing.T) {
	ctx := context.Background()
	localDir := t.TempDir()
	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	smallPreview := filepath.Join(localDir, "small-video.mp4")
	largePreview := filepath.Join(localDir, "large-video.mp4")
	smallThumb := filepath.Join(localDir, "thumbs", "small-video.jpg")
	largeThumb := filepath.Join(localDir, "thumbs", "large-video.jpg")
	for _, path := range []string{smallPreview, largePreview} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("preview"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	writeSolidJPEG(t, smallThumb, color.RGBA{R: 180, G: 80, B: 40, A: 255})
	writeSolidJPEG(t, largeThumb, color.RGBA{R: 180, G: 80, B: 40, A: 255})

	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	for _, v := range []*catalog.Video{
		{
			ID:              "small-video",
			DriveID:         "scriptcrawler-a",
			FileID:          "file-small",
			FileName:        "small.mp4",
			Title:           "反差极品大二女友，叫声可射～，“射进小骚逼里面～” - 91porn",
			DurationSeconds: 313,
			Size:            1024,
			ThumbnailURL:    "/p/thumb/small-video",
			PreviewLocal:    smallPreview,
			PreviewStatus:   "ready",
			PublishedAt:     now,
			CreatedAt:       now,
			UpdatedAt:       now,
		},
		{
			ID:              "large-video",
			DriveID:         "scriptcrawler-b",
			FileID:          "file-large",
			FileName:        "large.mp4",
			Title:           "反差极品大二女友，叫声可射～，“射进小骚逼里面～”_91pinse",
			DurationSeconds: 313,
			Size:            4096,
			ThumbnailURL:    "/p/thumb/large-video",
			PreviewLocal:    largePreview,
			PreviewStatus:   "ready",
			PublishedAt:     now.Add(time.Second),
			CreatedAt:       now.Add(time.Second),
			UpdatedAt:       now.Add(time.Second),
		},
	} {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed %s: %v", v.ID, err)
		}
	}

	app := &App{
		cfg: &config.Config{Storage: config.Storage{LocalPreviewDir: localDir}},
		cat: cat,
	}
	if err := app.cleanupDuplicateVideoAssets(ctx); err != nil {
		t.Fatalf("cleanup duplicate video assets: %v", err)
	}

	if _, err := cat.GetVideo(ctx, "small-video"); err != sql.ErrNoRows {
		t.Fatalf("small duplicate lookup error = %v, want sql.ErrNoRows", err)
	}
	deleted, err := cat.IsVideoDeleted(ctx, "small-video")
	if err != nil {
		t.Fatalf("check small tombstone: %v", err)
	}
	if !deleted {
		t.Fatalf("small duplicate tombstone missing")
	}
	deletedItems, _, err := cat.ListDeletedVideos(ctx, catalog.ListParams{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("list deleted videos: %v", err)
	}
	if len(deletedItems) != 1 || deletedItems[0].ID != "small-video" || deletedItems[0].Reason != catalog.DeletedVideoReasonDuplicate {
		t.Fatalf("small duplicate tombstone = %#v, want reason %q", deletedItems, catalog.DeletedVideoReasonDuplicate)
	}
	large, err := cat.GetVideo(ctx, "large-video")
	if err != nil {
		t.Fatalf("large canonical missing: %v", err)
	}
	if large.Size != 4096 {
		t.Fatalf("large canonical size = %d, want 4096", large.Size)
	}
	for _, path := range []string{smallPreview, smallThumb} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("small duplicate asset %s still exists, stat err=%v", path, err)
		}
	}
	for _, path := range []string{largePreview, largeThumb} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("large canonical asset %s missing: %v", path, err)
		}
	}
}

func writeSolidJPEG(t *testing.T, path string, c color.RGBA) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
}

type serverFakeTeaserGenerator struct {
	mu     sync.Mutex
	events []string
}

func (g *serverFakeTeaserGenerator) record(event string) {
	g.mu.Lock()
	g.events = append(g.events, event)
	g.mu.Unlock()
}

func (g *serverFakeTeaserGenerator) Events() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.events...)
}

func (g *serverFakeTeaserGenerator) Probe(context.Context, *drives.StreamLink) (float64, error) {
	return 30, nil
}

func (g *serverFakeTeaserGenerator) Generate(context.Context, *drives.StreamLink, float64) (string, error) {
	g.record("preview")
	return "/tmp/source-teaser.mp4", nil
}

func (g *serverFakeTeaserGenerator) MoveToLocal(_ string, videoID string) (string, error) {
	g.mu.Lock()
	if len(g.events) > 0 && g.events[len(g.events)-1] == "preview" {
		g.events[len(g.events)-1] = "preview:" + videoID
	}
	g.mu.Unlock()
	return "/tmp/" + videoID + ".mp4", nil
}

func (g *serverFakeTeaserGenerator) GenerateThumbnail(_ context.Context, _ *drives.StreamLink, videoID string, _ float64) (string, error) {
	g.record("thumb:" + videoID)
	return "/tmp/" + videoID + ".jpg", nil
}

type serverBlockingThumbGenerator struct {
	serverFakeTeaserGenerator
	started chan string
	release chan struct{}
}

func (g *serverBlockingThumbGenerator) GenerateThumbnail(ctx context.Context, _ *drives.StreamLink, videoID string, _ float64) (string, error) {
	g.record("thumb:" + videoID)
	if g.started != nil {
		select {
		case g.started <- videoID:
		default:
		}
	}
	select {
	case <-g.release:
		return "/tmp/" + videoID + ".jpg", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type serverFakeDrive struct{}

func (d *serverFakeDrive) Kind() string { return "fake" }
func (d *serverFakeDrive) ID() string   { return "drive-id" }
func (d *serverFakeDrive) Init(context.Context) error {
	return nil
}
func (d *serverFakeDrive) List(context.Context, string) ([]drives.Entry, error) {
	return nil, nil
}
func (d *serverFakeDrive) Stat(context.Context, string) (*drives.Entry, error) {
	return nil, drives.ErrNotSupported
}
func (d *serverFakeDrive) StreamURL(context.Context, string) (*drives.StreamLink, error) {
	return &drives.StreamLink{URL: "https://video.example/clip.mp4"}, nil
}
func (d *serverFakeDrive) Upload(context.Context, string, string, io.Reader, int64) (string, error) {
	return "", drives.ErrNotSupported
}
func (d *serverFakeDrive) EnsureDir(context.Context, string) (string, error) {
	return "", drives.ErrNotSupported
}
func (d *serverFakeDrive) RootID() string { return "root" }

type serverFakeKindDrive struct {
	serverFakeDrive
	id   string
	kind string
}

func (d *serverFakeKindDrive) Kind() string { return d.kind }
func (d *serverFakeKindDrive) ID() string   { return d.id }

type serverRemovableFakeDrive struct {
	serverFakeDrive
	id            string
	removedFileID string
}

func (d *serverRemovableFakeDrive) Kind() string { return "fake-removable" }
func (d *serverRemovableFakeDrive) ID() string   { return d.id }
func (d *serverRemovableFakeDrive) Remove(ctx context.Context, fileID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.removedFileID = fileID
	return nil
}

type serverSourceRemovableFakeDrive struct {
	serverFakeDrive
	id                   string
	removedSource        drives.SourceFile
	fallbackRemoveCalled bool
}

func (d *serverSourceRemovableFakeDrive) Kind() string { return "fake-source-removable" }
func (d *serverSourceRemovableFakeDrive) ID() string   { return d.id }
func (d *serverSourceRemovableFakeDrive) RemoveSource(ctx context.Context, source drives.SourceFile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.removedSource = source
	return nil
}
func (d *serverSourceRemovableFakeDrive) Remove(ctx context.Context, fileID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.fallbackRemoveCalled = true
	return nil
}

type serverFakeCrawlerUploadRunner struct {
	called int
}

func (r *serverFakeCrawlerUploadRunner) RunOnce(context.Context) error {
	r.called++
	return nil
}

type serverBlockingListDrive struct {
	id      string
	started chan string
	release chan struct{}
}

func (d *serverBlockingListDrive) Kind() string { return "fake" }
func (d *serverBlockingListDrive) ID() string   { return d.id }
func (d *serverBlockingListDrive) Init(context.Context) error {
	return nil
}
func (d *serverBlockingListDrive) List(ctx context.Context, _ string) ([]drives.Entry, error) {
	if d.started != nil {
		select {
		case d.started <- d.id:
		default:
		}
	}
	select {
	case <-d.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (d *serverBlockingListDrive) Stat(context.Context, string) (*drives.Entry, error) {
	return nil, drives.ErrNotSupported
}
func (d *serverBlockingListDrive) StreamURL(context.Context, string) (*drives.StreamLink, error) {
	return &drives.StreamLink{URL: "https://video.example/clip.mp4"}, nil
}
func (d *serverBlockingListDrive) Upload(context.Context, string, string, io.Reader, int64) (string, error) {
	return "", drives.ErrNotSupported
}
func (d *serverBlockingListDrive) EnsureDir(context.Context, string) (string, error) {
	return "", drives.ErrNotSupported
}
func (d *serverBlockingListDrive) RootID() string { return "root" }

type serverFingerprintFakeDrive struct {
	serverFakeDrive
	path string
}

func (d *serverFingerprintFakeDrive) StreamURL(context.Context, string) (*drives.StreamLink, error) {
	return &drives.StreamLink{URL: d.path}, nil
}

type serverScanFingerprintFakeDrive struct {
	serverFingerprintFakeDrive
	entries []drives.Entry
}

func (d *serverScanFingerprintFakeDrive) List(context.Context, string) ([]drives.Entry, error) {
	return d.entries, nil
}

type serverLocalUploadFakeDrive struct {
	serverFakeDrive
}

func (d *serverLocalUploadFakeDrive) ID() string { return "local-upload" }

// seedDriveWithTeaser 在 catalog 里 upsert 一个测试用的 drive 行，把 TeaserEnabled
// 设为 enabled。teaser 入队判断现在按 per-drive 而不是全局 setting，所以涉及到
// teaser worker 的测试都要先把 drive 行写进 catalog。
func seedDriveWithTeaser(t *testing.T, cat *catalog.Catalog, driveID string, enabled bool) {
	t.Helper()
	if err := cat.UpsertDrive(context.Background(), &catalog.Drive{
		ID:            driveID,
		Kind:          "fake",
		Name:          driveID,
		RootID:        "0",
		TeaserEnabled: enabled,
	}); err != nil {
		t.Fatalf("seed drive: %v", err)
	}
}
