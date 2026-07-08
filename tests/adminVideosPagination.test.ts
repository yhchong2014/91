import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const videosPageSource = readFileSync(new URL("../src/admin/VideosPage.tsx", import.meta.url), "utf8");
const emptyVisualSource = readFileSync(new URL("../src/admin/AdminEmptyVisual.tsx", import.meta.url), "utf8");
const adminCss = readFileSync(new URL("../src/styles/admin.css", import.meta.url), "utf8");

test("admin empty visual places the requested image above its text", () => {
  assert.match(emptyVisualSource, /import emptyImage from "@\/assets\/admin\/empty\.webp"/);
  assert.match(emptyVisualSource, /import noResultsImage from "@\/assets\/admin\/no-results\.webp"/);
  assert.match(emptyVisualSource, /variant === "no-results" \? noResultsImage : emptyImage/);
  assert.match(emptyVisualSource, /admin-empty-visual__media[\s\S]*?<img[\s\S]*?admin-empty-visual__text/);
});

test("normal videos use ten items while blacklist remains responsive", () => {
  assert.match(videosPageSource, /const NORMAL_VIDEOS_PAGE_SIZE = 10;/);
  assert.match(videosPageSource, /const DESKTOP_VIDEOS_PAGE_SIZE = 50;/);
  assert.match(videosPageSource, /const MOBILE_VIDEOS_PAGE_SIZE = 20;/);
  assert.match(videosPageSource, /const VIDEOS_MOBILE_QUERY = "\(max-width: 640px\)";/);
  assert.match(videosPageSource, /window\.matchMedia\(VIDEOS_MOBILE_QUERY\)/);
  assert.match(videosPageSource, /function CurrentVideosTab[\s\S]*?const pageSize = NORMAL_VIDEOS_PAGE_SIZE;/);
  assert.match(videosPageSource, /function BlacklistTab[\s\S]*?const pageSize = useVideosPageSize\(\);/);
  assert.match(videosPageSource, /api\.listVideos\(\{ page, size: pageSize, keyword: searchKeyword \}\)/);
});

test("admin video pagination only shows current and total pages", () => {
  const paginationCalls = Array.from(
    videosPageSource.matchAll(/<Pagination page=\{page\} totalPages=\{totalPages\} onPage=\{setPage\} \/>/g)
  );
  assert.match(videosPageSource, /第 \{page\} \/ \{totalPages\} 页/);
  assert.doesNotMatch(videosPageSource, /每页 \{pageSize\} 个/);
  assert.doesNotMatch(videosPageSource, /<Pagination[^>]*pageSize=\{pageSize\}/);
  assert.equal(paginationCalls.length, 2);
  assert.equal(
    Array.from(videosPageSource.matchAll(/\{showPagination && <Pagination page=\{page\} totalPages=\{totalPages\} onPage=\{setPage\} \/>\}/g)).length,
    2
  );
});

test("video pagination keeps its position when the page has fewer rows", () => {
  const currentSource = videosPageSource.slice(
    videosPageSource.indexOf("function CurrentVideosTab"),
    videosPageSource.indexOf("// ---------- 拉黑视频 ----------")
  );
  const blacklistSource = videosPageSource.slice(videosPageSource.indexOf("function BlacklistTab"));
  assert.equal(Array.from(videosPageSource.matchAll(/const showPagination = totalPages > 1;/g)).length, 2);
  assert.match(currentSource, /const placeholderRows = showPagination \? Math\.max\(0, pageSize - listItems\.length\) : 0;/);
  assert.match(blacklistSource, /const placeholderRows = showPagination \? Math\.max\(0, pageSize - list\.length\) : 0;/);
  assert.equal(Array.from(videosPageSource.matchAll(/Array\.from\(\{ length: placeholderRows \}/g)).length, 2);
  assert.match(videosPageSource, /className="admin-video-placeholder-row"/);
  assert.match(blacklistSource, /admin-table is-selectable admin-blacklist-table/);
  assert.match(blacklistSource, /data-label="文件名"[\s\S]*?admin-blacklist-filecell[\s\S]*?placeholder/);
  assert.match(
    adminCss,
    /\.admin-video-placeholder-row\s*\{[^}]*visibility\s*:\s*hidden;[^}]*pointer-events\s*:\s*none/s
  );
});

