package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/video-site/backend/internal/api"
	"github.com/video-site/backend/internal/auth"
	"github.com/video-site/backend/internal/catalog"
	"github.com/video-site/backend/internal/config"
	"github.com/video-site/backend/internal/crawlerupload"
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
	"github.com/video-site/backend/internal/drives/wopan"
	"github.com/video-site/backend/internal/fingerprint"
	"github.com/video-site/backend/internal/mediaasset"
	"github.com/video-site/backend/internal/mediasim"
	"github.com/video-site/backend/internal/nightly"
	"github.com/video-site/backend/internal/preview"
	"github.com/video-site/backend/internal/proxy"
	"github.com/video-site/backend/internal/scanner"
	"github.com/video-site/backend/internal/transcode"
)

const (
	fingerprintReconcileInterval = time.Minute

	videoMaintenanceTitleThreshold           = 0.90
	videoMaintenanceSSIMThreshold            = 0.95
	videoMaintenanceDurationToleranceSeconds = 2

	blacklistSourceDeletePace            = 250 * time.Millisecond
	blacklistSourceDeleteDefaultCooldown = 30 * time.Second
	blacklistSourceDeleteMaxAttempts     = 4
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		if err := runHashPasswordCommand(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	cfgPath := "./config.yaml"
	if v := os.Getenv("VIDEO_CONFIG"); v != "" {
		cfgPath = v
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(cfg.Storage.DBPath), 0o755); err != nil {
		log.Fatalf("mkdir db dir: %v", err)
	}
	if err := os.MkdirAll(cfg.Storage.LocalPreviewDir, 0o755); err != nil {
		log.Fatalf("mkdir preview dir: %v", err)
	}

	cat, err := catalog.Open(cfg.Storage.DBPath)
	if err != nil {
		log.Fatalf("open catalog: %v", err)
	}
	defer cat.Close()

	app := &App{
		cfg:                cfg,
		cat:                cat,
		registry:           proxy.NewRegistry(),
		workers:            make(map[string]*preview.Worker),
		thumbWorkers:       make(map[string]*preview.ThumbWorker),
		fingerprintWorkers: make(map[string]*fingerprint.Worker),
		scriptCrawlers:     make(map[string]*scriptcrawler.Crawler),
	}
	app.proxy = proxy.New(app.registry)
	app.crawlerUploader = crawlerupload.New(crawlerupload.Config{
		Catalog:          cat,
		Registry:         app.registry,
		CommonThumbDir:   app.commonThumbsDir(),
		OnUploadProgress: app.updateCrawlerUploadProgress,
	})

	// 初始化本地内置盘；外部云盘放到 HTTP 服务启动后异步挂载，避免上游
	// 登录态校验拖慢端口监听。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app.loadTheme(ctx)
	if removed, err := app.cleanupOrphanDriveVideos(ctx); err != nil {
		log.Printf("[cleanup] orphan drive videos: %v", err)
	} else if removed > 0 {
		log.Printf("[cleanup] removed %d orphan drive videos", removed)
	}
	if err := app.attachLocalUpload(ctx); err != nil {
		log.Printf("[local-upload] attach failed: %v", err)
	}
	go app.runFingerprintReconciler(ctx)

	authr := &auth.Authenticator{
		Username: cfg.Server.Admin.Username,
		Password: cfg.Server.Admin.Password,
		Catalog:  cat,
	}
	setupRequired := config.RequiresAdminSetup(cfg)
	if !setupRequired {
		if err := ensureConfigAdminUser(ctx, cat, cfg); err != nil {
			log.Printf("[auth] migrate config admin: %v", err)
		}
	}
	var setupMu sync.Mutex
	versionFilePath := strings.TrimSpace(os.Getenv("VIDEO_VERSION_FILE"))
	if versionFilePath == "" {
		versionFilePath = filepath.Join(filepath.Dir(cfgPath), ".version")
	}
	githubRepo := strings.TrimSpace(os.Getenv("VIDEO_GITHUB_REPO"))
	if githubRepo == "" {
		githubRepo = strings.TrimSpace(os.Getenv("GITHUB_REPO"))
	}

	apiServer := &api.Server{
		Catalog:   cat,
		Proxy:     app.proxy,
		LocalDir:  cfg.Storage.LocalPreviewDir,
		UploadDir: app.localUploadDir(),
		OnVideoUploaded: func(v *catalog.Video) {
			app.enqueueUploadedVideo(ctx, v)
		},
		// 前台「不再展示」走拉黑逻辑：删记录 + 删本地封面/预览 + 写墓碑，
		// 保留网盘源文件（deleteSource=false）。后续任务不再入库；可重新发现的
		// 普通网盘/爬虫来源可在后台解除墓碑，操作本身不会立即触发扫盘或爬取。
		OnHideVideo: func(reqCtx context.Context, videoID string) error {
			_, err := app.deleteVideo(reqCtx, videoID, false)
			return err
		},
		GetTheme: func() string { return app.Theme() },
	}

	adminServer := &api.AdminServer{
		Catalog:         cat,
		Auth:            authr,
		VersionFilePath: versionFilePath,
		ImageVersion:    strings.TrimSpace(os.Getenv("VIDEO_IMAGE_VERSION")),
		GitHubRepo:      githubRepo,
		SetupRequired: func() bool {
			setupMu.Lock()
			defer setupMu.Unlock()
			return setupRequired
		},
		OnSetup: func(username, password string) error {
			setupMu.Lock()
			defer setupMu.Unlock()
			if !setupRequired {
				return nil
			}
			if err := config.WriteAdminCredentials(cfgPath, username, password); err != nil {
				return err
			}
			hashed, err := auth.HashPassword(password)
			if err != nil {
				return err
			}
			if _, err := cat.CreateUser(ctx, username, hashed, "admin"); err != nil {
				return err
			}
			cfg.Server.Admin.Username = username
			cfg.Server.Admin.Password = password
			authr.SetCredentials(username, password)
			setupRequired = false
			return nil
		},
		LocalPreviewDir: cfg.Storage.LocalPreviewDir,
		OnDriveSaved: func(driveID string) error {
			d, err := cat.GetDrive(ctx, driveID)
			if err != nil {
				return err
			}
			if err := app.attachDrive(ctx, d); err != nil {
				return err
			}
			app.scheduleCrawlerUploadMigration(ctx, driveID)
			// 本地存储开启 .strm 越root后，之前因 strm 指向目录外而失败的封面/
			// 预览/指纹应自动重试，省得用户再手动点三个"重试失败"按钮。
			if d.Kind == localstorage.Kind &&
				parseBoolDefault(strings.TrimSpace(d.Credentials["strm_allow_outside_root"]), false) {
				go app.regenFailedThumbnails(ctx, driveID)
				go app.regenFailedPreviews(ctx, driveID)
				go app.regenFailedFingerprints(ctx, driveID)
			}
			return nil
		},
		OnDriveDeleteCleanup: func(cleanupCtx context.Context, driveID string) (int, error) {
			return app.cleanupDriveVideosForDelete(cleanupCtx, driveID)
		},
		OnDriveRemoved: func(driveID string) {
			app.detachDrive(driveID)
		},
		OnScanRequested: func(driveID string) bool {
			// 爬虫类 drive 的"重扫"等同于手动触发一次爬取；其它 drive 走标准 scan
			isScriptCrawler := false
			if d, err := app.cat.GetDrive(ctx, driveID); err == nil && d != nil {
				isScriptCrawler = d.Kind == scriptcrawler.Kind
			}
			if isScriptCrawler {
				return app.scheduleScriptCrawlerCrawl(ctx, driveID)
			}
			return app.scheduleScan(ctx, driveID)
		},
		OnCrawlerUploadRequested: func(driveID string) (bool, string) {
			return app.scheduleManualCrawlerUploadMigration(ctx, driveID)
		},
		OnStopDriveTasks: func(driveID string) bool {
			return app.stopDriveTasks(ctx, driveID)
		},
		OnStopAllTasks: func() int {
			return app.stopAllDriveTasks(ctx)
		},
		OnRegenPreview: func(videoID string) {
			go app.regenPreview(ctx, videoID)
		},
		OnRegenAllPreviews: func() {
			go app.regenAllPreviews(ctx)
		},
		OnRegenFailedPreviews: func(driveID string) {
			go app.regenFailedPreviews(ctx, driveID)
		},
		OnRegenFailedThumbnails: func(driveID string) {
			go app.regenFailedThumbnails(ctx, driveID)
		},
		OnRegenFailedFingerprints: func(driveID string) {
			go app.regenFailedFingerprints(ctx, driveID)
		},
		OnStartDriveTranscode: func(driveID string) (bool, string) {
			return app.startDriveTranscode(ctx, driveID)
		},
		OnStopDriveTranscode: func(driveID string) bool {
			return app.stopDriveTranscode(driveID)
		},
		OnDeleteVideo: func(reqCtx context.Context, videoID string, deleteSource bool) (api.DeleteVideoResult, error) {
			return app.deleteVideo(reqCtx, videoID, deleteSource)
		},
		OnStartBlacklistSourceDelete: func(req api.BlacklistSourceDeleteRequest) bool {
			return app.startBlacklistSourceDelete(ctx, req)
		},
		GetBlacklistSourceDeleteStatus: func() api.BlacklistSourceDeleteStatus {
			return app.blacklistSourceDeleteStatus()
		},
		OnStartTagRetag: func() bool {
			return app.startTagRetag(ctx)
		},
		GetTagJobStatus: func() api.TagJobStatus {
			return app.tagJobStatus()
		},
		GetDriveGenerationStatuses: func() map[string]api.DriveGenerationStatuses {
			return app.driveGenerationStatuses()
		},
		GetPreviewGenerationVideoIDs: func() map[string]bool {
			return app.previewGenerationVideoIDs()
		},
		OnTeaserEnabledChanged: func(driveID string, enabled bool) {
			// 从关到开时立刻补扫该盘 pending 预览视频，行为对齐旧的"全局开关从关到开"。
			// 关闭分支不需要做事 —— 入队前会重新查 catalog，新的 enqueue 自然停。
			if !enabled {
				return
			}
			app.mu.Lock()
			worker := app.workers[driveID]
			thumbWorker := app.thumbWorkers[driveID]
			app.mu.Unlock()
			go app.enqueueDriveGeneration(ctx, driveID, worker, thumbWorker)
		},
		GetTheme: func() string { return app.Theme() },
		SetTheme: func(theme string) error {
			return app.SetTheme(ctx, theme)
		},
		OnRunNightlyJob: func() bool {
			if app.nightlyRunner != nil {
				return app.nightlyRunner.TriggerNow()
			}
			return false
		},
		GetNightlyJobStatus: func() api.NightlyJobStatus {
			return app.nightlyJobStatus()
		},
		ListDriveDirChildren: func(reqCtx context.Context, driveID, parentID string) ([]api.DriveDirEntry, error) {
			return app.listDriveDirChildren(reqCtx, driveID, parentID)
		},
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware(cfg.Server.AllowedOrigins))

	apiServer.RegisterRoutes(r, authr)
	adminServer.Register(r)
	mountFrontend(r)

	// 凌晨流水线：每天 cron_hour 触发一次，串行跑
	//   Phase 1 扫所有非爬虫 / localupload 网盘 + 删除检测 + 入队封面/预览视频
	//   Phase 2 脚本爬虫 + 入队预览视频
	//   Phase 3 爬虫本地视频 → 云盘上传
	//   Phase 4 全库重复视频维护：精确指纹去重 + 标题/时长/封面近似去重
	// 标签匹配不在夜间流水线中全库重算；新视频入库和管理员修改标签规则时按事件刷新。
	// 也响应 admin "扫描所有网盘" 按钮（POST /admin/api/jobs/nightly/run → TriggerNow）。
	app.nightlyRunner = nightly.New(nightly.Config{
		Settings:              cat,
		CronHour:              cfg.Nightly.CronHour,
		ListScanTargets:       app.listScanTargetIDs,
		RunScan:               app.runScan,
		ListCrawlerDrives:     app.listCrawlerDriveIDs,
		RunCrawlerCrawl:       app.runScriptCrawlerCrawl,
		WaitPreviewQueuesIdle: app.waitAllPreviewQueuesIdle,
		RunMigration:          app.crawlerUploader.RunOnce,
		RunDedupeAssetCleanup: app.cleanupDuplicateVideoAssets,
	})
	go app.nightlyRunner.Run(ctx)

	srv := &http.Server{
		Addr:    cfg.Server.Listen,
		Handler: r,
	}
	listener, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.Server.Listen, err)
	}
	go func() {
		log.Printf("video-site backend listening on %s", cfg.Server.Listen)
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()
	go app.attachExistingDrives(ctx)
	go app.migrateHiddenVideosToTombstone(ctx)

	// 等待退出信号
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	log.Println("shutting down...")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
}

