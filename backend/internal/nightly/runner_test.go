package nightly

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubSettings is an in-memory SettingStore for tests.
type stubSettings struct {
	mu sync.Mutex
	kv map[string]string
}

func newStubSettings() *stubSettings { return &stubSettings{kv: make(map[string]string)} }

func (s *stubSettings) GetSetting(_ context.Context, key, def string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.kv[key]; ok {
		return v, nil
	}
	return def, nil
}

func (s *stubSettings) SetSetting(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kv[key] = value
	return nil
}

func TestShouldRunChecksDate(t *testing.T) {
	now := time.Date(2026, 5, 27, 1, 30, 0, 0, time.UTC)
	if !shouldRun(now, "") {
		t.Fatal("first ever run with empty last_run_date should be due")
	}
	if !shouldRun(now, "2026-05-26") {
		t.Fatal("yesterday's run should not block today")
	}
	if shouldRun(now, "2026-05-27") {
		t.Fatal("today's run already recorded should block another natural run")
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	r := New(Config{Settings: newStubSettings()})
	if r.cfg.CronHour != 1 {
		t.Errorf("CronHour zero-value should fall back to 1, got %d", r.cfg.CronHour)
	}
}

func TestNewRejectsInvalidCronHour(t *testing.T) {
	r := New(Config{CronHour: 0, Settings: newStubSettings()})
	if r.cfg.CronHour != 1 {
		t.Fatalf("invalid cron_hour fall back to 1, got %d", r.cfg.CronHour)
	}
	r2 := New(Config{CronHour: -1, Settings: newStubSettings()})
	if r2.cfg.CronHour != 1 {
		t.Fatalf("out-of-range cron_hour fall back to 1, got %d", r2.cfg.CronHour)
	}
	r3 := New(Config{CronHour: 25, Settings: newStubSettings()})
	if r3.cfg.CronHour != 1 {
		t.Fatalf("out-of-range cron_hour fall back to 1, got %d", r3.cfg.CronHour)
	}
}

// recorder accumulates the order of phase invocations so tests can assert
// orchestration semantics.
type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) push(s string) {
	r.mu.Lock()
	r.calls = append(r.calls, s)
	r.mu.Unlock()
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

func TestRunPipelineHonoursPhaseOrder(t *testing.T) {
	rec := &recorder{}
	settings := newStubSettings()

	r := New(Config{
		Settings: settings,
		ListScanTargets: func(context.Context) []string {
			rec.push("list-scan")
			return []string{"drive-a", "drive-b"}
		},
		RunScan: func(_ context.Context, id string) {
			rec.push("scan:" + id)
		},
		ListCrawlerDrives: func(context.Context) []string {
			rec.push("list-crawler")
			return []string{"sp-1"}
		},
		RunCrawlerCrawl: func(_ context.Context, id string) {
			rec.push("crawl:" + id)
		},
		WaitPreviewQueuesIdle: func(context.Context) error {
			rec.push("wait-idle")
			return nil
		},
		RunMigration: func(context.Context) error {
			rec.push("migrate")
			return nil
		},
		RunDedupeAssetCleanup: func(context.Context) error {
			rec.push("dedupe-cleanup")
			return nil
		},
		RunTagMaintenance: func(context.Context) error {
			rec.push("tag-maintenance")
			return nil
		},
	})

	r.runPipeline(context.Background())

	got := rec.snapshot()
	want := []string{
		"list-scan",
		"scan:drive-a",
		"scan:drive-b",
		"wait-idle", // after phase 1
		"list-crawler",
		"crawl:sp-1",
		"wait-idle", // after phase 2
		"migrate",
		"dedupe-cleanup",
		"tag-maintenance",
	}
	if len(got) != len(want) {
		t.Fatalf("call sequence len = %d, want %d; got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
}

func TestRunPipelineSkipsMigrationWhenNoCrawler(t *testing.T) {
	rec := &recorder{}

	r := New(Config{
		Settings:          newStubSettings(),
		ListScanTargets:   func(context.Context) []string { return []string{"drive-a"} },
		RunScan:           func(_ context.Context, id string) { rec.push("scan:" + id) },
		ListCrawlerDrives: func(context.Context) []string { return nil },
		RunCrawlerCrawl:   func(_ context.Context, id string) { rec.push("crawl:" + id) },
		WaitPreviewQueuesIdle: func(context.Context) error {
			rec.push("wait-idle")
			return nil
		},
		RunMigration: func(context.Context) error {
			rec.push("migrate")
			return nil
		},
		RunDedupeAssetCleanup: func(context.Context) error {
			rec.push("dedupe-cleanup")
			return nil
		},
		RunTagMaintenance: func(context.Context) error {
			rec.push("tag-maintenance")
			return nil
		},
	})

	r.runPipeline(context.Background())

	for _, c := range rec.snapshot() {
		if c == "migrate" || c == "crawl:sp-1" {
			t.Fatalf("phase 2/3 should be skipped when no crawler, got call %q", c)
		}
	}
	foundCleanup := false
	foundTagMaintenance := false
	for _, c := range rec.snapshot() {
		if c == "dedupe-cleanup" {
			foundCleanup = true
		}
		if c == "tag-maintenance" {
			foundTagMaintenance = true
		}
	}
	if !foundCleanup {
		t.Fatalf("dedupe cleanup should still run when crawler is absent; calls=%v", rec.snapshot())
	}
	if !foundTagMaintenance {
		t.Fatalf("tag maintenance should still run when crawler is absent; calls=%v", rec.snapshot())
	}
}

func TestRunPipelineExitsWhenContextCancelledMidPhase(t *testing.T) {
	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())

	r := New(Config{
		Settings: newStubSettings(),
		ListScanTargets: func(context.Context) []string {
			return []string{"drive-a", "drive-b", "drive-c"}
		},
		RunScan: func(_ context.Context, id string) {
			rec.push("scan:" + id)
			if id == "drive-a" {
				cancel()
			}
		},
		ListCrawlerDrives:     func(context.Context) []string { return []string{"x"} },
		RunCrawlerCrawl:       func(context.Context, string) { rec.push("crawl") },
		WaitPreviewQueuesIdle: func(context.Context) error { rec.push("wait-idle"); return nil },
		RunMigration:          func(context.Context) error { rec.push("migrate"); return nil },
		RunDedupeAssetCleanup: func(context.Context) error { rec.push("dedupe-cleanup"); return nil },
		RunTagMaintenance:     func(context.Context) error { rec.push("tag-maintenance"); return nil },
	})

	r.runPipeline(ctx)

	got := rec.snapshot()
	for _, c := range got {
		if c == "scan:drive-c" || c == "scan:drive-b" {
			t.Fatalf("scan should bail out after cancel, got call %q (full=%v)", c, got)
		}
	}
	for _, c := range got {
		if c == "crawl" || c == "migrate" {
			t.Fatalf("subsequent phase should not run after cancel, got call %q", c)
		}
		if c == "dedupe-cleanup" {
			t.Fatalf("dedupe cleanup should not run after cancel, got call %q", c)
		}
		if c == "tag-maintenance" {
			t.Fatalf("tag maintenance should not run after cancel, got call %q", c)
		}
	}
}

func TestRunPipelineRecordsLastRunDateAfterCompletion(t *testing.T) {
	settings := newStubSettings()
	now := time.Date(2026, 5, 27, 1, 5, 0, 0, time.UTC)
	r := New(Config{
		Settings:              settings,
		Now:                   func() time.Time { return now },
		ListScanTargets:       func(context.Context) []string { return nil },
		WaitPreviewQueuesIdle: func(context.Context) error { return nil },
	})

	r.runPipelineLocked(context.Background(), false)

	got, _ := settings.GetSetting(context.Background(), settingLastRunDate, "")
	if got != "2026-05-27" {
		t.Fatalf("last_run_date = %q, want 2026-05-27", got)
	}
}

func TestRunPipelineLockedDropsOverlappingTriggers(t *testing.T) {
	var (
		started      atomic.Int32
		releaseFirst = make(chan struct{})
	)
	r := New(Config{
		Settings: newStubSettings(),
		ListScanTargets: func(context.Context) []string {
			started.Add(1)
			<-releaseFirst
			return nil
		},
		WaitPreviewQueuesIdle: func(context.Context) error { return nil },
	})

	go r.runPipelineLocked(context.Background(), false)

	// Wait for first to start
	for started.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	// Second trigger should bail out without invoking ListScanTargets again
	r.runPipelineLocked(context.Background(), true)
	if started.Load() != 1 {
		t.Fatalf("overlapping run should be dropped; started=%d", started.Load())
	}
	close(releaseFirst)
}

// TestCtxCancelPreventsLaterPhases 校验：ctx 在 phase 边界已取消（进程退出）时，
// 后续 phase 不会启动。"ctx 已 done 就 bail" 仍保留。
func TestCtxCancelPreventsLaterPhases(t *testing.T) {
	rec := &recorder{}
	settings := newStubSettings()

	r := New(Config{
		Settings:        settings,
		ListScanTargets: func(context.Context) []string { return nil },
		WaitPreviewQueuesIdle: func(ctx context.Context) error {
			return ctx.Err()
		},
		ListCrawlerDrives: func(context.Context) []string {
			rec.push("list-crawler")
			return []string{"x"}
		},
		RunCrawlerCrawl: func(context.Context, string) { rec.push("crawl") },
		RunMigration:    func(context.Context) error { rec.push("migrate"); return nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r.runPipeline(ctx)

	for _, c := range rec.snapshot() {
		if c == "crawl" || c == "migrate" || c == "list-crawler" {
			t.Fatalf("later phase should not run after ctx done; got %q", c)
		}
	}
}

func TestTriggerNowIsNonBlocking(t *testing.T) {
	r := New(Config{Settings: newStubSettings()})
	// fill the trigger channel
	if !r.TriggerNow() {
		t.Fatal("first TriggerNow should be accepted")
	}
	// Second call must not block
	done := make(chan struct{})
	var accepted bool
	go func() {
		accepted = r.TriggerNow()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("TriggerNow blocked when channel is full")
	}
	if accepted {
		t.Fatal("second TriggerNow should be ignored when trigger channel is full")
	}
}

func TestStatusTracksQueuedRunningAndFinished(t *testing.T) {
	blockScan := make(chan struct{})
	scanStarted := make(chan struct{})
	var startedOnce sync.Once
	r := New(Config{
		Settings: newStubSettings(),
		ListScanTargets: func(context.Context) []string {
			return []string{"drive"}
		},
		RunScan: func(context.Context, string) {
			startedOnce.Do(func() { close(scanStarted) })
			<-blockScan
		},
	})

	if got := r.Status(); got.State != "idle" || got.Running || got.Queued {
		t.Fatalf("initial status = %#v, want idle", got)
	}

	if !r.TriggerNow() {
		t.Fatal("TriggerNow should queue a manual run")
	}
	if got := r.Status(); got.State != "queued" || got.Running || !got.Queued {
		t.Fatalf("queued status = %#v, want queued", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	select {
	case <-scanStarted:
	case <-time.After(time.Second):
		t.Fatal("pipeline did not start")
	}

	if got := r.Status(); got.State != "running" || !got.Running || got.Queued || got.StartedAt.IsZero() {
		t.Fatalf("running status = %#v, want running with startedAt", got)
	}

	if r.TriggerNow() {
		t.Fatal("TriggerNow during a run should be ignored")
	}
	if got := r.Status(); got.State != "running" || !got.Running || got.Queued {
		t.Fatalf("status after ignored trigger = %#v, want running", got)
	}

	close(blockScan)
	deadline := time.After(time.Second)
	for {
		got := r.Status()
		if !got.Running && !got.Queued && !got.LastFinishedAt.IsZero() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("status did not finish; got=%#v", got)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestStopCurrentCancelsRunningPipeline(t *testing.T) {
	scanStarted := make(chan struct{})
	scanCanceled := make(chan struct{})
	var startedOnce sync.Once
	r := New(Config{
		Settings: newStubSettings(),
		ListScanTargets: func(context.Context) []string {
			return []string{"drive"}
		},
		RunScan: func(ctx context.Context, _ string) {
			startedOnce.Do(func() { close(scanStarted) })
			<-ctx.Done()
			close(scanCanceled)
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	if !r.TriggerNow() {
		t.Fatal("TriggerNow should queue a manual run")
	}
	select {
	case <-scanStarted:
	case <-time.After(time.Second):
		t.Fatal("pipeline did not start")
	}

	if !r.StopCurrent() {
		t.Fatal("StopCurrent should report a running pipeline")
	}
	select {
	case <-scanCanceled:
	case <-time.After(time.Second):
		t.Fatal("StopCurrent did not cancel pipeline context")
	}
}

func TestStopCurrentDropsQueuedTrigger(t *testing.T) {
	r := New(Config{Settings: newStubSettings()})
	if !r.TriggerNow() {
		t.Fatal("TriggerNow should queue a manual run")
	}
	if !r.StopCurrent() {
		t.Fatal("StopCurrent should report a queued pipeline")
	}
	if got := r.Status(); got.State != "idle" || got.Running || got.Queued {
		t.Fatalf("status = %#v, want idle after dropping queued trigger", got)
	}
	if !r.TriggerNow() {
		t.Fatal("TriggerNow should accept a new request after queued stop")
	}
}

func TestTriggerNowAcceptsOnlyOneConcurrentRequest(t *testing.T) {
	r := New(Config{Settings: newStubSettings()})

	const callers = 16
	start := make(chan struct{})
	results := make(chan bool, callers)
	for i := 0; i < callers; i++ {
		go func() {
			<-start
			results <- r.TriggerNow()
		}()
	}
	close(start)

	accepted := 0
	for i := 0; i < callers; i++ {
		if <-results {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted triggers = %d, want 1", accepted)
	}
	if got := r.Status(); got.State != "queued" || got.Running || !got.Queued {
		t.Fatalf("status = %#v, want one queued trigger", got)
	}
}
