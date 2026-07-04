package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/video-site/backend/internal/auth"
	"github.com/video-site/backend/internal/catalog"
	"github.com/video-site/backend/internal/drives/guangyapan"
	"github.com/video-site/backend/internal/drives/p123"
	"github.com/video-site/backend/internal/drives/scriptcrawler"
	"github.com/video-site/backend/internal/drives/wopan"
	"github.com/video-site/backend/internal/tagging"
)

type AdminServer struct {
	Catalog *catalog.Catalog
	Auth    *auth.Authenticator
	// VersionFilePath points to the installer-written .version file.
	VersionFilePath string
	// ImageVersion is the Docker image version injected at build/runtime.
	// It takes precedence over VersionFilePath because Docker data volumes can
	// keep an older .version file across image upgrades.
	ImageVersion string
	// GitHubRepo is the owner/name repo used for update checks.
	GitHubRepo string
	// ReleaseAPIURL and HTTPClient are injectable for tests. Production code leaves them empty.
	ReleaseAPIURL string
	HTTPClient    *http.Client
	// SetupRequired 表示当前是否仍处于首次部署初始化状态。
	SetupRequired func() bool
	// OnSetup 持久化首次部署时设置的管理员账号密码，并更新运行中认证器。
	OnSetup func(username, password string) error
	// LocalPreviewDir is the local directory that stores generated preview videos and thumbs.
	LocalPreviewDir string
	// Hooks：外层注入实际执行者
	OnDriveSaved              func(driveID string) error
	OnDriveDeleteCleanup      func(ctx context.Context, driveID string) (int, error)
	OnDriveRemoved            func(driveID string)
	OnScanRequested           func(driveID string) bool
	OnCrawlerUploadRequested  func(driveID string) (bool, string)
	OnStopDriveTasks          func(driveID string) bool
	OnStopAllTasks            func() int
	OnRegenPreview            func(videoID string)
	OnRegenAllPreviews        func()
	OnRegenFailedPreviews     func(driveID string)
	OnRegenFailedThumbnails   func(driveID string)
	OnRegenFailedFingerprints func(driveID string)
	// OnStartDriveTranscode 手动开启某盘的浏览器兼容性转码任务。
	// 返回 (是否接受, 拒绝原因)。转码从不自动运行，只能在这里手动触发；
	// 处理完候选列表后任务自然结束。
	OnStartDriveTranscode func(driveID string) (bool, string)
	// OnStopDriveTranscode 手动停止某盘正在进行的转码任务。返回是否有任务被停。
	OnStopDriveTranscode           func(driveID string) bool
	OnDeleteVideo                  func(ctx context.Context, videoID string, deleteSource bool) (DeleteVideoResult, error)
	OnStartBlacklistSourceDelete   func(BlacklistSourceDeleteRequest) bool
	GetBlacklistSourceDeleteStatus func() BlacklistSourceDeleteStatus
	OnStartTagRetag                func() bool
	GetTagJobStatus                func() TagJobStatus
	GetDriveGenerationStatuses     func() map[string]DriveGenerationStatuses
	GetPreviewGenerationVideoIDs   func() map[string]bool
	// OnTeaserEnabledChanged 在 per-drive 预览视频开关被切换后调用。
	// enabled=true 时上层应该重新把 pending 预览视频入队（类似旧的全局开关从关到开）；
	// enabled=false 时通常不用做事 —— worker 入队前会再次查 catalog，自然停止。
	OnTeaserEnabledChanged func(driveID string, enabled bool)
	// Theme 读写（"dark" | "pink" | "sky"）
	GetTheme func() string
	SetTheme func(theme string) error
	// OnRunNightlyJob 触发一次完整的凌晨流水线（Phase1 扫盘 + Phase2 爬虫 +
	// Phase3 上传）。立即返回 —— 实际任务在后台跑，admin 在日志或下次状态查询里
	// 看进度。若流水线正在跑或已排队，Runner 会拒绝重复触发。
	OnRunNightlyJob func() bool
	// GetNightlyJobStatus 返回凌晨流水线当前状态，用于前端禁用重复触发按钮。
	GetNightlyJobStatus func() NightlyJobStatus
	// ListDriveDirChildren 列出某个 drive 在 parentID 目录下的直接子目录。
	// parentID 为空时使用 drive 的 RootID。返回 (子目录列表, error)。
	// 用于"设置跳过目录"弹窗按需展开浏览网盘目录树；只返回目录条目，文件忽略。
	// 调用方应当处理 error 并以 5xx 返回前端。
	ListDriveDirChildren func(ctx context.Context, driveID, parentID string) ([]DriveDirEntry, error)
	// 123网盘扫码登录接口测试注入；生产留空走官方 user.123pan.cn。
	P123UserAPIBaseURL string
	P123HTTPClient     *http.Client
	// 联通网盘扫码登录接口测试注入；生产留空走官方 panservice.mail.wo.cn。
	WopanQRAPIBaseURL string
	WopanQRHTTPClient *http.Client
	// 光鸭网盘扫码登录接口测试注入；生产留空走官方 account.guangyapan.com。
	GuangYaPanAccountBaseURL string
	GuangYaPanHTTPClient     *http.Client
}

const (
	driveTaskBusyMessage = "当前存储有正在进行的任务，请稍后重试"
	fullScanBusyMessage  = "当前有全量扫描任务正在进行，请稍后重试"
)

// DriveDirEntry 是 dirtree 接口的一条返回项：网盘上的一个目录节点。
type DriveDirEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type GenerationStatus struct {
	State         string `json:"state"`
	CurrentTitle  string `json:"currentTitle,omitempty"`
	QueueLength   int    `json:"queueLength"`
	CooldownUntil string `json:"cooldownUntil,omitempty"`
	ScannedCount  int    `json:"scannedCount"`
	AddedCount    int    `json:"addedCount"`
	DoneCount     int    `json:"doneCount"`
	TotalCount    int    `json:"totalCount"`
}

type DriveGenerationStatuses struct {
	Scan        GenerationStatus `json:"scan"`
	Thumbnail   GenerationStatus `json:"thumbnail"`
	Preview     GenerationStatus `json:"preview"`
	Fingerprint GenerationStatus `json:"fingerprint"`
	Upload      GenerationStatus `json:"upload"`
	Transcode   GenerationStatus `json:"transcode"`
}

type NightlyJobStatus struct {
	State          string `json:"state"`
	Running        bool   `json:"running"`
	Queued         bool   `json:"queued"`
	StartedAt      string `json:"startedAt,omitempty"`
	LastFinishedAt string `json:"lastFinishedAt,omitempty"`
}

type TagJobStatus struct {
	State          string `json:"state"`
	Running        bool   `json:"running"`
	Kind           string `json:"kind,omitempty"`
	Total          int    `json:"total"`
	Processed      int    `json:"processed"`
	LastError      string `json:"lastError,omitempty"`
	StartedAt      string `json:"startedAt,omitempty"`
	LastFinishedAt string `json:"lastFinishedAt,omitempty"`
}

type BlacklistSourceDeleteStatus struct {
	State        string `json:"state"`
	Running      bool   `json:"running"`
	Pending      int    `json:"pending"`
	Total        int    `json:"total"`
	Processed    int    `json:"processed"`
	Deleted      int    `json:"deleted"`
	Failed       int    `json:"failed"`
	CurrentFile  string `json:"currentFile,omitempty"`
	LastError    string `json:"lastError,omitempty"`
	StartedAt    string `json:"startedAt,omitempty"`
	LastFinished string `json:"lastFinishedAt,omitempty"`
}

type BlacklistSourceDeleteRequest struct {
	DeleteAllSources bool     `json:"deleteAllSources,omitempty"`
	IDs              []string `json:"ids,omitempty"`
}

const maxCrawlerScriptBytes = 2 * 1024 * 1024

type DeleteVideoResult struct {
	OK            bool `json:"ok"`
	DeletedSource bool `json:"deletedSource"`
}

type deleteVideoReq struct {
	DeleteSource bool `json:"deleteSource"`
}

func (a *AdminServer) Register(r chi.Router) {
	r.Route("/admin/api", func(r chi.Router) {
		// 登录、登出和首次部署初始化不需要鉴权
		r.Get("/setup", a.handleSetupStatus)
		r.Post("/setup", a.handleSetup)
		r.Post("/login", a.handleLogin)
		r.Post("/logout", a.handleLogout)
		r.Get("/me", a.handleMe)

		// 其余路由需管理员鉴权
		r.Group(func(r chi.Router) {
			r.Use(a.Auth.AdminRequired)

			// 网盘
			r.Get("/drives", a.handleListDrives)
			r.Get("/drives/storage", a.handleDriveStorage)
			r.Post("/drives", a.handleUpsertDrive)
			r.Post("/drives/p123/qr", a.handleP123QRStart)
			r.Get("/drives/p123/qr/{uniID}", a.handleP123QRStatus)
			r.Post("/drives/wopan/qr", a.handleWopanQRStart)
			r.Get("/drives/wopan/qr/{uuid}", a.handleWopanQRStatus)
			r.Post("/drives/guangyapan/qr", a.handleGuangYaPanQRStart)
			r.Get("/drives/guangyapan/qr/status", a.handleGuangYaPanQRStatus)
			r.Delete("/drives/{id}", a.handleDeleteDrive)
			r.Post("/drives/{id}/rescan", a.handleRescan)
			r.Post("/drives/{id}/tasks/stop", a.handleStopDriveTasks)
			r.Post("/drives/{id}/teaser-enabled", a.handleSetDriveTeaserEnabled)
			r.Post("/drives/{id}/skip-dirs", a.handleSetDriveSkipDirs)
			r.Get("/drives/{id}/dirtree", a.handleListDriveDirTree)
			r.Post("/drives/{id}/previews/failed/regenerate", a.handleRegenFailedPreviews)
			r.Post("/drives/{id}/thumbnails/failed/regenerate", a.handleRegenFailedThumbnails)
			r.Post("/drives/{id}/fingerprints/failed/regenerate", a.handleRegenFailedFingerprints)
			r.Post("/drives/{id}/transcode/start", a.handleStartDriveTranscode)
			r.Post("/drives/{id}/transcode/stop", a.handleStopDriveTranscode)

			// 爬虫
			r.Get("/crawlers", a.handleListCrawlers)
			r.Post("/crawlers", a.handleUpsertCrawler)
			r.Post("/crawlers/import-file", a.handleImportCrawlerScriptFile)
			r.Post("/crawlers/import-url", a.handleImportCrawlerScriptURL)
			r.Post("/crawlers/test-script", a.handleTestCrawlerScript)
			r.Delete("/crawlers/{id}", a.handleDeleteCrawler)
			r.Post("/crawlers/{id}/run", a.handleRunCrawler)
			r.Post("/crawlers/{id}/upload", a.handleUploadCrawlerVideos)
			r.Post("/crawlers/{id}/tasks/stop", a.handleStopCrawlerTasks)

			// 视频
			r.Get("/videos", a.handleAdminListVideos)
			r.Get("/videos/stats", a.handleVideoStats)
			r.Put("/videos/{id}", a.handleUpdateVideo)
			r.Delete("/videos/{id}", a.handleDeleteVideo)
			r.Post("/videos/regen-preview", a.handleRegenAllPreviews)
			r.Post("/videos/{id}/regen-preview", a.handleRegenPreview)
			// 黑名单（被拉黑/手动删除、扫盘不再入库的视频）
			r.Get("/blacklist", a.handleListBlacklist)
			r.Get("/blacklist/source-delete/status", a.handleBlacklistSourceDeleteStatus)
			r.Post("/blacklist/source-delete", a.handleStartBlacklistSourceDelete)
			r.Delete("/blacklist/{id}", a.handleRemoveBlacklist)

			// 标签
			r.Get("/tags", a.handleListTags)
			r.Post("/tags", a.handleCreateTag)
			r.Put("/tags/{id}", a.handleUpdateTag)
			r.Delete("/tags/{id}", a.handleDeleteTag)
			r.Post("/tags/retag", a.handleStartTagRetag)
			r.Get("/tags/jobs/status", a.handleTagJobStatus)

			// 用户管理
			r.Get("/users", a.handleListUsers)
			r.Post("/users", a.handleCreateUser)
			r.Delete("/users/{id}", a.handleDeleteUser)
			r.Post("/users/{id}/ban", a.handleBanUser)
			r.Post("/users/{id}/unban", a.handleUnbanUser)
			r.Put("/users/{id}/password", a.handleResetPassword)

			// IP 封禁管理
			r.Get("/banned-ips", a.handleListBannedIPs)
			r.Delete("/banned-ips/{ip}", a.handleUnbanIP)

			// 运行时设置
			r.Get("/settings", a.handleGetSettings)
			r.Put("/settings", a.handlePutSettings)

			// 运维任务
			r.Get("/update/check", a.handleCheckUpdate)
			r.Get("/jobs/nightly/status", a.handleNightlyJobStatus)
			r.Post("/jobs/nightly/run", a.handleRunNightlyJob)
			r.Post("/tasks/stop", a.handleStopAllTasks)
		})
	})
}