func runHashPasswordCommand(r io.Reader, w io.Writer) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	password := strings.TrimRight(string(data), "\r\n")
	if len(password) < 6 {
		return fmt.Errorf("password must be at least 6 characters")
	}
	hashed, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	_, err = fmt.Fprintln(w, hashed)
	return err
}

func ensureConfigAdminUser(ctx context.Context, cat *catalog.Catalog, cfg *config.Config) error {
	if cat == nil || cfg == nil {
		return nil
	}
	username := strings.TrimSpace(cfg.Server.Admin.Username)
	password := cfg.Server.Admin.Password
	if username == "" || password == "" {
		return nil
	}
	if _, err := cat.GetUserByUsername(ctx, username); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	hashed, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = cat.CreateUser(ctx, username, hashed, "admin")
	return err
}

// ---------- App ----------

type App struct {
	cfg      *config.Config
	cat      *catalog.Catalog
	registry *proxy.Registry
	proxy    *proxy.Proxy

	mu                 sync.Mutex
	workers            map[string]*preview.Worker
	thumbWorkers       map[string]*preview.ThumbWorker
	fingerprintWorkers map[string]*fingerprint.Worker
	cancels            map[string]context.CancelFunc
	// scriptCrawlers 按 driveID 索引，每个脚本爬虫 drive 独立一个 Crawler。
	scriptCrawlers map[string]*scriptcrawler.Crawler

	// driveAttachMu 串行化云盘挂载/重挂载。挂载会访问上游服务，可能较慢；
	// 串行化可以避免启动后台挂载和手动扫盘按需挂载同一个 drive 时重复创建 worker。
	driveAttachMu sync.Mutex

	// 全站主题（"dark" | "pink" | "sky"），从 DB 读
	theme string

	// crawlerUploader 把脚本爬虫保存在本地的视频上传到每个爬虫配置的目标 drive。
	crawlerUploader crawlerUploadRunner

	// nightlyRunner 是凌晨流水线调度器：每天 cron_hour 串行跑扫盘 → 脚本爬虫 → 上传。
	// 也响应 admin 「扫描所有网盘」按钮（TriggerNow）。
	nightlyRunner *nightly.Runner

	// scanQueueMu 保护 scanQueued 和 scanProgress。
	scanQueueMu sync.Mutex
	// scanQueued 跟踪哪些 driveID 已经排队或正在跑扫盘/爬取，去重后续重复点击。
	// 不同 drive 互不等待，可以并行扫；同一个 drive 只能有一个扫盘/抓取任务。
	scanQueued map[string]bool
	// scanProgress 跟踪每个正在扫盘/抓取的 drive 当前进度。
	scanProgress map[string]driveScanProgress

	// taskCancelMu 保护 driveTaskCancels。这里登记的是可被"停止任务"按钮中断
	// 的 drive 级任务上下文：扫盘、91 爬取、指纹补队列、失败生成重试等。
	taskCancelMu       sync.Mutex
	driveTaskCancelSeq uint64
	driveTaskCancels   map[string]map[uint64]context.CancelFunc

	// fingerprintQueueing 去重每个 drive 的 pending 指纹补队列任务，避免定时
	// reconcile 和扫盘结束同时为同一批 pending 视频启动多个长时间入队 goroutine。
	fingerprintQueueMu  sync.Mutex
	fingerprintQueueing map[string]bool

	// crawlerUploadRunning 去重"保存上传目标后检查本地未上传文件"的后台任务。
	crawlerUploadMu      sync.Mutex
	crawlerUploadRunning map[string]bool

	// uploadProgress 跟踪脚本爬虫迁移到云盘时的实时上传状态。
	uploadProgressMu sync.Mutex
	uploadProgress   map[string]driveUploadProgress

	// transcodeMu 保护 transcodeWorkers / transcodeCancels。
	// 浏览器兼容性转码每盘最多一个任务，且只能由管理员手动开启
	// （不随扫盘/夜间流水线自动运行），手动停止或处理完即从 map 清除。
	transcodeMu      sync.Mutex
	transcodeWorkers map[string]*transcode.Worker
	transcodeCancels map[string]context.CancelFunc

	// blacklistSourceDeleteMu protects the one-at-a-time background job that
	// removes source files for tombstoned videos. The job reads tombstones from
	// the catalog and purges each one after a successful provider delete.
	blacklistSourceDeleteMu    sync.Mutex
	blacklistSourceDeleteState api.BlacklistSourceDeleteStatus

	// tagJobMu protects the admin-visible tag job status. tagMaintenanceMu
	// serializes bulk writes to video_tags across startup, manual, and nightly
	// maintenance paths.
	tagJobMu         sync.Mutex
	tagMaintenanceMu sync.Mutex
	tagJobState      api.TagJobStatus
}

type driveScanProgress struct {
	Scanned       int
	Added         int
	CooldownUntil time.Time
}

type driveUploadProgress struct {
	State        string
	CurrentTitle string
	QueueLength  int
	DoneCount    int
	TotalCount   int
}

type crawlerUploadRunner interface {
	RunOnce(ctx context.Context) error
}

// teaserEnabledForDrive 查询某个 drive 当前的 per-drive 预览视频开关。
//
// 预览视频生成不再由全局 setting 控制，而是由 catalog.drives.teaser_enabled
// 决定。任何"是否入队 preview worker"的判断都应通过这个方法读，避免把状态
// 散落到 App 内存里和 DB 不一致。
//
// local-upload 是内置盘，不一定有 catalog.drives 行；缺省按开启处理。
//
// 其它 drive 读 catalog 失败时退化成 false（不生成）：比 "默认开" 更安全 —— 读不到
// 状态时倾向不消耗 ffmpeg；调用方会记日志，运维能立刻看到问题。
func (a *App) teaserEnabledForDrive(ctx context.Context, driveID string) bool {
	d, err := a.cat.GetDrive(ctx, driveID)
	if err != nil {
		if driveID == localupload.DriveID && errors.Is(err, sql.ErrNoRows) {
			return true
		}
		log.Printf("[preview] read teaser_enabled drive=%s: %v (treating as disabled)", driveID, err)
		return false
	}
	return d.TeaserEnabled
}

// Theme 线程安全读当前主题。
func (a *App) Theme() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.theme == "" {
		return "dark"
	}
	return a.theme
}

// SetTheme 切换并持久化主题；未知值会返回错误。
func (a *App) SetTheme(ctx context.Context, theme string) error {
	if theme != "dark" && theme != "pink" && theme != "sky" {
		return fmt.Errorf("unsupported theme %q", theme)
	}
	a.mu.Lock()
	a.theme = theme
	a.mu.Unlock()
	return a.cat.SetSetting(ctx, "ui.theme", theme)
}

// loadTheme 从 DB 读全站主题；找不到时回退到 "dark"。
func (a *App) loadTheme(ctx context.Context) {
	v, err := a.cat.GetSetting(ctx, "ui.theme", "dark")
	if err != nil {
		log.Printf("[theme] load setting: %v (fallback to dark)", err)
		a.mu.Lock()
		a.theme = "dark"
		a.mu.Unlock()
		return
	}
	if v != "pink" && v != "dark" && v != "sky" {
		v = "dark"
	}
	a.mu.Lock()
	a.theme = v
	a.mu.Unlock()
}

func (a *App) nightlyJobStatus() api.NightlyJobStatus {
	if a.nightlyRunner == nil {
		return api.NightlyJobStatus{State: "idle"}
	}
	status := a.nightlyRunner.Status()
	return api.NightlyJobStatus{
		State:          status.State,
		Running:        status.Running,
		Queued:         status.Queued,
		StartedAt:      formatOptionalRFC3339(status.StartedAt),
		LastFinishedAt: formatOptionalRFC3339(status.LastFinishedAt),
	}
}

func formatOptionalRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func (a *App) driveGenerationStatuses() map[string]api.DriveGenerationStatuses {
	a.scanQueueMu.Lock()
	scanningDrives := make(map[string]bool, len(a.scanQueued))
	for id, running := range a.scanQueued {
		scanningDrives[id] = running
	}
	scanProgresses := make(map[string]driveScanProgress, len(a.scanProgress))
	for id, progress := range a.scanProgress {
		scanProgresses[id] = progress
	}
	a.scanQueueMu.Unlock()

	a.uploadProgressMu.Lock()
	uploadProgresses := make(map[string]driveUploadProgress, len(a.uploadProgress))
	for id, progress := range a.uploadProgress {
		uploadProgresses[id] = progress
	}
	a.uploadProgressMu.Unlock()

	a.mu.Lock()
	previewWorkers := make(map[string]*preview.Worker, len(a.workers))
	for id, worker := range a.workers {
		previewWorkers[id] = worker
	}
	thumbWorkers := make(map[string]*preview.ThumbWorker, len(a.thumbWorkers))
	for id, worker := range a.thumbWorkers {
		thumbWorkers[id] = worker
	}
	fingerprintWorkers := make(map[string]*fingerprint.Worker, len(a.fingerprintWorkers))
	for id, worker := range a.fingerprintWorkers {
		fingerprintWorkers[id] = worker
	}
	a.mu.Unlock()

	a.transcodeMu.Lock()
	transcodeWorkers := make(map[string]*transcode.Worker, len(a.transcodeWorkers))
	for id, worker := range a.transcodeWorkers {
		transcodeWorkers[id] = worker
	}
	a.transcodeMu.Unlock()

	out := make(map[string]api.DriveGenerationStatuses, len(scanningDrives)+len(previewWorkers)+len(thumbWorkers)+len(fingerprintWorkers)+len(uploadProgresses)+len(transcodeWorkers))
	now := time.Now()
	for id, running := range scanningDrives {
		if !running {
			continue
		}
		progress := scanProgresses[id]
		state := "scanning"
		if progress.CooldownUntil.After(now) {
			state = "cooling"
		}
		status := out[id]
		status.Scan = api.GenerationStatus{
			State:        state,
			ScannedCount: progress.Scanned,
			AddedCount:   progress.Added,
		}
		if !progress.CooldownUntil.IsZero() {
			status.Scan.CooldownUntil = progress.CooldownUntil.Format(time.RFC3339)
		}
		out[id] = status
	}
	for id, worker := range previewWorkers {
		status := out[id]
		status.Preview = generationStatusFromPreview(worker.Status())
		out[id] = status
	}
	for id, worker := range thumbWorkers {
		status := out[id]
		status.Thumbnail = generationStatusFromPreview(worker.Status())
		out[id] = status
	}
	for id, worker := range fingerprintWorkers {
		status := out[id]
		status.Fingerprint = generationStatusFromFingerprint(worker.Status())
		out[id] = status
	}
	for id, progress := range uploadProgresses {
		state := progress.State
		if state == "" {
			state = "idle"
		}
		status := out[id]
		status.Upload = api.GenerationStatus{
			State:        state,
			CurrentTitle: progress.CurrentTitle,
			QueueLength:  progress.QueueLength,
			DoneCount:    progress.DoneCount,
			TotalCount:   progress.TotalCount,
		}
		out[id] = status
	}
	for id, worker := range transcodeWorkers {
		status := out[id]
		status.Transcode = generationStatusFromTranscode(worker.Status())
		out[id] = status
	}
	return out
}

