import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const tagsPageSource = readFileSync(
  new URL("../src/admin/TagsPage.tsx", import.meta.url),
  "utf8"
);
const adminCss = readFileSync(new URL("../src/styles/admin.css", import.meta.url), "utf8");

test("admin tags page limits visible tags by viewport", () => {
  assert.match(tagsPageSource, /const DESKTOP_TAGS_PAGE_SIZE = 25;/);
  assert.match(tagsPageSource, /const MOBILE_TAGS_PAGE_SIZE = 8;/);
  assert.match(tagsPageSource, /const TAGS_MOBILE_QUERY = "\(max-width: 640px\)";/);
  assert.match(tagsPageSource, /window\.matchMedia\(TAGS_MOBILE_QUERY\)/);
});

test("admin tags page renders only the current page", () => {
  assert.match(tagsPageSource, /filteredTags\.slice\(pageStartIndex, pageEndIndex\)/);
  assert.match(tagsPageSource, /pagedTags\.map\(\(tag\) =>/);
  assert.doesNotMatch(tagsPageSource, /filteredTags\.map\(\(tag\) =>/);
  assert.match(tagsPageSource, /全选本页/);
});

test("admin tag search miss uses the shared no-results visual", () => {
  assert.match(tagsPageSource, /const hasActiveSearch = searchQuery\.trim\(\)\.length > 0;/);
  assert.match(tagsPageSource, /const searchEmpty = hasActiveSearch && !loading && !loadError && filteredTags\.length === 0;/);
  assert.match(tagsPageSource, /searchEmpty \? " is-search-empty" : ""/);
  assert.match(
    tagsPageSource,
    /searchEmpty \? \(\s*<AdminEmptyVisual[\s\S]*?variant="no-results"[\s\S]*?text="未查询到"[\s\S]*?admin-tags-empty-search[\s\S]*?\) : \(\s*<div className="admin-tags-board">/
  );
  assert.match(
    adminCss,
    /\.admin-tags-page\.is-search-empty\s*\{[^}]*display\s*:\s*flex;[^}]*flex-direction\s*:\s*column;[^}]*min-height\s*:\s*calc\(100vh - \(var\(--space-7\) \* 2\)\)/s
  );
  assert.match(
    adminCss,
    /\.admin-tags-page\.is-search-empty \.admin-tags-layout,\s*\.admin-tags-page\.is-search-empty \.admin-tags-main\s*\{[^}]*display\s*:\s*flex;[^}]*width\s*:\s*100%;[^}]*min-height\s*:\s*0;/s
  );
  assert.match(
    adminCss,
    /\.admin-tags-empty-search\s*\{[^}]*box-sizing\s*:\s*border-box;[^}]*flex\s*:\s*1 1 auto;[^}]*min-height\s*:\s*0;[^}]*padding\s*:\s*0 16px 96px/s
  );
  assert.doesNotMatch(adminCss, /\.admin-tags-page\.is-search-empty \.admin-tags-board[\s\S]*?display\s*:\s*flex/);
});