type updateCheckDTO struct {
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	HasUpdate      bool   `json:"hasUpdate"`
	ReleaseURL     string `json:"releaseUrl,omitempty"`
	CheckedAt      string `json:"checkedAt"`
}

type githubReleaseDTO struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type setupReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *AdminServer) setupRequired() bool {
	return a.SetupRequired != nil && a.SetupRequired()
}

func (a *AdminServer) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"required": a.setupRequired()})
}

func (a *AdminServer) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !a.setupRequired() {
		http.Error(w, "setup already completed", http.StatusConflict)
		return
	}
	if a.OnSetup == nil || a.Auth == nil {
		http.Error(w, "setup is not available", http.StatusInternalServerError)
		return
	}
	var body setupReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	username := strings.TrimSpace(body.Username)
	password := body.Password
	if username == "" {
		http.Error(w, "username is required", http.StatusBadRequest)
		return
	}
	if len(password) < 6 {
		http.Error(w, "password must be at least 6 characters", http.StatusBadRequest)
		return
	}
	if err := a.OnSetup(username, password); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	role, err := a.Auth.UserLogin(w, r, username, password)
	if err != nil {
		if errors.Is(err, auth.ErrLoginIPBanned) {
			http.Error(w, "ip banned", http.StatusForbidden)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if role != "admin" {
		http.Error(w, "setup completed but login failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *AdminServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if a.setupRequired() {
		http.Error(w, "setup required", http.StatusPreconditionRequired)
		return
	}
	var body loginReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	role, err := a.Auth.UserLogin(w, r, body.Username, body.Password)
	if err != nil {
		if errors.Is(err, auth.ErrLoginIPBanned) {
			http.Error(w, "ip banned", http.StatusForbidden)
			return
		}
		if errors.Is(err, auth.ErrUserBanned) {
			http.Error(w, "user banned", http.StatusForbidden)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if role == "" {
		http.Error(w, "invalid credentials", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "role": role})
}

func (a *AdminServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	a.Auth.Logout(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *AdminServer) handleMe(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("vs_admin")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	ok, userID, err := a.Catalog.ValidateSession(r.Context(), c.Value)
	if err != nil || !ok {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}

	role := "user"
	if userID > 0 {
		u, err := a.Catalog.GetUserByID(r.Context(), userID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && u.Banned) {
			writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
			return
		}
		role = u.Role
	} else {
		role = "admin"
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "role": role})
}

func (a *AdminServer) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	info, err := a.checkUpdate(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, info)
}

func (a *AdminServer) checkUpdate(ctx context.Context) (updateCheckDTO, error) {
	current := a.installedVersion()
	if current == "" {
		current = "unknown"
	}
	release, err := a.latestRelease(ctx)
	if err != nil {
		return updateCheckDTO{
			CurrentVersion: current,
			CheckedAt:      time.Now().Format(time.RFC3339),
		}, err
	}
	latest := strings.TrimSpace(release.TagName)
	return updateCheckDTO{
		CurrentVersion: current,
		LatestVersion:  latest,
		HasUpdate:      current != "unknown" && latest != "" && current != latest,
		ReleaseURL:     release.HTMLURL,
		CheckedAt:      time.Now().Format(time.RFC3339),
	}, nil
}

func (a *AdminServer) installedVersion() string {
	if version := strings.TrimSpace(a.ImageVersion); version != "" {
		return version
	}
	path := strings.TrimSpace(a.VersionFilePath)
	if path == "" {
		path = ".version"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[0])
}

func (a *AdminServer) latestRelease(ctx context.Context) (githubReleaseDTO, error) {
	url := strings.TrimSpace(a.ReleaseAPIURL)
	if url == "" {
		repo := strings.TrimSpace(a.GitHubRepo)
		if repo == "" {
			repo = "nianzhibai/91"
		}
		url = "https://api.github.com/repos/" + repo + "/releases/latest"
	}
	client := a.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return githubReleaseDTO{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "video-site-91")
	res, err := client.Do(req)
	if err != nil {
		return githubReleaseDTO{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return githubReleaseDTO{}, fmt.Errorf("github release check failed: HTTP %d", res.StatusCode)
	}
	var release githubReleaseDTO
	if err := json.NewDecoder(res.Body).Decode(&release); err != nil {
		return githubReleaseDTO{}, err
	}
	if strings.TrimSpace(release.TagName) == "" {
		return githubReleaseDTO{}, errors.New("github release check returned empty tag")
	}
	return release, nil
}

func (a *AdminServer) handleListDrives(w http.ResponseWriter, r *http.Request) {
	drives, err := a.Catalog.ListDrives(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	teaserCounts, err := a.Catalog.CountTeasersByDrive(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	thumbnailCounts, err := a.Catalog.CountThumbnailsByDrive(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	fingerprintCounts, err := a.Catalog.CountFingerprintsByDrive(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	transcodeCounts, err := a.Catalog.CountTranscodesByDrive(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	generationStatuses := map[string]DriveGenerationStatuses{}
	if a.GetDriveGenerationStatuses != nil {
		generationStatuses = a.GetDriveGenerationStatuses()
	}
	// 出参不返回凭证明文，只告诉前端是否已配置
	type out struct {
		ID            string `json:"id"`
		Kind          string `json:"kind"`
		Name          string `json:"name"`
		RootID        string `json:"rootId"`
		ScanRootID    string `json:"scanRootId"`
		Status        string `json:"status"`
		LastError     string `json:"lastError,omitempty"`
		HasCredential bool   `json:"hasCredential"`
		// TeaserEnabled 控制是否给本盘生成预览视频；封面生成不受影响。
		// 前端用它在网盘列表/编辑表单展示开关状态。
		TeaserEnabled bool `json:"teaserEnabled"`
		// SkipDirIDs 是用户在 admin 配置的"扫描跳过目录"集合（drive 侧目录 fileID）。
		// 前端用它在"设置跳过目录"弹窗里回显已选项；JSON 字段名 camelCase 与
		// catalog.Drive 保持一致。
		SkipDirIDs                []string `json:"skipDirIds"`
		LastCrawlAt               int64    `json:"lastCrawlAt,omitempty"`
		GoogleDriveUseOnlineAPI   *bool    `json:"googleDriveUseOnlineAPI,omitempty"`
		GoogleDriveOpenListAPIURL string   `json:"googleDriveOpenListApiUrl,omitempty"`
		// STRMAllowOutsideRoot 是 localstorage 的 .strm 越root开关；其它 kind 省略。
		STRMAllowOutsideRoot          *bool            `json:"strmAllowOutsideRoot,omitempty"`
		ScanGenerationStatus          GenerationStatus `json:"scanGenerationStatus"`
		ThumbnailGenerationStatus     GenerationStatus `json:"thumbnailGenerationStatus"`
		PreviewGenerationStatus       GenerationStatus `json:"previewGenerationStatus"`
		FingerprintGenerationStatus   GenerationStatus `json:"fingerprintGenerationStatus"`
		ThumbnailReadyCount           int              `json:"thumbnailReadyCount"`
		ThumbnailPendingCount         int              `json:"thumbnailPendingCount"`
		ThumbnailFailedCount          int              `json:"thumbnailFailedCount"`
		ThumbnailDurationPendingCount int              `json:"thumbnailDurationPendingCount"`
		TeaserReadyCount              int              `json:"teaserReadyCount"`
		TeaserPendingCount            int              `json:"teaserPendingCount"`
		TeaserFailedCount             int              `json:"teaserFailedCount"`
		FingerprintReadyCount         int              `json:"fingerprintReadyCount"`
		FingerprintPendingCount       int              `json:"fingerprintPendingCount"`
		FingerprintFailedCount        int              `json:"fingerprintFailedCount"`
		TranscodeGenerationStatus     GenerationStatus `json:"transcodeGenerationStatus"`
		TranscodePendingCount         int              `json:"transcodePendingCount"`
		TranscodeReadyCount           int              `json:"transcodeReadyCount"`
		TranscodeFailedCount          int              `json:"transcodeFailedCount"`
		TranscodeSkippedCount         int              `json:"transcodeSkippedCount"`
	}
	list := make([]out, 0, len(drives))
	for _, d := range drives {
		if isCrawlerDriveKind(d.Kind) {
			continue
		}
		counts := teaserCounts[d.ID]
		thumbCounts := thumbnailCounts[d.ID]
		fingerprintCount := fingerprintCounts[d.ID]
		transcodeCount := transcodeCounts[d.ID]
		generation := generationStatuses[d.ID]
		if generation.Scan.State == "" {
			generation.Scan.State = "idle"
		}
		if generation.Thumbnail.State == "" {
			generation.Thumbnail.State = "idle"
		}
		if generation.Preview.State == "" {
			generation.Preview.State = "idle"
		}
		if generation.Fingerprint.State == "" {
			generation.Fingerprint.State = "idle"
		}
		if generation.Transcode.State == "" {
			generation.Transcode.State = "idle"
		}
		// last_crawl_at 是后端自动写入的运行状态字段，不计入 hasCredential 判定。
		hasCred := false
		userCredKeys := 0
		for k := range d.Credentials {
			if k == "last_crawl_at" {
				continue
			}
			userCredKeys++
		}
		hasCred = userCredKeys > 0

		var lastCrawlAt int64
		if d.Credentials != nil {
			if raw, ok := d.Credentials["last_crawl_at"]; ok && raw != "" {
				if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
					lastCrawlAt = v
				}
			}
		}

		list = append(list, out{
			ID: d.ID, Kind: d.Kind, Name: d.Name,
			RootID: d.RootID, ScanRootID: d.ScanRootID,
			Status: d.Status, LastError: d.LastError,
			HasCredential:                 hasCred,
			TeaserEnabled:                 d.TeaserEnabled,
			SkipDirIDs:                    append([]string{}, d.SkipDirIDs...),
			LastCrawlAt:                   lastCrawlAt,
			GoogleDriveUseOnlineAPI:       googleDriveUseOnlineAPIForDrive(d),
			GoogleDriveOpenListAPIURL:     googleDriveOpenListAPIURLForDrive(d),
			STRMAllowOutsideRoot:          strmAllowOutsideRootForDrive(d),
			ScanGenerationStatus:          generation.Scan,
			ThumbnailGenerationStatus:     generation.Thumbnail,
			PreviewGenerationStatus:       generation.Preview,
			FingerprintGenerationStatus:   generation.Fingerprint,
			ThumbnailReadyCount:           thumbCounts.Ready,
			ThumbnailPendingCount:         thumbCounts.Pending,
			ThumbnailFailedCount:          thumbCounts.Failed,
			ThumbnailDurationPendingCount: thumbCounts.DurationPending,
			TeaserReadyCount:              counts.Ready,
			TeaserPendingCount:            counts.Pending,
			TeaserFailedCount:             counts.Failed,
			FingerprintReadyCount:         fingerprintCount.Ready,
			FingerprintPendingCount:       fingerprintCount.Pending,
			FingerprintFailedCount:        fingerprintCount.Failed,
			TranscodeGenerationStatus:     generation.Transcode,
			TranscodePendingCount:         transcodeCount.Pending,
			TranscodeReadyCount:           transcodeCount.Ready,
			TranscodeFailedCount:          transcodeCount.Failed,
			TranscodeSkippedCount:         transcodeCount.Skipped,
		})
	}
	writeJSON(w, http.StatusOK, list)
}

type upsertDriveReq struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	RootID string `json:"rootId"`
	// Deprecated: 扫描起点已固定为 rootId；保留字段只为兼容旧客户端请求体。
	ScanRootID  string            `json:"scanRootId"`
	Credentials map[string]string `json:"credentials"`
	// TeaserEnabled 是 per-drive 预览视频生成开关；封面生成不受影响。
	// 用 *bool 区分 "未传" / "传了 false"：未传时表示客户端不打算改这个字段，
	// 沿用 catalog 现有值；新建时未传一律默认开启（true）。
	TeaserEnabled *bool `json:"teaserEnabled,omitempty"`
	// SkipDirIDs 同样用指针区分 "未传"（沿用旧值）/ "传了空数组"（清空）。
	// 推荐前端"设置跳过目录"走专用 POST /drives/{id}/skip-dirs；
	// 这里支持是为了允许批量编辑场景一次性提交。
	SkipDirIDs *[]string `json:"skipDirIds,omitempty"`
}

func (a *AdminServer) handleUpsertDrive(w http.ResponseWriter, r *http.Request) {
	var body upsertDriveReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.ID == "" || body.Kind == "" {
		http.Error(w, "id and kind are required", http.StatusBadRequest)
		return
	}
	// 凭证 / TeaserEnabled 都支持 "未传 = 沿用旧值"：先把现存 drive 拉出来一次。
	var existing *catalog.Drive
	if existingDrive, err := a.Catalog.GetDrive(r.Context(), body.ID); err == nil {
		existing = existingDrive
	}
	if !isSupportedDriveKind(body.Kind) {
		http.Error(w, "unsupported drive kind", http.StatusBadRequest)
		return
	}
	if body.Kind == scriptcrawler.Kind {
		credentials, err := mergeScriptCrawlerCredentials(existing, body.Credentials)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body.Credentials = credentials
	} else if body.Kind == "googledrive" {
		body.Credentials = mergeGoogleDriveCredentials(existing, body.Credentials)
	} else if body.Kind == "localstorage" || body.Kind == "guangyapan" {
		// 按键合并、空值沿用旧值：这些网盘的编辑表单允许只改某几个字段，
		// 其它 token / 路径 / 开关字段应保留旧值。
		body.Credentials = mergeNonEmptyCredentials(existing, body.Credentials)
	} else if len(body.Credentials) == 0 && existing != nil && len(existing.Credentials) > 0 {
		body.Credentials = existing.Credentials
	}

	// teaserEnabled 解析顺序：
	//   1. 请求显式带了 → 用请求值
	//   2. 请求没带 + 编辑现有 drive → 沿用旧值
	//   3. 请求没带 + 新建 drive → 默认 true（用户没特别说就生成）
	teaserEnabled := true
	switch {
	case body.TeaserEnabled != nil:
		teaserEnabled = *body.TeaserEnabled
	case existing != nil:
		teaserEnabled = existing.TeaserEnabled
	}

	// skipDirIds 解析顺序：
	//   1. 请求显式带了（包括空数组）→ 用请求值（空数组 = 清空）
	//   2. 请求没带 + 编辑现有 drive → 沿用旧值
	//   3. 请求没带 + 新建 drive → nil（不跳过任何目录）
	var skipDirIDs []string
	switch {
	case body.SkipDirIDs != nil:
		skipDirIDs = *body.SkipDirIDs
	case existing != nil:
		skipDirIDs = existing.SkipDirIDs
	}

	d := &catalog.Drive{
		ID: body.ID, Kind: body.Kind, Name: body.Name,
		RootID:        body.RootID,
		Credentials:   body.Credentials,
		Status:        "disconnected",
		TeaserEnabled: teaserEnabled,
		SkipDirIDs:    skipDirIDs,
	}
	if err := a.Catalog.UpsertDrive(r.Context(), d); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if a.OnDriveSaved != nil {
		if err := a.OnDriveSaved(body.ID); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "warning": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type crawlerDTO struct {
	ID                          string           `json:"id"`
	Name                        string           `json:"name"`
	Kind                        string           `json:"kind"`
	Status                      string           `json:"status"`
	LastError                   string           `json:"lastError,omitempty"`
	ScriptPath                  string           `json:"scriptPath"`
	ScriptSourceURL             string           `json:"scriptSourceUrl,omitempty"`
	Proxy                       string           `json:"proxy,omitempty"`
	TargetNew                   string           `json:"targetNew,omitempty"`
	UploadDriveID               string           `json:"uploadDriveId,omitempty"`
	TeaserEnabled               bool             `json:"teaserEnabled"`
	LastCrawlAt                 int64            `json:"lastCrawlAt,omitempty"`
	ScanGenerationStatus        GenerationStatus `json:"scanGenerationStatus"`
	ThumbnailGenerationStatus   GenerationStatus `json:"thumbnailGenerationStatus"`
	PreviewGenerationStatus     GenerationStatus `json:"previewGenerationStatus"`
	FingerprintGenerationStatus GenerationStatus `json:"fingerprintGenerationStatus"`
	UploadGenerationStatus      GenerationStatus `json:"uploadGenerationStatus"`
	ThumbnailReadyCount         int              `json:"thumbnailReadyCount"`
	ThumbnailPendingCount       int              `json:"thumbnailPendingCount"`
	ThumbnailFailedCount        int              `json:"thumbnailFailedCount"`
	TeaserReadyCount            int              `json:"teaserReadyCount"`
	TeaserPendingCount          int              `json:"teaserPendingCount"`
	TeaserFailedCount           int              `json:"teaserFailedCount"`
	FingerprintReadyCount       int              `json:"fingerprintReadyCount"`
	FingerprintPendingCount     int              `json:"fingerprintPendingCount"`
	FingerprintFailedCount      int              `json:"fingerprintFailedCount"`
	TotalCrawledCount           int              `json:"totalCrawledCount"`
	LocalVideoCount             int              `json:"localVideoCount"`
	MigratedVideoCount          int              `json:"migratedVideoCount"`
}

type upsertCrawlerReq struct {
	ID              string `json:"id"`
	ScriptPath      string `json:"scriptPath"`
	ScriptSourceURL string `json:"scriptSourceUrl"`
	Proxy           string `json:"proxy"`
	TargetNew       string `json:"targetNew"`
	UploadDriveID   string `json:"uploadDriveId"`
	TeaserEnabled   *bool  `json:"teaserEnabled,omitempty"`
}

func (a *AdminServer) handleListCrawlers(w http.ResponseWriter, r *http.Request) {
	all, err := a.Catalog.ListDrives(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	generationStatuses := map[string]DriveGenerationStatuses{}
	if a.GetDriveGenerationStatuses != nil {
		generationStatuses = a.GetDriveGenerationStatuses()
	}

	out := []crawlerDTO{}
	for _, d := range all {
		if d == nil || !isConfiguredCrawlerDrive(d) {
			continue
		}
		assetCounts, err := a.Catalog.CountCrawlerAssets(r.Context(), d.ID, crawlerVideoIDPrefixes(d))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, a.crawlerDTOForDrive(d, assetCounts, generationStatuses[d.ID]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *AdminServer) crawlerDTOForDrive(d *catalog.Drive, assets catalog.CrawlerAssetCounts, generation DriveGenerationStatuses) crawlerDTO {
	if generation.Scan.State == "" {
		generation.Scan.State = "idle"
	}
	if generation.Thumbnail.State == "" {
		generation.Thumbnail.State = "idle"
	}
	if generation.Preview.State == "" {
		generation.Preview.State = "idle"
	}
	if generation.Fingerprint.State == "" {
		generation.Fingerprint.State = "idle"
	}
	if generation.Upload.State == "" {
		generation.Upload.State = "idle"
	}
	lastCrawlAt := int64(0)
	if raw := strings.TrimSpace(d.Credentials["last_crawl_at"]); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			lastCrawlAt = v
		}
	}
	return crawlerDTO{
		ID:                          d.ID,
		Name:                        crawlerNameForDrive(d),
		Kind:                        d.Kind,
		Status:                      d.Status,
		LastError:                   d.LastError,
		ScriptPath:                  strings.TrimSpace(d.Credentials["script_path"]),
		ScriptSourceURL:             strings.TrimSpace(d.Credentials["script_source_url"]),
		Proxy:                       strings.TrimSpace(d.Credentials["proxy"]),
		TargetNew:                   strings.TrimSpace(d.Credentials["target_new"]),
		UploadDriveID:               strings.TrimSpace(d.Credentials["upload_drive_id"]),
		TeaserEnabled:               d.TeaserEnabled,
		LastCrawlAt:                 lastCrawlAt,
		ScanGenerationStatus:        generation.Scan,
		ThumbnailGenerationStatus:   generation.Thumbnail,
		PreviewGenerationStatus:     generation.Preview,
		FingerprintGenerationStatus: generation.Fingerprint,
		UploadGenerationStatus:      generation.Upload,
		ThumbnailReadyCount:         assets.Thumbnail.Ready,
		ThumbnailPendingCount:       assets.Thumbnail.Pending,
		ThumbnailFailedCount:        assets.Thumbnail.Failed,
		TeaserReadyCount:            assets.Teaser.Ready,
		TeaserPendingCount:          assets.Teaser.Pending,
		TeaserFailedCount:           assets.Teaser.Failed,
		FingerprintReadyCount:       assets.Fingerprint.Ready,
		FingerprintPendingCount:     assets.Fingerprint.Pending,
		FingerprintFailedCount:      assets.Fingerprint.Failed,
		TotalCrawledCount:           assets.Total,
		LocalVideoCount:             assets.Local,
		MigratedVideoCount:          assets.Migrated,
	}
}

func crawlerVideoIDPrefixes(d *catalog.Drive) []string {
	if d == nil {
		return nil
	}
	return []string{
		scriptcrawler.Kind + "-" + d.ID + "-",
	}
}

func crawlerNameForDrive(d *catalog.Drive) string {
	if d == nil {
		return ""
	}
	if d.Credentials != nil {
		if meta, err := scriptcrawler.ReadMetadata(strings.TrimSpace(d.Credentials["script_path"])); err == nil {
			return meta.Name
		}
	}
	return strings.TrimSpace(d.Name)
}

func (a *AdminServer) handleUpsertCrawler(w http.ResponseWriter, r *http.Request) {
	var body upsertCrawlerReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := strings.TrimSpace(body.ID)
	creds := map[string]string{}
	var existing *catalog.Drive
	if id != "" {
		existing, _ = a.Catalog.GetDrive(r.Context(), id)
	}
	if existing != nil {
		for k, v := range existing.Credentials {
			creds[k] = v
		}
	}
	scriptPath := strings.TrimSpace(body.ScriptPath)
	incoming := map[string]string{
		"script_path":       scriptPath,
		"script_source_url": strings.TrimSpace(body.ScriptSourceURL),
		"proxy":             strings.TrimSpace(body.Proxy),
		"target_new":        strings.TrimSpace(body.TargetNew),
		"upload_drive_id":   strings.TrimSpace(body.UploadDriveID),
	}
	for k, v := range incoming {
		creds[k] = v
	}
	if err := a.validateCrawlerUploadDrive(r.Context(), creds["upload_drive_id"]); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	merged, err := mergeScriptCrawlerCredentials(existing, creds)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	meta, err := scriptcrawler.ReadMetadata(merged["script_path"])
	if err != nil {
		http.Error(w, "脚本元信息无效："+err.Error(), http.StatusBadRequest)
		return
	}
	name := meta.Name
	teaserEnabled := true
	if existing != nil {
		teaserEnabled = existing.TeaserEnabled
	}
	if body.TeaserEnabled != nil {
		teaserEnabled = *body.TeaserEnabled
	}
	if id == "" {
		generatedID, err := a.generateCrawlerID(r.Context(), name)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		id = generatedID
	}
	d := &catalog.Drive{
		ID:            id,
		Kind:          scriptcrawler.Kind,
		Name:          name,
		RootID:        "/",
		Credentials:   merged,
		Status:        "disconnected",
		TeaserEnabled: teaserEnabled,
	}
	if err := a.Catalog.UpsertDrive(r.Context(), d); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if existing != nil && existing.TeaserEnabled != teaserEnabled && a.OnTeaserEnabledChanged != nil {
		a.OnTeaserEnabledChanged(id, teaserEnabled)
	}
	if a.OnDriveSaved != nil {
		if err := a.OnDriveSaved(id); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "warning": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

func (a *AdminServer) generateCrawlerID(ctx context.Context, name string) (string, error) {
	all, err := a.Catalog.ListDrives(ctx)
	if err != nil {
		return "", err
	}
	used := map[string]bool{}
	for _, d := range all {
		if d == nil {
			continue
		}
		if isCrawlerDriveKind(d.Kind) && strings.TrimSpace(d.Credentials["script_path"]) == "" {
			continue
		}
		used[d.ID] = true
	}
	slug := crawlerIDSlug(name)
	base := "crawler"
	if slug != "" {
		base += "-" + slug
	}
	candidate := base
	for suffix := 2; used[candidate]; suffix++ {
		candidate = fmt.Sprintf("%s-%d", base, suffix)
	}
	return candidate, nil
}

func (a *AdminServer) validateCrawlerUploadDrive(ctx context.Context, driveID string) error {
	driveID = strings.TrimSpace(driveID)
	if driveID == "" {
		return nil
	}
	if a == nil || a.Catalog == nil {
		return errors.New("crawler upload target validation unavailable")
	}
	d, err := a.Catalog.GetDrive(ctx, driveID)
	if err != nil || d == nil {
		return fmt.Errorf("上传目标网盘 %q 不存在", driveID)
	}
	if !isCrawlerUploadTargetKind(d.Kind) {
		return fmt.Errorf("上传目标网盘 %q 类型为 %s，仅支持 115网盘、PikPak、123网盘、Google Drive、OneDrive、联通网盘、光鸭网盘", driveID, d.Kind)
	}
	return nil
}

func isCrawlerUploadTargetKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "p115", "pikpak", "p123", "googledrive", "onedrive", "wopan", "guangyapan":
		return true
	default:
		return false
	}
}

func crawlerIDSlug(raw string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(raw) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if b.Len() > 0 && !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

type importCrawlerScriptURLReq struct {
	URL      string `json:"url"`
	FileName string `json:"fileName"`
}

type testCrawlerScriptReq struct {
	ScriptPath string `json:"scriptPath"`
	Proxy      string `json:"proxy"`
}

// handleTestCrawlerScript 试跑一个爬虫脚本：不入库，抓到第一条视频
// （并探测直链可达）即返回，让用户在保存前确认脚本能爬到视频。
func (a *AdminServer) handleTestCrawlerScript(w http.ResponseWriter, r *http.Request) {
	var body testCrawlerScriptReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	scriptPath := strings.TrimSpace(body.ScriptPath)
	if scriptPath == "" {
		http.Error(w, "请先导入爬虫脚本", http.StatusBadRequest)
		return
	}
	proxyURL, err := normalizeCrawlerProxyURL(body.Proxy, "脚本爬虫")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result := scriptcrawler.DryRun(r.Context(), scriptcrawler.DryRunConfig{
		ScriptPath: scriptPath,
		ProxyURL:   proxyURL,
	})
	writeJSON(w, http.StatusOK, result)
}

func (a *AdminServer) handleImportCrawlerScriptFile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCrawlerScriptBytes+1024*1024)
	if err := r.ParseMultipartForm(maxCrawlerScriptBytes + 1024*1024); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("file is required"))
		return
	}
	defer file.Close()

	name := "crawler.py"
	if header != nil && strings.TrimSpace(header.Filename) != "" {
		name = header.Filename
	}
	if _, err := safeCrawlerScriptFileName(name); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// 先读入并校验元信息，再落盘，避免坏脚本覆盖同名旧脚本
	data, meta, err := readCrawlerScript(file, maxCrawlerScriptBytes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	scriptPath, err := a.saveCrawlerScript(r.Context(), name, bytes.NewReader(data), maxCrawlerScriptBytes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scriptPath": scriptPath, "name": meta.Name})
}

func (a *AdminServer) handleImportCrawlerScriptURL(w http.ResponseWriter, r *http.Request) {
	var body importCrawlerScriptURLReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	rawURL := strings.TrimSpace(body.URL)
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		writeErr(w, http.StatusBadRequest, errors.New("脚本链接格式无效"))
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		writeErr(w, http.StatusBadRequest, errors.New("脚本链接仅支持 http:// 或 https://"))
		return
	}
	downloadURL := crawlerScriptDownloadURL(u)

	client := a.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, downloadURL.String(), nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Header.Set("User-Agent", "video-site-crawler-import/1.0")
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("下载脚本失败: HTTP %d", resp.StatusCode))
		return
	}
	if resp.ContentLength > maxCrawlerScriptBytes {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("脚本文件不能超过 %d KiB", maxCrawlerScriptBytes/1024))
		return
	}

	name := strings.TrimSpace(body.FileName)
	if name == "" {
		name = path.Base(downloadURL.Path)
	}
	if name == "." || name == "/" || name == "" {
		name = "crawler.py"
	}
	if _, err := safeCrawlerScriptFileName(name); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// 先读入并校验元信息，再落盘；从原链接更新时远端脚本损坏不会影响本地旧脚本
	data, meta, err := readCrawlerScript(resp.Body, maxCrawlerScriptBytes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	scriptPath, err := a.saveCrawlerScript(r.Context(), name, bytes.NewReader(data), maxCrawlerScriptBytes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scriptPath": scriptPath, "name": meta.Name, "sourceUrl": downloadURL.String()})
}

func crawlerScriptDownloadURL(u *url.URL) *url.URL {
	if raw, ok := githubRawCrawlerScriptURL(u); ok {
		return raw
	}
	return u
}

func githubRawCrawlerScriptURL(u *url.URL) (*url.URL, bool) {
	host := strings.ToLower(u.Hostname())
	if host != "github.com" && host != "www.github.com" {
		return nil, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 5 {
		return nil, false
	}
	if parts[0] == "" || parts[1] == "" || parts[3] == "" || (parts[2] != "blob" && parts[2] != "raw") {
		return nil, false
	}
	rawParts := append([]string{parts[0], parts[1], parts[3]}, parts[4:]...)
	return &url.URL{
		Scheme: "https",
		Host:   "raw.githubusercontent.com",
		Path:   "/" + strings.Join(rawParts, "/"),
	}, true
}

// readCrawlerScript 把脚本内容读入内存并校验大小和元信息，返回内容和元信息。
func readCrawlerScript(r io.Reader, maxBytes int64) ([]byte, scriptcrawler.Metadata, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, scriptcrawler.Metadata{}, err
	}
	if len(data) == 0 {
		return nil, scriptcrawler.Metadata{}, errors.New("脚本文件为空")
	}
	if int64(len(data)) > maxBytes {
		return nil, scriptcrawler.Metadata{}, fmt.Errorf("脚本文件不能超过 %d KiB", maxBytes/1024)
	}
	meta, err := scriptcrawler.ExtractMetadata(string(data))
	if err != nil {
		return nil, scriptcrawler.Metadata{}, fmt.Errorf("脚本元信息无效: %w", err)
	}
	return data, meta, nil
}

func (a *AdminServer) saveCrawlerScript(ctx context.Context, name string, r io.Reader, maxBytes int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fileName, err := safeCrawlerScriptFileName(name)
	if err != nil {
		return "", err
	}
	root, err := a.crawlerScriptImportDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(root, fileName)
	dstAbs, err := filepath.Abs(dst)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if dstAbs != rootAbs && !strings.HasPrefix(dstAbs, rootAbs+string(os.PathSeparator)) {
		return "", errors.New("invalid crawler script path")
	}

	tmp := dstAbs + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	limited := io.LimitReader(r, maxBytes+1)
	written, copyErr := io.Copy(out, limited)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return "", closeErr
	}
	if written <= 0 {
		_ = os.Remove(tmp)
		return "", errors.New("脚本文件为空")
	}
	if written > maxBytes {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("脚本文件不能超过 %d KiB", maxBytes/1024)
	}
	if err := os.Rename(tmp, dstAbs); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return dstAbs, nil
}

func (a *AdminServer) crawlerScriptImportDir() (string, error) {
	base := strings.TrimSpace(a.LocalPreviewDir)
	if base == "" {
		base = filepath.Join(".", "data", "previews")
	}
	root := filepath.Join(filepath.Dir(base), "crawler-scripts")
	return filepath.Abs(root)
}

func safeCrawlerScriptFileName(raw string) (string, error) {
	name := strings.TrimSpace(filepath.Base(raw))
	if name == "" || name == "." || name == string(os.PathSeparator) {
		name = "crawler.py"
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".py" {
		return "", errors.New("目前只支持导入 .py 爬虫脚本")
	}
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	var b strings.Builder
	for _, r := range stem {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	cleanStem := strings.Trim(b.String(), "._-")
	if cleanStem == "" {
		cleanStem = "crawler"
	}
	return cleanStem + ".py", nil
}

func (a *AdminServer) handleRunCrawler(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	d, err := a.Catalog.GetDrive(r.Context(), id)
	if err != nil || d == nil || !isCrawlerDriveKind(d.Kind) || d.Credentials == nil || strings.TrimSpace(d.Credentials["script_path"]) == "" {
		http.Error(w, "crawler not found", http.StatusNotFound)
		return
	}
	status := a.nightlyJobStatus()
	if status.Running || status.Queued {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":       true,
			"accepted": false,
			"message":  fullScanBusyMessage,
			"status":   status,
		})
		return
	}
	accepted := true
	if a.OnScanRequested != nil {
		accepted = a.OnScanRequested(id)
	}
	resp := map[string]any{"ok": true, "accepted": accepted}
	if !accepted {
		resp["message"] = driveTaskBusyMessage
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (a *AdminServer) handleUploadCrawlerVideos(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	d, err := a.Catalog.GetDrive(r.Context(), id)
	if err != nil || d == nil || !isConfiguredCrawlerDrive(d) {
		http.Error(w, "crawler not found", http.StatusNotFound)
		return
	}
	status := a.nightlyJobStatus()
	if status.Running || status.Queued {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":       true,
			"accepted": false,
			"message":  fullScanBusyMessage,
			"status":   status,
		})
		return
	}

	assets, err := a.Catalog.CountCrawlerAssets(r.Context(), d.ID, crawlerVideoIDPrefixes(d))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	generation := DriveGenerationStatuses{}
	if a.GetDriveGenerationStatuses != nil {
		generation = a.GetDriveGenerationStatuses()[d.ID]
	}
	if reason := crawlerUploadBlockedReason(d, assets, generation); reason != "" {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":       true,
			"accepted": false,
			"message":  reason,
		})
		return
	}

	accepted := true
	message := ""
	if a.OnCrawlerUploadRequested != nil {
		accepted, message = a.OnCrawlerUploadRequested(id)
	}
	resp := map[string]any{"ok": true, "accepted": accepted}
	if !accepted {
		if strings.TrimSpace(message) == "" {
			message = driveTaskBusyMessage
		}
		resp["message"] = message
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func crawlerUploadBlockedReason(d *catalog.Drive, assets catalog.CrawlerAssetCounts, generation DriveGenerationStatuses) string {
	if d == nil || !isConfiguredCrawlerDrive(d) {
		return "爬虫不存在"
	}
	if strings.TrimSpace(d.Credentials["upload_drive_id"]) == "" {
		return "请先配置上传网盘"
	}
	if assets.Local <= 0 {
		return "没有待上传的本地视频"
	}
	if crawlerGenerationBusy(generation) {
		return "当前爬虫有正在进行的任务，请稍后重试"
	}
	if assets.Fingerprint.Pending > 0 {
		return "还有待生成的视频指纹"
	}
	if assets.Fingerprint.Failed > 0 {
		return "存在指纹生成失败的视频，请先重试或处理失败项"
	}
	if d.TeaserEnabled {
		if assets.Teaser.Pending > 0 {
			return "还有待生成的预览视频"
		}
		if assets.Teaser.Failed > 0 {
			return "存在预览视频生成失败的视频，请先重试或处理失败项"
		}
	}
	return ""
}

func crawlerGenerationBusy(g DriveGenerationStatuses) bool {
	return generationBusy(g.Scan) ||
		generationBusy(g.Thumbnail) ||
		generationBusy(g.Preview) ||
		generationBusy(g.Fingerprint) ||
		generationBusy(g.Upload)
}

func generationBusy(g GenerationStatus) bool {
	switch strings.TrimSpace(g.State) {
	case "", "idle":
		return false
	default:
		return true
	}
}

func (a *AdminServer) handleStopCrawlerTasks(w http.ResponseWriter, r *http.Request) {
	a.handleStopDriveTasks(w, r)
}

func (a *AdminServer) handleDeleteCrawler(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	d, err := a.Catalog.GetDrive(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if !isCrawlerDriveKind(d.Kind) {
		http.Error(w, "crawler not found", http.StatusNotFound)
		return
	}
	if a.OnStopDriveTasks != nil {
		a.OnStopDriveTasks(id)
	}

	deletedScript, scriptErr := a.removeImportedCrawlerScript(d)
	if d.Credentials == nil {
		d.Credentials = map[string]string{}
	}
	delete(d.Credentials, "script_path")
	delete(d.Credentials, "proxy")
	delete(d.Credentials, "target_new")
	delete(d.Credentials, "builtin")
	delete(d.Credentials, "python_path")
	delete(d.Credentials, "config_json")
	d.Status = "disconnected"
	d.LastError = ""
	if err := a.Catalog.UpsertDrive(r.Context(), d); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	resp := map[string]any{
		"ok":            true,
		"deletedVideos": 0,
		"deletedScript": deletedScript,
	}
	if scriptErr != nil {
		resp["warning"] = scriptErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func isCrawlerDriveKind(kind string) bool {
	return kind == scriptcrawler.Kind
}

func isSupportedDriveKind(kind string) bool {
	switch kind {
	case "quark", "p115", "p123", "pikpak", "wopan", "guangyapan", "onedrive", "googledrive", "localstorage", scriptcrawler.Kind:
		return true
	default:
		return false
	}
}

func isConfiguredCrawlerDrive(d *catalog.Drive) bool {
	return d != nil &&
		isCrawlerDriveKind(d.Kind) &&
		d.Credentials != nil &&
		strings.TrimSpace(d.Credentials["script_path"]) != ""
}

func (a *AdminServer) removeImportedCrawlerScript(d *catalog.Drive) (bool, error) {
	if d == nil || d.Credentials == nil {
		return false, nil
	}
	scriptPath := strings.TrimSpace(d.Credentials["script_path"])
	if scriptPath == "" {
		return false, nil
	}
	scriptAbs, err := filepath.Abs(scriptPath)
	if err != nil {
		return false, err
	}
	rootAbs, err := a.crawlerScriptImportDir()
	if err != nil {
		return false, err
	}
	if scriptAbs == rootAbs || !strings.HasPrefix(scriptAbs, rootAbs+string(os.PathSeparator)) {
		return false, nil
	}
	if err := os.Remove(scriptAbs); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// strmAllowOutsideRootForDrive 返回 localstorage 的 .strm 越root开关；
// 其它 kind 返回 nil（JSON 省略）。未配置时默认 false。
func strmAllowOutsideRootForDrive(d *catalog.Drive) *bool {
	if d == nil || d.Kind != "localstorage" {
		return nil
	}
	result := false
	if d.Credentials != nil {
		if v, err := strconv.ParseBool(strings.TrimSpace(d.Credentials["strm_allow_outside_root"])); err == nil {
			result = v
		}
	}
	return &result
}

func googleDriveUseOnlineAPIForDrive(d *catalog.Drive) *bool {
	if d == nil || d.Kind != "googledrive" {
		return nil
	}
	result := true
	if d.Credentials == nil {
		return &result
	}
	raw := strings.TrimSpace(d.Credentials["use_online_api"])
	if raw == "" {
		return &result
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return &result
	}
	result = v
	return &result
}

func googleDriveOpenListAPIURLForDrive(d *catalog.Drive) string {
	if d == nil || d.Kind != "googledrive" || d.Credentials == nil {
		return ""
	}
	return strings.TrimSpace(d.Credentials["api_url_address"])
}

func mergeGoogleDriveCredentials(existing *catalog.Drive, incoming map[string]string) map[string]string {
	merged := mergeNonEmptyCredentials(existing, incoming)
	if _, ok := incoming["api_url_address"]; ok && strings.TrimSpace(incoming["api_url_address"]) == "" {
		delete(merged, "api_url_address")
	}
	return merged
}

// mergeNonEmptyCredentials 逐键合并凭证：incoming 里非空的键覆盖旧值，
// 空值/缺失的键沿用旧值。googledrive、localstorage 和 guangyapan 的编辑表单都依赖
// 这个语义（留空 = 不修改）。
func mergeNonEmptyCredentials(existing *catalog.Drive, incoming map[string]string) map[string]string {
	merged := map[string]string{}
	if existing != nil {
		for k, v := range existing.Credentials {
			merged[k] = v
		}
	}
	for k, v := range incoming {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		value := strings.TrimSpace(v)
		if value == "" {
			continue
		}
		merged[key] = value
	}
	return merged
}

func mergeScriptCrawlerCredentials(existing *catalog.Drive, incoming map[string]string) (map[string]string, error) {
	merged := map[string]string{}
	if existing != nil {
		for k, v := range existing.Credentials {
			merged[k] = v
		}
	}
	for k, v := range incoming {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		value := strings.TrimSpace(v)
		switch key {
		case "proxy":
			proxy, err := normalizeCrawlerProxyURL(value, "脚本爬虫")
			if err != nil {
				return nil, err
			}
			if proxy == "" {
				delete(merged, key)
			} else {
				merged[key] = proxy
			}
		case "target_new":
			if value == "" {
				delete(merged, key)
				continue
			}
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("脚本爬虫 target_new 必须是正整数")
			}
			merged[key] = strconv.Itoa(n)
		case "script_path":
			if value == "" {
				if existing == nil {
					delete(merged, key)
				}
				continue
			}
			merged[key] = value
		case "builtin", "python_path", "config_json":
			delete(merged, key)
		default:
			if value == "" {
				delete(merged, key)
			} else {
				merged[key] = value
			}
		}
	}
	if strings.TrimSpace(merged["script_path"]) == "" {
		return nil, fmt.Errorf("脚本爬虫必须填写 script_path")
	}
	delete(merged, "builtin")
	delete(merged, "python_path")
	delete(merged, "config_json")
	return merged, nil
}

func normalizeCrawlerProxyURL(raw, label string) (string, error) {
	proxy := strings.TrimSpace(raw)
	if proxy == "" {
		return "", nil
	}
	u, err := url.Parse(proxy)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%s 代理地址格式无效，请填写类似 http://127.0.0.1:7890 的地址", label)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return proxy, nil
	default:
		return "", fmt.Errorf("%s 代理地址仅支持 http://、https://、socks5:// 或 socks5h://", label)
	}
}

func (a *AdminServer) handleDeleteDrive(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body deleteDriveReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !body.DeleteVideos {
		http.Error(w, "deleteVideos=true is required when deleting a drive", http.StatusBadRequest)
		return
	}

	deletedVideos := 0
	if a.OnDriveDeleteCleanup == nil {
		http.Error(w, "drive video cleanup is not available", http.StatusInternalServerError)
		return
	}
	removed, err := a.OnDriveDeleteCleanup(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	deletedVideos = removed

	if err := a.Catalog.DeleteDrive(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if a.OnDriveRemoved != nil {
		a.OnDriveRemoved(id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deletedVideos": deletedVideos})
}

type deleteDriveReq struct {
	DeleteVideos bool `json:"deleteVideos"`
}

func (a *AdminServer) handleRescan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	status := a.nightlyJobStatus()
	if status.Running || status.Queued {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":       true,
			"accepted": false,
			"message":  fullScanBusyMessage,
			"status":   status,
		})
		return
	}

	accepted := true
	if a.OnScanRequested != nil {
		accepted = a.OnScanRequested(id)
	}
	resp := map[string]any{"ok": true, "accepted": accepted}
	if !accepted {
		resp["message"] = driveTaskBusyMessage
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (a *AdminServer) handleStopDriveTasks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	stopped := false
	if a.OnStopDriveTasks != nil {
		stopped = a.OnStopDriveTasks(id)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":      true,
		"stopped": stopped,
	})
}

// handleStartDriveTranscode 手动开启某盘的浏览器兼容性转码。
// 转码默认不开启、从不自动运行；本接口是唯一入口。
func (a *AdminServer) handleStartDriveTranscode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if a.OnStartDriveTranscode == nil {
		writeErr(w, http.StatusNotImplemented, errors.New("transcode not supported"))
		return
	}
	accepted, message := a.OnStartDriveTranscode(id)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":       true,
		"accepted": accepted,
		"message":  message,
	})
}

// handleStopDriveTranscode 手动停止某盘正在进行的转码任务。
func (a *AdminServer) handleStopDriveTranscode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	stopped := false
	if a.OnStopDriveTranscode != nil {
		stopped = a.OnStopDriveTranscode(id)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":      true,
		"stopped": stopped,
	})
}

func (a *AdminServer) p123QRClient() *p123.QRClient {
	return p123.NewQRClient(p123.QRConfig{
		UserAPIBaseURL: a.P123UserAPIBaseURL,
		HTTPClient:     a.P123HTTPClient,
	})
}

func (a *AdminServer) handleP123QRStart(w http.ResponseWriter, r *http.Request) {
	session, err := a.p123QRClient().Generate(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, session)
}

func (a *AdminServer) handleP123QRStatus(w http.ResponseWriter, r *http.Request) {
	uniID := chi.URLParam(r, "uniID")
	loginUUID := r.URL.Query().Get("loginUuid")
	if strings.TrimSpace(uniID) == "" || strings.TrimSpace(loginUUID) == "" {
		http.Error(w, "uniID and loginUuid are required", http.StatusBadRequest)
		return
	}
	status, err := a.p123QRClient().Poll(r.Context(), loginUUID, uniID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, status)
}

func (a *AdminServer) wopanQRClient() *wopan.QRClient {
	return wopan.NewQRClient(wopan.QRConfig{
		APIBaseURL: a.WopanQRAPIBaseURL,
		HTTPClient: a.WopanQRHTTPClient,
	})
}

func (a *AdminServer) handleWopanQRStart(w http.ResponseWriter, r *http.Request) {
	session, err := a.wopanQRClient().Generate(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, session)
}

func (a *AdminServer) handleWopanQRStatus(w http.ResponseWriter, r *http.Request) {
	uuid := chi.URLParam(r, "uuid")
	if strings.TrimSpace(uuid) == "" {
		http.Error(w, "uuid is required", http.StatusBadRequest)
		return
	}
	status, err := a.wopanQRClient().Poll(r.Context(), uuid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, status)
}

func (a *AdminServer) guangYaPanQRClient() *guangyapan.QRClient {
	return guangyapan.NewQRClient(guangyapan.QRConfig{
		AccountBaseURL: a.GuangYaPanAccountBaseURL,
		HTTPClient:     a.GuangYaPanHTTPClient,
	})
}

func (a *AdminServer) handleGuangYaPanQRStart(w http.ResponseWriter, r *http.Request) {
	session, err := a.guangYaPanQRClient().Generate(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, session)
}

func (a *AdminServer) handleGuangYaPanQRStatus(w http.ResponseWriter, r *http.Request) {
	deviceCode := r.URL.Query().Get("deviceCode")
	if strings.TrimSpace(deviceCode) == "" {
		http.Error(w, "deviceCode is required", http.StatusBadRequest)
		return
	}
	status, err := a.guangYaPanQRClient().Poll(r.Context(), deviceCode)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, status)
}

// handleRunNightlyJob 触发一次完整的凌晨流水线（不论当前时间，不论今日是否已跑）。
// 立即返回 202；进度通过 backend 日志和下次 GET /admin/api/drives 的状态变化观察。
// 流水线已在跑或已排队时，Runner 会拒绝重复触发。
func (a *AdminServer) handleRunNightlyJob(w http.ResponseWriter, r *http.Request) {
	accepted := false
	if a.OnRunNightlyJob != nil {
		accepted = a.OnRunNightlyJob()
	}
	resp := map[string]any{
		"ok":       true,
		"accepted": accepted,
		"status":   a.nightlyJobStatus(),
	}
	if !accepted {
		resp["message"] = fullScanBusyMessage
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (a *AdminServer) handleNightlyJobStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.nightlyJobStatus())
}

func (a *AdminServer) handleStopAllTasks(w http.ResponseWriter, r *http.Request) {
	stoppedDrives := 0
	if a.OnStopAllTasks != nil {
		stoppedDrives = a.OnStopAllTasks()
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":            true,
		"stoppedDrives": stoppedDrives,
		"status":        a.nightlyJobStatus(),
	})
}

func (a *AdminServer) nightlyJobStatus() NightlyJobStatus {
	if a.GetNightlyJobStatus == nil {
		return NightlyJobStatus{State: "idle"}
	}
	status := a.GetNightlyJobStatus()
	if status.State == "" {
		status.State = "idle"
	}
	return status
}

// teaserEnabledReq 是 POST /admin/api/drives/{id}/teaser-enabled 的入参。
type teaserEnabledReq struct {
	Enabled bool `json:"enabled"`
}

// handleSetDriveTeaserEnabled 切换某盘的预览视频生成开关。
//
// 行为：
//   - 写 catalog.drives.teaser_enabled
//   - 调 OnTeaserEnabledChanged（main 注入；从关到开时会重新入队 pending 预览视频）
//   - 返回切换后的新值，方便前端乐观更新但又能以服务端为准
//
// 与 upsertDrive 的区别：那条接口要重传 kind / name / rootId 等，开关切换不该
// 牵连这些字段（顺手覆盖凭证或 rootID 容易出 bug）。所以单独走一条。
func (a *AdminServer) handleSetDriveTeaserEnabled(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	var body teaserEnabledReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.Catalog.SetDriveTeaserEnabled(r.Context(), id, body.Enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "drive not found", http.StatusNotFound)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if a.OnTeaserEnabledChanged != nil {
		a.OnTeaserEnabledChanged(id, body.Enabled)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "teaserEnabled": body.Enabled})
}

// skipDirsReq 是 POST /admin/api/drives/{id}/skip-dirs 的入参。
//
// 整体覆盖语义：传啥就保存啥（不是增量合并）。dirIds 可以是 nil/空数组 表示
// 清空跳过列表。
type skipDirsReq struct {
	DirIDs []string `json:"dirIds"`
}

// handleSetDriveSkipDirs 更新某盘的"扫描跳过目录"集合。
//
// 与 upsertDrive 的区别：那条接口要重传 kind / name / rootId / credentials 等字段，
// 用户保存跳过目录时不该牵连这些。所以单独走一条 PUT 风格接口。
//
// 行为：
//   - 写 catalog.drives.skip_dir_ids（整体覆盖）
//   - 不重新触发扫描；下次 nightly Phase 1 或 admin 手动重扫时生效
//   - 返回保存后的列表，方便前端乐观更新但又能以服务端为准
func (a *AdminServer) handleSetDriveSkipDirs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	var body skipDirsReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// 去重 + trim 空白；前端理论上保证清洁，这里再防一道。
	seen := map[string]struct{}{}
	cleaned := make([]string, 0, len(body.DirIDs))
	for _, raw := range body.DirIDs {
		s := raw
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		cleaned = append(cleaned, s)
	}
	if err := a.Catalog.SetDriveSkipDirIDs(r.Context(), id, cleaned); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "drive not found", http.StatusNotFound)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "skipDirIds": cleaned})
}