func (a *App) previewGenerationVideoIDs() map[string]bool {
	a.mu.Lock()
	previewWorkers := make([]*preview.Worker, 0, len(a.workers))
	for _, worker := range a.workers {
		previewWorkers = append(previewWorkers, worker)
	}
	a.mu.Unlock()

	out := make(map[string]bool)
	for _, worker := range previewWorkers {
		for _, id := range worker.ActiveVideoIDs() {
			out[id] = true
		}
	}
	return out
}

func (a *App) updateCrawlerUploadProgress(progress crawlerupload.UploadProgress) {
	driveID := strings.TrimSpace(progress.DriveID)
	if driveID == "" {
		return
	}
	state := strings.TrimSpace(progress.State)
	if state == "" {
		state = "idle"
	}
	a.uploadProgressMu.Lock()
	if a.uploadProgress == nil {
		a.uploadProgress = make(map[string]driveUploadProgress)
	}
	if state == "idle" {
		delete(a.uploadProgress, driveID)
		a.uploadProgressMu.Unlock()
		return
	}
	a.uploadProgress[driveID] = driveUploadProgress{
		State:        state,
		CurrentTitle: strings.TrimSpace(progress.CurrentTitle),
		QueueLength:  progress.QueueLength,
		DoneCount:    progress.DoneCount,
		TotalCount:   progress.TotalCount,
	}
	a.uploadProgressMu.Unlock()
}

func (a *App) clearCrawlerUploadProgress(driveID string) bool {
	driveID = strings.TrimSpace(driveID)
	if driveID == "" {
		return false
	}
	a.uploadProgressMu.Lock()
	_, ok := a.uploadProgress[driveID]
	delete(a.uploadProgress, driveID)
	a.uploadProgressMu.Unlock()
	return ok
}

func (a *App) clearAllCrawlerUploadProgress() []string {
	a.uploadProgressMu.Lock()
	ids := make([]string, 0, len(a.uploadProgress))
	for id := range a.uploadProgress {
		ids = append(ids, id)
	}
	a.uploadProgress = nil
	a.uploadProgressMu.Unlock()
	return ids
}

func generationStatusFromPreview(status preview.TaskStatus) api.GenerationStatus {
	state := status.State
	if state == "" {
		state = "idle"
	}
	out := api.GenerationStatus{
		State:        state,
		CurrentTitle: status.CurrentTitle,
		QueueLength:  status.QueueLength,
	}
	if !status.CooldownUntil.IsZero() {
		out.CooldownUntil = status.CooldownUntil.Format(time.RFC3339)
	}
	return out
}

func generationStatusFromFingerprint(status fingerprint.TaskStatus) api.GenerationStatus {
	state := status.State
	if state == "" {
		state = "idle"
	}
	out := api.GenerationStatus{
		State:        state,
		CurrentTitle: status.CurrentTitle,
		QueueLength:  status.QueueLength,
	}
	if !status.CooldownUntil.IsZero() {
		out.CooldownUntil = status.CooldownUntil.Format(time.RFC3339)
	}
	return out
}

func generationStatusFromTranscode(status transcode.TaskStatus) api.GenerationStatus {
	state := status.State
	if state == "" {
		state = "idle"
	}
	return api.GenerationStatus{
		State:        state,
		CurrentTitle: status.CurrentTitle,
		QueueLength:  status.QueueLength,
		DoneCount:    status.DoneCount,
		TotalCount:   status.TotalCount,
	}
}

// transcodeWorkDir 返回转码用的本地临时目录（下载原片 / 写产物），与
// localUploadDir 一样挂在数据目录下，避免 /tmp 空间不足。
func (a *App) transcodeWorkDir() string {
	return filepath.Join(filepath.Dir(a.cfg.Storage.LocalPreviewDir), "transcode-tmp")
}

// startDriveTranscode 手动开启某盘的浏览器兼容性转码。
// 转码从不自动运行：扫盘、夜间流水线都不会触发，这里是唯一入口。
// 任务跑完候选列表后自然结束；中途可用 stopDriveTranscode / 停止所有任务中断。
func (a *App) startDriveTranscode(ctx context.Context, driveID string) (bool, string) {
	driveID = strings.TrimSpace(driveID)
	if driveID == "" {
		return false, "缺少存储 ID"
	}
	drv, ok := a.registry.Get(driveID)
	if !ok {
		return false, "存储未挂载或不可用"
	}
	switch drv.Kind() {
	case scriptcrawler.Kind:
		return false, "爬虫存储不支持转码"
	}
	workDir := a.transcodeWorkDir()
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return false, "创建转码临时目录失败: " + err.Error()
	}

	a.transcodeMu.Lock()
	if a.transcodeWorkers == nil {
		a.transcodeWorkers = make(map[string]*transcode.Worker)
		a.transcodeCancels = make(map[string]context.CancelFunc)
	}
	if existing := a.transcodeWorkers[driveID]; existing != nil {
		a.transcodeMu.Unlock()
		return false, "该存储的转码任务已在运行"
	}
	worker := transcode.NewWorker(transcode.Config{
		FFmpegPath:  a.cfg.Preview.FFmpegPath,
		FFprobePath: a.cfg.Preview.FFprobePath,
		WorkDir:     workDir,
	}, a.cat, drv)
	taskCtx, done := a.registerDriveTaskContext(ctx, driveID)
	runCtx, cancel := context.WithCancel(taskCtx)
	a.transcodeWorkers[driveID] = worker
	a.transcodeCancels[driveID] = cancel
	a.transcodeMu.Unlock()

	go func() {
		defer func() {
			cancel()
			done()
			a.transcodeMu.Lock()
			if a.transcodeWorkers[driveID] == worker {
				delete(a.transcodeWorkers, driveID)
				delete(a.transcodeCancels, driveID)
			}
			a.transcodeMu.Unlock()
		}()
		candidates, err := a.cat.ListTranscodeCandidates(runCtx, driveID, 0)
		if err != nil {
			log.Printf("[transcode] list candidates drive=%s: %v", driveID, err)
			return
		}
		if len(candidates) == 0 {
			log.Printf("[transcode] drive=%s no candidates", driveID)
			return
		}
		log.Printf("[transcode] drive=%s start, %d candidates", driveID, len(candidates))
		worker.Run(runCtx, candidates)
	}()
	return true, ""
}

// stopAllDriveTranscodes 停掉所有盘的转码任务，返回被停的 driveID 列表。
func (a *App) stopAllDriveTranscodes() []string {
	a.transcodeMu.Lock()
	cancels := a.transcodeCancels
	a.transcodeCancels = nil
	a.transcodeWorkers = nil
	a.transcodeMu.Unlock()
	ids := make([]string, 0, len(cancels))
	for id, cancel := range cancels {
		if cancel != nil {
			cancel()
		}
		ids = append(ids, id)
	}
	return ids
}

// stopDriveTranscode 手动停止某盘的转码任务。返回是否有任务被停。
func (a *App) stopDriveTranscode(driveID string) bool {
	driveID = strings.TrimSpace(driveID)
	a.transcodeMu.Lock()
	cancel := a.transcodeCancels[driveID]
	delete(a.transcodeCancels, driveID)
	delete(a.transcodeWorkers, driveID)
	a.transcodeMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	log.Printf("[transcode] stop drive=%s", driveID)
	return true
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
			RootPath:       d.Credentials["root_path"],
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
			UseOnlineAPI: parseBoolDefault(d.Credentials["use_online_api"], true),
			RenewAPIURL:  d.Credentials["api_url_address"],
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

// migrateHiddenVideosToTombstone 把历史「隐藏」视频一次性迁移为黑名单墓碑。
// 隐藏机制已废弃——前台「不再展示」改走拉黑逻辑。迁移＝删库记录 + 删本地
// 封面/预览 + 写墓碑，保留网盘源文件。迁移后无 hidden=1 记录，重复执行为空操作。
func (a *App) migrateHiddenVideosToTombstone(ctx context.Context) {
	if a == nil || a.cat == nil {
		return
	}
	hidden, err := a.cat.ListHiddenVideos(ctx)
	if err != nil {
		log.Printf("[migrate] list hidden videos: %v", err)
		return
	}
	if len(hidden) == 0 {
		return
	}
	log.Printf("[migrate] converting %d hidden video(s) to blacklist tombstones", len(hidden))
	migrated := 0
	for _, v := range hidden {
		if _, err := a.deleteVideo(ctx, v.ID, false); err != nil {
			log.Printf("[migrate] hidden->tombstone %s: %v", v.ID, err)
			continue
		}
		migrated++
	}
	log.Printf("[migrate] hidden->tombstone done: %d/%d", migrated, len(hidden))
}

func (a *App) deleteVideo(ctx context.Context, videoID string, deleteSource bool) (api.DeleteVideoResult, error) {
	if a == nil || a.cat == nil {
		return api.DeleteVideoResult{}, sql.ErrNoRows
	}
	v, err := a.cat.GetVideo(ctx, videoID)
	if err != nil {
		return api.DeleteVideoResult{}, err
	}

	deletedSource := false
	if deleteSource {
		deletedSource, err = a.removeVideoSourceFile(ctx, v)
		if err != nil {
			return api.DeleteVideoResult{}, err
		}
	}

	localDir := ""
	if a.cfg != nil {
		localDir = a.cfg.Storage.LocalPreviewDir
	}
	if err := removeLocalVideoAssets(localDir, v); err != nil {
		return api.DeleteVideoResult{}, fmt.Errorf("remove local assets for %s: %w", v.ID, err)
	}
	if err := a.cat.DeleteVideoWithTombstoneOptions(ctx, v.ID, catalog.DeleteVideoTombstoneOptions{
		SourceDeleted: deletedSource,
	}); err != nil {
		return api.DeleteVideoResult{}, err
	}
	return api.DeleteVideoResult{OK: true, DeletedSource: deletedSource}, nil
}

func (a *App) startBlacklistSourceDelete(ctx context.Context, req api.BlacklistSourceDeleteRequest) bool {
	if a == nil || a.cat == nil {
		return false
	}
	req = normalizeBlacklistSourceDeleteRequest(req)
	a.blacklistSourceDeleteMu.Lock()
	if a.blacklistSourceDeleteState.Running {
		a.blacklistSourceDeleteMu.Unlock()
		return false
	}
	a.blacklistSourceDeleteState = api.BlacklistSourceDeleteStatus{
		State:     "running",
		Running:   true,
		StartedAt: time.Now().Format(time.RFC3339),
	}
	a.blacklistSourceDeleteMu.Unlock()

	go a.runBlacklistSourceDelete(ctx, req)
	return true
}

func normalizeBlacklistSourceDeleteRequest(req api.BlacklistSourceDeleteRequest) api.BlacklistSourceDeleteRequest {
	if req.DeleteAllSources {
		return api.BlacklistSourceDeleteRequest{DeleteAllSources: true}
	}
	seen := make(map[string]bool, len(req.IDs))
	ids := make([]string, 0, len(req.IDs))
	for _, id := range req.IDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return api.BlacklistSourceDeleteRequest{DeleteAllSources: true}
	}
	return api.BlacklistSourceDeleteRequest{IDs: ids}
}

func (a *App) blacklistSourceDeleteStatus() api.BlacklistSourceDeleteStatus {
	if a == nil {
		return api.BlacklistSourceDeleteStatus{State: "idle"}
	}
	a.blacklistSourceDeleteMu.Lock()
	defer a.blacklistSourceDeleteMu.Unlock()
	status := a.blacklistSourceDeleteState
	if status.State == "" {
		status.State = "idle"
	}
	return status
}

