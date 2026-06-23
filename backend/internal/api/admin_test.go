package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/video-site/backend/internal/auth"
	"github.com/video-site/backend/internal/catalog"
	"github.com/video-site/backend/internal/drives/scriptcrawler"
)

func TestHandleLoginReturnsForbiddenForBannedIP(t *testing.T) {
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
	if err := cat.BanLoginIP(ctx, "203.0.113.20", "test"); err != nil {
		t.Fatalf("ban ip: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/login", strings.NewReader(`{"username":"admin","password":"secret"}`))
	req.RemoteAddr = "203.0.113.20:12345"
	rr := httptest.NewRecorder()

	(&AdminServer{
		Catalog: cat,
		Auth:    &auth.Authenticator{Username: "admin", Password: "secret", Catalog: cat},
	}).handleLogin(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleLoginRequiresSetupBeforeDefaultLogin(t *testing.T) {
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/admin/api/login", strings.NewReader(`{"username":"admin","password":"admin123"}`))
	rr := httptest.NewRecorder()
	(&AdminServer{
		Catalog:       cat,
		Auth:          &auth.Authenticator{Username: "admin", Password: "admin123", Catalog: cat},
		SetupRequired: func() bool { return true },
	}).handleLogin(rr, req)

	if rr.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want 428; body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleSetupStoresCredentialsAndCreatesSession(t *testing.T) {
	cat, err := catalog.Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	authr := &auth.Authenticator{Username: "admin", Password: "admin123", Catalog: cat}
	setupRequired := true
	var savedUser, savedPass string
	req := httptest.NewRequest(http.MethodPost, "/admin/api/setup", strings.NewReader(`{"username":"owner","password":"secret123"}`))
	rr := httptest.NewRecorder()

	(&AdminServer{
		Catalog:       cat,
		Auth:          authr,
		SetupRequired: func() bool { return setupRequired },
		OnSetup: func(username, password string) error {
			savedUser, savedPass = username, password
			authr.SetCredentials(username, password)
			setupRequired = false
			return nil
		},
	}).handleSetup(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
	if savedUser != "owner" || savedPass != "secret123" {
		t.Fatalf("saved credentials = %q/%q, want owner/secret123", savedUser, savedPass)
	}
	cookies := rr.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("setup did not set a session cookie")
	}
	ok, err := cat.ValidateSession(context.Background(), cookies[0].Value)
	if err != nil || !ok {
		t.Fatalf("setup session valid=%v err=%v", ok, err)
	}
}

func TestHandleDeleteVideoDefaultsDeleteSourceFalse(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/videos/video-1", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "video-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	called := false
	(&AdminServer{
		OnDeleteVideo: func(ctx context.Context, videoID string, deleteSource bool) (DeleteVideoResult, error) {
			called = true
			if videoID != "video-1" {
				t.Fatalf("videoID = %q, want video-1", videoID)
			}
			if deleteSource {
				t.Fatal("deleteSource defaulted to true")
			}
			return DeleteVideoResult{OK: true}, nil
		},
	}).handleDeleteVideo(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Fatal("OnDeleteVideo was not called")
	}
}

func TestHandleDeleteVideoPassesDeleteSourceOption(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/videos/video-1", strings.NewReader(`{"deleteSource":true}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "video-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	(&AdminServer{
		OnDeleteVideo: func(ctx context.Context, videoID string, deleteSource bool) (DeleteVideoResult, error) {
			if !deleteSource {
				t.Fatal("deleteSource = false, want true")
			}
			return DeleteVideoResult{OK: true, DeletedSource: true}, nil
		},
	}).handleDeleteVideo(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
	var got DeleteVideoResult
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.DeletedSource {
		t.Fatalf("DeletedSource = false, want true; response = %s", rr.Body.String())
	}
}

func TestHandleCheckUpdateReportsNewRelease(t *testing.T) {
	dir := t.TempDir()
	versionFile := filepath.Join(dir, ".version")
	if err := os.WriteFile(versionFile, []byte("v0.1.0\n2026-05-29 12:00:00\n"), 0o644); err != nil {
		t.Fatalf("write version file: %v", err)
	}
	releaseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "missing user agent", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tag_name": "v0.2.0",
			"html_url": "https://github.com/nianzhibai/91/releases/tag/v0.2.0",
		})
	}))
	t.Cleanup(releaseServer.Close)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/update/check", nil)
	rr := httptest.NewRecorder()
	(&AdminServer{
		VersionFilePath: versionFile,
		ReleaseAPIURL:   releaseServer.URL,
	}).handleCheckUpdate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got updateCheckDTO
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.CurrentVersion != "v0.1.0" {
		t.Fatalf("currentVersion = %q, want v0.1.0", got.CurrentVersion)
	}
	if got.LatestVersion != "v0.2.0" {
		t.Fatalf("latestVersion = %q, want v0.2.0", got.LatestVersion)
	}
	if !got.HasUpdate {
		t.Fatalf("hasUpdate = false, want true")
	}
	if got.ReleaseURL == "" {
		t.Fatalf("releaseUrl is empty")
	}
}

func TestHandleCheckUpdateReportsUpToDate(t *testing.T) {
	dir := t.TempDir()
	versionFile := filepath.Join(dir, ".version")
	if err := os.WriteFile(versionFile, []byte("v0.2.0\n"), 0o644); err != nil {
		t.Fatalf("write version file: %v", err)
	}
	releaseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"tag_name": "v0.2.0",
			"html_url": "https://github.com/nianzhibai/91/releases/tag/v0.2.0",
		})
	}))
	t.Cleanup(releaseServer.Close)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/update/check", nil)
	rr := httptest.NewRecorder()
	(&AdminServer{
		VersionFilePath: versionFile,
		ReleaseAPIURL:   releaseServer.URL,
	}).handleCheckUpdate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got updateCheckDTO
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.HasUpdate {
		t.Fatalf("hasUpdate = true, want false")
	}
}

func TestHandleCheckUpdateUsesDockerImageVersion(t *testing.T) {
	releaseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"tag_name": "v0.2.0",
			"html_url": "https://github.com/nianzhibai/91/releases/tag/v0.2.0",
		})
	}))
	t.Cleanup(releaseServer.Close)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/update/check", nil)
	rr := httptest.NewRecorder()
	(&AdminServer{
		ImageVersion:  "v0.1.0",
		ReleaseAPIURL: releaseServer.URL,
	}).handleCheckUpdate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got updateCheckDTO
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.CurrentVersion != "v0.1.0" {
		t.Fatalf("currentVersion = %q, want v0.1.0", got.CurrentVersion)
	}
	if !got.HasUpdate {
		t.Fatalf("hasUpdate = false, want true")
	}
}

func TestInstalledVersionPrefersDockerImageVersionOverVersionFile(t *testing.T) {
	dir := t.TempDir()
	versionFile := filepath.Join(dir, ".version")
	if err := os.WriteFile(versionFile, []byte("v0.1.0\n"), 0o644); err != nil {
		t.Fatalf("write version file: %v", err)
	}

	got := (&AdminServer{
		VersionFilePath: versionFile,
		ImageVersion:    "v0.2.0",
	}).installedVersion()

	if got != "v0.2.0" {
		t.Fatalf("installedVersion = %q, want v0.2.0", got)
	}
}

