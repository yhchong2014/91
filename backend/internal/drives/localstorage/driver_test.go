package localstorage

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/video-site/backend/internal/catalog"
	"github.com/video-site/backend/internal/scanner"
)

func TestListEncodesRelativePathsAndStreamURLResolvesFile(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "clips")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	videoPath := filepath.Join(sub, "sample.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write video: %v", err)
	}

	drv := New(Config{ID: "local", RootPath: root})
	if err := drv.Init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	rootEntries, err := drv.List(context.Background(), drv.RootID())
	if err != nil {
		t.Fatalf("list root: %v", err)
	}
	if len(rootEntries) != 1 || !rootEntries[0].IsDir {
		t.Fatalf("root entries = %#v, want one directory", rootEntries)
	}
	if strings.Contains(rootEntries[0].ID, "/") {
		t.Fatalf("encoded dir id contains slash: %q", rootEntries[0].ID)
	}

	fileEntries, err := drv.List(context.Background(), rootEntries[0].ID)
	if err != nil {
		t.Fatalf("list subdir: %v", err)
	}
	if len(fileEntries) != 1 || fileEntries[0].Name != "sample.mp4" {
		t.Fatalf("file entries = %#v, want sample.mp4", fileEntries)
	}
	if strings.Contains(fileEntries[0].ID, "/") {
		t.Fatalf("encoded file id contains slash: %q", fileEntries[0].ID)
	}

	link, err := drv.StreamURL(context.Background(), fileEntries[0].ID)
	if err != nil {
		t.Fatalf("stream url: %v", err)
	}
	if link.URL != videoPath {
		t.Fatalf("url = %q, want %q", link.URL, videoPath)
	}
}

func TestStreamURLResolvesHTTPSTRM(t *testing.T) {
	root := t.TempDir()
	strmPath := filepath.Join(root, "movie.strm")
	target := "https://media.example/clip.mp4?token=abc"
	if err := os.WriteFile(strmPath, []byte("\ufeff\n  "+target+"\n"), 0o644); err != nil {
		t.Fatalf("write strm: %v", err)
	}
	drv := New(Config{ID: "local", RootPath: root})

	link, err := drv.StreamURL(context.Background(), encodeRel("movie.strm"))
	if err != nil {
		t.Fatalf("stream url: %v", err)
	}
	if link.URL != target {
		t.Fatalf("url = %q, want %q", link.URL, target)
	}
}

func TestStreamURLResolvesRelativeLocalSTRM(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "links"), 0o755); err != nil {
		t.Fatalf("mkdir links: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "media"), 0o755); err != nil {
		t.Fatalf("mkdir media: %v", err)
	}
	videoPath := filepath.Join(root, "media", "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "links", "movie.strm"), []byte("../media/clip.mp4\n"), 0o644); err != nil {
		t.Fatalf("write strm: %v", err)
	}
	drv := New(Config{ID: "local", RootPath: root})

	link, err := drv.StreamURL(context.Background(), encodeRel("links/movie.strm"))
	if err != nil {
		t.Fatalf("stream url: %v", err)
	}
	if link.URL != videoPath {
		t.Fatalf("url = %q, want %q", link.URL, videoPath)
	}
}

func TestStreamURLRejectsInvalidSTRMTargets(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string) string
		want  string
	}{
		{
			name: "empty",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				writeLocalStorageTestFile(t, filepath.Join(root, "empty.strm"), []byte("\n  \r\n"))
				return "empty.strm"
			},
			want: "empty strm target",
		},
		{
			name: "escapes root",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				writeLocalStorageTestFile(t, filepath.Join(filepath.Dir(root), "outside.mp4"), []byte("video"))
				writeLocalStorageTestFile(t, filepath.Join(root, "escape.strm"), []byte("../outside.mp4\n"))
				return "escape.strm"
			},
			want: "escapes root",
		},
		{
			name: "nested",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				writeLocalStorageTestFile(t, filepath.Join(root, "nested.strm"), []byte("https://media.example/clip.mp4\n"))
				writeLocalStorageTestFile(t, filepath.Join(root, "outer.strm"), []byte("nested.strm\n"))
				return "outer.strm"
			},
			want: "nested strm target",
		},
		{
			name: "unsupported scheme",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				writeLocalStorageTestFile(t, filepath.Join(root, "ftp.strm"), []byte("ftp://media.example/clip.mp4\n"))
				return "ftp.strm"
			},
			want: "unsupported strm target scheme",
		},
		{
			name: "too large",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				writeLocalStorageTestFile(t, filepath.Join(root, "large.strm"), []byte(strings.Repeat("x", maxSTRMBytes+1)))
				return "large.strm"
			},
			want: "strm file is too large",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			rel := tt.setup(t, root)
			drv := New(Config{ID: "local", RootPath: root})

			_, err := drv.StreamURL(context.Background(), encodeRel(rel))

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want contain %q", err, tt.want)
			}
		})
	}
}

