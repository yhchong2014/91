// Package nightly orchestrates the single nightly maintenance pipeline that
// replaces the legacy scanLoop / crawlerLoop / crawler upload periodic loop.
//
// Pipeline (fired once per day at cron_hour, also via TriggerNow for admin
// "扫描所有网盘"):
//
//	Phase 1: for each non-crawler cloud drive
//	           scan + delete-detection + enqueue thumb + enqueue preview video
//	         wait until all thumb / preview-video queues are idle
//	Phase 2: if any script crawler configured
//	           crawl + enqueue preview video for new videos
//	         wait until preview-video queues are idle
//	Phase 3: crawler local video → cloud upload (single sweep, captcha cooldown still
//	         honored within this call)
//	Phase 4: full-library duplicate video maintenance:
//	         exact size+sampled_sha256 dedupe, then title/duration/thumbnail dedupe
//
// The pipeline runs until all phases finish, the process exits, or an admin
// stop request cancels the run. Provider cooldowns may make a single phase take
// a long time, so there is no fixed duration cutoff.
//
// State persistence: the date string of the most recent successfully started
// run is stored in catalog.settings under the key "nightly.last_run_date".
// This survives restarts so a quick crash inside cron_hour won't trigger a
// duplicate pipeline.
package nightly

import (
	"context"
	"log"
	"sync"
	"time"
)

const (
	// settingLastRunDate stores the YYYY-MM-DD of the last natural cron-triggered
	// pipeline run. Manual TriggerNow() also updates this to keep behavior consistent.
	settingLastRunDate = "nightly.last_run_date"
	// dateLayout matches catalog.GetSetting string semantics; using ISO-8601 date.
	dateLayout = "2006-01-02"
	// pollInterval is the heartbeat for the natural cron decision loop.
	pollInterval = time.Minute
)