test("empty video tabs use the correct visual and distinguish search misses", () => {
  const currentSource = videosPageSource.slice(
    videosPageSource.indexOf("function CurrentVideosTab"),
    videosPageSource.indexOf("// ---------- 拉黑视频 ----------")
  );
  const blacklistSource = videosPageSource.slice(videosPageSource.indexOf("function BlacklistTab"));
  assert.match(currentSource, /const hasActiveSearch = searchKeyword\.trim\(\)\.length > 0;/);
  assert.match(blacklistSource, /const hasActiveSearch = searchKeyword\.trim\(\)\.length > 0;/);
  assert.match(currentSource, /const hasVideoActions = listItems\.length > 0;/);
  assert.match(blacklistSource, /const hasBlacklistActions = list\.length > 0;/);
  assert.match(currentSource, /\{hasVideoActions && \(\s*<button[\s\S]*?批量选择/);
  assert.match(blacklistSource, /\{hasBlacklistActions && \(\s*<div className="admin-videos-filter__actions admin-blacklist-source-delete">[\s\S]*?删除全部[\s\S]*?批量选择/);
  assert.match(currentSource, /admin-empty-state admin-empty-state--plain/);
  assert.match(blacklistSource, /admin-empty-state admin-empty-state--plain/);
  assert.match(currentSource, /variant=\{hasActiveSearch \? "no-results" : "empty"\}/);
  assert.match(blacklistSource, /variant=\{hasActiveSearch \? "no-results" : "empty"\}/);
  assert.match(currentSource, /hasActiveSearch \? "未查询到" : "当前库中没有视频"/);
  assert.match(blacklistSource, /hasActiveSearch \? "未查询到" : "暂无拉黑视频"/);
  assert.match(blacklistSource, /暂无拉黑视频/);
  assert.doesNotMatch(currentSource, /还没有视频。先在「网盘管理」里配置好盘并触发扫描，或调整搜索词。/);
  assert.doesNotMatch(blacklistSource, /黑名单为空/);
  assert.doesNotMatch(currentSource, /<Image size=\{48\}/);
  assert.doesNotMatch(blacklistSource, /<Ban size=\{48\}/);
  assert.match(
    adminCss,
    /\.admin-empty-state--plain\s*\{[^}]*border\s*:\s*0;[^}]*background\s*:\s*transparent/s
  );
  assert.match(
    adminCss,
    /\.admin-videos-current,[\s\S]*?\.admin-videos-blacklist\s*\{[^}]*display\s*:\s*flex;[^}]*flex-direction\s*:\s*column;[^}]*min-height\s*:\s*calc\(100vh - \(var\(--space-7\) \* 2\)\)/s
  );
  assert.match(
    adminCss,
    /\.admin-videos-current > \.admin-empty-state--plain,[\s\S]*?\.admin-videos-blacklist > \.admin-empty-state--plain\s*\{[^}]*box-sizing\s*:\s*border-box;[^}]*flex\s*:\s*1 1 auto;[^}]*min-height\s*:\s*0;[^}]*padding\s*:\s*0 16px 96px/s
  );
  assert.doesNotMatch(adminCss, /translateY\(-48px\)/);
});

test("video tabs do not show the loading state while switching tabs", () => {
  const currentSource = videosPageSource.slice(
    videosPageSource.indexOf("function CurrentVideosTab"),
    videosPageSource.indexOf("// ---------- 拉黑视频 ----------")
  );
  const blacklistSource = videosPageSource.slice(videosPageSource.indexOf("function BlacklistTab"));
  assert.match(currentSource, /loading \? null : loadError \?/);
  assert.match(blacklistSource, /loading \? null : loadError \?/);
  assert.doesNotMatch(videosPageSource, /function LoadingState/);
  assert.doesNotMatch(currentSource, /loading \? \(\s*<LoadingState \/>/);
  assert.doesNotMatch(blacklistSource, /loading \? \(\s*<LoadingState \/>/);
});

test("admin videos batch delete runs deletions sequentially", () => {
  assert.match(videosPageSource, /for \(const id of ids\) \{/);
  assert.match(videosPageSource, /const result = await api\.deleteVideo\(id, \{ deleteSource: batchDeleteSource \}\);/);
  assert.doesNotMatch(
    videosPageSource,
    /Promise\.allSettled\(\s*ids\.map\(\(id\) => api\.deleteVideo\(id(?:, [^)]+)?\)\)\s*\)/
  );
});

test("admin videos track preview regeneration after it is accepted", () => {
  assert.match(videosPageSource, /const REGEN_PREVIEW_STATUS = "generating";/);
  assert.match(videosPageSource, /const \[regenPreviewById, setRegenPreviewById\]/);
  assert.match(videosPageSource, /trackRegeneratingPreview\(\[v\]\)/);
  assert.doesNotMatch(videosPageSource, /data-label="预览视频"[\s\S]*?<PreviewStatus/);
  assert.match(videosPageSource, /onRegenPreview=\{\(\) => handleRegen\(editingVideo\)\}/);
  assert.match(videosPageSource, /className="admin-btn admin-video-preview-button"/);
  assert.match(videosPageSource, /refreshListOnly\(\)/);
});

test("admin videos keep generating status after page refresh", () => {
  assert.match(videosPageSource, /const hasGeneratingPreview = list\.some\(\(v\) => v\.previewStatus === REGEN_PREVIEW_STATUS\);/);
  assert.match(videosPageSource, /if \(trackedRegenCount === 0 && !hasGeneratingPreview\) return;/);
  assert.match(videosPageSource, /function isPreviewGenerating\(v: api\.AdminVideo\)/);
  assert.match(videosPageSource, /return !!regenPreviewById\[v\.id\] \|\| v\.previewStatus === REGEN_PREVIEW_STATUS;/);
  assert.match(videosPageSource, /previewGenerating=\{isPreviewGenerating\(editingVideo\)\}/);
  assert.match(videosPageSource, /disabled=\{saving \|\| previewBusy\}/);
});
