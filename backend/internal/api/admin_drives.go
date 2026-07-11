package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/video-site/backend/internal/catalog"
	"github.com/video-site/backend/internal/drives/scriptcrawler"
)

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
		SkipDirIDs  []string `json:"skipDirIds"`
		LastCrawlAt int64    `json:"lastCrawlAt,omitempty"`
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
	} else if body.Kind == "localstorage" || body.Kind == "guangyapan" || body.Kind == "webdav" {
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