// SettingStore is the minimal catalog.Catalog surface we rely on.
type SettingStore interface {
	GetSetting(ctx context.Context, key, defaultValue string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Config wires the runner to its dependencies. The function-callback shape
// avoids importing main / drives / preview from this package, keeping the
// dependency graph clean.
type Config struct {
	Settings SettingStore
	CronHour int // default 1 (01:00)

	// ListScanTargets returns the drive IDs to run Phase 1 on, in deterministic
	// order. Should exclude crawler and localupload drives.
	ListScanTargets func(ctx context.Context) []string

	// RunScan synchronously runs scan + cleanup + enqueueDriveGeneration for
	// one drive. Errors are expected to be logged inside, not surfaced.
	RunScan func(ctx context.Context, driveID string)

	// ListCrawlerDrives returns script crawler drive IDs to crawl in Phase 2.
	// Returns empty slice when no crawler is configured.
	ListCrawlerDrives func(ctx context.Context) []string

	// RunCrawlerCrawl synchronously runs one crawl cycle (downloads + thumbs +
	// preview-video enqueue) for a single crawler drive.
	RunCrawlerCrawl func(ctx context.Context, driveID string)

	// WaitPreviewQueuesIdle blocks until both the thumbnail and preview-video queues
	// across all drives are drained (queue empty + no in-flight task). It must
	// honor ctx cancellation.
	WaitPreviewQueuesIdle func(ctx context.Context) error

	// RunMigration runs crawlerupload.Migrator.RunOnce for Phase 3.
	RunMigration func(ctx context.Context) error

	// RunDedupeAssetCleanup runs full-library duplicate video maintenance. It
	// removes duplicate catalog rows and local generated assets, but never
	// deletes cloud source files.
	RunDedupeAssetCleanup func(ctx context.Context) error

	// RunTagMaintenance is optional. The main server leaves this nil because tag
	// matching is event-driven: new videos and administrator tag edits refresh
	// labels immediately.
	RunTagMaintenance func(ctx context.Context) error

	// Now is injected for tests; nil → time.Now.
	Now func() time.Time
}

type Status struct {
	State          string
	Running        bool
	Queued         bool
	StartedAt      time.Time
	LastFinishedAt time.Time
}

// Runner drives the nightly pipeline.
type Runner struct {
	cfg     Config
	trigger chan struct{} // buffered(1); manual "run now"
	runMu   sync.Mutex    // prevents overlapping pipeline runs

	stateMu        sync.Mutex
	running        bool
	queued         bool
	startedAt      time.Time
	lastFinishedAt time.Time
	currentCancel  context.CancelFunc
}

// New constructs a Runner. cfg is shallow-copied; defaults are applied.
func New(cfg Config) *Runner {
	if cfg.CronHour <= 0 || cfg.CronHour > 23 {
		cfg.CronHour = 1
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Runner{
		cfg:     cfg,
		trigger: make(chan struct{}, 1),
	}
}

// Run is a blocking loop until ctx is done. It wakes up once per minute and
// either fires the natural cron-driven pipeline (when cron_hour matches and
// today hasn't run) or honors a manual TriggerNow() request.
func (r *Runner) Run(ctx context.Context) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	log.Printf("[nightly] runner started; cron_hour=%d", r.cfg.CronHour)
	for {
		select {
		case <-ctx.Done():
			log.Printf("[nightly] runner stopping: %v", ctx.Err())
			return
		case <-t.C:
			r.tryNaturalRun(ctx)
		case <-r.trigger:
			log.Printf("[nightly] manual trigger received")
			r.runPipelineLocked(ctx, true)
		}
	}
}

// TriggerNow asks the running loop to fire a pipeline ASAP. Only one manual
// trigger can be active at a time: if a pipeline is already running or waiting
// in the trigger channel, the request is ignored and returns false.
func (r *Runner) TriggerNow() bool {
	r.stateMu.Lock()
	if r.running || r.queued {
		r.stateMu.Unlock()
		return false
	}
	r.queued = true
	r.stateMu.Unlock()

	select {
	case r.trigger <- struct{}{}:
		return true
	default:
		r.stateMu.Lock()
		r.queued = false
		r.stateMu.Unlock()
		return false
	}
}

// StopCurrent cancels the currently running pipeline and drops one queued
// manual trigger, if present. It returns true when there was something to stop.
func (r *Runner) StopCurrent() bool {
	r.stateMu.Lock()
	wasRunning := r.running
	wasQueued := r.queued
	cancel := r.currentCancel
	r.queued = false
	r.stateMu.Unlock()

	if wasQueued {
		select {
		case <-r.trigger:
		default:
		}
	}
	if cancel != nil {
		cancel()
	}
	return wasRunning || wasQueued || cancel != nil
}

func (r *Runner) Status() Status {
	r.stateMu.Lock()
	running := r.running
	queued := r.queued
	startedAt := r.startedAt
	lastFinishedAt := r.lastFinishedAt
	r.stateMu.Unlock()

	state := "idle"
	switch {
	case running && queued:
		state = "running_queued"
	case running:
		state = "running"
	case queued:
		state = "queued"
	}

	return Status{
		State:          state,
		Running:        running,
		Queued:         queued,
		StartedAt:      startedAt,
		LastFinishedAt: lastFinishedAt,
	}
}

// tryNaturalRun checks the cron decision and runs the pipeline if due today.
func (r *Runner) tryNaturalRun(ctx context.Context) {
	now := r.cfg.Now()
	if now.Hour() != r.cfg.CronHour {
		return
	}
	last, err := r.readLastRunDate(ctx)
	if err != nil {
		log.Printf("[nightly] read last_run_date: %v", err)
		return
	}
	if !shouldRun(now, last) {
		return
	}
	log.Printf("[nightly] natural cron trigger at %s", now.Format(time.RFC3339))
	r.runPipelineLocked(ctx, false)
}

// shouldRun returns true when "today" (per now) hasn't already been processed.
func shouldRun(now time.Time, lastRunDate string) bool {
	return lastRunDate != now.Format(dateLayout)
}

// runPipelineLocked guards against overlapping runs. If another pipeline is
// in progress, the call returns immediately (logged once). After completion
// (regardless of success), today's date is recorded so subsequent triggers
// the same calendar day are skipped.
//
// 流水线没有总耗时上限：一直跑到 ctx 取消（进程退出）或所有 phase 完成。
func (r *Runner) runPipelineLocked(ctx context.Context, manual bool) {
	if manual {
		r.stateMu.Lock()
		queued := r.queued
		r.stateMu.Unlock()
		if !queued {
			log.Printf("[nightly] manual trigger was canceled before start")
			return
		}
	}
	if !r.runMu.TryLock() {
		log.Printf("[nightly] another pipeline is already running, skipping this trigger")
		return
	}

	started := r.cfg.Now()
	runCtx, cancel := context.WithCancel(ctx)
	r.markStarted(started, cancel)
	defer func() {
		cancel()
		r.markFinished(r.cfg.Now())
		r.runMu.Unlock()
	}()

	mode := "scheduled"
	if manual {
		mode = "manual"
	}
	log.Printf("[nightly] pipeline (%s) start", mode)

	r.runPipeline(runCtx)

	finished := r.cfg.Now()
	log.Printf("[nightly] pipeline (%s) finish; took=%s", mode, finished.Sub(started).Round(time.Second))

	// Mark today as processed regardless of success/error. This is intentional:
	// a partial / failing pipeline shouldn't trigger again the same day, the
	// admin can inspect logs and click "扫描所有网盘" to retry explicitly.
	dateStr := started.Format(dateLayout)
	if err := r.cfg.Settings.SetSetting(ctx, settingLastRunDate, dateStr); err != nil {
		log.Printf("[nightly] persist last_run_date: %v", err)
	}
}

func (r *Runner) markStarted(started time.Time, cancel context.CancelFunc) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.running = true
	r.queued = false
	r.startedAt = started
	r.currentCancel = cancel
}

func (r *Runner) markFinished(finished time.Time) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.running = false
	r.startedAt = time.Time{}
	r.lastFinishedAt = finished
	r.currentCancel = nil
}

