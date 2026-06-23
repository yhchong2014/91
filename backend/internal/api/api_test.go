package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/video-site/backend/internal/catalog"
	"github.com/video-site/backend/internal/drives"
	"github.com/video-site/backend/internal/mediaasset"
	"github.com/video-site/backend/internal/proxy"
)

func TestVideoSourceUsesDirectStreamForAvi(t *testing.T) {
	v := &catalog.Video{
		ID:      "video-1",
		DriveID: "drive-1",
		FileID:  "file-1",
		Ext:     "avi",
	}

	got := videoSource(v)

	if got != "/p/stream/drive-1/file-1" {
		t.Fatalf("video source = %q, want direct stream route", got)
	}
}

func TestVideoSourceUsesDirectStreamForMkv(t *testing.T) {
	v := &catalog.Video{
		ID:      "video-1",
		DriveID: "drive-1",
		FileID:  "file-1",
		Ext:     "mkv",
	}

	got := videoSource(v)

	if got != "/p/stream/drive-1/file-1" {
		t.Fatalf("video source = %q, want direct stream route", got)
	}
}

func TestVideoSourceKeepsDirectStreamForMp4(t *testing.T) {
	v := &catalog.Video{
		ID:      "video-1",
		DriveID: "drive-1",
		FileID:  "file-1",
		Ext:     "mp4",
	}

	got := videoSource(v)

	if got != "/p/stream/drive-1/file-1" {
		t.Fatalf("video source = %q, want direct stream route", got)
	}
}

func TestVideoURLsEscapePathSegments(t *testing.T) {
	updated := time.UnixMilli(1778863000123)
	v := &catalog.Video{
		ID:        "wopan-drive-fid/with space",
		DriveID:   "drive-1",
		FileID:    "fid/with space",
		Title:     "Video",
		UpdatedAt: updated,
	}

	dto := mapVideo(v)
	if dto.Href != "/video/wopan-drive-fid%2Fwith%20space" {
		t.Fatalf("href = %q, want escaped video id", dto.Href)
	}
	if dto.PreviewSrc != "/p/preview/wopan-drive-fid%2Fwith%20space?v=1778863000123" {
		t.Fatalf("preview = %q, want escaped video id", dto.PreviewSrc)
	}
	if dto.Thumbnail != "/p/thumb/wopan-drive-fid%2Fwith%20space?v=1778863000123" {
		t.Fatalf("thumbnail = %q, want escaped video id", dto.Thumbnail)
	}
	if got := videoSource(v); got != "/p/stream/drive-1/fid%2Fwith%20space" {
		t.Fatalf("video source = %q, want escaped file id", got)
	}
}

func TestThumbnailURLRewritesStoredLocalURLForUnsafeVideoID(t *testing.T) {
	got := thumbnailURL(&catalog.Video{
		ID:           "wopan-drive-fid/with space",
		ThumbnailURL: "/p/thumb/wopan-drive-fid/with space",
		UpdatedAt:    time.UnixMilli(1778863000123),
	})

	if got != "/p/thumb/wopan-drive-fid%2Fwith%20space?v=1778863000123" {
		t.Fatalf("thumbnail URL = %q, want escaped local URL", got)
	}
}