func TestHandleRunNightlyJobReturnsAcceptedStatus(t *testing.T) {
	called := false
	req := httptest.NewRequest(http.MethodPost, "/admin/api/jobs/nightly/run", nil)
	rr := httptest.NewRecorder()

	(&AdminServer{
		OnRunNightlyJob: func() bool {
			called = true
			return true
		},
		GetNightlyJobStatus: func() NightlyJobStatus {
			return NightlyJobStatus{State: "queued", Queued: true}
		},
	}).handleRunNightlyJob(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Fatal("OnRunNightlyJob was not called")
	}
	var got struct {
		OK       bool             `json:"ok"`
		Accepted bool             `json:"accepted"`
		Status   NightlyJobStatus `json:"status"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.OK || !got.Accepted || got.Status.State != "queued" || !got.Status.Queued {
		t.Fatalf("response = %#v, want accepted queued status", got)
	}
}

func TestHandleRunNightlyJobReturnsBusyMessageWhenRejected(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/api/jobs/nightly/run", nil)
	rr := httptest.NewRecorder()

	(&AdminServer{
		OnRunNightlyJob: func() bool {
			return false
		},
		GetNightlyJobStatus: func() NightlyJobStatus {
			return NightlyJobStatus{State: "running", Running: true}
		},
	}).handleRunNightlyJob(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		OK       bool             `json:"ok"`
		Accepted bool             `json:"accepted"`
		Message  string           `json:"message"`
		Status   NightlyJobStatus `json:"status"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.OK || got.Accepted || got.Message != fullScanBusyMessage || !got.Status.Running {
		t.Fatalf("response = %#v, want rejected busy message", got)
	}
}

func TestHandleRescanRejectsWhenNightlyBusy(t *testing.T) {
	called := false
	req := httptest.NewRequest(http.MethodPost, "/admin/api/drives/PikPak/rescan", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "PikPak")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	(&AdminServer{
		OnScanRequested: func(driveID string) bool {
			called = true
			return true
		},
		GetNightlyJobStatus: func() NightlyJobStatus {
			return NightlyJobStatus{State: "running", Running: true}
		},
	}).handleRescan(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rr.Code, rr.Body.String())
	}
	if called {
		t.Fatal("OnScanRequested was called while nightly job was busy")
	}
	var got struct {
		OK       bool             `json:"ok"`
		Accepted bool             `json:"accepted"`
		Message  string           `json:"message"`
		Status   NightlyJobStatus `json:"status"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.OK || got.Accepted || got.Message != fullScanBusyMessage || !got.Status.Running {
		t.Fatalf("response = %#v, want rejected full scan busy message", got)
	}
}

func TestHandleRescanReturnsAcceptedFlagAndBusyMessage(t *testing.T) {
	calledWith := ""
	req := httptest.NewRequest(http.MethodPost, "/admin/api/drives/PikPak/rescan", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "PikPak")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	(&AdminServer{
		OnScanRequested: func(driveID string) bool {
			calledWith = driveID
			return false
		},
	}).handleRescan(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		OK       bool   `json:"ok"`
		Accepted bool   `json:"accepted"`
		Message  string `json:"message"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if calledWith != "PikPak" {
		t.Fatalf("hook called with %q, want PikPak", calledWith)
	}
	if !got.OK || got.Accepted || got.Message != driveTaskBusyMessage {
		t.Fatalf("response = %#v, want rejected busy message", got)
	}
}

func TestHandleNightlyJobStatusDefaultsToIdle(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/api/jobs/nightly/status", nil)
	rr := httptest.NewRecorder()

	(&AdminServer{}).handleNightlyJobStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
	var got NightlyJobStatus
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.State != "idle" || got.Running || got.Queued {
		t.Fatalf("status = %#v, want idle", got)
	}
}

func TestHandleStopDriveTasksInvokesHookWithDriveID(t *testing.T) {
	calledWith := ""
	server := &AdminServer{
		OnStopDriveTasks: func(driveID string) bool {
			calledWith = driveID
			return true
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/drives/PikPak/tasks/stop", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "PikPak")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	server.handleStopDriveTasks(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if calledWith != "PikPak" {
		t.Fatalf("hook called with %q, want PikPak", calledWith)
	}
	var got struct {
		OK      bool `json:"ok"`
		Stopped bool `json:"stopped"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.OK || !got.Stopped {
		t.Fatalf("response = %#v, want stopped", got)
	}
}

func TestHandleStopAllTasksInvokesHookAndReturnsStatus(t *testing.T) {
	called := false
	server := &AdminServer{
		OnStopAllTasks: func() int {
			called = true
			return 2
		},
		GetNightlyJobStatus: func() NightlyJobStatus {
			return NightlyJobStatus{State: "running", Running: true}
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/tasks/stop", nil)
	rr := httptest.NewRecorder()
	server.handleStopAllTasks(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Fatal("OnStopAllTasks was not called")
	}
	var got struct {
		OK            bool             `json:"ok"`
		StoppedDrives int              `json:"stoppedDrives"`
		Status        NightlyJobStatus `json:"status"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.OK || got.StoppedDrives != 2 || got.Status.State != "running" || !got.Status.Running {
		t.Fatalf("response = %#v, want stopped drives and status", got)
	}
}

func TestHandleUpsertDrivePreservesExistingCredentialsWhenRequestCredentialsEmpty(t *testing.T) {
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
		ID:         "quark-main",
		Kind:       "quark",
		Name:       "Old name",
		RootID:     "0",
		ScanRootID: "0",
		Credentials: map[string]string{
			"cookie": "existing-cookie",
		},
		Status: "ok",
	}); err != nil {
		t.Fatalf("seed drive: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/drives", strings.NewReader(`{
		"id": "quark-main",
		"kind": "quark",
		"name": "New name",
		"rootId": "0",
		"scanRootId": "scan-root",
		"credentials": {}
	}`))
	rr := httptest.NewRecorder()

	(&AdminServer{Catalog: cat}).handleUpsertDrive(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	got, err := cat.GetDrive(ctx, "quark-main")
	if err != nil {
		t.Fatalf("get drive: %v", err)
	}
	if got.Name != "New name" {
		t.Fatalf("name = %q, want New name", got.Name)
	}
	if got.ScanRootID != "0" {
		t.Fatalf("scanRootId = %q, want rootId 0", got.ScanRootID)
	}
	if got.Credentials["cookie"] != "existing-cookie" {
		t.Fatalf("cookie credential = %q, want existing-cookie", got.Credentials["cookie"])
	}
}

func TestHandleUpsertDriveDefaultsEmptyRootID(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodPost, "/admin/api/drives", strings.NewReader(`{
		"id": "onedrive-main",
		"kind": "onedrive",
		"name": "OneDrive",
		"rootId": "",
		"credentials": {"refresh_token": "token"}
	}`))
	rr := httptest.NewRecorder()

	(&AdminServer{Catalog: cat}).handleUpsertDrive(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	got, err := cat.GetDrive(ctx, "onedrive-main")
	if err != nil {
		t.Fatalf("get drive: %v", err)
	}
	if got.RootID != "root" {
		t.Fatalf("rootId = %q, want root", got.RootID)
	}
	if got.ScanRootID != got.RootID {
		t.Fatalf("scanRootId = %q, want rootId %q", got.ScanRootID, got.RootID)
	}
}

func TestHandleUpsertDriveReplacesExistingCredentialsWhenProvided(t *testing.T) {
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
		ID:         "quark-main",
		Kind:       "quark",
		Name:       "Old name",
		RootID:     "0",
		ScanRootID: "0",
		Credentials: map[string]string{
			"cookie": "existing-cookie",
		},
		Status: "ok",
	}); err != nil {
		t.Fatalf("seed drive: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/drives", bytes.NewBufferString(`{
		"id": "quark-main",
		"kind": "quark",
		"name": "New name",
		"rootId": "0",
		"scanRootId": "0",
		"credentials": {"cookie": "new-cookie"}
	}`))
	rr := httptest.NewRecorder()

	(&AdminServer{Catalog: cat}).handleUpsertDrive(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	got, err := cat.GetDrive(ctx, "quark-main")
	if err != nil {
		t.Fatalf("get drive: %v", err)
	}
	if got.Credentials["cookie"] != "new-cookie" {
		t.Fatalf("cookie credential = %q, want new-cookie", got.Credentials["cookie"])
	}
}

func TestHandleUpsertGoogleDriveMergesOAuthCredentials(t *testing.T) {
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
		ID:     "google-main",
		Kind:   "googledrive",
		Name:   "Google Drive",
		RootID: "root",
		Credentials: map[string]string{
			"refresh_token":   "existing-refresh",
			"access_token":    "existing-access",
			"use_online_api":  "true",
			"api_url_address": "https://api.oplist.org/googleui/renewapi",
		},
		Status: "ok",
	}); err != nil {
		t.Fatalf("seed drive: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/drives", bytes.NewBufferString(`{
		"id": "google-main",
		"kind": "googledrive",
		"name": "Google Drive",
		"rootId": "root",
		"credentials": {
			"use_online_api": "false",
			"client_id": "google-client-id",
			"client_secret": "google-client-secret"
		}
	}`))
	rr := httptest.NewRecorder()

	(&AdminServer{Catalog: cat}).handleUpsertDrive(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	got, err := cat.GetDrive(ctx, "google-main")
	if err != nil {
		t.Fatalf("get drive: %v", err)
	}
	if got.Credentials["refresh_token"] != "existing-refresh" || got.Credentials["access_token"] != "existing-access" {
		t.Fatalf("tokens were not preserved: %#v", got.Credentials)
	}
	if got.Credentials["use_online_api"] != "false" {
		t.Fatalf("use_online_api = %q, want false", got.Credentials["use_online_api"])
	}
	if got.Credentials["client_id"] != "google-client-id" || got.Credentials["client_secret"] != "google-client-secret" {
		t.Fatalf("oauth client credentials = %#v, want saved", got.Credentials)
	}
	if got.Credentials["api_url_address"] != "https://api.oplist.org/googleui/renewapi" {
		t.Fatalf("api_url_address = %q, want preserved", got.Credentials["api_url_address"])
	}

	clearReq := httptest.NewRequest(http.MethodPost, "/admin/api/drives", bytes.NewBufferString(`{
		"id": "google-main",
		"kind": "googledrive",
		"name": "Google Drive",
		"rootId": "root",
		"credentials": {
			"api_url_address": ""
		}
	}`))
	clearRR := httptest.NewRecorder()
	(&AdminServer{Catalog: cat}).handleUpsertDrive(clearRR, clearReq)
	if clearRR.Code != http.StatusOK {
		t.Fatalf("clear status = %d, body = %s", clearRR.Code, clearRR.Body.String())
	}
	cleared, err := cat.GetDrive(ctx, "google-main")
	if err != nil {
		t.Fatalf("get cleared drive: %v", err)
	}
	if _, ok := cleared.Credentials["api_url_address"]; ok {
		t.Fatalf("api_url_address was not cleared: %#v", cleared.Credentials)
	}
}

func TestHandleUpsertUnknownDriveKindIsRejected(t *testing.T) {
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
		ID:     "unknown-main",
		Kind:   "unknown",
		Name:   "Unknown",
		RootID: "/",
		Credentials: map[string]string{
			"token": "old-token",
		},
		Status: "ok",
	}); err != nil {
		t.Fatalf("seed drive: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/drives", strings.NewReader(`{
		"id": "unknown-main",
		"kind": "unknown",
		"name": "Unknown",
		"rootId": "/",
		"credentials": {"token": "new-token"}
	}`))
	rr := httptest.NewRecorder()
	(&AdminServer{Catalog: cat}).handleUpsertDrive(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "unsupported drive kind\n" {
		t.Fatalf("body = %q, want unsupported kind", rr.Body.String())
	}

	got, err := cat.GetDrive(ctx, "unknown-main")
	if err != nil {
		t.Fatalf("get drive: %v", err)
	}
	if got.Credentials["token"] != "old-token" {
		t.Fatalf("token = %q, want unchanged old token", got.Credentials["token"])
	}
}

func TestHandleDeleteDriveRunsRequestedCleanupBeforeDeletingDrive(t *testing.T) {
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
		ID:            "drive-one",
		Kind:          "pikpak",
		Name:          "Drive One",
		RootID:        "root",
		TeaserEnabled: true,
	}); err != nil {
		t.Fatalf("seed drive: %v", err)
	}

	cleanupCalled := ""
	removedCalled := ""
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/drives/drive-one", strings.NewReader(`{"deleteVideos":true}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "drive-one")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	(&AdminServer{
		Catalog: cat,
		OnDriveDeleteCleanup: func(cleanupCtx context.Context, driveID string) (int, error) {
			cleanupCalled = driveID
			if _, err := cat.GetDrive(cleanupCtx, driveID); err != nil {
				t.Fatalf("drive should still exist during cleanup: %v", err)
			}
			return 3, nil
		},
		OnDriveRemoved: func(driveID string) {
			removedCalled = driveID
		},
	}).handleDeleteDrive(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if cleanupCalled != "drive-one" {
		t.Fatalf("cleanup called with %q, want drive-one", cleanupCalled)
	}
	if removedCalled != "drive-one" {
		t.Fatalf("removed hook called with %q, want drive-one", removedCalled)
	}
	if _, err := cat.GetDrive(ctx, "drive-one"); err != sql.ErrNoRows {
		t.Fatalf("drive lookup error = %v, want sql.ErrNoRows", err)
	}
	var got struct {
		OK            bool `json:"ok"`
		DeletedVideos int  `json:"deletedVideos"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.OK || got.DeletedVideos != 3 {
		t.Fatalf("response = %#v, want ok with deletedVideos=3", got)
	}
}

func TestHandleDeleteDriveRequiresCleanupConfirmation(t *testing.T) {
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
		ID:            "drive-one",
		Kind:          "pikpak",
		Name:          "Drive One",
		RootID:        "root",
		TeaserEnabled: true,
	}); err != nil {
		t.Fatalf("seed drive: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/admin/api/drives/drive-one", strings.NewReader(`{"deleteVideos":false}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "drive-one")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	(&AdminServer{
		Catalog: cat,
		OnDriveDeleteCleanup: func(context.Context, string) (int, error) {
			t.Fatal("cleanup hook should not be called without confirmation")
			return 0, nil
		},
	}).handleDeleteDrive(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
	if _, err := cat.GetDrive(ctx, "drive-one"); err != nil {
		t.Fatalf("drive should remain after rejected delete: %v", err)
	}
}

func TestHandleListCrawlersOnlyIncludesCrawlerPageScripts(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cat, err := catalog.Open(filepath.Join(tmp, "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	scriptPath := filepath.Join(tmp, "demo_crawler.py")
	if err := os.WriteFile(scriptPath, []byte("CRAWLER_NAME = \"Demo Crawler\"\n"), 0o644); err != nil {
		t.Fatalf("write crawler script: %v", err)
	}

	for _, d := range []*catalog.Drive{
		{
			ID:     "crawler-main",
			Kind:   "scriptcrawler",
			Name:   "Crawler",
			RootID: "/",
			Credentials: map[string]string{
				"last_crawl_at":   "1800000000",
				"proxy":           " http://127.0.0.1:7890 ",
				"script_path":     scriptPath,
				"upload_drive_id": "p115-target",
			},
			Status:        "ok",
			TeaserEnabled: false,
		},
		{
			ID:          "p115-target",
			Kind:        "p115",
			Name:        "115",
			RootID:      "0",
			Credentials: map[string]string{"cookie": "x"},
			Status:      "ok",
		},
		{
			ID:     "onedrive-main",
			Kind:   "onedrive",
			Name:   "OneDrive",
			RootID: "root",
			Credentials: map[string]string{
				"proxy": "http://should-not-leak.local:7890",
			},
			Status: "ok",
		},
		{
			ID:          "crawler-script-deleted",
			Kind:        "scriptcrawler",
			Name:        "Deleted Script",
			RootID:      "/",
			Credentials: map[string]string{},
			Status:      "disconnected",
		},
	} {
		if err := cat.UpsertDrive(ctx, d); err != nil {
			t.Fatalf("seed drive %s: %v", d.ID, err)
		}
	}
	for _, v := range []*catalog.Video{
		{
			ID:              "scriptcrawler-crawler-main-local",
			DriveID:         "crawler-main",
			FileID:          "local.mp4",
			FileName:        "local.mp4",
			Title:           "Local",
			Size:            123,
			Ext:             "mp4",
			ThumbnailURL:    "/p/thumb/scriptcrawler-crawler-main-local",
			PreviewStatus:   "ready",
			DurationSeconds: 12,
			PublishedAt:     time.Now(),
		},
		{
			ID:              "scriptcrawler-crawler-main-migrated",
			DriveID:         "p115-target",
			FileID:          "uploaded-id",
			FileName:        "migrated.mp4",
			Title:           "Migrated",
			Size:            456,
			Ext:             "mp4",
			ThumbnailURL:    "/p/thumb/scriptcrawler-crawler-main-migrated",
			PreviewStatus:   "ready",
			DurationSeconds: 34,
			PublishedAt:     time.Now(),
		},
	} {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed crawler video %s: %v", v.ID, err)
		}
		if err := cat.UpdateVideoFingerprint(ctx, v.ID, "sha-"+v.ID, "ready", ""); err != nil {
			t.Fatalf("seed crawler fingerprint %s: %v", v.ID, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/api/crawlers", nil)
	rr := httptest.NewRecorder()
	srv := &AdminServer{Catalog: cat}
	srv.handleListCrawlers(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var got []struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		Kind             string `json:"kind"`
		Proxy            string `json:"proxy"`
		UploadDriveID    string `json:"uploadDriveId"`
		TeaserEnabled    bool   `json:"teaserEnabled"`
		LastCrawlAt      int64  `json:"lastCrawlAt"`
		TotalCrawled     int    `json:"totalCrawledCount"`
		LocalVideos      int    `json:"localVideoCount"`
		MigratedVideo    int    `json:"migratedVideoCount"`
		ThumbnailReady   int    `json:"thumbnailReadyCount"`
		TeaserReady      int    `json:"teaserReadyCount"`
		FingerprintReady int    `json:"fingerprintReadyCount"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	type crawlerListRow struct {
		Name             string
		Kind             string
		Proxy            string
		UploadDriveID    string
		TeaserEnabled    bool
		LastCrawlAt      int64
		TotalCrawled     int
		LocalVideos      int
		MigratedVideo    int
		ThumbnailReady   int
		TeaserReady      int
		FingerprintReady int
	}
	byID := map[string]crawlerListRow{}
	for _, d := range got {
		byID[d.ID] = crawlerListRow{
			Name:             d.Name,
			Kind:             d.Kind,
			Proxy:            d.Proxy,
			UploadDriveID:    d.UploadDriveID,
			TeaserEnabled:    d.TeaserEnabled,
			LastCrawlAt:      d.LastCrawlAt,
			TotalCrawled:     d.TotalCrawled,
			LocalVideos:      d.LocalVideos,
			MigratedVideo:    d.MigratedVideo,
			ThumbnailReady:   d.ThumbnailReady,
			TeaserReady:      d.TeaserReady,
			FingerprintReady: d.FingerprintReady,
		}
	}
	if _, ok := byID["crawler-script-deleted"]; ok {
		t.Fatal("crawler without script_path should not be returned by crawler list")
	}
	if byID["crawler-main"].Kind != "scriptcrawler" {
		t.Fatalf("crawler kind = %q, want scriptcrawler", byID["crawler-main"].Kind)
	}
	if byID["crawler-main"].Name != "Demo Crawler" {
		t.Fatalf("crawler name = %q, want script metadata name", byID["crawler-main"].Name)
	}
	if byID["crawler-main"].Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("crawler proxy = %q, want trimmed proxy", byID["crawler-main"].Proxy)
	}
	if byID["crawler-main"].UploadDriveID != "p115-target" {
		t.Fatalf("uploadDriveId = %q, want p115-target", byID["crawler-main"].UploadDriveID)
	}
	if byID["crawler-main"].TeaserEnabled {
		t.Fatal("teaserEnabled = true, want false from crawler drive")
	}
	if byID["crawler-main"].LastCrawlAt != 1800000000 {
		t.Fatalf("lastCrawlAt = %d, want 1800000000", byID["crawler-main"].LastCrawlAt)
	}
	if byID["crawler-main"].TotalCrawled != 2 || byID["crawler-main"].LocalVideos != 1 || byID["crawler-main"].MigratedVideo != 1 {
		t.Fatalf("crawler counts = total %d local %d migrated %d, want 2/1/1", byID["crawler-main"].TotalCrawled, byID["crawler-main"].LocalVideos, byID["crawler-main"].MigratedVideo)
	}
	if byID["crawler-main"].ThumbnailReady != 2 || byID["crawler-main"].TeaserReady != 2 || byID["crawler-main"].FingerprintReady != 2 {
		t.Fatalf("asset ready counts = thumb %d teaser %d fingerprint %d, want 2/2/2", byID["crawler-main"].ThumbnailReady, byID["crawler-main"].TeaserReady, byID["crawler-main"].FingerprintReady)
	}
	if _, ok := byID["onedrive-main"]; ok {
		t.Fatal("onedrive should not be returned by crawler list")
	}

	driveReq := httptest.NewRequest(http.MethodGet, "/admin/api/drives", nil)
	driveRR := httptest.NewRecorder()
	srv.handleListDrives(driveRR, driveReq)
	if driveRR.Code != http.StatusOK {
		t.Fatalf("drive status = %d, body = %s", driveRR.Code, driveRR.Body.String())
	}
	var drives []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(driveRR.Body).Decode(&drives); err != nil {
		t.Fatalf("decode drives: %v", err)
	}
	driveIDs := map[string]bool{}
	for _, d := range drives {
		driveIDs[d.ID] = true
	}
	if driveIDs["crawler-main"] {
		t.Fatal("scriptcrawler should not be returned by drive list")
	}
}

func TestHandleUpsertCrawlerRequiresScriptPath(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cat, err := catalog.Open(filepath.Join(tmp, "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	srv := &AdminServer{Catalog: cat}
	scriptPath := filepath.Join(tmp, "custom.py")
	if err := os.WriteFile(scriptPath, []byte("CRAWLER_NAME = \"Demo Crawler\"\n"), 0o644); err != nil {
		t.Fatalf("write crawler script: %v", err)
	}

	// 不再内置任何爬虫：没有脚本路径的保存请求必须被拒绝，
	// 旧的 builtin 字段也不再有"免脚本"特权。
	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawlers", strings.NewReader(`{
		"id": "crawler-main",
		"builtin": "legacy",
		"scriptPath": "",
		"targetNew": "15"
	}`))
	rr := httptest.NewRecorder()
	srv.handleUpsertCrawler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s, want 400", rr.Code, rr.Body.String())
	}

	// 带脚本路径时正常保存，且请求中的 builtin 字段被忽略，不会写入凭证。
	req = httptest.NewRequest(http.MethodPost, "/admin/api/crawlers", strings.NewReader(`{
		"id": "crawler-main",
		"builtin": "legacy",
		"scriptPath": "`+scriptPath+`",
		"targetNew": "15",
		"teaserEnabled": false
	}`))
	rr = httptest.NewRecorder()
	srv.handleUpsertCrawler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	got, err := cat.GetDrive(ctx, "crawler-main")
	if err != nil {
		t.Fatalf("get crawler drive: %v", err)
	}
	if got.Kind != "scriptcrawler" || got.Credentials["builtin"] != "" {
		t.Fatalf("kind/builtin = %q/%q, want scriptcrawler with no builtin credential", got.Kind, got.Credentials["builtin"])
	}
	if got.Credentials["python_path"] != "" || got.Credentials["config_json"] != "" {
		t.Fatalf("legacy hidden credentials should not be saved: %+v", got.Credentials)
	}
	if got.Name != "Demo Crawler" {
		t.Fatalf("name = %q, want script metadata name", got.Name)
	}
	if got.Credentials["script_path"] != scriptPath {
		t.Fatalf("script_path = %q, want %q", got.Credentials["script_path"], scriptPath)
	}
	if got.TeaserEnabled {
		t.Fatal("teaserEnabled = true, want false from request")
	}
}

func TestHandleUpsertCrawlerGeneratesIDFromScriptName(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cat, err := catalog.Open(filepath.Join(tmp, "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	if err := cat.UpsertDrive(ctx, &catalog.Drive{
		ID:          "crawler-my-spider",
		Kind:        scriptcrawler.Kind,
		Name:        "Existing",
		RootID:      "/",
		Credentials: map[string]string{"script_path": "/opt/crawlers/existing.py"},
	}); err != nil {
		t.Fatalf("seed crawler: %v", err)
	}
	scriptPath := filepath.Join(tmp, "custom.py")
	if err := os.WriteFile(scriptPath, []byte("CRAWLER_NAME = \"My Spider\"\n"), 0o644); err != nil {
		t.Fatalf("write crawler script: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawlers", strings.NewReader(`{
		"scriptPath": "`+scriptPath+`",
		"targetNew": "15"
	}`))
	rr := httptest.NewRecorder()
	(&AdminServer{Catalog: cat}).handleUpsertCrawler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		OK bool   `json:"ok"`
		ID string `json:"id"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.ID != "crawler-my-spider-2" {
		t.Fatalf("response = %+v, want generated suffix id", resp)
	}
	got, err := cat.GetDrive(ctx, resp.ID)
	if err != nil {
		t.Fatalf("get generated crawler: %v", err)
	}
	if got.Name != "My Spider" || got.Kind != scriptcrawler.Kind {
		t.Fatalf("generated crawler = %+v", got)
	}
}

func TestHandleUpsertCrawlerPersistsAndValidatesUploadDrive(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cat, err := catalog.Open(filepath.Join(tmp, "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})
	scriptPath := filepath.Join(tmp, "custom.py")
	if err := os.WriteFile(scriptPath, []byte("CRAWLER_NAME = \"Upload Spider\"\n"), 0o644); err != nil {
		t.Fatalf("write crawler script: %v", err)
	}
	for _, d := range []*catalog.Drive{
		{ID: "p115-target", Kind: "p115", Name: "115", RootID: "0", Credentials: map[string]string{"cookie": "x"}},
		{ID: "wopan-target", Kind: "wopan", Name: "沃盘", RootID: "0", Credentials: map[string]string{"access_token": "a", "refresh_token": "r"}},
		{ID: "guangyapan-target", Kind: "guangyapan", Name: "光鸭", RootID: "", Credentials: map[string]string{"access_token": "a", "refresh_token": "r"}},
		{ID: "local-target", Kind: "localstorage", Name: "Local", RootID: "/", Credentials: map[string]string{"path": tmp}},
	} {
		if err := cat.UpsertDrive(ctx, d); err != nil {
			t.Fatalf("seed drive %s: %v", d.ID, err)
		}
	}
	var teaserCallbackID string
	var teaserCallbackEnabled bool
	srv := &AdminServer{
		Catalog: cat,
		OnTeaserEnabledChanged: func(id string, enabled bool) {
			teaserCallbackID = id
			teaserCallbackEnabled = enabled
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawlers", strings.NewReader(`{
		"id": "crawler-upload",
		"scriptPath": "`+scriptPath+`",
		"uploadDriveId": "p115-target",
		"teaserEnabled": false
	}`))
	rr := httptest.NewRecorder()
	srv.handleUpsertCrawler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	got, err := cat.GetDrive(ctx, "crawler-upload")
	if err != nil {
		t.Fatalf("get crawler: %v", err)
	}
	if got.Credentials["upload_drive_id"] != "p115-target" {
		t.Fatalf("upload_drive_id = %q, want p115-target", got.Credentials["upload_drive_id"])
	}
	if got.TeaserEnabled {
		t.Fatal("teaserEnabled = true, want false")
	}
	if teaserCallbackID != "" {
		t.Fatalf("teaser callback on create = %q, want none", teaserCallbackID)
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/api/crawlers", strings.NewReader(`{
		"id": "crawler-upload",
		"scriptPath": "`+scriptPath+`",
		"uploadDriveId": "wopan-target"
	}`))
	rr = httptest.NewRecorder()
	srv.handleUpsertCrawler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("wopan target status = %d, body = %s", rr.Code, rr.Body.String())
	}
	got, err = cat.GetDrive(ctx, "crawler-upload")
	if err != nil {
		t.Fatalf("get crawler after wopan target: %v", err)
	}
	if got.Credentials["upload_drive_id"] != "wopan-target" {
		t.Fatalf("upload_drive_id = %q, want wopan-target", got.Credentials["upload_drive_id"])
	}
	if got.TeaserEnabled {
		t.Fatal("teaserEnabled after edit without field = true, want preserved false")
	}
	if teaserCallbackID != "" {
		t.Fatalf("teaser callback after preserved edit = %q, want none", teaserCallbackID)
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/api/crawlers", strings.NewReader(`{
		"id": "crawler-upload",
		"scriptPath": "`+scriptPath+`",
		"uploadDriveId": "guangyapan-target"
	}`))
	rr = httptest.NewRecorder()
	srv.handleUpsertCrawler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("guangyapan target status = %d, body = %s", rr.Code, rr.Body.String())
	}
	got, err = cat.GetDrive(ctx, "crawler-upload")
	if err != nil {
		t.Fatalf("get crawler after guangyapan target: %v", err)
	}
	if got.Credentials["upload_drive_id"] != "guangyapan-target" {
		t.Fatalf("upload_drive_id = %q, want guangyapan-target", got.Credentials["upload_drive_id"])
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/api/crawlers", strings.NewReader(`{
		"id": "crawler-upload",
		"scriptPath": "`+scriptPath+`",
		"uploadDriveId": "wopan-target",
		"teaserEnabled": true
	}`))
	rr = httptest.NewRecorder()
	srv.handleUpsertCrawler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("enable teaser status = %d, body = %s", rr.Code, rr.Body.String())
	}
	got, err = cat.GetDrive(ctx, "crawler-upload")
	if err != nil {
		t.Fatalf("get crawler after teaser enable: %v", err)
	}
	if !got.TeaserEnabled {
		t.Fatal("teaserEnabled after explicit enable = false, want true")
	}
	if teaserCallbackID != "crawler-upload" || !teaserCallbackEnabled {
		t.Fatalf("teaser callback = %q/%v, want crawler-upload/true", teaserCallbackID, teaserCallbackEnabled)
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/api/crawlers", strings.NewReader(`{
		"id": "crawler-upload",
		"scriptPath": "`+scriptPath+`",
		"uploadDriveId": "local-target"
	}`))
	rr = httptest.NewRecorder()
	srv.handleUpsertCrawler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid target status = %d, body = %s, want 400", rr.Code, rr.Body.String())
	}
}

func TestHandleImportCrawlerScriptFile(t *testing.T) {
	tmp := t.TempDir()
	script := "CRAWLER_NAME = \"Demo Crawler\"\nprint('crawler')\n"
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "../demo crawler.py")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte(script)); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawlers/import-file", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	(&AdminServer{LocalPreviewDir: filepath.Join(tmp, "previews")}).handleImportCrawlerScriptFile(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		ScriptPath string `json:"scriptPath"`
		Name       string `json:"name"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantRoot := filepath.Join(tmp, "crawler-scripts")
	if !strings.HasPrefix(got.ScriptPath, wantRoot+string(os.PathSeparator)) {
		t.Fatalf("script path = %q, want under %q", got.ScriptPath, wantRoot)
	}
	if filepath.Ext(got.ScriptPath) != ".py" {
		t.Fatalf("script path = %q, want .py", got.ScriptPath)
	}
	if filepath.Base(got.ScriptPath) != "demo_crawler.py" {
		t.Fatalf("script filename = %q, want original sanitized filename", filepath.Base(got.ScriptPath))
	}
	data, err := os.ReadFile(got.ScriptPath)
	if err != nil {
		t.Fatalf("read imported script: %v", err)
	}
	if got.Name != "Demo Crawler" {
		t.Fatalf("name = %q, want script metadata name", got.Name)
	}
	if string(data) != script {
		t.Fatalf("script content = %q", string(data))
	}
}

func TestHandleImportCrawlerScriptFileRejectsMissingName(t *testing.T) {
	tmp := t.TempDir()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "crawler.py")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("print('crawler')\n")); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawlers/import-file", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	(&AdminServer{LocalPreviewDir: filepath.Join(tmp, "previews")}).handleImportCrawlerScriptFile(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "CRAWLER_NAME") {
		t.Fatalf("body = %s, want CRAWLER_NAME error", rr.Body.String())
	}
}

func TestHandleImportCrawlerScriptFileRejectsNonPython(t *testing.T) {
	tmp := t.TempDir()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "crawler.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("print('crawler')\n")); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawlers/import-file", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	(&AdminServer{LocalPreviewDir: filepath.Join(tmp, "previews")}).handleImportCrawlerScriptFile(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ".py") {
		t.Fatalf("body = %s, want .py error", rr.Body.String())
	}
}

func TestHandleImportCrawlerScriptURL(t *testing.T) {
	tmp := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crawler.py" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("CRAWLER_NAME = \"URL Crawler\"\n# crawler from url\n"))
	}))
	defer upstream.Close()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawlers/import-url", strings.NewReader(`{
		"url": "`+upstream.URL+`/crawler.py"
	}`))
	rr := httptest.NewRecorder()
	(&AdminServer{LocalPreviewDir: filepath.Join(tmp, "previews")}).handleImportCrawlerScriptURL(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		ScriptPath string `json:"scriptPath"`
		Name       string `json:"name"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantRoot := filepath.Join(tmp, "crawler-scripts")
	if !strings.HasPrefix(got.ScriptPath, wantRoot+string(os.PathSeparator)) {
		t.Fatalf("script path = %q, want under %q", got.ScriptPath, wantRoot)
	}
	data, err := os.ReadFile(got.ScriptPath)
	if err != nil {
		t.Fatalf("read imported script: %v", err)
	}
	if got.Name != "URL Crawler" {
		t.Fatalf("name = %q, want script metadata name", got.Name)
	}
	if filepath.Base(got.ScriptPath) != "crawler.py" {
		t.Fatalf("script filename = %q, want original filename", filepath.Base(got.ScriptPath))
	}
	if string(data) != "CRAWLER_NAME = \"URL Crawler\"\n# crawler from url\n" {
		t.Fatalf("script content = %q", string(data))
	}
}

