import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const sortToolbarSource = readFileSync(
  new URL("../src/components/SortToolbar.tsx", import.meta.url),
  "utf8"
);
const listingPageSource = readFileSync(
  new URL("../src/pages/ListingPage.tsx", import.meta.url),
  "utf8"
);
const layoutCss = readFileSync(
  new URL("../src/styles/layout.css", import.meta.url),
  "utf8"
);
const searchCss = readFileSync(
  new URL("../src/styles/search.css", import.meta.url),
  "utf8"
);
const typesSource = readFileSync(new URL("../src/types.ts", import.meta.url), "utf8");

function ruleBody(css: string, selector: string): string {
  const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = css.match(new RegExp(`${escapedSelector}\\s*\\{([^}]*)\\}`));
  assert.ok(match, `Expected CSS rule for ${selector}`);
  return match[1];
}

test("list page sort toolbar only exposes active sort options", () => {
  assert.match(sortToolbarSource, /\{ key: "latest", label: "最新" \}/);
  assert.match(sortToolbarSource, /\{ key: "hot", label: "最热" \}/);
  assert.match(sortToolbarSource, /\{ key: "recent", label: "最近观看" \}/);

  for (const removed of ["本周", "最长", "高清", "精选"]) {
    assert.doesNotMatch(sortToolbarSource, new RegExp(removed));
  }
  assert.match(typesSource, /export type SortKey = "latest" \| "hot" \| "recent";/);
});

test("listing page uses compact spacing after the tag cloud", () => {
  assert.match(listingPageSource, /className="container page-section listing-discovery-section"/);
  assert.match(listingPageSource, /className="container page-section listing-primary-section"/);
  assert.match(listingPageSource, /import \{ AdminEmptyVisual \} from "@\/admin\/AdminEmptyVisual"/);
  assert.match(listingPageSource, /const hasActiveFilter = keyword\.trim\(\)\.length > 0 \|\| tag\.trim\(\)\.length > 0;/);
  assert.match(listingPageSource, /variant=\{hasActiveFilter \? "no-results" : "empty"\}/);
  assert.match(listingPageSource, /text=\{hasActiveFilter \? "未查询到" : "当前库中没有视频"\}/);
  assert.match(listingPageSource, /className="admin-empty-state admin-empty-state--plain listing-empty-state"/);
  assert.doesNotMatch(listingPageSource, /没有找到匹配的视频/);
  assert.doesNotMatch(listingPageSource, /SectionHeader/);
  assert.doesNotMatch(listingPageSource, /全部视频/);
  assert.doesNotMatch(listingPageSource, /搜索结果：/);
  assert.doesNotMatch(listingPageSource, /标签：/);
  assert.doesNotMatch(listingPageSource, /共 \$\{total\} 个视频/);

  const discoverySection = ruleBody(layoutCss, ".listing-discovery-section");
  assert.match(discoverySection, /padding-bottom\s*:\s*var\(--space-2\)/);
  const listingEmptyState = ruleBody(layoutCss, ".admin-empty-state.listing-empty-state");
  assert.match(listingEmptyState, /min-height\s*:\s*clamp\(360px,\s*58vh,\s*620px\)/);
  assert.match(listingEmptyState, /padding\s*:\s*72px 16px 24px/);
});

test("sort toolbar has no outer frame around its controls", () => {
  const toolbar = ruleBody(searchCss, ".sort-toolbar");
  const group = ruleBody(searchCss, ".sort-toolbar__group");

  assert.match(toolbar, /padding\s*:\s*0/);
  assert.doesNotMatch(toolbar, /background\s*:/);
  assert.doesNotMatch(toolbar, /border\s*:/);
  assert.doesNotMatch(toolbar, /border-radius\s*:/);

  assert.match(group, /background\s*:\s*var\(--bg-sunken\)/);
  assert.match(group, /border\s*:\s*1px solid var\(--border-subtle\)/);
});