// handleListDriveDirTree 列出某 drive 在指定父目录下的直接子目录。
//
// 查询参数 ?parent=<dirID>：留空 = drive 的 RootID。前端按需展开调用 ——
// 每展开一层调一次，避免一次性递归整个网盘（115 限频会很难受）。
//
// 错误：drive 未挂载 / List 失败 → 500，body 是错误文案；前端展示给用户。
func (a *AdminServer) handleListDriveDirTree(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	if a.ListDriveDirChildren == nil {
		writeErr(w, http.StatusInternalServerError, errors.New("dirtree not configured"))
		return
	}
	parent := r.URL.Query().Get("parent")
	entries, err := a.ListDriveDirChildren(r.Context(), id, parent)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if entries == nil {
		entries = []DriveDirEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func (a *AdminServer) handleAdminListVideos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 100
	}
	items, total, err := a.Catalog.ListVideos(r.Context(), catalog.ListParams{
		Keyword:  q.Get("keyword"),
		DriveID:  q.Get("driveId"),
		Page:     page,
		PageSize: size,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if a.GetPreviewGenerationVideoIDs != nil {
		generating := a.GetPreviewGenerationVideoIDs()
		for _, item := range items {
			if item != nil && generating[item.ID] {
				item.PreviewStatus = "generating"
			}
		}
	}
	videoIDs := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil {
			videoIDs = append(videoIDs, item.ID)
		}
	}
	tagMetadata, err := a.Catalog.ListVideoTagMetadata(r.Context(), videoIDs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	mappedItems := mapAdminVideos(items)
	for i := range mappedItems {
		metadata := tagMetadata[mappedItems[i].ID]
		if len(metadata) == 0 {
			continue
		}
		mappedItems[i].TagSources = make(map[string]string, len(metadata))
		mappedItems[i].TagEvidence = make(map[string]string, len(metadata))
		for label, item := range metadata {
			mappedItems[i].TagSources[label] = item.Source
			if item.Evidence != "" {
				mappedItems[i].TagEvidence[label] = item.Evidence
			}
		}
		if len(mappedItems[i].TagEvidence) == 0 {
			mappedItems[i].TagEvidence = nil
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": mappedItems,
		"total": total,
		"page":  page,
		"size":  size,
	})
}

// handleVideoStats 返回后台视频管理两个标签页的计数（当前/拉黑）。
func (a *AdminServer) handleVideoStats(w http.ResponseWriter, r *http.Request) {
	current, blacklisted, err := a.Catalog.VideoManagementCounts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"current":     current,
		"blacklisted": blacklisted,
	})
}

