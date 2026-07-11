// Package webdav implements a WebDAV-based Drive for the video aggregator.
//
// It connects to any WebDAV server (Nextcloud, Alist, rclone serve webdav, etc.)
// and exposes its file tree through the standard drives.Drive interface.
//
// Playback uses 302 redirect: the backend resolves the WebDAV GET URL,
// follows one redirect hop to capture the real download location, then
// returns that URL to the proxy layer which 302s the browser directly.
// This keeps video traffic off your backend server.
//
// .strm support: when a .strm file is encountered, the driver downloads its
// content from the WebDAV server, parses the first non-empty line as an
// HTTP/HTTPS URL, and returns that URL as the stream link. This lets you
// keep lightweight .strm pointer files on your WebDAV and have them resolve
// to the actual media URLs at playback time.
package webdav

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/video-site/backend/internal/drives"
)

const Kind = "webdav"

// maxSTRMBytes is the maximum size we'll read from a .strm file.
// Matches the localstorage driver's limit.
const maxSTRMBytes = 64 * 1024

// Config holds the parameters needed to connect to a WebDAV server.
type Config struct {
	ID       string
	Address  string // e.g. "https://dav.example.com/remote.php/dav/files/user"
	Username string
	Password string
	RootPath string // sub-path within the WebDAV tree; default "/"
	// TLSInsecureSkipVerify disables certificate validation (self-signed certs).
	TLSInsecureSkipVerify bool
}

// Driver is the WebDAV-backed Drive implementation.
type Driver struct {
	id       string
	address  string
	username string
	password string
	rootPath string

	httpClient     *http.Client
	noRedirectHTTP *http.Client // follows zero redirects, used for 302 resolution
}

// New creates a WebDAV driver from the given config.
func New(c Config) *Driver {
	rootPath := strings.TrimSpace(c.RootPath)
	if rootPath == "" {
		rootPath = "/"
	}
	if !strings.HasPrefix(rootPath, "/") {
		rootPath = "/" + rootPath
	}

	transport := &http.Transport{}
	if c.TLSInsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	noRedirectHTTP := &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // stop on first redirect
		},
	}

	return &Driver{
		id:             c.ID,
		address:        strings.TrimRight(c.Address, "/"),
		username:       c.Username,
		password:       c.Password,
		rootPath:       rootPath,
		httpClient:     httpClient,
		noRedirectHTTP: noRedirectHTTP,
	}
}

func (d *Driver) Kind() string   { return Kind }
func (d *Driver) ID() string     { return d.id }
func (d *Driver) RootID() string { return "/" }

