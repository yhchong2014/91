-- 视频元数据主表
CREATE TABLE IF NOT EXISTS videos (
    id               TEXT PRIMARY KEY,          -- <drive>-<fileID> 拼接的稳定 ID
    drive_id         TEXT NOT NULL,
    file_id          TEXT NOT NULL,
    file_name        TEXT DEFAULT '',           -- 网盘侧原始文件名，用于同名同大小去重
    content_hash     TEXT DEFAULT '',
    sampled_sha256   TEXT DEFAULT '',           -- 跨网盘统一采样指纹（size + sampled bytes）
    fingerprint_status TEXT DEFAULT 'pending',  -- pending / ready / failed
    fingerprint_error  TEXT DEFAULT '',
    parent_id        TEXT,
    dir_name         TEXT DEFAULT '',           -- 所在目录名（扫盘时落库，供标签重算使用）
    title            TEXT NOT NULL,
    author           TEXT,
    tags             TEXT,                      -- JSON array
    duration_seconds INTEGER DEFAULT 0,
    size_bytes       INTEGER DEFAULT 0,
    ext              TEXT,
    quality          TEXT,                      -- HD / SD
    thumbnail_url    TEXT,
    thumbnail_status TEXT DEFAULT 'pending',    -- pending / ready / failed / skipped
    thumbnail_failures INTEGER DEFAULT 0,        -- consecutive transient thumbnail generation failures
    preview_file_id  TEXT,                      -- deprecated: 旧版回写网盘后的预览视频 file id
    preview_local    TEXT,                      -- 本地预览视频路径（兜底）
    preview_status   TEXT DEFAULT 'pending',    -- pending / ready / failed / disabled
    transcode_status TEXT DEFAULT '',           -- '' / pending / ready / skipped / failed（浏览器兼容性转码）
    transcode_error  TEXT DEFAULT '',
    transcoded_file_id TEXT DEFAULT '',         -- 转码产物在同一 drive 上的 fileID，播放源优先用它
    transcoded_size  INTEGER DEFAULT 0,
    views            INTEGER DEFAULT 0,
    last_viewed_at   INTEGER DEFAULT 0,
    favorites        INTEGER DEFAULT 0,
    comments         INTEGER DEFAULT 0,
    likes            INTEGER DEFAULT 0,
    last_liked_at    INTEGER DEFAULT 0,
    dislikes         INTEGER DEFAULT 0,
    hidden           INTEGER DEFAULT 0,          -- 1 = hidden from public display
    tags_manual      INTEGER DEFAULT 0,          -- 1 = user explicitly curated tags
    badges           TEXT,                      -- JSON array
    description      TEXT,
    published_at     INTEGER NOT NULL,          -- unix ms
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_videos_drive ON videos(drive_id, file_id);
CREATE INDEX IF NOT EXISTS idx_videos_pub   ON videos(published_at DESC);
CREATE INDEX IF NOT EXISTS idx_videos_views ON videos(views DESC);

-- 统一标签池
CREATE TABLE IF NOT EXISTS tags (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    label       TEXT NOT NULL UNIQUE COLLATE NOCASE,
    aliases     TEXT NOT NULL DEFAULT '[]',       -- JSON array，旧版别名数据，保留用于迁移兼容
    -- 匹配规则 JSON：{"keywords":[],"matchAvCode":bool}
    -- 为空时匹配器按 label+旧版 aliases 兜底。
    match_rules TEXT NOT NULL DEFAULT '{}',
    source      TEXT NOT NULL DEFAULT 'user',     -- builtin / user / generated
    origin      TEXT NOT NULL DEFAULT '',         -- crawler 等来源型标签标记；不参与匹配来源归一
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS video_tags (
    video_id   TEXT NOT NULL,
    tag_id     INTEGER NOT NULL,
    -- auto=规则引擎 / manual=人工 / legacy=旧数据回填 / crawler=爬虫脚本或爬虫名 /
    -- series=番号系列 / propagated=同类传播
    source     TEXT NOT NULL DEFAULT 'auto',
    evidence   TEXT NOT NULL DEFAULT '',          -- 命中证据，如 "文件名:翘臀"
    created_at INTEGER NOT NULL,
    PRIMARY KEY (video_id, tag_id)
);

CREATE INDEX IF NOT EXISTS idx_video_tags_tag ON video_tags(tag_id);
CREATE INDEX IF NOT EXISTS idx_video_tags_video ON video_tags(video_id);

-- 被拉黑、删除或自动去重的视频。用于防止后续扫描 / 爬虫把同一个源文件
-- 再次入库；source_deleted 是旧版本兼容字段，源文件删除成功后会清除墓碑。
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
);

CREATE INDEX IF NOT EXISTS idx_deleted_videos_drive_file
    ON deleted_videos(drive_id, file_id);
CREATE INDEX IF NOT EXISTS idx_deleted_videos_drive_hash
    ON deleted_videos(drive_id, content_hash);
CREATE INDEX IF NOT EXISTS idx_deleted_videos_drive_signature
    ON deleted_videos(drive_id, file_name, size_bytes);

-- 爬虫来源记录。用于把已确认重复的 source_id 写回 seen 列表，
-- 避免后续爬虫反复下载同一个候选视频。
CREATE TABLE IF NOT EXISTS crawler_seen_sources (
    kind               TEXT NOT NULL,
    drive_id           TEXT NOT NULL,
    source_id          TEXT NOT NULL,
    status             TEXT NOT NULL DEFAULT 'imported', -- imported / duplicate
    canonical_video_id TEXT NOT NULL DEFAULT '',
    sampled_sha256     TEXT NOT NULL DEFAULT '',
    size_bytes         INTEGER NOT NULL DEFAULT 0,
    first_seen_at      INTEGER NOT NULL,
    last_seen_at       INTEGER NOT NULL,
    PRIMARY KEY (kind, drive_id, source_id)
);

CREATE INDEX IF NOT EXISTS idx_crawler_seen_sources_drive
    ON crawler_seen_sources(kind, drive_id, status);

-- 网盘账户
CREATE TABLE IF NOT EXISTS drives (
    id            TEXT PRIMARY KEY,
    kind          TEXT NOT NULL,                -- quark / p115 / p123 / pikpak / wopan / guangyapan / onedrive / googledrive / localstorage / scriptcrawler
    name          TEXT NOT NULL,
    root_id       TEXT NOT NULL DEFAULT '0',
    scan_root_id  TEXT,                          -- deprecated: 扫描起点固定等于 root_id
    credentials   TEXT,                          -- JSON: cookie / refresh_token 等
    status        TEXT DEFAULT 'disconnected',   -- disconnected / ok / error
    last_error    TEXT,
    -- 是否给该盘生成预览视频：1 开 / 0 关。封面生成不受影响。
    -- 替代了早期的全局 preview.enabled 设置（保留旧 setting 行不再读）。
    teaser_enabled INTEGER NOT NULL DEFAULT 1,
    -- 扫描时要跳过的目录 ID 集合（JSON array of string）。命中其中任意一个的目录及其
    -- 全部子目录都不会被递归扫描，也不会进入 SeenFileIDs / VisitedDirIDs 统计。
    -- 替代了早期硬编码"影视"目录的特例分支。
    skip_dir_ids  TEXT NOT NULL DEFAULT '[]',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

-- 扫描任务状态
CREATE TABLE IF NOT EXISTS scans (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    drive_id    TEXT NOT NULL,
    started_at  INTEGER NOT NULL,
    finished_at INTEGER,
    scanned     INTEGER DEFAULT 0,
    added       INTEGER DEFAULT 0,
    error       TEXT
);

-- 管理后台 session（简单 token 存储）
CREATE TABLE IF NOT EXISTS admin_sessions (
    token      TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

-- 管理后台登录永久封禁 IP
CREATE TABLE IF NOT EXISTS banned_login_ips (
    ip         TEXT PRIMARY KEY,
    reason     TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

-- 全局 key-value 设置（preview 开关等）
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

-- 普通用户表
CREATE TABLE IF NOT EXISTS users (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    username   TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password   TEXT NOT NULL,                    -- bcrypt 哈希
    role       TEXT NOT NULL DEFAULT 'user',     -- admin / user
    banned     INTEGER NOT NULL DEFAULT 0,       -- 1 = 被封禁
    created_at INTEGER NOT NULL
);