func (a *App) runBlacklistSourceDelete(ctx context.Context, reqs ...api.BlacklistSourceDeleteRequest) {
	req := api.BlacklistSourceDeleteRequest{DeleteAllSources: true}
	if len(reqs) > 0 {
		req = normalizeBlacklistSourceDeleteRequest(reqs[0])
	}

	var (
		items []*catalog.DeletedVideo
		err   error
	)
	if req.DeleteAllSources {
		items, err = a.cat.ListDeletedVideosPendingSourceDeletion(ctx)
	} else {
		items, err = a.cat.ListDeletedVideosPendingSourceDeletionByIDs(ctx, req.IDs)
	}
	if err != nil {
		a.finishBlacklistSourceDelete("failed", err)
		return
	}

	a.blacklistSourceDeleteMu.Lock()
	a.blacklistSourceDeleteState.Total = len(items)
	a.blacklistSourceDeleteMu.Unlock()

	for index, item := range items {
		if err := ctx.Err(); err != nil {
			a.finishBlacklistSourceDelete("canceled", err)
			return
		}
		if item == nil {
			continue
		}

		a.blacklistSourceDeleteMu.Lock()
		a.blacklistSourceDeleteState.CurrentFile = item.FileName
		if a.blacklistSourceDeleteState.CurrentFile == "" {
			a.blacklistSourceDeleteState.CurrentFile = item.ID
		}
		a.blacklistSourceDeleteMu.Unlock()

		deleteErr := a.removeDeletedVideoSourceFile(ctx, item)
		if deleteErr == nil {
			deleteErr = a.purgeDeletedVideoTombstone(ctx, item.ID)
		}

		a.blacklistSourceDeleteMu.Lock()
		a.blacklistSourceDeleteState.Processed++
		if deleteErr != nil {
			a.blacklistSourceDeleteState.Failed++
			a.blacklistSourceDeleteState.LastError = deleteErr.Error()
		} else {
			a.blacklistSourceDeleteState.Deleted++
		}
		a.blacklistSourceDeleteMu.Unlock()

		if deleteErr != nil {
			log.Printf("[blacklist-source-delete] id=%s drive=%s file=%s failed: %v", item.ID, item.DriveID, item.FileID, deleteErr)
		} else {
			log.Printf("[blacklist-source-delete] id=%s drive=%s file=%s deleted", item.ID, item.DriveID, item.FileID)
		}

		if index+1 < len(items) {
			if err := waitForBlacklistSourceDelete(ctx, blacklistSourceDeletePace); err != nil {
				a.finishBlacklistSourceDelete("canceled", err)
				return
			}
		}
	}

	a.finishBlacklistSourceDelete("completed", nil)
}

func (a *App) removeDeletedVideoSourceFile(ctx context.Context, item *catalog.DeletedVideo) error {
	if item == nil {
		return errors.New("remove blacklisted source: empty tombstone")
	}
	if strings.TrimSpace(item.FileID) == "" {
		return fmt.Errorf("remove blacklisted source %s: empty file id", item.ID)
	}
	video := &catalog.Video{
		ID:       item.ID,
		DriveID:  item.DriveID,
		FileID:   item.FileID,
		ParentID: item.ParentID,
		FileName: item.FileName,
		Size:     item.Size,
	}
	var lastErr error
	for attempt := 0; attempt < blacklistSourceDeleteMaxAttempts; attempt++ {
		_, err := a.removeVideoSourceFile(ctx, video)
		if err == nil {
			return nil
		}
		lastErr = err
		wait, rateLimited := drives.RateLimitRetryAfter(err)
		if !rateLimited && drives.TextMentionsHTTPStatus(err.Error(), http.StatusTooManyRequests) {
			rateLimited = true
		}
		if !rateLimited || attempt+1 >= blacklistSourceDeleteMaxAttempts {
			return err
		}
		if wait <= 0 {
			wait = blacklistSourceDeleteDefaultCooldown
		}
		a.blacklistSourceDeleteMu.Lock()
		a.blacklistSourceDeleteState.LastError = fmt.Sprintf("%s 限流，等待 %s 后重试", item.FileName, wait)
		a.blacklistSourceDeleteMu.Unlock()
		log.Printf("[blacklist-source-delete] id=%s drive=%s rate limited; retry_in=%s attempt=%d", item.ID, item.DriveID, wait, attempt+1)
		if err := waitForBlacklistSourceDelete(ctx, wait); err != nil {
			return err
		}
	}
	return lastErr
}

func waitForBlacklistSourceDelete(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *App) purgeDeletedVideoTombstone(ctx context.Context, videoID string) error {
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.cat.PurgeDeletedVideo(ctx, videoID); err != nil {
			if !isSQLiteBusyError(err) {
				return err
			}
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
			continue
		}
		return nil
	}
	return fmt.Errorf("purge blacklisted tombstone after retries: %w", lastErr)
}

func (a *App) finishBlacklistSourceDelete(state string, err error) {
	a.blacklistSourceDeleteMu.Lock()
	defer a.blacklistSourceDeleteMu.Unlock()
	a.blacklistSourceDeleteState.State = state
	a.blacklistSourceDeleteState.Running = false
	a.blacklistSourceDeleteState.CurrentFile = ""
	a.blacklistSourceDeleteState.LastFinished = time.Now().Format(time.RFC3339)
	if err != nil {
		a.blacklistSourceDeleteState.LastError = err.Error()
	}
}

func (a *App) removeVideoSourceFile(ctx context.Context, v *catalog.Video) (bool, error) {
	if v == nil {
		return false, errors.New("remove video source: empty video")
	}
	if a == nil {
		return false, fmt.Errorf("remove video source %s: app unavailable: %w", v.ID, drives.ErrNotSupported)
	}
	fileID := strings.TrimSpace(v.FileID)
	if fileID == "" {
		return false, fmt.Errorf("remove video source %s: empty file id", v.ID)
	}
	if a == nil || a.registry == nil {
		return false, fmt.Errorf("remove video source %s: drive registry unavailable: %w", v.ID, drives.ErrNotSupported)
	}
	if _, ok := a.registry.Get(v.DriveID); !ok {
		if a.cat == nil {
			return false, fmt.Errorf("remove video source %s: drive %s not attached: %w", v.ID, v.DriveID, drives.ErrNotSupported)
		}
		if err := a.ensureDriveAttached(ctx, v.DriveID); err != nil {
			return false, fmt.Errorf("remove video source %s: attach drive %s: %w", v.ID, v.DriveID, err)
		}
	}
	drv, ok := a.registry.Get(v.DriveID)
	if !ok {
		return false, fmt.Errorf("remove video source %s: drive %s not attached: %w", v.ID, v.DriveID, drives.ErrNotSupported)
	}
	if sourceRemover, ok := drv.(drives.SourceRemover); ok {
		if err := sourceRemover.RemoveSource(ctx, drives.SourceFile{
			FileID:   fileID,
			ParentID: strings.TrimSpace(v.ParentID),
			Name:     strings.TrimSpace(v.FileName),
			Size:     v.Size,
		}); err != nil {
			return false, fmt.Errorf("remove video source %s from drive %s: %w", v.ID, v.DriveID, err)
		}
		return true, nil
	}
	remover, ok := drv.(drives.Remover)
	if !ok {
		return false, fmt.Errorf("remove video source %s: drive %s (%s) does not support source deletion: %w", v.ID, v.DriveID, drv.Kind(), drives.ErrNotSupported)
	}
	if err := remover.Remove(ctx, fileID); err != nil {
		return false, fmt.Errorf("remove video source %s from drive %s: %w", v.ID, v.DriveID, err)
	}
	return true, nil
}

func (a *App) cleanupDriveVideosForDelete(ctx context.Context, driveID string) (int, error) {
	if a == nil || a.cat == nil {
		return 0, nil
	}
	d, err := a.cat.GetDrive(ctx, driveID)
	if err != nil {
		return 0, err
	}

	// Stop generation/crawl workers before deleting assets so they do not keep
	// writing files for a drive that is being removed.
	a.detachDrive(driveID)

	items, err := a.videosForDriveDelete(ctx, d)
	if err != nil {
		return 0, err
	}

	localDir := ""
	if a.cfg != nil {
		localDir = a.cfg.Storage.LocalPreviewDir
	}
	for _, v := range items {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := removeLocalVideoAssets(localDir, v); err != nil {
			return 0, fmt.Errorf("remove local assets for %s: %w", v.ID, err)
		}
	}

	removed := 0
	for _, v := range items {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if err := a.cat.DeleteVideo(ctx, v.ID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return removed, fmt.Errorf("delete catalog video %s: %w", v.ID, err)
		}
		removed++
	}
	return removed, nil
}

func (a *App) cleanupOrphanDriveVideos(ctx context.Context) (int, error) {
	if a == nil || a.cat == nil {
		return 0, nil
	}
	items, err := a.cat.ListVideosWithMissingDrive(ctx)
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		return 0, nil
	}

	localDir := ""
	if a.cfg != nil {
		localDir = a.cfg.Storage.LocalPreviewDir
	}
	for _, v := range items {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := removeLocalVideoAssets(localDir, v); err != nil {
			return 0, fmt.Errorf("remove local assets for orphan %s: %w", v.ID, err)
		}
	}

	removed := 0
	for _, v := range items {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if err := a.cat.DeleteVideo(ctx, v.ID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return removed, fmt.Errorf("delete orphan catalog video %s: %w", v.ID, err)
		}
		removed++
	}
	return removed, nil
}

func (a *App) videosForDriveDelete(ctx context.Context, d *catalog.Drive) ([]*catalog.Video, error) {
	if d == nil {
		return nil, nil
	}
	items, err := a.cat.ListVideosByDrive(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*catalog.Video, len(items))
	for _, v := range items {
		byID[v.ID] = v
	}

	out := make([]*catalog.Video, 0, len(byID))
	for _, v := range byID {
		out = append(out, v)
	}
	return out, nil
}

