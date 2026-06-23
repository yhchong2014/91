package catalog

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestListVideosNeedingThumbnailIncludesExistingThumbnailMissingDuration(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	videos := []*Video{
		{
			ID:           "duration-only",
			DriveID:      "drive",
			FileID:       "file-duration-only",
			Title:        "Duration Only",
			ThumbnailURL: "/p/thumb/duration-only",
			PublishedAt:  now,
			CreatedAt:    now,
			UpdatedAt:    now,
		},
		{
			ID:              "complete",
			DriveID:         "drive",
			FileID:          "file-complete",
			Title:           "Complete",
			DurationSeconds: 12,
			ThumbnailURL:    "/p/thumb/complete",
			PublishedAt:     now.Add(time.Second),
			CreatedAt:       now.Add(time.Second),
			UpdatedAt:       now.Add(time.Second),
		},
		{
			ID:              "missing-thumb",
			DriveID:         "drive",
			FileID:          "file-missing-thumb",
			Title:           "Missing Thumb",
			DurationSeconds: 18,
			PublishedAt:     now.Add(2 * time.Second),
			CreatedAt:       now.Add(2 * time.Second),
			UpdatedAt:       now.Add(2 * time.Second),
		},
		{
			ID:          "failed",
			DriveID:     "drive",
			FileID:      "file-failed",
			Title:       "Failed",
			PublishedAt: now.Add(3 * time.Second),
			CreatedAt:   now.Add(3 * time.Second),
			UpdatedAt:   now.Add(3 * time.Second),
		},
	}
	for _, v := range videos {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed %s: %v", v.ID, err)
		}
	}
	if err := cat.UpdateVideoMeta(ctx, "failed", VideoMetaPatch{ThumbnailStatus: "failed"}); err != nil {
		t.Fatalf("mark failed thumbnail: %v", err)
	}

	items, err := cat.ListVideosNeedingThumbnail(ctx, "drive", 0)
	if err != nil {
		t.Fatalf("list videos needing thumbnail: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %#v, want duration-only and missing-thumb", items)
	}
	if items[0].ID != "duration-only" || items[1].ID != "missing-thumb" {
		t.Fatalf("item ids = %q, %q; want duration-only, missing-thumb", items[0].ID, items[1].ID)
	}

	count, err := cat.CountVideosNeedingThumbnail(ctx, "drive")
	if err != nil {
		t.Fatalf("count videos needing thumbnail: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}

	counts, err := cat.CountThumbnailsByDrive(ctx)
	if err != nil {
		t.Fatalf("count thumbnails by drive: %v", err)
	}
	if got := counts["drive"]; got.Ready != 2 || got.Pending != 1 || got.Failed != 1 || got.DurationPending != 1 {
		t.Fatalf("thumbnail counts = %#v, want ready=2 pending=1 failed=1 durationPending=1", got)
	}

	if err := cat.UpdateVideoMeta(ctx, "duration-only", VideoMetaPatch{ThumbnailStatus: "skipped"}); err != nil {
		t.Fatalf("mark duration-only skipped: %v", err)
	}
	count, err = cat.CountVideosNeedingThumbnail(ctx, "drive")
	if err != nil {
		t.Fatalf("count videos needing thumbnail after skip: %v", err)
	}
	if count != 1 {
		t.Fatalf("count after skip = %d, want 1", count)
	}
	counts, err = cat.CountThumbnailsByDrive(ctx)
	if err != nil {
		t.Fatalf("count thumbnails by drive after skip: %v", err)
	}
	if got := counts["drive"]; got.Ready != 2 || got.Pending != 1 || got.Failed != 1 || got.DurationPending != 0 {
		t.Fatalf("thumbnail counts after skip = %#v, want ready=2 pending=1 failed=1 durationPending=0", got)
	}
}

func TestCreateTagAndClassifyAddsTagToMatchingExistingVideos(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	if err := cat.UpsertVideo(ctx, &Video{
		ID:          "video-1",
		DriveID:     "drive",
		FileID:      "file-1",
		Title:       "清纯短发合集",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed matching video: %v", err)
	}
	if err := cat.UpsertVideo(ctx, &Video{
		ID:          "video-2",
		DriveID:     "drive",
		FileID:      "file-2",
		Title:       "普通标题",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed non-matching video: %v", err)
	}

	classified, err := cat.CreateTagAndClassify(ctx, "清纯", nil, "user")
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}
	if classified != 1 {
		t.Fatalf("classified = %d, want 1", classified)
	}

	got, err := cat.GetVideo(ctx, "video-1")
	if err != nil {
		t.Fatalf("get matching video: %v", err)
	}
	if !sameStrings(got.Tags, []string{"清纯"}) {
		t.Fatalf("matching tags = %#v, want 清纯", got.Tags)
	}

	other, err := cat.GetVideo(ctx, "video-2")
	if err != nil {
		t.Fatalf("get non-matching video: %v", err)
	}
	if len(other.Tags) != 0 {
		t.Fatalf("non-matching tags = %#v, want none", other.Tags)
	}
}

func TestDeleteTagRemovesTagFromVideos(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	if err := cat.UpsertVideo(ctx, &Video{
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

	tag := mustTagByLabel(t, ctx, cat, "清纯")
	removed, err := cat.DeleteTag(ctx, tag.ID)
	if err != nil {
		t.Fatalf("delete tag: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	got, err := cat.GetVideo(ctx, "video-1")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if len(got.Tags) != 0 {
		t.Fatalf("video tags = %#v, want none", got.Tags)
	}
	for _, tag := range mustListTags(t, ctx, cat) {
		if tag.Label == "清纯" {
			t.Fatal("deleted tag still appears in ListTags")
		}
	}
}

func TestCreateTagAndClassifyRestoresDeletedTag(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	if err := cat.UpsertVideo(ctx, &Video{
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
	tag := mustTagByLabel(t, ctx, cat, "清纯")
	if _, err := cat.DeleteTag(ctx, tag.ID); err != nil {
		t.Fatalf("delete tag: %v", err)
	}

	classified, err := cat.CreateTagAndClassify(ctx, "清纯", nil, "user")
	if err != nil {
		t.Fatalf("recreate tag: %v", err)
	}
	if classified != 1 {
		t.Fatalf("classified = %d, want 1", classified)
	}
	got, err := cat.GetVideo(ctx, "video-1")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if !sameStrings(got.Tags, []string{"清纯"}) {
		t.Fatalf("video tags = %#v, want 清纯", got.Tags)
	}
}

func TestEnsureTagForVideoIDPrefixBackfillsSourceTag(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	for _, seed := range []struct {
		id     string
		manual bool
	}{
		{id: "scriptcrawler-crawler-a-source001"},
		{id: "scriptcrawler-crawler-a-source002", manual: true},
		{id: "scriptcrawler-other-source003"},
	} {
		if err := cat.UpsertVideo(ctx, &Video{
			ID:          seed.id,
			DriveID:     "crawler-a",
			FileID:      seed.id + ".mp4",
			Title:       "legacy title without source text",
			PublishedAt: now,
			CreatedAt:   now,
			UpdatedAt:   now,
		}); err != nil {
			t.Fatalf("seed %s: %v", seed.id, err)
		}
		if seed.manual {
			if err := cat.SetManualVideoTags(ctx, seed.id, nil); err != nil {
				t.Fatalf("mark %s manual: %v", seed.id, err)
			}
		}
	}

	added, err := cat.EnsureTagForVideoIDPrefix(ctx, "scriptcrawler-crawler-a-", "crawler-tag", nil, "system")
	if err != nil {
		t.Fatalf("ensure prefix tag: %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}
	got, err := cat.GetVideo(ctx, "scriptcrawler-crawler-a-source001")
	if err != nil {
		t.Fatalf("get tagged video: %v", err)
	}
	if !sameStrings(got.Tags, []string{"crawler-tag"}) {
		t.Fatalf("tagged video tags = %#v, want crawler-tag", got.Tags)
	}
	manual, err := cat.GetVideo(ctx, "scriptcrawler-crawler-a-source002")
	if err != nil {
		t.Fatalf("get manual video: %v", err)
	}
	if len(manual.Tags) != 0 {
		t.Fatalf("manual video tags = %#v, want unchanged", manual.Tags)
	}
	other, err := cat.GetVideo(ctx, "scriptcrawler-other-source003")
	if err != nil {
		t.Fatalf("get other prefix video: %v", err)
	}
	if len(other.Tags) != 0 {
		t.Fatalf("other prefix video tags = %#v, want unchanged", other.Tags)
	}
}

func TestDeleteTagRejectsSystemTags(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	tag := mustTagByLabel(t, ctx, cat, "AV")
	if _, err := cat.DeleteTag(ctx, tag.ID); !errors.Is(err, ErrSystemTag) {
		t.Fatalf("delete system tag err = %v, want ErrSystemTag", err)
	}

	if tag := mustTagByLabel(t, ctx, cat, "AV"); tag.Source != "system" {
		t.Fatalf("AV source = %q, want system", tag.Source)
	}
}

func TestOpenClassifiesSystemTagsForExistingVideos(t *testing.T) {
	path := t.TempDir() + "/catalog.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	now := time.Now().UnixMilli()
	if _, err := db.Exec(`
INSERT INTO videos (id, drive_id, file_id, title, tags, tags_manual, published_at, created_at, updated_at)
VALUES
	('video-auto', 'drive', 'file-auto', '巨乳后入合集', '[]', 0, ?, ?, ?),
	('video-manual', 'drive', 'file-manual', '巨乳后入合集', '[]', 1, ?, ?, ?)`,
		now, now, now, now, now, now); err != nil {
		t.Fatalf("seed videos: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	ctx := context.Background()
	cat, err := Open(path)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	got, err := cat.GetVideo(ctx, "video-auto")
	if err != nil {
		t.Fatalf("get auto video: %v", err)
	}
	if !sameStrings(got.Tags, []string{"后入", "奶子"}) {
		t.Fatalf("auto tags = %#v, want 后入/奶子", got.Tags)
	}

	manual, err := cat.GetVideo(ctx, "video-manual")
	if err != nil {
		t.Fatalf("get manual video: %v", err)
	}
	if len(manual.Tags) != 0 {
		t.Fatalf("manual tags = %#v, want unchanged", manual.Tags)
	}
}

func TestMigrateDoesNotRewriteAlreadySyncedVideoTags(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	for _, id := range []string{"video-1", "video-2", "video-3"} {
		if err := cat.UpsertVideo(ctx, &Video{
			ID:          id,
			DriveID:     "drive",
			FileID:      id,
			Title:       "巨乳后入合集",
			PublishedAt: now,
			CreatedAt:   now,
			UpdatedAt:   now,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	if err := cat.migrate(ctx); err != nil {
		t.Fatalf("first migrate: %v", err)
	}

	before := videoUpdatedAtByID(t, ctx, cat, "video-1", "video-2", "video-3")
	time.Sleep(5 * time.Millisecond)
	if err := cat.migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	after := videoUpdatedAtByID(t, ctx, cat, "video-1", "video-2", "video-3")
	for id, want := range before {
		if got := after[id]; got != want {
			t.Fatalf("%s updated_at changed on no-op migrate: got %d, want %d", id, got, want)
		}
	}
}

func TestMigrateBackfillsLegacyTagsWithoutRelations(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now().UnixMilli()
	if _, err := cat.db.ExecContext(ctx, `
INSERT INTO videos (id, drive_id, file_id, title, tags, tags_manual, published_at, created_at, updated_at)
VALUES ('legacy-video', 'drive', 'file-legacy', 'legacy title', '["legacy-tag"]', 0, ?, ?, ?)`,
		now, now, now); err != nil {
		t.Fatalf("seed legacy video: %v", err)
	}
	if err := cat.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tag := mustTagByLabel(t, ctx, cat, "legacy-tag")
	var count int
	if err := cat.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM video_tags WHERE video_id = 'legacy-video' AND tag_id = ?`, tag.ID).Scan(&count); err != nil {
		t.Fatalf("count video tag: %v", err)
	}
	if count != 1 {
		t.Fatalf("legacy video tag relation count = %d, want 1", count)
	}
}

func TestOpenMigratesLegacyVideosWithoutFileName(t *testing.T) {
	path := t.TempDir() + "/catalog.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE videos (
	id               TEXT PRIMARY KEY,
	drive_id         TEXT NOT NULL,
	file_id          TEXT NOT NULL,
	content_hash     TEXT DEFAULT '',
	parent_id        TEXT,
	title            TEXT NOT NULL,
	author           TEXT,
	tags             TEXT,
	duration_seconds INTEGER DEFAULT 0,
	size_bytes       INTEGER DEFAULT 0,
	ext              TEXT,
	quality          TEXT,
	thumbnail_url    TEXT,
	preview_file_id  TEXT,
	preview_local    TEXT,
	preview_status   TEXT DEFAULT 'pending',
	views            INTEGER DEFAULT 0,
	favorites        INTEGER DEFAULT 0,
	comments         INTEGER DEFAULT 0,
	likes            INTEGER DEFAULT 0,
	dislikes         INTEGER DEFAULT 0,
	category         TEXT,
	hidden           INTEGER DEFAULT 0,
	tags_manual      INTEGER DEFAULT 0,
	badges           TEXT,
	description      TEXT,
	published_at     INTEGER NOT NULL,
	created_at       INTEGER NOT NULL,
	updated_at       INTEGER NOT NULL
)`); err != nil {
		t.Fatalf("create legacy videos table: %v", err)
	}
	nowMillis := time.Now().UnixMilli()
	if _, err := db.Exec(`
INSERT INTO videos (
	id, drive_id, file_id, content_hash, parent_id, title, author, tags,
	duration_seconds, size_bytes, ext, quality, thumbnail_url, preview_file_id,
	preview_local, preview_status, views, favorites, comments, likes, dislikes,
	category, hidden, tags_manual, badges, description, published_at, created_at, updated_at
) VALUES (
	'legacy-video', 'drive', 'file-legacy', 'hash-legacy', 'parent-1', 'Legacy Video', 'Legacy Author', '["旧标签"]',
	180, 1024, 'mp4', 'HD', '/thumb.jpg', 'preview-file',
	'/preview.mp4', 'ready', 7, 1, 2, 3, 4,
	'legacy-category', 0, 0, '["精选"]', 'legacy description', ?, ?, ?
)`,
		nowMillis, nowMillis, nowMillis); err != nil {
		t.Fatalf("insert legacy video: %v", err)
	}
	if _, err := db.Exec(`CREATE INDEX idx_legacy_videos_category ON videos(category)`); err != nil {
		t.Fatalf("create legacy category index: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	cat, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	var fileNameDefault string
	if err := cat.db.QueryRow(`SELECT COALESCE(file_name, '') FROM videos LIMIT 1`).Scan(&fileNameDefault); err != nil && err != sql.ErrNoRows {
		t.Fatalf("query migrated file_name column: %v", err)
	}
	if fileNameDefault != "" {
		t.Fatalf("file_name default = %q, want empty", fileNameDefault)
	}
	if hasColumn(t, cat, "videos", "category") {
		t.Fatal("legacy category column was not dropped")
	}
	if indexExists(t, cat, "idx_legacy_videos_category") {
		t.Fatal("legacy category index was not dropped")
	}
	for _, index := range []string{"idx_videos_drive", "idx_videos_pub", "idx_videos_views"} {
		if !indexExists(t, cat, index) {
			t.Fatalf("base video index %s was not recreated", index)
		}
	}

	ctx := context.Background()
	got, err := cat.GetVideo(ctx, "legacy-video")
	if err != nil {
		t.Fatalf("get migrated legacy video: %v", err)
	}
	if got.Title != "Legacy Video" || got.Author != "Legacy Author" || got.Views != 7 {
		t.Fatalf("migrated video lost data: %#v", got)
	}
	if !sameStrings(got.Tags, []string{"旧标签"}) {
		t.Fatalf("migrated video tags = %#v, want legacy tag preserved", got.Tags)
	}

	now := time.Now()
	if err := cat.UpsertVideo(ctx, &Video{
		ID:          "new-video",
		DriveID:     "drive",
		FileID:      "file-new",
		Title:       "New Video",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("upsert after migration: %v", err)
	}
}

func TestSetManualVideoTagsRejectsUnknownLabels(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	if err := cat.UpsertVideo(ctx, &Video{
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

	if err := cat.SetManualVideoTags(ctx, "video-1", []string{"不存在"}); err == nil {
		t.Fatal("SetManualVideoTags accepted an unknown tag label")
	}
}

func TestSetAutoVideoTagsDoesNotOverwriteManualVideoTags(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	if err := cat.UpsertVideo(ctx, &Video{
		ID:          "video-1",
		DriveID:     "drive",
		FileID:      "file-1",
		Title:       "清纯后入",
		PublishedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := cat.CreateTagAndClassify(ctx, "清纯", nil, "user"); err != nil {
		t.Fatalf("create user tag: %v", err)
	}
	if err := cat.SetManualVideoTags(ctx, "video-1", []string{"清纯"}); err != nil {
		t.Fatalf("set manual tags: %v", err)
	}

	if err := cat.SetAutoVideoTags(ctx, "video-1", []string{"后入"}); err != nil {
		t.Fatalf("set auto tags: %v", err)
	}

	got, err := cat.GetVideo(ctx, "video-1")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if !sameStrings(got.Tags, []string{"清纯"}) {
		t.Fatalf("tags = %#v, want manual 清纯 only", got.Tags)
	}
}

func TestCreateTagAndClassifyMapsAVCodeLabelToAV(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	if _, err := cat.CreateTagAndClassify(ctx, "cc-1750027", nil, "user"); err != nil {
		t.Fatalf("create code tag: %v", err)
	}

	tags, err := cat.ListTags(ctx)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	for _, tag := range tags {
		if tag.Label == "cc-1750027" {
			t.Fatal("created standalone AV code tag cc-1750027")
		}
	}
}

func TestMigrateCollapsesAVCodeTagsIntoAV(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	for _, seed := range []struct {
		id    string
		label string
	}{
		{id: "video-1", label: "cc-1750027"},
		{id: "video-2", label: "ADN-778-FHD(1)"},
		{id: "video-3", label: "[44x.me]idbd-786"},
		{id: "video-4", label: "390JAC-233"},
	} {
		if err := cat.UpsertVideo(ctx, &Video{
			ID:          seed.id,
			DriveID:     "drive",
			FileID:      seed.id,
			Title:       seed.label + " sample",
			Tags:        []string{seed.label},
			PublishedAt: now,
			CreatedAt:   now,
			UpdatedAt:   now,
		}); err != nil {
			t.Fatalf("seed polluted video %s: %v", seed.label, err)
		}
	}

	if err := cat.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tags, err := cat.ListTags(ctx)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	var sawAV bool
	polluted := map[string]bool{}
	for _, tag := range tags {
		if tag.Label == "AV" {
			sawAV = true
		}
		if tag.Label != "AV" && isAVCodePollutedLabel(tag.Label) {
			polluted[tag.Label] = true
		}
	}
	if !sawAV {
		t.Fatal("AV tag was not seeded")
	}
	if len(polluted) > 0 {
		t.Fatalf("AV code tags were not removed: %#v", polluted)
	}

	for _, id := range []string{"video-1", "video-2", "video-3", "video-4"} {
		got, err := cat.GetVideo(ctx, id)
		if err != nil {
			t.Fatalf("get video %s: %v", id, err)
		}
		if !sameStrings(got.Tags, []string{"AV"}) {
			t.Fatalf("%s tags = %#v, want AV", id, got.Tags)
		}
	}
}

func TestMigrateClearsRemoteThumbnailURLs(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	if err := cat.UpsertDrive(ctx, &Drive{
		ID:        "onedrive-main",
		Kind:      "onedrive",
		Name:      "OneDrive",
		RootID:    "root",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed onedrive: %v", err)
	}
	if err := cat.UpsertDrive(ctx, &Drive{
		ID:        "p123-main",
		Kind:      "p123",
		Name:      "123Pan",
		RootID:    "root",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed p123: %v", err)
	}
	if err := cat.UpsertDrive(ctx, &Drive{
		ID:        "pikpak-main",
		Kind:      "pikpak",
		Name:      "PikPak",
		RootID:    "root",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed pikpak: %v", err)
	}
	if err := cat.UpsertDrive(ctx, &Drive{
		ID:        "crawler-main",
		Kind:      "scriptcrawler",
		Name:      "Crawler",
		RootID:    "/",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed crawler: %v", err)
	}

	videos := []*Video{
		{
			ID:           "onedrive-video",
			DriveID:      "onedrive-main",
			FileID:       "file-1",
			Title:        "OneDrive",
			ThumbnailURL: "https://westus21-mediap.svc.ms/transform/thumbnail?provider=spo&tempauth=expired",
		},
		{
			ID:           "local-thumb-video",
			DriveID:      "onedrive-main",
			FileID:       "file-2",
			Title:        "Local thumb",
			ThumbnailURL: "/p/thumb/local-thumb-video",
		},
		{
			ID:           "pikpak-video",
			DriveID:      "pikpak-main",
			FileID:       "file-3",
			Title:        "PikPak",
			ThumbnailURL: "https://sg-thumbnail-drive.mypikpak.net/v0/screenshot-thumbnails/demo",
		},
		{
			ID:           "p123-remote-thumb-video",
			DriveID:      "p123-main",
			FileID:       "file-4",
			Title:        "123Pan remote thumb",
			ThumbnailURL: "https://download.123pan.com/thumb/file_70_70?w=70&h=70",
		},
		{
			ID:           "p123-local-thumb-video",
			DriveID:      "p123-main",
			FileID:       "file-5",
			Title:        "123Pan local thumb",
			ThumbnailURL: "/p/thumb/p123-local-thumb-video",
		},
		{
			ID:           "scriptcrawler-crawler-main-local-thumb",
			DriveID:      "crawler-main",
			FileID:       "file-6",
			Title:        "Crawler local thumb",
			ThumbnailURL: "/p/thumb/scriptcrawler-crawler-main-local-thumb",
		},
		{
			ID:           "scriptcrawler-crawler-main-remote-thumb",
			DriveID:      "crawler-main",
			FileID:       "file-7",
			Title:        "Crawler remote thumb",
			ThumbnailURL: "https://example.invalid/crawler-thumb.jpg",
		},
	}
	for _, v := range videos {
		v.PublishedAt = now
		v.CreatedAt = now
		v.UpdatedAt = now
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed video %s: %v", v.ID, err)
		}
	}

	if err := cat.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	got, err := cat.GetVideo(ctx, "onedrive-video")
	if err != nil {
		t.Fatalf("get onedrive video: %v", err)
	}
	if got.ThumbnailURL != "" {
		t.Fatalf("onedrive thumbnail = %q, want cleared", got.ThumbnailURL)
	}

	local, err := cat.GetVideo(ctx, "local-thumb-video")
	if err != nil {
		t.Fatalf("get local thumb video: %v", err)
	}
	if local.ThumbnailURL != "/p/thumb/local-thumb-video" {
		t.Fatalf("local thumbnail = %q, want preserved", local.ThumbnailURL)
	}

	pikpak, err := cat.GetVideo(ctx, "pikpak-video")
	if err != nil {
		t.Fatalf("get pikpak video: %v", err)
	}
	if pikpak.ThumbnailURL != "" {
		t.Fatalf("pikpak thumbnail = %q, want cleared", pikpak.ThumbnailURL)
	}

	p123Remote, err := cat.GetVideo(ctx, "p123-remote-thumb-video")
	if err != nil {
		t.Fatalf("get p123 remote thumb video: %v", err)
	}
	if p123Remote.ThumbnailURL != "" {
		t.Fatalf("p123 remote thumbnail = %q, want cleared", p123Remote.ThumbnailURL)
	}
	var p123Status string
	if err := cat.db.QueryRowContext(ctx, `SELECT thumbnail_status FROM videos WHERE id = ?`, "p123-remote-thumb-video").Scan(&p123Status); err != nil {
		t.Fatalf("read p123 thumbnail status: %v", err)
	}
	if p123Status != "pending" {
		t.Fatalf("p123 remote thumbnail_status = %q, want pending", p123Status)
	}

	p123Local, err := cat.GetVideo(ctx, "p123-local-thumb-video")
	if err != nil {
		t.Fatalf("get p123 local thumb video: %v", err)
	}
	if p123Local.ThumbnailURL != "/p/thumb/p123-local-thumb-video" {
		t.Fatalf("p123 local thumbnail = %q, want preserved", p123Local.ThumbnailURL)
	}

	crawlerLocal, err := cat.GetVideo(ctx, "scriptcrawler-crawler-main-local-thumb")
	if err != nil {
		t.Fatalf("get crawler local thumb video: %v", err)
	}
	if crawlerLocal.ThumbnailURL != "/p/thumb/scriptcrawler-crawler-main-local-thumb" {
		t.Fatalf("crawler local thumbnail = %q, want preserved", crawlerLocal.ThumbnailURL)
	}

	crawlerRemote, err := cat.GetVideo(ctx, "scriptcrawler-crawler-main-remote-thumb")
	if err != nil {
		t.Fatalf("get crawler remote thumb video: %v", err)
	}
	if crawlerRemote.ThumbnailURL != "" {
		t.Fatalf("crawler remote thumbnail = %q, want cleared", crawlerRemote.ThumbnailURL)
	}
}

func TestMigrateHidesZeroSizeVideosForKnownDrives(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	if err := cat.UpsertDrive(ctx, &Drive{
		ID:        "drive-main",
		Kind:      "onedrive",
		Name:      "OneDrive",
		RootID:    "root",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed drive: %v", err)
	}
	for _, v := range []*Video{
		{ID: "empty-video", DriveID: "drive-main", FileID: "file-1", Title: "Empty", Size: 0},
		{ID: "normal-video", DriveID: "drive-main", FileID: "file-2", Title: "Normal", Size: 123},
		{ID: "orphan-empty-video", DriveID: "unknown-drive", FileID: "file-3", Title: "Orphan", Size: 0},
	} {
		v.PublishedAt = now
		v.CreatedAt = now
		v.UpdatedAt = now
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed video %s: %v", v.ID, err)
		}
	}

	if err := cat.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	empty, err := cat.GetVideo(ctx, "empty-video")
	if err != nil {
		t.Fatalf("get empty video: %v", err)
	}
	if !empty.Hidden {
		t.Fatal("empty video was not hidden")
	}

	normal, err := cat.GetVideo(ctx, "normal-video")
	if err != nil {
		t.Fatalf("get normal video: %v", err)
	}
	if normal.Hidden {
		t.Fatal("normal video was hidden")
	}

	orphan, err := cat.GetVideo(ctx, "orphan-empty-video")
	if err != nil {
		t.Fatalf("get orphan empty video: %v", err)
	}
	if orphan.Hidden {
		t.Fatal("orphan empty video without a known drive was hidden")
	}
}

func TestListVideosHidesDuplicateContentHashes(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	for _, v := range []*Video{
		{
			ID:          "video-1",
			DriveID:     "drive",
			FileID:      "file-1",
			Title:       "First",
			ContentHash: "hash-same",
			PublishedAt: now,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "video-2",
			DriveID:     "drive",
			FileID:      "file-2",
			Title:       "Second",
			ContentHash: "hash-same",
			PublishedAt: now.Add(time.Second),
			CreatedAt:   now.Add(time.Second),
			UpdatedAt:   now.Add(time.Second),
		},
	} {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed video %s: %v", v.ID, err)
		}
	}

	items, total, err := cat.ListVideos(ctx, ListParams{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("list videos: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("visible videos total=%d len=%d, want 1", total, len(items))
	}
	if items[0].ID != "video-1" {
		t.Fatalf("visible id = %q, want video-1", items[0].ID)
	}
}

func TestTagFilterMatchesCanonicalDuplicateVideo(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	for _, v := range []*Video{
		{
			ID:          "pikpak-canonical",
			DriveID:     "pikpak",
			FileID:      "canonical.mp4",
			Title:       "Canonical",
			Size:        1024,
			PublishedAt: now,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "scriptcrawler-crawler-a-dup-1",
			DriveID:     "crawler-a",
			FileID:      "dup-1.mp4",
			Title:       "Crawler duplicate 1",
			Tags:        []string{"crawler-tag"},
			Size:        1024,
			PublishedAt: now.Add(time.Second),
			CreatedAt:   now.Add(time.Second),
			UpdatedAt:   now.Add(time.Second),
		},
		{
			ID:          "scriptcrawler-crawler-a-dup-2",
			DriveID:     "crawler-a",
			FileID:      "dup-2.mp4",
			Title:       "Crawler duplicate 2",
			Tags:        []string{"crawler-tag"},
			Size:        1024,
			PublishedAt: now.Add(2 * time.Second),
			CreatedAt:   now.Add(2 * time.Second),
			UpdatedAt:   now.Add(2 * time.Second),
		},
		{
			ID:          "scriptcrawler-crawler-a-visible",
			DriveID:     "crawler-a",
			FileID:      "visible.mp4",
			Title:       "Crawler visible",
			Tags:        []string{"crawler-tag"},
			Size:        2048,
			PublishedAt: now.Add(3 * time.Second),
			CreatedAt:   now.Add(3 * time.Second),
			UpdatedAt:   now.Add(3 * time.Second),
		},
	} {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed %s: %v", v.ID, err)
		}
	}
	for _, id := range []string{"pikpak-canonical", "scriptcrawler-crawler-a-dup-1", "scriptcrawler-crawler-a-dup-2"} {
		if err := cat.UpdateVideoFingerprint(ctx, id, "same-sampled-sha256", "ready", ""); err != nil {
			t.Fatalf("fingerprint %s: %v", id, err)
		}
	}
	if err := cat.UpdateVideoFingerprint(ctx, "scriptcrawler-crawler-a-visible", "unique-sampled-sha256", "ready", ""); err != nil {
		t.Fatalf("fingerprint visible: %v", err)
	}

	items, total, err := cat.ListVideos(ctx, ListParams{Tag: "crawler-tag", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("list videos by tag: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("tagged videos total=%d len=%d, want 2", total, len(items))
	}
	gotIDs := map[string]bool{}
	for _, item := range items {
		gotIDs[item.ID] = true
	}
	for _, want := range []string{"pikpak-canonical", "scriptcrawler-crawler-a-visible"} {
		if !gotIDs[want] {
			t.Fatalf("tagged video ids = %#v, want %s", gotIDs, want)
		}
	}
	if got := mustTagByLabel(t, ctx, cat, "crawler-tag").Count; got != 2 {
		t.Fatalf("crawler-tag count = %d, want 2 visible canonical videos", got)
	}
}

func TestListVideosCanFilterReadyThumbnails(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	for _, v := range []*Video{
		{
			ID:           "ready-video",
			DriveID:      "drive",
			FileID:       "file-ready",
			Title:        "Ready",
			ThumbnailURL: "/p/thumb/ready-video",
			PublishedAt:  now,
			CreatedAt:    now,
			UpdatedAt:    now,
		},
		{
			ID:          "pending-video",
			DriveID:     "drive",
			FileID:      "file-pending",
			Title:       "Pending",
			PublishedAt: now.Add(time.Second),
			CreatedAt:   now.Add(time.Second),
			UpdatedAt:   now.Add(time.Second),
		},
	} {
		if err := cat.UpsertVideo(ctx, v); err != nil {
			t.Fatalf("seed video %s: %v", v.ID, err)
		}
	}

	items, total, err := cat.ListVideos(ctx, ListParams{
		Page: 1, PageSize: 10, ThumbnailReadyOnly: true,
	})
	if err != nil {
		t.Fatalf("list videos: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("ready videos total=%d len=%d, want 1", total, len(items))
	}
	if items[0].ID != "ready-video" {
		t.Fatalf("ready video id = %q, want ready-video", items[0].ID)
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

func mustListTags(t *testing.T, ctx context.Context, cat *Catalog) []Tag {
	t.Helper()
	tags, err := cat.ListTags(ctx)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	return tags
}

func mustTagByLabel(t *testing.T, ctx context.Context, cat *Catalog, label string) Tag {
	t.Helper()
	for _, tag := range mustListTags(t, ctx, cat) {
		if tag.Label == label {
			return tag
		}
	}
	t.Fatalf("tag %q not found", label)
	return Tag{}
}

func hasColumn(t *testing.T, cat *Catalog, table, column string) bool {
	t.Helper()
	rows, err := cat.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("query table info for %s: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table info for %s: %v", table, err)
		}
		if strings.EqualFold(name, column) {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table info for %s: %v", table, err)
	}
	return false
}

func indexExists(t *testing.T, cat *Catalog, name string) bool {
	t.Helper()
	var count int
	if err := cat.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'index' AND name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("query index %s: %v", name, err)
	}
	return count > 0
}

func videoUpdatedAtByID(t *testing.T, ctx context.Context, cat *Catalog, ids ...string) map[string]int64 {
	t.Helper()
	out := make(map[string]int64, len(ids))
	for _, id := range ids {
		var updatedAt int64
		if err := cat.db.QueryRowContext(ctx, `SELECT updated_at FROM videos WHERE id = ?`, id).Scan(&updatedAt); err != nil {
			t.Fatalf("read updated_at for %s: %v", id, err)
		}
		out[id] = updatedAt
	}
	return out
}

// 删除旧版本 collection 标签的最后一个引用视频后，标签应当自动从 tags 表里消失。
// user/system 标签不受影响：用户/系统标签的语义由人维护，孤儿状态保留。
func TestDeleteVideoPrunesLegacyOrphanCollectionTag(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := cat.Close(); err != nil {
			t.Fatalf("close catalog: %v", err)
		}
	})

	now := time.Now()
	for _, id := range []string{"video-a", "video-b"} {
		if err := cat.UpsertVideo(ctx, &Video{
			ID:          id,
			DriveID:     "drive",
			FileID:      id,
			Title:       id,
			PublishedAt: now,
			CreatedAt:   now,
			UpdatedAt:   now,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	nowMillis := now.UnixMilli()
	if _, err := cat.db.ExecContext(ctx,
		`INSERT INTO tags (label, aliases, source, created_at, updated_at) VALUES (?, '[]', 'collection', ?, ?)`,
		"Better Call Saul S02", nowMillis, nowMillis); err != nil {
		t.Fatalf("insert legacy collection tag: %v", err)
	}
	var collectionTagID int64
	if err := cat.db.QueryRowContext(ctx, `SELECT id FROM tags WHERE label = ?`, "Better Call Saul S02").Scan(&collectionTagID); err != nil {
		t.Fatalf("lookup legacy collection tag: %v", err)
	}
	for _, id := range []string{"video-a", "video-b"} {
		if _, err := cat.db.ExecContext(ctx,
			`INSERT INTO video_tags (video_id, tag_id, source, created_at) VALUES (?, ?, 'auto', ?)`,
			id, collectionTagID, nowMillis); err != nil {
			t.Fatalf("attach legacy collection tag to %s: %v", id, err)
		}
	}

	if _, err := cat.db.ExecContext(ctx,
		`INSERT INTO tags (label, aliases, source, created_at, updated_at) VALUES (?, '[]', 'user', ?, ?)`,
		"用户标签", nowMillis, nowMillis); err != nil {
		t.Fatalf("insert user orphan tag: %v", err)
	}

	collectionExists := func() bool {
		var n int
		if err := cat.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tags WHERE label = ? AND source = 'collection'`,
			"Better Call Saul S02").Scan(&n); err != nil {
			t.Fatalf("count collection tag: %v", err)
		}
		return n > 0
	}
	if !collectionExists() {
		t.Fatal("collection tag missing right after creation")
	}

	// 删第一个视频：还有 video-b 在引用旧 collection 标签，应保留。
	if err := cat.DeleteVideo(ctx, "video-a"); err != nil {
		t.Fatalf("delete video-a: %v", err)
	}
	if !collectionExists() {
		t.Fatal("collection tag was pruned while another video still references it")
	}

	// 删最后一个引用视频，旧 collection 标签应当被同步清掉。
	if err := cat.DeleteVideo(ctx, "video-b"); err != nil {
		t.Fatalf("delete video-b: %v", err)
	}
	if collectionExists() {
		t.Fatal("orphan collection tag was not pruned after deleting the last referencing video")
	}

	// 用户标签即使是孤儿也必须保留。
	var userCount int
	if err := cat.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tags WHERE label = ? AND source = 'user'`,
		"用户标签").Scan(&userCount); err != nil {
		t.Fatalf("count user tag: %v", err)
	}
	if userCount != 1 {
		t.Fatalf("user tag count = %d, want 1 (user-source orphans must be preserved)", userCount)
	}

	// AV 系统标签也不能被孤儿清理影响。
	var avCount int
	if err := cat.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tags WHERE label = 'AV' AND source = 'system'`).Scan(&avCount); err != nil {
		t.Fatalf("count av tag: %v", err)
	}
	if avCount != 1 {
		t.Fatalf("system AV tag count = %d, want 1", avCount)
	}
}

// 重启时 migrate 应当一次性把历史遗留的孤儿 collection 标签清掉。
func TestMigratePrunesPreexistingOrphanCollectionTags(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/catalog.db"
	cat, err := Open(path)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}

	// 直接往 tags 表里写两条 collection 行：一条没有任何 video_tags 关联（孤儿），另一条人为关联视频（非孤儿）。
	now := time.Now().UnixMilli()
	if _, err := cat.db.ExecContext(ctx,
		`INSERT INTO tags (label, aliases, source, created_at, updated_at) VALUES (?, '[]', 'collection', ?, ?)`,
		"孤儿合集", now, now); err != nil {
		t.Fatalf("insert orphan tag: %v", err)
	}
	if _, err := cat.db.ExecContext(ctx,
		`INSERT INTO tags (label, aliases, source, created_at, updated_at) VALUES (?, '[]', 'collection', ?, ?)`,
		"在用合集", now, now); err != nil {
		t.Fatalf("insert in-use tag: %v", err)
	}
	var inUseTagID int64
	if err := cat.db.QueryRowContext(ctx, `SELECT id FROM tags WHERE label = ?`, "在用合集").Scan(&inUseTagID); err != nil {
		t.Fatalf("lookup in-use tag id: %v", err)
	}
	if err := cat.UpsertVideo(ctx, &Video{
		ID:          "video-keeper",
		DriveID:     "drive",
		FileID:      "file-keeper",
		Title:       "keeper",
		PublishedAt: time.Now(),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("seed keeper video: %v", err)
	}
	if _, err := cat.db.ExecContext(ctx,
		`INSERT INTO video_tags (video_id, tag_id, source, created_at) VALUES (?, ?, 'auto', ?)`,
		"video-keeper", inUseTagID, now); err != nil {
		t.Fatalf("attach in-use tag: %v", err)
	}

	// 同样写一个 user 来源的孤儿，验证 migrate 不会误删 user 孤儿。
	if _, err := cat.db.ExecContext(ctx,
		`INSERT INTO tags (label, aliases, source, created_at, updated_at) VALUES (?, '[]', 'user', ?, ?)`,
		"用户孤儿", now, now); err != nil {
		t.Fatalf("insert user orphan: %v", err)
	}

	if err := cat.Close(); err != nil {
		t.Fatalf("close before reopen: %v", err)
	}

	// 重新打开 → 触发 migrate → 应当只清掉 source='collection' 且无引用的 "孤儿合集"。
	cat2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen catalog: %v", err)
	}
	t.Cleanup(func() { _ = cat2.Close() })

	count := func(label string) int {
		var n int
		if err := cat2.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tags WHERE label = ?`, label).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", label, err)
		}
		return n
	}
	if count("孤儿合集") != 0 {
		t.Fatal("migrate did not prune orphan collection tag")
	}
	if count("在用合集") != 1 {
		t.Fatal("migrate wrongly pruned in-use collection tag")
	}
	if count("用户孤儿") != 1 {
		t.Fatal("migrate wrongly pruned user-source orphan tag")
	}
}

// TestReconcileThumbnailStatusOnce 检查升级时的"url 已写但 status=pending"修复。
// catalog.Open 会自动跑这个 migration（调用链 Open→ensureSchema→reconcileThumbnailStatusOnce）。
// 因此这里通过手动写脏数据 + 直接调 reconcile 来验证；脏数据是"绕过 Open
// 默认运行的 migration"的近似 —— 写完后状态就和遗留库一样。
func TestReconcileThumbnailStatusOnce(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { cat.Close() })

	now := time.Now()
	cases := []struct {
		id, url, status string
		wantStatus      string
	}{
		{"v-pending-url", "/p/thumb/v-pending-url", "pending", "ready"},             // 主要修复目标
		{"v-empty-url-pending", "", "pending", "pending"},                           // 没 url 不动
		{"v-failed-with-url", "/p/thumb/v-failed-with-url", "failed", "failed"},     // 显式失败保留
		{"v-empty-url-failed", "", "failed", "failed"},                              // 失败 + 没 url 也保留
		{"v-skipped-with-url", "/p/thumb/v-skipped-with-url", "skipped", "skipped"}, // 已跳过的时长补全保留
		{"v-already-ready", "/p/thumb/v-already-ready", "ready", "ready"},           // 幂等
	}
	for _, c := range cases {
		if err := cat.UpsertVideo(ctx, &Video{
			ID: c.id, DriveID: "d", FileID: "f-" + c.id,
			Title: c.id, ThumbnailURL: c.url,
			PublishedAt: now, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed %s: %v", c.id, err)
		}
		// UpsertVideo 默认把 thumbnail_status 留给 schema DEFAULT 'pending'。
		// 显式覆盖成测试想要的状态。
		if _, err := cat.db.ExecContext(ctx,
			`UPDATE videos SET thumbnail_status = ? WHERE id = ?`, c.status, c.id); err != nil {
			t.Fatalf("force seed status %s: %v", c.id, err)
		}
	}
	// 抹掉 Open 自动跑过的 marker，让我们直接验证函数本体
	if err := cat.SetSetting(ctx, "videos.thumbnail_status.url_present_to_ready_migrated", ""); err != nil {
		t.Fatalf("clear marker: %v", err)
	}

	if err := cat.reconcileThumbnailStatusOnce(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	for _, c := range cases {
		var got string
		if err := cat.db.QueryRowContext(ctx,
			`SELECT thumbnail_status FROM videos WHERE id = ?`, c.id).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", c.id, err)
		}
		if got != c.wantStatus {
			t.Errorf("%s: status = %q, want %q", c.id, got, c.wantStatus)
		}
	}

	// 二次调用应是 no-op（marker 已写）
	// 为验证：人为再插一条脏数据，第二次 reconcile 不应碰它
	if err := cat.UpsertVideo(ctx, &Video{
		ID: "v-second-call", DriveID: "d", FileID: "f-2nd",
		Title: "second", ThumbnailURL: "/p/thumb/v-second-call",
		PublishedAt: now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed second-call: %v", err)
	}
	if _, err := cat.db.ExecContext(ctx,
		`UPDATE videos SET thumbnail_status='pending' WHERE id='v-second-call'`); err != nil {
		t.Fatalf("force-pending second-call: %v", err)
	}
	if err := cat.reconcileThumbnailStatusOnce(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	var status string
	if err := cat.db.QueryRowContext(ctx,
		`SELECT thumbnail_status FROM videos WHERE id='v-second-call'`).Scan(&status); err != nil {
		t.Fatalf("read second-call: %v", err)
	}
	if status != "pending" {
		t.Errorf("second-call status = %q, want pending (migration should be no-op after marker)", status)
	}
}

func TestRequeueSkippedPreviews(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { cat.Close() })

	now := time.Now()
	cases := []struct {
		id         string
		status     string
		local      string
		fileID     string
		wantStatus string
		wantLocal  string
		wantFileID string
	}{
		{"preview-skipped", "skipped", "/tmp/old-preview.mp4", "old-preview-file", "pending", "", ""},
		{"preview-ready", "ready", "/tmp/ready-preview.mp4", "ready-preview-file", "ready", "/tmp/ready-preview.mp4", "ready-preview-file"},
		{"preview-failed", "failed", "/tmp/failed-preview.mp4", "failed-preview-file", "failed", "/tmp/failed-preview.mp4", "failed-preview-file"},
	}
	for _, c := range cases {
		if err := cat.UpsertVideo(ctx, &Video{
			ID: c.id, DriveID: "d", FileID: "source-" + c.id, Title: c.id,
			PreviewStatus: c.status, PreviewLocal: c.local, PreviewFileID: c.fileID,
			PublishedAt: now, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed %s: %v", c.id, err)
		}
	}

	if err := cat.requeueSkippedPreviews(ctx); err != nil {
		t.Fatalf("requeue skipped previews: %v", err)
	}
	if err := cat.requeueSkippedPreviews(ctx); err != nil {
		t.Fatalf("second requeue skipped previews: %v", err)
	}

	for _, c := range cases {
		got, err := cat.GetVideo(ctx, c.id)
		if err != nil {
			t.Fatalf("get %s: %v", c.id, err)
		}
		if got.PreviewStatus != c.wantStatus {
			t.Errorf("%s: preview status = %q, want %q", c.id, got.PreviewStatus, c.wantStatus)
		}
		if got.PreviewLocal != c.wantLocal {
			t.Errorf("%s: preview local = %q, want %q", c.id, got.PreviewLocal, c.wantLocal)
		}
		if got.PreviewFileID != c.wantFileID {
			t.Errorf("%s: preview file id = %q, want %q", c.id, got.PreviewFileID, c.wantFileID)
		}
	}

	pending, err := cat.ListVideosByPreviewStatus(ctx, "d", "pending", 0)
	if err != nil {
		t.Fatalf("list pending previews: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != "preview-skipped" {
		t.Fatalf("pending previews = %#v, want only preview-skipped", pending)
	}
}

// TestUpsertVideoSyncsThumbnailStatus 验证 scanner 创建/补回视频时
// thumbnail_status 跟随 thumbnail_url 自动设。这是历史 bug 的修复回归测试 ——
// 之前 UpsertVideo 的 SQL 不带 thumbnail_status 列，所有新视频都依赖
// 列 DEFAULT 'pending'，url 非空时和 status 字段长期不一致。
func TestUpsertVideoSyncsThumbnailStatusFromURL(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { cat.Close() })

	now := time.Now()
	cases := []struct {
		name       string
		thumb      string
		wantStatus string
	}{
		{"insert with remote URL → ready", "https://drive.example/thumb.jpg", "ready"},
		{"insert with local /p/thumb URL → ready", "/p/thumb/insert-local", "ready"},
		{"insert without URL → pending", "", "pending"},
	}
	for _, c := range cases {
		id := "ins-" + c.wantStatus + "-" + c.thumb
		if err := cat.UpsertVideo(ctx, &Video{
			ID: id, DriveID: "d", FileID: "f-" + id, Title: c.name,
			ThumbnailURL: c.thumb, PublishedAt: now, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("%s: upsert: %v", c.name, err)
		}
		var got string
		if err := cat.db.QueryRowContext(ctx,
			`SELECT thumbnail_status FROM videos WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("%s: read: %v", c.name, err)
		}
		if got != c.wantStatus {
			t.Errorf("%s: status = %q, want %q", c.name, got, c.wantStatus)
		}
	}
}

func TestUpsertVideoOnConflictSyncsStatusOnURLChange(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { cat.Close() })

	now := time.Now()
	id := "conflict-vid"

	// 第一次 upsert：无 url → pending
	if err := cat.UpsertVideo(ctx, &Video{
		ID: id, DriveID: "d", FileID: "f", Title: "v",
		PublishedAt: now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	var s string
	cat.db.QueryRowContext(ctx, `SELECT thumbnail_status FROM videos WHERE id=?`, id).Scan(&s)
	if s != "pending" {
		t.Fatalf("first: status = %q, want pending", s)
	}

	// 第二次 upsert（ON CONFLICT 路径）：带上 url → 自动同步 status='ready'
	if err := cat.UpsertVideo(ctx, &Video{
		ID: id, DriveID: "d", FileID: "f", Title: "v",
		ThumbnailURL: "https://drive.example/thumb.jpg",
		PublishedAt:  now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	cat.db.QueryRowContext(ctx, `SELECT thumbnail_status FROM videos WHERE id=?`, id).Scan(&s)
	if s != "ready" {
		t.Fatalf("after url upsert: status = %q, want ready", s)
	}

	// 第三次 upsert：清空 url（直 SQL 模拟 clearVolatileOneDriveThumbnails 没走 UpsertVideo
	// 这条路径，但场景就是用户手动把 thumbnail 改空。url='' 时 UpsertVideo
	// 不应改变已有 status，因为 UpsertVideo 不是清空场景的合法接口）。
	if err := cat.UpsertVideo(ctx, &Video{
		ID: id, DriveID: "d", FileID: "f", Title: "v",
		ThumbnailURL: "",
		PublishedAt:  now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("third upsert: %v", err)
	}
	cat.db.QueryRowContext(ctx, `SELECT thumbnail_status FROM videos WHERE id=?`, id).Scan(&s)
	if s != "ready" {
		t.Fatalf("after url cleared via upsert: status = %q, want unchanged 'ready'", s)
	}
}

func TestUpdateVideoMetaInfersReadyWhenURLPresent(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { cat.Close() })

	now := time.Now()
	id := "meta-test"
	if err := cat.UpsertVideo(ctx, &Video{
		ID: id, DriveID: "d", FileID: "f", Title: "v",
		PublishedAt: now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// status 初始 pending（无 url）
	var s string
	cat.db.QueryRowContext(ctx, `SELECT thumbnail_status FROM videos WHERE id=?`, id).Scan(&s)
	if s != "pending" {
		t.Fatalf("seed status = %q, want pending", s)
	}

	// 仅传 ThumbnailURL，期望 status 自动推到 'ready'
	if err := cat.UpdateVideoMeta(ctx, id, VideoMetaPatch{
		ThumbnailURL: "/p/thumb/" + id,
	}); err != nil {
		t.Fatalf("update meta: %v", err)
	}
	cat.db.QueryRowContext(ctx, `SELECT thumbnail_status FROM videos WHERE id=?`, id).Scan(&s)
	if s != "ready" {
		t.Errorf("after URL-only patch: status = %q, want ready (auto-inferred)", s)
	}

	// 显式传 ThumbnailStatus 时 patch 应该被尊重，而不是被自动推断覆盖
	if err := cat.UpdateVideoMeta(ctx, id, VideoMetaPatch{
		ThumbnailURL:    "/p/thumb/another",
		ThumbnailStatus: "failed",
	}); err != nil {
		t.Fatalf("update meta with status: %v", err)
	}
	cat.db.QueryRowContext(ctx, `SELECT thumbnail_status FROM videos WHERE id=?`, id).Scan(&s)
	if s != "failed" {
		t.Errorf("explicit status overrides inference: got %q, want failed", s)
	}
}

func TestClearVolatileOneDriveThumbnailsResetsStatus(t *testing.T) {
	ctx := context.Background()
	cat, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { cat.Close() })

	now := time.Now()
	if err := cat.UpsertVideo(ctx, &Video{
		ID: "od-vid", DriveID: "OneDrive", FileID: "od-1", Title: "od",
		ThumbnailURL: "https://westus21-mediap.svc.ms/transform/thumbnail?provider=spo&tempauth=expired",
		PublishedAt:  now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 经 UpsertVideo 自动同步，此时 status='ready'
	var s string
	cat.db.QueryRowContext(ctx, `SELECT thumbnail_status FROM videos WHERE id='od-vid'`).Scan(&s)
	if s != "ready" {
		t.Fatalf("pre-clear: status = %q, want ready", s)
	}

	if err := cat.clearVolatileOneDriveThumbnails(ctx); err != nil {
		t.Fatalf("clear: %v", err)
	}

	var url string
	cat.db.QueryRowContext(ctx, `SELECT COALESCE(thumbnail_url,''), thumbnail_status FROM videos WHERE id='od-vid'`).Scan(&url, &s)
	if url != "" {
		t.Errorf("url after clear = %q, want empty", url)
	}
	if s != "pending" {
		t.Errorf("status after clear = %q, want pending (so worker re-enqueues)", s)
	}
}
