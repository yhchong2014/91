import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const homePageSource = readFileSync(
  new URL("../src/pages/HomePage.tsx", import.meta.url),
  "utf8"
);
const tagCloudSource = readFileSync(
  new URL("../src/components/TagCloud.tsx", import.meta.url),
  "utf8"
);
const searchPanelSource = readFileSync(
  new URL("../src/components/SearchPanel.tsx", import.meta.url),
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
const appShellSource = readFileSync(
  new URL("../src/components/AppShell.tsx", import.meta.url),
  "utf8"
);
const backToTopSource = readFileSync(
  new URL("../src/components/BackToTop.tsx", import.meta.url),
  "utf8"
);

function ruleBody(css: string, selector: string): string {
  const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = css.match(new RegExp(`${escapedSelector}\\s*\\{([^}]*)\\}`));
  assert.ok(match, `Expected CSS rule for ${selector}`);
  return match[1];
}

test("home page refresh button shares back-to-top slot until back-to-top is visible", () => {
  assert.match(homePageSource, /import \{ RefreshCw \} from "lucide-react"/);
  assert.match(homePageSource, /const LATEST_POOL_SIZE = 96;/);
  assert.match(homePageSource, /const HOME_LATEST_CURSOR_KEY = "home\.latest\.cursor";/);
  assert.match(homePageSource, /function nextLatestBatch/);
  assert.match(homePageSource, /let cachedLatestBatch: VideoItem\[\] \| null = null;/);
  assert.match(homePageSource, /function cacheNextLatestBatch/);
  assert.match(homePageSource, /loadLatestCursor\(items\.length\)/);
  assert.match(homePageSource, /saveLatestCursor\(\(start \+ count\) % items\.length\)/);
  assert.match(homePageSource, /const refreshHome = useCallback\(async \(\) =>/);
  assert.match(homePageSource, /fetchHomeVideos\(excludeIds\)/);
  assert.match(homePageSource, /fetchListing\(1,\s*LATEST_POOL_SIZE,\s*\{ sort: "latest", includeTotal: false \}\)/);
  assert.match(homePageSource, /const HOME_SEARCH_PAGE_SIZE = 24;/);
  assert.match(homePageSource, /setLatestVideos\(latestBatch\)/);
  assert.match(homePageSource, /setLatestVideos\(cachedLatestBatch \?\? cacheNextLatestBatch\(cachedLatestPool,\s*DESKTOP_COUNT\)\)/);
  assert.match(homePageSource, /className=\{`home-refresh \$\{refreshing \? "is-refreshing" : ""\}`\}/);
  assert.match(homePageSource, /aria-label="刷新首页"/);
  assert.match(homePageSource, /<RefreshCw size=\{18\} \/>/);

  const refresh = ruleBody(layoutCss, ".home-refresh");
  const shiftedRefresh = ruleBody(layoutCss, ".app-shell.is-back-to-top-visible .home-refresh");
  const backToTop = ruleBody(layoutCss, ".back-to-top");
  assert.match(refresh, /position\s*:\s*fixed/);
  assert.match(refresh, /bottom\s*:\s*24px/);
  assert.match(backToTop, /bottom\s*:\s*24px/);
  assert.match(shiftedRefresh, /bottom\s*:\s*80px/);
  assert.match(refresh, /z-index\s*:\s*var\(--z-overlay\)/);
  assert.doesNotMatch(layoutCss, /\.home-refresh\.is-visible/);

  assert.match(appShellSource, /const \[backToTopVisible,\s*setBackToTopVisible\] = useState\(false\)/);
  assert.match(appShellSource, /backToTopVisible \? "is-back-to-top-visible" : ""/);
  assert.match(appShellSource, /<BackToTop onVisibilityChange=\{setBackToTopVisible\} \/>/);
  assert.match(backToTopSource, /onVisibilityChange\?: \(visible: boolean\) => void/);
  assert.match(backToTopSource, /onVisibilityChange\?\.\(nextVisible\)/);
});

test("home page reuses the cached latest batch when returning from detail", () => {
  assert.match(homePageSource, /const \[latestVideos,\s*setLatestVideos\] = useState<VideoItem\[\]>\(cachedLatestBatch \?\? \[\]\)/);
  assert.match(homePageSource, /const \[latestLoading,\s*setLatestLoading\] = useState\(cachedLatestBatch === null\)/);
  assert.match(homePageSource, /setLatestVideos\(cachedLatestBatch \?\? cacheNextLatestBatch\(cachedLatestPool,\s*DESKTOP_COUNT\)\)/);
  assert.doesNotMatch(homePageSource, /setLatestVideos\(nextLatestBatch\(cachedLatestPool,\s*DESKTOP_COUNT\)\)/);
});

test("home page reserves tag cloud space while tags load and uses one empty library state", () => {
  assert.match(tagCloudSource, /const visibleTags = useMemo/);
  assert.match(tagCloudSource, /typeof tag\.count !== "number" \|\| tag\.count > 0/);
  assert.match(tagCloudSource, /const \[loaded,\s*setLoaded\] = useState\(false\)/);
  assert.match(tagCloudSource, /setLoaded\(true\)/);
  assert.match(tagCloudSource, /if \(loaded && visibleTags\.length === 0\) return null/);
  assert.match(tagCloudSource, /const loading = !loaded && visibleTags\.length === 0/);
  assert.match(tagCloudSource, /const TAG_PLACEHOLDER_COUNT = 16;/);
  assert.match(tagCloudSource, /className=\{`tag-cloud-container \$\{loading \? "is-loading" : ""\}`\}/);
  assert.match(tagCloudSource, /aria-busy=\{loading \? "true" : undefined\}/);
  assert.match(tagCloudSource, /Array\.from\(\{ length: TAG_PLACEHOLDER_COUNT \}/);
  assert.match(tagCloudSource, /tag-chip--placeholder/);
  assert.doesNotMatch(tagCloudSource, /setTimeout/);
  assert.match(tagCloudSource, /visibleTags\.map\(renderTag\)/);
  assert.doesNotMatch(tagCloudSource, /const row[12] = visibleTags\.filter/);
  assert.doesNotMatch(tagCloudSource, /\(\{tag\.count\}\)/);
  assert.doesNotMatch(tagCloudSource, /`\$\{tag\.count\} 个视频`/);

  const tagCloudContainer = ruleBody(searchCss, ".tag-cloud-container");
  const loadingTagCloud = ruleBody(searchCss, ".tag-cloud-container.is-loading");
  const reservedTagCloud = ruleBody(searchCss, ".tag-cloud-container.is-reserved");
  const tagCloudRow = ruleBody(searchCss, ".tag-cloud__row");
  const tagChip = ruleBody(searchCss, ".tag-chip");
  const tagPlaceholder = ruleBody(searchCss, ".tag-chip--placeholder");
  assert.match(tagCloudContainer, /min-height\s*:\s*34px/);
  assert.match(loadingTagCloud, /pointer-events\s*:\s*none/);
  assert.match(reservedTagCloud, /mask-image\s*:\s*none/);
  assert.match(tagCloudRow, /flex-wrap\s*:\s*nowrap/);
  assert.match(tagChip, /flex\s*:\s*0 0 auto/);
  assert.match(tagPlaceholder, /width\s*:\s*68px/);
  assert.match(tagPlaceholder, /animation\s*:\s*tag-chip-placeholder/);
  assert.match(searchCss, /\.tag-chip--placeholder:nth-child\(6n \+ 1\)/);
  assert.match(searchCss, /\.tag-chip--placeholder:nth-child\(6n\)/);

  const searchForm = ruleBody(searchCss, ".search-panel__form");
  const searchInput = ruleBody(searchCss, ".search-panel__input");
  const searchSubmit = ruleBody(searchCss, ".search-panel__submit");
  assert.match(searchPanelSource, /placeholder="搜索视频标题或作者"/);
  assert.doesNotMatch(searchPanelSource, /搜索视频标题或作者\.\.\./);
  assert.match(searchPanelSource, /const SEARCH_DEBOUNCE_MS = 500;/);
  assert.match(searchPanelSource, /window\.setTimeout\(\(\) => \{\s*commitSearch\(keyword\);/);
  assert.match(searchPanelSource, /onSearch\(q\);/);
  assert.doesNotMatch(searchPanelSource, /onChange=\{\(e\) => navigate/);
  assert.match(searchForm, /padding\s*:\s*4px/);
  assert.match(searchInput, /height\s*:\s*36px/);
  assert.match(searchSubmit, /height\s*:\s*36px/);

  assert.match(homePageSource, /const homeLoading = rankingLoading \|\| latestLoading/);
  assert.match(homePageSource, /import \{ AdminEmptyVisual \} from "@\/admin\/AdminEmptyVisual"/);
  assert.match(homePageSource, /const \[searchQuery, setSearchQuery\] = useState\(""\)/);
  assert.match(homePageSource, /const \[searchSort, setSearchSort\] = useState<SortKey>\("latest"\)/);
  assert.match(homePageSource, /const \[searchView, setSearchView\] = useState<ViewMode>\("grid"\)/);
  assert.match(homePageSource, /<SearchPanel value=\{searchQuery\} onSearch=\{handleSearch\} \/>/);
  assert.match(homePageSource, /fetchListing\(searchPage,\s*HOME_SEARCH_PAGE_SIZE,\s*\{[\s\S]*?q: activeSearchQuery,[\s\S]*?sort: searchSort/);
  assert.doesNotMatch(homePageSource, /搜索结果：/);
  assert.match(homePageSource, /<SortToolbar[\s\S]*?sort=\{searchSort\}[\s\S]*?view=\{searchView\}/);
  assert.match(homePageSource, /setSearchSort\(nextSort\);[\s\S]*?setSearchPage\(1\);/);
  assert.match(homePageSource, /onViewChange=\{setSearchView\}/);
  assert.match(homePageSource, /compact=\{searchView === "compact"\}/);
  assert.match(homePageSource, /variant="no-results"[\s\S]*?text="未查询到"[\s\S]*?className="admin-empty-state admin-empty-state--plain home-empty-state"/);
  assert.match(homePageSource, /<Pagination[\s\S]*?page=\{searchPage\}[\s\S]*?pageSize=\{HOME_SEARCH_PAGE_SIZE\}/);
  assert.match(homePageSource, /const hasAnyVideos = ranking\.length > 0 \|\| latest\.length > 0/);
  assert.match(homePageSource, /const showEmptyHome = !homeLoading && !hasAnyVideos/);
  assert.match(homePageSource, /\{!hasActiveSearch && \(\s*hasAnyVideos \? \(\s*<TagCloud \/>\s*\) : \(\s*<div className="tag-cloud-container is-reserved" aria-hidden="true" \/>\s*\)\s*\)\}/);
  assert.match(homePageSource, /<SectionHeader title="随机推荐" \/>/);
  assert.match(homePageSource, /<SectionHeader title="最新视频" \/>/);
  assert.doesNotMatch(homePageSource, /随机展示/);
  assert.doesNotMatch(homePageSource, /共 \$\{latest\.length\} 个/);
  assert.match(homePageSource, /className="container page-section home-discovery-section"/);
  assert.match(homePageSource, /className="container page-section home-primary-section"/);
  assert.match(homePageSource, /variant="empty"[\s\S]*?text="当前库中没有视频"[\s\S]*?className="admin-empty-state admin-empty-state--plain home-empty-state"/);
  assert.doesNotMatch(homePageSource, /className="home-empty"/);
  assert.doesNotMatch(homePageSource, /当前没有可播放视频/);

  const discoverySection = ruleBody(layoutCss, ".home-discovery-section");
  const primaryHeader = ruleBody(layoutCss, ".home-primary-section .section-header");
  assert.match(discoverySection, /padding-bottom\s*:\s*var\(--space-2\)/);
  assert.match(primaryHeader, /margin-top\s*:\s*var\(--space-2\)/);

  assert.doesNotMatch(layoutCss, /\.home-empty\s*\{/);
  const homeEmptyState = ruleBody(layoutCss, ".admin-empty-state.home-empty-state");
  assert.match(homeEmptyState, /min-height\s*:\s*clamp\(360px,\s*58vh,\s*620px\)/);
  assert.match(homeEmptyState, /padding\s*:\s*72px 16px 24px/);
});