// Init validates that the WebDAV server is reachable and the root path exists.
func (d *Driver) Init(ctx context.Context) error {
	// Issue a PROPFIND depth=0 on the root to verify connectivity + auth.
	reqURL := d.buildURL(d.rootPath)
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", reqURL, nil)
	if err != nil {
		return fmt.Errorf("webdav: build init request: %w", err)
	}
	d.setAuth(req)
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("webdav: init request to %s failed: %w", reqURL, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != 207 && resp.StatusCode != 200 {
		return fmt.Errorf("webdav: init: server returned %d for root %q", resp.StatusCode, d.rootPath)
	}
	return nil
}

// List returns the direct children of the given directory.
// dirID is "/" for the root or a base64-encoded relative path.
func (d *Driver) List(ctx context.Context, dirID string) ([]drives.Entry, error) {
	rel, err := decodeRel(dirID)
	if err != nil {
		return nil, err
	}
	fullPath := joinPath(d.rootPath, rel)
	if !strings.HasSuffix(fullPath, "/") {
		fullPath += "/"
	}

	reqURL := d.buildURL(fullPath)
	body := `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:displayname/>
    <d:getcontentlength/>
    <d:getlastmodified/>
    <d:resourcetype/>
    <d:getcontenttype/>
  </d:prop>
</d:propfind>`

	req, err := http.NewRequestWithContext(ctx, "PROPFIND", reqURL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	d.setAuth(req)
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("webdav: PROPFIND %s: %w", reqURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 207 {
		return nil, fmt.Errorf("webdav: PROPFIND returned %d", resp.StatusCode)
	}

	responses, err := parseMultiStatus(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("webdav: parse multistatus: %w", err)
	}

	// The first response is the directory itself; skip it.
	var entries []drives.Entry
	parentID := idForRel(rel)
	for i, r := range responses {
		if i == 0 {
			continue // skip self
		}
		name := r.Name
		if name == "" {
			continue
		}
		childRel := joinRel(rel, name)
		entries = append(entries, drives.Entry{
			ID:       encodeRel(childRel),
			Name:     name,
			Size:     r.Size,
			IsDir:    r.IsDir,
			ParentID: parentID,
			MimeType: r.ContentType,
			ModTime:  r.ModTime,
		})
	}
	return entries, nil
}

// Stat returns the metadata for a single file/directory.
func (d *Driver) Stat(ctx context.Context, fileID string) (*drives.Entry, error) {
	rel, err := decodeRel(fileID)
	if err != nil {
		return nil, err
	}
	fullPath := joinPath(d.rootPath, rel)

	reqURL := d.buildURL(fullPath)
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", reqURL, nil)
	if err != nil {
		return nil, err
	}
	d.setAuth(req)
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("webdav: not found: %s", fileID)
	}
	if resp.StatusCode != 207 {
		return nil, fmt.Errorf("webdav: PROPFIND returned %d", resp.StatusCode)
	}

	responses, err := parseMultiStatus(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(responses) == 0 {
		return nil, fmt.Errorf("webdav: empty PROPFIND response")
	}

	r := responses[0]
	return &drives.Entry{
		ID:       idForRel(rel),
		Name:     r.Name,
		Size:     r.Size,
		IsDir:    r.IsDir,
		ParentID: idForRel(parentRel(rel)),
		MimeType: r.ContentType,
		ModTime:  r.ModTime,
	}, nil
}

// StreamURL returns the direct-download URL for the file.
//
// For .strm files: downloads the file content from WebDAV, parses the URL
// inside, and returns that URL directly. Only HTTP/HTTPS URLs are accepted;
// local paths and file:// URLs are rejected (they don't make sense for a
// remote WebDAV drive). Nested .strm references are also rejected.
//
// For regular files: attempts to resolve 302/307/308 redirects so the proxy
// layer can send the browser directly to the final CDN URL. This keeps video
// traffic off the backend — identical to OpenList's approach.
func (d *Driver) StreamURL(ctx context.Context, fileID string) (*drives.StreamLink, error) {
	rel, err := decodeRel(fileID)
	if err != nil {
		return nil, err
	}

	// Detect .strm files and handle them specially.
	if isSTRM(rel) {
		return d.streamURLFromSTRM(ctx, rel)
	}

	return d.streamURLDirect(ctx, rel)
}

// streamURLFromSTRM downloads the .strm file from the WebDAV server, reads
// the target URL from its contents, and returns it as the stream link.
//
// .strm files are tiny text files containing a single URL (one per line,
// first non-empty line wins). They're used by media managers (Jellyfin,
// Emby, Kodi, etc.) to point to remote media without storing the actual
// video file locally.
func (d *Driver) streamURLFromSTRM(ctx context.Context, rel string) (*drives.StreamLink, error) {
	target, err := d.readSTRMContent(ctx, rel)
	if err != nil {
		return nil, fmt.Errorf("webdav: read strm %q: %w", rel, err)
	}

	// Parse and validate the target URL.
	u, parseErr := url.Parse(target)

	// Check for HTTP/HTTPS URL — the primary use case for WebDAV .strm files.
	if parseErr == nil {
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			if u.Host == "" {
				return nil, fmt.Errorf("webdav: invalid strm url (no host): %q", target)
			}
			// Reject nested .strm: the target URL must not point to another .strm.
			if isSTRM(u.Path) {
				return nil, errors.New("webdav: nested strm target is not supported")
			}
			return &drives.StreamLink{
				URL:     target,
				Expires: time.Now().Add(24 * time.Hour),
			}, nil

		case "file":
			// file:// URLs don't make sense for a remote WebDAV drive — the
			// backend can't read a file path from the WebDAV server's local FS.
			return nil, fmt.Errorf("webdav: file:// strm targets are not supported for remote WebDAV drives (got %q)", target)

		case "":
			// No scheme — could be a bare path. Fall through to rejection below.

		default:
			// rtsp://, rtmp://, etc. — pass through as-is; the player might handle them.
			if u.Host != "" {
				return &drives.StreamLink{
					URL:     target,
					Expires: time.Now().Add(24 * time.Hour),
				}, nil
			}
			return nil, fmt.Errorf("webdav: unsupported strm target scheme %q", u.Scheme)
		}
	}

	// If we get here, the target is either an unparseable string, a bare
	// relative/absolute path, or something without a scheme. On a remote
	// WebDAV server, local paths don't make sense — we can't resolve them.
	return nil, fmt.Errorf("webdav: strm target must be an HTTP/HTTPS URL for remote WebDAV drives (got %q)", target)
}

// readSTRMContent downloads a .strm file from the WebDAV server and returns
// the first non-empty line (trimmed). This is the target URL/path.
func (d *Driver) readSTRMContent(ctx context.Context, rel string) (string, error) {
	fullPath := joinPath(d.rootPath, rel)
	getURL := d.buildURL(fullPath)

	req, err := http.NewRequestWithContext(ctx, "GET", getURL, nil)
	if err != nil {
		return "", err
	}
	d.setAuth(req)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", getURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("GET returned %d", resp.StatusCode)
	}

	// Read with a size limit to avoid abuse.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSTRMBytes+1))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	if len(data) > maxSTRMBytes {
		return "", errors.New("strm file is too large")
	}

	// Parse: first non-empty line wins (matching localstorage behavior).
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if i == 0 {
			// Strip UTF-8 BOM if present.
			line = strings.TrimPrefix(line, "\ufeff")
		}
		line = strings.TrimSpace(line)
		if line != "" {
			return line, nil
		}
	}
	return "", errors.New("empty strm file")
}

