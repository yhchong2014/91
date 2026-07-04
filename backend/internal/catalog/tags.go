package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/video-site/backend/internal/fixedtags"
	"github.com/video-site/backend/internal/tagging"
)

var ErrUnknownTag = errors.New("unknown tag")
var ErrAutoTagGenerationDisabled = errors.New("auto tag generation is disabled")

const avTagLabel = "AV"

var avTagRule = tagging.Rule{MatchAVCode: true, AVCodePrefixes: tagging.DefaultAVCodePrefixes()}

func avRuleFromPrefixes(prefixes []string) tagging.Rule {
	prefixes = tagging.CleanAVCodePrefixes(prefixes)
	if len(prefixes) == 0 {
		return tagging.Rule{}
	}
	return tagging.Rule{MatchAVCode: true, AVCodePrefixes: prefixes}
}

var avLegacyAliases = map[string]struct{}{
	"jav": {},
	"番号":  {},
	"番號":  {},
}

// settingTagRulesVersion 是标签规则版本号（settings 表）。任何标签的创建、
// 规则修改、删除都会 +1；Matcher 缓存据此失效重建。
const (
	settingTagRulesVersion         = "tags.rules_version"
	settingAutoGenerateTagsEnabled = "tags.auto_generate_enabled"
	settingAVCodeMatchingDisabled  = "tags.av_code_matching_disabled"
	settingBuiltinTagPackInit      = "tags.builtin_pack_initialized_v1"
)

const avSeriesOrigin = "av_series"

type Tag struct {
	ID           int64        `json:"id"`
	Label        string       `json:"label"`
	Aliases      []string     `json:"-"`
	MatchRules   tagging.Rule `json:"matchRules"`
	Source       string       `json:"source"`
	Count        int          `json:"count"`
	CrawlerOwned bool         `json:"crawlerOwned,omitempty"`
}

// TagAssignment 是一次"给视频挂标签"的完整描述：标签名、来源、命中证据。
type TagAssignment struct {
	Label    string
	Source   string
	Evidence string
}

type VideoTagMetadata struct {
	Source   string `json:"source"`
	Evidence string `json:"evidence"`
}

func (c *Catalog) migrate(ctx context.Context) error {
	if err := c.addColumnIfMissing(ctx, "videos", "tags_manual", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "videos", "content_hash", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "videos", "sampled_sha256", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "videos", "fingerprint_status", "TEXT DEFAULT 'pending'"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "videos", "fingerprint_error", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "videos", "file_name", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "videos", "hidden", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "videos", "thumbnail_status", "TEXT DEFAULT 'pending'"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "videos", "thumbnail_failures", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "videos", "last_viewed_at", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "videos", "last_liked_at", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	// videos.transcode_*：浏览器兼容性转码状态。
	// status：''=未检测 / pending=已入队 / ready=已转码 / skipped=检测后无需转码 / failed=失败。
	// transcoded_file_id 指向转码产物在同一 drive 上的 fileID，播放源优先使用它。
	if err := c.addColumnIfMissing(ctx, "videos", "transcode_status", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "videos", "transcode_error", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "videos", "transcoded_file_id", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "videos", "transcoded_size", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	// videos.dir_name：视频所在目录名，扫盘时落库；标签全库重算需要用它做匹配材料。
	if err := c.addColumnIfMissing(ctx, "videos", "dir_name", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	// tags.match_rules：标签匹配规则 JSON；video_tags.evidence：命中证据。
	if err := c.addColumnIfMissing(ctx, "tags", "match_rules", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	if err := c.removeRetiredTagRuleFields(ctx); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "tags", "origin", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "video_tags", "evidence", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := c.dropTagTombstones(ctx); err != nil {
		return err
	}
	if err := c.dropColumnIfExists(ctx, "videos", "category"); err != nil {
		return err
	}
	if err := c.dropColumnIfExists(ctx, "videos", "llm_tagged_at"); err != nil {
		return err
	}
	if err := c.ensureBaseVideoIndexes(ctx); err != nil {
		return err
	}
	// drives.teaser_enabled：每盘预览视频开关，替代旧的全局 preview.enabled。
	// 升级路径：直接让 ALTER TABLE 的 DEFAULT 1 兜底 —— 每个现存 drive 都默认开启，
	// 不读旧的 settings.preview.enabled 字段。这样老用户即便之前关过全局开关，
	// 升级后所有盘也都恢复"默认生成预览视频"，跟新建保持一致。
	if _, err := c.addColumnIfMissingReportNew(ctx, "drives", "teaser_enabled", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	// drives.skip_dir_ids：每盘扫描跳过目录集合（JSON array of string）。命中
	// 其中任意一个的目录及其全部子目录都不会被递归扫描。替代旧版硬编码"影视"
	// 目录例外分支；旧 drive 升级后默认空数组 → 行为等同于以前未启用跳过。
	if err := c.addColumnIfMissing(ctx, "drives", "skip_dir_ids", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS deleted_videos (
	id                 TEXT PRIMARY KEY,
	drive_id           TEXT NOT NULL DEFAULT '',
	file_id            TEXT NOT NULL DEFAULT '',
	parent_id          TEXT NOT NULL DEFAULT '',
	content_hash       TEXT NOT NULL DEFAULT '',
	file_name          TEXT NOT NULL DEFAULT '',
	size_bytes         INTEGER NOT NULL DEFAULT 0,
	reason             TEXT NOT NULL DEFAULT '',
	source_deleted     INTEGER NOT NULL DEFAULT 0,
	canonical_video_id TEXT NOT NULL DEFAULT '',
	deleted_at         INTEGER NOT NULL
)`); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "deleted_videos", "reason", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "deleted_videos", "parent_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "deleted_videos", "source_deleted", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := c.addColumnIfMissing(ctx, "deleted_videos", "canonical_video_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := c.purgeLegacySourceDeletedTombstones(ctx); err != nil {
		return err
	}
	if err := c.syncDriveScanRootIDToRootID(ctx); err != nil {
		return err
	}
	// 一次性修正：早期版本（短暂存在过）会把现存 drive 的 teaser_enabled 同步成
	// 旧的全局 preview.enabled 值，导致升级后所有 drive 都是关。"默认开启"约定下，
	// 这里一次性把所有 drive 强制重置为 1，并用 marker setting 记号，避免之后
	// 再覆盖用户后续在 UI 里 per-drive 改成关的设置。
	if err := c.resetDriveTeaserEnabledToDefaultOnce(ctx); err != nil {
		return err
	}
	// 一次性修正：thumbnail_status 列是后加的（DEFAULT 'pending'），所有列加之前
	// 已有 thumbnail_url 的视频都被填成了 pending。worker 入队按 url 判定不会重复
	// 生成，但 status 字段对管理员/统计是误导（admin API 自己已经按 url 计数所以
	// 不受影响，但直接 SQL 查会以为有 N 千个待生成）。
	// 这里把"url 已写但 status 仍是 pending"的修正为 ready；status=failed 不动。
	if err := c.reconcileThumbnailStatusOnce(ctx); err != nil {
		return err
	}
	if err := c.requeueSkippedPreviews(ctx); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_videos_content_hash ON videos(content_hash)`); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_videos_content_hash_created ON videos(content_hash, created_at, id)`); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_videos_sampled_sha256 ON videos(size_bytes, sampled_sha256)`); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_videos_sampled_sha256_created ON videos(size_bytes, sampled_sha256, created_at, id)`); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_videos_hidden ON videos(hidden)`); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_videos_visible_pub ON videos(COALESCE(hidden, 0), published_at DESC)`); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_videos_last_viewed ON videos(last_viewed_at DESC)`); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_videos_hot ON videos(likes DESC, last_liked_at DESC, published_at DESC)`); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_videos_file_name_size ON videos(file_name, size_bytes)`); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_videos_file_name_size_created ON videos(file_name, size_bytes, created_at, id)`); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_deleted_videos_drive_file ON deleted_videos(drive_id, file_id)`); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_deleted_videos_drive_hash ON deleted_videos(drive_id, content_hash)`); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_deleted_videos_drive_signature ON deleted_videos(drive_id, file_name, size_bytes)`); err != nil {
		return err
	}
	if err := c.normalizeStoredTagSources(ctx); err != nil {
		return err
	}
	if err := c.backfillCrawlerTagOrigins(ctx); err != nil {
		return err
	}
	if err := c.normalizeRetiredVideoTagSources(ctx); err != nil {
		return err
	}
	if err := c.migrateBuiltinTagLabels(ctx); err != nil {
		return err
	}
	if err := c.demoteRetiredBuiltinTags(ctx); err != nil {
		return err
	}
	if err := c.initializeBuiltinTagPackOnce(ctx); err != nil {
		return err
	}
	if err := c.removeAutomaticTaggingArtifacts(ctx); err != nil {
		return err
	}
	if err := c.cleanupInvalidAVSeriesTags(ctx); err != nil {
		return err
	}
	if err := c.clearVolatileOneDriveThumbnails(ctx); err != nil {
		return err
	}
	if err := c.clearRemoteP123ThumbnailsOnce(ctx); err != nil {
		return err
	}
	if err := c.clearRemoteThumbnails(ctx); err != nil {
		return err
	}
	if err := c.hideZeroSizeVideosFromKnownDrives(ctx); err != nil {
		return err
	}
	// admin_sessions.user_id：关联到 users 表，用于区分管理员/普通用户 session
	if err := c.addColumnIfMissing(ctx, "admin_sessions", "user_id", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	return nil
}

// RunPostStartupTagMaintenance normalizes the tag pool, removes retired
// generated labels, and re-matches videos. The only generated labels it may
// add are AV series labels while the built-in AV mechanism is enabled.
func (c *Catalog) RunPostStartupTagMaintenance(ctx context.Context) error {
	if err := c.removeRetiredTagRuleFields(ctx); err != nil {
		return err
	}
	if err := c.removeAutomaticTaggingArtifacts(ctx); err != nil {
		return err
	}
	if err := c.cleanupInvalidAVSeriesTags(ctx); err != nil {
		return err
	}
	matcher, err := c.Matcher(ctx)
	if err != nil {
		return err
	}
	lastID := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, nextID, done, err := c.RetagVideosBatch(ctx, matcher, lastID, 500, 0)
		if err != nil {
			return err
		}
		lastID = nextID
		if done {
			_, err := c.PruneUnreferencedTags(ctx)
			return err
		}
	}
}

func (c *Catalog) removeRetiredTagRuleFields(ctx context.Context) error {
	rows, err := c.db.QueryContext(ctx, `SELECT id, COALESCE(match_rules, '{}') FROM tags`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type update struct {
		id    int64
		rules string
	}
	var updates []update
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		if !strings.Contains(raw, `"words"`) && !strings.Contains(raw, `"excludes"`) {
			continue
		}
		var rule tagging.Rule
		_ = json.Unmarshal([]byte(raw), &rule)
		cleaned := cleanStoredTagRule(rule)
		rulesJSON, _ := json.Marshal(cleaned)
		updates = append(updates, update{id: id, rules: string(rulesJSON)})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx,
			`UPDATE tags SET match_rules = ?, updated_at = ? WHERE id = ?`,
			item.rules, now, item.id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return c.bumpTagRulesVersion(ctx)
}

// normalizeStoredTagSources 把历史标签来源收敛为三类。视频与标签的关联来源
// video_tags.source 记录具体挂载方式，保留 auto/crawler/series 等细分值。
func (c *Catalog) normalizeStoredTagSources(ctx context.Context) error {
	if _, err := c.db.ExecContext(ctx, `
UPDATE tags
   SET source = CASE
       WHEN lower(trim(COALESCE(source, ''))) IN ('system', 'builtin') THEN 'builtin'
       WHEN lower(trim(COALESCE(source, ''))) = 'user' THEN 'user'
       ELSE 'generated'
   END
 WHERE source IS NULL
    OR source != CASE
       WHEN lower(trim(COALESCE(source, ''))) IN ('system', 'builtin') THEN 'builtin'
       WHEN lower(trim(COALESCE(source, ''))) = 'user' THEN 'user'
       ELSE 'generated'
   END`); err != nil {
		return fmt.Errorf("normalize tag sources: %w", err)
	}
	return nil
}

func (c *Catalog) dropTagTombstones(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, `DROP TABLE IF EXISTS deleted_tags`)
	return err
}

