package webdav

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEncodeDecodeRel(t *testing.T) {
	cases := []struct{ input, want string }{
		{"", ""},
		{"movies/file.mp4", "movies/file.mp4"},
		{"a/b/c", "a/b/c"},
	}
	for _, tc := range cases {
		id := encodeRel(tc.input)
		got, err := decodeRel(id)
		if err != nil {
			t.Fatalf("decodeRel(%q): %v", id, err)
		}
		if got != tc.want {
			t.Errorf("roundtrip %q: got %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestDecodeRelRoot(t *testing.T) {
	for _, id := range []string{"", "/", "  "} {
		got, err := decodeRel(id)
		if err != nil {
			t.Fatalf("decodeRel(%q): %v", id, err)
		}
		if got != "" {
			t.Errorf("decodeRel(%q) = %q, want empty", id, got)
		}
	}
}

func TestDecodeRelRejectsTraversal(t *testing.T) {
	// Manually encode a traversal path.
	bad := encodeRel("../../etc/passwd")
	_, err := decodeRel(bad)
	if err == nil {
		t.Error("expected error for path traversal, got nil")
	}
}

func TestIsSTRM(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"movie.strm", true},
		{"MOVIE.STRM", true},
		{"dir/sub/file.Strm", true},
		{"movie.mp4", false},
		{"movie.strm.bak", false},
		{"", false},
		{".strm", true},
	}
	for _, tc := range cases {
		got := isSTRM(tc.path)
		if got != tc.want {
			t.Errorf("isSTRM(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestInitSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			w.WriteHeader(207)
			fmt.Fprint(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:href>/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
			return
		}
		w.WriteHeader(405)
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
}

func TestInitFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	if err := d.Init(context.Background()); err == nil {
		t.Fatal("expected Init error for 401, got nil")
	}
}

func TestStreamURLRedirect(t *testing.T) {
	cdnURL := "https://cdn.example.com/video.mp4?token=abc"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			http.Redirect(w, r, cdnURL, http.StatusFound)
			return
		}
		w.WriteHeader(405)
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	link, err := d.StreamURL(context.Background(), encodeRel("video.mp4"))
	if err != nil {
		t.Fatalf("StreamURL: %v", err)
	}
	if link.URL != cdnURL {
		t.Errorf("got URL %q, want %q", link.URL, cdnURL)
	}
	// Redirected URLs should not have auth headers
	if link.Headers.Get("Authorization") != "" {
		t.Error("redirected URL should not carry Authorization header")
	}
}

func TestStreamURLDirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, "fake-video-bytes")
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, Username: "user", Password: "pass", RootPath: "/"})
	link, err := d.StreamURL(context.Background(), encodeRel("video.mp4"))
	if err != nil {
		t.Fatalf("StreamURL: %v", err)
	}
	if !strings.Contains(link.URL, srv.URL) {
		t.Errorf("direct URL should contain server URL, got %q", link.URL)
	}
	if link.Headers.Get("Authorization") == "" {
		t.Error("direct URL should carry Authorization header")
	}
}

// ── .strm tests ─────────────────────────────────────────────────────────

func TestStreamURL_STRM_HTTPS(t *testing.T) {
	// WebDAV server serves .strm files with an HTTPS URL inside.
	targetURL := "https://media.example.com/videos/movie.mp4"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, ".strm") {
			w.WriteHeader(200)
			fmt.Fprint(w, targetURL+"\n")
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	link, err := d.StreamURL(context.Background(), encodeRel("movie.strm"))
	if err != nil {
		t.Fatalf("StreamURL: %v", err)
	}
	if link.URL != targetURL {
		t.Errorf("got URL %q, want %q", link.URL, targetURL)
	}
	// STRM-resolved URLs should not have WebDAV auth headers.
	if link.Headers != nil && link.Headers.Get("Authorization") != "" {
		t.Error("strm-resolved URL should not carry Authorization header")
	}
}

func TestStreamURL_STRM_HTTP(t *testing.T) {
	targetURL := "http://192.168.1.100:8080/stream/video.mkv"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(200)
			fmt.Fprint(w, "  \n"+targetURL+"\n\n")
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	link, err := d.StreamURL(context.Background(), encodeRel("dir/video.strm"))
	if err != nil {
		t.Fatalf("StreamURL: %v", err)
	}
	if link.URL != targetURL {
		t.Errorf("got URL %q, want %q", link.URL, targetURL)
	}
}