func TestCrawlerScriptDownloadURLConvertsGitHubBlob(t *testing.T) {
	input, err := url.Parse("https://github.com/Just-Spider/SpiderFor91/blob/main/91Porn/91Porn.py")
	if err != nil {
		t.Fatalf("parse input: %v", err)
	}
	got := crawlerScriptDownloadURL(input)
	want := "https://raw.githubusercontent.com/Just-Spider/SpiderFor91/main/91Porn/91Porn.py"
	if got.String() != want {
		t.Fatalf("download URL = %q, want %q", got.String(), want)
	}
}

func TestCrawlerScriptDownloadURLKeepsNonGitHubURL(t *testing.T) {
	input, err := url.Parse("https://example.com/crawlers/demo.py")
	if err != nil {
		t.Fatalf("parse input: %v", err)
	}
	got := crawlerScriptDownloadURL(input)
	if got.String() != input.String() {
		t.Fatalf("download URL = %q, want original %q", got.String(), input.String())
	}
}

func TestHandleDeleteCrawlerRemovesImportedScript(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cat, err := catalog.Open(filepath.Join(tmp, "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	scriptDir := filepath.Join(tmp, "crawler-scripts")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatalf("mkdir script dir: %v", err)
	}
	scriptPath := filepath.Join(scriptDir, "crawler.py")
	if err := os.WriteFile(scriptPath, []byte("CRAWLER_NAME = \"Delete Me\"\n"), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := cat.UpsertDrive(ctx, &catalog.Drive{
		ID:     "crawler-delete-me",
		Kind:   scriptcrawler.Kind,
		Name:   "Delete Me",
		RootID: "/",
		Credentials: map[string]string{
			"script_path": scriptPath,
			"proxy":       "http://127.0.0.1:7890",
			"target_new":  "10",
		},
	}); err != nil {
		t.Fatalf("seed crawler: %v", err)
	}
	now := time.Now()
	if err := cat.UpsertVideo(ctx, &catalog.Video{
		ID:          "video-from-crawler",
		DriveID:     "crawler-delete-me",
		FileID:      "video.mp4",
		Title:       "Keep Me",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/admin/api/crawlers/crawler-delete-me", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "crawler-delete-me")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	stopped := false
	(&AdminServer{
		Catalog:         cat,
		LocalPreviewDir: filepath.Join(tmp, "previews"),
		OnDriveDeleteCleanup: func(context.Context, string) (int, error) {
			t.Fatal("crawler delete must not delete imported videos")
			return 0, nil
		},
		OnStopDriveTasks: func(driveID string) bool {
			stopped = driveID == "crawler-delete-me"
			return true
		},
	}).handleDeleteCrawler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(scriptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("script stat error = %v, want not exist", err)
	}
	if !stopped {
		t.Fatal("stop hook was not called")
	}
	drive, err := cat.GetDrive(ctx, "crawler-delete-me")
	if err != nil {
		t.Fatalf("crawler drive should remain for existing videos: %v", err)
	}
	if drive.Credentials["script_path"] != "" || drive.Credentials["proxy"] != "" || drive.Credentials["target_new"] != "" {
		t.Fatalf("crawler credentials were not cleared: %+v", drive.Credentials)
	}
	if _, err := cat.GetVideo(ctx, "video-from-crawler"); err != nil {
		t.Fatalf("imported video should remain: %v", err)
	}
	var got struct {
		OK            bool `json:"ok"`
		DeletedVideos int  `json:"deletedVideos"`
		DeletedScript bool `json:"deletedScript"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.OK || got.DeletedVideos != 0 || !got.DeletedScript {
		t.Fatalf("response = %#v", got)
	}
}

func TestHandleImportCrawlerScriptURLRejectsNonPython(t *testing.T) {
	tmp := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crawler.txt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("# crawler from url\n"))
	}))
	defer upstream.Close()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawlers/import-url", strings.NewReader(`{
		"url": "`+upstream.URL+`/crawler.txt"
	}`))
	rr := httptest.NewRecorder()
	(&AdminServer{LocalPreviewDir: filepath.Join(tmp, "previews")}).handleImportCrawlerScriptURL(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ".py") {
		t.Fatalf("body = %s, want .py error", rr.Body.String())
	}
}

func TestHandleWopanQRStart(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/QRCode/generate" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta": map[string]string{"code": "0000", "message": "ok"},
			"result": map[string]string{
				"uuid":  "uuid-1",
				"image": "iVBORw0KGgo=",
			},
		})
	}))
	t.Cleanup(upstream.Close)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/drives/wopan/qr", nil)
	rr := httptest.NewRecorder()
	(&AdminServer{WopanQRAPIBaseURL: upstream.URL + "/QRCode"}).handleWopanQRStart(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		UUID           string `json:"uuid"`
		QRImageDataURL string `json:"qrImageDataUrl"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.UUID != "uuid-1" || got.QRImageDataURL != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("response = %#v", got)
	}
}

func TestHandleWopanQRStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/QRCode/query" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("uuid") != "uuid-1" {
			t.Fatalf("uuid = %q, want uuid-1", r.URL.Query().Get("uuid"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta": map[string]string{"code": "0000", "message": "ok"},
			"result": map[string]any{
				"state":        3,
				"token":        "access-1",
				"refreshToken": "refresh-1",
			},
		})
	}))
	t.Cleanup(upstream.Close)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/drives/wopan/qr/uuid-1", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("uuid", "uuid-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	(&AdminServer{WopanQRAPIBaseURL: upstream.URL + "/QRCode"}).handleWopanQRStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		State        int    `json:"state"`
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.State != 3 || got.AccessToken != "access-1" || got.RefreshToken != "refresh-1" {
		t.Fatalf("response = %#v", got)
	}
}