func removeLocalVideoAssets(localDir string, v *catalog.Video) error {
	if localDir == "" || v == nil || v.ID == "" {
		return nil
	}
	candidates := []string{
		v.PreviewLocal,
	}
	candidates = append(candidates, mediaasset.PreviewPathCandidates(localDir, v.ID)...)
	candidates = append(candidates, mediaasset.ThumbnailPathCandidates(localDir, v.ID)...)
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		clean, ok := localPathWithin(localDir, candidate)
		if !ok {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		info, err := os.Stat(clean)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := os.Remove(clean); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

type duplicateVideoMaintenanceStats struct {
	VideosScanned       int
	ExactGroups         int
	ExactDeleted        int
	NearCandidates      int
	NearSSIMComparisons int
	NearGroups          int
	NearDeleted         int
}

type nearDuplicateMaintenanceStats struct {
	Candidates      int
	SSIMComparisons int
	Groups          int
	Deleted         int
}

type videoMaintenanceCandidate struct {
	video         *catalog.Video
	thumbnailPath string
	assetScore    int
	titleKeys     []string
	titleQGrams   map[string]struct{}
	titleBuckets  []string
}

type fingerprintDuplicateKey struct {
	size    int64
	sampled string
}

func (a *App) cleanupDuplicateVideoAssets(ctx context.Context) error {
	if a == nil || a.cat == nil {
		return nil
	}
	localDir := ""
	if a.cfg != nil {
		localDir = strings.TrimSpace(a.cfg.Storage.LocalPreviewDir)
	}
	videos, err := a.cat.ListVideoMaintenanceCandidates(ctx)
	if err != nil {
		return err
	}
	stats := duplicateVideoMaintenanceStats{VideosScanned: len(videos)}
	if len(videos) == 0 {
		log.Printf("[dedupe-maintenance] no videos to maintain")
		return nil
	}

	deleted := make(map[string]struct{})
	exactGroups, exactDeleted, err := a.cleanupExactDuplicateVideos(ctx, localDir, videos, deleted)
	if err != nil {
		return err
	}
	stats.ExactGroups = exactGroups
	stats.ExactDeleted = exactDeleted

	remaining := make([]*catalog.Video, 0, len(videos)-len(deleted))
	for _, v := range videos {
		if v == nil {
			continue
		}
		if _, ok := deleted[v.ID]; ok {
			continue
		}
		remaining = append(remaining, v)
	}
	nearStats, err := a.cleanupNearDuplicateVideos(ctx, localDir, remaining, deleted)
	if err != nil {
		return err
	}
	stats.NearCandidates = nearStats.Candidates
	stats.NearSSIMComparisons = nearStats.SSIMComparisons
	stats.NearGroups = nearStats.Groups
	stats.NearDeleted = nearStats.Deleted

	log.Printf("[dedupe-maintenance] videos=%d exact_groups=%d exact_deleted=%d near_candidates=%d near_ssim_comparisons=%d near_groups=%d near_deleted=%d",
		stats.VideosScanned, stats.ExactGroups, stats.ExactDeleted, stats.NearCandidates, stats.NearSSIMComparisons, stats.NearGroups, stats.NearDeleted)
	return nil
}

func (a *App) cleanupExactDuplicateVideos(ctx context.Context, localDir string, videos []*catalog.Video, deleted map[string]struct{}) (int, int, error) {
	groups := make(map[fingerprintDuplicateKey][]*catalog.Video)
	for _, v := range videos {
		if v == nil {
			continue
		}
		if _, ok := deleted[v.ID]; ok {
			continue
		}
		sampled := strings.ToLower(strings.TrimSpace(v.SampledSHA256))
		if v.Size <= 0 || sampled == "" {
			continue
		}
		key := fingerprintDuplicateKey{size: v.Size, sampled: sampled}
		groups[key] = append(groups[key], v)
	}

	keys := make([]fingerprintDuplicateKey, 0, len(groups))
	for key := range groups {
		if len(groups[key]) > 1 {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].size != keys[j].size {
			return keys[i].size < keys[j].size
		}
		return keys[i].sampled < keys[j].sampled
	})

	groupCount := 0
	deletedCount := 0
	for _, key := range keys {
		group := groups[key]
		canonical := selectExactDuplicateCanonical(localDir, group)
		if canonical == nil {
			continue
		}
		groupCount++
		for _, v := range group {
			if v == nil || v.ID == canonical.ID {
				continue
			}
			if _, ok := deleted[v.ID]; ok {
				continue
			}
			if err := a.deleteDuplicateVideoWithAssets(ctx, localDir, v, canonical.ID); err != nil {
				return groupCount, deletedCount, fmt.Errorf("exact duplicate size=%d sampled=%s: %w", key.size, shortHashForLog(key.sampled), err)
			}
			deleted[v.ID] = struct{}{}
			deletedCount++
			log.Printf("[dedupe-maintenance] exact duplicate deleted id=%s canonical=%s size=%d sampled=%s", v.ID, canonical.ID, key.size, shortHashForLog(key.sampled))
		}
	}
	return groupCount, deletedCount, nil
}

func (a *App) cleanupNearDuplicateVideos(ctx context.Context, localDir string, videos []*catalog.Video, deleted map[string]struct{}) (nearDuplicateMaintenanceStats, error) {
	candidates := collectNearDuplicateMaintenanceCandidates(localDir, videos, deleted)
	stats := nearDuplicateMaintenanceStats{Candidates: len(candidates)}
	if len(candidates) < 2 {
		return stats, nil
	}

	sets := newVideoMaintenanceDisjointSet(len(candidates))
	bucketIndex := make(map[int]map[string][]int)
	seenPairs := make(map[uint64]struct{})
	for i, right := range candidates {
		if right.video == nil {
			continue
		}
		for duration := right.video.DurationSeconds - videoMaintenanceDurationToleranceSeconds; duration <= right.video.DurationSeconds+videoMaintenanceDurationToleranceSeconds; duration++ {
			byBucket := bucketIndex[duration]
			if len(byBucket) == 0 {
				continue
			}
			for _, bucket := range right.titleBuckets {
				for _, j := range byBucket[bucket] {
					if j == i {
						continue
					}
					pairKey := videoMaintenancePairKey(i, j)
					if _, ok := seenPairs[pairKey]; ok {
						continue
					}
					seenPairs[pairKey] = struct{}{}
					left := candidates[j]
					if left.video == nil {
						continue
					}
					if !nearDuplicateTitlePrefilter(left, right) {
						continue
					}
					titleScore := mediasim.TitleSimilarity(left.video.Title, right.video.Title)
					if titleScore < videoMaintenanceTitleThreshold {
						continue
					}
					stats.SSIMComparisons++
					ssimScore, err := mediasim.ImageSSIM(left.thumbnailPath, right.thumbnailPath)
					if err != nil {
						log.Printf("[dedupe-maintenance] thumbnail ssim failed left=%s right=%s: %v", left.video.ID, right.video.ID, err)
						continue
					}
					if ssimScore >= videoMaintenanceSSIMThreshold {
						sets.union(i, j)
					}
				}
			}
		}
		if len(right.titleBuckets) == 0 {
			continue
		}
		byBucket := bucketIndex[right.video.DurationSeconds]
		if byBucket == nil {
			byBucket = make(map[string][]int)
			bucketIndex[right.video.DurationSeconds] = byBucket
		}
		for _, bucket := range right.titleBuckets {
			byBucket[bucket] = append(byBucket[bucket], i)
		}
	}

	groups := make(map[int][]videoMaintenanceCandidate)
	for i, candidate := range candidates {
		root := sets.find(i)
		groups[root] = append(groups[root], candidate)
	}
	roots := make([]int, 0, len(groups))
	for root, group := range groups {
		if len(group) > 1 {
			roots = append(roots, root)
		}
	}
	sort.Ints(roots)

	for _, root := range roots {
		group := groups[root]
		canonical := selectNearDuplicateCanonical(group)
		if canonical.video == nil {
			continue
		}
		stats.Groups++
		for _, candidate := range group {
			v := candidate.video
			if v == nil || v.ID == canonical.video.ID {
				continue
			}
			if _, ok := deleted[v.ID]; ok {
				continue
			}
			if err := a.deleteDuplicateVideoWithAssets(ctx, localDir, v, canonical.video.ID); err != nil {
				return stats, fmt.Errorf("near duplicate canonical=%s duplicate=%s: %w", canonical.video.ID, v.ID, err)
			}
			deleted[v.ID] = struct{}{}
			stats.Deleted++
			log.Printf("[dedupe-maintenance] near duplicate deleted id=%s canonical=%s size=%d canonical_size=%d duration=%d title=%q",
				v.ID, canonical.video.ID, v.Size, canonical.video.Size, v.DurationSeconds, v.Title)
		}
	}
	return stats, nil
}

func collectNearDuplicateMaintenanceCandidates(localDir string, videos []*catalog.Video, deleted map[string]struct{}) []videoMaintenanceCandidate {
	localDir = strings.TrimSpace(localDir)
	if localDir == "" {
		return nil
	}
	out := make([]videoMaintenanceCandidate, 0, len(videos))
	for _, v := range videos {
		if v == nil || strings.TrimSpace(v.ID) == "" {
			continue
		}
		if _, ok := deleted[v.ID]; ok {
			continue
		}
		if strings.TrimSpace(v.Title) == "" || v.DurationSeconds <= 0 {
			continue
		}
		titleKeys := mediasim.TitleKeys(v.Title)
		if len(titleKeys) == 0 {
			continue
		}
		titleBuckets := titlePrefixBuckets(titleKeys, 12)
		if len(titleBuckets) == 0 {
			continue
		}
		thumbPath, ok := localGeneratedThumbnailPath(localDir, v)
		if !ok {
			continue
		}
		out = append(out, videoMaintenanceCandidate{
			video:         v,
			thumbnailPath: thumbPath,
			assetScore:    videoAssetCompletenessScore(localDir, v),
			titleKeys:     titleKeys,
			titleQGrams:   titleQGrams(titleKeys, 4),
			titleBuckets:  titleBuckets,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		left := out[i].video
		right := out[j].video
		if left.DurationSeconds != right.DurationSeconds {
			return left.DurationSeconds < right.DurationSeconds
		}
		return earlierVideo(left, right)
	})
	return out
}

func nearDuplicateTitlePrefilter(left, right videoMaintenanceCandidate) bool {
	if !titleLengthCouldReachThreshold(left.titleKeys, right.titleKeys, videoMaintenanceTitleThreshold) {
		return false
	}
	return qGramContainment(left.titleQGrams, right.titleQGrams) >= 0.45
}

func videoMaintenancePairKey(left, right int) uint64 {
	if left > right {
		left, right = right, left
	}
	return uint64(uint32(left))<<32 | uint64(uint32(right))
}

func titlePrefixBuckets(keys []string, prefixRunes int) []string {
	if prefixRunes <= 0 {
		prefixRunes = 12
	}
	seen := make(map[string]struct{})
	var out []string
	var fallback []string
	for _, key := range keys {
		runes := []rune(key)
		if len(runes) == 0 {
			continue
		}
		if len(runes) > prefixRunes {
			runes = runes[:prefixRunes]
		}
		bucket := string(runes)
		if _, ok := seen[bucket]; ok {
			continue
		}
		seen[bucket] = struct{}{}
		if lowInformationTitleBucket(bucket) {
			fallback = append(fallback, bucket)
			continue
		}
		out = append(out, bucket)
	}
	if len(out) > 0 {
		return out
	}
	return fallback
}

func lowInformationTitleBucket(bucket string) bool {
	if strings.HasPrefix(bucket, "www") {
		return true
	}
	if strings.Contains(bucket, "com") {
		limit := len(bucket)
		if limit > 8 {
			limit = 8
		}
		for _, r := range bucket[:limit] {
			if r >= '0' && r <= '9' {
				return true
			}
		}
	}
	return false
}

func titleLengthCouldReachThreshold(leftKeys, rightKeys []string, threshold float64) bool {
	for _, left := range leftKeys {
		leftLen := len([]rune(left))
		if leftLen == 0 {
			continue
		}
		for _, right := range rightKeys {
			rightLen := len([]rune(right))
			if rightLen == 0 {
				continue
			}
			maxLen := leftLen
			minLen := rightLen
			if rightLen > maxLen {
				maxLen = rightLen
				minLen = leftLen
			}
			if float64(minLen)/float64(maxLen) >= threshold {
				return true
			}
		}
	}
	return false
}

func titleQGrams(keys []string, n int) map[string]struct{} {
	out := make(map[string]struct{})
	if n <= 0 {
		n = 4
	}
	for _, key := range keys {
		runes := []rune(key)
		if len(runes) == 0 {
			continue
		}
		if len(runes) <= n {
			out[string(runes)] = struct{}{}
			continue
		}
		for i := 0; i+n <= len(runes); i++ {
			out[string(runes[i:i+n])] = struct{}{}
		}
	}
	return out
}

func qGramContainment(left, right map[string]struct{}) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	smaller := left
	larger := right
	if len(right) < len(left) {
		smaller = right
		larger = left
	}
	common := 0
	for gram := range smaller {
		if _, ok := larger[gram]; ok {
			common++
		}
	}
	return float64(common) / float64(len(smaller))
}

func selectExactDuplicateCanonical(localDir string, group []*catalog.Video) *catalog.Video {
	var best *catalog.Video
	for _, v := range group {
		if v == nil {
			continue
		}
		if best == nil || betterExactDuplicateCanonical(localDir, v, best) {
			best = v
		}
	}
	return best
}

func betterExactDuplicateCanonical(localDir string, left, right *catalog.Video) bool {
	leftScore := videoAssetCompletenessScore(localDir, left)
	rightScore := videoAssetCompletenessScore(localDir, right)
	if leftScore != rightScore {
		return leftScore > rightScore
	}
	return earlierVideo(left, right)
}

func selectNearDuplicateCanonical(group []videoMaintenanceCandidate) videoMaintenanceCandidate {
	var best videoMaintenanceCandidate
	for _, candidate := range group {
		if candidate.video == nil {
			continue
		}
		if best.video == nil || betterNearDuplicateCanonical(candidate, best) {
			best = candidate
		}
	}
	return best
}

func betterNearDuplicateCanonical(left, right videoMaintenanceCandidate) bool {
	if right.video == nil {
		return true
	}
	if left.video == nil {
		return false
	}
	if left.video.Size != right.video.Size {
		return left.video.Size > right.video.Size
	}
	if left.assetScore != right.assetScore {
		return left.assetScore > right.assetScore
	}
	return earlierVideo(left.video, right.video)
}

func earlierVideo(left, right *catalog.Video) bool {
	if right == nil {
		return true
	}
	if left == nil {
		return false
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	return left.ID < right.ID
}

func videoAssetCompletenessScore(localDir string, v *catalog.Video) int {
	if v == nil {
		return 0
	}
	score := 0
	if localGeneratedPreviewReady(localDir, v) {
		score++
	}
	if _, ok := localGeneratedThumbnailPath(localDir, v); ok {
		score++
	}
	if strings.TrimSpace(v.SampledSHA256) != "" && strings.TrimSpace(v.FingerprintStatus) == "ready" {
		score++
	}
	return score
}

func localGeneratedPreviewReady(localDir string, v *catalog.Video) bool {
	if v == nil || strings.TrimSpace(v.PreviewStatus) != "ready" || strings.TrimSpace(v.PreviewLocal) == "" {
		return false
	}
	localDir = strings.TrimSpace(localDir)
	if localDir == "" {
		return true
	}
	clean, ok := localPathWithin(localDir, v.PreviewLocal)
	if !ok {
		return false
	}
	return regularFileExists(clean)
}

func localGeneratedThumbnailPath(localDir string, v *catalog.Video) (string, bool) {
	if v == nil || strings.TrimSpace(localDir) == "" || strings.TrimSpace(v.ID) == "" {
		return "", false
	}
	if strings.TrimSpace(v.ThumbnailURL) != "/p/thumb/"+v.ID {
		return "", false
	}
	for _, candidate := range mediaasset.ThumbnailPathCandidates(localDir, v.ID) {
		clean, ok := localPathWithin(localDir, candidate)
		if !ok {
			continue
		}
		if regularFileExists(clean) {
			return clean, true
		}
	}
	return "", false
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (a *App) deleteDuplicateVideoWithAssets(ctx context.Context, localDir string, v *catalog.Video, canonicalID string) error {
	if v == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := removeLocalVideoAssets(localDir, v); err != nil {
		return fmt.Errorf("remove local assets for %s: %w", v.ID, err)
	}
	var lastErr error
	for attempt := 0; attempt < 12; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.cat.DeleteVideoWithTombstoneOptions(ctx, v.ID, catalog.DeleteVideoTombstoneOptions{
			Reason:           catalog.DeletedVideoReasonDuplicate,
			CanonicalVideoID: canonicalID,
		}); err != nil {
			if !isSQLiteBusyError(err) {
				return fmt.Errorf("delete catalog video %s canonical=%s: %w", v.ID, canonicalID, err)
			}
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * 250 * time.Millisecond)
			continue
		}
		return nil
	}
	return fmt.Errorf("delete catalog video %s canonical=%s after retries: %w", v.ID, canonicalID, lastErr)
}

func isSQLiteBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "sqlite_locked") ||
		strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked")
}