// streamURLDirect resolves the download URL for a regular (non-.strm) file.
func (d *Driver) streamURLDirect(ctx context.Context, rel string) (*drives.StreamLink, error) {
	fullPath := joinPath(d.rootPath, rel)
	getURL := d.buildURL(fullPath)

	// Issue a GET with no-redirect client to capture Location.
	req, err := http.NewRequestWithContext(ctx, "GET", getURL, nil)
	if err != nil {
		return nil, err
	}
	d.setAuth(req)

	resp, err := d.noRedirectHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("webdav: resolve redirect: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	finalURL := getURL
	headers := http.Header{}

	switch resp.StatusCode {
	case 302, 307, 308:
		// Redirect found — use the Location as the direct URL.
		loc := resp.Header.Get("Location")
		if loc == "" {
			return nil, fmt.Errorf("webdav: %d redirect but no Location header", resp.StatusCode)
		}
		// Resolve relative Location URLs against the original request URL.
		if !strings.HasPrefix(loc, "http://") && !strings.HasPrefix(loc, "https://") {
			base, _ := url.Parse(getURL)
			ref, _ := url.Parse(loc)
			loc = base.ResolveReference(ref).String()
		}
		finalURL = loc
		// No auth headers needed — the redirect URL is self-authenticating.

	case 200:
		// No redirect — the WebDAV server serves files directly.
		// We'll need to pass auth headers for the proxy to use.
		if d.username != "" {
			headers.Set("Authorization", req.Header.Get("Authorization"))
		}

	default:
		return nil, fmt.Errorf("webdav: GET returned %d", resp.StatusCode)
	}

	return &drives.StreamLink{
		URL:     finalURL,
		Headers: headers,
		Expires: time.Now().Add(1 * time.Hour),
	}, nil
}

// Upload writes a stream to the WebDAV server via PUT.
func (d *Driver) Upload(ctx context.Context, parentID, name string, r io.Reader, size int64) (string, error) {
	pRel, err := decodeRel(parentID)
	if err != nil {
		return "", err
	}
	childRel := joinRel(pRel, name)
	fullPath := joinPath(d.rootPath, childRel)
	putURL := d.buildURL(fullPath)

	req, err := http.NewRequestWithContext(ctx, "PUT", putURL, r)
	if err != nil {
		return "", err
	}
	d.setAuth(req)
	if size > 0 {
		req.ContentLength = size
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("webdav: PUT %s: %w", putURL, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != 201 && resp.StatusCode != 204 && resp.StatusCode != 200 {
		return "", fmt.Errorf("webdav: PUT returned %d", resp.StatusCode)
	}
	return encodeRel(childRel), nil
}

// EnsureDir creates directories recursively via MKCOL.
func (d *Driver) EnsureDir(ctx context.Context, pathFromRoot string) (string, error) {
	parts := strings.Split(strings.Trim(pathFromRoot, "/"), "/")
	current := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = joinRel(current, part)
		fullPath := joinPath(d.rootPath, current)
		mkcolURL := d.buildURL(fullPath + "/")

		req, err := http.NewRequestWithContext(ctx, "MKCOL", mkcolURL, nil)
		if err != nil {
			return "", err
		}
		d.setAuth(req)

		resp, err := d.httpClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("webdav: MKCOL %s: %w", mkcolURL, err)
		}
		resp.Body.Close()

		// 201 = created, 405 = already exists — both are fine.
		if resp.StatusCode != 201 && resp.StatusCode != 405 {
			return "", fmt.Errorf("webdav: MKCOL returned %d for %s", resp.StatusCode, current)
		}
	}
	return encodeRel(current), nil
}

// Remove deletes a file from the WebDAV server.
// The driver implements drives.Remover so source deletion is available.
func (d *Driver) Remove(ctx context.Context, fileID string) error {
	rel, err := decodeRel(fileID)
	if err != nil {
		return err
	}
	fullPath := joinPath(d.rootPath, rel)
	delURL := d.buildURL(fullPath)

	req, err := http.NewRequestWithContext(ctx, "DELETE", delURL, nil)
	if err != nil {
		return err
	}
	d.setAuth(req)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("webdav: DELETE %s: %w", delURL, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != 204 && resp.StatusCode != 200 && resp.StatusCode != 404 {
		return fmt.Errorf("webdav: DELETE returned %d", resp.StatusCode)
	}
	return nil
}

// Compile-time interface checks.
var _ drives.Drive = (*Driver)(nil)
var _ drives.Remover = (*Driver)(nil)

// ── helpers ─────────────────────────────────────────────────────────────

func (d *Driver) buildURL(davPath string) string {
	// d.address is the base WebDAV URL; davPath is the resource path.
	// If address already contains a path component, we append davPath to it.
	u, err := url.Parse(d.address)
	if err != nil {
		return d.address + davPath
	}
	u.Path = path.Join(u.Path, davPath)
	return u.String()
}

func (d *Driver) setAuth(req *http.Request) {
	if d.username != "" || d.password != "" {
		req.SetBasicAuth(d.username, d.password)
	}
}

// isSTRM returns true if the given path (relative or absolute) ends with .strm
// (case-insensitive).
func isSTRM(p string) bool {
	return strings.EqualFold(path.Ext(p), ".strm")
}

func decodeRel(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || id == "/" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", fmt.Errorf("webdav: invalid file id: %w", err)
	}
	rel := path.Clean(string(raw))
	if rel == "." {
		return "", nil
	}
	if strings.HasPrefix(rel, "../") || rel == ".." || strings.HasPrefix(rel, "/") {
		return "", errors.New("webdav: invalid relative path")
	}
	return rel, nil
}

func encodeRel(rel string) string {
	rel = path.Clean(rel)
	if rel == "." || rel == "" {
		return "/"
	}
	return base64.RawURLEncoding.EncodeToString([]byte(rel))
}

func idForRel(rel string) string {
	if rel == "" {
		return "/"
	}
	return encodeRel(rel)
}

func joinRel(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

func joinPath(root, rel string) string {
	if rel == "" {
		return root
	}
	return strings.TrimRight(root, "/") + "/" + rel
}

func parentRel(rel string) string {
	if rel == "" {
		return ""
	}
	parent := path.Dir(rel)
	if parent == "." {
		return ""
	}
	return parent
}