// handleListBlacklist 分页返回黑名单（墓碑）视频。
func (a *AdminServer) handleListBlacklist(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 100
	}
	items, total, err := a.Catalog.ListDeletedVideos(r.Context(), catalog.ListParams{
		Keyword:  q.Get("keyword"),
		DriveID:  q.Get("driveId"),
		Page:     page,
		PageSize: size,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": total,
		"page":  page,
		"size":  size,
	})
}

func (a *AdminServer) handleStartBlacklistSourceDelete(w http.ResponseWriter, r *http.Request) {
	var body BlacklistSourceDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := normalizeBlacklistSourceDeleteRequest(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	accepted := false
	if a.OnStartBlacklistSourceDelete != nil {
		accepted = a.OnStartBlacklistSourceDelete(body)
	}
	resp := map[string]any{
		"ok":       true,
		"accepted": accepted,
		"status":   a.blacklistSourceDeleteStatus(r.Context()),
	}
	if !accepted {
		resp["message"] = "黑名单源文件删除任务已在运行"
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func normalizeBlacklistSourceDeleteRequest(req *BlacklistSourceDeleteRequest) error {
	if req == nil {
		return errors.New("blacklist source delete request is required")
	}
	seen := make(map[string]bool, len(req.IDs))
	ids := req.IDs[:0]
	for _, id := range req.IDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	req.IDs = ids

	hasIDs := len(req.IDs) > 0
	switch {
	case req.DeleteAllSources && hasIDs:
		return errors.New("deleteAllSources and ids cannot be used together")
	case !req.DeleteAllSources && !hasIDs:
		return errors.New("deleteAllSources=true or ids is required")
	default:
		return nil
	}
}

func (a *AdminServer) handleBlacklistSourceDeleteStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.blacklistSourceDeleteStatus(r.Context()))
}

func (a *AdminServer) blacklistSourceDeleteStatus(ctx context.Context) BlacklistSourceDeleteStatus {
	var status BlacklistSourceDeleteStatus
	if a.GetBlacklistSourceDeleteStatus == nil {
		status.State = "idle"
	} else {
		status = a.GetBlacklistSourceDeleteStatus()
	}
	if status.State == "" {
		status.State = "idle"
	}
	if a.Catalog != nil {
		if pending, err := a.Catalog.CountDeletedVideosPendingSourceDeletion(ctx); err == nil {
			status.Pending = pending
		}
	}
	return status
}

// handleRemoveBlacklist 允许视频在后续手动/定时任务中重新入库，不会立即触发
// 扫盘或爬取。不可重新发现的来源会返回 409。
func (a *AdminServer) handleRemoveBlacklist(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := a.Catalog.RemoveDeletedVideo(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		if errors.Is(err, catalog.ErrDeletedVideoNotRestorable) {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- User Management ----------

type createUserReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

type userDTO struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	Banned    bool   `json:"banned"`
	CreatedAt int64  `json:"createdAt"`
}

func (a *AdminServer) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.Catalog.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]userDTO, 0, len(users))
	for _, u := range users {
		out = append(out, userDTO{
			ID: u.ID, Username: u.Username, Role: u.Role,
			Banned: u.Banned, CreatedAt: u.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *AdminServer) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var body createUserReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	username := strings.TrimSpace(body.Username)
	if username == "" {
		http.Error(w, "username is required", http.StatusBadRequest)
		return
	}
	if len(body.Password) < 6 {
		http.Error(w, "password must be at least 6 characters", http.StatusBadRequest)
		return
	}
	role := body.Role
	if role == "" {
		role = "user"
	}
	if role != "admin" && role != "user" {
		http.Error(w, "role must be admin or user", http.StatusBadRequest)
		return
	}

	hashed, err := auth.HashPassword(body.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	id, err := a.Catalog.CreateUser(r.Context(), username, hashed, role)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			http.Error(w, "username already exists", http.StatusConflict)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

func (a *AdminServer) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	target, err := a.Catalog.GetUserByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if currentSessionUserID(r.Context(), a.Catalog, r) == id {
		http.Error(w, "cannot delete yourself", http.StatusBadRequest)
		return
	}
	if target.Role == "admin" {
		admins, err := a.Catalog.CountAdmins(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if admins <= 1 {
			http.Error(w, "cannot delete the last admin", http.StatusBadRequest)
			return
		}
	}
	if err := a.Catalog.DeleteUser(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := a.Catalog.DeleteSessionsForUser(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *AdminServer) handleBanUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	target, err := a.Catalog.GetUserByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if currentSessionUserID(r.Context(), a.Catalog, r) == id {
		http.Error(w, "cannot ban yourself", http.StatusBadRequest)
		return
	}
	if target.Role == "admin" && !target.Banned {
		activeAdmins, err := a.Catalog.CountActiveAdmins(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if activeAdmins <= 1 {
			http.Error(w, "cannot ban the last active admin", http.StatusBadRequest)
			return
		}
	}
	if err := a.Catalog.SetUserBanned(r.Context(), id, true); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := a.Catalog.DeleteSessionsForUser(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *AdminServer) handleUnbanUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	if err := a.Catalog.SetUserBanned(r.Context(), id, false); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *AdminServer) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(body.Password) < 6 {
		http.Error(w, "password must be at least 6 characters", http.StatusBadRequest)
		return
	}
	hashed, err := auth.HashPassword(body.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := a.Catalog.UpdateUserPassword(r.Context(), id, hashed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := a.Catalog.DeleteSessionsForUser(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func currentSessionUserID(ctx context.Context, cat *catalog.Catalog, r *http.Request) int64 {
	if cat == nil || r == nil {
		return 0
	}
	c, err := r.Cookie("vs_admin")
	if err != nil {
		return 0
	}
	ok, userID, err := cat.ValidateSession(ctx, c.Value)
	if err != nil || !ok {
		return 0
	}
	return userID
}

// ---------- IP Ban Management ----------

type bannedIPDTO struct {
	IP        string `json:"ip"`
	Reason    string `json:"reason"`
	CreatedAt int64  `json:"createdAt"`
}

func (a *AdminServer) handleListBannedIPs(w http.ResponseWriter, r *http.Request) {
	ips, err := a.Catalog.ListBannedLoginIPs(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]bannedIPDTO, 0, len(ips))
	for _, ip := range ips {
		out = append(out, bannedIPDTO{IP: ip.IP, Reason: ip.Reason, CreatedAt: ip.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *AdminServer) handleUnbanIP(w http.ResponseWriter, r *http.Request) {
	ip := chi.URLParam(r, "ip")
	if err := a.Catalog.UnbanLoginIP(r.Context(), ip); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "IP not found", http.StatusNotFound)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *AdminServer) handleListTags(w http.ResponseWriter, r *http.Request) {
	tags, err := a.Catalog.ListTags(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, tags)
}

type createTagReq struct {
	Label string `json:"label"`
}

type updateTagReq struct {
	MatchRules tagging.Rule `json:"matchRules"`
}

func (a *AdminServer) handleCreateTag(w http.ResponseWriter, r *http.Request) {
	var body createTagReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	classified, err := a.Catalog.CreateTagAndClassify(r.Context(), body.Label, nil, "user")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"label":      body.Label,
		"classified": classified,
	})
}

func (a *AdminServer) handleUpdateTag(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("invalid tag id"))
		return
	}
	var body updateTagReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	tag, err := a.Catalog.UpdateTag(r.Context(), id, body.MatchRules)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, http.StatusNotFound, err)
		} else {
			writeErr(w, http.StatusBadRequest, err)
		}
		return
	}
	if err := a.Catalog.RunPostStartupTagMaintenance(r.Context()); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tag": tag})
}

func (a *AdminServer) handleStartTagRetag(w http.ResponseWriter, _ *http.Request) {
	if a.OnStartTagRetag == nil {
		writeErr(w, http.StatusNotImplemented, errors.New("tag maintenance is not configured"))
		return
	}
	if !a.OnStartTagRetag() {
		writeJSON(w, http.StatusConflict, map[string]any{"accepted": false})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

func (a *AdminServer) handleTagJobStatus(w http.ResponseWriter, _ *http.Request) {
	status := TagJobStatus{State: "idle"}
	if a.GetTagJobStatus != nil {
		status = a.GetTagJobStatus()
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *AdminServer) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("invalid tag id"))
		return
	}
	removedVideos, err := a.Catalog.DeleteTag(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeErr(w, http.StatusNotFound, err)
		default:
			writeErr(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removedVideos": removedVideos})
}

type updateVideoReq struct {
	Title       string   `json:"title"`
	Author      string   `json:"author"`
	Tags        []string `json:"tags"`
	Badges      []string `json:"badges"`
	Description string   `json:"description"`
	Thumbnail   string   `json:"thumbnail"`
	Quality     string   `json:"quality"`
	DurationSec int      `json:"durationSeconds"`
}

type adminVideoDTO struct {
	ID                string            `json:"id"`
	DriveID           string            `json:"driveId"`
	FileID            string            `json:"fileId"`
	FileName          string            `json:"fileName"`
	ContentHash       string            `json:"contentHash"`
	SampledSHA256     string            `json:"sampledSha256"`
	FingerprintStatus string            `json:"fingerprintStatus"`
	FingerprintError  string            `json:"fingerprintError"`
	ParentID          string            `json:"parentId"`
	Title             string            `json:"title"`
	Author            string            `json:"author"`
	Tags              []string          `json:"tags"`
	TagSources        map[string]string `json:"tagSources,omitempty"`
	TagEvidence       map[string]string `json:"tagEvidence,omitempty"`
	DurationSeconds   int               `json:"durationSeconds"`
	Size              int64             `json:"size"`
	Ext               string            `json:"ext"`
	Quality           string            `json:"quality"`
	ThumbnailURL      string            `json:"thumbnailUrl"`
	PreviewFileID     string            `json:"previewFileId"`
	PreviewLocal      string            `json:"previewLocal"`
	PreviewStatus     string            `json:"previewStatus"`
	TranscodeStatus   string            `json:"transcodeStatus"`
	TranscodeError    string            `json:"transcodeError"`
	TranscodedFileID  string            `json:"transcodedFileId"`
	TranscodedSize    int64             `json:"transcodedSize"`
	Views             int               `json:"views"`
	LastViewedAt      time.Time         `json:"lastViewedAt"`
	Favorites         int               `json:"favorites"`
	Comments          int               `json:"comments"`
	Likes             int               `json:"likes"`
	Dislikes          int               `json:"dislikes"`
	Hidden            bool              `json:"hidden"`
	Badges            []string          `json:"badges"`
	Description       string            `json:"description"`
	PublishedAt       time.Time         `json:"publishedAt"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
}

func mapAdminVideo(v *catalog.Video) adminVideoDTO {
	if v == nil {
		return adminVideoDTO{}
	}
	return adminVideoDTO{
		ID:                v.ID,
		DriveID:           v.DriveID,
		FileID:            v.FileID,
		FileName:          v.FileName,
		ContentHash:       v.ContentHash,
		SampledSHA256:     v.SampledSHA256,
		FingerprintStatus: v.FingerprintStatus,
		FingerprintError:  v.FingerprintError,
		ParentID:          v.ParentID,
		Title:             v.Title,
		Author:            v.Author,
		Tags:              v.Tags,
		DurationSeconds:   v.DurationSeconds,
		Size:              v.Size,
		Ext:               v.Ext,
		Quality:           v.Quality,
		ThumbnailURL:      v.ThumbnailURL,
		PreviewFileID:     v.PreviewFileID,
		PreviewLocal:      v.PreviewLocal,
		PreviewStatus:     v.PreviewStatus,
		TranscodeStatus:   v.TranscodeStatus,
		TranscodeError:    v.TranscodeError,
		TranscodedFileID:  v.TranscodedFileID,
		TranscodedSize:    v.TranscodedSize,
		Views:             v.Views,
		LastViewedAt:      v.LastViewedAt,
		Favorites:         v.Favorites,
		Comments:          v.Comments,
		Likes:             v.Likes,
		Dislikes:          v.Dislikes,
		Hidden:            v.Hidden,
		Badges:            v.Badges,
		Description:       v.Description,
		PublishedAt:       v.PublishedAt,
		CreatedAt:         v.CreatedAt,
		UpdatedAt:         v.UpdatedAt,
	}
}

func mapAdminVideos(vs []*catalog.Video) []adminVideoDTO {
	out := make([]adminVideoDTO, 0, len(vs))
	for _, v := range vs {
		out = append(out, mapAdminVideo(v))
	}
	return out
}

func (a *AdminServer) handleUpdateVideo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body updateVideoReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	v, err := a.Catalog.GetVideo(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if body.Title != "" {
		v.Title = body.Title
	}
	if body.Author != "" {
		v.Author = body.Author
	}
	if body.Badges != nil {
		v.Badges = body.Badges
	}
	if body.Description != "" {
		v.Description = body.Description
	}
	if body.Thumbnail != "" {
		v.ThumbnailURL = body.Thumbnail
	}
	if body.Quality != "" {
		v.Quality = body.Quality
	}
	if body.DurationSec > 0 {
		v.DurationSeconds = body.DurationSec
	}
	if err := a.Catalog.UpsertVideo(r.Context(), v); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if body.Tags != nil {
		if err := a.Catalog.SetManualVideoTags(r.Context(), id, body.Tags); err != nil {
			if errors.Is(err, catalog.ErrUnknownTag) {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		v, err = a.Catalog.GetVideo(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, mapAdminVideo(v))
}

func (a *AdminServer) handleDeleteVideo(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, errors.New("invalid video id"))
		return
	}
	var body deleteVideoReq
	if r.Body != nil {
		defer r.Body.Close()
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	var (
		result DeleteVideoResult
		err    error
	)
	if a.OnDeleteVideo != nil {
		result, err = a.OnDeleteVideo(r.Context(), id, body.DeleteSource)
	} else {
		err = a.Catalog.DeleteVideoWithTombstone(r.Context(), id)
		result = DeleteVideoResult{OK: err == nil}
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !result.OK {
		result.OK = true
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *AdminServer) handleRegenPreview(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if a.OnRegenPreview != nil {
		a.OnRegenPreview(id)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *AdminServer) handleRegenAllPreviews(w http.ResponseWriter, r *http.Request) {
	if a.OnRegenAllPreviews != nil {
		a.OnRegenAllPreviews()
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *AdminServer) handleRegenFailedPreviews(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if a.OnRegenFailedPreviews != nil {
		a.OnRegenFailedPreviews(id)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

// handleRegenFailedThumbnails 触发某 drive 下所有 thumbnail_status=failed 的封面
// 重新入队生成。和 handleRegenFailedPreviews 行为对称（一个管预览视频，一个管封面）。
//
// 立即返回 202；实际执行在后台 goroutine 跑，状态可在下次 GET /admin/api/drives
// 的 thumbnailFailedCount / thumbnailGenerationStatus 看变化。
func (a *AdminServer) handleRegenFailedThumbnails(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if a.OnRegenFailedThumbnails != nil {
		a.OnRegenFailedThumbnails(id)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

// handleRegenFailedFingerprints triggers regeneration for all failed sampled
// fingerprints on a drive. It mirrors the failed preview-video/thumbnail retry endpoints.
func (a *AdminServer) handleRegenFailedFingerprints(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if a.OnRegenFailedFingerprints != nil {
		a.OnRegenFailedFingerprints(id)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

// ---------- Settings ----------

// settingsDTO 是 GET/PUT /admin/api/settings 的入参/出参。
//
// 注意：早期的全局 previewEnabled 字段已经下沉为每盘 teaser_enabled，
// 不再出现在这里；前端要切换某个盘的预览视频生成请用 POST /admin/api/drives 上传
// teaserEnabled 字段。settings 目前保留全站主题和标签自动生成开关。
type settingsDTO struct {
	Theme                   string `json:"theme"`
	AutoGenerateTagsEnabled bool   `json:"autoGenerateTagsEnabled"`
}

func (a *AdminServer) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	theme := "dark"
	if a.GetTheme != nil {
		if v := a.GetTheme(); v != "" {
			theme = v
		}
	}
	autoGenerateTagsEnabled := false
	if a.Catalog != nil {
		enabled, err := a.Catalog.AutoGenerateTagsEnabled(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		autoGenerateTagsEnabled = enabled
	}
	writeJSON(w, http.StatusOK, settingsDTO{
		Theme:                   theme,
		AutoGenerateTagsEnabled: autoGenerateTagsEnabled,
	})
}

func (a *AdminServer) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	if v, ok := raw["theme"]; ok && a.SetTheme != nil {
		var theme string
		if err := json.Unmarshal(v, &theme); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if theme != "" {
			if err := a.SetTheme(theme); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
		}
	}
	if v, ok := raw["autoGenerateTagsEnabled"]; ok && a.Catalog != nil {
		var enabled bool
		if err := json.Unmarshal(v, &enabled); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := a.Catalog.SetAutoGenerateTagsEnabled(r.Context(), enabled); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}

	// 回显当前值
	resp := settingsDTO{AutoGenerateTagsEnabled: false}
	if a.GetTheme != nil {
		resp.Theme = a.GetTheme()
	}
	if a.Catalog != nil {
		enabled, err := a.Catalog.AutoGenerateTagsEnabled(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		resp.AutoGenerateTagsEnabled = enabled
	}
	writeJSON(w, http.StatusOK, resp)
}