func shortHashForLog(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

type videoMaintenanceDisjointSet struct {
	parent []int
	rank   []int
}

func newVideoMaintenanceDisjointSet(n int) *videoMaintenanceDisjointSet {
	parent := make([]int, n)
	rank := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	return &videoMaintenanceDisjointSet{parent: parent, rank: rank}
}

func (s *videoMaintenanceDisjointSet) find(x int) int {
	if s.parent[x] != x {
		s.parent[x] = s.find(s.parent[x])
	}
	return s.parent[x]
}

func (s *videoMaintenanceDisjointSet) union(a, b int) {
	rootA := s.find(a)
	rootB := s.find(b)
	if rootA == rootB {
		return
	}
	if s.rank[rootA] < s.rank[rootB] {
		s.parent[rootA] = rootB
		return
	}
	if s.rank[rootA] > s.rank[rootB] {
		s.parent[rootB] = rootA
		return
	}
	s.parent[rootB] = rootA
	s.rank[rootA]++
}

func cleanupDuplicatePreviewAsset(localDir, previewLocal string) (clear bool, removed bool, missing bool, skippedUnsafe bool, err error) {
	clean, ok := localPathWithin(localDir, previewLocal)
	if !ok {
		if strings.TrimSpace(previewLocal) != "" {
			return false, false, false, true, nil
		}
		return false, false, false, false, nil
	}
	removed, missing, err = removeRegularFileIfExists(clean)
	if err != nil {
		return false, false, false, false, err
	}
	return true, removed, missing, false, nil
}

func cleanupDuplicateThumbnailAsset(localDir, videoID, thumbnailURL string) (clear bool, removed bool, missing bool, err error) {
	if thumbnailURL != "/p/thumb/"+videoID {
		return false, false, false, nil
	}
	candidates := mediaasset.ThumbnailPathCandidates(localDir, videoID)
	seen := make(map[string]struct{}, len(candidates))
	anyChecked := false
	allMissing := true
	for _, candidate := range candidates {
		clean, ok := localPathWithin(localDir, candidate)
		if !ok {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		anyChecked = true
		removedOne, missingOne, removeErr := removeRegularFileIfExists(clean)
		if removeErr != nil {
			return false, false, false, removeErr
		}
		if removedOne {
			removed = true
		}
		if !missingOne {
			allMissing = false
		}
	}
	if !anyChecked {
		return false, false, false, nil
	}
	missing = allMissing && !removed
	return true, removed, missing, nil
}

func removeRegularFileIfExists(path string) (removed bool, missing bool, err error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, true, nil
		}
		return false, false, err
	}
	if !info.Mode().IsRegular() {
		return false, false, nil
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return false, true, nil
		}
		return false, false, err
	}
	return true, false, nil
}

func localPathWithin(root, path string) (string, bool) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(path) == "" {
		return "", false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return pathAbs, true
}

func (a *App) enqueueUploadedVideo(ctx context.Context, v *catalog.Video) {
	if v == nil {
		return
	}
	a.mu.Lock()
	worker := a.workers[v.DriveID]
	thumbWorker := a.thumbWorkers[v.DriveID]
	fingerprintWorker := a.fingerprintWorkers[v.DriveID]
	a.mu.Unlock()

	if thumbWorker != nil && v.ThumbnailURL == "" {
		thumbWorker.Enqueue(v)
	}
	if worker != nil && a.teaserEnabledForDrive(ctx, v.DriveID) {
		worker.Enqueue(v)
	}
	if fingerprintWorker != nil {
		fingerprintWorker.Enqueue(v)
	}
}

func (a *App) regenPreview(ctx context.Context, videoID string) {
	v, err := a.cat.GetVideo(ctx, videoID)
	if err != nil {
		return
	}
	taskCtx, done := a.registerDriveTaskContext(ctx, v.DriveID)
	defer done()
	a.mu.Lock()
	worker := a.workers[v.DriveID]
	a.mu.Unlock()
	if worker != nil {
		worker.EnqueueBlocking(taskCtx, v)
	}
}

func (a *App) regenAllPreviews(ctx context.Context) {
	items, total, err := a.cat.ListVideos(ctx, catalog.ListParams{Page: 1, PageSize: 1000000})
	if err != nil {
		log.Printf("[preview] list all videos for regen: %v", err)
		return
	}
	log.Printf("[preview] enqueue all visible videos for regen count=%d total=%d", len(items), total)
	queued := 0
	for _, v := range items {
		if err := ctx.Err(); err != nil {
			log.Printf("[preview] enqueue all canceled after %d videos: %v", queued, err)
			return
		}
		a.mu.Lock()
		worker := a.workers[v.DriveID]
		a.mu.Unlock()
		if worker == nil {
			continue
		}
		if !worker.EnqueueBlocking(ctx, v) {
			log.Printf("[preview] enqueue all canceled after %d videos", queued)
			return
		}
		queued++
	}
	log.Printf("[preview] enqueued all visible videos for regen queued=%d", queued)
}

func (a *App) regenFailedPreviews(ctx context.Context, driveID string) {
	taskCtx, done := a.registerDriveTaskContext(ctx, driveID)
	defer done()
	failed, err := a.cat.ListVideosByPreviewStatus(taskCtx, driveID, "failed", 0)
	if err != nil {
		log.Printf("[preview] list failed videos for regen drive=%s: %v", driveID, err)
		return
	}
	a.mu.Lock()
	worker := a.workers[driveID]
	a.mu.Unlock()
	if worker == nil {
		log.Printf("[preview] regen failed drive=%s skipped: worker not found", driveID)
		return
	}
	reset := 0
	for _, v := range failed {
		if err := taskCtx.Err(); err != nil {
			log.Printf("[preview] reset failed canceled drive=%s reset=%d: %v", driveID, reset, err)
			return
		}
		if err := a.cat.UpdatePreview(taskCtx, v.ID, "", "pending"); err != nil {
			log.Printf("[preview] reset failed video %s drive=%s: %v", v.ID, driveID, err)
			continue
		}
		reset++
	}
	items, err := a.cat.ListVideosByPreviewStatus(taskCtx, driveID, "pending", 0)
	if err != nil {
		log.Printf("[preview] list pending videos for regen drive=%s: %v", driveID, err)
		return
	}
	log.Printf("[preview] enqueue pending videos for regen drive=%s count=%d reset_failed=%d", driveID, len(items), reset)
	queued := 0
	for _, v := range items {
		if err := taskCtx.Err(); err != nil {
			log.Printf("[preview] enqueue pending canceled drive=%s queued=%d: %v", driveID, queued, err)
			return
		}
		if !worker.EnqueueBlocking(taskCtx, v) {
			log.Printf("[preview] enqueue pending canceled drive=%s queued=%d", driveID, queued)
			return
		}
		queued++
	}
	log.Printf("[preview] enqueued pending videos for regen drive=%s queued=%d reset_failed=%d", driveID, queued, reset)
}

// regenFailedThumbnails 把某 drive 下 thumbnail_status=failed 的视频全部重置为
// pending 并重新入队封面 worker。与 regenFailedPreviews 行为对称：那条管预览视频，
// 这条管封面图（两个 worker 是独立队列）。
//
// 操作不会触发已生成失败的视频重新去网盘取流 —— 只是把 catalog 的状态翻到 pending
// 并入队；真正的取链 / ffmpeg 在 thumb worker 里执行。
func (a *App) regenFailedThumbnails(ctx context.Context, driveID string) {
	taskCtx, done := a.registerDriveTaskContext(ctx, driveID)
	defer done()
	failed, err := a.cat.ListVideosByThumbnailStatus(taskCtx, driveID, "failed", 0)
	if err != nil {
		log.Printf("[thumb] list failed videos for regen drive=%s: %v", driveID, err)
		return
	}
	a.mu.Lock()
	thumbWorker := a.thumbWorkers[driveID]
	a.mu.Unlock()
	if thumbWorker == nil {
		log.Printf("[thumb] regen failed drive=%s skipped: thumb worker not found", driveID)
		return
	}
	reset := 0
	for _, v := range failed {
		if err := taskCtx.Err(); err != nil {
			log.Printf("[thumb] reset failed canceled drive=%s reset=%d: %v", driveID, reset, err)
			return
		}
		// 状态翻 pending；保留 thumbnail_url 字段（thumb worker 先看 url 是否已写
		// 来判断是否真的要再生）。但既然之前是 failed 说明 url 没写过，所以这里
		// 把 url 一并清空更稳。
		if err := a.cat.UpdateVideoMeta(taskCtx, v.ID, catalog.VideoMetaPatch{
			ThumbnailURL:           "",
			ThumbnailStatus:        "pending",
			ResetThumbnailFailures: true,
		}); err != nil {
			log.Printf("[thumb] reset failed video %s drive=%s: %v", v.ID, driveID, err)
			continue
		}
		reset++
	}
	items, err := a.cat.ListVideosNeedingThumbnail(taskCtx, driveID, 0)
	if err != nil {
		log.Printf("[thumb] list pending thumbnails for regen drive=%s: %v", driveID, err)
		return
	}
	log.Printf("[thumb] enqueue pending thumbnails for regen drive=%s count=%d reset_failed=%d", driveID, len(items), reset)
	queued := 0
	for _, v := range items {
		if err := taskCtx.Err(); err != nil {
			log.Printf("[thumb] enqueue pending canceled drive=%s queued=%d: %v", driveID, queued, err)
			return
		}
		if !thumbWorker.EnqueueBlocking(taskCtx, v) {
			log.Printf("[thumb] enqueue pending canceled drive=%s queued=%d", driveID, queued)
			return
		}
		queued++
	}
	log.Printf("[thumb] enqueued pending thumbnails for regen drive=%s queued=%d reset_failed=%d", driveID, queued, reset)
}