func (c *Catalog) backfillCrawlerTagOrigins(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, `
UPDATE tags
   SET origin = 'crawler'
 WHERE COALESCE(origin, '') != 'crawler'
   AND (
       EXISTS (
         SELECT 1
           FROM video_tags vt
          WHERE vt.tag_id = tags.id
            AND lower(trim(COALESCE(vt.source, ''))) = 'crawler'
       )
       OR EXISTS (
         SELECT 1
           FROM drives d
          WHERE d.kind = 'scriptcrawler'
            AND d.name = tags.label COLLATE NOCASE
       )
   )`)
	return err
}

func (c *Catalog) normalizeRetiredVideoTagSources(ctx context.Context) error {
	rows, err := c.db.QueryContext(ctx, `
SELECT DISTINCT video_id
  FROM video_tags
 WHERE lower(trim(COALESCE(source, ''))) = 'llm'`)
	if err != nil {
		return err
	}
	var videoIDs []string
	for rows.Next() {
		var videoID string
		if err := rows.Scan(&videoID); err != nil {
			rows.Close()
			return err
		}
		videoIDs = append(videoIDs, videoID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(videoIDs) == 0 {
		return nil
	}
	if _, err := c.db.ExecContext(ctx, `
DELETE FROM video_tags
 WHERE lower(trim(COALESCE(source, ''))) = 'llm'`); err != nil {
		return err
	}
	for _, videoID := range videoIDs {
		if err := c.syncVideoTagsJSON(ctx, videoID, c.hasManualTags(ctx, videoID)); err != nil {
			return err
		}
	}
	return nil
}

// migrateBuiltinTagLabels handles builtin-label renames while preserving
// existing video assignments.
func (c *Catalog) migrateBuiltinTagLabels(ctx context.Context) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	videoIDs, err := mergeBuiltinTagLabelTx(ctx, tx, "臀", "美臀")
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, videoID := range uniqueStrings(videoIDs) {
		if err := c.syncVideoTagsJSON(ctx, videoID, c.hasManualTags(ctx, videoID)); err != nil {
			return err
		}
	}
	return nil
}

// demoteRetiredBuiltinTags keeps tags.source=builtin limited to fixedtags.All.
// Retired builtin labels are kept as generated until the retired generated-tag
// cleanup removes ordinary generated labels.
func (c *Catalog) demoteRetiredBuiltinTags(ctx context.Context) error {
	labels := fixedtags.Labels
	if len(labels) == 0 {
		if _, err := c.db.ExecContext(ctx, `UPDATE tags SET source = 'generated', updated_at = ? WHERE source = 'builtin'`, time.Now().UnixMilli()); err != nil {
			return fmt.Errorf("demote retired builtin tags: %w", err)
		}
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(labels)), ",")
	now := time.Now().UnixMilli()
	tagArgs := make([]any, 0, len(labels)+1)
	tagArgs = append(tagArgs, now)
	for _, label := range labels {
		tagArgs = append(tagArgs, label)
	}
	if _, err := c.db.ExecContext(ctx, `
UPDATE tags
   SET source = 'generated',
       updated_at = ?
 WHERE source = 'builtin'
   AND label COLLATE NOCASE NOT IN (`+placeholders+`)`, tagArgs...); err != nil {
		return fmt.Errorf("demote retired builtin tags: %w", err)
	}
	return nil
}

func mergeBuiltinTagLabelTx(ctx context.Context, tx *sql.Tx, oldLabel, newLabel string) ([]string, error) {
	var oldID int64
	var oldSource string
	err := tx.QueryRowContext(ctx, `SELECT id, source FROM tags WHERE label = ? COLLATE NOCASE`, oldLabel).Scan(&oldID, &oldSource)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if normalizeTagSource(oldSource) != "builtin" {
		return nil, nil
	}
	videoIDs, err := videoIDsForTagIDTx(ctx, tx, oldID)
	if err != nil {
		return nil, err
	}

	var newID int64
	var newSource string
	err = tx.QueryRowContext(ctx, `SELECT id, source FROM tags WHERE label = ? COLLATE NOCASE`, newLabel).Scan(&newID, &newSource)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx,
			`UPDATE tags SET label = ?, source = 'builtin', updated_at = ? WHERE id = ?`,
			newLabel, time.Now().UnixMilli(), oldID)
		return videoIDs, err
	}
	if err != nil {
		return nil, err
	}

	if normalizeTagSource(newSource) != "builtin" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE tags SET source = 'builtin', updated_at = ? WHERE id = ?`,
			time.Now().UnixMilli(), newID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO video_tags (video_id, tag_id, source, evidence, created_at)
SELECT video_id, ?, source, evidence, created_at
  FROM video_tags
 WHERE tag_id = ?`, newID, oldID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM video_tags WHERE tag_id = ?`, oldID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, oldID); err != nil {
		return nil, err
	}
	return videoIDs, nil
}

func videoIDsForTagIDTx(ctx context.Context, tx *sql.Tx, tagID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT video_id FROM video_tags WHERE tag_id = ?`, tagID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var videoIDs []string
	for rows.Next() {
		var videoID string
		if err := rows.Scan(&videoID); err != nil {
			return nil, err
		}
		videoIDs = append(videoIDs, videoID)
	}
	return videoIDs, rows.Err()
}

func (c *Catalog) purgeLegacySourceDeletedTombstones(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM deleted_videos WHERE COALESCE(source_deleted, 0) = 1`)
	return err
}

func (c *Catalog) addColumnIfMissing(ctx context.Context, table, column, definition string) error {
	_, err := c.addColumnIfMissingReportNew(ctx, table, column, definition)
	return err
}

func (c *Catalog) dropColumnIfExists(ctx context.Context, table, column string) error {
	rows, err := c.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if strings.EqualFold(name, column) {
			found = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !found {
		return nil
	}
	if _, err = c.db.ExecContext(ctx, `ALTER TABLE `+table+` DROP COLUMN `+column); err == nil {
		return nil
	}
	if table == "videos" && (strings.EqualFold(column, "category") || strings.EqualFold(column, "llm_tagged_at")) {
		log.Printf("[catalog] native drop column videos.%s failed, rebuilding videos table with current columns: %v", column, err)
		return c.rebuildVideosTableWithoutCategory(ctx)
	}
	return err
}

func (c *Catalog) ensureBaseVideoIndexes(ctx context.Context) error {
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_videos_drive ON videos(drive_id, file_id)`,
		`CREATE INDEX IF NOT EXISTS idx_videos_pub ON videos(published_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_videos_views ON videos(views DESC)`,
	} {
		if _, err := c.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

var currentVideoColumnNames = []string{
	"id",
	"drive_id",
	"file_id",
	"file_name",
	"content_hash",
	"sampled_sha256",
	"fingerprint_status",
	"fingerprint_error",
	"parent_id",
	"dir_name",
	"title",
	"author",
	"tags",
	"duration_seconds",
	"size_bytes",
	"ext",
	"quality",
	"thumbnail_url",
	"thumbnail_status",
	"thumbnail_failures",
	"preview_file_id",
	"preview_local",
	"preview_status",
	"transcode_status",
	"transcode_error",
	"transcoded_file_id",
	"transcoded_size",
	"views",
	"last_viewed_at",
	"favorites",
	"comments",
	"likes",
	"last_liked_at",
	"dislikes",
	"hidden",
	"tags_manual",
	"badges",
	"description",
	"published_at",
	"created_at",
	"updated_at",
}

const createVideosWithoutCategorySQL = `
CREATE TABLE videos_category_drop_new (
    id                 TEXT PRIMARY KEY,
    drive_id           TEXT NOT NULL,
    file_id            TEXT NOT NULL,
    file_name          TEXT DEFAULT '',
    content_hash       TEXT DEFAULT '',
    sampled_sha256     TEXT DEFAULT '',
    fingerprint_status TEXT DEFAULT 'pending',
    fingerprint_error  TEXT DEFAULT '',
    parent_id          TEXT,
    dir_name           TEXT DEFAULT '',
    title              TEXT NOT NULL,
    author             TEXT,
    tags               TEXT,
    duration_seconds   INTEGER DEFAULT 0,
    size_bytes         INTEGER DEFAULT 0,
    ext                TEXT,
    quality            TEXT,
    thumbnail_url      TEXT,
    thumbnail_status   TEXT DEFAULT 'pending',
    thumbnail_failures INTEGER DEFAULT 0,
    preview_file_id    TEXT,
    preview_local      TEXT,
    preview_status     TEXT DEFAULT 'pending',
    transcode_status   TEXT DEFAULT '',
    transcode_error    TEXT DEFAULT '',
    transcoded_file_id TEXT DEFAULT '',
    transcoded_size    INTEGER DEFAULT 0,
    views              INTEGER DEFAULT 0,
    last_viewed_at     INTEGER DEFAULT 0,
    favorites          INTEGER DEFAULT 0,
    comments           INTEGER DEFAULT 0,
    likes              INTEGER DEFAULT 0,
    last_liked_at      INTEGER DEFAULT 0,
    dislikes           INTEGER DEFAULT 0,
    hidden             INTEGER DEFAULT 0,
    tags_manual        INTEGER DEFAULT 0,
    badges             TEXT,
    description        TEXT,
    published_at       INTEGER NOT NULL,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
)`

func (c *Catalog) rebuildVideosTableWithoutCategory(ctx context.Context) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS videos_category_drop_new`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, createVideosWithoutCategorySQL); err != nil {
		return err
	}
	cols := strings.Join(currentVideoColumnNames, ", ")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO videos_category_drop_new (`+cols+`) SELECT `+cols+` FROM videos`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE videos`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE videos_category_drop_new RENAME TO videos`); err != nil {
		return err
	}
	return tx.Commit()
}

// addColumnIfMissingReportNew 与 addColumnIfMissing 同步，但额外返回 added=true 表示
// 本次确实创建了新列（即旧 schema 缺这列），方便调用方仅在迁移路径里补做一次性
// 数据初始化（如把全局 setting 同步到新 per-drive 字段）。
//
// 已存在该列时返回 added=false，任何 ALTER TABLE 错误也直接透传。
func (c *Catalog) addColumnIfMissingReportNew(ctx context.Context, table, column, definition string) (bool, error) {
	rows, err := c.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return false, nil
		}
	}
	if _, err := c.db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+definition); err != nil {
		return false, err
	}
	return true, nil
}

// resetDriveTeaserEnabledToDefaultOnce 把所有现存 drive 的 teaser_enabled 强制
// 设为 1（开启），但仅在历史上没跑过这条迁移时执行（用 marker setting 记号）。
//
// 为什么需要：早期短暂存在过的版本会从旧的全局 preview.enabled = "0" 同步到
// 所有 drive 的 teaser_enabled = 0；用户报告升级后页面全显示"预览视频关"。新版
// 约定 per-drive 默认开启，所以这里跑一次性修正。
//
// 幂等保证：marker setting 设过了就不再跑，确保用户在 UI 里把某盘关了不会被
// 重启时反复打开。
func (c *Catalog) resetDriveTeaserEnabledToDefaultOnce(ctx context.Context) error {
	const markerKey = "drives.teaser_enabled.default_open_migrated"
	marker, err := c.GetSetting(ctx, markerKey, "")
	if err != nil {
		return fmt.Errorf("read %s marker: %w", markerKey, err)
	}
	if strings.TrimSpace(marker) == "1" {
		return nil
	}
	if _, err := c.db.ExecContext(ctx, `UPDATE drives SET teaser_enabled = 1, updated_at = ?`, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("reset teaser_enabled to default: %w", err)
	}
	if err := c.SetSetting(ctx, markerKey, "1"); err != nil {
		return fmt.Errorf("write %s marker: %w", markerKey, err)
	}
	return nil
}