func TestStreamURL_STRM_WithBOM(t *testing.T) {
	// Some editors save .strm files with a UTF-8 BOM.
	targetURL := "https://cdn.example.com/video.mp4"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, "\ufeff"+targetURL)
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	link, err := d.StreamURL(context.Background(), encodeRel("bom.strm"))
	if err != nil {
		t.Fatalf("StreamURL: %v", err)
	}
	if link.URL != targetURL {
		t.Errorf("got URL %q, want %q", link.URL, targetURL)
	}
}

func TestStreamURL_STRM_NestedSTRM_Rejected(t *testing.T) {
	// A .strm pointing to another .strm should be rejected.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, "https://example.com/other.strm")
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	_, err := d.StreamURL(context.Background(), encodeRel("chain.strm"))
	if err == nil {
		t.Fatal("expected error for nested .strm, got nil")
	}
	if !strings.Contains(err.Error(), "nested strm") {
		t.Errorf("error = %q, want mention of nested strm", err.Error())
	}
}

func TestStreamURL_STRM_FileURL_Rejected(t *testing.T) {
	// file:// URLs don't make sense for remote WebDAV.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, "file:///mnt/videos/movie.mp4")
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	_, err := d.StreamURL(context.Background(), encodeRel("local.strm"))
	if err == nil {
		t.Fatal("expected error for file:// strm target, got nil")
	}
	if !strings.Contains(err.Error(), "file://") {
		t.Errorf("error = %q, want mention of file://", err.Error())
	}
}

func TestStreamURL_STRM_LocalPath_Rejected(t *testing.T) {
	// Bare local paths don't make sense for remote WebDAV.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, "/mnt/videos/movie.mp4")
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	_, err := d.StreamURL(context.Background(), encodeRel("bare-path.strm"))
	if err == nil {
		t.Fatal("expected error for bare local path, got nil")
	}
	if !strings.Contains(err.Error(), "must be an HTTP/HTTPS URL") {
		t.Errorf("error = %q, want mention of HTTP/HTTPS requirement", err.Error())
	}
}

func TestStreamURL_STRM_RelativePath_Rejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, "../other/movie.mp4")
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	_, err := d.StreamURL(context.Background(), encodeRel("rel-path.strm"))
	if err == nil {
		t.Fatal("expected error for relative path, got nil")
	}
}

func TestStreamURL_STRM_Empty_Rejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, "   \n\n  \n")
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	_, err := d.StreamURL(context.Background(), encodeRel("empty.strm"))
	if err == nil {
		t.Fatal("expected error for empty strm, got nil")
	}
	if !strings.Contains(err.Error(), "empty strm") {
		t.Errorf("error = %q, want mention of empty", err.Error())
	}
}

func TestStreamURL_STRM_TooLarge_Rejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		// Write more than maxSTRMBytes.
		w.Write(make([]byte, maxSTRMBytes+100))
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	_, err := d.StreamURL(context.Background(), encodeRel("huge.strm"))
	if err == nil {
		t.Fatal("expected error for oversized strm, got nil")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %q, want mention of too large", err.Error())
	}
}

func TestStreamURL_STRM_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	_, err := d.StreamURL(context.Background(), encodeRel("error.strm"))
	if err == nil {
		t.Fatal("expected error for 500 strm fetch, got nil")
	}
}

func TestStreamURL_STRM_RTSP_PassThrough(t *testing.T) {
	// RTSP/RTMP URLs should be passed through for the player to handle.
	targetURL := "rtsp://192.168.1.100:554/live/stream"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, targetURL)
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	link, err := d.StreamURL(context.Background(), encodeRel("live.strm"))
	if err != nil {
		t.Fatalf("StreamURL: %v", err)
	}
	if link.URL != targetURL {
		t.Errorf("got URL %q, want %q", link.URL, targetURL)
	}
}

func TestStreamURL_STRM_CaseInsensitive(t *testing.T) {
	// .STRM (uppercase) should also be detected.
	targetURL := "https://cdn.example.com/video.mp4"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, targetURL)
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	link, err := d.StreamURL(context.Background(), encodeRel("MOVIE.STRM"))
	if err != nil {
		t.Fatalf("StreamURL: %v", err)
	}
	if link.URL != targetURL {
		t.Errorf("got URL %q, want %q", link.URL, targetURL)
	}
}

// ── non-.strm tests ─────────────────────────────────────────────────────