func (a *App) regenFailedFingerprints(ctx context.Context, driveID string) {
	taskCtx, done := a.registerDriveTaskContext(ctx, driveID)
	defer done()
	failed, err := a.cat.ListVideosByFingerprintStatus(taskCtx, driveID, "failed", 0)
	if err != nil {
		log.Printf("[fingerprint] list failed videos for regen drive=%s: %v", driveID, err)
		return
	}
	a.mu.Lock()
	fingerprintWorker := a.fingerprintWorkers[driveID]
	a.mu.Unlock()
	if fingerprintWorker == nil {
		log.Printf("[fingerprint] regen failed drive=%s skipped: fingerprint worker not found", driveID)
		return
	}
	reset := 0
	for _, v := range failed {
		if err := taskCtx.Err(); err != nil {
			log.Printf("[fingerprint] reset failed canceled drive=%s reset=%d: %v", driveID, reset, err)
			return
		}
		if err := a.cat.UpdateVideoFingerprint(taskCtx, v.ID, "", "pending", ""); err != nil {
			log.Printf("[fingerprint] reset failed video %s drive=%s: %v", v.ID, driveID, err)
			continue
		}
		reset++
	}
	items, err := a.cat.ListVideosNeedingFingerprint(taskCtx, driveID, 0)
	if err != nil {
		log.Printf("[fingerprint] list pending videos for regen drive=%s: %v", driveID, err)
		return
	}
	log.Printf("[fingerprint] enqueue pending videos for regen drive=%s count=%d reset_failed=%d", driveID, len(items), reset)
	queued := 0
	for _, v := range items {
		if err := taskCtx.Err(); err != nil {
			log.Printf("[fingerprint] enqueue pending canceled drive=%s queued=%d: %v", driveID, queued, err)
			return
		}
		if !fingerprintWorker.EnqueueBlocking(taskCtx, v) {
			log.Printf("[fingerprint] enqueue pending canceled drive=%s queued=%d", driveID, queued)
			return
		}
		queued++
	}
	log.Printf("[fingerprint] enqueued pending videos for regen drive=%s queued=%d reset_failed=%d", driveID, queued, reset)
}

// listScanTargetIDs 返回 nightly Phase 1 应扫描的所有 drive ID
// （非爬虫、非 localupload）。它直接读 catalog，而不是 registry，这样
// 进程刚启动、云盘还在后台挂载时，nightly 也不会漏掉配置过的 drive。
func (a *App) listScanTargetIDs(ctx context.Context) []string {
	all, err := a.cat.ListDrives(ctx)
	if err != nil {
		log.Printf("[nightly] list scan target drives: %v", err)
		return nil
	}
	out := make([]string, 0, len(all))
	for _, d := range all {
		if d == nil || d.ID == localupload.DriveID || d.Kind == scriptcrawler.Kind {
			continue
		}
		out = append(out, d.ID)
	}
	return out
}

// listCrawlerDriveIDs 返回 nightly Phase 2 应触发爬取的爬虫 drive ID 列表。
func (a *App) listCrawlerDriveIDs(ctx context.Context) []string {
	all, err := a.cat.ListDrives(ctx)
	if err != nil {
		log.Printf("[nightly] list crawler drives: %v", err)
		return nil
	}
	out := make([]string, 0, len(all))
	for _, d := range all {
		if d != nil && d.Kind == scriptcrawler.Kind && strings.TrimSpace(d.Credentials["script_path"]) != "" {
			out = append(out, d.ID)
		}
	}
	return out
}

