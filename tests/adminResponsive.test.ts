import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const adminCss = readFileSync(
  new URL("../src/styles/admin.css", import.meta.url),
  "utf8"
);
const videosPageSource = readFileSync(
  new URL("../src/admin/VideosPage.tsx", import.meta.url),
  "utf8"
);
const usersPageSource = readFileSync(
  new URL("../src/admin/UsersPage.tsx", import.meta.url),
  "utf8"
);
const tagsPageSource = readFileSync(
  new URL("../src/admin/TagsPage.tsx", import.meta.url),
  "utf8"
);
const apiSource = readFileSync(
  new URL("../src/admin/api.ts", import.meta.url),
  "utf8"
);

function ruleBody(css: string, selector: string): string {
  const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = css.match(new RegExp(`${escapedSelector}\\s*\\{([^}]*)\\}`));
  assert.ok(match, `Expected CSS rule for ${selector}`);
  return match[1];
}

function allRuleBodies(css: string, selector: string): string {
  const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return Array.from(css.matchAll(new RegExp(`${escapedSelector}\\s*\\{([^}]*)\\}`, "g")))
    .map((match) => match[1])
    .join("\n");
}

// ruleBodyByContains 处理 CSS 里"多 selector 共享 body"的合并写法：
//   .a, .b, .c {
//     ...
//   }
// 上面的 `.b` 用直接的 `selector\s*\{` 正则匹不到。这里改成"找到任何包含目标
// selector 的连续 selector 列表（可含逗号 + 空白），紧跟一个 { ... } body"。
//
// 仅支持 body 内不再嵌套 `{}`（admin.css 没有 nesting，足够）。
function ruleBodyByContains(css: string, needle: string): string {
  const escapedNeedle = needle.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const re = new RegExp(`([^{}]*${escapedNeedle}[^{}]*)\\{([^}]*)\\}`, "g");
  const bodies: string[] = [];
  for (const m of css.matchAll(re)) {
    bodies.push(m[2]);
  }
  assert.ok(bodies.length > 0, `Expected at least one CSS rule containing ${needle}`);
  return bodies.join("\n");
}

function mobileCss(): string {
  const marker = "@media (max-width: 768px)";
  const start = adminCss.indexOf(marker);
  assert.notEqual(start, -1, "Expected mobile admin media query");
  return adminCss.slice(start);
}

test("admin login card fits narrow phone screens", () => {
  const body = ruleBody(adminCss, ".admin-login__card");

  // 桌面规则就用 min(...) 让窄屏自然适配；具体上限以 CSS 当前值为准（400px），
  // 关键是 `min(<某值>, 100%)` + `box-sizing: border-box`。
  assert.match(body, /width\s*:\s*min\(\d+px,\s*100%\)/);
  assert.match(body, /box-sizing\s*:\s*border-box/);
});

test("admin tables scroll inside the mobile viewport", () => {
  const css = mobileCss();
  // 视频/标签等"长内容"表的 mobile 形态：用 `.admin-table:not(.admin-drives-table)`
  // 把它们改成 display:block 卡片栈；网盘表 .admin-drives-table 走另一组 1280px 媒体
  // 查询。这里只断"非 drives 表的 mobile 卡片化"。
  const body = ruleBody(css, ".admin-table:not(.admin-drives-table)");

  assert.match(body, /display\s*:\s*block/);
});

test("mobile user management cards keep identity, metadata, and actions separated", () => {
  const css = mobileCss();
  const userCard = ruleBodyByContains(css, ".admin-users-table:not(.admin-drives-table) tr");
  const ipCard = ruleBodyByContains(css, ".admin-banned-ips-table:not(.admin-drives-table) tr");
  const username = ruleBody(css, ".admin-users-table:not(.admin-drives-table) .admin-users-table__username");
  const userId = ruleBody(css, ".admin-users-table:not(.admin-drives-table) .admin-users-table__id");
  const userRole = ruleBodyByContains(css, ".admin-users-table:not(.admin-drives-table) .admin-users-table__role");
  const userTime = ruleBody(css, ".admin-users-table:not(.admin-drives-table) .admin-users-table__time");
  const userActions = ruleBody(css, ".admin-users-table:not(.admin-drives-table) .admin-users-table__actions");
  const userStatus = ruleBody(css, ".admin-users-table:not(.admin-drives-table) .admin-status");
  const userStatusDot = ruleBody(css, ".admin-users-table:not(.admin-drives-table) .admin-status::before");
  const actionRow = ruleBody(css, ".admin-users-table__action-row");
  const ipIdentity = ruleBody(css, ".admin-banned-ips-table:not(.admin-drives-table) .admin-banned-ips-table__ip");
  const ipReason = ruleBodyByContains(css, ".admin-banned-ips-table:not(.admin-drives-table) .admin-banned-ips-table__reason");
  const ipActions = ruleBody(css, ".admin-banned-ips-table:not(.admin-drives-table) .admin-banned-ips-table__actions");

  assert.match(usersPageSource, /className="admin-table admin-users-table"/);
  assert.match(usersPageSource, /className="admin-table admin-banned-ips-table"/);
  assert.match(usersPageSource, /data-label="用户名"/);
  assert.match(usersPageSource, /data-label="IP 地址"/);
  assert.match(usersPageSource, /className="admin-btn admin-btn--small is-danger"/);
  assert.match(userCard, /grid-template-columns\s*:\s*repeat\(12,\s*minmax\(0,\s*1fr\)\)/);
  assert.match(userCard, /border-radius\s*:\s*var\(--radius-sm\)/);
  assert.match(ipCard, /grid-template-columns\s*:\s*repeat\(12,\s*minmax\(0,\s*1fr\)\)/);
  assert.match(username, /grid-column\s*:\s*1\s*\/\s*9/);
  assert.match(userId, /grid-column\s*:\s*9\s*\/\s*-1/);
  assert.match(userRole, /grid-row\s*:\s*2/);
  assert.match(userRole, /justify-items\s*:\s*center/);
  assert.match(userRole, /text-align\s*:\s*center/);
  assert.match(userRole, /border-top\s*:\s*1px\s+solid\s+var\(--border-subtle\)/);
  assert.match(userTime, /grid-column\s*:\s*1\s*\/\s*-1/);
  assert.match(userTime, /grid-row\s*:\s*3/);
  assert.match(userActions, /grid-row\s*:\s*4/);
  assert.match(userStatus, /gap\s*:\s*0/);
  assert.match(userStatusDot, /content\s*:\s*none/);
  assert.match(userStatusDot, /display\s*:\s*none/);
  assert.match(actionRow, /grid-template-columns\s*:\s*repeat\(3,\s*minmax\(0,\s*1fr\)\)/);
  assert.match(ipIdentity, /grid-column\s*:\s*1\s*\/\s*-1/);
  assert.match(ipReason, /grid-row\s*:\s*2/);
  assert.match(ipReason, /border-top\s*:\s*1px\s+solid\s+var\(--border-subtle\)/);
  assert.match(ipActions, /grid-row\s*:\s*4/);
  assert.match(ipActions, /display\s*:\s*block/);
});