func TestHandleStreamDecodesEscapedWildcardFileID(t *testing.T) {
	local := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(local, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write local video: %v", err)
	}
	drv := &apiStreamFakeDrive{localPath: local}
	reg := proxy.NewRegistry()
	reg.Set("drive-1", drv)
	srv := &Server{Proxy: proxy.New(reg)}

	router := chi.NewRouter()
	router.Get("/p/stream/{driveID}/*", srv.handleStream)
	req := httptest.NewRequest(http.MethodGet, "/p/stream/drive-1/fid%2Fwith%20space", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if drv.fileID != "fid/with space" {
		t.Fatalf("fileID = %q, want decoded original", drv.fileID)
	}
}

func TestVideoSourceUsesLocalUploadRoute(t *testing.T) {
	v := &catalog.Video{
		ID:      "video-1",
		DriveID: localUploadDriveID,
		FileID:  "upload-1.mp4",
		Ext:     "mp4",
	}

	got := videoSource(v)

	if got != "/p/upload/video-1" {
		t.Fatalf("video source = %q, want local upload route", got)
	}
}

func TestPreviewURLIncludesUpdatedAtVersion(t *testing.T) {
	got := previewURL(&catalog.Video{
		ID:        "video-1",
		UpdatedAt: time.UnixMilli(1778863000123),
	})

	if got != "/p/preview/video-1?v=1778863000123" {
		t.Fatalf("preview URL = %q, want versioned URL", got)
	}
}

func TestPreviewURLFallsBackWithoutUpdatedAt(t *testing.T) {
	got := previewURL(&catalog.Video{ID: "video-1"})

	if got != "/p/preview/video-1" {
		t.Fatalf("preview URL = %q, want unversioned URL", got)
	}
}

func TestHandleVideoDetailDecodesEscapedVideoID(t *testing.T) {
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
		ID:          "wopan-drive-fid/with space",
		DriveID:     "drive-1",
		FileID:      "fid/with space",
		Title:       "Video",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	router := chi.NewRouter()
	router.Get("/api/video/{id}", (&Server{Catalog: cat}).handleVideoDetail)
	req := httptest.NewRequest(http.MethodGet, "/api/video/wopan-drive-fid%2Fwith%20space", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got VideoDetailDTO
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "wopan-drive-fid/with space" {
		t.Fatalf("id = %q, want original video id", got.ID)
	}
}

func TestThumbnailURLVersionsLocalGeneratedThumbnails(t *testing.T) {
	got := thumbnailURL(&catalog.Video{
		ID:           "video-1",
		ThumbnailURL: "/p/thumb/video-1",
		UpdatedAt:    time.UnixMilli(1778863000123),
	})
	if got != "/p/thumb/video-1?v=1778863000123" {
		t.Fatalf("thumbnail URL = %q, want versioned local URL", got)
	}

	remote := "https://thumb.example/video-1.jpg"
	got = thumbnailURL(&catalog.Video{
		ID:           "video-1",
		ThumbnailURL: remote,
		UpdatedAt:    time.UnixMilli(1778863000123),
	})
	if got != remote {
		t.Fatalf("remote thumbnail URL = %q, want unchanged %q", got, remote)
	}
}

func TestHandleHomePrioritizesVideosWithReadyThumbnails(t *testing.T) {
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
	for i := 0; i < 20; i++ {
		id := "pending-video-" + strconv.Itoa(i)
		if err := cat.UpsertVideo(ctx, &catalog.Video{
			ID:          id,
			DriveID:     "drive",
			FileID:      id,
			Title:       id,
			PublishedAt: now.Add(time.Duration(i) * time.Minute),
			CreatedAt:   now.Add(time.Duration(i) * time.Minute),
			UpdatedAt:   now.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("seed pending video %s: %v", id, err)
		}
	}
	for i := 0; i < homePageSize+2; i++ {
		id := "ready-video-" + strconv.Itoa(i)
		if err := cat.UpsertVideo(ctx, &catalog.Video{
			ID:           id,
			DriveID:      "drive",
			FileID:       id,
			Title:        id,
			ThumbnailURL: "https://thumb.example/" + id + ".jpg",
			PublishedAt:  now.Add(-time.Duration(i+1) * time.Hour),
			CreatedAt:    now.Add(-time.Duration(i+1) * time.Hour),
			UpdatedAt:    now.Add(-time.Duration(i+1) * time.Hour),
		}); err != nil {
			t.Fatalf("seed ready video %s: %v", id, err)
		}
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/home", nil)
	(&Server{Catalog: cat}).handleHome(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got []VideoDTO
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != homePageSize {
		t.Fatalf("home items = %d, want %d", len(got), homePageSize)
	}
	for _, item := range got {
		if !strings.HasPrefix(item.ID, "ready-video-") {
			t.Fatalf("home returned %q without a ready thumbnail; items=%#v", item.ID, got)
		}
		if !strings.HasPrefix(item.Thumbnail, "https://thumb.example/") {
			t.Fatalf("thumbnail for %q = %q, want ready thumbnail URL", item.ID, item.Thumbnail)
		}
	}
}

func TestHandleHomeExcludesRecentlyShownVideos(t *testing.T) {
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
	for i := 0; i < homePageSize+4; i++ {
		id := "ready-video-" + strconv.Itoa(i)
		if err := cat.UpsertVideo(ctx, &catalog.Video{
			ID:           id,
			DriveID:      "drive",
			FileID:       id,
			Title:        id,
			ThumbnailURL: "https://thumb.example/" + id + ".jpg",
			PublishedAt:  now.Add(time.Duration(i) * time.Minute),
			CreatedAt:    now.Add(time.Duration(i) * time.Minute),
			UpdatedAt:    now.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("seed ready video %s: %v", id, err)
		}
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/home?exclude=ready-video-0&exclude=ready-video-1", nil)
	(&Server{Catalog: cat}).handleHome(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got []VideoDTO
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != homePageSize {
		t.Fatalf("home items = %d, want %d", len(got), homePageSize)
	}
	for _, item := range got {
		if item.ID == "ready-video-0" || item.ID == "ready-video-1" {
			t.Fatalf("home returned excluded video %q; items=%#v", item.ID, got)
		}
		if !strings.HasPrefix(item.ID, "ready-video-") {
			t.Fatalf("home returned %q without a ready thumbnail; items=%#v", item.ID, got)
		}
	}
}

func TestHandleHomeStartsNewRoundWhenRecentExcludesAllVisibleVideos(t *testing.T) {
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
	excludes := make([]string, 0, homePageSize+2)
	for i := 0; i < homePageSize+2; i++ {
		id := "ready-video-" + strconv.Itoa(i)
		excludes = append(excludes, "exclude="+id)
		if err := cat.UpsertVideo(ctx, &catalog.Video{
			ID:           id,
			DriveID:      "drive",
			FileID:       id,
			Title:        id,
			ThumbnailURL: "https://thumb.example/" + id + ".jpg",
			PublishedAt:  now.Add(time.Duration(i) * time.Minute),
			CreatedAt:    now.Add(time.Duration(i) * time.Minute),
			UpdatedAt:    now.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("seed ready video %s: %v", id, err)
		}
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/home?"+strings.Join(excludes, "&"), nil)
	(&Server{Catalog: cat}).handleHome(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got []VideoDTO
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != homePageSize {
		t.Fatalf("home items = %d, want %d; body=%s", len(got), homePageSize, rr.Body.String())
	}
	seen := map[string]bool{}
	for _, item := range got {
		if seen[item.ID] {
			t.Fatalf("home returned duplicate video %q; items=%#v", item.ID, got)
		}
		seen[item.ID] = true
		if !strings.HasPrefix(item.ID, "ready-video-") {
			t.Fatalf("home returned unexpected video %q; items=%#v", item.ID, got)
		}
	}
}

func TestHandleListLatestPrefersReadyThumbnails(t *testing.T) {
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
	for i := 0; i < 20; i++ {
		id := "pending-latest-" + strconv.Itoa(i)
		if err := cat.UpsertVideo(ctx, &catalog.Video{
			ID:          id,
			DriveID:     "drive",
			FileID:      id,
			Title:       id,
			PublishedAt: now.Add(time.Duration(i) * time.Minute),
			CreatedAt:   now.Add(time.Duration(i) * time.Minute),
			UpdatedAt:   now.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("seed pending video %s: %v", id, err)
		}
	}
	for i := 0; i < 12; i++ {
		id := "ready-latest-" + strconv.Itoa(i)
		if err := cat.UpsertVideo(ctx, &catalog.Video{
			ID:           id,
			DriveID:      "drive",
			FileID:       id,
			Title:        id,
			ThumbnailURL: "https://thumb.example/" + id + ".jpg",
			PublishedAt:  now.Add(-time.Duration(i+1) * time.Hour),
			CreatedAt:    now.Add(-time.Duration(i+1) * time.Hour),
			UpdatedAt:    now.Add(-time.Duration(i+1) * time.Hour),
		}); err != nil {
			t.Fatalf("seed ready video %s: %v", id, err)
		}
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/list?page=1&size=12&sort=latest", nil)
	(&Server{Catalog: cat}).handleList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Items []VideoDTO `json:"items"`
		Total int        `json:"total"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Total != 32 {
		t.Fatalf("total = %d, want all matching videos included", got.Total)
	}
	if len(got.Items) != 12 {
		t.Fatalf("items = %d, want 12", len(got.Items))
	}
	for _, item := range got.Items {
		if !strings.HasPrefix(item.ID, "ready-latest-") {
			t.Fatalf("latest list returned %q before ready thumbnails; items=%#v", item.ID, got.Items)
		}
		if !strings.HasPrefix(item.Thumbnail, "https://thumb.example/") {
			t.Fatalf("thumbnail for %q = %q, want ready thumbnail URL", item.ID, item.Thumbnail)
		}
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/list?page=1&size=12&sort=latest&count=false", nil)
	(&Server{Catalog: cat}).handleList(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("count=false status = %d, body = %s", rr.Code, rr.Body.String())
	}
	got = struct {
		Items []VideoDTO `json:"items"`
		Total int        `json:"total"`
	}{}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode count=false response: %v", err)
	}
	if got.Total != 0 {
		t.Fatalf("count=false total = %d, want 0", got.Total)
	}
	if len(got.Items) != 12 {
		t.Fatalf("count=false items = %d, want 12", len(got.Items))
	}
}

func TestHandleListIgnoresCategoryQueryAndDoesNotExposeCategory(t *testing.T) {
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
	for _, v := range []*catalog.Video{
		{
			ID:          "video-a",
			DriveID:     "drive",
			FileID:      "file-a",
			Title:       "A",
			PublishedAt: now,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "video-b",
			DriveID:     "drive",
			FileID:      "file-b",
			Title:       "B",
			PublishedAt: now.Add(-time.Hour),
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	} {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed video %s: %v", v.ID, err)
		}
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/list?page=1&size=24&cat=alpha", nil)
	(&Server{Catalog: cat}).handleList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Total != 2 || len(got.Items) != 2 {
		t.Fatalf("response total/items = %d/%d, want 2/2", got.Total, len(got.Items))
	}
	for _, item := range got.Items {
		if _, ok := item["category"]; ok {
			t.Fatalf("list response exposed category: %#v", item)
		}
	}
}

func TestHandleUploadVideoSavesFileVideoTagsAndQueuesPreview(t *testing.T) {
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

	var queued *catalog.Video
	server := &Server{
		Catalog:  cat,
		LocalDir: t.TempDir(),
		OnVideoUploaded: func(v *catalog.Video) {
			queued = v
		},
	}
	req := multipartUploadRequest(t, map[string]string{
		"title": "用户上传标题",
		"tags":  "奶子,口交,AV,女大",
	}, "clip.mp4", "video-bytes")
	rr := httptest.NewRecorder()

	server.handleUploadVideo(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var dto VideoDTO
	if err := json.NewDecoder(rr.Body).Decode(&dto); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if dto.ID == "" {
		t.Fatal("response video id is empty")
	}
	got, err := cat.GetVideo(ctx, dto.ID)
	if err != nil {
		t.Fatalf("get uploaded video: %v", err)
	}
	if got.DriveID != localUploadDriveID {
		t.Fatalf("drive id = %q, want %q", got.DriveID, localUploadDriveID)
	}
	if got.Title != "用户上传标题" {
		t.Fatalf("title = %q, want submitted title", got.Title)
	}
	if !sameStringSet(got.Tags, []string{"奶子", "口交", "AV", "女大"}) {
		t.Fatalf("tags = %#v, want selected tags", got.Tags)
	}
	if got.PreviewStatus != "pending" {
		t.Fatalf("preview status = %q, want pending", got.PreviewStatus)
	}
	path := filepath.Join(server.localUploadDir(), got.FileID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(data) != "video-bytes" {
		t.Fatalf("uploaded file content = %q, want original bytes", string(data))
	}
	if queued == nil || queued.ID != got.ID {
		t.Fatalf("queued video = %#v, want uploaded video", queued)
	}
}

func TestHandleUploadVideoDefaultsBlankTitleToOriginalFileName(t *testing.T) {
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
	server := &Server{Catalog: cat, LocalDir: t.TempDir()}
	req := multipartUploadRequest(t, map[string]string{"title": "  "}, "holiday.clip.final.mp4", "video-bytes")
	rr := httptest.NewRecorder()

	server.handleUploadVideo(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var dto VideoDTO
	if err := json.NewDecoder(rr.Body).Decode(&dto); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	got, err := cat.GetVideo(ctx, dto.ID)
	if err != nil {
		t.Fatalf("get uploaded video: %v", err)
	}
	if got.Title != "holiday.clip.final" {
		t.Fatalf("title = %q, want original file name without extension", got.Title)
	}
}

func TestHandleUploadVideoRejectsUnsupportedTag(t *testing.T) {
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	server := &Server{Catalog: cat, LocalDir: t.TempDir()}
	req := multipartUploadRequest(t, map[string]string{"tags": "奶子,后入"}, "clip.mp4", "video-bytes")
	rr := httptest.NewRecorder()

	server.handleUploadVideo(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleUploadedVideoServesLocalUploadFile(t *testing.T) {
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
	root := t.TempDir()
	localDir := filepath.Join(root, "previews")
	uploadDir := filepath.Join(root, "uploads")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(uploadDir, "upload-1.mp4"), []byte("video-bytes"), 0o644); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	now := time.Now()
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:          "video-1",
		DriveID:     localUploadDriveID,
		FileID:      "upload-1.mp4",
		Title:       "Uploaded",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	server := &Server{Catalog: cat, LocalDir: localDir}
	req := requestWithRouteParam(http.MethodGet, "/p/upload/video-1", "videoID", "video-1", strings.NewReader(``))
	rr := httptest.NewRecorder()

	server.handleUploadedVideo(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "video-bytes" {
		t.Fatalf("body = %q, want uploaded bytes", rr.Body.String())
	}
}

func TestHandlePreviewIgnoresRemotePreviewFileIDAndServesLocalFile(t *testing.T) {
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
	localDir := t.TempDir()
	localPreview := filepath.Join(localDir, "video-1.mp4")
	if err := os.WriteFile(localPreview, []byte("local teaser"), 0o644); err != nil {
		t.Fatalf("write local preview: %v", err)
	}
	now := time.Now()
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:            "video-1",
		DriveID:       "drive-1",
		FileID:        "file-1",
		Title:         "Video",
		PreviewStatus: "ready",
		PreviewFileID: "remote-preview-file",
		PreviewLocal:  localPreview,
		PublishedAt:   now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	server := &Server{
		Catalog:  cat,
		LocalDir: localDir,
		Proxy:    proxy.New(proxy.NewRegistry()),
	}
	req := requestWithRouteParam(http.MethodGet, "/p/preview/video-1", "videoID", "video-1", strings.NewReader(``))
	rr := httptest.NewRecorder()

	server.handlePreview(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "local teaser" {
		t.Fatalf("body = %q, want local teaser bytes", rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestHandleThumbServesHashedPathForLongVideoID(t *testing.T) {
	localDir := t.TempDir()
	longID := "localstorage-" + strings.Repeat("x", 240)
	thumbPath := mediaasset.ThumbnailPath(localDir, longID)
	if err := os.MkdirAll(filepath.Dir(thumbPath), 0o755); err != nil {
		t.Fatalf("mkdir thumb dir: %v", err)
	}
	if err := os.WriteFile(thumbPath, []byte("thumb-bytes"), 0o644); err != nil {
		t.Fatalf("write thumb: %v", err)
	}

	server := &Server{
		LocalDir: localDir,
		Proxy:    proxy.New(proxy.NewRegistry()),
	}
	req := requestWithRouteParam(http.MethodGet, "/p/thumb/"+longID, "videoID", longID, strings.NewReader(``))
	rr := httptest.NewRecorder()

	server.handleThumb(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "thumb-bytes" {
		t.Fatalf("body = %q, want thumb bytes", rr.Body.String())
	}
}

func TestHandleTagsReturnsUnifiedTagPool(t *testing.T) {
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
		ID:          "video-1",
		DriveID:     "drive",
		FileID:      "file-1",
		Title:       "清纯女大后入",
		Tags:        []string{"后入", "女大"},
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := cat.CreateTagAndClassify(ctx, "清纯", nil, "user"); err != nil {
		t.Fatalf("create tag: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tags", nil)
	rr := httptest.NewRecorder()
	(&Server{Catalog: cat}).handleTags(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Count int    `json:"count"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	labels := make([]string, 0, len(got))
	for _, tag := range got {
		labels = append(labels, tag.Label)
	}
	if !containsString(labels, "清纯") {
		t.Fatalf("labels = %#v, want user tag 清纯", labels)
	}
	if !containsString(labels, "后入") {
		t.Fatalf("labels = %#v, want system tag 后入", labels)
	}
	var qingchunCount int
	for _, tag := range got {
		if tag.Label == "清纯" {
			qingchunCount = tag.Count
		}
	}
	if qingchunCount != 1 {
		t.Fatalf("清纯 count = %d, want 1; tags = %#v", qingchunCount, got)
	}
}

func TestHandleShortsNextReturnsRandomBatchExcludingSeen(t *testing.T) {
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
	for _, v := range []*catalog.Video{
		{ID: "current", DriveID: "drive", FileID: "f-current", Title: "current", Tags: []string{"common", "rare"}, PublishedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "common-1", DriveID: "drive", FileID: "f-common-1", Title: "common 1", Tags: []string{"common"}, PublishedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "common-2", DriveID: "drive", FileID: "f-common-2", Title: "common 2", Tags: []string{"common"}, PublishedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "rare-1", DriveID: "drive", FileID: "f-rare-1", Title: "rare 1", Tags: []string{"rare"}, PublishedAt: now, CreatedAt: now, UpdatedAt: now},
	} {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed %s: %v", v.ID, err)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/shorts/next", strings.NewReader(`{"seenIds":["current"],"count":3}`))
	rr := httptest.NewRecorder()
	(&Server{Catalog: cat}).handleShortsNext(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Items         []ShortsItemDTO `json:"items"`
		Total         int             `json:"total"`
		RoundComplete bool            `json:"roundComplete"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ids := make([]string, 0, len(got.Items))
	for _, item := range got.Items {
		ids = append(ids, item.ID)
	}
	if got.Total != 4 {
		t.Fatalf("total = %d, want 4", got.Total)
	}
	if got.RoundComplete {
		t.Fatalf("roundComplete = true, want false with a full remaining batch")
	}
	if containsString(ids, "current") {
		t.Fatalf("ids = %#v, should exclude current", ids)
	}
	if len(ids) != 3 {
		t.Fatalf("ids = %#v, want 3 items", ids)
	}
	for _, want := range []string{"common-1", "common-2", "rare-1"} {
		if !containsString(ids, want) {
			t.Fatalf("ids = %#v, want remaining id %s", ids, want)
		}
	}
}

func TestHandleShortsNextDoesNotResetForStaleSeenIDs(t *testing.T) {
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
	for _, v := range []*catalog.Video{
		{ID: "seen-1", DriveID: "drive", FileID: "f-seen-1", Title: "seen 1", PublishedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "fresh-1", DriveID: "drive", FileID: "f-fresh-1", Title: "fresh 1", PublishedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "fresh-2", DriveID: "drive", FileID: "f-fresh-2", Title: "fresh 2", PublishedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "hidden-1", DriveID: "drive", FileID: "f-hidden-1", Title: "hidden 1", PublishedAt: now, CreatedAt: now, UpdatedAt: now},
	} {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed %s: %v", v.ID, err)
		}
	}
	if err := cat.HideVideo(ctx, "hidden-1"); err != nil {
		t.Fatalf("hide hidden-1: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/shorts/next", strings.NewReader(`{"seenIds":["seen-1","hidden-1","deleted-stale"],"count":3}`))
	rr := httptest.NewRecorder()
	(&Server{Catalog: cat}).handleShortsNext(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Items         []ShortsItemDTO `json:"items"`
		Total         int             `json:"total"`
		RoundComplete bool            `json:"roundComplete"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ids := make([]string, 0, len(got.Items))
	for _, item := range got.Items {
		ids = append(ids, item.ID)
	}
	if got.Total != 3 {
		t.Fatalf("total = %d, want 3", got.Total)
	}
	if !got.RoundComplete {
		t.Fatalf("roundComplete = false, want true after returning all unviewed visible videos")
	}
	if containsString(ids, "seen-1") || containsString(ids, "hidden-1") {
		t.Fatalf("ids = %#v, should not reset and return seen or hidden videos", ids)
	}
	for _, want := range []string{"fresh-1", "fresh-2"} {
		if !containsString(ids, want) {
			t.Fatalf("ids = %#v, want %s", ids, want)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %#v, want exactly the two unviewed visible videos", ids)
	}
}

func TestHandleUpdateVideoTagsRejectsUnknownTags(t *testing.T) {
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
		ID:          "video-1",
		DriveID:     "drive",
		FileID:      "file-1",
		Title:       "普通标题",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	req := requestWithVideoID(http.MethodPut, "/api/video/video-1/tags", "video-1", strings.NewReader(`{"tags":["不存在"]}`))
	rr := httptest.NewRecorder()
	(&Server{Catalog: cat}).handleUpdateVideoTags(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleUpdateVideoTagsSavesExistingTags(t *testing.T) {
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
		ID:          "video-1",
		DriveID:     "drive",
		FileID:      "file-1",
		Title:       "清纯标题",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := cat.CreateTagAndClassify(ctx, "清纯", nil, "user"); err != nil {
		t.Fatalf("create tag: %v", err)
	}

	req := requestWithVideoID(http.MethodPut, "/api/video/video-1/tags", "video-1", strings.NewReader(`{"tags":["清纯"]}`))
	rr := httptest.NewRecorder()
	(&Server{Catalog: cat}).handleUpdateVideoTags(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	got, err := cat.GetVideo(ctx, "video-1")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if !sameStrings(got.Tags, []string{"清纯"}) {
		t.Fatalf("tags = %#v, want 清纯", got.Tags)
	}
}

func TestHandleVideoDetailIncludesDriveKindLabel(t *testing.T) {
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
	if err := cat.UpsertDrive(ctx, &catalog.Drive{
		ID:        "drive-onedrive",
		Kind:      "onedrive",
		Name:      "Personal Drive",
		RootID:    "root",
		Status:    "ok",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed drive: %v", err)
	}
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:          "video-1",
		DriveID:     "drive-onedrive",
		FileID:      "file-1",
		Title:       "Video",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	req := requestWithVideoID(http.MethodGet, "/api/video/video-1", "video-1", strings.NewReader(``))
	rr := httptest.NewRecorder()
	(&Server{Catalog: cat}).handleVideoDetail(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got VideoDetailDTO
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SourceLabel != "OneDrive" {
		t.Fatalf("sourceLabel = %q, want OneDrive", got.SourceLabel)
	}
}

func TestHandleVideoDetailRecommendationsPreferReadyThumbnails(t *testing.T) {
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
		ID:           "current-video",
		DriveID:      "drive",
		FileID:       "current-video",
		Title:        "Current",
		Tags:         []string{"same-tag"},
		ThumbnailURL: "https://thumb.example/current-video.jpg",
		PublishedAt:  now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("seed current video: %v", err)
	}
	for i := 0; i < 20; i++ {
		id := "pending-related-" + strconv.Itoa(i)
		if err := cat.UpsertVideo(ctx, &catalog.Video{
			ID:          id,
			DriveID:     "drive",
			FileID:      id,
			Title:       id,
			Tags:        []string{"same-tag"},
			PublishedAt: now.Add(time.Duration(i+1) * time.Minute),
			CreatedAt:   now.Add(time.Duration(i+1) * time.Minute),
			UpdatedAt:   now.Add(time.Duration(i+1) * time.Minute),
		}); err != nil {
			t.Fatalf("seed pending related video %s: %v", id, err)
		}
	}
	for i := 0; i < 8; i++ {
		id := "ready-related-" + strconv.Itoa(i)
		if err := cat.UpsertVideo(ctx, &catalog.Video{
			ID:           id,
			DriveID:      "drive",
			FileID:       id,
			Title:        id,
			Tags:         []string{"same-tag"},
			ThumbnailURL: "https://thumb.example/" + id + ".jpg",
			PublishedAt:  now.Add(-time.Duration(i+1) * time.Hour),
			CreatedAt:    now.Add(-time.Duration(i+1) * time.Hour),
			UpdatedAt:    now.Add(-time.Duration(i+1) * time.Hour),
		}); err != nil {
			t.Fatalf("seed ready related video %s: %v", id, err)
		}
	}

	req := requestWithVideoID(http.MethodGet, "/api/video/current-video", "current-video", strings.NewReader(``))
	rr := httptest.NewRecorder()
	(&Server{Catalog: cat}).handleVideoDetail(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got VideoDetailDTO
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.RelatedVideos) != 6 {
		t.Fatalf("related videos = %d, want 6; items=%#v", len(got.RelatedVideos), got.RelatedVideos)
	}
	for _, item := range got.RelatedVideos {
		if !strings.HasPrefix(item.ID, "ready-related-") {
			t.Fatalf("related returned %q before ready thumbnails; items=%#v", item.ID, got.RelatedVideos)
		}
		if !strings.HasPrefix(item.Thumbnail, "https://thumb.example/") {
			t.Fatalf("thumbnail for %q = %q, want ready thumbnail URL", item.ID, item.Thumbnail)
		}
	}
}

func TestHandleHideVideoRemovesVideoFromPublicListAndDetail(t *testing.T) {
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
	for _, v := range []*catalog.Video{
		{
			ID:          "video-hidden",
			DriveID:     "drive",
			FileID:      "file-hidden",
			Title:       "Hide me",
			PublishedAt: now,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "video-visible",
			DriveID:     "drive",
			FileID:      "file-visible",
			Title:       "Keep me",
			PublishedAt: now.Add(-time.Minute),
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	} {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed video %s: %v", v.ID, err)
		}
	}

	server := &Server{Catalog: cat}
	hideReq := requestWithVideoID(http.MethodPost, "/api/video/video-hidden/hide", "video-hidden", strings.NewReader(``))
	hideRR := httptest.NewRecorder()
	server.handleHideVideo(hideRR, hideReq)

	if hideRR.Code != http.StatusOK {
		t.Fatalf("hide status = %d, body = %s", hideRR.Code, hideRR.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/list?page=1&size=24", nil)
	listRR := httptest.NewRecorder()
	server.handleList(listRR, listReq)

	if listRR.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRR.Code, listRR.Body.String())
	}
	var listed struct {
		Items []VideoDTO `json:"items"`
		Total int        `json:"total"`
	}
	if err := json.NewDecoder(listRR.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listed.Total != 1 || len(listed.Items) != 1 || listed.Items[0].ID != "video-visible" {
		t.Fatalf("listed = total:%d items:%#v, want only video-visible", listed.Total, listed.Items)
	}

	detailReq := requestWithVideoID(http.MethodGet, "/api/video/video-hidden", "video-hidden", strings.NewReader(``))
	detailRR := httptest.NewRecorder()
	server.handleVideoDetail(detailRR, detailReq)

	if detailRR.Code != http.StatusNotFound {
		t.Fatalf("detail status = %d, want 404; body = %s", detailRR.Code, detailRR.Body.String())
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, value := range a {
		seen[value]++
	}
	for _, value := range b {
		if seen[value] == 0 {
			return false
		}
		seen[value]--
	}
	return true
}

type apiStreamFakeDrive struct {
	localPath string
	fileID    string
}

func (d *apiStreamFakeDrive) Kind() string { return "fake" }
func (d *apiStreamFakeDrive) ID() string   { return "drive-1" }
func (d *apiStreamFakeDrive) Init(context.Context) error {
	return nil
}
func (d *apiStreamFakeDrive) List(context.Context, string) ([]drives.Entry, error) {
	return nil, drives.ErrNotSupported
}
func (d *apiStreamFakeDrive) Stat(context.Context, string) (*drives.Entry, error) {
	return nil, drives.ErrNotSupported
}
func (d *apiStreamFakeDrive) StreamURL(_ context.Context, fileID string) (*drives.StreamLink, error) {
	d.fileID = fileID
	return &drives.StreamLink{
		URL:     d.localPath,
		Expires: time.Now().Add(time.Minute),
	}, nil
}
func (d *apiStreamFakeDrive) Upload(context.Context, string, string, io.Reader, int64) (string, error) {
	return "", drives.ErrNotSupported
}
func (d *apiStreamFakeDrive) EnsureDir(context.Context, string) (string, error) {
	return "", drives.ErrNotSupported
}
func (d *apiStreamFakeDrive) RootID() string { return "root" }

func requestWithVideoID(method, target, videoID string, body *strings.Reader) *http.Request {
	return requestWithRouteParam(method, target, "id", videoID, body)
}

func requestWithRouteParam(method, target, key, value string, body *strings.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req
}

func multipartUploadRequest(t *testing.T, fields map[string]string, fileName, fileContent string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := part.Write([]byte(fileContent)); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}