// runPipeline executes the maintenance phases. It returns when the pipeline
// finishes or ctx is done. Errors are logged but not propagated —
// each phase is best-effort; downstream phases still attempt to run unless ctx
// is dead.
func (r *Runner) runPipeline(ctx context.Context) {
	// ---------- Phase 1 ----------
	if r.shouldStop(ctx, "phase 1") {
		return
	}
	scanIDs := []string{}
	if r.cfg.ListScanTargets != nil {
		scanIDs = r.cfg.ListScanTargets(ctx)
	}
	if len(scanIDs) == 0 {
		log.Printf("[nightly] phase 1 skipped: no cloud drives to scan")
	} else {
		log.Printf("[nightly] phase 1: scanning %d drive(s)", len(scanIDs))
		for _, id := range scanIDs {
			if ctx.Err() != nil {
				log.Printf("[nightly] phase 1 aborted by ctx: %v", ctx.Err())
				return
			}
			log.Printf("[nightly] phase 1: scanning drive=%s", id)
			r.cfg.RunScan(ctx, id)
		}
		log.Printf("[nightly] phase 1: waiting for preview queues to drain")
		if err := r.waitIdle(ctx, "phase 1"); err != nil {
			return
		}
	}

	// ---------- Phase 2 ----------
	if r.shouldStop(ctx, "phase 2") {
		return
	}
	crawlerIDs := []string{}
	if r.cfg.ListCrawlerDrives != nil {
		crawlerIDs = r.cfg.ListCrawlerDrives(ctx)
	}
	if len(crawlerIDs) == 0 {
		log.Printf("[nightly] phase 2/3 skipped: no crawler configured")
		r.runDedupeAssetCleanupPhase(ctx)
		r.runTagMaintenancePhase(ctx)
		return
	}
	log.Printf("[nightly] phase 2: crawling %d crawler drive(s)", len(crawlerIDs))
	for _, id := range crawlerIDs {
		if ctx.Err() != nil {
			log.Printf("[nightly] phase 2 aborted by ctx: %v", ctx.Err())
			return
		}
		log.Printf("[nightly] phase 2: crawling drive=%s", id)
		r.cfg.RunCrawlerCrawl(ctx, id)
	}
	log.Printf("[nightly] phase 2: waiting for teaser queue to drain")
	if err := r.waitIdle(ctx, "phase 2"); err != nil {
		return
	}

	// ---------- Phase 3 ----------
	if r.shouldStop(ctx, "phase 3") {
		return
	}
	log.Printf("[nightly] phase 3: crawler upload")
	if r.cfg.RunMigration != nil {
		if err := r.cfg.RunMigration(ctx); err != nil {
			log.Printf("[nightly] phase 3 migration: %v", err)
		}
	}

	r.runDedupeAssetCleanupPhase(ctx)
	r.runTagMaintenancePhase(ctx)
}

func (r *Runner) shouldStop(ctx context.Context, phase string) bool {
	if err := ctx.Err(); err != nil {
		log.Printf("[nightly] %s: ctx done (%v), bailing out", phase, err)
		return true
	}
	return false
}

// waitIdle calls the configured WaitPreviewQueuesIdle, logging the outcome.
func (r *Runner) waitIdle(ctx context.Context, phase string) error {
	if r.cfg.WaitPreviewQueuesIdle == nil {
		return nil
	}
	if err := r.cfg.WaitPreviewQueuesIdle(ctx); err != nil {
		log.Printf("[nightly] %s: wait preview queues: %v", phase, err)
		return err
	}
	return nil
}

func (r *Runner) runTagMaintenancePhase(ctx context.Context) {
	if r.cfg.RunTagMaintenance == nil {
		return
	}
	if r.shouldStop(ctx, "phase 5") {
		return
	}
	log.Printf("[nightly] phase 5: tag maintenance")
	if err := r.cfg.RunTagMaintenance(ctx); err != nil {
		log.Printf("[nightly] phase 5 tag maintenance: %v", err)
	}
}

func (r *Runner) runDedupeAssetCleanupPhase(ctx context.Context) {
	if r.shouldStop(ctx, "phase 4") {
		return
	}
	if r.cfg.RunDedupeAssetCleanup == nil {
		return
	}
	log.Printf("[nightly] phase 4: duplicate video maintenance")
	if err := r.cfg.RunDedupeAssetCleanup(ctx); err != nil {
		log.Printf("[nightly] phase 4 duplicate video maintenance: %v", err)
	}
}

// readLastRunDate reads the persisted last_run_date or returns "" when unset.
func (r *Runner) readLastRunDate(ctx context.Context) (string, error) {
	if r.cfg.Settings == nil {
		return "", nil
	}
	return r.cfg.Settings.GetSetting(ctx, settingLastRunDate, "")
}