// reconcileThumbnailStatusOnce 把所有"封面 URL 已写但 thumbnail_status 仍停留在
// 'pending'"的视频行修正为 'ready'。仅在历史上没跑过这条迁移时执行（marker 守护）。
//
// 为什么需要：thumbnail_status 列是历史某次加进 schema 的（addColumnIfMissing
// 在 tags.go:51，DEFAULT 'pending'）。列加入时所有已存在的视频 thumbnail_url
// 已经填好（指向本地 /p/thumb/<id>），但 status 列 ALTER 时按 DEFAULT 全部填了
// 'pending'。worker 入队按 url 判定（不看 status）所以行为正确，但：
//   - 直接 SQL 查 thumbnail_status='pending' 会以为有几千条待生成
//   - 管理员凭直觉认知字段名时会被误导
//
// 修正策略：
//   - thumbnail_url 非空 + status 非 'ready' + status 非 'failed' + status 非 'skipped' → 改成 'ready'
//   - status='failed' 不动（这是 worker 显式标的失败，要保留以便管理员手动重生）
//   - status='skipped' 不动（已有封面但时长探测不可用，避免重启后重复排队）
//
// 幂等保证：marker setting 写过就不再跑，避免每次重启都 update 一遍。
func (c *Catalog) reconcileThumbnailStatusOnce(ctx context.Context) error {
	const markerKey = "videos.thumbnail_status.url_present_to_ready_migrated"
	marker, err := c.GetSetting(ctx, markerKey, "")
	if err != nil {
		return fmt.Errorf("read %s marker: %w", markerKey, err)
	}
	if strings.TrimSpace(marker) == "1" {
		return nil
	}
	res, err := c.db.ExecContext(ctx, `
UPDATE videos
   SET thumbnail_status = 'ready',
       updated_at = ?
 WHERE COALESCE(thumbnail_url, '') != ''
   AND COALESCE(thumbnail_status, 'pending') NOT IN ('ready', 'failed', 'skipped')
`, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("reconcile thumbnail_status: %w", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected > 0 {
		log.Printf("[catalog] reconciled %d video(s) thumbnail_status pending→ready (url already written)", affected)
	}
	if err := c.SetSetting(ctx, markerKey, "1"); err != nil {
		return fmt.Errorf("write %s marker: %w", markerKey, err)
	}
	return nil
}

func (c *Catalog) requeueSkippedPreviews(ctx context.Context) error {
	res, err := c.db.ExecContext(ctx, `
UPDATE videos
   SET preview_file_id = '',
       preview_local = '',
       preview_status = 'pending',
       updated_at = ?
 WHERE COALESCE(preview_status, 'pending') = 'skipped'
`, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("requeue skipped previews: %w", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected > 0 {
		log.Printf("[catalog] requeued %d skipped preview(s) for generation", affected)
	}
	return nil
}

func (c *Catalog) clearVolatileOneDriveThumbnails(ctx context.Context) error {
	// 把 OneDrive 过期的 mediap.svc.ms thumb URL 清空，让 worker 重新抽帧生成本地封面。
	// 同步把 thumbnail_status 重置为 'pending'：清空后 url 是空的，本应进 worker 重做，
	// 若 status 还停留在 'ready' / 'failed' 会和 ListVideosNeedingThumbnail 的语义不一致
	// （admin/统计按 url 看：空 + 非 'failed' = pending；status='failed' 会让重做被阻断）。
	_, err := c.db.ExecContext(ctx, `
UPDATE videos
   SET thumbnail_url = '',
       thumbnail_status = 'pending',
       updated_at = ?
 WHERE lower(COALESCE(thumbnail_url, '')) LIKE 'https://%mediap.svc.ms/transform/thumbnail%'
`, time.Now().UnixMilli())
	return err
}

func (c *Catalog) clearRemoteP123ThumbnailsOnce(ctx context.Context) error {
	// 123网盘列表返回的缩略图尺寸和稳定性都不适合作为站内封面；清空历史写入的
	// 远程 URL，让封面 worker 统一从视频直链抽帧生成本地 /p/thumb/<id>。
	const markerKey = "videos.p123.remote_thumbnails_cleared"
	marker, err := c.GetSetting(ctx, markerKey, "")
	if err != nil {
		return fmt.Errorf("read %s marker: %w", markerKey, err)
	}
	if strings.TrimSpace(marker) == "1" {
		return nil
	}

	var p123Drives int
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM drives WHERE kind = 'p123'`).Scan(&p123Drives); err != nil {
		return fmt.Errorf("count p123 drives: %w", err)
	}
	if p123Drives == 0 {
		return nil
	}

	res, err := c.db.ExecContext(ctx, `
	UPDATE videos
	   SET thumbnail_url = '',
	       thumbnail_status = 'pending',
	       thumbnail_failures = 0,
	       updated_at = ?
	 WHERE EXISTS (
	       SELECT 1
	         FROM drives
	        WHERE drives.id = videos.drive_id
	          AND drives.kind = 'p123'
	   )
	   AND (
	       lower(COALESCE(thumbnail_url, '')) LIKE 'http://%'
	       OR lower(COALESCE(thumbnail_url, '')) LIKE 'https://%'
	   )
	`, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err == nil && affected > 0 {
		log.Printf("[catalog] cleared %d remote 123pan thumbnail(s) for local regeneration", affected)
	}
	if err := c.SetSetting(ctx, markerKey, "1"); err != nil {
		return fmt.Errorf("write %s marker: %w", markerKey, err)
	}
	return nil
}

func (c *Catalog) clearRemoteThumbnails(ctx context.Context) error {
	// 不再使用网盘侧返回的远程缩略图。清空历史 http/https thumbnail_url 后，
	// 封面 worker 会重新从视频中间帧生成本地 /p/thumb/<id>。
	res, err := c.db.ExecContext(ctx, `
UPDATE videos
   SET thumbnail_url = '',
       thumbnail_status = 'pending',
       thumbnail_failures = 0,
       updated_at = ?
 WHERE (
       lower(COALESCE(thumbnail_url, '')) LIKE 'http://%'
       OR lower(COALESCE(thumbnail_url, '')) LIKE 'https://%'
   )
`, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err == nil && affected > 0 {
		log.Printf("[catalog] cleared %d remote thumbnail(s) for local regeneration", affected)
	}
	return nil
}

func (c *Catalog) hideZeroSizeVideosFromKnownDrives(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, `
UPDATE videos
   SET hidden = 1,
       updated_at = ?
 WHERE COALESCE(size_bytes, 0) <= 0
   AND COALESCE(hidden, 0) = 0
   AND EXISTS (
	 SELECT 1
	   FROM drives
	  WHERE drives.id = videos.drive_id
   )
`, time.Now().UnixMilli())
	return err
}

// initializeBuiltinTagPackOnce runs the one-time legacy tag-pool reset:
// keep administrator-created tags, drop non-user tags, then add the current
// builtin pack. After the marker is written, deleted builtin tags are treated
// as deliberate user edits and are not restored by startup or nightly work.
func (c *Catalog) initializeBuiltinTagPackOnce(ctx context.Context) error {
	marker, err := c.GetSetting(ctx, settingBuiltinTagPackInit, "")
	if err != nil {
		return err
	}
	if parseSettingBool(marker, false) {
		return nil
	}
	if err := c.resetNonUserTagsForBuiltinInit(ctx); err != nil {
		return err
	}
	if err := c.seedBuiltinTagPack(ctx); err != nil {
		return err
	}
	return c.SetSetting(ctx, settingBuiltinTagPackInit, "1")
}

func (c *Catalog) resetNonUserTagsForBuiltinInit(ctx context.Context) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	builtinPlaceholders := placeholders(len(fixedtags.Labels))
	resetFilterWithAlias := `lower(trim(COALESCE(t.source, ''))) != 'user'`
	resetFilter := `lower(trim(COALESCE(source, ''))) != 'user'`
	args := make([]any, 0, len(fixedtags.Labels))
	if builtinPlaceholders != "" {
		resetFilterWithAlias += ` OR t.label COLLATE NOCASE IN (` + builtinPlaceholders + `)`
		resetFilter += ` OR label COLLATE NOCASE IN (` + builtinPlaceholders + `)`
		for _, label := range fixedtags.Labels {
			args = append(args, label)
		}
	}

	rows, err := tx.QueryContext(ctx, `
SELECT DISTINCT vt.video_id
  FROM video_tags vt
  JOIN tags t ON t.id = vt.tag_id
 WHERE `+resetFilterWithAlias, args...)
	if err != nil {
		return err
	}
	var videoIDs []string
	for rows.Next() {
		var videoID string
		if err := rows.Scan(&videoID); err != nil {
			rows.Close()
			return err
		}
		videoIDs = append(videoIDs, videoID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
DELETE FROM video_tags
 WHERE tag_id IN (
       SELECT id
         FROM tags
        WHERE `+resetFilter+`
 )`, args...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM tags
 WHERE `+resetFilter, args...); err != nil {
		return err
	}
	for _, videoID := range uniqueStrings(videoIDs) {
		manual := hasManualTagsTx(ctx, tx, videoID)
		if err := syncVideoTagsJSONTx(ctx, tx, videoID, manual); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

// seedBuiltinTagPack writes the current builtin tag pack. Existing user tags
// with the same label are kept as user tags; existing non-empty rules are not
// overwritten.
func (c *Catalog) seedBuiltinTagPack(ctx context.Context) error {
	for _, t := range fixedtags.All() {
		isAVTag := strings.EqualFold(t.Label, avTagLabel)
		rule := t.Rule
		if isAVTag {
			rule = avTagRule
		}
		if _, err := c.ensureTagWithRules(ctx, t.Label, t.Aliases, rule, t.Source); err != nil {
			return err
		}
		if isAVTag {
			if err := c.removeAVLegacyAliases(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// removeAutomaticTaggingArtifacts removes the retired "create new labels from
// content" model. It preserves builtin/user tag definitions plus crawler-owned
// tags, and leaves engine assignments that point at preserved tags for the
// subsequent existing-tag retag pass to refresh.
func (c *Catalog) removeAutomaticTaggingArtifacts(ctx context.Context) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	generatedTagFilter := `
SELECT t.id
  FROM tags t
 WHERE lower(trim(COALESCE(t.source, ''))) = 'generated'
   AND lower(trim(COALESCE(t.origin, ''))) != 'crawler'
   AND lower(trim(COALESCE(t.origin, ''))) != '` + avSeriesOrigin + `'
   AND NOT EXISTS (
     SELECT 1
       FROM video_tags vt_crawler
      WHERE vt_crawler.tag_id = t.id
        AND lower(trim(COALESCE(vt_crawler.source, ''))) = 'crawler'
   )`

	affectedRows, err := tx.QueryContext(ctx, `
SELECT DISTINCT vt.video_id
  FROM video_tags vt
  LEFT JOIN tags t ON t.id = vt.tag_id
 WHERE lower(trim(COALESCE(vt.source, ''))) IN ('series', 'propagated')
    OR vt.tag_id IN (`+generatedTagFilter+`)`)
	if err != nil {
		return err
	}
	var videoIDs []string
	for affectedRows.Next() {
		var videoID string
		if err := affectedRows.Scan(&videoID); err != nil {
			affectedRows.Close()
			return err
		}
		videoIDs = append(videoIDs, videoID)
	}
	if err := affectedRows.Err(); err != nil {
		affectedRows.Close()
		return err
	}
	if err := affectedRows.Close(); err != nil {
		return err
	}

	removedAssignments := int64(0)
	res, err := tx.ExecContext(ctx, `
DELETE FROM video_tags
 WHERE lower(trim(COALESCE(source, ''))) IN ('series', 'propagated')`)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil {
		removedAssignments += n
	}
	res, err = tx.ExecContext(ctx, `DELETE FROM video_tags WHERE tag_id IN (`+generatedTagFilter+`)`)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil {
		removedAssignments += n
	}

	res, err = tx.ExecContext(ctx, `DELETE FROM tags WHERE id IN (`+generatedTagFilter+`)`)
	if err != nil {
		return err
	}
	removedTags, _ := res.RowsAffected()

	staleRows, err := tx.QueryContext(ctx, `
SELECT id
  FROM videos
 WHERE COALESCE(tags_manual, 0) = 0
   AND COALESCE(tags, '') NOT IN ('', '[]', 'null')
   AND NOT EXISTS (
     SELECT 1
       FROM video_tags vt
      WHERE vt.video_id = videos.id
   )`)
	if err != nil {
		return err
	}
	for staleRows.Next() {
		var videoID string
		if err := staleRows.Scan(&videoID); err != nil {
			staleRows.Close()
			return err
		}
		videoIDs = append(videoIDs, videoID)
	}
	if err := staleRows.Err(); err != nil {
		staleRows.Close()
		return err
	}
	if err := staleRows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE videos
   SET tags = '[]'
 WHERE COALESCE(tags_manual, 0) = 0
   AND COALESCE(tags, '') NOT IN ('', '[]', 'null')
   AND NOT EXISTS (
     SELECT 1
       FROM video_tags vt
      WHERE vt.video_id = videos.id
   )`); err != nil {
		return err
	}

	for _, videoID := range uniqueStrings(videoIDs) {
		manual := hasManualTagsTx(ctx, tx, videoID)
		if err := syncVideoTagsJSONTx(ctx, tx, videoID, manual); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at) VALUES (?, 'false', ?)
ON CONFLICT(key) DO UPDATE SET
  value = 'false',
  updated_at = excluded.updated_at`, settingAutoGenerateTagsEnabled, time.Now().UnixMilli()); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	if removedAssignments > 0 || removedTags > 0 {
		log.Printf("[catalog] removed retired automatic tagging artifacts: assignments=%d tags=%d", removedAssignments, removedTags)
		if removedTags > 0 {
			if err := c.bumpTagRulesVersion(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// classifyAllTagsAddOnly 用当前标签池对全库做一次"只增不减"的分类：
// 给每个非人工锁定视频补上缺失的命中标签，不移除任何已有标签。
// HTTP 监听完成后在后台执行，保证新加的内置标签对存量视频生效且不阻塞启动；
// 完整的重算走 retag job。
func (c *Catalog) classifyAllTagsAddOnly(ctx context.Context) error {
	matcher, err := c.Matcher(ctx)
	if err != nil {
		return err
	}
	if len(matcher.Labels()) == 0 {
		return nil
	}

	existing, err := c.loadVideoTagLabelSets(ctx)
	if err != nil {
		return err
	}

	rows, err := c.db.QueryContext(ctx, `
SELECT id, title, COALESCE(author, ''), COALESCE(file_name, ''), COALESCE(dir_name, ''), COALESCE(tags_manual, 0)
FROM videos`)
	if err != nil {
		return err
	}
	type pendingVideo struct {
		id      string
		matches []tagging.Match
	}
	var pending []pendingVideo
	for rows.Next() {
		var videoID, title, author, fileName, dirName string
		var manual int
		if err := rows.Scan(&videoID, &title, &author, &fileName, &dirName, &manual); err != nil {
			rows.Close()
			return err
		}
		if manual == 1 {
			continue
		}
		matches := matcher.Match(matchFields(title, fileName, author, dirName)...)
		if len(matches) == 0 {
			continue
		}
		have := existing[videoID]
		var missing []tagging.Match
		for _, m := range matches {
			if _, ok := have[strings.ToLower(m.Label)]; !ok {
				missing = append(missing, m)
			}
		}
		if len(missing) > 0 {
			pending = append(pending, pendingVideo{id: videoID, matches: missing})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	total := 0
	for _, p := range pending {
		changed := false
		for _, m := range p.matches {
			tag, err := c.getTagByLabel(ctx, m.Label)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				return err
			}
			if err := c.insertVideoTag(ctx, p.id, tag.ID, "auto", m.Evidence()); err != nil {
				return err
			}
			changed = true
			total++
		}
		if changed {
			if err := c.syncVideoTagsJSON(ctx, p.id, false); err != nil {
				return err
			}
		}
	}
	if total > 0 {
		log.Printf("[catalog] classified %d missing video tag(s) in post-startup background job", total)
	}
	return nil
}

// matchFields 组装匹配材料，顺序即证据优先级。
func matchFields(title, fileName, author, dirName string) []tagging.Field {
	return []tagging.Field{
		{Name: "标题", Text: title},
		{Name: "文件名", Text: fileName},
		{Name: "作者", Text: author},
		{Name: "目录", Text: dirName},
	}
}

// loadVideoTagLabelSets 一次性载入全部视频当前已挂标签（video_id → 小写 label 集合）。
func (c *Catalog) loadVideoTagLabelSets(ctx context.Context) (map[string]map[string]struct{}, error) {
	rows, err := c.db.QueryContext(ctx, `
SELECT vt.video_id, t.label
FROM video_tags vt
JOIN tags t ON t.id = vt.tag_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]struct{}{}
	for rows.Next() {
		var videoID, label string
		if err := rows.Scan(&videoID, &label); err != nil {
			return nil, err
		}
		set := out[videoID]
		if set == nil {
			set = map[string]struct{}{}
			out[videoID] = set
		}
		set[strings.ToLower(label)] = struct{}{}
	}
	return out, rows.Err()
}

func (c *Catalog) backfillVideoTags(ctx context.Context) error {
	rows, err := c.db.QueryContext(ctx, `
SELECT id, COALESCE(tags, '[]')
FROM videos
WHERE COALESCE(tags, '') NOT IN ('', '[]', 'null')
  AND NOT EXISTS (
	SELECT 1
	  FROM video_tags vt
	 WHERE vt.video_id = videos.id
  )`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var videoID, tagsJSON string
		if err := rows.Scan(&videoID, &tagsJSON); err != nil {
			return err
		}
		var labels []string
		if err := json.Unmarshal([]byte(tagsJSON), &labels); err != nil {
			continue
		}
		if len(labels) == 0 {
			continue
		}
		added, err := c.addVideoTags(ctx, videoID, labels, "legacy", true)
		if err != nil {
			return err
		}
		if added {
			if err := c.syncVideoTagsJSON(ctx, videoID, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Catalog) CreateTagAndClassify(ctx context.Context, label string, aliases []string, source string) (int, error) {
	tag, err := c.ensureTag(ctx, label, aliases, source)
	if err != nil {
		return 0, err
	}
	return c.classifyTag(ctx, tag)
}

// UpdateTag 更新管理后台"编辑标签"内容。普通标签保存完整匹配规则；
// AV 标签只保存车牌前缀规则。
func (c *Catalog) UpdateTag(ctx context.Context, tagID int64, rule tagging.Rule) (Tag, error) {
	tag, err := c.getTagByID(ctx, tagID)
	if err != nil {
		return Tag{}, err
	}
	if strings.EqualFold(tag.Label, avTagLabel) {
		prefixes := tagging.CleanAVCodePrefixes(rule.AVCodePrefixes)
		rule = avRuleFromPrefixes(prefixes)
		aliasesJSON, _ := json.Marshal([]string{})
		rulesJSON, _ := json.Marshal(rule)
		if _, err := c.db.ExecContext(ctx,
			`UPDATE tags SET aliases = ?, match_rules = ?, updated_at = ? WHERE id = ?`,
			string(aliasesJSON), string(rulesJSON), time.Now().UnixMilli(), tagID); err != nil {
			return Tag{}, err
		}
		if err := c.setAVCodeMatchingDisabled(ctx, len(prefixes) == 0); err != nil {
			return Tag{}, err
		}
		if err := c.bumpTagRulesVersion(ctx); err != nil {
			return Tag{}, err
		}
		return c.getTagByID(ctx, tagID)
	}
	rule = cleanTagRule(rule)
	aliasesJSON, _ := json.Marshal([]string{})
	rulesJSON, _ := json.Marshal(rule)
	if _, err := c.db.ExecContext(ctx,
		`UPDATE tags SET aliases = ?, match_rules = ?, updated_at = ? WHERE id = ?`,
		string(aliasesJSON), string(rulesJSON), time.Now().UnixMilli(), tagID); err != nil {
		return Tag{}, err
	}
	if err := c.bumpTagRulesVersion(ctx); err != nil {
		return Tag{}, err
	}
	return c.getTagByID(ctx, tagID)
}

// ClassifyTagByID applies an existing tag's current rule to matching unlocked
// videos. It never creates new tag definitions.
func (c *Catalog) ClassifyTagByID(ctx context.Context, tagID int64) (int, error) {
	tag, err := c.getTagByID(ctx, tagID)
	if err != nil {
		return 0, err
	}
	return c.classifyTag(ctx, tag)
}

func (c *Catalog) EnsureTagForVideoIDPrefix(ctx context.Context, prefix, label string, aliases []string, source string) (int, error) {
	return c.ensureTagForVideoIDPrefix(ctx, prefix, label, aliases, source, true)
}

func (c *Catalog) EnsureCrawlerTagForVideoIDPrefix(ctx context.Context, prefix, label string) (int, error) {
	hasVideos, err := c.videoIDPrefixExists(ctx, prefix)
	if err != nil || !hasVideos {
		return 0, err
	}
	tag, err := c.EnsureCrawlerTag(ctx, label)
	if err != nil {
		return 0, err
	}
	return c.addTagForVideoIDPrefix(ctx, prefix, tag, false)
}

func (c *Catalog) videoIDPrefixExists(ctx context.Context, prefix string) (bool, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return false, errors.New("video id prefix is required")
	}
	var n int
	err := c.db.QueryRowContext(ctx, `
SELECT 1
  FROM videos
 WHERE id LIKE ? || '%'
 LIMIT 1`, prefix).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (c *Catalog) ensureTagForVideoIDPrefix(ctx context.Context, prefix, label string, aliases []string, source string, respectAutoGenerateSetting bool) (int, error) {
	tag, err := c.ensureTagWithRulesInternal(ctx, label, aliases, tagging.Rule{}, source, respectAutoGenerateSetting)
	if err != nil {
		return 0, err
	}
	return c.addTagForVideoIDPrefix(ctx, prefix, tag, true)
}

func (c *Catalog) addTagForVideoIDPrefix(ctx context.Context, prefix string, tag Tag, skipManual bool) (int, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return 0, errors.New("video id prefix is required")
	}
	manualWhere := ""
	if skipManual {
		manualWhere = "   AND COALESCE(v.tags_manual, 0) = 0\n"
	}
	rows, err := c.db.QueryContext(ctx, `
SELECT v.id
  FROM videos v
 WHERE v.id LIKE ? || '%'
`+manualWhere+`
   AND NOT EXISTS (
	 SELECT 1
	   FROM video_tags vt
	  WHERE vt.video_id = v.id
	    AND vt.tag_id = ?
   )
 ORDER BY v.id ASC`, prefix, tag.ID)
	if err != nil {
		return 0, err
	}
	var videoIDs []string
	for rows.Next() {
		var videoID string
		if err := rows.Scan(&videoID); err != nil {
			rows.Close()
			return 0, err
		}
		videoIDs = append(videoIDs, videoID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, videoID := range videoIDs {
		if err := c.insertVideoTag(ctx, videoID, tag.ID, "crawler", "爬虫:"+tag.Label); err != nil {
			return 0, err
		}
		if err := c.syncVideoTagsJSON(ctx, videoID, c.hasManualTags(ctx, videoID)); err != nil {
			return 0, err
		}
	}
	return len(videoIDs), nil
}

func (c *Catalog) DeleteTag(ctx context.Context, tagID int64) (int, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	tag, err := c.getTagByIDTx(ctx, tx, tagID)
	if err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT video_id FROM video_tags WHERE tag_id = ?`, tagID)
	if err != nil {
		return 0, err
	}
	var videoIDs []string
	for rows.Next() {
		var videoID string
		if err := rows.Scan(&videoID); err != nil {
			rows.Close()
			return 0, err
		}
		videoIDs = append(videoIDs, videoID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM video_tags WHERE tag_id = ?`, tagID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, tagID); err != nil {
		return 0, err
	}
	if strings.EqualFold(tag.Label, avTagLabel) {
		avSeriesVideoIDs, err := cleanupGeneratedAVSeriesTagsTx(ctx, tx)
		if err != nil {
			return 0, err
		}
		videoIDs = append(videoIDs, avSeriesVideoIDs...)
		if err := setAVCodeMatchingDisabledTx(ctx, tx, true); err != nil {
			return 0, err
		}
	}

	affectedVideoIDs := uniqueStrings(videoIDs)
	for _, videoID := range affectedVideoIDs {
		manual := hasManualTagsTx(ctx, tx, videoID)
		if err := syncVideoTagsJSONTx(ctx, tx, videoID, manual); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if err := c.bumpTagRulesVersion(ctx); err != nil {
		return 0, err
	}
	return len(affectedVideoIDs), nil
}

func cleanupGeneratedAVSeriesTagsTx(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT DISTINCT vt.video_id
  FROM video_tags vt
  JOIN tags t ON t.id = vt.tag_id
 WHERE lower(trim(COALESCE(t.source, ''))) = 'generated'
   AND lower(trim(COALESCE(t.origin, ''))) = ?`, avSeriesOrigin)
	if err != nil {
		return nil, err
	}
	var videoIDs []string
	for rows.Next() {
		var videoID string
		if err := rows.Scan(&videoID); err != nil {
			rows.Close()
			return nil, err
		}
		videoIDs = append(videoIDs, videoID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM video_tags
 WHERE tag_id IN (
       SELECT id
         FROM tags
        WHERE lower(trim(COALESCE(source, ''))) = 'generated'
          AND lower(trim(COALESCE(origin, ''))) = ?
 )`, avSeriesOrigin); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM tags
 WHERE lower(trim(COALESCE(source, ''))) = 'generated'
   AND lower(trim(COALESCE(origin, ''))) = ?`, avSeriesOrigin); err != nil {
		return nil, err
	}
	return videoIDs, nil
}

func (c *Catalog) cleanupInvalidAVSeriesTags(ctx context.Context) error {
	allowedLabels := map[string]struct{}{}
	avCodes, err := c.avCodeMatcher(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, prefix := range avCodes.Prefixes() {
		allowedLabels[strings.ToLower(prefix)] = struct{}{}
	}

	rows, err := c.db.QueryContext(ctx, `
SELECT id, label
  FROM tags
 WHERE lower(trim(COALESCE(source, ''))) = 'generated'
   AND lower(trim(COALESCE(origin, ''))) = ?`, avSeriesOrigin)
	if err != nil {
		return err
	}
	var tagIDs []int64
	for rows.Next() {
		var tagID int64
		var label string
		if err := rows.Scan(&tagID, &label); err != nil {
			rows.Close()
			return err
		}
		if _, ok := allowedLabels[strings.ToLower(tagging.NormalizeAVCodePrefix(label))]; !ok {
			tagIDs = append(tagIDs, tagID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(tagIDs) == 0 {
		return nil
	}

	args := make([]any, 0, len(tagIDs))
	for _, tagID := range tagIDs {
		args = append(args, tagID)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	affectedRows, err := tx.QueryContext(ctx, `SELECT DISTINCT video_id FROM video_tags WHERE tag_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return err
	}
	var videoIDs []string
	for affectedRows.Next() {
		var videoID string
		if err := affectedRows.Scan(&videoID); err != nil {
			affectedRows.Close()
			return err
		}
		videoIDs = append(videoIDs, videoID)
	}
	if err := affectedRows.Err(); err != nil {
		affectedRows.Close()
		return err
	}
	if err := affectedRows.Close(); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM video_tags WHERE tag_id IN (`+placeholders+`)`, args...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE id IN (`+placeholders+`)`, args...); err != nil {
		return err
	}
	for _, videoID := range uniqueStrings(videoIDs) {
		manual := hasManualTagsTx(ctx, tx, videoID)
		if err := syncVideoTagsJSONTx(ctx, tx, videoID, manual); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := c.bumpTagRulesVersion(ctx); err != nil {
		return err
	}
	log.Printf("[catalog] removed %d invalid AV series tag(s)", len(tagIDs))
	return nil
}

func (c *Catalog) ListTags(ctx context.Context) ([]Tag, error) {
	rows, err := c.db.QueryContext(ctx, `
WITH tagged_tags AS (
	SELECT vt.tag_id,
	       tagged.id,
	       COALESCE(tagged.content_hash, '') AS content_hash,
	       COALESCE(tagged.sampled_sha256, '') AS sampled_sha256,
	       tagged.size_bytes,
	       COALESCE(tagged.file_name, '') AS file_name
	  FROM video_tags vt
	  JOIN videos tagged ON tagged.id = vt.video_id
	 WHERE COALESCE(tagged.hidden, 0) = 0
),
tag_candidates AS (
	SELECT tag_id, id AS video_id
	  FROM tagged_tags
	UNION ALL
	SELECT tag_id,
	       (SELECT canonical.id
	          FROM videos canonical
	         WHERE tagged_tags.content_hash != ''
	           AND canonical.content_hash = tagged_tags.content_hash
	           AND COALESCE(canonical.content_hash, '') != ''
	         ORDER BY canonical.created_at ASC, canonical.id ASC
	         LIMIT 1) AS video_id
	  FROM tagged_tags
	 WHERE content_hash != ''
	UNION ALL
	SELECT tag_id,
	       (SELECT canonical.id
	          FROM videos canonical
	         WHERE tagged_tags.sampled_sha256 != ''
	           AND tagged_tags.size_bytes > 0
	           AND canonical.sampled_sha256 = tagged_tags.sampled_sha256
	           AND canonical.size_bytes = tagged_tags.size_bytes
	           AND COALESCE(canonical.sampled_sha256, '') != ''
	           AND canonical.size_bytes > 0
	         ORDER BY canonical.created_at ASC, canonical.id ASC
	         LIMIT 1) AS video_id
	  FROM tagged_tags
	 WHERE sampled_sha256 != '' AND size_bytes > 0
	UNION ALL
	SELECT tag_id,
	       (SELECT canonical.id
	          FROM videos canonical
	         WHERE tagged_tags.file_name != ''
	           AND tagged_tags.size_bytes > 0
	           AND canonical.file_name = tagged_tags.file_name
	           AND canonical.size_bytes = tagged_tags.size_bytes
	           AND COALESCE(canonical.file_name, '') != ''
	           AND canonical.size_bytes > 0
	         ORDER BY canonical.created_at ASC, canonical.id ASC
	         LIMIT 1) AS video_id
	  FROM tagged_tags
	 WHERE file_name != '' AND size_bytes > 0
)
SELECT t.id,
       t.label,
       t.aliases,
       COALESCE(t.match_rules, '{}'),
       t.source,
       COUNT(DISTINCT videos.id) AS cnt,
       CASE
         WHEN COALESCE(t.origin, '') = 'crawler'
           OR EXISTS (
             SELECT 1
               FROM video_tags vt_origin
              WHERE vt_origin.tag_id = t.id
                AND lower(trim(COALESCE(vt_origin.source, ''))) = 'crawler'
           )
         THEN 1 ELSE 0
       END AS crawler_owned
FROM tags t
LEFT JOIN tag_candidates tc ON tc.tag_id = t.id AND tc.video_id IS NOT NULL
LEFT JOIN videos ON videos.id = tc.video_id
	AND COALESCE(videos.hidden, 0) = 0
	AND `+uniqueVideoWhereSQL+`
GROUP BY t.id, t.label, t.aliases, t.match_rules, t.source, t.origin
ORDER BY cnt DESC, t.label ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tag
	for rows.Next() {
		var tag Tag
		var aliasesJSON, rulesJSON string
		var crawlerOwned int
		if err := rows.Scan(&tag.ID, &tag.Label, &aliasesJSON, &rulesJSON, &tag.Source, &tag.Count, &crawlerOwned); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(aliasesJSON), &tag.Aliases)
		_ = json.Unmarshal([]byte(rulesJSON), &tag.MatchRules)
		tag.MatchRules = effectiveRule(tag.Label, tag.Aliases, tag.MatchRules)
		tag.CrawlerOwned = crawlerOwned != 0
		out = append(out, tag)
	}
	return out, nil
}

func videoMatchesTagLabelSQL(videoAlias string) string {
	return fmt.Sprintf(`%s.id IN (
			WITH tagged_videos AS (
				SELECT tagged.id,
				       COALESCE(tagged.content_hash, '') AS content_hash,
				       COALESCE(tagged.sampled_sha256, '') AS sampled_sha256,
				       tagged.size_bytes,
				       COALESCE(tagged.file_name, '') AS file_name
				  FROM video_tags vt
				  JOIN tags tag_filter ON tag_filter.id = vt.tag_id
				  JOIN videos tagged ON tagged.id = vt.video_id
				 WHERE tag_filter.label = ? COLLATE NOCASE
				   AND COALESCE(tagged.hidden, 0) = 0
			),
			tag_candidates AS (
				SELECT id AS video_id
				  FROM tagged_videos
				UNION ALL
				SELECT (SELECT canonical.id
				          FROM videos canonical
				         WHERE tagged_videos.content_hash != ''
				           AND canonical.content_hash = tagged_videos.content_hash
				           AND COALESCE(canonical.content_hash, '') != ''
				         ORDER BY canonical.created_at ASC, canonical.id ASC
				         LIMIT 1) AS video_id
				  FROM tagged_videos
				 WHERE content_hash != ''
				UNION ALL
				SELECT (SELECT canonical.id
				          FROM videos canonical
				         WHERE tagged_videos.sampled_sha256 != ''
				           AND tagged_videos.size_bytes > 0
				           AND canonical.sampled_sha256 = tagged_videos.sampled_sha256
				           AND canonical.size_bytes = tagged_videos.size_bytes
				           AND COALESCE(canonical.sampled_sha256, '') != ''
				           AND canonical.size_bytes > 0
				         ORDER BY canonical.created_at ASC, canonical.id ASC
				         LIMIT 1) AS video_id
				  FROM tagged_videos
				 WHERE sampled_sha256 != '' AND size_bytes > 0
				UNION ALL
				SELECT (SELECT canonical.id
				          FROM videos canonical
				         WHERE tagged_videos.file_name != ''
				           AND tagged_videos.size_bytes > 0
				           AND canonical.file_name = tagged_videos.file_name
				           AND canonical.size_bytes = tagged_videos.size_bytes
				           AND COALESCE(canonical.file_name, '') != ''
				           AND canonical.size_bytes > 0
				         ORDER BY canonical.created_at ASC, canonical.id ASC
				         LIMIT 1) AS video_id
				  FROM tagged_videos
				 WHERE file_name != '' AND size_bytes > 0
			)
			SELECT video_id
			  FROM tag_candidates
			 WHERE video_id IS NOT NULL
		)`, videoAlias)
}

func (c *Catalog) SetManualVideoTags(ctx context.Context, videoID string, labels []string) error {
	if _, err := c.GetVideo(ctx, videoID); err != nil {
		return err
	}
	return c.replaceVideoTags(ctx, videoID, labels, "manual", true, false)
}

// SetAutoVideoTags 用引擎结果覆盖视频的 auto/legacy 标签行；其余来源
// （crawler/series/propagated/manual）不受影响。
func (c *Catalog) SetAutoVideoTags(ctx context.Context, videoID string, labels []string) error {
	assignments := make([]TagAssignment, 0, len(labels))
	for _, label := range labels {
		assignments = append(assignments, TagAssignment{Label: label, Source: "auto"})
	}
	_, err := c.ReplaceAutoVideoTags(ctx, videoID, assignments)
	return err
}

// ---------- 匹配引擎入口 ----------

// Matcher 返回按当前标签池编译的匹配器；带版本号缓存，标签变更后自动重建。
func (c *Catalog) Matcher(ctx context.Context) (*tagging.Matcher, error) {
	version, err := c.tagRulesVersion(ctx)
	if err != nil {
		return nil, err
	}
	c.matcherMu.Lock()
	if c.matcher != nil && c.matcherVersion == version {
		m := c.matcher
		c.matcherMu.Unlock()
		return m, nil
	}
	c.matcherMu.Unlock()

	m, err := c.buildMatcher(ctx)
	if err != nil {
		return nil, err
	}
	c.matcherMu.Lock()
	c.matcher = m
	c.matcherVersion = version
	c.matcherMu.Unlock()
	return m, nil
}

func (c *Catalog) buildMatcher(ctx context.Context) (*tagging.Matcher, error) {
	avEnabled, err := c.avCodeMatchingEnabled(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT label, aliases, COALESCE(match_rules, '{}'), COALESCE(origin, '') FROM tags ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tagRules []tagging.TagRule
	for rows.Next() {
		var label, aliasesJSON, rulesJSON, origin string
		if err := rows.Scan(&label, &aliasesJSON, &rulesJSON, &origin); err != nil {
			return nil, err
		}
		origin = strings.ToLower(strings.TrimSpace(origin))
		if origin == avSeriesOrigin {
			continue
		}
		if !avEnabled && strings.EqualFold(label, avTagLabel) {
			continue
		}
		var aliases []string
		_ = json.Unmarshal([]byte(aliasesJSON), &aliases)
		var rule tagging.Rule
		_ = json.Unmarshal([]byte(rulesJSON), &rule)
		tagRules = append(tagRules, tagging.TagRule{Label: label, Rule: effectiveRule(label, aliases, rule)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tagging.NewMatcher(tagRules), nil
}

// effectiveRule 计算标签的生效规则：无显式规则时按 label+legacy aliases 兜底；
// 有显式规则时按规则本身执行。AV 标签例外：它只按番号规则识别，避免
// "AV/JAV/番号" 这类普通描述误触发。
func effectiveRule(label string, aliases []string, rule tagging.Rule) tagging.Rule {
	if strings.EqualFold(label, avTagLabel) {
		prefixes := append([]string{}, rule.AVCodePrefixes...)
		prefixes = append(prefixes, aliases...)
		prefixes = tagging.CleanAVCodePrefixes(prefixes)
		if len(prefixes) == 0 {
			return tagging.Rule{}
		}
		return tagging.Rule{MatchAVCode: true, AVCodePrefixes: prefixes}
	}
	if rule.IsEmpty() {
		return tagging.RuleFromAliases(label, aliases)
	}
	return rule
}

func (c *Catalog) tagRulesVersion(ctx context.Context) (int64, error) {
	raw, err := c.GetSetting(ctx, settingTagRulesVersion, "0")
	if err != nil {
		return 0, err
	}
	version, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, nil
	}
	return version, nil
}

func (c *Catalog) bumpTagRulesVersion(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at) VALUES (?, '1', ?)
ON CONFLICT(key) DO UPDATE SET
  value = CAST(CAST(settings.value AS INTEGER) + 1 AS TEXT),
  updated_at = excluded.updated_at`, settingTagRulesVersion, time.Now().UnixMilli())
	return err
}

// LookupTagLabel 查询某个标签是否已存在（大小写不敏感），返回库中的规范写法。
func (c *Catalog) LookupTagLabel(ctx context.Context, label string) (string, bool, error) {
	label = cleanTagLabel(label)
	if label == "" {
		return "", false, nil
	}
	tag, err := c.getTagByLabel(ctx, label)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return tag.Label, true, nil
}

// MatchTags 对一段文本运行标签匹配，返回命中的标签名。
func (c *Catalog) MatchTags(ctx context.Context, text string) ([]string, error) {
	matcher, err := c.Matcher(ctx)
	if err != nil {
		return nil, err
	}
	return matcher.MatchLabels(text), nil
}

// MatchTagAssignments matches video metadata against the existing tag pool.
// The only tag definition it may create is an AV series label such as FC2PPV,
// and only while the built-in AV mechanism is enabled.
func (c *Catalog) MatchTagAssignments(ctx context.Context, title, fileName, author, dirName string) ([]TagAssignment, error) {
	matcher, err := c.Matcher(ctx)
	if err != nil {
		return nil, err
	}
	return c.matchTagAssignmentsWithMatcher(ctx, matcher, title, fileName, author, dirName)
}

func (c *Catalog) matchTagAssignmentsWithMatcher(ctx context.Context, matcher *tagging.Matcher, title, fileName, author, dirName string) ([]TagAssignment, error) {
	matches := matcher.Match(matchFields(title, fileName, author, dirName)...)
	out := make([]TagAssignment, 0, len(matches))
	seen := map[string]struct{}{}
	for _, m := range matches {
		seen[strings.ToLower(strings.TrimSpace(m.Label))] = struct{}{}
		out = append(out, TagAssignment{Label: m.Label, Source: "auto", Evidence: m.Evidence()})
	}
	series, evidence, err := c.matchAVSeriesAssignment(ctx, title, fileName, author, dirName)
	if err != nil {
		return nil, err
	}
	if series != "" {
		key := strings.ToLower(series)
		if _, ok := seen[key]; !ok {
			out = append(out, TagAssignment{Label: series, Source: "auto", Evidence: evidence})
		}
	}
	return out, nil
}

func (c *Catalog) matchAVSeriesAssignment(ctx context.Context, title, fileName, author, dirName string) (string, string, error) {
	enabled, err := c.avCodeMatchingEnabled(ctx)
	if err != nil || !enabled {
		return "", "", err
	}
	avCodes, err := c.avCodeMatcher(ctx)
	if err != nil {
		return "", "", err
	}
	for _, field := range matchFields(title, fileName, author, dirName) {
		code := avCodes.Find(field.Text)
		if code == "" {
			continue
		}
		series := avCodes.SeriesOf(code)
		if series == "" {
			continue
		}
		tag, err := c.ensureAVSeriesTag(ctx, series)
		if err != nil {
			return "", "", err
		}
		evidence := code
		if field.Name != "" {
			evidence = field.Name + ":" + code
		}
		return tag.Label, evidence, nil
	}
	return "", "", nil
}

func (c *Catalog) avCodeMatcher(ctx context.Context) (*tagging.AVCodeMatcher, error) {
	tag, err := c.getTagByLabel(ctx, avTagLabel)
	if err != nil {
		return nil, err
	}
	rule := effectiveRule(avTagLabel, tag.Aliases, tag.MatchRules)
	return tagging.NewAVCodeMatcher(rule.AVCodePrefixes), nil
}

func (c *Catalog) ensureTag(ctx context.Context, label string, aliases []string, source string) (Tag, error) {
	return c.ensureTagWithRules(ctx, label, aliases, tagging.Rule{}, source)
}

// EnsureTag ensures that a tag exists and returns its canonical database row.
func (c *Catalog) EnsureTag(ctx context.Context, label, source string) (Tag, error) {
	return c.ensureTag(ctx, label, nil, source)
}

// EnsureCrawlerTag ensures the crawler ownership tag exists. Crawler tags are
// source provenance, so they are not blocked by tags.auto_generate_enabled.
func (c *Catalog) EnsureCrawlerTag(ctx context.Context, label string) (Tag, error) {
	label = cleanTagLabel(label)
	tag, err := c.ensureTagWithRulesInternal(ctx, label, nil, tagging.Rule{}, "generated", false)
	if err != nil {
		return Tag{}, err
	}
	if err := c.markTagOrigin(ctx, tag.ID, "crawler"); err != nil {
		return Tag{}, err
	}
	tag.CrawlerOwned = true
	return tag, nil
}

func (c *Catalog) markTagOrigin(ctx context.Context, tagID int64, origin string) error {
	origin = strings.TrimSpace(strings.ToLower(origin))
	if origin != "crawler" && origin != avSeriesOrigin {
		origin = ""
	}
	res, err := c.db.ExecContext(ctx, `
UPDATE tags
   SET origin = ?, updated_at = ?
 WHERE id = ?
   AND COALESCE(origin, '') != ?`, origin, time.Now().UnixMilli(), tagID, origin)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return c.bumpTagRulesVersion(ctx)
	}
	return nil
}

// EnsureCrawlerTagForVideo ensures a single crawler-owned video carries its
// crawler provenance tag. Unlike ordinary auto tags, this bypasses the
// auto-generation setting and does not skip manually curated videos.
func (c *Catalog) EnsureCrawlerTagForVideo(ctx context.Context, videoID, label string) (bool, error) {
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return false, errors.New("video id is required")
	}
	tag, err := c.EnsureCrawlerTag(ctx, label)
	if err != nil {
		return false, err
	}
	changed, labelAdded, err := c.upsertVideoTagAssignment(ctx, videoID, tag.ID, "crawler", "爬虫:"+tag.Label)
	if err != nil {
		return false, err
	}
	if labelAdded {
		if err := c.syncVideoTagsJSON(ctx, videoID, c.hasManualTags(ctx, videoID)); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// ensureTagWithRules 建标签（存在则复用）。规则只在两种情况下写入：
// 新建时、或已有行的 match_rules 为空时（升级回填）；不会覆盖管理员显式改过的规则。
func (c *Catalog) ensureTagWithRules(ctx context.Context, label string, aliases []string, rule tagging.Rule, source string) (Tag, error) {
	return c.ensureTagWithRulesInternal(ctx, label, aliases, rule, source, true)
}

func (c *Catalog) ensureTagWithRulesInternal(ctx context.Context, label string, aliases []string, rule tagging.Rule, source string, respectAutoGenerateSetting bool) (Tag, error) {
	label = cleanTagLabel(label)
	if label == "" {
		return Tag{}, errors.New("tag label is required")
	}
	if isAVCodePollutedLabel(label) {
		label = avTagLabel
		aliases = fixedtags.AliasesFor(avTagLabel)
		rule = avTagRule
		source = fixedtags.SourceBuiltin
	}
	if source == "" {
		source = "user"
	}
	source = normalizeTagSource(source)
	if source == "builtin" && !fixedtags.IsBuiltinLabel(label) {
		source = "generated"
	}
	if source == "generated" {
		tag, err := c.getTagByLabel(ctx, label)
		if err == nil {
			return tag, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Tag{}, err
		}
		if respectAutoGenerateSetting {
			enabled, err := c.AutoGenerateTagsEnabled(ctx)
			if err != nil {
				return Tag{}, err
			}
			if !enabled {
				return Tag{}, ErrAutoTagGenerationDisabled
			}
		}
	}
	aliases = cleanAliases(aliases, label)
	aliasesJSON, _ := json.Marshal(aliases)
	rulesJSON, _ := json.Marshal(rule)
	now := time.Now().UnixMilli()
	res, err := c.db.ExecContext(ctx, `
INSERT OR IGNORE INTO tags (label, aliases, match_rules, source, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)`, label, string(aliasesJSON), string(rulesJSON), source, now, now)
	if err != nil {
		return Tag{}, err
	}
	inserted := false
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		inserted = true
	}
	changed := inserted
	if !inserted {
		if source == fixedtags.SourceBuiltin {
			res, err := c.db.ExecContext(ctx, `
UPDATE tags
   SET source = ?, updated_at = ?
 WHERE label = ? COLLATE NOCASE
   AND source != ?
   AND source != 'user'`,
				source, now, label, source)
			if err != nil {
				return Tag{}, err
			}
			if n, err := res.RowsAffected(); err == nil && n > 0 {
				changed = true
			}
		}
		if strings.EqualFold(label, avTagLabel) && source == fixedtags.SourceBuiltin {
			current, err := c.getTagByLabel(ctx, label)
			if err != nil {
				return Tag{}, err
			}
			legacyMissingPrefixes := current.MatchRules.IsEmpty() ||
				(current.MatchRules.MatchAVCode && len(current.MatchRules.AVCodePrefixes) == 0)
			if legacyMissingPrefixes {
				res, err := c.db.ExecContext(ctx, `
UPDATE tags
   SET match_rules = ?, updated_at = ?
 WHERE label = ? COLLATE NOCASE`,
					string(rulesJSON), now, label)
				if err != nil {
					return Tag{}, err
				}
				if n, err := res.RowsAffected(); err == nil && n > 0 {
					changed = true
				}
			}
		}
		if len(aliases) > 0 {
			res, err := c.db.ExecContext(ctx,
				`UPDATE tags SET aliases = ?, updated_at = ? WHERE label = ? COLLATE NOCASE AND COALESCE(aliases, '[]') != ?`,
				string(aliasesJSON), now, label, string(aliasesJSON))
			if err != nil {
				return Tag{}, err
			}
			if n, err := res.RowsAffected(); err == nil && n > 0 {
				changed = true
			}
		}
		if !rule.IsEmpty() {
			// 升级回填：已有行没有显式规则时补上默认规则。
			res, err := c.db.ExecContext(ctx, `
UPDATE tags SET match_rules = ?, updated_at = ?
 WHERE label = ? COLLATE NOCASE
   AND COALESCE(match_rules, '{}') IN ('', '{}', 'null')`,
				string(rulesJSON), now, label)
			if err != nil {
				return Tag{}, err
			}
			if n, err := res.RowsAffected(); err == nil && n > 0 {
				changed = true
			}
		}
	}
	if changed {
		if err := c.bumpTagRulesVersion(ctx); err != nil {
			return Tag{}, err
		}
	}
	if strings.EqualFold(label, avTagLabel) && (source == fixedtags.SourceBuiltin || source == "user") {
		if err := c.setAVCodeMatchingDisabled(ctx, false); err != nil {
			return Tag{}, err
		}
	}
	return c.getTagByLabel(ctx, label)
}

func (c *Catalog) ensureAVSeriesTag(ctx context.Context, series string) (Tag, error) {
	series = strings.ToUpper(cleanTagLabel(series))
	if series == "" {
		return Tag{}, errors.New("AV series tag label is required")
	}
	tag, err := c.ensureTagWithRulesInternal(ctx, series, nil, tagging.Rule{Keywords: []string{series}}, "generated", false)
	if err != nil {
		return Tag{}, err
	}
	if tag.Source == "generated" {
		if err := c.markTagOrigin(ctx, tag.ID, avSeriesOrigin); err != nil {
			return Tag{}, err
		}
	}
	return c.getTagByLabel(ctx, series)
}

func (c *Catalog) avCodeMatchingEnabled(ctx context.Context) (bool, error) {
	disabled, err := c.avCodeMatchingDisabled(ctx)
	if err != nil || disabled {
		return false, err
	}
	if _, err := c.getTagByLabel(ctx, avTagLabel); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

func (c *Catalog) avCodeMatchingDisabled(ctx context.Context) (bool, error) {
	raw, err := c.GetSetting(ctx, settingAVCodeMatchingDisabled, "false")
	if err != nil {
		return false, err
	}
	return parseSettingBool(raw, false), nil
}

func (c *Catalog) setAVCodeMatchingDisabled(ctx context.Context, disabled bool) error {
	current, err := c.avCodeMatchingDisabled(ctx)
	if err != nil {
		return err
	}
	if current == disabled {
		return nil
	}
	value := "false"
	if disabled {
		value = "true"
	}
	if err := c.SetSetting(ctx, settingAVCodeMatchingDisabled, value); err != nil {
		return err
	}
	return c.bumpTagRulesVersion(ctx)
}

func setAVCodeMatchingDisabledTx(ctx context.Context, tx *sql.Tx, disabled bool) error {
	value := "false"
	if disabled {
		value = "true"
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
  value = excluded.value,
  updated_at = excluded.updated_at`, settingAVCodeMatchingDisabled, value, time.Now().UnixMilli())
	return err
}

const tagSelectCols = `id, label, aliases, COALESCE(match_rules, '{}'), source, 0`

func (c *Catalog) getTagByLabel(ctx context.Context, label string) (Tag, error) {
	row := c.db.QueryRowContext(ctx,
		`SELECT `+tagSelectCols+` FROM tags WHERE label = ? COLLATE NOCASE`,
		label)
	return scanTag(row)
}

func (c *Catalog) getTagByID(ctx context.Context, id int64) (Tag, error) {
	row := c.db.QueryRowContext(ctx,
		`SELECT `+tagSelectCols+` FROM tags WHERE id = ?`,
		id)
	return scanTag(row)
}

// classifyTag 用单个标签的规则对全库做"只增"分类（新建/编辑标签后调用）。
func (c *Catalog) classifyTag(ctx context.Context, tag Tag) (int, error) {
	matcher := tagging.NewMatcher([]tagging.TagRule{
		{Label: tag.Label, Rule: effectiveRule(tag.Label, tag.Aliases, tag.MatchRules)},
	})
	rows, err := c.db.QueryContext(ctx, `
SELECT id, title, COALESCE(author, ''), COALESCE(file_name, ''), COALESCE(dir_name, ''), COALESCE(tags_manual, 0)
FROM videos`)
	if err != nil {
		return 0, err
	}

	type hit struct {
		videoID  string
		evidence string
	}
	var hits []hit
	for rows.Next() {
		var videoID, title, author, fileName, dirName string
		var manual int
		if err := rows.Scan(&videoID, &title, &author, &fileName, &dirName, &manual); err != nil {
			rows.Close()
			return 0, err
		}
		if manual == 1 {
			continue
		}
		matches := matcher.Match(matchFields(title, fileName, author, dirName)...)
		if len(matches) == 0 {
			continue
		}
		hits = append(hits, hit{videoID: videoID, evidence: matches[0].Evidence()})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	changedCount := 0
	for _, h := range hits {
		changed, labelAdded, err := c.upsertVideoTagAssignment(ctx, h.videoID, tag.ID, "auto", h.evidence)
		if err != nil {
			return 0, err
		}
		if changed {
			changedCount++
			if labelAdded {
				if err := c.syncVideoTagsJSON(ctx, h.videoID, false); err != nil {
					return 0, err
				}
			}
		}
	}
	return changedCount, nil
}

func (c *Catalog) replaceVideoTags(ctx context.Context, videoID string, labels []string, source string, manual bool, createMissing bool) error {
	labels = uniqueStrings(cleanLabels(labels))
	if createMissing {
		ensureSource := "legacy"
		if source == "manual" {
			ensureSource = "user"
		}
		for _, label := range labels {
			if _, err := c.ensureTag(ctx, label, nil, ensureSource); err != nil {
				return err
			}
		}
	} else {
		if err := c.validateTagsExist(ctx, labels); err != nil {
			return err
		}
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM video_tags WHERE video_id = ?`, videoID); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, label := range labels {
		tag, err := c.getTagByLabelTx(ctx, tx, label)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO video_tags (video_id, tag_id, source, evidence, created_at) VALUES (?, ?, ?, '', ?)`,
			videoID, tag.ID, source, now); err != nil {
			return err
		}
	}
	manualValue := 0
	if manual {
		manualValue = 1
	}
	if _, err := tx.ExecContext(ctx, `UPDATE videos SET tags_manual = ? WHERE id = ?`, manualValue, videoID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return c.syncVideoTagsJSON(ctx, videoID, manual)
}

// ReplaceAutoVideoTags 用给定分配覆盖视频的引擎标签（source IN auto/legacy），
// 其它来源的行保留。人工锁定视频直接跳过。返回是否发生了变更。
func (c *Catalog) ReplaceAutoVideoTags(ctx context.Context, videoID string, assignments []TagAssignment) (bool, error) {
	if c.hasManualTags(ctx, videoID) {
		return false, nil
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	changed, err := replaceAutoVideoTagsTx(ctx, tx, videoID, assignments)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	if err := syncVideoTagsJSONTx(ctx, tx, videoID, false); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

type videoTagAssignmentRow struct {
	tagID    int64
	label    string
	source   string
	evidence string
}

type desiredVideoTagAssignment struct {
	tagID    int64
	label    string
	source   string
	evidence string
}

// replaceAutoVideoTagsTx 是 ReplaceAutoVideoTags 的事务内实现，供批量重算复用。
// 返回是否有实际变更（用于跳过无谓的 JSON 同步）。
func replaceAutoVideoTagsTx(ctx context.Context, tx *sql.Tx, videoID string, assignments []TagAssignment) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT t.id, t.label, COALESCE(vt.source, ''), COALESCE(vt.evidence, '')
  FROM video_tags vt
  JOIN tags t ON t.id = vt.tag_id
 WHERE vt.video_id = ?`, videoID)
	if err != nil {
		return false, err
	}
	current := map[string]videoTagAssignmentRow{}
	for rows.Next() {
		var row videoTagAssignmentRow
		if err := rows.Scan(&row.tagID, &row.label, &row.source, &row.evidence); err != nil {
			rows.Close()
			return false, err
		}
		current[strings.ToLower(row.label)] = row
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}

	desired := map[string]desiredVideoTagAssignment{}
	for _, a := range assignments {
		label := cleanTagLabel(a.Label)
		if label == "" {
			continue
		}
		tag, err := getTagByLabelTxRaw(ctx, tx, label)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return false, err
		}
		source := normalizeVideoTagSource(a.Source)
		desired[strings.ToLower(tag.Label)] = desiredVideoTagAssignment{
			tagID:    tag.ID,
			label:    tag.Label,
			source:   source,
			evidence: a.Evidence,
		}
	}

	changed := false
	now := time.Now().UnixMilli()
	for key, existing := range current {
		if _, ok := desired[key]; ok {
			continue
		}
		if existing.source != "auto" && existing.source != "legacy" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM video_tags WHERE video_id = ? AND tag_id = ? AND source IN ('auto', 'legacy')`,
			videoID, existing.tagID); err != nil {
			return false, err
		}
		changed = true
	}

	for key, desiredRow := range desired {
		if existing, ok := current[key]; ok {
			if !shouldReplaceVideoTagAssignment(existing.source, desiredRow.source) {
				continue
			}
			evidence := desiredRow.evidence
			if evidence == "" {
				evidence = existing.evidence
			}
			if normalizeVideoTagSource(existing.source) == desiredRow.source && existing.evidence == evidence {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE video_tags SET source = ?, evidence = ? WHERE video_id = ? AND tag_id = ?`,
				desiredRow.source, evidence, videoID, desiredRow.tagID); err != nil {
				return false, err
			}
			changed = true
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO video_tags (video_id, tag_id, source, evidence, created_at) VALUES (?, ?, ?, ?, ?)`,
			videoID, desiredRow.tagID, desiredRow.source, desiredRow.evidence, now); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

// AddVideoTagAssignments 给视频追加标签（series/propagated/crawler 等来源）。
// 只挂已存在的标签；人工锁定视频跳过。返回实际新增或来源/证据更新数。
func (c *Catalog) AddVideoTagAssignments(ctx context.Context, videoID string, assignments []TagAssignment) (int, error) {
	if len(assignments) == 0 {
		return 0, nil
	}
	if c.hasManualTags(ctx, videoID) {
		return 0, nil
	}
	changedCount := 0
	labelAdded := false
	for _, a := range assignments {
		label := cleanTagLabel(a.Label)
		if label == "" {
			continue
		}
		tag, err := c.getTagByLabel(ctx, label)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return changedCount, err
		}
		source := normalizeVideoTagSource(a.Source)
		changed, labelWasAdded, err := c.upsertVideoTagAssignment(ctx, videoID, tag.ID, source, a.Evidence)
		if err != nil {
			return changedCount, err
		}
		if changed {
			changedCount++
		}
		if labelWasAdded {
			labelAdded = true
		}
	}
	if labelAdded {
		if err := c.syncVideoTagsJSON(ctx, videoID, false); err != nil {
			return changedCount, err
		}
	}
	return changedCount, nil
}

func (c *Catalog) upsertVideoTagAssignment(ctx context.Context, videoID string, tagID int64, source, evidence string) (bool, bool, error) {
	source = normalizeVideoTagSource(source)
	var existingSource, existingEvidence string
	err := c.db.QueryRowContext(ctx,
		`SELECT COALESCE(source, ''), COALESCE(evidence, '') FROM video_tags WHERE video_id = ? AND tag_id = ?`,
		videoID, tagID).Scan(&existingSource, &existingEvidence)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := c.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO video_tags (video_id, tag_id, source, evidence, created_at) VALUES (?, ?, ?, ?, ?)`,
			videoID, tagID, source, evidence, time.Now().UnixMilli())
		if err != nil {
			return false, false, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return true, true, nil
		}
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !shouldReplaceVideoTagAssignment(existingSource, source) {
		return false, false, nil
	}
	if evidence == "" {
		evidence = existingEvidence
	}
	if normalizeVideoTagSource(existingSource) == source && existingEvidence == evidence {
		return false, false, nil
	}
	_, err = c.db.ExecContext(ctx,
		`UPDATE video_tags SET source = ?, evidence = ? WHERE video_id = ? AND tag_id = ?`,
		source, evidence, videoID, tagID)
	if err != nil {
		return false, false, err
	}
	return true, false, nil
}

func normalizeVideoTagSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "manual":
		return "manual"
	case "crawler":
		return "crawler"
	case "series":
		return "series"
	case "propagated":
		return "propagated"
	case "legacy":
		return "legacy"
	case "auto", "":
		return "auto"
	default:
		return "auto"
	}
}