func TestHandleGuangYaPanQRStart(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/auth/device/code" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["scope"] != "user" {
			t.Fatalf("scope = %#v, want user", body["scope"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "device-1",
			"verification_uri_complete": "https://account.guangyapan.example/device?code=abc",
			"interval":                  5,
			"expires_in":                300,
		})
	}))
	t.Cleanup(upstream.Close)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/drives/guangyapan/qr", nil)
	rr := httptest.NewRecorder()
	(&AdminServer{GuangYaPanAccountBaseURL: upstream.URL}).handleGuangYaPanQRStart(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		DeviceCode     string `json:"deviceCode"`
		QRCodeURL      string `json:"qrCodeUrl"`
		QRImageDataURL string `json:"qrImageDataUrl"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.DeviceCode != "device-1" || got.QRCodeURL != "https://account.guangyapan.example/device?code=abc" {
		t.Fatalf("response = %#v", got)
	}
	if !strings.HasPrefix(got.QRImageDataURL, "data:image/png;base64,") {
		t.Fatalf("qr image = %q", got.QRImageDataURL)
	}
}

func TestHandleGuangYaPanQRStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/auth/token" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["device_code"] != "device-1" {
			t.Fatalf("device_code = %#v, want device-1", body["device_code"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"token_type":    "Bearer",
		})
	}))
	t.Cleanup(upstream.Close)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/drives/guangyapan/qr/status?deviceCode=device-1", nil)
	rr := httptest.NewRecorder()
	(&AdminServer{GuangYaPanAccountBaseURL: upstream.URL}).handleGuangYaPanQRStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		State        string `json:"state"`
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.State != "success" || got.AccessToken != "access-1" || got.RefreshToken != "refresh-1" {
		t.Fatalf("response = %#v", got)
	}
}

func TestHandleTestCrawlerScriptRunsImportedScript(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for crawler script dry-run")
	}
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/video.mp4" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/2048")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte{0})
			return
		}
		_, _ = w.Write([]byte("video"))
	}))
	defer media.Close()

	script := filepath.Join(t.TempDir(), "crawler.py")
	body := `import json
print(json.dumps({"title": "Dry Run Video", "source_id": "dry-1", "media_url": "` + media.URL + `/video.mp4", "thumbnail_url": "` + media.URL + `/thumb.jpg", "detail_url": "` + media.URL + `/detail"}))
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	reqBody, err := json.Marshal(map[string]string{
		"scriptPath": script,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawlers/test-script", bytes.NewReader(reqBody))
	rr := httptest.NewRecorder()
	(&AdminServer{}).handleTestCrawlerScript(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var got struct {
		OK    bool `json:"ok"`
		Items []struct {
			Title    string `json:"title"`
			SourceID string `json:"sourceId"`
			MediaURL string `json:"mediaUrl"`
		} `json:"items"`
		MediaCheck *struct {
			OK            bool   `json:"ok"`
			Status        int    `json:"status"`
			ContentType   string `json:"contentType"`
			ContentLength int64  `json:"contentLengthBytes"`
		} `json:"mediaCheck"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.OK {
		t.Fatalf("ok = false, body = %s", rr.Body.String())
	}
	if len(got.Items) != 1 || got.Items[0].Title != "Dry Run Video" || got.Items[0].SourceID != "dry-1" {
		t.Fatalf("items = %#v", got.Items)
	}
	if got.Items[0].MediaURL != media.URL+"/video.mp4" {
		t.Fatalf("mediaUrl = %q", got.Items[0].MediaURL)
	}
	if got.MediaCheck == nil || !got.MediaCheck.OK || got.MediaCheck.Status != http.StatusPartialContent {
		t.Fatalf("mediaCheck = %#v", got.MediaCheck)
	}
	if got.MediaCheck.ContentLength != 2048 {
		t.Fatalf("contentLength = %d, want 2048", got.MediaCheck.ContentLength)
	}
}

func TestHandleListDrivesIncludesGoogleDriveOnlineAPIMode(t *testing.T) {
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
		{
			ID:     "google-legacy",
			Kind:   "googledrive",
			Name:   "Google Legacy",
			RootID: "root",
			Credentials: map[string]string{
				"refresh_token":   "legacy-refresh",
				"api_url_address": "https://openlist-api.example/googleui/renewapi",
			},
			Status: "ok",
		},
		{
			ID:     "google-oauth",
			Kind:   "googledrive",
			Name:   "Google OAuth",
			RootID: "root",
			Credentials: map[string]string{
				"refresh_token":  "oauth-refresh",
				"use_online_api": "false",
				"client_id":      "client-id",
				"client_secret":  "client-secret",
			},
			Status: "ok",
		},
	} {
		if err := cat.UpsertDrive(ctx, d); err != nil {
			t.Fatalf("seed drive %s: %v", d.ID, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/api/drives", nil)
	rr := httptest.NewRecorder()
	(&AdminServer{Catalog: cat}).handleListDrives(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var got []struct {
		ID                        string `json:"id"`
		GoogleDriveUseOnlineAPI   bool   `json:"googleDriveUseOnlineAPI"`
		GoogleDriveOpenListAPIURL string `json:"googleDriveOpenListApiUrl"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]bool{}
	byAPIURL := map[string]string{}
	for _, d := range got {
		byID[d.ID] = d.GoogleDriveUseOnlineAPI
		byAPIURL[d.ID] = d.GoogleDriveOpenListAPIURL
	}
	if !byID["google-legacy"] {
		t.Fatalf("legacy google drive use_online_api = false, want true")
	}
	if byID["google-oauth"] {
		t.Fatalf("oauth google drive use_online_api = true, want false")
	}
	if byAPIURL["google-legacy"] != "https://openlist-api.example/googleui/renewapi" {
		t.Fatalf("legacy google drive openlist api url = %q, want custom URL", byAPIURL["google-legacy"])
	}
}

func TestHandleListDrivesIncludesTeaserCounts(t *testing.T) {
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
		{ID: "OneDrive", Kind: "onedrive", Name: "OneDrive", RootID: "root", Status: "ok"},
		{ID: "PikPak", Kind: "pikpak", Name: "PikPak", RootID: "", Status: "ok"},
	} {
		if err := cat.UpsertDrive(ctx, d); err != nil {
			t.Fatalf("seed drive %s: %v", d.ID, err)
		}
	}

	now := time.Now()
	videos := []*catalog.Video{
		{ID: "od-ready-1", DriveID: "OneDrive", FileID: "od-file-1", Title: "OD Ready 1", Size: 100, ThumbnailURL: "/p/thumb/od-ready-1", PreviewStatus: "ready", PublishedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "od-ready-2", DriveID: "OneDrive", FileID: "od-file-2", Title: "OD Ready 2", Size: 100, PreviewStatus: "ready", PublishedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "od-pending", DriveID: "OneDrive", FileID: "od-file-3", Title: "OD Pending", Size: 100, PreviewStatus: "pending", PublishedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "pp-pending", DriveID: "PikPak", FileID: "pp-file-1", Title: "PP Pending", Size: 100, PreviewStatus: "pending", PublishedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "pp-failed", DriveID: "PikPak", FileID: "pp-file-2", Title: "PP Failed", Size: 100, ThumbnailURL: "/p/thumb/pp-failed", PreviewStatus: "failed", PublishedAt: now, CreatedAt: now, UpdatedAt: now},
	}
	for _, v := range videos {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed video %s: %v", v.ID, err)
		}
	}
	if err := cat.UpdateVideoMeta(ctx, "od-ready-2", catalog.VideoMetaPatch{ThumbnailStatus: "failed"}); err != nil {
		t.Fatalf("mark thumbnail failed: %v", err)
	}
	if err := cat.UpdateVideoFingerprint(ctx, "od-ready-1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "ready", ""); err != nil {
		t.Fatalf("mark fingerprint ready: %v", err)
	}
	if err := cat.UpdateVideoFingerprint(ctx, "od-ready-2", "", "failed", "sample failed"); err != nil {
		t.Fatalf("mark fingerprint failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/api/drives", nil)
	rr := httptest.NewRecorder()
	(&AdminServer{
		Catalog: cat,
		GetDriveGenerationStatuses: func() map[string]DriveGenerationStatuses {
			return map[string]DriveGenerationStatuses{
				"OneDrive": {
					Scan:        GenerationStatus{State: "scanning", ScannedCount: 12, AddedCount: 3},
					Thumbnail:   GenerationStatus{State: "cooling", QueueLength: 3, CooldownUntil: "2026-05-16T21:00:00+08:00"},
					Preview:     GenerationStatus{State: "generating", CurrentTitle: "OD Pending"},
					Fingerprint: GenerationStatus{State: "generating", CurrentTitle: "OD Pending"},
				},
			}
		},
	}).handleListDrives(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got []struct {
		ID                            string           `json:"id"`
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
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]struct {
		TeaserReady              int
		TeaserPending            int
		TeaserFailed             int
		ThumbnailReady           int
		ThumbnailPending         int
		ThumbnailFailed          int
		ThumbnailDurationPending int
		FingerprintReady         int
		FingerprintPending       int
		FingerprintFailed        int
		Scan                     GenerationStatus
		Thumbnail                GenerationStatus
		Preview                  GenerationStatus
		Fingerprint              GenerationStatus
	}{}
	for _, d := range got {
		byID[d.ID] = struct {
			TeaserReady              int
			TeaserPending            int
			TeaserFailed             int
			ThumbnailReady           int
			ThumbnailPending         int
			ThumbnailFailed          int
			ThumbnailDurationPending int
			FingerprintReady         int
			FingerprintPending       int
			FingerprintFailed        int
			Scan                     GenerationStatus
			Thumbnail                GenerationStatus
			Preview                  GenerationStatus
			Fingerprint              GenerationStatus
		}{
			TeaserReady:              d.TeaserReadyCount,
			TeaserPending:            d.TeaserPendingCount,
			TeaserFailed:             d.TeaserFailedCount,
			ThumbnailReady:           d.ThumbnailReadyCount,
			ThumbnailPending:         d.ThumbnailPendingCount,
			ThumbnailFailed:          d.ThumbnailFailedCount,
			ThumbnailDurationPending: d.ThumbnailDurationPendingCount,
			FingerprintReady:         d.FingerprintReadyCount,
			FingerprintPending:       d.FingerprintPendingCount,
			FingerprintFailed:        d.FingerprintFailedCount,
			Scan:                     d.ScanGenerationStatus,
			Thumbnail:                d.ThumbnailGenerationStatus,
			Preview:                  d.PreviewGenerationStatus,
			Fingerprint:              d.FingerprintGenerationStatus,
		}
	}
	if byID["OneDrive"].TeaserReady != 2 || byID["OneDrive"].TeaserPending != 1 || byID["OneDrive"].TeaserFailed != 0 {
		t.Fatalf("OneDrive counts = %#v, want ready=2 pending=1 failed=0", byID["OneDrive"])
	}
	if byID["OneDrive"].ThumbnailReady != 1 || byID["OneDrive"].ThumbnailPending != 1 || byID["OneDrive"].ThumbnailFailed != 1 {
		t.Fatalf("OneDrive thumbnail counts = %#v, want ready=1 pending=1 failed=1", byID["OneDrive"])
	}
	if byID["OneDrive"].ThumbnailDurationPending != 1 {
		t.Fatalf("OneDrive thumbnail duration pending = %#v, want 1", byID["OneDrive"])
	}
	if byID["OneDrive"].Thumbnail.State != "cooling" || byID["OneDrive"].Preview.State != "generating" {
		t.Fatalf("OneDrive generation statuses = %#v, want thumbnail cooling and preview generating", byID["OneDrive"])
	}
	if byID["OneDrive"].Scan.State != "scanning" {
		t.Fatalf("OneDrive scan status = %#v, want scanning", byID["OneDrive"].Scan)
	}
	if byID["OneDrive"].Scan.ScannedCount != 12 || byID["OneDrive"].Scan.AddedCount != 3 {
		t.Fatalf("OneDrive scan counts = %#v, want scanned=12 added=3", byID["OneDrive"].Scan)
	}
	if byID["OneDrive"].FingerprintReady != 1 || byID["OneDrive"].FingerprintPending != 1 || byID["OneDrive"].FingerprintFailed != 1 {
		t.Fatalf("OneDrive fingerprint counts = %#v, want ready=1 pending=1 failed=1", byID["OneDrive"])
	}
	if byID["OneDrive"].Fingerprint.State != "generating" {
		t.Fatalf("OneDrive fingerprint status = %#v, want generating", byID["OneDrive"].Fingerprint)
	}
	if byID["PikPak"].TeaserReady != 0 || byID["PikPak"].TeaserPending != 1 || byID["PikPak"].TeaserFailed != 1 {
		t.Fatalf("PikPak counts = %#v, want ready=0 pending=1 failed=1", byID["PikPak"])
	}
	if byID["PikPak"].ThumbnailReady != 1 || byID["PikPak"].ThumbnailPending != 1 || byID["PikPak"].ThumbnailFailed != 0 {
		t.Fatalf("PikPak thumbnail counts = %#v, want ready=1 pending=1 failed=0", byID["PikPak"])
	}
	if byID["PikPak"].FingerprintPending != 2 {
		t.Fatalf("PikPak fingerprint counts = %#v, want pending=2", byID["PikPak"])
	}
	if byID["PikPak"].Scan.State != "idle" || byID["PikPak"].Thumbnail.State != "idle" || byID["PikPak"].Preview.State != "idle" || byID["PikPak"].Fingerprint.State != "idle" {
		t.Fatalf("PikPak generation statuses = %#v, want idle defaults", byID["PikPak"])
	}
}

func TestHandleRegenFailedFingerprintsInvokesHook(t *testing.T) {
	called := ""
	req := httptest.NewRequest(http.MethodPost, "/admin/api/drives/drive-one/fingerprints/failed/regenerate", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "drive-one")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	(&AdminServer{
		OnRegenFailedFingerprints: func(driveID string) {
			called = driveID
		},
	}).handleRegenFailedFingerprints(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if called != "drive-one" {
		t.Fatalf("called drive = %q, want drive-one", called)
	}
}

func TestHandleDriveStorageReportsLocalMediaUsage(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cat, err := catalog.Open(filepath.Join(root, "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	localDir := filepath.Join(root, "previews")
	thumbDir := filepath.Join(localDir, "thumbs")
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		t.Fatalf("mkdir thumbs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "drive-one-video.mp4"), []byte("teaser-one"), 0o644); err != nil {
		t.Fatalf("write teaser one: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "drive-two-video.mp4"), []byte("teaser-two!!"), 0o644); err != nil {
		t.Fatalf("write teaser two: %v", err)
	}
	if err := os.WriteFile(filepath.Join(thumbDir, "drive-one-video.jpg"), []byte("jpg-one"), 0o644); err != nil {
		t.Fatalf("write thumb one: %v", err)
	}
	if err := os.WriteFile(filepath.Join(thumbDir, "drive-two-video.jpg"), []byte("jpg-two!!"), 0o644); err != nil {
		t.Fatalf("write thumb two: %v", err)
	}

	for _, d := range []*catalog.Drive{
		{ID: "drive-one", Kind: "onedrive", Name: "Drive One", RootID: "root", Status: "ok"},
		{ID: "drive-two", Kind: "pikpak", Name: "Drive Two", RootID: "", Status: "ok"},
	} {
		if err := cat.UpsertDrive(ctx, d); err != nil {
			t.Fatalf("seed drive %s: %v", d.ID, err)
		}
	}
	now := time.Now()
	for _, v := range []*catalog.Video{
		{
			ID:            "drive-one-video",
			DriveID:       "drive-one",
			FileID:        "file-one",
			Title:         "Video One",
			PreviewLocal:  filepath.Join(localDir, "drive-one-video.mp4"),
			PreviewStatus: "ready",
			PublishedAt:   now,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		{
			ID:            "drive-two-video",
			DriveID:       "drive-two",
			FileID:        "file-two",
			Title:         "Video Two",
			PreviewLocal:  filepath.Join(localDir, "drive-two-video.mp4"),
			PreviewStatus: "ready",
			PublishedAt:   now,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
	} {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed video %s: %v", v.ID, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/api/drives/storage", nil)
	rr := httptest.NewRecorder()
	(&AdminServer{Catalog: cat, LocalPreviewDir: localDir}).handleDriveStorage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		ThumbnailBytes int64 `json:"thumbnailBytes"`
		TeaserBytes    int64 `json:"teaserBytes"`
		TotalBytes     int64 `json:"totalBytes"`
		AvailableBytes int64 `json:"availableBytes"`
		Drives         map[string]struct {
			ThumbnailBytes int64 `json:"thumbnailBytes"`
			TeaserBytes    int64 `json:"teaserBytes"`
			TotalBytes     int64 `json:"totalBytes"`
		} `json:"drives"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ThumbnailBytes != int64(len("jpg-one")+len("jpg-two!!")) {
		t.Fatalf("thumbnail bytes = %d, want %d", got.ThumbnailBytes, len("jpg-one")+len("jpg-two!!"))
	}
	if got.TeaserBytes != int64(len("teaser-one")+len("teaser-two!!")) {
		t.Fatalf("teaser bytes = %d, want %d", got.TeaserBytes, len("teaser-one")+len("teaser-two!!"))
	}
	if got.TotalBytes != got.ThumbnailBytes+got.TeaserBytes {
		t.Fatalf("total bytes = %d, want thumbnail + teaser", got.TotalBytes)
	}
	if got.AvailableBytes <= 0 {
		t.Fatalf("available bytes = %d, want positive", got.AvailableBytes)
	}
	if got.Drives["drive-one"].ThumbnailBytes != int64(len("jpg-one")) ||
		got.Drives["drive-one"].TeaserBytes != int64(len("teaser-one")) {
		t.Fatalf("drive-one usage = %#v", got.Drives["drive-one"])
	}
	if got.Drives["drive-two"].TotalBytes != int64(len("jpg-two!!")+len("teaser-two!!")) {
		t.Fatalf("drive-two total = %d, want %d", got.Drives["drive-two"].TotalBytes, len("jpg-two!!")+len("teaser-two!!"))
	}
}

func TestHandleCreateTagClassifiesExistingVideos(t *testing.T) {
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
		Title:       "清纯短发",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/tags", strings.NewReader(`{"label":"清纯"}`))
	rr := httptest.NewRecorder()
	(&AdminServer{Catalog: cat}).handleCreateTag(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Label      string `json:"label"`
		Classified int    `json:"classified"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Label != "清纯" || got.Classified != 1 {
		t.Fatalf("response = %#v, want 清纯 classified 1", got)
	}

	video, err := cat.GetVideo(ctx, "video-1")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if len(video.Tags) != 1 || video.Tags[0] != "清纯" {
		t.Fatalf("video tags = %#v, want 清纯", video.Tags)
	}
}

func TestHandleDeleteTagRemovesTagFromVideos(t *testing.T) {
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
		Title:       "清纯短发",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := cat.CreateTagAndClassify(ctx, "清纯", nil, "user"); err != nil {
		t.Fatalf("create tag: %v", err)
	}
	tags, err := cat.ListTags(ctx)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	var tagID int64
	for _, tag := range tags {
		if tag.Label == "清纯" {
			tagID = tag.ID
			break
		}
	}
	if tagID == 0 {
		t.Fatal("created tag not found")
	}

	req := requestWithRouteParam(http.MethodDelete, "/admin/api/tags/1", "id", strconv.FormatInt(tagID, 10), strings.NewReader(``))
	rr := httptest.NewRecorder()
	(&AdminServer{Catalog: cat}).handleDeleteTag(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	video, err := cat.GetVideo(ctx, "video-1")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if len(video.Tags) != 0 {
		t.Fatalf("video tags = %#v, want none", video.Tags)
	}
}

func TestHandleAdminListVideosFiltersByDriveID(t *testing.T) {
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
	videos := []*catalog.Video{
		{
			ID:          "od-video",
			DriveID:     "OneDrive",
			FileID:      "od-file",
			Title:       "OneDrive video",
			PublishedAt: now,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "pp-video",
			DriveID:     "PikPak",
			FileID:      "pp-file",
			Title:       "PikPak video",
			PublishedAt: now.Add(-time.Hour),
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}
	for _, v := range videos {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed video %s: %v", v.ID, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/api/videos?driveId=OneDrive", nil)
	rr := httptest.NewRecorder()
	(&AdminServer{Catalog: cat}).handleAdminListVideos(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Items []catalog.Video `json:"items"`
		Total int             `json:"total"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Total != 1 || len(got.Items) != 1 {
		t.Fatalf("response total/items = %d/%d, want 1/1: %#v", got.Total, len(got.Items), got.Items)
	}
	if got.Items[0].DriveID != "OneDrive" || got.Items[0].ID != "od-video" {
		t.Fatalf("item = %#v, want OneDrive od-video", got.Items[0])
	}
}

func TestHandleAdminListVideosDoesNotExposeCategory(t *testing.T) {
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
		Title:       "Video",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/api/videos", nil)
	rr := httptest.NewRecorder()
	(&AdminServer{Catalog: cat}).handleAdminListVideos(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items len = %d, want 1", len(got.Items))
	}
	if _, ok := got.Items[0]["category"]; ok {
		t.Fatalf("admin video response exposed category: %#v", got.Items[0])
	}
}

func TestHandleAdminListVideosPaginates(t *testing.T) {
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
	for i, title := range []string{"first", "second", "third"} {
		v := &catalog.Video{
			ID:          title,
			DriveID:     "OneDrive",
			FileID:      title + "-file",
			Title:       title,
			PublishedAt: now.Add(-time.Duration(i) * time.Hour),
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed video %s: %v", v.ID, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/api/videos?driveId=OneDrive&page=2&size=2", nil)
	rr := httptest.NewRecorder()
	(&AdminServer{Catalog: cat}).handleAdminListVideos(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Items []catalog.Video `json:"items"`
		Total int             `json:"total"`
		Page  int             `json:"page"`
		Size  int             `json:"size"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Total != 3 || got.Page != 2 || got.Size != 2 {
		t.Fatalf("pagination meta = total:%d page:%d size:%d, want 3/2/2", got.Total, got.Page, got.Size)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "third" {
		t.Fatalf("items = %#v, want only third", got.Items)
	}
}

func TestHandleAdminListVideosMarksActivePreviewGeneration(t *testing.T) {
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
			ID:            "active-video",
			DriveID:       "OneDrive",
			FileID:        "active-file",
			Title:         "Active video",
			PreviewStatus: "ready",
			PublishedAt:   now,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		{
			ID:            "idle-video",
			DriveID:       "OneDrive",
			FileID:        "idle-file",
			Title:         "Idle video",
			PreviewStatus: "ready",
			PublishedAt:   now.Add(-time.Hour),
			CreatedAt:     now,
			UpdatedAt:     now,
		},
	} {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed video %s: %v", v.ID, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/api/videos?driveId=OneDrive", nil)
	rr := httptest.NewRecorder()
	(&AdminServer{
		Catalog: cat,
		GetPreviewGenerationVideoIDs: func() map[string]bool {
			return map[string]bool{"active-video": true}
		},
	}).handleAdminListVideos(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Items []catalog.Video `json:"items"`
		Total int             `json:"total"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Total != 2 || len(got.Items) != 2 {
		t.Fatalf("response total/items = %d/%d, want 2/2", got.Total, len(got.Items))
	}
	statusByID := map[string]string{}
	for _, item := range got.Items {
		statusByID[item.ID] = item.PreviewStatus
	}
	if statusByID["active-video"] != "generating" {
		t.Fatalf("active status = %q, want generating", statusByID["active-video"])
	}
	if statusByID["idle-video"] != "ready" {
		t.Fatalf("idle status = %q, want ready", statusByID["idle-video"])
	}
}

func TestHandleRegenAllPreviewsInvokesHook(t *testing.T) {
	called := false
	server := &AdminServer{
		OnRegenAllPreviews: func() {
			called = true
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/videos/regen-preview", nil)
	rr := httptest.NewRecorder()
	server.handleRegenAllPreviews(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Fatal("regen all previews hook was not called")
	}
}

func TestHandleRegenFailedPreviewsInvokesHookWithDriveID(t *testing.T) {
	calledWith := ""
	server := &AdminServer{
		OnRegenFailedPreviews: func(driveID string) {
			calledWith = driveID
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/drives/PikPak/previews/failed/regenerate", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "PikPak")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	server.handleRegenFailedPreviews(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if calledWith != "PikPak" {
		t.Fatalf("hook called with %q, want PikPak", calledWith)
	}
}
