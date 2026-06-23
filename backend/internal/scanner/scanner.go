package scanner

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"path"
	"strings"
	"time"

	"github.com/video-site/backend/internal/catalog"
	"github.com/video-site/backend/internal/drives"
)

type Scanner struct {
	Catalog *catalog.Catalog
	Drive   drives.Drive
	Exts    map[string]bool
	// SkipDirIDs 是用户在 admin 后台配置的"扫描跳过目录"集合（drive 侧的目录 fileID）。
	// 命中其中任意一个时 scanner 直接 continue —— 不递归、不收集文件、不计入
	// SeenFileIDs / VisitedDirIDs，自然也不会被后续 cleanupMissingDriveVideos 当
	// 成"消失了"误删。替代旧版硬编码 p115 "影视" 目录例外分支。
	//
	// nil / 空集合 → 行为等同于不跳过任何目录。
	SkipDirIDs map[string]struct{}
	// 回调：新视频被加入后触发预览视频生成
	OnNewVideo func(v *catalog.Video)
	// OnProgress 在扫描进度变化时触发。回调只应读取 Stats 里的计数，不应修改 map 字段。
	OnProgress func(stats Stats)
	// ProgressInterval 控制扫描内部 heartbeat 的最小输出间隔。
	// 0 → 默认 30s；< 0 → 关闭 heartbeat（仅留外层 start / done 两行）。
	// heartbeat 单行格式：
	//   [scanner] drive=X progress: scanned=N added=K errors=E dirs=M elapsed=Ts at=<dir>
	ProgressInterval time.Duration
}

const defaultScanProgressInterval = 30 * time.Second

// New 构造一个 Scanner。
//
// skipDirIDs 是用户为该 drive 配置的"扫描跳过目录"集合（可空）；nil / 空集合
// 表示不跳过任何目录。被跳过的目录及其全部子目录都不递归。
func New(cat *catalog.Catalog, drv drives.Drive, exts []string, skipDirIDs []string, onNew func(v *catalog.Video)) *Scanner {
	m := make(map[string]bool, len(exts))
	for _, e := range exts {
		m[strings.ToLower(e)] = true
	}
	skip := make(map[string]struct{}, len(skipDirIDs))
	for _, id := range skipDirIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		skip[id] = struct{}{}
	}
	return &Scanner{
		Catalog:    cat,
		Drive:      drv,
		Exts:       m,
		SkipDirIDs: skip,
		OnNewVideo: onNew,
	}
}

type Stats struct {
	Scanned       int
	Added         int
	Errors        int
	SeenFileIDs   map[string]struct{}
	VisitedDirIDs map[string]struct{}
}

// Run 从 Drive.RootID 开始扫描
func (s *Scanner) Run(ctx context.Context, startDirID string) (Stats, error) {
	if startDirID == "" {
		startDirID = s.Drive.RootID()
	}
	stats := Stats{
		SeenFileIDs:   make(map[string]struct{}),
		VisitedDirIDs: make(map[string]struct{}),
	}

	// heartbeat 闭包：进 / 退每个目录、每处理完一个文件后调一下，用一个时间戳节流。
	// 闭包持有的状态都是单 goroutine 顺序写读，不需要锁。
	interval := s.ProgressInterval
	if interval == 0 {
		interval = defaultScanProgressInterval
	}
	started := time.Now()
	lastBeat := started
	driveID := ""
	if s.Drive != nil {
		driveID = s.Drive.ID()
	}
	progress := func(currentDir string) {
		if s.OnProgress != nil {
			s.OnProgress(stats)
		}
		if interval < 0 {
			return
		}
		now := time.Now()
		if now.Sub(lastBeat) < interval {
			return
		}
		lastBeat = now
		shown := currentDir
		if shown == "" {
			shown = "(root)"
		}
		log.Printf("[scanner] drive=%s progress: scanned=%d added=%d errors=%d dirs=%d elapsed=%s at=%s",
			driveID, stats.Scanned, stats.Added, stats.Errors, len(stats.VisitedDirIDs),
			now.Sub(started).Round(time.Second), shown)
	}

	if err := s.walk(ctx, startDirID, "", &stats, progress); err != nil {
		return stats, err
	}
	return stats, nil
}