// waitAllPreviewQueuesIdle 阻塞直到所有 drive 的封面、预览视频和指纹 worker
// 队列都为空且无 in-flight 任务。
//
// 顺序：先等所有 thumb worker，再等预览视频，最后等指纹。队列生成时互不等待；
// nightly 只在 phase 边界统一等待它们都 drain，保证爬虫视频迁移前本地资产已产出。
// 若 ctx 在等待中被取消（shutdown / 管理员停止），立即返回 ctx.Err。
func (a *App) waitAllPreviewQueuesIdle(ctx context.Context) error {
	a.mu.Lock()
	thumbWorkers := make([]*preview.ThumbWorker, 0, len(a.thumbWorkers))
	previewWorkers := make([]*preview.Worker, 0, len(a.workers))
	fingerprintWorkers := make([]*fingerprint.Worker, 0, len(a.fingerprintWorkers))
	for _, w := range a.thumbWorkers {
		thumbWorkers = append(thumbWorkers, w)
	}
	for _, w := range a.workers {
		previewWorkers = append(previewWorkers, w)
	}
	for _, w := range a.fingerprintWorkers {
		fingerprintWorkers = append(fingerprintWorkers, w)
	}
	a.mu.Unlock()

	for _, w := range thumbWorkers {
		if err := w.WaitIdle(ctx); err != nil {
			return err
		}
	}
	for _, w := range previewWorkers {
		if err := w.WaitIdle(ctx); err != nil {
			return err
		}
	}
	if err := a.waitFingerprintQueueingIdle(ctx, ""); err != nil {
		return err
	}
	for _, w := range fingerprintWorkers {
		if err := w.WaitIdle(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) waitDriveGenerationQueuesIdle(ctx context.Context, driveID string) error {
	a.mu.Lock()
	thumbWorker := a.thumbWorkers[driveID]
	previewWorker := a.workers[driveID]
	fingerprintWorker := a.fingerprintWorkers[driveID]
	a.mu.Unlock()
	if err := thumbWorker.WaitIdle(ctx); err != nil {
		return err
	}
	if err := previewWorker.WaitIdle(ctx); err != nil {
		return err
	}
	if err := a.waitFingerprintQueueingIdle(ctx, driveID); err != nil {
		return err
	}
	if err := fingerprintWorker.WaitIdle(ctx); err != nil {
		return err
	}
	return nil
}

func (a *App) waitFingerprintQueueingIdle(ctx context.Context, driveID string) error {
	if !a.fingerprintQueueingBusy(driveID) {
		return nil
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !a.fingerprintQueueingBusy(driveID) {
				return nil
			}
		}
	}
}

func (a *App) fingerprintQueueingBusy(driveID string) bool {
	a.fingerprintQueueMu.Lock()
	defer a.fingerprintQueueMu.Unlock()
	if driveID != "" {
		return a.fingerprintQueueing[driveID]
	}
	return len(a.fingerprintQueueing) > 0
}

func shouldScanDrive(d drives.Drive) bool {
	if d == nil || d.ID() == localupload.DriveID {
		return false
	}
	// 爬虫类 drive 由专用 crawl 阶段触发，不参与普通 scan
	if d.Kind() == scriptcrawler.Kind {
		return false
	}
	return true
}

// ---------- script crawler crawl ----------

func (a *App) scheduleScriptCrawlerCrawl(ctx context.Context, driveID string) bool {
	if a.driveHasActiveWork(driveID) {
		log.Printf("[scriptcrawler] drive=%s has active work, skip duplicate crawl request", driveID)
		return false
	}
	if !a.beginDriveScanOrCrawl(driveID) {
		log.Printf("[scriptcrawler] drive=%s already queued or running, skip duplicate crawl request", driveID)
		return false
	}
	taskCtx, done := a.registerDriveTaskContext(ctx, driveID)

	go func() {
		defer func() {
			a.endDriveScanOrCrawl(driveID)
			done()
		}()
		if a.runScriptCrawlerCrawlWithTaskContext(taskCtx, driveID) {
			a.runCrawlerMigrationAfterManualCrawl(taskCtx, driveID)
		}
	}()
	return true
}

func (a *App) runScriptCrawlerCrawl(ctx context.Context, driveID string) {
	if !a.beginDriveScanOrCrawl(driveID) {
		log.Printf("[scriptcrawler] drive=%s already queued or running, skip direct crawl", driveID)
		return
	}
	defer a.endDriveScanOrCrawl(driveID)
	taskCtx, done := a.registerDriveTaskContext(ctx, driveID)
	defer done()
	a.runScriptCrawlerCrawlWithTaskContext(taskCtx, driveID)
}

func (a *App) runScriptCrawlerCrawlWithTaskContext(ctx context.Context, driveID string) bool {
	if err := ctx.Err(); err != nil {
		log.Printf("[scriptcrawler] drive=%s crawl canceled before start: %v", driveID, err)
		return false
	}
	a.mu.Lock()
	c := a.scriptCrawlers[driveID]
	a.mu.Unlock()
	if c == nil {
		if err := a.ensureDriveAttached(ctx, driveID); err != nil {
			log.Printf("[scriptcrawler] drive=%s attach failed: %v", driveID, err)
			return false
		}
		a.mu.Lock()
		c = a.scriptCrawlers[driveID]
		a.mu.Unlock()
		if c == nil {
			log.Printf("[scriptcrawler] drive=%s crawler not attached", driveID)
			return false
		}
	}

	d, err := a.cat.GetDrive(ctx, driveID)
	if err != nil || d == nil {
		log.Printf("[scriptcrawler] drive=%s lookup failed: %v", driveID, err)
		return false
	}
	targetNew := crawlerIntCred(d, "target_new", scriptcrawler.DefaultTargetNew)
	if targetNew <= 0 {
		targetNew = scriptcrawler.DefaultTargetNew
	}

	log.Printf("[scriptcrawler] drive=%s start crawl target_new=%d", driveID, targetNew)
	res, runErr := c.RunOnce(ctx, targetNew)
	if runErr != nil {
		log.Printf("[scriptcrawler] drive=%s crawl failed: %v", driveID, runErr)
	} else if res != nil {
		log.Printf("[scriptcrawler] drive=%s crawl done target=%d candidate_budget=%d total=%d new=%d skipped=%d failed=%d seen_snapshot=%d",
			driveID, res.TargetNew, res.CandidateBudget, res.TotalEntries, res.NewVideos, res.Skipped, res.Failed, res.SeenSnapshot)
	}

	if err := a.updateScriptCrawlerRunState(ctx, driveID, runErr); err != nil {
		log.Printf("[scriptcrawler] drive=%s update last_crawl_at: %v", driveID, err)
	}
	if err := ctx.Err(); err != nil {
		log.Printf("[scriptcrawler] drive=%s crawl canceled after run: %v", driveID, err)
		return false
	}

	a.mu.Lock()
	worker := a.workers[driveID]
	thumbWorker := a.thumbWorkers[driveID]
	fingerprintWorker := a.fingerprintWorkers[driveID]
	a.mu.Unlock()
	a.scheduleFingerprintBackfill(ctx, driveID, fingerprintWorker)
	a.enqueueDriveGeneration(ctx, driveID, worker, thumbWorker)
	return runErr == nil
}

func (a *App) updateScriptCrawlerRunState(ctx context.Context, driveID string, runErr error) error {
	d, err := a.cat.GetDrive(ctx, driveID)
	if err != nil {
		return err
	}
	if d.Credentials == nil {
		d.Credentials = make(map[string]string)
	}
	d.Credentials["last_crawl_at"] = strconv.FormatInt(time.Now().Unix(), 10)
	if runErr != nil {
		d.Status = "error"
		d.LastError = runErr.Error()
	} else {
		d.Status = "ok"
		d.LastError = ""
	}
	return a.cat.UpsertDrive(ctx, d)
}

func (a *App) scheduleCrawlerUploadMigration(ctx context.Context, driveID string) bool {
	driveID = strings.TrimSpace(driveID)
	if driveID == "" || a == nil || a.cat == nil {
		return false
	}
	d, err := a.cat.GetDrive(ctx, driveID)
	if err != nil || d == nil || d.Kind != scriptcrawler.Kind || strings.TrimSpace(d.Credentials["upload_drive_id"]) == "" {
		return false
	}
	if a.crawlerUploader == nil {
		log.Printf("[scriptcrawler] drive=%s skip saved upload migration: migrator not configured", driveID)
		return false
	}

	a.crawlerUploadMu.Lock()
	if a.crawlerUploadRunning == nil {
		a.crawlerUploadRunning = make(map[string]bool)
	}
	if a.crawlerUploadRunning[driveID] {
		a.crawlerUploadMu.Unlock()
		log.Printf("[scriptcrawler] drive=%s saved upload migration already running", driveID)
		return false
	}
	a.crawlerUploadRunning[driveID] = true
	a.crawlerUploadMu.Unlock()

	taskCtx, done := a.registerDriveTaskContext(ctx, driveID)
	go func() {
		defer func() {
			done()
			a.crawlerUploadMu.Lock()
			delete(a.crawlerUploadRunning, driveID)
			a.crawlerUploadMu.Unlock()
		}()
		a.runCrawlerUploadMigrationAfterSave(taskCtx, driveID)
	}()
	return true
}

func (a *App) runCrawlerUploadMigrationAfterSave(ctx context.Context, driveID string) {
	if err := ctx.Err(); err != nil {
		log.Printf("[scriptcrawler] drive=%s skip saved upload migration: %v", driveID, err)
		return
	}
	d, err := a.cat.GetDrive(ctx, driveID)
	if err != nil || d == nil {
		log.Printf("[scriptcrawler] drive=%s saved upload migration lookup: %v", driveID, err)
		return
	}
	targetDriveID := strings.TrimSpace(d.Credentials["upload_drive_id"])
	if d.Kind != scriptcrawler.Kind || targetDriveID == "" {
		return
	}
	if err := a.ensureDriveAttached(ctx, driveID); err != nil {
		log.Printf("[scriptcrawler] drive=%s saved upload migration attach: %v", driveID, err)
		return
	}

	a.mu.Lock()
	worker := a.workers[driveID]
	thumbWorker := a.thumbWorkers[driveID]
	fingerprintWorker := a.fingerprintWorkers[driveID]
	a.mu.Unlock()
	a.scheduleFingerprintBackfill(ctx, driveID, fingerprintWorker)
	a.enqueueDriveGeneration(ctx, driveID, worker, thumbWorker)

	log.Printf("[scriptcrawler] drive=%s checking local videos for upload target=%s", driveID, targetDriveID)
	if err := a.waitDriveGenerationQueuesIdle(ctx, driveID); err != nil {
		log.Printf("[scriptcrawler] drive=%s saved upload migration wait canceled: %v", driveID, err)
		return
	}
	if err := ctx.Err(); err != nil {
		log.Printf("[scriptcrawler] drive=%s skip saved upload migration after wait: %v", driveID, err)
		return
	}
	if err := a.crawlerUploader.RunOnce(ctx); err != nil {
		log.Printf("[scriptcrawler] drive=%s saved upload migration: %v", driveID, err)
	}
}

func (a *App) scheduleManualCrawlerUploadMigration(ctx context.Context, driveID string) (bool, string) {
	driveID = strings.TrimSpace(driveID)
	if driveID == "" || a == nil || a.cat == nil {
		return false, "爬虫不存在"
	}
	if a.crawlerUploader == nil {
		return false, "上传迁移器未初始化"
	}
	if a.driveHasActiveWork(driveID) {
		return false, "当前爬虫有正在进行的任务，请稍后重试"
	}
	d, err := a.cat.GetDrive(ctx, driveID)
	if err != nil || d == nil || d.Kind != scriptcrawler.Kind {
		return false, "爬虫不存在"
	}
	targetDriveID := strings.TrimSpace(d.Credentials["upload_drive_id"])
	if targetDriveID == "" {
		return false, "请先配置上传网盘"
	}
	assets, err := a.cat.CountCrawlerAssets(ctx, driveID, crawlerCatalogVideoIDPrefixes(d))
	if err != nil {
		log.Printf("[scriptcrawler] drive=%s manual upload count assets: %v", driveID, err)
		return false, "读取待上传视频失败"
	}
	if reason := crawlerUploadAssetBlockReason(d, assets); reason != "" {
		return false, reason
	}
	if err := a.ensureDriveAttached(ctx, driveID); err != nil {
		log.Printf("[scriptcrawler] drive=%s manual upload source attach: %v", driveID, err)
		return false, "爬虫本地存储不可用"
	}
	if err := a.ensureDriveAttached(ctx, targetDriveID); err != nil {
		log.Printf("[scriptcrawler] drive=%s manual upload target=%s attach: %v", driveID, targetDriveID, err)
		return false, "上传网盘不可用：" + err.Error()
	}

	a.crawlerUploadMu.Lock()
	if a.crawlerUploadRunning == nil {
		a.crawlerUploadRunning = make(map[string]bool)
	}
	if a.crawlerUploadRunning[driveID] {
		a.crawlerUploadMu.Unlock()
		return false, "当前爬虫已有上传任务正在运行"
	}
	a.crawlerUploadRunning[driveID] = true
	a.crawlerUploadMu.Unlock()

	taskCtx, done := a.registerDriveTaskContext(ctx, driveID)
	go func() {
		defer func() {
			done()
			a.crawlerUploadMu.Lock()
			delete(a.crawlerUploadRunning, driveID)
			a.crawlerUploadMu.Unlock()
		}()
		a.runManualCrawlerUploadMigration(taskCtx, driveID, targetDriveID)
	}()
	return true, ""
}

func crawlerUploadAssetBlockReason(d *catalog.Drive, assets catalog.CrawlerAssetCounts) string {
	if assets.Local <= 0 {
		return "没有待上传的本地视频"
	}
	if assets.Fingerprint.Pending > 0 {
		return "还有待生成的视频指纹"
	}
	if assets.Fingerprint.Failed > 0 {
		return "存在指纹生成失败的视频，请先重试或处理失败项"
	}
	if d != nil && d.TeaserEnabled {
		if assets.Teaser.Pending > 0 {
			return "还有待生成的预览视频"
		}
		if assets.Teaser.Failed > 0 {
			return "存在预览视频生成失败的视频，请先重试或处理失败项"
		}
	}
	return ""
}

func crawlerCatalogVideoIDPrefixes(d *catalog.Drive) []string {
	if d == nil {
		return nil
	}
	return []string{
		scriptcrawler.Kind + "-" + d.ID + "-",
	}
}

func (a *App) runManualCrawlerUploadMigration(ctx context.Context, driveID, targetDriveID string) {
	if err := ctx.Err(); err != nil {
		log.Printf("[scriptcrawler] drive=%s skip manual upload migration: %v", driveID, err)
		return
	}
	log.Printf("[scriptcrawler] drive=%s running manual upload migration target=%s", driveID, targetDriveID)
	if err := a.crawlerUploader.RunOnce(ctx); err != nil {
		log.Printf("[scriptcrawler] drive=%s manual upload migration: %v", driveID, err)
	}
}

func (a *App) runCrawlerMigrationAfterManualCrawl(ctx context.Context, driveID string) {
	if err := ctx.Err(); err != nil {
		log.Printf("[scriptcrawler] drive=%s skip post-crawl migration: %v", driveID, err)
		return
	}
	d, err := a.cat.GetDrive(ctx, driveID)
	if err != nil || d == nil {
		log.Printf("[scriptcrawler] drive=%s skip post-crawl migration lookup: %v", driveID, err)
		return
	}
	targetDriveID := strings.TrimSpace(d.Credentials["upload_drive_id"])
	if targetDriveID == "" {
		return
	}
	if a.crawlerUploader == nil {
		log.Printf("[scriptcrawler] drive=%s skip post-crawl migration: migrator not configured", driveID)
		return
	}
	log.Printf("[scriptcrawler] drive=%s waiting for generation queues before post-crawl migration target=%s", driveID, targetDriveID)
	if err := a.waitDriveGenerationQueuesIdle(ctx, driveID); err != nil {
		log.Printf("[scriptcrawler] drive=%s post-crawl migration wait canceled: %v", driveID, err)
		return
	}
	if err := ctx.Err(); err != nil {
		log.Printf("[scriptcrawler] drive=%s skip post-crawl migration after wait: %v", driveID, err)
		return
	}
	log.Printf("[scriptcrawler] drive=%s running post-crawl migration target=%s", driveID, targetDriveID)
	if err := a.crawlerUploader.RunOnce(ctx); err != nil {
		log.Printf("[scriptcrawler] drive=%s post-crawl migration: %v", driveID, err)
	}
}

// crawlerIntCred 解析 credentials 中的整数字段，缺省时返回 def。
func crawlerIntCred(d *catalog.Drive, key string, def int) int {
	if d == nil || d.Credentials == nil {
		return def
	}
	raw := strings.TrimSpace(d.Credentials[key])
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// ---------- middleware ----------

// corsMiddleware 返回一个 chi 中间件，按白名单匹配 Origin 决定是否回写
// CORS 响应头。
//
// 设计要点：
//   - 不再反射任意 Origin。Origin 必须出现在 allowedOrigins 中才会得到
//     Access-Control-Allow-Origin / Allow-Credentials 的"放行"响应头；
//     不在白名单的跨源请求拿不到这些头，浏览器会拒绝读响应内容。
//   - 同源请求（浏览器不发 Origin 头，或 Origin 等于自己）不需要 CORS 头，
//     直接放行。
//   - 始终带 Vary: Origin，避免反代缓存把 A Origin 的允许头喂给 B Origin。
//   - 对不在白名单的 OPTIONS 预检直接 403，避免被当成"放行"信号。
//
// allowedOrigins 由 config.Server.AllowedOrigins 注入；默认为空 = 完全
// 不允许跨源（最安全的默认值，同源部署不受影响）。
func corsMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allow := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		o = strings.TrimSpace(o)
		if o == "" || o == "*" {
			// 通配符在带 cookie 的 CORS 下没意义且危险，直接忽略
			continue
		}
		allow[o] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// 任何走过 CORS 检查的响应都要带 Vary: Origin，避免缓存污染。
			w.Header().Add("Vary", "Origin")

			isAllowedOrigin := false
			if origin != "" {
				_, isAllowedOrigin = allow[origin]
			}

			if isAllowedOrigin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				w.Header().Set("Access-Control-Max-Age", "600")
			}

			if r.Method == http.MethodOptions {
				// 预检请求：只对白名单 Origin 返回 204；否则 403 让浏览器把请求拦下来。
				// 同源场景一般不会触发预检（浏览器只在跨源 + 复杂请求时才发 OPTIONS）。
				if isAllowedOrigin {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if origin != "" {
					http.Error(w, "cors: origin not allowed", http.StatusForbidden)
					return
				}
				// 没带 Origin 的 OPTIONS 不是 CORS 预检（可能是健康检查工具），
				// 直接交给下游处理。
			}

			next.ServeHTTP(w, r)
		})
	}
}

func mountFrontend(r chi.Router) {
	dir, ok := resolveFrontendDir()
	if !ok {
		return
	}
	log.Printf("serving frontend from %s", dir)
	r.NotFound(frontendHandler(dir))
}

func resolveFrontendDir() (string, bool) {
	candidates := []string{}
	if dir := strings.TrimSpace(os.Getenv("VIDEO_FRONTEND_DIR")); dir != "" {
		candidates = append(candidates, dir)
	} else {
		candidates = append(candidates, "./dist", "../dist")
	}
	for _, dir := range candidates {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		indexPath := filepath.Join(dir, "index.html")
		if st, err := os.Stat(indexPath); err == nil && !st.IsDir() {
			return dir, true
		}
	}
	return "", false
}

func frontendHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		if isBackendRoute(r.URL.Path) {
			http.NotFound(w, r)
			return
		}

		cleanPath := path.Clean("/" + r.URL.Path)
		rel := strings.TrimPrefix(cleanPath, "/")
		if rel != "" && rel != "." {
			name := filepath.FromSlash(rel)
			f, err := os.Open(filepath.Join(dir, name))
			if err == nil {
				defer f.Close()
				if st, statErr := f.Stat(); statErr == nil && !st.IsDir() {
					http.ServeContent(w, r, st.Name(), st.ModTime(), f)
					return
				}
			}
			if filepath.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}

		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	}
}

func isBackendRoute(p string) bool {
	return p == "/api" ||
		strings.HasPrefix(p, "/api/") ||
		p == "/admin/api" ||
		strings.HasPrefix(p, "/admin/api/") ||
		p == "/p" ||
		strings.HasPrefix(p, "/p/")
}

func parseBoolDefault(raw string, def bool) bool {
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return v
}

func parseIntDefault(raw string, def int) int {
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}