test("admin video filter select uses an aligned custom arrow", () => {
  const select = allRuleBodies(adminCss, ".admin-videos-filter__select");
  const icon = allRuleBodies(adminCss, ".admin-videos-filter__select-icon");
  const focus = ruleBody(adminCss, ".admin-videos-filter__select:focus");
  const mobileWrap = ruleBodyByContains(mobileCss(), ".admin-videos-filter__select-wrap");

  assert.match(select, /appearance\s*:\s*none/);
  assert.match(select, /padding\s*:\s*0\s+36px\s+0\s+var\(--space-3\)/);
  assert.match(icon, /top\s*:\s*50%/);
  assert.match(icon, /right\s*:\s*12px/);
  assert.match(icon, /transform\s*:\s*translateY\(-50%\)/);
  assert.match(focus, /box-shadow\s*:\s*0\s+0\s+0\s+3px\s+var\(--accent-soft\)/);
  assert.match(mobileWrap, /flex\s*:\s*1\s+1\s+100%/);
});

test("admin video bulk actions use semantic theme colors", () => {
  const base = ruleBody(adminCss, ".admin-videos-bulk-actions__btn");
  const primary = ruleBody(adminCss, ".admin-videos-bulk-actions__btn.is-primary");
  const danger = ruleBody(adminCss, ".admin-videos-bulk-actions__btn.is-danger");
  const dangerHover = ruleBody(adminCss, ".admin-videos-bulk-actions__btn.is-danger:hover:not(:disabled)");
  const bulkBodies = [base, primary, danger, dangerHover].join("\n");

  assert.match(videosPageSource, /className="admin-btn is-primary admin-videos-bulk-actions__btn"/);
  assert.match(videosPageSource, /className="admin-btn is-danger admin-videos-bulk-actions__btn"/);
  assert.match(primary, /var\(--accent-glow\)/);
  assert.match(danger, /background\s*:\s*var\(--danger-soft\)/);
  assert.match(danger, /border-color\s*:\s*var\(--danger\)/);
  assert.match(danger, /color\s*:\s*var\(--danger\)/);
  assert.match(dangerHover, /background\s*:\s*var\(--danger\)/);
  assert.doesNotMatch(bulkBodies, /#ff5b8a|#fff6f9|rgba\(255,\s*91,\s*138/);
});

test("admin tag bulk actions use a fixed floating toolbar", () => {
  const css = mobileCss();
  const pageWithBulk = ruleBody(adminCss, ".admin-tags-page.has-bulk-actions");
  const toolbar = ruleBody(adminCss, ".admin-tags-bulk-toolbar");
  const actions = ruleBody(adminCss, ".admin-tags-bulk-actions");
  const count = ruleBodyByContains(adminCss, ".admin-tags-bulk-actions__count");
  const mobileToolbar = allRuleBodies(css, ".admin-tags-bulk-toolbar");
  const mobilePageWithBulk = allRuleBodies(css, ".admin-tags-page.has-bulk-actions");
  const mobileActions = allRuleBodies(css, ".admin-tags-bulk-actions");
  const mobileButton = allRuleBodies(css, ".admin-tags-bulk-actions__btn");

  assert.match(tagsPageSource, /className="admin-tags-bulk-toolbar"/);
  assert.match(tagsPageSource, /aria-label="标签批量操作"/);
  assert.match(tagsPageSource, /aria-pressed=\{isSelected\}/);
  assert.match(tagsPageSource, />已选择 \{selected\.size\} 项</);
  assert.match(tagsPageSource, /全选本页/);
  assert.match(tagsPageSource, /取消选中/);
  assert.doesNotMatch(tagsPageSource, /全选本页 \(/);
  assert.doesNotMatch(tagsPageSource, /CheckSquare/);
  assert.doesNotMatch(tagsPageSource, /admin-tag-card__check/);
  assert.doesNotMatch(tagsPageSource, /admin-tags-bulk-actions__select-all/);
  assert.doesNotMatch(tagsPageSource, /checked=\{allSelected\}/);
  assert.doesNotMatch(tagsPageSource, /admin-tags-bulkbar/);
  assert.doesNotMatch(adminCss, /admin-tags-bulkbar/);
  assert.doesNotMatch(adminCss, /admin-tag-card__check/);
  assert.match(pageWithBulk, /padding-bottom\s*:\s*72px/);
  assert.match(toolbar, /position\s*:\s*fixed/);
  assert.match(toolbar, /right\s*:\s*var\(--space-7\)/);
  assert.match(toolbar, /bottom\s*:\s*var\(--space-5\)/);
  assert.match(toolbar, /margin\s*:\s*0/);
  assert.match(actions, /display\s*:\s*inline-flex/);
  assert.match(count, /white-space\s*:\s*nowrap/);
  assert.match(mobileToolbar, /left\s*:\s*var\(--space-3\)/);
  assert.match(mobileToolbar, /right\s*:\s*var\(--space-3\)/);
  assert.match(mobileToolbar, /bottom\s*:\s*calc\(var\(--space-3\)\s*\+\s*env\(safe-area-inset-bottom\)\)/);
  assert.match(mobilePageWithBulk, /padding-bottom\s*:\s*calc\(104px\s*\+\s*env\(safe-area-inset-bottom\)\)/);
  assert.match(mobileActions, /display\s*:\s*grid/);
  assert.match(mobileActions, /grid-template-columns\s*:\s*repeat\(2,\s*minmax\(0,\s*1fr\)\)/);
  assert.match(mobileButton, /min-height\s*:\s*40px/);
  assert.match(mobileButton, /min-width\s*:\s*0/);
});

test("admin tag auto-generation setting is removed", () => {
  assert.doesNotMatch(tagsPageSource, /admin-tag-setting-toggle__switch/);
  assert.doesNotMatch(tagsPageSource, /role="switch"/);
  assert.doesNotMatch(tagsPageSource, /onClick=\{toggleAutoGenerateTags\}/);
  assert.doesNotMatch(tagsPageSource, /autoGenerateTagsEnabled/);
  assert.doesNotMatch(tagsPageSource, /admin-tag-setting-toggle__hint/);
  assert.doesNotMatch(tagsPageSource, /admin-tag-setting-toggle__body/);
  assert.doesNotMatch(tagsPageSource, /<label\s+className=\{`admin-tag-setting-toggle/);
  assert.doesNotMatch(adminCss, /admin-tag-setting-toggle/);
});

test("current video list does not render the drive summary under filters", () => {
  const shell = ruleBody(adminCss, ".admin-shell");
  const navGroupLabel = ruleBody(adminCss, ".admin-nav__group-label");
  const navLink = ruleBody(adminCss, ".admin-nav__link");
  const navText = ruleBody(adminCss, ".admin-nav__text");
  const sidebarFooterAction = ruleBodyByContains(adminCss, ".admin-sidebar__footer .admin-sidebar__check-update");
  const pagination = ruleBody(adminCss, ".admin-table-pagination");
  const filter = ruleBody(adminCss, ".admin-videos-filter");
  const toolbar = ruleBody(adminCss, ".admin-videos-list-toolbar");
  const currentToolbar = ruleBodyByContains(adminCss, ".admin-videos-current .admin-videos-list-toolbar");
  const currentWithBulk = ruleBodyByContains(adminCss, ".admin-videos-current.has-bulk-actions");

  assert.doesNotMatch(videosPageSource, /listSummary/);
  assert.doesNotMatch(videosPageSource, /全部网盘：共/);
  assert.doesNotMatch(videosPageSource, /withCounts/);
  assert.doesNotMatch(videosPageSource, /teaserReadyCount|teaserPendingCount/);
  assert.match(videosPageSource, /admin-videos-filter admin-videos-filter--current/);
  assert.doesNotMatch(videosPageSource, /aria-label="刷新当前视频"/);
  assert.match(videosPageSource, /admin-videos-filter__batch\$\{selectMode \? " is-primary" : ""\}/);
  assert.match(videosPageSource, /selectMode \? "退出选择" : "批量选择"/);
  assert.match(videosPageSource, /admin-videos-current\$\{selectedIds\.size > 0 \? " has-bulk-actions" : ""\}/);
  assert.match(videosPageSource, /\{!loading && selectedIds\.size > 0 && \(/);
  assert.match(shell, /--admin-sidebar-width\s*:\s*232px/);
  assert.match(shell, /grid-template-columns\s*:\s*var\(--admin-sidebar-width\)\s+minmax\(0,\s*1fr\)/);
  assert.match(navGroupLabel, /padding\s*:\s*0\s+12px/);
  assert.match(navGroupLabel, /text-align\s*:\s*left/);
  assert.match(navLink, /display\s*:\s*flex/);
  assert.match(navLink, /justify-content\s*:\s*flex-start/);
  assert.match(navLink, /text-align\s*:\s*left/);
  assert.match(navText, /justify-items\s*:\s*start/);
  assert.match(sidebarFooterAction, /display\s*:\s*flex/);
  assert.match(sidebarFooterAction, /justify-content\s*:\s*center/);
  assert.match(pagination, /justify-content\s*:\s*center/);
  assert.match(filter, /margin-bottom\s*:\s*var\(--space-4\)/);
  assert.match(toolbar, /margin\s*:\s*var\(--space-2\)\s+0\s+var\(--space-4\)/);
  assert.match(currentToolbar, /position\s*:\s*fixed/);
  assert.match(currentToolbar, /left\s*:\s*auto/);
  assert.match(currentToolbar, /right\s*:\s*var\(--space-7\)/);
  assert.match(currentToolbar, /bottom\s*:\s*var\(--space-5\)/);
  assert.match(currentToolbar, /max-width\s*:\s*calc\(100vw\s*-\s*var\(--admin-sidebar-width\)\s*-\s*\(var\(--space-7\)\s*\*\s*2\)\)/);
  assert.match(currentToolbar, /margin\s*:\s*0/);
  assert.match(currentWithBulk, /padding-bottom\s*:\s*72px/);
});

test("desktop current video list uses long cards without a header row", () => {
  const table = ruleBody(adminCss, ".admin-videos-current .admin-videos-table");
  const body = ruleBody(adminCss, ".admin-videos-current .admin-videos-table tbody");
  const row = ruleBody(adminCss, ".admin-videos-current .admin-videos-table tr");
  const selected = ruleBody(adminCss, ".admin-videos-current .admin-videos-table tr.is-selected");
  const cell = ruleBody(adminCss, ".admin-videos-current .admin-videos-table td");
  const label = ruleBody(adminCss, ".admin-videos-current .admin-videos-table td::before");
  const titleCell = ruleBody(adminCss, ".admin-videos-current .admin-videos-table .admin-video-title-cell");
  const thumb = ruleBody(adminCss, ".admin-videos-current .admin-videos-table .admin-video-thumb-wrap");
  const actions = ruleBody(adminCss, ".admin-videos-current .admin-videos-table td.is-actions");

  assert.match(videosPageSource, /admin-table is-selectable admin-videos-table/);
  assert.match(videosPageSource, /<tbody>\s*\{\s*listItems\.map/);
  assert.doesNotMatch(videosPageSource, /<th>标题<\/th>/);
  assert.doesNotMatch(videosPageSource, /<th>作者<\/th>/);
  assert.doesNotMatch(videosPageSource, /<th>时长<\/th>/);
  assert.doesNotMatch(videosPageSource, /<th>预览视频<\/th>/);
  assert.match(table, /display\s*:\s*block/);
  assert.match(table, /background\s*:\s*transparent/);
  assert.match(body, /display\s*:\s*grid/);
  assert.match(body, /gap\s*:\s*10px/);
  assert.match(row, /display\s*:\s*grid/);
  assert.match(row, /grid-template-columns\s*:\s*[\s\S]*minmax\(320px,\s*1\.55fr\)/);
  assert.match(row, /min-height\s*:\s*80px/);
  assert.match(row, /border-radius\s*:\s*8px/);
  assert.match(selected, /background\s*:\s*var\(--accent-soft\)/);
  assert.match(cell, /background\s*:\s*transparent\s*!important/);
  assert.match(label, /content\s*:\s*none/);
  assert.match(titleCell, /width\s*:\s*100%/);
  assert.match(thumb, /width\s*:\s*96px/);
  assert.match(thumb, /height\s*:\s*54px/);
  assert.match(actions, /justify-content\s*:\s*flex-end/);
});

test("desktop video management filters use a stable three-column toolbar", () => {
  const css = adminCss;
  const currentFilter = ruleBodyByContains(css, ".admin-videos-filter--current");
  const blacklistFilter = ruleBodyByContains(css, ".admin-videos-filter--blacklist");
  const currentFilterSelect = ruleBodyByContains(css, ".admin-videos-filter--current .admin-videos-filter__select-wrap");
  const blacklistFilterSelect = ruleBodyByContains(css, ".admin-videos-filter--blacklist .admin-videos-filter__select-wrap");
  const currentSelect = ruleBodyByContains(css, ".admin-videos-filter--current .admin-videos-filter__select");
  const blacklistSelect = ruleBodyByContains(css, ".admin-videos-filter--blacklist .admin-videos-filter__select");
  const currentIcon = ruleBodyByContains(css, ".admin-videos-filter--current .admin-videos-filter__select-icon");
  const blacklistIcon = ruleBodyByContains(css, ".admin-videos-filter--blacklist .admin-videos-filter__select-icon");
  const currentFilterSearch = ruleBodyByContains(css, ".admin-videos-filter--current .admin-videos-filter__search");
  const blacklistFilterSearch = ruleBodyByContains(css, ".admin-videos-filter--blacklist .admin-videos-filter__search");
  const batch = ruleBody(css, ".admin-videos-filter__batch");

  assert.doesNotMatch(videosPageSource, /aria-label="刷新当前视频"/);
  assert.doesNotMatch(videosPageSource, /aria-label="刷新拉黑视频"/);
  assert.match(videosPageSource, /admin-videos-filter__batch/);
  assert.match(currentFilter, /display\s*:\s*grid/);
  assert.match(currentFilter, /grid-template-columns\s*:\s*72px\s+minmax\(0,\s*1fr\)\s+auto/);
  assert.match(currentFilter, /width\s*:\s*100%/);
  assert.match(blacklistFilter, /display\s*:\s*grid/);
  assert.match(blacklistFilter, /grid-template-columns\s*:\s*72px\s+minmax\(0,\s*1fr\)\s+auto/);
  assert.match(blacklistFilter, /width\s*:\s*100%/);
  assert.match(currentFilterSelect, /min-width\s*:\s*0/);
  assert.match(blacklistFilterSelect, /min-width\s*:\s*0/);
  assert.match(currentSelect, /padding\s*:\s*0\s+4px/);
  assert.match(blacklistSelect, /padding\s*:\s*0\s+4px/);
  assert.match(currentSelect, /text-align\s*:\s*center/);
  assert.match(blacklistSelect, /text-align\s*:\s*center/);
  assert.match(currentIcon, /display\s*:\s*none/);
  assert.match(blacklistIcon, /display\s*:\s*none/);
  assert.match(currentFilterSearch, /min-width\s*:\s*0/);
  assert.match(blacklistFilterSearch, /min-width\s*:\s*0/);
  assert.match(batch, /white-space\s*:\s*nowrap/);
  assert.doesNotMatch(batch, /display\s*:\s*none/);
});

test("admin table action headers center-align with action buttons", () => {
  const actionHeader = ruleBody(adminCss, ".admin-table th.is-actions");
  const actionCell = ruleBody(adminCss, ".admin-table td.is-actions");

  assert.match(actionHeader, /text-align\s*:\s*center/);
  assert.match(actionCell, /text-align\s*:\s*center/);
});

test("blacklist restore action uses a light button style", () => {
  const restoreButton = ruleBody(adminCss, ".admin-blacklist-restore-btn");
  const unavailable = ruleBody(adminCss, ".admin-blacklist-unavailable");

  assert.match(videosPageSource, /const \[driveId, setDriveId\] = useState\(""\);/);
  assert.match(videosPageSource, /api\.listBlacklist\(\{ driveId, page, size: pageSize, keyword: searchKeyword \}\)/);
  assert.match(videosPageSource, /admin-videos-filter admin-videos-filter--blacklist/);
  assert.match(videosPageSource, /<DriveFilter drives=\{drives\} driveId=\{driveId\}/);
  assert.match(apiSource, /listBlacklist\(\s*params: \{ driveId\?: string; page\?: number; size\?: number; keyword\?: string \}/);
  assert.match(apiSource, /if \(params\.driveId\) qs\.set\("driveId", params\.driveId\);/);
  assert.match(videosPageSource, /className="admin-btn admin-blacklist-restore-btn"/);
  assert.match(videosPageSource, /v\.restorePolicy !== "none"/);
  assert.match(videosPageSource, /允许重新入库/);
  assert.match(videosPageSource, /此操作不会立即扫盘/);
  assert.match(videosPageSource, /此操作不会立即运行爬虫/);
  assert.match(videosPageSource, /v\.sourceDeleted/);
  assert.match(videosPageSource, /v\.driveId === "local-upload"/);
  assert.doesNotMatch(videosPageSource, /被删除和被隐藏的视频会进入黑名单/);
  assert.doesNotMatch(videosPageSource, /原始记录、封面、预览已删除/);
  assert.match(restoreButton, /background\s*:\s*var\(--accent-softer\)/);
  assert.match(restoreButton, /color\s*:\s*var\(--accent\)/);
  assert.doesNotMatch(restoreButton, /background\s*:\s*var\(--accent\)/);
  assert.match(unavailable, /color\s*:\s*var\(--text-faint\)/);
});

test("blacklist duplicate reason renders as a compact pill", () => {
  const pill = ruleBody(adminCss, ".admin-blacklist-reason-pill");
  const canonicalButton = ruleBody(adminCss, ".admin-blacklist-canonical-btn");

  assert.match(videosPageSource, /admin-blacklist-reason-pill/);
  assert.match(videosPageSource, /重复文件/);
  assert.match(videosPageSource, /v\.canonicalVideoId/);
  assert.match(videosPageSource, /查看保留视频/);
  assert.doesNotMatch(videosPageSource, /保留视频不可用/);
  assert.match(pill, /border-radius\s*:\s*999px/);
  assert.match(pill, /white-space\s*:\s*nowrap/);
  assert.match(canonicalButton, /background\s*:\s*var\(--surface-2\)/);
});

test("blacklist source files can be deleted by one serialized background task", () => {
  const action = ruleBody(adminCss, ".admin-blacklist-source-delete");
  const status = ruleBody(adminCss, ".admin-blacklist-source-delete__status");
  const button = ruleBody(adminCss, ".admin-blacklist-source-delete__button");
  const rowActions = ruleBody(adminCss, ".admin-blacklist-actions");
  const rowDelete = ruleBody(adminCss, ".admin-blacklist-delete-source-btn");

  assert.match(apiSource, /startBlacklistSourceDelete/);
  assert.match(apiSource, /getBlacklistSourceDeleteStatus/);
  assert.match(apiSource, /ids\?: string\[\]/);
  assert.match(videosPageSource, /删除全部/);
  assert.match(videosPageSource, /批量删除/);
  assert.match(videosPageSource, /删除拉黑视频源文件/);
  assert.match(videosPageSource, /\{ ids: \[target\.id\] \}/);
  assert.match(videosPageSource, /\{ ids \}/);
  assert.match(videosPageSource, /admin-table is-selectable admin-blacklist-table\$\{selectMode \? " is-row-select-mode" : ""\}/);
  assert.doesNotMatch(videosPageSource, /admin-table-checkbox-btn/);
  assert.match(videosPageSource, /后台逐个删除/);
  assert.match(videosPageSource, /sourceDeleteStatus\.processed/);
  assert.match(videosPageSource, /sourceDeleteStatus\.total/);
  assert.match(action, /display\s*:\s*flex/);
  assert.match(status, /font-size\s*:\s*var\(--font-xs\)/);
  assert.match(button, /white-space\s*:\s*nowrap/);
  assert.match(rowActions, /display\s*:\s*flex/);
  assert.match(rowActions, /flex-wrap\s*:\s*wrap/);
  assert.match(rowDelete, /white-space\s*:\s*nowrap/);
});

test("admin video management controls wrap instead of covering text on mobile", () => {
  const css = mobileCss();
  const paginationInfo = allRuleBodies(css, ".admin-table-pagination__info");
  const currentFilter = ruleBody(css, ".admin-videos-filter--current");
  const currentFilterField = ruleBodyByContains(css, ".admin-videos-filter--current .admin-videos-filter__search");
  const currentFilterSelect = ruleBody(css, ".admin-videos-filter--current .admin-videos-filter__select");
  const currentFilterIcon = ruleBody(css, ".admin-videos-filter--current .admin-videos-filter__select-icon");
  const currentFilterBatch = ruleBodyByContains(css, ".admin-videos-filter--current .admin-videos-filter__batch");
  const blacklistFilter = allRuleBodies(css, ".admin-videos-filter--blacklist");
  const blacklistFilterField = ruleBodyByContains(css, ".admin-videos-filter--blacklist .admin-videos-filter__search");
  const blacklistFilterSelect = ruleBodyByContains(css, ".admin-videos-filter--blacklist .admin-videos-filter__select");
  const blacklistFilterIcon = ruleBodyByContains(css, ".admin-videos-filter--blacklist .admin-videos-filter__select-icon");
  const blacklistFilterBatch = ruleBodyByContains(css, ".admin-videos-filter--blacklist .admin-videos-filter__batch");
  const bulkToolbar = ruleBodyByContains(css, ".admin-videos-current .admin-videos-list-toolbar");
  const blacklistBulkToolbar = ruleBodyByContains(css, ".admin-blacklist-bulk-toolbar");
  const currentWithBulk = ruleBodyByContains(css, ".admin-videos-current.has-bulk-actions");
  const blacklistWithBulk = ruleBodyByContains(css, ".admin-videos-blacklist.has-bulk-actions");
  const bulkActions = allRuleBodies(css, ".admin-videos-bulk-actions");
  const bulkCount = allRuleBodies(css, ".admin-videos-bulk-actions__count");
  const bulkButton = allRuleBodies(css, ".admin-videos-bulk-actions__btn");
  const blacklistBulkButton = ruleBody(css, ".admin-blacklist-bulk-toolbar .admin-videos-bulk-actions__btn");
  const blacklistName = ruleBody(
    css,
    '.admin-blacklist-table:not(.admin-drives-table) td[data-label="文件名"]'
  );
  const blacklistTime = ruleBody(
    css,
    '.admin-blacklist-table:not(.admin-drives-table) td[data-label="拉黑时间"]'
  );
  const blacklistActions = ruleBody(
    css,
    ".admin-blacklist-table:not(.admin-drives-table) td.is-actions"
  );
  const blacklistActionsLabel = ruleBody(
    css,
    ".admin-blacklist-table:not(.admin-drives-table) td.is-actions::before"
  );
  const blacklistActionButton = ruleBody(
    css,
    ".admin-blacklist-table:not(.admin-drives-table) td.is-actions .admin-btn"
  );
  const blacklistCard = ruleBody(css, ".admin-blacklist-table:not(.admin-drives-table) tr");
  const blacklistSelected = ruleBody(css, ".admin-blacklist-table:not(.admin-drives-table) tr.is-selected");

  assert.match(paginationInfo, /flex\s*:\s*1\s+0\s+100%/);
  assert.match(currentFilter, /display\s*:\s*grid/);
  assert.match(currentFilter, /grid-template-columns\s*:\s*72px\s+minmax\(0,\s*1fr\)\s+auto/);
  assert.match(currentFilterField, /min-width\s*:\s*0/);
  assert.match(currentFilterSelect, /padding\s*:\s*0\s+4px/);
  assert.match(currentFilterSelect, /text-align\s*:\s*center/);
  assert.match(currentFilterSelect, /text-align-last\s*:\s*center/);
  assert.match(currentFilterSelect, /text-overflow\s*:\s*ellipsis/);
  assert.match(currentFilterIcon, /display\s*:\s*none/);
  assert.match(currentFilterBatch, /min-width\s*:\s*54px/);
  assert.match(currentFilterBatch, /white-space\s*:\s*nowrap/);
  assert.match(blacklistFilter, /display\s*:\s*grid/);
  assert.match(blacklistFilter, /grid-template-columns\s*:\s*72px\s+minmax\(0,\s*1fr\)\s+auto/);
  assert.match(blacklistFilterField, /min-width\s*:\s*0/);
  assert.match(blacklistFilterSelect, /padding\s*:\s*0\s+4px/);
  assert.match(blacklistFilterSelect, /text-align\s*:\s*center/);
  assert.match(blacklistFilterSelect, /text-align-last\s*:\s*center/);
  assert.match(blacklistFilterIcon, /display\s*:\s*none/);
  assert.match(blacklistFilterBatch, /white-space\s*:\s*nowrap/);
  assert.match(bulkToolbar, /position\s*:\s*fixed/);
  assert.match(bulkToolbar, /bottom\s*:\s*calc\(var\(--space-3\)\s*\+\s*env\(safe-area-inset-bottom\)\)/);
  assert.match(bulkToolbar, /margin\s*:\s*0/);
  assert.match(blacklistBulkToolbar, /position\s*:\s*fixed/);
  assert.match(blacklistBulkToolbar, /bottom\s*:\s*calc\(var\(--space-3\)\s*\+\s*env\(safe-area-inset-bottom\)\)/);
  assert.match(blacklistBulkToolbar, /margin\s*:\s*0/);
  assert.match(currentWithBulk, /padding-bottom\s*:\s*calc\(104px\s*\+\s*env\(safe-area-inset-bottom\)\)/);
  assert.match(blacklistWithBulk, /padding-bottom\s*:\s*calc\(104px\s*\+\s*env\(safe-area-inset-bottom\)\)/);
  assert.match(bulkActions, /display\s*:\s*grid/);
  assert.match(bulkActions, /grid-template-columns\s*:\s*repeat\(2,\s*minmax\(0,\s*1fr\)\)/);
  assert.match(bulkCount, /grid-column\s*:\s*1\s*\/\s*-1/);
  assert.match(bulkButton, /min-height\s*:\s*40px/);
  assert.match(bulkButton, /min-width\s*:\s*0/);
  assert.match(blacklistBulkButton, /grid-column\s*:\s*1\s*\/\s*-1/);
  assert.match(videosPageSource, /admin-videos-blacklist/);
  assert.match(blacklistName, /grid-column\s*:\s*1\s*\/\s*-1/);
  assert.match(blacklistTime, /grid-column\s*:\s*1/);
  assert.match(blacklistActions, /grid-column\s*:\s*2/);
  assert.match(blacklistCard, /--admin-video-card-bg\s*:\s*var\(--bg-surface\)/);
  assert.match(blacklistSelected, /background\s*:\s*var\(--admin-video-card-bg\)/);
  assert.match(blacklistSelected, /box-shadow\s*:\s*var\(--admin-video-card-selected-shadow\)/);
  assert.match(blacklistActions, /justify-content\s*:\s*flex-end/);
  assert.match(blacklistActionsLabel, /content\s*:\s*none/);
  assert.match(blacklistActionButton, /white-space\s*:\s*normal/);
});

test("admin loading spinner rotates around icon center", () => {
  const spinner = ruleBody(adminCss, ".admin-spin");
  const reducedMotion = ruleBodyByContains(adminCss, ".admin-sidebar__check-update:disabled svg");

  assert.match(spinner, /animation\s*:\s*admin-update-spin\s+0\.9s\s+linear\s+infinite/);
  assert.match(spinner, /transform-box\s*:\s*fill-box/);
  assert.match(spinner, /transform-origin\s*:\s*center/);
  assert.match(spinner, /will-change\s*:\s*transform/);
  assert.match(reducedMotion, /animation-duration\s*:\s*0\.9s\s*!important/);
});

test("mobile video management uses compact theme-aware video cards", () => {
  const css = mobileCss();
  const card = ruleBody(css, ".admin-videos-table:not(.admin-drives-table) tr");
  const title = ruleBody(css, ".admin-videos-table:not(.admin-drives-table) td[data-label=\"标题\"]");
  const label = ruleBody(css, ".admin-videos-table:not(.admin-drives-table) td::before");
  const titleCell = ruleBody(css, ".admin-videos-table:not(.admin-drives-table) .admin-video-title-cell");
  const thumb = ruleBody(css, ".admin-videos-table:not(.admin-drives-table) .admin-video-thumb-wrap");
  const titleText = ruleBody(css, ".admin-videos-table:not(.admin-drives-table) .admin-video-title");
  const pills = ruleBody(css, ".admin-videos-table:not(.admin-drives-table) .admin-video-filemeta-pills");
  const authorColumn = ruleBodyByContains(css, ".admin-videos-table:not(.admin-drives-table) td[data-label=\"作者\"]");
  const sourceColumn = ruleBodyByContains(css, ".admin-videos-table:not(.admin-drives-table) td[data-label=\"来源\"]");
  const durationColumn = ruleBodyByContains(css, ".admin-videos-table:not(.admin-drives-table) td[data-label=\"时长\"]");
  const previewColumn = ruleBodyByContains(css, ".admin-videos-table:not(.admin-drives-table) td[data-label=\"预览视频\"]");
  const actions = ruleBody(css, ".admin-videos-table:not(.admin-drives-table) td.is-actions");
  const actionsLabel = ruleBody(css, ".admin-videos-table:not(.admin-drives-table) td.is-actions::before");
  const status = ruleBody(css, ".admin-videos-table:not(.admin-drives-table) .admin-status");
  const statusDot = ruleBody(css, ".admin-videos-table:not(.admin-drives-table) .admin-status::before");
  const actionButton = ruleBody(css, ".admin-videos-table:not(.admin-drives-table) td.is-actions .admin-btn");
  const dangerButton = ruleBody(css, ".admin-videos-table:not(.admin-drives-table) td.is-actions .admin-btn.is-danger");

  assert.match(card, /--admin-video-card-bg\s*:\s*var\(--bg-surface\)/);
  assert.match(card, /background\s*:\s*var\(--admin-video-card-bg\)/);
  assert.match(card, /border-radius\s*:\s*14px/);
  assert.match(card, /padding\s*:\s*12px\s+14px/);
  assert.match(card, /grid-template-columns\s*:\s*repeat\(12,\s*minmax\(0,\s*1fr\)\)/);
  assert.match(card, /gap\s*:\s*0\s+10px/);
  assert.match(css, /:root:not\(\[data-theme="pink"\]\)\s+\.admin-videos-table:not\(\.admin-drives-table\)\s+tr\s*\{[^}]*--admin-video-card-bg\s*:\s*#1e1e1e/s);
  assert.match(css, /:root\[data-theme="pink"\]\s+\.admin-videos-table:not\(\.admin-drives-table\)\s+tr\s*\{/);
  assert.doesNotMatch(videosPageSource, /className="is-checkbox"/);
  assert.doesNotMatch(videosPageSource, /admin-table-checkbox-btn/);
  assert.match(title, /padding-left\s*:\s*0/);
  assert.match(title, /min-height\s*:\s*72px/);
  assert.match(label, /font-size\s*:\s*10px/);
  assert.match(label, /letter-spacing\s*:\s*0\.06em/);
  assert.match(titleCell, /grid-template-columns\s*:\s*clamp\(104px,\s*32vw,\s*156px\)\s+minmax\(0,\s*1fr\)/);
  assert.match(thumb, /aspect-ratio\s*:\s*16\s*\/\s*9/);
  assert.match(thumb, /border-radius\s*:\s*8px/);
  assert.match(titleText, /-webkit-line-clamp\s*:\s*2/);
  assert.match(titleText, /overflow-wrap\s*:\s*anywhere/);
  assert.match(videosPageSource, /loading="lazy"\s+decoding="async"/);
  assert.match(videosPageSource, /className="admin-video-title" title=\{v\.title\}/);
  assert.match(pills, /display\s*:\s*flex/);
  assert.doesNotMatch(videosPageSource, /admin-video-filemeta-pill is-category/);
  assert.doesNotMatch(css, /admin-video-card-category/);
  assert.match(authorColumn, /display\s*:\s*none/);
  assert.match(sourceColumn, /grid-row\s*:\s*2/);
  assert.match(sourceColumn, /grid-column\s*:\s*1\s*\/\s*5/);
  assert.match(sourceColumn, /justify-items\s*:\s*start/);
  assert.match(sourceColumn, /text-overflow\s*:\s*ellipsis/);
  assert.match(durationColumn, /grid-row\s*:\s*2/);
  assert.match(durationColumn, /grid-column\s*:\s*5\s*\/\s*9/);
  assert.match(durationColumn, /justify-items\s*:\s*center/);
  assert.match(previewColumn, /grid-row\s*:\s*2/);
  assert.match(previewColumn, /grid-column\s*:\s*9\s*\/\s*-1/);
  assert.match(previewColumn, /justify-items\s*:\s*end/);
  assert.match(actions, /grid-column\s*:\s*1\s*\/\s*-1/);
  assert.match(actions, /grid-row\s*:\s*3/);
  assert.match(actions, /display\s*:\s*grid/);
  assert.match(actions, /grid-template-columns\s*:\s*repeat\(3,\s*minmax\(0,\s*1fr\)\)/);
  assert.match(actions, /gap\s*:\s*10px/);
  assert.match(actionsLabel, /content\s*:\s*none/);
  assert.match(status, /gap\s*:\s*0/);
  assert.match(statusDot, /content\s*:\s*none/);
  assert.doesNotMatch(sourceColumn, /border-left/);
  assert.match(actionButton, /width\s*:\s*100%/);
  assert.match(actionButton, /height\s*:\s*32px/);
  assert.match(actionButton, /justify-content\s*:\s*center/);
  assert.match(actionButton, /border-radius\s*:\s*8px/);
  assert.match(dangerButton, /border-color\s*:\s*var\(--admin-video-card-danger-border\)/);
  assert.match(dangerButton, /color\s*:\s*var\(--admin-video-card-danger\)/);
});

test("video edit modal stays focused on common metadata", () => {
  assert.match(videosPageSource, /ariaLabel="编辑视频"/);
  assert.doesNotMatch(videosPageSource, /title=\{`编辑视频 ·/);
  assert.doesNotMatch(videosPageSource, /const \[badges, setBadges\]/);
  assert.doesNotMatch(videosPageSource, /const \[thumbnail, setThumbnail\]/);
  assert.doesNotMatch(videosPageSource, /const \[quality, setQuality\]/);
  assert.doesNotMatch(videosPageSource, /video-badges/);
  assert.doesNotMatch(videosPageSource, /video-quality/);
  assert.doesNotMatch(videosPageSource, /video-thumbnail/);
  assert.doesNotMatch(videosPageSource, /徽标（/);
  assert.doesNotMatch(videosPageSource, /封面 URL/);
  assert.doesNotMatch(videosPageSource, /封面预览/);
  assert.doesNotMatch(videosPageSource, /badges:\s*splitList\(badges\)/);
  assert.doesNotMatch(videosPageSource, /thumbnail:\s*thumbnail\.trim\(\)/);
  assert.doesNotMatch(videosPageSource, /quality:\s*quality\.trim\(\)/);
});

test("admin modals and action footers adapt on mobile", () => {
  const css = mobileCss();

  // .admin-modal 桌面段已用 `width: min(620px, 100%)`，窄屏自然 100%；mobile 段
  // 只重写 max-height，所以这里断桌面规则即可。
  assert.match(ruleBody(adminCss, ".admin-modal"), /width\s*:\s*min\(\d+px,\s*100%\)/);
  assert.match(ruleBody(adminCss, ".admin-modal.admin-modal--crawler"), /width\s*:\s*min\(1080px,\s*100%\)/);
  assert.match(allRuleBodies(css, ".admin-modal"), /display\s*:\s*flex/);
  assert.match(allRuleBodies(css, ".admin-modal"), /overflow\s*:\s*hidden/);
  assert.match(allRuleBodies(css, ".admin-modal__body"), /overflow-y\s*:\s*auto/);
  assert.match(allRuleBodies(css, ".admin-modal-backdrop"), /safe-area-inset-top/);
  assert.match(allRuleBodies(css, ".admin-modal-backdrop"), /place-items\s*:\s*center/);
  assert.doesNotMatch(allRuleBodies(css, ".admin-modal-backdrop"), /align-items\s*:\s*stretch/);
  // 多按钮 footer 在 mobile 下要换行避免溢出。
  assert.match(allRuleBodies(css, ".admin-modal__footer"), /flex-wrap\s*:\s*wrap/);
  // 删除/放弃类确认弹窗在 mobile 下不能跟随通用 modal stretch 到顶部。
  const confirmModal = ruleBody(css, ".admin-modal--delete-confirm");
  assert.match(confirmModal, /align-self\s*:\s*center/);
  assert.match(confirmModal, /justify-self\s*:\s*center/);
  assert.match(ruleBody(adminCss, ".admin-modal__header.is-titleless"), /justify-content\s*:\s*flex-end/);
  // 表单 input/select/textarea 在 mobile 下铺满。规则用逗号合并写法（多 selector
  // 共享 body），所以走 ruleBodyByContains 而不是简单正则。
  assert.match(ruleBodyByContains(css, ".admin-form__row input"), /width\s*:\s*100%/);
});

test("mobile drive type picker uses compact three-column cards", () => {
  const driveTypeGridBodies = allRuleBodies(adminCss, ".admin-drive-type-grid");
  const driveTypeCardBodies = allRuleBodies(adminCss, ".admin-drive-type-card");
  const driveTypeIconBodies = allRuleBodies(adminCss, ".admin-drive-type-card__icon");

  assert.match(driveTypeGridBodies, /grid-template-columns\s*:\s*repeat\(3,\s*minmax\(0,\s*1fr\)\)/);
  assert.doesNotMatch(driveTypeGridBodies, /grid-template-columns\s*:\s*repeat\(2,\s*1fr\)/);
  assert.match(driveTypeCardBodies, /min-height\s*:\s*94px/);
  assert.match(driveTypeIconBodies, /width\s*:\s*38px/);
  assert.match(driveTypeIconBodies, /height\s*:\s*38px/);
});

test("mobile tags management does not create horizontal page overflow", () => {
  const css = mobileCss();
  const layout = allRuleBodies(css, ".admin-tags-layout");
  const desktopLayout = allRuleBodies(adminCss, ".admin-tags-layout");
  const main = allRuleBodies(adminCss, ".admin-tags-main");
  const board = allRuleBodies(adminCss, ".admin-tags-board");
  const mobileBoard = allRuleBodies(css, ".admin-tags-board");
  const toolbar = allRuleBodies(css, ".admin-tags-toolbar");
  const search = allRuleBodies(css, ".admin-tags-search");
  const filters = allRuleBodies(css, ".admin-tags-filter-tabs");
  const desktopFilters = allRuleBodies(adminCss, ".admin-tags-filter-tabs");
  const filterPanel = allRuleBodies(css, ".admin-tags-filter-panel");
  const desktopFilterPanel = ruleBody(adminCss, ".admin-tags-filter-panel");
  const filterTab = allRuleBodies(adminCss, ".admin-tags-filter-tab");
  const filterTabText = allRuleBodies(adminCss, ".admin-tags-filter-tab__text");
  const mobileFilterTabText = allRuleBodies(css, ".admin-tags-filter-tab__text");
  const grid = allRuleBodies(css, ".admin-tags-grid");
  const card = allRuleBodies(css, ".admin-tag-card");
  const cardFooter = allRuleBodies(adminCss, ".admin-tag-card__footer");
  const cardCount = allRuleBodies(adminCss, ".admin-tag-card__count");
  const cardActions = allRuleBodies(adminCss, ".admin-tag-card__footer-actions");
  const cardEdit = allRuleBodies(adminCss, ".admin-tag-card__edit");
  const cardDelete = allRuleBodies(adminCss, ".admin-tag-card__delete");
  const pagination = allRuleBodies(css, ".admin-tags-pagination");
  const paginationInfo = allRuleBodies(css, ".admin-tags-pagination .admin-table-pagination__info");

  assert.match(desktopLayout, /grid-template-columns\s*:\s*minmax\(0,\s*1fr\)/);
  assert.match(main, /--tags-cards-width\s*:\s*calc\(\(240px \* 4\) \+ \(var\(--space-3\) \* 3\)\)/);
  assert.doesNotMatch(board, /--tags-filter-width|--tags-board-width/);
  assert.match(board, /grid-template-columns\s*:\s*minmax\(0,\s*var\(--tags-cards-width\)\)/);
  assert.match(board, /justify-content\s*:\s*center/);
  assert.match(board, /align-items\s*:\s*stretch/);
  assert.match(layout, /width\s*:\s*100%/);
  assert.match(layout, /max-width\s*:\s*100%/);
  assert.match(layout, /overflow-x\s*:\s*clip/);
  assert.match(mobileBoard, /grid-template-columns\s*:\s*1fr/);
  assert.match(allRuleBodies(adminCss, ".admin-tags-toolbar"), /grid-template-columns\s*:\s*minmax\(0,\s*1fr\)\s+minmax\(240px,\s*360px\)\s+minmax\(0,\s*1fr\)/);
  assert.match(toolbar, /max-width\s*:\s*100%/);
  assert.match(toolbar, /grid-template-columns\s*:\s*1fr/);
  assert.match(toolbar, /justify-items\s*:\s*stretch/);
  assert.match(allRuleBodies(adminCss, ".admin-tags-search"), /grid-column\s*:\s*2/);
  assert.match(allRuleBodies(adminCss, ".admin-tags-search"), /grid-row\s*:\s*1/);
  assert.match(allRuleBodies(adminCss, ".admin-tags-search"), /justify-self\s*:\s*center/);
  assert.match(search, /grid-column\s*:\s*1/);
  assert.match(search, /grid-row\s*:\s*1/);
  assert.match(search, /width\s*:\s*100%/);
  assert.match(search, /min-width\s*:\s*0/);
  assert.match(desktopFilterPanel, /grid-column\s*:\s*2/);
  assert.match(desktopFilterPanel, /grid-row\s*:\s*2/);
  assert.match(desktopFilterPanel, /justify-self\s*:\s*center/);
  assert.match(desktopFilterPanel, /display\s*:\s*flex/);
  assert.match(desktopFilterPanel, /justify-content\s*:\s*center/);
  assert.match(desktopFilterPanel, /width\s*:\s*auto/);
  assert.match(desktopFilterPanel, /max-width\s*:\s*100%/);
  assert.match(desktopFilterPanel, /min-width\s*:\s*0/);
  assert.match(desktopFilterPanel, /margin\s*:\s*0/);
  assert.doesNotMatch(desktopFilterPanel, /position\s*:\s*(fixed|sticky)/);
  assert.doesNotMatch(desktopFilterPanel, /\bleft\s*:/);
  assert.doesNotMatch(desktopFilterPanel, /\btop\s*:/);
  assert.doesNotMatch(desktopFilterPanel, /transform\s*:/);
  assert.match(filterPanel, /grid-row\s*:\s*2/);
  assert.match(filterPanel, /width\s*:\s*100%/);
  assert.match(desktopFilters, /display\s*:\s*flex/);
  assert.match(desktopFilters, /flex-direction\s*:\s*row/);
  assert.match(desktopFilters, /gap\s*:\s*4px/);
  assert.match(desktopFilters, /padding\s*:\s*3px/);
  assert.match(desktopFilters, /width\s*:\s*auto/);
  assert.match(desktopFilters, /min-height\s*:\s*0/);
  assert.match(desktopFilters, /max-width\s*:\s*100%/);
  assert.match(desktopFilters, /overflow-x\s*:\s*auto/);
  assert.doesNotMatch(desktopFilters, /height\s*:\s*var\(--tags-filter-height\)/);
  assert.doesNotMatch(adminCss, /admin-tags-filter-tab__count/);
  assert.match(filterTab, /flex\s*:\s*0 0 auto/);
  assert.match(filterTab, /width\s*:\s*auto/);
  assert.match(filterTab, /min-height\s*:\s*0/);
  assert.match(filterTab, /flex-direction\s*:\s*row/);
  assert.match(filterTab, /padding\s*:\s*6px\s+12px/);
  assert.match(filterTab, /text-align\s*:\s*center/);
  assert.match(filterTab, /white-space\s*:\s*nowrap/);
  assert.match(filterTabText, /writing-mode\s*:\s*horizontal-tb/);
  assert.match(filterTabText, /text-orientation\s*:\s*mixed/);
  assert.match(filters, /width\s*:\s*100%/);
  assert.match(filters, /min-width\s*:\s*0/);
  assert.match(filters, /max-width\s*:\s*100%/);
  assert.match(filters, /flex-direction\s*:\s*row/);
  assert.match(filters, /overflow-x\s*:\s*auto/);
  assert.match(mobileFilterTabText, /writing-mode\s*:\s*horizontal-tb/);
  assert.match(allRuleBodies(adminCss, ".admin-tags-grid"), /display\s*:\s*grid/);
  assert.match(allRuleBodies(adminCss, ".admin-tags-grid"), /grid-template-columns\s*:\s*repeat\(4,\s*minmax\(0,\s*240px\)\)/);
  assert.match(allRuleBodies(adminCss, ".admin-tags-grid"), /width\s*:\s*min\(100%,\s*var\(--tags-cards-width\)\)/);
  assert.match(allRuleBodies(adminCss, ".admin-tags-grid"), /margin\s*:\s*0 auto/);
  assert.match(allRuleBodies(adminCss, ".admin-tags-grid"), /justify-content\s*:\s*start/);
  assert.match(allRuleBodies(adminCss, ".admin-tags-grid"), /align-items\s*:\s*stretch/);
  assert.doesNotMatch(allRuleBodies(adminCss, ".admin-tags-grid"), /display\s*:\s*flex/);
  assert.doesNotMatch(allRuleBodies(adminCss, ".admin-tags-grid"), /flex-wrap\s*:\s*wrap/);
  assert.match(grid, /grid-template-columns\s*:\s*1fr/);
  assert.match(grid, /justify-content\s*:\s*stretch/);
  assert.match(grid, /max-width\s*:\s*100%/);
  assert.doesNotMatch(card, /flex-basis/);
  assert.match(card, /width\s*:\s*100%/);
  assert.match(card, /max-width\s*:\s*100%/);
  assert.match(cardFooter, /flex-wrap\s*:\s*nowrap/);
  assert.match(cardCount, /white-space\s*:\s*nowrap/);
  assert.match(cardActions, /min-width\s*:\s*0/);
  assert.match(cardActions, /white-space\s*:\s*nowrap/);
  assert.match(cardEdit, /white-space\s*:\s*nowrap/);
  assert.match(cardDelete, /white-space\s*:\s*nowrap/);
  assert.match(cardDelete, /width\s*:\s*0/);
  assert.match(cardDelete, /padding-inline\s*:\s*0/);
  assert.match(pagination, /min-width\s*:\s*0/);
  assert.match(paginationInfo, /overflow-wrap\s*:\s*anywhere/);
});

test("mobile admin top navigation stays compact", () => {
  const css = mobileCss();

  assert.match(ruleBody(css, ".admin-shell"), /display\s*:\s*flex/);
  assert.match(ruleBody(css, ".admin-shell"), /flex-direction\s*:\s*column/);
  assert.match(ruleBody(css, ".admin-sidebar"), /flex\s*:\s*0\s+0\s+calc\(48px\s*\+\s*env\(safe-area-inset-top,\s*0px\)\)/);
  assert.match(ruleBody(css, ".admin-sidebar"), /height\s*:\s*calc\(48px\s*\+\s*env\(safe-area-inset-top,\s*0px\)\)/);
  assert.match(ruleBody(css, ".admin-sidebar"), /min-height\s*:\s*calc\(48px\s*\+\s*env\(safe-area-inset-top,\s*0px\)\)/);
  assert.match(ruleBody(css, ".admin-sidebar"), /calc\(6px\s*\+\s*env\(safe-area-inset-top,\s*0px\)\)/);
  assert.match(ruleBody(css, ".admin-sidebar"), /overflow-x\s*:\s*hidden/);
  assert.match(ruleBody(css, ".admin-sidebar__mobile-menu"), /position\s*:\s*absolute/);
  assert.match(ruleBody(css, ".admin-sidebar__mobile-menu"), /top\s*:\s*calc\(env\(safe-area-inset-top,\s*0px\)\s*\+\s*24px\)/);
  assert.match(ruleBody(css, ".admin-sidebar__mobile-menu"), /right\s*:\s*var\(--space-2\)/);
  assert.match(ruleBody(css, ".admin-sidebar__mobile-menu"), /transform\s*:\s*translateY\(-50%\)/);
  assert.match(ruleBody(css, ".admin-nav"), /align-items\s*:\s*center/);
  assert.match(ruleBody(css, ".admin-nav"), /justify-content\s*:\s*flex-start/);
  assert.match(ruleBody(css, ".admin-nav"), /overflow-x\s*:\s*auto/);
  assert.match(ruleBody(css, ".admin-nav__link"), /height\s*:\s*34px/);
  assert.match(ruleBody(css, ".admin-nav__link"), /line-height\s*:\s*1/);
  assert.match(ruleBody(css, ".admin-nav__link"), /flex\s*:\s*0\s+0\s+auto/);
  assert.match(ruleBody(css, ".admin-main"), /padding\s*:\s*var\(--space-2\)\s+var\(--space-3\)\s+var\(--space-4\)/);
  assert.match(ruleBody(css, ".admin-page__header"), /margin-bottom\s*:\s*var\(--space-3\)/);
});
