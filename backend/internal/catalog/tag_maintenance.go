package catalog

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/video-site/backend/internal/tagging"
)

// 本文件是标签维护相关的批量操作：全库重算、系列标签同步、同类传播、
// 零引用标签清理。全部按"人工锁定视频（tags_manual=1）不动"的约定实现。

// retagVideoRow 是重算时读取的最小视频行。
type retagVideoRow struct {
	id          string
	title       string
	author      string
	fileName    string
	dirName     string
	manual      bool
	assignments []TagAssignment
}

type TagStateResetResult struct {
	RemovedAssignments int
	RemovedTags        int
}

func (c *Catalog) CountVideosForRetag(ctx context.Context, sinceMs int64) (int, error) {
	query := `SELECT COUNT(*) FROM videos`
	var args []any
	if sinceMs > 0 {
		query += ` WHERE updated_at >= ?`
		args = append(args, sinceMs)
	}
	var count int
	err := c.db.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}

// RetagVideosBatch recalculates engine-managed assignments for one page of
// videos using the existing tag matcher. It may create AV series labels while
// the built-in AV matching mechanism is enabled; other automatic label
// generation remains disabled.
// 返回 (本批处理数, 最后一个 id, 是否已到结尾)。
func (c *Catalog) RetagVideosBatch(ctx context.Context, matcher *tagging.Matcher, afterID string, limit int, sinceMs int64) (int, string, bool, error) {
	if limit <= 0 {
		limit = 500
	}
	query := `
SELECT id, title, COALESCE(author, ''), COALESCE(file_name, ''), COALESCE(dir_name, ''), COALESCE(tags_manual, 0)
  FROM videos
 WHERE id > ?`
	args := []any{afterID}
	if sinceMs > 0 {
		query += ` AND updated_at >= ?`
		args = append(args, sinceMs)
	}
	query += ` ORDER BY id ASC LIMIT ?`
	args = append(args, limit)

	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, afterID, false, err
	}
	var batch []retagVideoRow
	for rows.Next() {
		var row retagVideoRow
		var manual int
		if err := rows.Scan(&row.id, &row.title, &row.author, &row.fileName, &row.dirName, &manual); err != nil {
			rows.Close()
			return 0, afterID, false, err
		}
		row.manual = manual == 1
		batch = append(batch, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, afterID, false, err
	}
	if err := rows.Close(); err != nil {
		return 0, afterID, false, err
	}
	if len(batch) == 0 {
		return 0, afterID, true, nil
	}
	for i := range batch {
		if batch[i].manual {
			continue
		}
		assignments, err := c.matchTagAssignmentsWithMatcher(ctx, matcher, batch[i].title, batch[i].fileName, batch[i].author, batch[i].dirName)
		if err != nil {
			return 0, afterID, false, err
		}
		batch[i].assignments = assignments
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, afterID, false, err
	}
	defer tx.Rollback()
	for _, row := range batch {
		if row.manual {
			continue
		}
		changed, err := replaceAutoVideoTagsTx(ctx, tx, row.id, row.assignments)
		if err != nil {
			return 0, afterID, false, err
		}
		if changed {
			if err := syncVideoTagsJSONTx(ctx, tx, row.id, false); err != nil {
				return 0, afterID, false, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, afterID, false, err
	}
	lastID := batch[len(batch)-1].id
	return len(batch), lastID, len(batch) < limit, nil
}

// ResetGeneratedTagState clears ordinary generated tags, engine-managed
// assignments. Crawler ownership tags are preserved
// because they represent source provenance, not automatic content matching.
func (c *Catalog) ResetGeneratedTagState(ctx context.Context) (TagStateResetResult, error) {
	var result TagStateResetResult
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	affectedRows, err := tx.QueryContext(ctx, `
SELECT DISTINCT vt.video_id
  FROM video_tags vt
  LEFT JOIN tags t ON t.id = vt.tag_id
 WHERE lower(trim(COALESCE(vt.source, ''))) IN ('auto', 'legacy', 'series', 'propagated')
    OR (
       lower(trim(COALESCE(t.source, ''))) = 'generated'
       AND lower(trim(COALESCE(t.origin, ''))) != 'crawler'
       AND NOT EXISTS (
         SELECT 1
           FROM video_tags vt_crawler
          WHERE vt_crawler.tag_id = t.id
            AND lower(trim(COALESCE(vt_crawler.source, ''))) = 'crawler'
       )
    )`)
	if err != nil {
		return result, err
	}
	var affectedVideoIDs []string
	for affectedRows.Next() {
		var videoID string
		if err := affectedRows.Scan(&videoID); err != nil {
			affectedRows.Close()
			return result, err
		}
		affectedVideoIDs = append(affectedVideoIDs, videoID)
	}
	if err := affectedRows.Err(); err != nil {
		affectedRows.Close()
		return result, err
	}
	if err := affectedRows.Close(); err != nil {
		return result, err
	}

	res, err := tx.ExecContext(ctx, `
DELETE FROM video_tags
 WHERE lower(trim(COALESCE(source, ''))) IN ('auto', 'legacy', 'series', 'propagated')`)
	if err != nil {
		return result, err
	}
	if n, err := res.RowsAffected(); err == nil {
		result.RemovedAssignments += int(n)
	}

	generatedTagFilter := `
SELECT t.id
  FROM tags t
 WHERE lower(trim(COALESCE(t.source, ''))) = 'generated'
   AND lower(trim(COALESCE(t.origin, ''))) != 'crawler'
   AND NOT EXISTS (
     SELECT 1
       FROM video_tags vt_crawler
      WHERE vt_crawler.tag_id = t.id
        AND lower(trim(COALESCE(vt_crawler.source, ''))) = 'crawler'
   )`
	res, err = tx.ExecContext(ctx, `DELETE FROM video_tags WHERE tag_id IN (`+generatedTagFilter+`)`)
	if err != nil {
		return result, err
	}
	if n, err := res.RowsAffected(); err == nil {
		result.RemovedAssignments += int(n)
	}
	res, err = tx.ExecContext(ctx, `DELETE FROM tags WHERE id IN (`+generatedTagFilter+`)`)
	if err != nil {
		return result, err
	}
	if n, err := res.RowsAffected(); err == nil {
		result.RemovedTags = int(n)
	}

	for _, videoID := range affectedVideoIDs {
		manual := hasManualTagsTx(ctx, tx, videoID)
		if err := syncVideoTagsJSONTx(ctx, tx, videoID, manual); err != nil {
			return result, err
		}
	}

	if err := tx.Commit(); err != nil {
		return result, err
	}
	if result.RemovedTags > 0 {
		if err := c.bumpTagRulesVersion(ctx); err != nil {
			return result, err
		}
	}
	return result, nil
}

// ReconcileBuiltinTags initializes the built-in tag pack once. After the
// initialization marker is present, deleted builtin tags are not restored.
func (c *Catalog) ReconcileBuiltinTags(ctx context.Context) error {
	return c.initializeBuiltinTagPackOnce(ctx)
}

// PruneUnreferencedTags 删除零引用的 generated 标签，包括没有任何视频引用的
// 爬虫来源标签。builtin / user 标签即使零引用也保留（人工维护语义）。
func (c *Catalog) PruneUnreferencedTags(ctx context.Context) (int, error) {
	res, err := c.db.ExecContext(ctx, `
DELETE FROM tags
 WHERE source = 'generated'
   AND id NOT IN (SELECT DISTINCT tag_id FROM video_tags)`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		if err := c.bumpTagRulesVersion(ctx); err != nil {
			return int(n), err
		}
	}
	return int(n), nil
}

func (c *Catalog) AutoGenerateTagsEnabled(ctx context.Context) (bool, error) {
	return false, nil
}

func (c *Catalog) SetAutoGenerateTagsEnabled(ctx context.Context, enabled bool) error {
	return c.SetSetting(ctx, settingAutoGenerateTagsEnabled, "false")
}

// SyncSeriesTags is retained for older callers. Series tags were part of the
// retired automatic tagging model, so this no longer creates or attaches them.
func (c *Catalog) SyncSeriesTags(ctx context.Context, minVideos int) (int, error) {
	return 0, nil
}

func (c *Catalog) syncSeriesTagsRetired(ctx context.Context, minVideos int) (int, error) {
	if minVideos <= 0 {
		minVideos = 3
	}
	rows, err := c.db.QueryContext(ctx, `
SELECT id, title, COALESCE(file_name, ''), COALESCE(tags_manual, 0)
  FROM videos
 WHERE COALESCE(hidden, 0) = 0`)
	if err != nil {
		return 0, err
	}
	type seriesVideo struct {
		id     string
		code   string
		manual bool
	}
	bySeries := map[string][]seriesVideo{}
	for rows.Next() {
		var id, title, fileName string
		var manual int
		if err := rows.Scan(&id, &title, &fileName, &manual); err != nil {
			rows.Close()
			return 0, err
		}
		code := tagging.FindAVCode(fileName)
		if code == "" {
			code = tagging.FindAVCode(title)
		}
		if code == "" {
			continue
		}
		series := tagging.SeriesOf(code)
		if series == "" {
			continue
		}
		bySeries[series] = append(bySeries[series], seriesVideo{id: id, code: code, manual: manual == 1})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	// desired: series → videoID → evidence（人工锁定视频不写）。
	desired := map[string]map[string]string{}
	for series, videos := range bySeries {
		if len(videos) < minVideos {
			continue
		}
		videoMap := map[string]string{}
		for _, v := range videos {
			if v.manual {
				continue
			}
			videoMap[v.id] = "系列:" + v.code
		}
		if len(videoMap) > 0 {
			desired[series] = videoMap
		}
	}

	// 确保标签存在（跳过被删除过的系列）。
	tagIDBySeries := map[string]int64{}
	for series := range desired {
		tag, err := c.ensureTag(ctx, series, nil, "generated")
		if err != nil {
			if errors.Is(err, ErrAutoTagGenerationDisabled) {
				delete(desired, series)
				continue
			}
			return 0, err
		}
		tagIDBySeries[series] = tag.ID
	}

	// 现有 series 行。
	existingRows, err := c.db.QueryContext(ctx, `
SELECT vt.video_id, t.id, t.label
  FROM video_tags vt
  JOIN tags t ON t.id = vt.tag_id
 WHERE vt.source = 'series'`)
	if err != nil {
		return 0, err
	}
	type existingRow struct {
		videoID string
		tagID   int64
		label   string
	}
	var existing []existingRow
	for existingRows.Next() {
		var row existingRow
		if err := existingRows.Scan(&row.videoID, &row.tagID, &row.label); err != nil {
			existingRows.Close()
			return 0, err
		}
		existing = append(existing, row)
	}
	if err := existingRows.Err(); err != nil {
		existingRows.Close()
		return 0, err
	}
	if err := existingRows.Close(); err != nil {
		return 0, err
	}

	changedVideos := map[string]struct{}{}

	// 删除不再成立的行。
	for _, row := range existing {
		videoMap := desired[strings.ToUpper(row.label)]
		if videoMap != nil {
			if _, ok := videoMap[row.videoID]; ok {
				delete(videoMap, row.videoID) // 剩下的就是待新增
				continue
			}
		}
		if _, err := c.db.ExecContext(ctx,
			`DELETE FROM video_tags WHERE video_id = ? AND tag_id = ? AND source = 'series'`,
			row.videoID, row.tagID); err != nil {
			return 0, err
		}
		changedVideos[row.videoID] = struct{}{}
	}

	// 新增缺失的行。
	added := 0
	for series, videoMap := range desired {
		if _, ok := tagIDBySeries[series]; !ok {
			continue
		}
		for videoID, evidence := range videoMap {
			n, err := c.AddVideoTagAssignments(ctx, videoID, []TagAssignment{{
				Label:    series,
				Source:   "series",
				Evidence: evidence,
			}})
			if err != nil {
				return added, err
			}
			added += n
		}
	}

	for videoID := range changedVideos {
		if err := c.syncVideoTagsJSON(ctx, videoID, c.hasManualTags(ctx, videoID)); err != nil {
			return added, err
		}
	}
	if _, err := c.PruneUnreferencedTags(ctx); err != nil {
		return added, err
	}
	return added, nil
}

// ClearPropagatedTags 删除全部 propagated 行并同步受影响视频的 JSON 缓存。
// 传播标签每轮夜间任务整体重算，不再成立的自动撤销。返回受影响视频数。
func (c *Catalog) ClearPropagatedTags(ctx context.Context) (int, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT DISTINCT video_id FROM video_tags WHERE source = 'propagated'`)
	if err != nil {
		return 0, err
	}
	var videoIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		videoIDs = append(videoIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(videoIDs) == 0 {
		return 0, nil
	}
	if _, err := c.db.ExecContext(ctx, `DELETE FROM video_tags WHERE source = 'propagated'`); err != nil {
		return 0, err
	}
	for _, id := range videoIDs {
		if err := c.syncVideoTagsJSON(ctx, id, c.hasManualTags(ctx, id)); err != nil {
			return 0, err
		}
	}
	return len(videoIDs), nil
}

// PropagateTagsAcrossDuplicates is retained for older callers. Propagated
// assignments were part of automatic tagging, so no new rows are created.
func (c *Catalog) PropagateTagsAcrossDuplicates(ctx context.Context) (int, error) {
	return 0, nil
}

func (c *Catalog) propagateTagsAcrossDuplicatesRetired(ctx context.Context) (int, error) {
	rows, err := c.db.QueryContext(ctx, `
SELECT v.id, v.size_bytes, v.sampled_sha256, COALESCE(v.tags_manual, 0)
  FROM videos v
  JOIN (
	SELECT size_bytes, sampled_sha256
	  FROM videos
	 WHERE size_bytes > 0 AND COALESCE(sampled_sha256, '') != ''
	   AND COALESCE(hidden, 0) = 0
	 GROUP BY size_bytes, sampled_sha256
	HAVING COUNT(*) > 1
  ) dup ON dup.size_bytes = v.size_bytes AND dup.sampled_sha256 = v.sampled_sha256
 WHERE COALESCE(v.hidden, 0) = 0`)
	if err != nil {
		return 0, err
	}
	type dupMember struct {
		id     string
		manual bool
	}
	groups := map[string][]dupMember{}
	for rows.Next() {
		var id, sampled string
		var size int64
		var manual int
		if err := rows.Scan(&id, &size, &sampled, &manual); err != nil {
			rows.Close()
			return 0, err
		}
		key := sampled + "/" + strconv64(size)
		groups[key] = append(groups[key], dupMember{id: id, manual: manual == 1})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(groups) == 0 {
		return 0, nil
	}

	labelSets, err := c.loadVideoTagLabelSets(ctx)
	if err != nil {
		return 0, err
	}
	labelCase, err := c.tagLabelCanonicalCase(ctx)
	if err != nil {
		return 0, err
	}

	added := 0
	for _, members := range groups {
		union := map[string]struct{}{}
		for _, m := range members {
			for label := range labelSets[m.id] {
				union[label] = struct{}{}
			}
		}
		if len(union) == 0 {
			continue
		}
		for _, m := range members {
			if m.manual {
				continue
			}
			var assignments []TagAssignment
			for labelKey := range union {
				if _, ok := labelSets[m.id][labelKey]; ok {
					continue
				}
				label, ok := labelCase[labelKey]
				if !ok {
					continue
				}
				assignments = append(assignments, TagAssignment{Label: label, Source: "propagated", Evidence: "同文件"})
			}
			if len(assignments) == 0 {
				continue
			}
			n, err := c.AddVideoTagAssignments(ctx, m.id, assignments)
			if err != nil {
				return added, err
			}
			added += n
		}
	}
	return added, nil
}

// tagLabelCanonicalCase 返回 小写label → 原始写法 的映射。
func (c *Catalog) tagLabelCanonicalCase(ctx context.Context) (map[string]string, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT label FROM tags`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, err
		}
		out[strings.ToLower(label)] = label
	}
	return out, rows.Err()
}

// ListManualTagVideoIDs 返回全部人工锁定标签的视频 ID 集合。
func (c *Catalog) ListManualTagVideoIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT id FROM videos WHERE COALESCE(tags_manual, 0) = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func strconv64(v int64) string {
	return strconv.FormatInt(v, 10)
}
