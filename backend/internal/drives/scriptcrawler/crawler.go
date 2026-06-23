package scriptcrawler

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/video-site/backend/internal/catalog"
	"github.com/video-site/backend/internal/fingerprint"
	"github.com/video-site/backend/internal/mediaasset"
	"golang.org/x/net/proxy"
)

const (
	DefaultTargetNew           = 10
	defaultUserAgent           = "Mozilla/5.0 (compatible; video-site-91-scriptcrawler/1.0)"
	defaultCandidateMultiplier = 10
	defaultCandidateFloorExtra = 50
	defaultCandidateBudgetMax  = 500
)

type CrawlerConfig struct {
	Driver          *Driver
	Catalog         *catalog.Catalog
	CrawlerName     string
	PythonPath      string
	FFmpegPath      string
	FFprobePath     string
	ScriptPath      string
	WorkDir         string
	CommonThumbDir  string
	ProxyURL        string
	ConfigJSON      string
	DisablePreview  bool
	HTTPClient      *http.Client
	DownloadTimeout time.Duration
	OnProgress      func(CrawlProgress)
}

type Crawler struct {
	cfg   CrawlerConfig
	runMu sync.Mutex
}

func NewCrawler(cfg CrawlerConfig) *Crawler {
	if strings.TrimSpace(cfg.PythonPath) == "" {
		cfg.PythonPath = "python3"
	}
	if strings.TrimSpace(cfg.FFmpegPath) == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if strings.TrimSpace(cfg.FFprobePath) == "" {
		cfg.FFprobePath = "ffprobe"
	}
	if cfg.DownloadTimeout <= 0 {
		cfg.DownloadTimeout = 30 * time.Minute
	}
	if cfg.HTTPClient == nil {
		transport := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 60 * time.Second,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
		}
		if err := configureExplicitProxy(transport, cfg.ProxyURL); err != nil {
			log.Printf("[scriptcrawler] invalid configured proxy URL, falling back to env: %v", err)
		}
		cfg.HTTPClient = &http.Client{Transport: transport}
	}
	return &Crawler{cfg: cfg}
}

type CrawlResult struct {
	TargetNew       int
	CandidateBudget int
	TotalEntries    int
	NewVideos       int
	Skipped         int
	Failed          int
	SeenSnapshot    int
	StartedAt       time.Time
	FinishedAt      time.Time
	JobFile         string
	SeenFile        string
}

type CrawlProgress struct {
	TargetNew    int
	TotalEntries int
	NewVideos    int
	Skipped      int
	Failed       int
	SeenSnapshot int
	Checked      int
	Emitted      int
	Message      string
}

type Job struct {
	Protocol          string          `json:"protocol"`
	Mode              string          `json:"mode"`
	RunID             string          `json:"run_id"`
	CrawlerID         string          `json:"crawler_id"`
	TargetNew         int             `json:"target_new"`
	UniqueTarget      int             `json:"unique_target,omitempty"`
	CandidateBudget   int             `json:"candidate_budget,omitempty"`
	SeenSourceIDsFile string          `json:"seen_source_ids_file"`
	OutputDir         string          `json:"output_dir"`
	Config            json.RawMessage `json:"config"`
	Network           JobNetwork      `json:"network"`
}

type JobNetwork struct {
	ProxyURL string `json:"proxy_url,omitempty"`
}

type Event struct {
	Type               string            `json:"type"`
	Item               Item              `json:"item"`
	SourceID           string            `json:"source_id,omitempty"`
	Title              string            `json:"title,omitempty"`
	MediaURL           string            `json:"media_url,omitempty"`
	MediaLocalFile     string            `json:"media_local_file,omitempty"`
	ThumbnailURL       string            `json:"thumbnail_url,omitempty"`
	ThumbnailLocalFile string            `json:"thumbnail_local_file,omitempty"`
	DetailURL          string            `json:"detail_url,omitempty"`
	Author             string            `json:"author,omitempty"`
	Tags               []string          `json:"tags,omitempty"`
	Quality            string            `json:"quality,omitempty"`
	DurationSeconds    int               `json:"duration_seconds,omitempty"`
	Description        string            `json:"description,omitempty"`
	PublishedAt        string            `json:"published_at,omitempty"`
	Headers            map[string]string `json:"headers,omitempty"`
	MediaHeaders       map[string]string `json:"media_headers,omitempty"`
	ThumbnailHeaders   map[string]string `json:"thumbnail_headers,omitempty"`
	Checked            int               `json:"checked,omitempty"`
	Emitted            int               `json:"emitted,omitempty"`
	Message            string            `json:"message,omitempty"`
	Stats              json.RawMessage   `json:"stats,omitempty"`
}