func shouldReplaceVideoTagAssignment(existingSource, incomingSource string) bool {
	existingSource = normalizeVideoTagSource(existingSource)
	incomingSource = normalizeVideoTagSource(incomingSource)
	if existingSource == incomingSource {
		return true
	}
	return videoTagAssignmentPriority(incomingSource) > videoTagAssignmentPriority(existingSource)
}

func videoTagAssignmentPriority(source string) int {
	switch normalizeVideoTagSource(source) {
	case "manual":
		return 100
	case "crawler":
		return 90
	case "series":
		return 80
	case "auto":
		return 60
	case "propagated":
		return 50
	case "legacy":
		return 40
	default:
		return 0
	}
}

// ListVideoTagMetadata returns assignment source/evidence for the requested
// videos in one query. Keys are video ID, then canonical tag label.
func (c *Catalog) ListVideoTagMetadata(ctx context.Context, videoIDs []string) (map[string]map[string]VideoTagMetadata, error) {
	out := make(map[string]map[string]VideoTagMetadata)
	seen := make(map[string]bool, len(videoIDs))
	args := make([]any, 0, len(videoIDs))
	for _, videoID := range videoIDs {
		videoID = strings.TrimSpace(videoID)
		if videoID == "" || seen[videoID] {
			continue
		}
		seen[videoID] = true
		args = append(args, videoID)
	}
	if len(args) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
	rows, err := c.db.QueryContext(ctx, `
SELECT vt.video_id, t.label, COALESCE(vt.source, ''), COALESCE(vt.evidence, '')
  FROM video_tags vt
  JOIN tags t ON t.id = vt.tag_id
 WHERE vt.video_id IN (`+placeholders+`)
 ORDER BY vt.video_id, t.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var videoID, label string
		var metadata VideoTagMetadata
		if err := rows.Scan(&videoID, &label, &metadata.Source, &metadata.Evidence); err != nil {
			return nil, err
		}
		if out[videoID] == nil {
			out[videoID] = make(map[string]VideoTagMetadata)
		}
		out[videoID][label] = metadata
	}
	return out, rows.Err()
}

func (c *Catalog) addVideoTags(ctx context.Context, videoID string, labels []string, source string, createMissing bool) (bool, error) {
	labels = uniqueStrings(cleanLabels(labels))
	changed := false
	for _, label := range labels {
		added, err := c.addVideoTag(ctx, videoID, label, source, createMissing)
		if err != nil {
			return false, err
		}
		if added {
			changed = true
		}
	}
	return changed, nil
}

func (c *Catalog) addVideoTag(ctx context.Context, videoID, label, source string, createMissing bool) (bool, error) {
	if createMissing {
		ensureSource := "legacy"
		if source == "manual" {
			ensureSource = "user"
		}
		if _, err := c.ensureTag(ctx, label, nil, ensureSource); err != nil {
			return false, err
		}
	}
	tag, err := c.getTagByLabel(ctx, label)
	if err != nil {
		return false, err
	}
	now := time.Now().UnixMilli()
	res, err := c.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO video_tags (video_id, tag_id, source, evidence, created_at) VALUES (?, ?, ?, '', ?)`,
		videoID, tag.ID, source, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (c *Catalog) insertVideoTag(ctx context.Context, videoID string, tagID int64, source, evidence string) error {
	_, err := c.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO video_tags (video_id, tag_id, source, evidence, created_at) VALUES (?, ?, ?, ?, ?)`,
		videoID, tagID, source, evidence, time.Now().UnixMilli())
	return err
}

func (c *Catalog) collapseAVCodeTags(ctx context.Context) error {
	disabled, err := c.avCodeMatchingDisabled(ctx)
	if err != nil || disabled {
		return err
	}
	if _, err := c.ensureTagWithRules(ctx, avTagLabel, fixedtags.AliasesFor(avTagLabel), avTagRule, fixedtags.SourceBuiltin); err != nil {
		return err
	}
	if err := c.removeAVLegacyAliases(ctx); err != nil {
		return err
	}

	rows, err := c.db.QueryContext(ctx, `SELECT id, label FROM tags`)
	if err != nil {
		return err
	}

	type pollutedTag struct {
		id    int64
		label string
	}
	var polluted []pollutedTag
	for rows.Next() {
		var tag pollutedTag
		if err := rows.Scan(&tag.id, &tag.label); err != nil {
			return err
		}
		if strings.EqualFold(tag.label, avTagLabel) || !isAVCodePollutedLabel(tag.label) {
			continue
		}
		polluted = append(polluted, tag)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, tag := range polluted {
		videoIDs, err := c.videoIDsForTagID(ctx, tag.id)
		if err != nil {
			return err
		}
		for _, videoID := range videoIDs {
			if _, err := c.addVideoTag(ctx, videoID, avTagLabel, "auto", false); err != nil {
				return err
			}
		}
		if _, err := c.db.ExecContext(ctx, `DELETE FROM video_tags WHERE tag_id = ?`, tag.id); err != nil {
			return err
		}
		if _, err := c.db.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, tag.id); err != nil {
			return err
		}
		for _, videoID := range videoIDs {
			if err := c.syncVideoTagsJSON(ctx, videoID, c.hasManualTags(ctx, videoID)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Catalog) removeAVLegacyAliases(ctx context.Context) error {
	tag, err := c.getTagByLabel(ctx, avTagLabel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	aliases := tag.Aliases
	if len(aliases) == 0 {
		return nil
	}
	filtered := aliases[:0]
	removed := false
	for _, alias := range aliases {
		if _, ok := avLegacyAliases[strings.ToLower(strings.TrimSpace(alias))]; ok {
			removed = true
			continue
		}
		filtered = append(filtered, alias)
	}
	if !removed {
		return nil
	}
	aliasesJSON, _ := json.Marshal(filtered)
	res, err := c.db.ExecContext(ctx,
		`UPDATE tags SET aliases = ?, updated_at = ? WHERE id = ?`,
		string(aliasesJSON), time.Now().UnixMilli(), tag.ID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return c.bumpTagRulesVersion(ctx)
	}
	return nil
}

func (c *Catalog) videoIDsForTagID(ctx context.Context, tagID int64) ([]string, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT video_id FROM video_tags WHERE tag_id = ?`, tagID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var videoIDs []string
	for rows.Next() {
		var videoID string
		if err := rows.Scan(&videoID); err != nil {
			return nil, err
		}
		videoIDs = append(videoIDs, videoID)
	}
	return videoIDs, rows.Err()
}

func (c *Catalog) videoIDSetForTagID(ctx context.Context, tagID int64) (map[string]bool, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT video_id FROM video_tags WHERE tag_id = ?`, tagID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var videoID string
		if err := rows.Scan(&videoID); err != nil {
			return nil, err
		}
		out[videoID] = true
	}
	return out, rows.Err()
}

func (c *Catalog) validateTagsExist(ctx context.Context, labels []string) error {
	for _, label := range labels {
		if _, err := c.getTagByLabel(ctx, label); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrUnknownTag, label)
			}
			return err
		}
	}
	return nil
}

func (c *Catalog) syncVideoTagsJSON(ctx context.Context, videoID string, manual bool) error {
	rows, err := c.db.QueryContext(ctx, `
SELECT t.label
FROM video_tags vt
JOIN tags t ON t.id = vt.tag_id
WHERE vt.video_id = ?
ORDER BY t.id ASC`, videoID)
	if err != nil {
		return err
	}
	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return err
		}
		labels = append(labels, label)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	labelsJSON, _ := json.Marshal(labels)
	manualValue := 0
	if manual {
		manualValue = 1
	}
	_, err = c.db.ExecContext(ctx,
		`UPDATE videos SET tags = ?, tags_manual = ?, updated_at = ? WHERE id = ?`,
		string(labelsJSON), manualValue, time.Now().UnixMilli(), videoID)
	return err
}

func (c *Catalog) hasManualTags(ctx context.Context, videoID string) bool {
	var manual int
	err := c.db.QueryRowContext(ctx, `SELECT COALESCE(tags_manual, 0) FROM videos WHERE id = ?`, videoID).Scan(&manual)
	return err == nil && manual == 1
}

func (c *Catalog) videoExists(ctx context.Context, videoID string) bool {
	var exists int
	err := c.db.QueryRowContext(ctx, `SELECT 1 FROM videos WHERE id = ?`, videoID).Scan(&exists)
	return err == nil
}

func (c *Catalog) tagExists(ctx context.Context, label string) bool {
	var exists int
	err := c.db.QueryRowContext(ctx, `SELECT 1 FROM tags WHERE label = ? COLLATE NOCASE`, label).Scan(&exists)
	return err == nil
}

func (c *Catalog) getTagByLabelTx(ctx context.Context, tx *sql.Tx, label string) (Tag, error) {
	return getTagByLabelTxRaw(ctx, tx, label)
}

func getTagByLabelTxRaw(ctx context.Context, tx *sql.Tx, label string) (Tag, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+tagSelectCols+` FROM tags WHERE label = ? COLLATE NOCASE`,
		label)
	return scanTag(row)
}

func (c *Catalog) getTagByIDTx(ctx context.Context, tx *sql.Tx, id int64) (Tag, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+tagSelectCols+` FROM tags WHERE id = ?`,
		id)
	return scanTag(row)
}

func hasManualTagsTx(ctx context.Context, tx *sql.Tx, videoID string) bool {
	var manual int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(tags_manual, 0) FROM videos WHERE id = ?`, videoID).Scan(&manual)
	return err == nil && manual == 1
}

func syncVideoTagsJSONTx(ctx context.Context, tx *sql.Tx, videoID string, manual bool) error {
	rows, err := tx.QueryContext(ctx, `
SELECT t.label
FROM video_tags vt
JOIN tags t ON t.id = vt.tag_id
WHERE vt.video_id = ?
ORDER BY t.id ASC`, videoID)
	if err != nil {
		return err
	}
	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			rows.Close()
			return err
		}
		labels = append(labels, label)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	labelsJSON, _ := json.Marshal(labels)
	manualValue := 0
	if manual {
		manualValue = 1
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE videos SET tags = ?, tags_manual = ?, updated_at = ? WHERE id = ?`,
		string(labelsJSON), manualValue, time.Now().UnixMilli(), videoID)
	return err
}

type tagRowScanner interface {
	Scan(dest ...any) error
}

func scanTag(row tagRowScanner) (Tag, error) {
	var tag Tag
	var aliasesJSON, rulesJSON string
	if err := row.Scan(&tag.ID, &tag.Label, &aliasesJSON, &rulesJSON, &tag.Source, &tag.Count); err != nil {
		return Tag{}, err
	}
	_ = json.Unmarshal([]byte(aliasesJSON), &tag.Aliases)
	_ = json.Unmarshal([]byte(rulesJSON), &tag.MatchRules)
	return tag, nil
}

// IsAVCode / ContainsAVCode 委托给 tagging 包（历史实现已迁移）。
func IsAVCode(label string) bool {
	return tagging.IsAVCode(cleanTagLabel(label))
}

func ContainsAVCode(text string) bool {
	return tagging.ContainsAVCode(text)
}

func isAVCodePollutedLabel(label string) bool {
	label = cleanTagLabel(label)
	if label == "" {
		return false
	}
	return tagging.IsAVCode(label) || tagging.ContainsAVCode(label)
}

func cleanLabels(labels []string) []string {
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		label = cleanTagLabel(label)
		if label != "" {
			if isAVCodePollutedLabel(label) {
				label = avTagLabel
			}
			out = append(out, label)
		}
	}
	return out
}

func cleanTagLabel(label string) string {
	return strings.TrimSpace(label)
}

func cleanTagRule(rule tagging.Rule) tagging.Rule {
	return tagging.Rule{
		Keywords: cleanRuleTerms(rule.Keywords),
	}
}

func cleanStoredTagRule(rule tagging.Rule) tagging.Rule {
	return tagging.Rule{
		Keywords:       cleanRuleTerms(rule.Keywords),
		MatchAVCode:    rule.MatchAVCode,
		AVCodePrefixes: tagging.CleanAVCodePrefixes(rule.AVCodePrefixes),
	}
}

func cleanRuleTerms(terms []string) []string {
	out := make([]string, 0, len(terms))
	seen := map[string]struct{}{}
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		key := strings.ToLower(term)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, term)
	}
	return out
}

// normalizeTagSource 只用于 tags.source。video_tags.source 是标签挂载来源，
// 需要继续保留 auto/manual/crawler/series/propagated/legacy 等细分值。
func normalizeTagSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "system", "builtin":
		return "builtin"
	case "user":
		return "user"
	default:
		return "generated"
	}
}

func parseSettingBool(value string, defaultValue bool) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on", "enabled":
		return true
	case "0", "false", "no", "n", "off", "disabled":
		return false
	default:
		return defaultValue
	}
}

func cleanAliases(aliases []string, label string) []string {
	out := make([]string, 0, len(aliases))
	seen := map[string]bool{strings.ToLower(label): true}
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		key := strings.ToLower(alias)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, alias)
	}
	return out
}

func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

// pruneOrphanGeneratedTagsByID 在事务里检查并删除不再被引用的自动生成标签。
func pruneOrphanGeneratedTagsByID(ctx context.Context, tx *sql.Tx, tagIDs []int64) error {
	for _, tagID := range tagIDs {
		var src string
		err := tx.QueryRowContext(ctx, `SELECT source FROM tags WHERE id = ?`, tagID).Scan(&src)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if normalizeTagSource(src) != "generated" {
			continue
		}
		var refCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_tags WHERE tag_id = ?`, tagID).Scan(&refCount); err != nil {
			return err
		}
		if refCount > 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, tagID); err != nil {
			return err
		}
	}
	return nil
}

// collectVideoTagIDs 在事务里读出当前视频关联的 tag_id，供后续清理判断。
func collectVideoTagIDs(ctx context.Context, tx *sql.Tx, videoID string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT tag_id FROM video_tags WHERE video_id = ?`, videoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
