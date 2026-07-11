// Package metatube provides a client for querying a self-hosted MetaTube
// backend server for video metadata (title, actors, tags, cover images).
//
// MetaTube API reference: https://github.com/metatube-community/metatube-sdk-go
//
// The typical flow:
//  1. Scanner imports a video file (e.g. "ABP-123.mp4")
//  2. metatube.Client.SearchByCode("ABP-123") hits your MetaTube backend
//  3. The response includes title, actors, genres, cover URL, etc.
//  4. The caller (scanner/nightly) updates the catalog with the scraped metadata.
package metatube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

// Config for the MetaTube client.
type Config struct {
	// ServerURL is the base URL of your MetaTube backend (e.g. "http://127.0.0.1:8080").
	ServerURL string
	// Token is an optional auth token sent as "Authorization: Bearer ..." header.
	Token string
}

// Client talks to a MetaTube backend server.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// New creates a MetaTube client.
func New(cfg Config) *Client {
	return &Client{
		baseURL: strings.TrimRight(cfg.ServerURL, "/"),
		token:   cfg.Token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// VideoMetadata holds the scraped metadata for a video.
type VideoMetadata struct {
	// Provider is the source site (e.g. "javbus", "fanza", etc.)
	Provider string `json:"provider"`
	// ID is the provider-specific ID
	ID string `json:"id"`
	// Code is the video code (e.g. "ABP-123")
	Code string `json:"number"`
	// Title is the original title
	Title string `json:"title"`
	// Summary / plot overview
	Summary string `json:"summary"`
	// Actors / performers
	Actors []Actor `json:"actors"`
	// Genres / tags
	Genres []string `json:"genres"`
	// CoverURL is the primary cover image URL (proxied through MetaTube backend)
	CoverURL string `json:"cover_url"`
	// ThumbURL is a thumbnail image URL
	ThumbURL string `json:"thumb_url"`
	// ReleaseDate
	ReleaseDate string `json:"release_date"`
	// Director
	Director string `json:"director"`
	// Studio / maker
	Studio string `json:"maker"`
	// Label / publisher
	Label string `json:"label"`
	// Series
	Series string `json:"series"`
	// Runtime in minutes
	Runtime int `json:"runtime"`
	// Score / rating (0-10)
	Score float64 `json:"score"`
}

// Actor represents a performer.
type Actor struct {
	Name     string `json:"name"`
	ImageURL string `json:"image_url,omitempty"`
}

// SearchResult is one item from a search response.
type SearchResult struct {
	Provider string  `json:"provider"`
	ID       string  `json:"id"`
	Code     string  `json:"number"`
	Title    string  `json:"title"`
	CoverURL string  `json:"cover_url"`
	ThumbURL string  `json:"thumb_url"`
	Score    float64 `json:"score"`
}

// SearchByCode searches your MetaTube backend for metadata matching a video code.
// The code is typically extracted from the filename (e.g. "ABP-123" from "ABP-123.mp4").
func (c *Client) SearchByCode(ctx context.Context, code string) ([]SearchResult, error) {
	if code == "" {
		return nil, nil
	}
	endpoint := fmt.Sprintf("%s/api/v1/movies/search?q=%s&provider=", c.baseURL, url.QueryEscape(code))
	body, err := c.doGet(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("metatube search %q: %w", code, err)
	}

	var resp struct {
		Data []SearchResult `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("metatube search: parse response: %w", err)
	}
	return resp.Data, nil
}

// GetMovieDetail fetches full metadata from a specific provider + ID.
func (c *Client) GetMovieDetail(ctx context.Context, provider, id string) (*VideoMetadata, error) {
	endpoint := fmt.Sprintf("%s/api/v1/movies/%s/%s", c.baseURL, url.PathEscape(provider), url.PathEscape(id))
	body, err := c.doGet(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("metatube detail %s/%s: %w", provider, id, err)
	}

	var resp struct {
		Data VideoMetadata `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("metatube detail: parse response: %w", err)
	}
	return &resp.Data, nil
}

// GetPrimaryImageURL returns the MetaTube proxied primary image URL for a movie.
// This avoids CORS / hotlinking issues by going through the MetaTube backend.
func (c *Client) GetPrimaryImageURL(provider, id string) string {
	u := fmt.Sprintf("%s/api/v1/images/primary/%s/%s", c.baseURL, url.PathEscape(provider), url.PathEscape(id))
	if c.token != "" {
		u += "?token=" + url.QueryEscape(c.token)
	}
	return u
}

// GetThumbImageURL returns the MetaTube proxied thumb image URL.
func (c *Client) GetThumbImageURL(provider, id string) string {
	u := fmt.Sprintf("%s/api/v1/images/thumb/%s/%s", c.baseURL, url.PathEscape(provider), url.PathEscape(id))
	if c.token != "" {
		u += "?token=" + url.QueryEscape(c.token)
	}
	return u
}

// DownloadImage fetches an image from MetaTube backend and returns the bytes.
func (c *Client) DownloadImage(ctx context.Context, imageURL string) ([]byte, string, error) {
	body, err := c.doGet(ctx, imageURL)
	if err != nil {
		return nil, "", err
	}
	// Detect content type from first bytes.
	ct := http.DetectContentType(body)
	return body, ct, nil
}

// codePatterns are the regexes used to extract a video code from a filename,
// tried in order from most specific to most general.
var codePatterns = []*regexp.Regexp{
	// FC2-PPV pattern: FC2-PPV-1234567
	regexp.MustCompile(`(?i)\b(fc2-ppv-\d+)\b`),
	// Standard: ABC-123, ABCD-1234
	regexp.MustCompile(`(?i)\b([a-z]{2,6}-\d{2,8})\b`),
	// Uncensored pattern: 123456-789
	regexp.MustCompile(`(?i)\b(\d{4,6}-\d{2,4})\b`),
}

// ExtractCode attempts to parse a video code from a filename.
// Common patterns: "ABP-123.mp4", "[ABP-123] title.mp4", "abp-123.mp4"
func ExtractCode(filename string) string {
	// Remove extension.
	name := strings.TrimSuffix(filename, path.Ext(filename))

	for _, p := range codePatterns {
		if m := p.FindStringSubmatch(name); len(m) > 1 {
			return strings.ToUpper(m[1])
		}
	}
	return ""
}

// ScrapeByFilename is a convenience method: extract code → search → get best detail.
func (c *Client) ScrapeByFilename(ctx context.Context, filename string) (*VideoMetadata, error) {
	code := ExtractCode(filename)
	if code == "" {
		return nil, nil // not a recognizable code
	}

	results, err := c.SearchByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}

	// Use the first (best) result.
	best := results[0]
	return c.GetMovieDetail(ctx, best.Provider, best.ID)
}

func (c *Client) doGet(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, preview)
	}
	return body, nil
}