type Item struct {
	SourceID           string            `json:"source_id,omitempty"`
	Title              string            `json:"title"`
	MediaURL           string            `json:"media_url,omitempty"`
	MediaLocalFile     string            `json:"media_local_file,omitempty"`
	ThumbnailURL       string            `json:"thumbnail_url,omitempty"`
	ThumbnailLocalFile string            `json:"thumbnail_local_file,omitempty"`
	DetailURL          string            `json:"detail_url,omitempty"`
	Author             string            `json:"author,omitempty"`
	Tags               []string          `json:"tags,omitempty"`
	Quality            string            `json:"quality,omitempty"`
	DurationSeconds    int               `json:"duration_seconds,omitempty"`
	Description        string            `json:"description,omitempty"`
	PublishedAt        string            `json:"published_at,omitempty"`
	Headers            map[string]string `json:"headers,omitempty"`
	MediaHeaders       map[string]string `json:"media_headers,omitempty"`
	ThumbnailHeaders   map[string]string `json:"thumbnail_headers,omitempty"`
	Media              MediaRef          `json:"media,omitempty"`
	Thumbnail          MediaRef          `json:"thumbnail,omitempty"`
}

type MediaRef struct {
	URL       string            `json:"url,omitempty"`
	LocalFile string            `json:"local_file,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

func (e Event) normalizedItem() Item {
	item := e.Item
	if strings.TrimSpace(item.SourceID) == "" {
		item.SourceID = e.SourceID
	}
	if strings.TrimSpace(item.Title) == "" {
		item.Title = e.Title
	}
	if strings.TrimSpace(item.MediaURL) == "" {
		item.MediaURL = e.MediaURL
	}
	if strings.TrimSpace(item.MediaLocalFile) == "" {
		item.MediaLocalFile = e.MediaLocalFile
	}
	if strings.TrimSpace(item.ThumbnailURL) == "" {
		item.ThumbnailURL = e.ThumbnailURL
	}
	if strings.TrimSpace(item.ThumbnailLocalFile) == "" {
		item.ThumbnailLocalFile = e.ThumbnailLocalFile
	}
	if strings.TrimSpace(item.DetailURL) == "" {
		item.DetailURL = e.DetailURL
	}
	if strings.TrimSpace(item.Author) == "" {
		item.Author = e.Author
	}
	if len(item.Tags) == 0 && len(e.Tags) > 0 {
		item.Tags = e.Tags
	}
	if strings.TrimSpace(item.Quality) == "" {
		item.Quality = e.Quality
	}
	if item.DurationSeconds == 0 {
		item.DurationSeconds = e.DurationSeconds
	}
	if strings.TrimSpace(item.Description) == "" {
		item.Description = e.Description
	}
	if strings.TrimSpace(item.PublishedAt) == "" {
		item.PublishedAt = e.PublishedAt
	}
	if len(item.Headers) == 0 && len(e.Headers) > 0 {
		item.Headers = e.Headers
	}
	if len(item.MediaHeaders) == 0 && len(e.MediaHeaders) > 0 {
		item.MediaHeaders = e.MediaHeaders
	}
	if len(item.ThumbnailHeaders) == 0 && len(e.ThumbnailHeaders) > 0 {
		item.ThumbnailHeaders = e.ThumbnailHeaders
	}
	return item
}

func (item Item) hasPayload() bool {
	return strings.TrimSpace(item.Title) != "" ||
		strings.TrimSpace(item.SourceID) != "" ||
		strings.TrimSpace(item.MediaURL) != "" ||
		strings.TrimSpace(item.MediaLocalFile) != "" ||
		strings.TrimSpace(item.Media.URL) != "" ||
		strings.TrimSpace(item.Media.LocalFile) != ""
}

func (c *Crawler) RunOnce(ctx context.Context, targetNew int) (*CrawlResult, error) {
	c.runMu.Lock()
	defer c.runMu.Unlock()

	if c.cfg.Driver == nil {
		return nil, errors.New("scriptcrawler: driver not set")
	}
	if c.cfg.Catalog == nil {
		return nil, errors.New("scriptcrawler: catalog not set")
	}
	if strings.TrimSpace(c.cfg.ScriptPath) == "" {
		return nil, errors.New("scriptcrawler: script_path is required")
	}
	if _, err := os.Stat(c.cfg.ScriptPath); err != nil {
		return nil, fmt.Errorf("scriptcrawler: script not found: %w", err)
	}
	if targetNew <= 0 {
		targetNew = DefaultTargetNew
	}
	candidateBudget := candidateBudgetForTarget(targetNew)
	if err := c.cfg.Driver.Init(ctx); err != nil {
		return nil, fmt.Errorf("scriptcrawler: driver init: %w", err)
	}

	result := &CrawlResult{TargetNew: targetNew, CandidateBudget: candidateBudget, StartedAt: time.Now()}
	defer func() { result.FinishedAt = time.Now() }()
	emit := func(p CrawlProgress) {
		if c.cfg.OnProgress == nil {
			return
		}
		p.TargetNew = result.TargetNew
		p.TotalEntries = result.TotalEntries
		p.NewVideos = result.NewVideos
		p.Skipped = result.Skipped
		p.Failed = result.Failed
		p.SeenSnapshot = result.SeenSnapshot
		c.cfg.OnProgress(p)
	}
	emit(CrawlProgress{})

	crawlDir, err := filepath.Abs(c.cfg.Driver.CrawlDir())
	if err != nil {
		return result, fmt.Errorf("scriptcrawler: resolve crawl dir: %w", err)
	}
	if err := os.MkdirAll(crawlDir, 0o755); err != nil {
		return result, err
	}
	runID := time.Now().UTC().Format("20060102T150405Z")
	seenPath := filepath.Join(crawlDir, "seen-"+runID+".txt")
	jobPath := filepath.Join(crawlDir, "job-"+runID+".json")
	result.SeenFile = seenPath
	result.JobFile = jobPath

	seenCount, err := c.writeSeenSourceIDs(ctx, seenPath)
	if err != nil {
		return result, fmt.Errorf("scriptcrawler: build seen list: %w", err)
	}
	result.SeenSnapshot = seenCount
	emit(CrawlProgress{})

	if err := c.writeJobFile(jobPath, runID, targetNew, candidateBudget, seenPath); err != nil {
		return result, fmt.Errorf("scriptcrawler: write job: %w", err)
	}

	cmd, stdout, err := c.startScript(ctx, jobPath, targetNew, candidateBudget)
	if err != nil {
		return result, fmt.Errorf("scriptcrawler: start: %w", err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	progress := CrawlProgress{}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			_ = cmd.Process.Kill()
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			log.Printf("[scriptcrawler] drive=%s stdout parse: %v line=%q", c.cfg.Driver.ID(), err, line)
			continue
		}
		eventType := strings.ToLower(strings.TrimSpace(event.Type))
		item := event.normalizedItem()
		if eventType == "" && item.hasPayload() {
			eventType = "item"
		}
		switch eventType {
		case "item":
			result.TotalEntries++
			progress.Emitted++
			emit(progress)
			if result.NewVideos >= targetNew {
				_ = cmd.Process.Kill()
				break
			}
			added, err := c.processItem(ctx, item)
			if err != nil {
				log.Printf("[scriptcrawler] drive=%s item failed source_id=%q title=%q: %v", c.cfg.Driver.ID(), item.SourceID, item.Title, err)
				result.Failed++
			} else if added {
				result.NewVideos++
			} else {
				result.Skipped++
			}
			emit(progress)
			if result.NewVideos >= targetNew {
				_ = cmd.Process.Kill()
				break
			}
		case "progress":
			if event.Checked > 0 {
				progress.Checked = event.Checked
			}
			if event.Emitted > 0 {
				progress.Emitted = event.Emitted
			}
			progress.Message = event.Message
			emit(progress)
		case "done":
			progress.Message = event.Message
			emit(progress)
		case "":
			log.Printf("[scriptcrawler] drive=%s missing event type line=%q", c.cfg.Driver.ID(), line)
		default:
			log.Printf("[scriptcrawler] drive=%s unknown event type=%q", c.cfg.Driver.ID(), event.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("[scriptcrawler] drive=%s stdout scan: %v", c.cfg.Driver.ID(), err)
	}
	if err := cmd.Wait(); err != nil && ctx.Err() == nil && result.NewVideos < targetNew {
		log.Printf("[scriptcrawler] drive=%s script exit: %v", c.cfg.Driver.ID(), err)
	}
	return result, nil
}

func (c *Crawler) writeSeenSourceIDs(ctx context.Context, path string) (int, error) {
	seenIDs, err := c.cfg.Catalog.ListCrawlerSourceIDs(ctx, Kind, c.cfg.Driver.ID())
	if err != nil {
		return 0, err
	}
	seen := make(map[string]struct{}, len(seenIDs))
	for _, id := range seenIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			seen[id] = struct{}{}
		}
	}
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	for id := range seen {
		if _, err := f.WriteString(id + "\n"); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return 0, err
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return len(seen), nil
}

func (c *Crawler) writeJobFile(path, runID string, targetNew, candidateBudget int, seenPath string) error {
	cfg := json.RawMessage([]byte("{}"))
	if raw := strings.TrimSpace(c.cfg.ConfigJSON); raw != "" {
		if !json.Valid([]byte(raw)) {
			return errors.New("config_json must be valid JSON")
		}
		cfg = json.RawMessage(raw)
	}
	outputDir, err := filepath.Abs(c.cfg.Driver.OutputDir())
	if err != nil {
		return fmt.Errorf("resolve output dir: %w", err)
	}
	job := Job{
		Protocol:          "crawler.v1",
		Mode:              "crawl",
		RunID:             runID,
		CrawlerID:         c.cfg.Driver.ID(),
		TargetNew:         candidateBudget,
		UniqueTarget:      targetNew,
		CandidateBudget:   candidateBudget,
		SeenSourceIDsFile: seenPath,
		OutputDir:         outputDir,
		Config:            cfg,
		Network:           JobNetwork{ProxyURL: strings.TrimSpace(c.cfg.ProxyURL)},
	}
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Crawler) startScript(ctx context.Context, jobPath string, targetNew, candidateBudget int) (*exec.Cmd, io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, c.cfg.PythonPath, c.cfg.ScriptPath, "--job", jobPath)
	if strings.TrimSpace(c.cfg.WorkDir) != "" {
		cmd.Dir = c.cfg.WorkDir
	}
	if proxyURL := strings.TrimSpace(c.cfg.ProxyURL); proxyURL != "" {
		cmd.Env = append(os.Environ(),
			"HTTP_PROXY="+proxyURL,
			"HTTPS_PROXY="+proxyURL,
			"http_proxy="+proxyURL,
			"https_proxy="+proxyURL,
			"NO_PROXY=",
			"no_proxy=",
		)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, nil, err
	}
	log.Printf("[scriptcrawler] drive=%s exec %s --job=%s unique_target=%d candidate_budget=%d", c.cfg.Driver.ID(), c.cfg.ScriptPath, jobPath, targetNew, candidateBudget)
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, nil, err
	}
	go forwardScriptLog(c.cfg.Driver.ID(), stderr)
	return cmd, stdout, nil
}

func forwardScriptLog(driveID string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		log.Printf("[scriptcrawler:script] drive=%s %s", driveID, line)
	}
}

func (c *Crawler) processItem(ctx context.Context, item Item) (bool, error) {
	item, sourceID, err := normalizeItemForImport(item)
	if err != nil {
		return false, err
	}
	videoID := BuildVideoID(c.cfg.Driver.ID(), sourceID)
	if deleted, err := c.cfg.Catalog.IsVideoDeleted(ctx, videoID); err != nil {
		return false, err
	} else if deleted {
		return false, nil
	}
	if existing, _ := c.cfg.Catalog.GetVideo(ctx, videoID); existing != nil {
		return false, nil
	}
	videoExt := detectVideoExt(item.Media.URL, item.Media.LocalFile)
	videoFile := sourceID + videoExt
	videoPath, err := c.cfg.Driver.VideoPath(videoFile)
	if err != nil {
		return false, err
	}
	size, err := c.materializeMedia(ctx, item.Media, videoPath, item.DetailURL, true)
	if err != nil {
		return false, fmt.Errorf("video: %w", err)
	}
	if err := c.validateDownloadedVideo(ctx, videoPath); err != nil {
		_ = os.Remove(videoPath)
		return false, fmt.Errorf("video invalid: %w", err)
	}

	now := time.Now()
	title := strings.TrimSpace(item.Title)
	if title == "" {
		title = sourceID
	}
	author := strings.TrimSpace(item.Author)
	if author == "" {
		author = c.cfg.Driver.ID()
	}
	tags := cleanStringList(item.Tags)
	if matched, err := c.cfg.Catalog.MatchTags(ctx, title+" "+author+" "+strings.Join(tags, " ")); err == nil {
		tags = mergeStringLists(tags, matched)
	}
	if crawlerTag := c.crawlerTagName(); crawlerTag != "" {
		tags = mergeStringLists(tags, []string{crawlerTag})
	}
	publishedAt := now
	if parsed := parsePublishedAt(item.PublishedAt); !parsed.IsZero() {
		publishedAt = parsed
	}
	quality := strings.TrimSpace(item.Quality)
	if quality == "" {
		quality = "HD"
	}
	previewStatus := "pending"
	if c.previewDisabled(ctx) {
		previewStatus = "disabled"
	}
	v := &catalog.Video{
		ID:              videoID,
		DriveID:         c.cfg.Driver.ID(),
		FileID:          videoFile,
		FileName:        videoFile,
		Title:           title,
		Author:          author,
		Tags:            tags,
		DurationSeconds: item.DurationSeconds,
		Size:            size,
		Ext:             strings.TrimPrefix(videoExt, "."),
		Quality:         quality,
		Description:     strings.TrimSpace(item.Description),
		PreviewStatus:   previewStatus,
		PublishedAt:     publishedAt,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	sampled, err := fingerprint.Compute(ctx, c.cfg.Driver, v, fingerprint.Config{}, c.cfg.HTTPClient)
	if err != nil {
		_ = os.Remove(videoPath)
		return false, fmt.Errorf("fingerprint: %w", err)
	}
	v.SampledSHA256 = sampled
	v.FingerprintStatus = "ready"
	if duplicate, err := c.cfg.Catalog.FindVideoBySampledFingerprint(ctx, v); err == nil && duplicate != nil {
		_ = os.Remove(videoPath)
		if markErr := c.cfg.Catalog.MarkCrawlerSourceSeen(ctx, Kind, c.cfg.Driver.ID(), sourceID, "duplicate", duplicate.ID, sampled, size); markErr != nil {
			log.Printf("[scriptcrawler] drive=%s source_id=%s mark duplicate seen: %v", c.cfg.Driver.ID(), sourceID, markErr)
		}
		log.Printf("[scriptcrawler] drive=%s source_id=%s duplicate_of=%s title=%q size=%d", c.cfg.Driver.ID(), sourceID, duplicate.ID, title, size)
		return false, nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		_ = os.Remove(videoPath)
		return false, fmt.Errorf("duplicate lookup: %w", err)
	}

	thumbReady := false
	thumbPath := ""
	commonThumbPath := ""
	if item.Thumbnail.URL != "" || item.Thumbnail.LocalFile != "" {
		thumbFile := sourceID + detectThumbExt(item.Thumbnail.URL, item.Thumbnail.LocalFile)
		thumbPath, err = c.cfg.Driver.ThumbPath(thumbFile)
		if err == nil {
			if _, err := c.materializeMedia(ctx, item.Thumbnail, thumbPath, item.DetailURL, false); err != nil {
				log.Printf("[scriptcrawler] drive=%s source_id=%s thumbnail failed: %v", c.cfg.Driver.ID(), sourceID, err)
			} else if c.cfg.CommonThumbDir != "" {
				if err := os.MkdirAll(c.cfg.CommonThumbDir, 0o755); err != nil {
					log.Printf("[scriptcrawler] drive=%s common thumbs mkdir: %v", c.cfg.Driver.ID(), err)
				} else {
					dst := mediaasset.ThumbnailPathInDir(c.cfg.CommonThumbDir, videoID)
					if err := copyFileAtomic(thumbPath, dst); err != nil {
						log.Printf("[scriptcrawler] drive=%s source_id=%s copy thumbnail: %v", c.cfg.Driver.ID(), sourceID, err)
					} else {
						commonThumbPath = dst
						thumbReady = true
					}
				}
			}
		}
	}
	if thumbReady {
		v.ThumbnailURL = "/p/thumb/" + v.ID
	}
	if duplicate, err := c.findNearDuplicateVideo(ctx, v, commonThumbPath); err != nil {
		_ = os.Remove(videoPath)
		if thumbPath != "" {
			_ = os.Remove(thumbPath)
		}
		if commonThumbPath != "" {
			_ = os.Remove(commonThumbPath)
		}
		return false, fmt.Errorf("near duplicate lookup: %w", err)
	} else if duplicate != nil && duplicate.video != nil {
		if v.Size > duplicate.video.Size {
			if err := c.cfg.Catalog.DeleteVideoWithTombstoneReason(ctx, duplicate.video.ID, catalog.DeletedVideoReasonDuplicate); err != nil {
				_ = os.Remove(videoPath)
				if thumbPath != "" {
					_ = os.Remove(thumbPath)
				}
				if commonThumbPath != "" {
					_ = os.Remove(commonThumbPath)
				}
				return false, fmt.Errorf("delete smaller near duplicate %s: %w", duplicate.video.ID, err)
			}
			log.Printf("[scriptcrawler] drive=%s source_id=%s replacing_smaller_near_duplicate=%s old_size=%d new_size=%d title_similarity=%.3f thumbnail_ssim=%.3f title=%q duration=%d", c.cfg.Driver.ID(), sourceID, duplicate.video.ID, duplicate.video.Size, v.Size, duplicate.titleSimilarity, duplicate.thumbnailSSIM, title, v.DurationSeconds)
		} else {
			_ = os.Remove(videoPath)
			if thumbPath != "" {
				_ = os.Remove(thumbPath)
			}
			if commonThumbPath != "" {
				_ = os.Remove(commonThumbPath)
			}
			if markErr := c.cfg.Catalog.MarkCrawlerSourceSeen(ctx, Kind, c.cfg.Driver.ID(), sourceID, "duplicate", duplicate.video.ID, sampled, size); markErr != nil {
				log.Printf("[scriptcrawler] drive=%s source_id=%s mark near duplicate seen: %v", c.cfg.Driver.ID(), sourceID, markErr)
			}
			log.Printf("[scriptcrawler] drive=%s source_id=%s near_duplicate_of=%s old_size=%d new_size=%d title_similarity=%.3f thumbnail_ssim=%.3f title=%q duration=%d", c.cfg.Driver.ID(), sourceID, duplicate.video.ID, duplicate.video.Size, v.Size, duplicate.titleSimilarity, duplicate.thumbnailSSIM, title, v.DurationSeconds)
			return false, nil
		}
	}
	if err := c.cfg.Catalog.UpsertVideo(ctx, v); err != nil {
		_ = os.Remove(videoPath)
		return false, err
	}
	if err := c.cfg.Catalog.MarkCrawlerSourceSeen(ctx, Kind, c.cfg.Driver.ID(), sourceID, "imported", v.ID, sampled, size); err != nil {
		log.Printf("[scriptcrawler] drive=%s source_id=%s mark imported seen: %v", c.cfg.Driver.ID(), sourceID, err)
	}
	log.Printf("[scriptcrawler] drive=%s source_id=%s ok title=%q size=%d", c.cfg.Driver.ID(), sourceID, title, size)
	return true, nil
}

func (c *Crawler) previewDisabled(ctx context.Context) bool {
	if c == nil {
		return false
	}
	if c.cfg.Catalog != nil && c.cfg.Driver != nil {
		if d, err := c.cfg.Catalog.GetDrive(ctx, c.cfg.Driver.ID()); err == nil && d != nil {
			return !d.TeaserEnabled
		}
	}
	return c.cfg.DisablePreview
}

func (c *Crawler) materializeMedia(ctx context.Context, ref MediaRef, dst, referer string, required bool) (int64, error) {
	if local := strings.TrimSpace(ref.LocalFile); local != "" {
		return c.copyLocalOutput(local, dst)
	}
	if rawURL := strings.TrimSpace(ref.URL); rawURL != "" {
		attemptCtx, cancel := c.downloadAttemptContext(ctx)
		defer cancel()
		return c.downloadAtomic(attemptCtx, ref, dst, referer)
	}
	if required {
		return 0, errors.New("missing url or local_file")
	}
	return 0, nil
}

func (c *Crawler) validateDownloadedVideo(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_type",
		"-of", "csv=p=0",
		path,
	}
	out, err := exec.CommandContext(ctx, c.cfg.FFprobePath, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("ffprobe: %s", msg)
	}
	if !strings.Contains(strings.ToLower(string(out)), "video") {
		return errors.New("ffprobe: no video stream")
	}
	return nil
}

func (c *Crawler) copyLocalOutput(src, dst string) (int64, error) {
	outputRoot, err := filepath.Abs(c.cfg.Driver.OutputDir())
	if err != nil {
		return 0, err
	}
	srcAbs, err := filepath.Abs(src)
	if err != nil {
		return 0, err
	}
	if srcAbs != outputRoot && !strings.HasPrefix(srcAbs, outputRoot+string(os.PathSeparator)) {
		return 0, errors.New("local_file must be inside job output_dir")
	}
	info, err := os.Stat(srcAbs)
	if err != nil {
		return 0, err
	}
	if info.IsDir() || info.Size() == 0 {
		return 0, errors.New("local_file is empty or directory")
	}
	return info.Size(), copyFileAtomic(srcAbs, dst)
}

func (c *Crawler) downloadAttemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.cfg.DownloadTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.cfg.DownloadTimeout)
}

func (c *Crawler) downloadAtomic(ctx context.Context, ref MediaRef, dst, referer string) (int64, error) {
	src := strings.TrimSpace(ref.URL)
	if src == "" {
		return 0, errors.New("empty url")
	}
	if _, err := url.Parse(src); err != nil {
		return 0, fmt.Errorf("parse url: %w", err)
	}
	if looksLikeHLSURL(src) {
		return c.downloadHLSAtomic(ctx, ref, dst, referer)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	for k, v := range ref.Headers {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("http %d", resp.StatusCode)
	}
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(out, resp.Body)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return 0, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return 0, closeErr
	}
	if written <= 0 {
		_ = os.Remove(tmp)
		return 0, errors.New("empty body")
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return written, nil
}

func (c *Crawler) downloadHLSAtomic(ctx context.Context, ref MediaRef, dst, referer string) (int64, error) {
	src := strings.TrimSpace(ref.URL)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	tmp := dst + ".part"
	_ = os.Remove(tmp)
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
	}
	headers := mediaRequestHeaders(ref, referer)
	if ua := strings.TrimSpace(headers.Get("User-Agent")); ua != "" {
		args = append(args, "-user_agent", ua)
	}
	if h := ffmpegHeaderBlock(headers); h != "" {
		args = append(args, "-headers", h)
	}
	args = append(args,
		"-protocol_whitelist", "http,https,tcp,tls,crypto",
		"-allowed_extensions", "ALL",
		"-allowed_segment_extensions", "ALL",
		"-extension_picky", "0",
		"-i", src,
		"-c", "copy",
		"-bsf:a", "aac_adtstoasc",
		"-movflags", "+faststart",
		"-f", "mp4",
		tmp,
	)
	out, err := exec.CommandContext(ctx, c.cfg.FFmpegPath, args...).CombinedOutput()
	if err != nil {
		_ = os.Remove(tmp)
		return 0, mediaCommandError("ffmpeg hls", err, out)
	}
	info, err := os.Stat(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if info.IsDir() || info.Size() <= 0 {
		_ = os.Remove(tmp)
		return 0, errors.New("empty hls output")
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return info.Size(), nil
}

func looksLikeHLSURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err == nil && u != nil && strings.EqualFold(path.Ext(u.Path), ".m3u8") {
		return true
	}
	return strings.Contains(strings.ToLower(raw), ".m3u8")
}

func mediaRequestHeaders(ref MediaRef, referer string) http.Header {
	headers := make(http.Header)
	headers.Set("User-Agent", defaultUserAgent)
	if referer != "" {
		headers.Set("Referer", referer)
	}
	for k, v := range ref.Headers {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		headers.Set(k, v)
	}
	return headers
}

func ffmpegHeaderBlock(headers http.Header) string {
	var b strings.Builder
	for k, values := range headers {
		k = strings.TrimSpace(k)
		if k == "" || strings.EqualFold(k, "User-Agent") {
			continue
		}
		for _, v := range values {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\r\n")
		}
	}
	return b.String()
}

func mediaCommandError(tool string, err error, output []byte) error {
	msg := strings.TrimSpace(redactMediaURLs(string(output)))
	if msg == "" {
		return fmt.Errorf("%s: %w", tool, err)
	}
	return fmt.Errorf("%s: %w: %s", tool, err, msg)
}

func redactMediaURLs(text string) string {
	fields := strings.Fields(text)
	for i, field := range fields {
		if strings.HasPrefix(field, "http://") || strings.HasPrefix(field, "https://") {
			suffix := ""
			for len(field) > 0 {
				last := field[len(field)-1]
				if last != '.' && last != ',' && last != ';' && last != ')' {
					break
				}
				suffix = string(last) + suffix
				field = field[:len(field)-1]
			}
			fields[i] = "https://<redacted>" + suffix
		}
	}
	return strings.Join(fields, " ")
}

func configureExplicitProxy(transport *http.Transport, raw string) error {
	proxyURL := strings.TrimSpace(raw)
	if proxyURL == "" {
		return nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("invalid proxy URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(u)
		transport.DialContext = nil
		return nil
	case "socks5", "socks5h":
		dialContext, err := socksProxyDialContext(u)
		if err != nil {
			return err
		}
		transport.Proxy = nil
		transport.DialContext = dialContext
		return nil
	default:
		return fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
}

func socksProxyDialContext(proxyURL *url.URL) (func(context.Context, string, string) (net.Conn, error), error) {
	var auth *proxy.Auth
	if proxyURL.User != nil {
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		auth = &proxy.Auth{User: username, Password: password}
	}
	dialer, err := proxy.SOCKS5("tcp", proxyURL.Host, auth, &net.Dialer{Timeout: 60 * time.Second})
	if err != nil {
		return nil, err
	}
	remoteDNS := strings.EqualFold(proxyURL.Scheme, "socks5h")
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		target := addr
		if !remoteDNS {
			resolved, err := resolveSocksTarget(ctx, addr)
			if err != nil {
				return nil, err
			}
			target = resolved
		}
		if ctxDialer, ok := dialer.(proxy.ContextDialer); ok {
			return ctxDialer.DialContext(ctx, network, target)
		}
		type result struct {
			conn net.Conn
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			conn, err := dialer.Dial(network, target)
			ch <- result{conn: conn, err: err}
		}()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case res := <-ch:
			return res.conn, res.err
		}
	}, nil
}

func resolveSocksTarget(ctx context.Context, addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) != nil {
		return addr, nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", err
	}
	for _, addr := range ips {
		if ip4 := addr.IP.To4(); ip4 != nil {
			return net.JoinHostPort(ip4.String(), port), nil
		}
	}
	if len(ips) > 0 && ips[0].IP != nil {
		return net.JoinHostPort(ips[0].IP.String(), port), nil
	}
	return "", fmt.Errorf("resolve %s: no address", host)
}

func normalizeItemForImport(item Item) (Item, string, error) {
	item.Title = strings.TrimSpace(item.Title)
	if item.Title == "" {
		return item, "", errors.New("title is required")
	}
	item.DetailURL = strings.TrimSpace(item.DetailURL)
	item.Author = strings.TrimSpace(item.Author)
	item.Quality = strings.TrimSpace(item.Quality)
	item.Description = strings.TrimSpace(item.Description)
	item.PublishedAt = strings.TrimSpace(item.PublishedAt)
	item.MediaURL = strings.TrimSpace(item.MediaURL)
	item.MediaLocalFile = strings.TrimSpace(item.MediaLocalFile)
	item.ThumbnailURL = strings.TrimSpace(item.ThumbnailURL)
	item.ThumbnailLocalFile = strings.TrimSpace(item.ThumbnailLocalFile)

	if strings.TrimSpace(item.Media.URL) == "" {
		item.Media.URL = item.MediaURL
	}
	if strings.TrimSpace(item.Media.LocalFile) == "" {
		item.Media.LocalFile = item.MediaLocalFile
	}
	if len(item.Media.Headers) == 0 {
		if len(item.MediaHeaders) > 0 {
			item.Media.Headers = item.MediaHeaders
		} else if len(item.Headers) > 0 {
			item.Media.Headers = item.Headers
		}
	}
	if strings.TrimSpace(item.Thumbnail.URL) == "" {
		item.Thumbnail.URL = item.ThumbnailURL
	}
	if strings.TrimSpace(item.Thumbnail.LocalFile) == "" {
		item.Thumbnail.LocalFile = item.ThumbnailLocalFile
	}
	if len(item.Thumbnail.Headers) == 0 {
		if len(item.ThumbnailHeaders) > 0 {
			item.Thumbnail.Headers = item.ThumbnailHeaders
		} else if len(item.Headers) > 0 {
			item.Thumbnail.Headers = item.Headers
		}
	}

	item.Media.URL = strings.TrimSpace(item.Media.URL)
	item.Media.LocalFile = strings.TrimSpace(item.Media.LocalFile)
	item.Thumbnail.URL = strings.TrimSpace(item.Thumbnail.URL)
	item.Thumbnail.LocalFile = strings.TrimSpace(item.Thumbnail.LocalFile)
	if item.Media.URL == "" && item.Media.LocalFile == "" {
		return item, "", errors.New("media_url is required")
	}

	sourceID := normalizeSourceID(item.SourceID)
	if sourceID == "" {
		sourceID = generatedSourceID(item)
	}
	return item, sourceID, nil
}

func normalizeSourceID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range raw {
		allowed := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == '.'
		if allowed {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	id := strings.Trim(b.String(), "._-")
	if len(id) > 160 {
		id = strings.Trim(id[:160], "._-")
	}
	return id
}

func generatedSourceID(item Item) string {
	signature := strings.Join([]string{
		item.Title,
		stableURLKey(item.DetailURL),
		stableURLKey(item.Media.URL),
		strings.TrimSpace(item.Media.LocalFile),
	}, "\n")
	sum := sha256.Sum256([]byte(signature))
	return "auto-" + hex.EncodeToString(sum[:])[:24]
}

func stableURLKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	u.Fragment = ""
	if u.RawQuery != "" && strings.TrimSpace(u.Path) != "" && !strings.Contains(strings.ToLower(u.RawQuery), "viewkey=") {
		u.RawQuery = ""
	}
	return u.String()
}

func (c *Crawler) crawlerTagName() string {
	if c == nil {
		return ""
	}
	if v := strings.TrimSpace(c.cfg.CrawlerName); v != "" {
		return v
	}
	if c.cfg.Driver != nil {
		return strings.TrimSpace(c.cfg.Driver.ID())
	}
	return ""
}

func candidateBudgetForTarget(targetNew int) int {
	if targetNew <= 0 {
		targetNew = DefaultTargetNew
	}
	budget := targetNew * defaultCandidateMultiplier
	if floor := targetNew + defaultCandidateFloorExtra; budget < floor {
		budget = floor
	}
	if budget > defaultCandidateBudgetMax {
		budget = defaultCandidateBudgetMax
	}
	if budget < targetNew {
		return targetNew
	}
	return budget
}

func BuildVideoID(driveID, sourceID string) string {
	return Kind + "-" + driveID + "-" + sourceID
}

func detectVideoExt(rawURL, localFile string) string {
	if ext := mediaExt(localFile, true); ext != "" {
		return ext
	}
	if ext := mediaExt(rawURL, true); ext != "" {
		return ext
	}
	return ".mp4"
}

func detectThumbExt(rawURL, localFile string) string {
	if ext := mediaExt(localFile, false); ext != "" {
		return ext
	}
	if ext := mediaExt(rawURL, false); ext != "" {
		return ext
	}
	return ".jpg"
}

func mediaExt(raw string, video bool) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	value := raw
	if u, err := url.Parse(strings.TrimSpace(raw)); err == nil && u != nil && u.Path != "" {
		value = u.Path
	}
	ext := strings.ToLower(path.Ext(value))
	if video {
		switch ext {
		case ".mp4", ".webm", ".mkv", ".mov", ".m4v", ".flv", ".avi":
			return ext
		}
		return ""
	}
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return ext
	}
	return ""
}

func parsePublishedAt(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if ms, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if ms > 100000000000 {
			return time.UnixMilli(ms)
		}
		return time.Unix(ms, 0)
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

func cleanStringList(in []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		key := strings.ToLower(s)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	return out
}

func mergeStringLists(lists ...[]string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, list := range lists {
		for _, s := range list {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			key := strings.ToLower(s)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