func TestStreamURLRejectsSTRMTargetEscapingRootThroughSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeLocalStorageTestFile(t, filepath.Join(outside, "secret.mp4"), []byte("secret"))
	if err := os.MkdirAll(filepath.Join(root, "links"), 0o755); err != nil {
		t.Fatalf("mkdir links: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "real", "outside")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeLocalStorageTestFile(t, filepath.Join(root, "links", "movie.strm"), []byte("../real/outside/secret.mp4\n"))
	drv := New(Config{ID: "local", RootPath: root})

	_, err := drv.StreamURL(context.Background(), encodeRel("links/movie.strm"))

	if err == nil || !strings.Contains(err.Error(), "strm target escapes root") {
		t.Fatalf("error = %v, want strm target escapes root", err)
	}
}

func TestStreamURLAllowsSTRMTargetOutsideRootWhenEnabled(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "movie.mp4")
	writeLocalStorageTestFile(t, target, []byte("movie-data"))
	writeLocalStorageTestFile(t, filepath.Join(root, "movie.strm"), []byte(target+"\n"))

	// 默认关闭：根目录外的目标仍被拒绝
	strict := New(Config{ID: "local", RootPath: root})
	if _, err := strict.StreamURL(context.Background(), encodeRel("movie.strm")); err == nil || !strings.Contains(err.Error(), "strm target escapes root") {
		t.Fatalf("default error = %v, want strm target escapes root", err)
	}

	// 开启 strm_allow_outside_root 后放行
	relaxed := New(Config{ID: "local", RootPath: root, STRMAllowOutsideRoot: true})
	link, err := relaxed.StreamURL(context.Background(), encodeRel("movie.strm"))
	if err != nil {
		t.Fatalf("StreamURL with allow-outside-root: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("eval target: %v", err)
	}
	if link.URL != resolved {
		t.Fatalf("link url = %q, want %q", link.URL, resolved)
	}
}

func TestStreamURLAllowOutsideRootStillRejectsNestedSTRM(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeLocalStorageTestFile(t, filepath.Join(outside, "inner.strm"), []byte("http://example.com/v.mp4\n"))
	writeLocalStorageTestFile(t, filepath.Join(root, "movie.strm"), []byte(filepath.Join(outside, "inner.strm")+"\n"))

	drv := New(Config{ID: "local", RootPath: root, STRMAllowOutsideRoot: true})
	if _, err := drv.StreamURL(context.Background(), encodeRel("movie.strm")); err == nil || !strings.Contains(err.Error(), "nested strm") {
		t.Fatalf("error = %v, want nested strm rejection", err)
	}
}

func TestStreamURLRejectsSymlinkFileIDEscapingRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeLocalStorageTestFile(t, filepath.Join(outside, "secret.mp4"), []byte("secret"))
	if err := os.Symlink(filepath.Join(outside, "secret.mp4"), filepath.Join(root, "link.mp4")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	drv := New(Config{ID: "local", RootPath: root})

	_, err := drv.StreamURL(context.Background(), encodeRel("link.mp4"))

	if err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("error = %v, want path escapes root", err)
	}
}

func TestStreamURLRejectsEscapingID(t *testing.T) {
	drv := New(Config{ID: "local", RootPath: t.TempDir()})
	escaped := base64.RawURLEncoding.EncodeToString([]byte("../secret.mp4"))

	_, err := drv.StreamURL(context.Background(), escaped)

	if err == nil || !strings.Contains(err.Error(), "invalid relative path") {
		t.Fatalf("error = %v, want invalid relative path", err)
	}
}

func TestInitRequiresExistingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	drv := New(Config{ID: "local", RootPath: missing})

	err := drv.Init(context.Background())

	if err == nil || !strings.Contains(err.Error(), "stat root") {
		t.Fatalf("error = %v, want stat root failure", err)
	}
	if !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "configured=") {
		t.Fatalf("error = %v, want diagnostic path details", err)
	}
}

func TestPathForIDAllowsRootPathSlash(t *testing.T) {
	drv := New(Config{ID: "local", RootPath: string(os.PathSeparator)})
	childID := encodeRel("tmp")

	path, rel, err := drv.pathForID(childID)

	if err != nil {
		t.Fatalf("pathForID: %v", err)
	}
	if rel != "tmp" {
		t.Fatalf("rel = %q, want tmp", rel)
	}
	if path != filepath.Join(string(os.PathSeparator), "tmp") {
		t.Fatalf("path = %q, want /tmp", path)
	}
}

func TestScannerPersistsLocalStorageSTRM(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "collection"), 0o755); err != nil {
		t.Fatalf("mkdir collection: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "collection", "clip.strm"), []byte("https://media.example/clip.mp4\n"), 0o644); err != nil {
		t.Fatalf("write strm: %v", err)
	}
	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	drv := New(Config{ID: "local", RootPath: root})
	sc := scanner.New(cat, drv, []string{".strm"}, nil, nil)
	stats, err := sc.Run(ctx, drv.RootID())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if stats.Added != 1 {
		t.Fatalf("added = %d, want 1", stats.Added)
	}

	fileID := encodeRel("collection/clip.strm")
	got, err := cat.GetVideo(ctx, Kind+"-local-"+fileID)
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if got.Ext != "strm" || got.FileID != fileID || got.ParentID != encodeRel("collection") {
		t.Fatalf("video = %#v, want local strm video under collection", got)
	}
}

func TestScannerPersistsLocalStorageVideo(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "collection"), 0o755); err != nil {
		t.Fatalf("mkdir collection: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "collection", "clip.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	drv := New(Config{ID: "local", RootPath: root})
	sc := scanner.New(cat, drv, []string{".mp4"}, nil, nil)
	stats, err := sc.Run(ctx, drv.RootID())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if stats.Added != 1 {
		t.Fatalf("added = %d, want 1", stats.Added)
	}

	fileID := encodeRel("collection/clip.mp4")
	got, err := cat.GetVideo(ctx, Kind+"-local-"+fileID)
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if got.DriveID != "local" || got.FileID != fileID || got.ParentID != encodeRel("collection") {
		t.Fatalf("video = %#v, want local drive video under collection", got)
	}
}

func writeLocalStorageTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
