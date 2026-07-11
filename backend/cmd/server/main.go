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
	"path/filepath"
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
	"github.com/video-site/backend/internal/metatube"
	"github.com/video-site/backend/internal/drives/localstorage"
	"github.com/video-site/backend/internal/drives/scriptcrawler"
	"github.com/video-site/backend/internal/fingerprint"
	"github.com/video-site/backend/internal/nightly"
	"github.com/video-site/backend/internal/preview"
	"github.com/video-site/backend/internal/proxy"
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

	// Initialize MetaTube client if enabled.
	if cfg.MetaTube.Enabled && cfg.MetaTube.ServerURL != "" {
		app.metaTubeClient = metatube.New(metatube.Config{
			ServerURL: cfg.MetaTube.ServerURL,
			Token:     cfg.MetaTube.Token,
		})
		log.Printf("[metatube] scraper enabled, backend: %s", cfg.MetaTube.ServerURL)
	}

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
	//   Phase 4 扫描爬虫本地目录并恢复已取消拉黑的视频
	//   Phase 5 全库重复视频维护：精确指纹去重 + 标题/时长/封面近似去重
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
		RestoreCrawlerVideos:  app.restoreScriptCrawlerVideos,
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