func TestListDirectory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			w.WriteHeader(405)
			return
		}
		w.WriteHeader(207)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/root/</d:href>
    <d:propstat>
      <d:prop><d:resourcetype><d:collection/></d:resourcetype><d:displayname>root</d:displayname></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/root/movie.mp4</d:href>
    <d:propstat>
      <d:prop><d:resourcetype/><d:displayname>movie.mp4</d:displayname><d:getcontentlength>1048576</d:getcontentlength></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/root/movie.strm</d:href>
    <d:propstat>
      <d:prop><d:resourcetype/><d:displayname>movie.strm</d:displayname><d:getcontentlength>42</d:getcontentlength></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/root/subdir/</d:href>
    <d:propstat>
      <d:prop><d:resourcetype><d:collection/></d:resourcetype><d:displayname>subdir</d:displayname></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`)
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/root"})
	entries, err := d.List(context.Background(), "/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries (mp4 + strm + subdir), got %d", len(entries))
	}
	if entries[0].Name != "movie.mp4" {
		t.Errorf("first entry name = %q, want movie.mp4", entries[0].Name)
	}
	if entries[0].Size != 1048576 {
		t.Errorf("first entry size = %d, want 1048576", entries[0].Size)
	}
	if entries[0].IsDir {
		t.Error("movie.mp4 should not be dir")
	}
	// .strm file is listed normally
	if entries[1].Name != "movie.strm" {
		t.Errorf("second entry name = %q, want movie.strm", entries[1].Name)
	}
	if entries[1].Size != 42 {
		t.Errorf("second entry size = %d, want 42", entries[1].Size)
	}
	if !entries[2].IsDir {
		t.Error("subdir should be dir")
	}
}

func TestUpload(t *testing.T) {
	var gotPath string
	var gotLen int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			gotPath = r.URL.Path
			gotLen = r.ContentLength
			w.WriteHeader(201)
			return
		}
		w.WriteHeader(405)
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/media"})
	id, err := d.Upload(context.Background(), "/", "test.mp4", strings.NewReader("data"), 4)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if id == "" || id == "/" {
		t.Error("expected non-root file ID")
	}
	if gotLen != 4 {
		t.Errorf("content-length = %d, want 4", gotLen)
	}
	if !strings.HasSuffix(gotPath, "/media/test.mp4") {
		t.Errorf("upload path = %q, want suffix /media/test.mp4", gotPath)
	}
}

func TestRemove(t *testing.T) {
	var deletedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deletedPath = r.URL.Path
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(405)
	}))
	defer srv.Close()

	d := New(Config{ID: "test", Address: srv.URL, RootPath: "/"})
	fileID := encodeRel("old-video.mp4")
	if err := d.Remove(context.Background(), fileID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !strings.HasSuffix(deletedPath, "/old-video.mp4") {
		t.Errorf("delete path = %q, want suffix /old-video.mp4", deletedPath)
	}
}

func TestParseMultiStatus(t *testing.T) {
	xmlBody := `<?xml version="1.0" encoding="UTF-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/root/</d:href>
    <d:propstat>
      <d:prop>
        <d:resourcetype><d:collection/></d:resourcetype>
        <d:displayname>root</d:displayname>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/root/movie.mp4</d:href>
    <d:propstat>
      <d:prop>
        <d:resourcetype/>
        <d:displayname>movie.mp4</d:displayname>
        <d:getcontentlength>1048576</d:getcontentlength>
        <d:getcontenttype>video/mp4</d:getcontenttype>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/root/subdir/</d:href>
    <d:propstat>
      <d:prop>
        <d:resourcetype><d:collection/></d:resourcetype>
        <d:displayname>subdir</d:displayname>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`

	results, err := parseMultiStatus(strings.NewReader(xmlBody))
	if err != nil {
		t.Fatalf("parseMultiStatus: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(results))
	}
	if !results[0].IsDir {
		t.Error("first response should be dir")
	}
	if results[1].Name != "movie.mp4" {
		t.Errorf("second name = %q, want movie.mp4", results[1].Name)
	}
	if results[1].Size != 1048576 {
		t.Errorf("second size = %d, want 1048576", results[1].Size)
	}
	if results[1].IsDir {
		t.Error("movie.mp4 should not be dir")
	}
	if results[1].ContentType != "video/mp4" {
		t.Errorf("content type = %q, want video/mp4", results[1].ContentType)
	}
	if !results[2].IsDir || results[2].Name != "subdir" {
		t.Errorf("third: isDir=%v name=%q", results[2].IsDir, results[2].Name)
	}
}