func (s *Scanner) walk(ctx context.Context, dirID, dirName string, stats *Stats, progress func(string)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stats.VisitedDirIDs[dirID] = struct{}{}
	progress(dirName) // 心跳：进入新目录前后是天然的节流点

	entries, err := s.Drive.List(ctx, dirID)
	if err != nil {
		return fmt.Errorf("list %s: %w", dirID, err)
	}

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.IsDir {
			// 跳过 previews 目录，避免扫到自己生成的预览视频
			if strings.EqualFold(e.Name, "previews") {
				continue
			}
			// 用户在 admin 配置的跳过目录：直接 continue，不递归、不收集文件。
			if _, skip := s.SkipDirIDs[e.ID]; skip {
				continue
			}
			if err := s.walk(ctx, e.ID, e.Name, stats, progress); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				stats.Errors++
				log.Printf("[scanner] walk %s error: %v", e.Name, err)
			}
			continue
		}

		ext := strings.ToLower(path.Ext(e.Name))
		if !s.Exts[ext] {
			continue
		}
		if e.Size <= 0 {
			continue
		}
		stats.Scanned++
		progress(dirName)
		stats.SeenFileIDs[e.ID] = struct{}{}

		id := s.Drive.Kind() + "-" + s.Drive.ID() + "-" + videoIDFilePart(e.ID)
		if deleted, err := s.Catalog.IsDeletedVideoCandidate(ctx, id, s.Drive.ID(), e.ID, e.Hash, e.Name, e.Size); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			stats.Errors++
			log.Printf("[scanner] check deleted video %s error: %v", id, err)
			continue
		} else if deleted {
			continue
		}

		parsed := Parse(e.Name)
		if parsed.Title == "" {
			parsed.Title = strings.TrimSuffix(e.Name, ext)
		}
		tags := parsed.Tags
		if matched, err := s.Catalog.MatchTags(ctx, e.Name+" "+dirName+" "+parsed.Author); err == nil {
			tags = mergeTags(tags, matched)
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		existing, _ := s.Catalog.GetVideo(ctx, id)
		if err := ctx.Err(); err != nil {
			return err
		}
		if existing != nil {
			patch := catalog.VideoMetaPatch{}
			if e.Hash != "" && existing.ContentHash == "" {
				patch.ContentHash = e.Hash
				existing.ContentHash = e.Hash
			}
			if e.Name != "" && existing.FileName != e.Name {
				patch.FileName = e.Name
				existing.FileName = e.Name
				patch.Title = parsed.Title
				patch.TitleSet = true
				patch.Author = parsed.Author
				patch.AuthorSet = true
			}
			if patch.ContentHash != "" || patch.FileName != "" || patch.TitleSet || patch.AuthorSet {
				_ = s.Catalog.UpdateVideoMeta(ctx, id, patch)
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if dup := s.findDuplicate(ctx, e.Hash, e.Name, e.Size, id); dup != nil {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !sameTags(existing.Tags, tags) {
				_ = s.Catalog.SetAutoVideoTags(ctx, id, tags)
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			continue
		}

		if dup := s.findDuplicate(ctx, e.Hash, e.Name, e.Size, id); dup != nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		now := time.Now()
		v := &catalog.Video{
			ID:            id,
			DriveID:       s.Drive.ID(),
			FileID:        e.ID,
			FileName:      e.Name,
			ContentHash:   e.Hash,
			ParentID:      e.ParentID,
			Title:         parsed.Title,
			Author:        parsed.Author,
			Tags:          tags,
			Ext:           strings.TrimPrefix(ext, "."),
			Quality:       "HD",
			Size:          e.Size,
			PreviewStatus: "pending",
			PublishedAt:   now,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if err := s.Catalog.UpsertVideo(ctx, v); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			log.Printf("[scanner] upsert %s error: %v", v.Title, err)
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		stats.Added++
		progress(dirName)
		if s.OnNewVideo != nil {
			s.OnNewVideo(v)
		}
		// 兜底：如果某个目录里挤了几千个文件，仅靠"进目录心跳"会很久不响一下；
		// 在每条文件处理完之后再 ping 一次，progress 内部的 30s 节流会把绝大多数
		// 调用变成廉价的时间比较。
		progress(dirName)
	}
	return nil
}

func (s *Scanner) findDuplicate(ctx context.Context, hash, fileName string, size int64, currentID string) *catalog.Video {
	if dup := s.findDuplicateByHash(ctx, hash, currentID); dup != nil {
		return dup
	}
	return s.findDuplicateByFileSignature(ctx, fileName, size, currentID)
}

func (s *Scanner) findDuplicateByHash(ctx context.Context, hash, currentID string) *catalog.Video {
	if hash == "" {
		return nil
	}
	dup, err := s.Catalog.FindVideoByContentHash(ctx, hash)
	if err != nil || dup == nil || dup.ID == currentID {
		return nil
	}
	return dup
}

func (s *Scanner) findDuplicateByFileSignature(ctx context.Context, fileName string, size int64, currentID string) *catalog.Video {
	if fileName == "" || size <= 0 {
		return nil
	}
	dup, err := s.Catalog.FindVideoByFileSignature(ctx, fileName, size)
	if err != nil || dup == nil || dup.ID == currentID {
		return nil
	}
	return dup
}

func sameTags(a, b []string) bool {
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

func mergeTags(lists ...[]string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, list := range lists {
		for _, tag := range list {
			if tag == "" || seen[tag] {
				continue
			}
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}

func videoIDFilePart(fileID string) string {
	if !strings.ContainsAny(fileID, `/\`+"\x00") {
		return fileID
	}
	return "b64_" + base64.RawURLEncoding.EncodeToString([]byte(fileID))
}
