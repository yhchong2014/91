import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const drivesPageSource = readFileSync(
  new URL("../src/admin/DrivesPage.tsx", import.meta.url),
  "utf8"
);
const driveComponentsSource = readFileSync(
  new URL("../src/admin/drive/DriveComponents.tsx", import.meta.url),
  "utf8"
);
const crawlerPageSource = readFileSync(
  new URL("../src/admin/CrawlersPage.tsx", import.meta.url),
  "utf8"
);
const adminLayoutSource = readFileSync(
  new URL("../src/admin/AdminLayout.tsx", import.meta.url),
  "utf8"
);
const appSource = readFileSync(
  new URL("../src/App.tsx", import.meta.url),
  "utf8"
);
const crawlerUploadTargetSource = readFileSync(
  new URL("../src/admin/drive/CrawlerUploadTargetField.tsx", import.meta.url),
  "utf8"
);
const driveFormSource = readFileSync(
  new URL("../src/admin/drive/DriveForm.tsx", import.meta.url),
  "utf8"
);
const adminCss = readFileSync(
  new URL("../src/styles/admin.css", import.meta.url),
  "utf8"
);
const apiSource = readFileSync(
  new URL("../src/admin/api.ts", import.meta.url),
  "utf8"
);
const constantsSource = readFileSync(
  new URL("../src/admin/drive/constants.ts", import.meta.url),
  "utf8"
);

const combinedSource = drivesPageSource + "\n" + driveFormSource + "\n" + constantsSource + "\n" + crawlerUploadTargetSource;

function driveTypeOptions() {
  const match = /const DRIVE_OPTIONS:\s*DriveOption\[]\s*=\s*\[([\s\S]*?)\];/.exec(
    driveFormSource
  );
  assert.ok(match, "drive option card list should be present");
  return Array.from(
    match[1].matchAll(/\{\s*kind:\s*"([^"]+)",\s*label:\s*"([^"]+)"/g),
    (option) => ({ value: option[1], label: option[2] })
  );
}

function assertDriveTypeOption(value: string, label: string) {
  assert.ok(
    driveTypeOptions().some((option) => option.value === value && option.label === label),
    `${value} drive type option should be present`
  );
}

test("crawler sources are not selectable as storage drives", () => {
  assert.ok(
    !driveTypeOptions().some((option) => option.value === "spider91"),
    "spider91 should not be a storage drive option"
  );
  assert.ok(
    !driveTypeOptions().some((option) => option.value === "scriptcrawler"),
    "scriptcrawler should not be a storage drive option"
  );
});

test("crawler upload target uses explicit local-save option instead of auto target", () => {
  assert.match(combinedSource, /本地保存，不上传/);
  assert.match(
    crawlerPageSource,
    /UPLOAD_TARGET_KINDS\s*=\s*new Set\(\["p115", "pikpak", "p123", "googledrive", "onedrive", "wopan", "guangyapan"\]\)/
  );
  assert.match(crawlerPageSource, /drives\.filter\(\(d\) => UPLOAD_TARGET_KINDS\.has\(d\.kind\)\)/);
  assert.doesNotMatch(combinedSource, /自动：唯一/);
  assert.doesNotMatch(combinedSource, /自动模式/);
  assert.doesNotMatch(combinedSource, /较早的视频会上传到该云盘根目录下/);
});

test("crawler upload target select uses an aligned custom arrow", () => {
  assert.match(crawlerUploadTargetSource, /className="admin-form-select-wrap"/);
  assert.match(crawlerUploadTargetSource, /className="admin-form-select"/);
  assert.match(crawlerUploadTargetSource, /className="admin-form-select__icon"/);
  assert.match(adminCss, /\.admin-form__row \.admin-form-select\s*\{[^}]*appearance\s*:\s*none/s);
  assert.match(
    adminCss,
    /\.admin-form-select__icon\s*\{[^}]*top\s*:\s*50%[^}]*right\s*:\s*12px[^}]*transform\s*:\s*translateY\(-50%\)/s
  );
});

test("drive form hides root directory id for localstorage", () => {
  assert.match(combinedSource, /<label[^>]*>根目录 ID<\/label>/);
  assert.match(
    combinedSource,
    /usesRootDirectoryID\(kind:\s*Kind\):\s*boolean\s*\{\s*return kind !== "localstorage";\s*\}/
  );
  assert.match(combinedSource, /\{usesRootDirectoryID\(form\.kind\) && \(/);
  assert.match(combinedSource, /\{usesRootDirectoryID\(d\.kind\) && \(/);
  assert.match(combinedSource, /placeholder=\{rootIdPlaceholder\(form\.kind\)\}/);
  assert.doesNotMatch(combinedSource, /扫描起点目录 ID/);
  assert.doesNotMatch(combinedSource, /set\("scanRootId"/);
});

test("onedrive drive form only exposes required default-app fields", () => {
  const match =
    /case "onedrive":\s*return \[([\s\S]*?)\];\s*case "googledrive":/.exec(
      combinedSource
    );
  assert.ok(match, "onedrive credential field block should be present");
  const fields = match[1];

  assert.match(fields, /key: "refresh_token"/);
  assert.doesNotMatch(fields, /key: "access_token"/);
  assert.doesNotMatch(fields, /key: "api_url_address"/);
  assert.doesNotMatch(fields, /key: "region"/);
  assert.doesNotMatch(fields, /key: "is_sharepoint"/);
  assert.doesNotMatch(fields, /key: "site_id"/);
});

test("googledrive drive form supports online API and custom OAuth client modes", () => {
  assertDriveTypeOption("googledrive", "Google Drive");

  const match =
    /case "googledrive":\s*return \[([\s\S]*?)\];\s*case "localstorage":/.exec(
      combinedSource
    );
  assert.ok(match, "googledrive credential field block should be present");
  const fields = match[1];

  assert.match(fields, /key: "refresh_token"/);
  assert.match(fields, /key: "use_online_api"/);
  assert.match(fields, /type: "select"/);
  assert.match(fields, /defaultValue: "true"/);
  assert.match(fields, /OpenList 在线 API/);
  assert.match(fields, /自建 Google OAuth 客户端/);
  assert.match(fields, /key: "client_id"/);
  assert.match(fields, /key: "client_secret"/);
  assert.match(fields, /googleDriveUsesOnlineAPI\(creds\)/);
  assert.match(fields, /key: "api_url_address"/);
  assert.match(fields, /OpenList 在线 API URL/);
  assert.doesNotMatch(fields, /在线 API 模式填写 OpenList 获取的 refresh_token/);
  assert.doesNotMatch(constantsSource, /请参考OpenList文档中关于谷歌云盘的配置方法。/);
  assert.doesNotMatch(constantsSource, /选择自建 Google OAuth 客户端后，服务端会直接请求 Google OAuth token 接口续期。/);
  assert.match(driveFormSource, /<select/);
  assert.match(driveFormSource, /value=\{form\.creds\[f\.key\] \?\? f\.defaultValue \?\? ""\}/);
  assert.match(driveFormSource, /className="admin-form-select"/);
  assert.match(driveFormSource, /ChevronDown/);
  assert.match(drivesPageSource, /googleDriveUseOnlineAPI/);
  assert.match(drivesPageSource, /googleDriveOpenListApiUrl/);
  assert.match(apiSource, /googleDriveUseOnlineAPI\?: boolean/);
  assert.match(apiSource, /googleDriveOpenListApiUrl\?: string/);
  assert.doesNotMatch(fields, /key: "access_token"/);
});

test("pikpak drive form only exposes account login fields", () => {
  const match =
    /case "pikpak":\s*return \[([\s\S]*?)\];\s*case "wopan":/.exec(
      combinedSource
    );
  assert.ok(match, "pikpak credential field block should be present");
  const fields = match[1];

  assert.match(fields, /key: "username"/);
  assert.match(fields, /key: "password"/);
  assert.doesNotMatch(fields, /key: "platform"/);
  assert.doesNotMatch(fields, /key: "refresh_token"/);
  assert.doesNotMatch(fields, /key: "captcha_token"/);
  assert.doesNotMatch(fields, /key: "device_id"/);
  assert.doesNotMatch(fields, /key: "disable_media_link"/);
});

test("guangyapan drive form exposes qr login and token fields", () => {
  assertDriveTypeOption("guangyapan", "光鸭网盘");
  assert.match(driveFormSource, /GuangYaPanQRCodeLogin/);
  assert.match(driveFormSource, /form\.kind === "guangyapan"/);
  assert.match(apiSource, /startGuangYaPanQRLogin/);
  assert.match(apiSource, /getGuangYaPanQRStatus/);

  const match =
    /case "guangyapan":\s*return \[([\s\S]*?)\];\s*case "onedrive":/.exec(
      combinedSource
    );
  assert.ok(match, "guangyapan credential field block should be present");
  const fields = match[1];

  assert.match(fields, /key: "root_path"/);
  assert.match(fields, /key: "refresh_token"/);
  assert.match(fields, /key: "access_token"/);
  assert.doesNotMatch(fields, /key: "phone_number"/);
  assert.doesNotMatch(fields, /key: "send_code"/);
  assert.doesNotMatch(fields, /key: "verify_code"/);
  assert.doesNotMatch(fields, /key: "captcha_token"/);
  assert.doesNotMatch(fields, /key: "client_id"/);
  assert.doesNotMatch(fields, /key: "device_id"/);
  assert.match(combinedSource, /if \(kind === "guangyapan"\) return ""/);
});

test("localstorage drive form asks for a server directory path", () => {
  assertDriveTypeOption("localstorage", "本地存储");

  const match =
    /case "localstorage":\s*return \[([\s\S]*?)\];\s*\}\s*\}/.exec(
      combinedSource
    );
  assert.ok(match, "localstorage credential field block should be present");
  const fields = match[1];

  assert.match(fields, /key: "path"/);
  assert.match(fields, /label: "本地目录路径"/);
  assert.match(combinedSource, /if \(kind === "localstorage"\) return "\/"/);
  assert.match(combinedSource, /kind !== "localstorage"/);
  assert.doesNotMatch(combinedSource, /spider91/);
});

test("drive type selector keeps primary source order", () => {
  assert.deepEqual(driveTypeOptions(), [
    { value: "p115", label: "115 网盘" },
    { value: "p123", label: "123网盘" },
    { value: "pikpak", label: "PikPak" },
    { value: "guangyapan", label: "光鸭网盘" },
    { value: "onedrive", label: "OneDrive" },
    { value: "googledrive", label: "Google Drive" },
    { value: "localstorage", label: "本地存储" },
    { value: "quark", label: "夸克网盘" },
    { value: "wopan", label: "联通网盘" },
  ]);
});

test("crawler management is a separate admin section", () => {
  assert.match(adminLayoutSource, /to="\/admin\/crawlers"/);
  assert.match(adminLayoutSource, /admin-nav__title">爬虫管理/);
  assert.match(adminLayoutSource, /admin-nav__icon"><SpiderIcon size=\{16\} \/>/);
  assert.match(
    appSource,
    /path="crawlers"[\s\S]*<PageSuspense>[\s\S]*<CrawlersPage \/>[\s\S]*<\/PageSuspense>/
  );
  assert.match(crawlerPageSource, /export function CrawlersPage/);
  assert.match(crawlerPageSource, /SpiderIcon/);
  assert.match(crawlerPageSource, /添加爬虫/);
  // 新设计：列表 + Modal 三步编辑器，删除确认走 ConfirmModal，任务进行中自动轮询
  assert.match(crawlerPageSource, /CrawlerEditorModal/);
  assert.match(crawlerPageSource, /ConfirmModal/);
  assert.doesNotMatch(crawlerPageSource, /window\.confirm/);
  assert.match(crawlerPageSource, /POLL_INTERVAL_MS/);
  assert.match(crawlerPageSource, /api\.listCrawlers/);
  assert.match(crawlerPageSource, /api\.listDrives/);
  assert.match(crawlerPageSource, /api\.upsertCrawler/);
  assert.match(crawlerPageSource, /api\.runCrawler/);
  assert.match(crawlerPageSource, /api\.uploadCrawlerVideos/);
  assert.match(crawlerPageSource, /api\.stopCrawlerTasks/);
  assert.match(crawlerPageSource, /api\.deleteCrawler/);
  assert.match(crawlerPageSource, /api\.importCrawlerScriptFile/);
  assert.match(crawlerPageSource, /api\.importCrawlerScriptURL/);
  assert.match(crawlerPageSource, /api\.testCrawlerScript/);
  assert.match(crawlerPageSource, /type="file"/);
  assert.match(crawlerPageSource, /链接导入/);
  assert.match(crawlerPageSource, /测试脚本/);
  assert.match(crawlerPageSource, /测试通过/);
  assert.match(crawlerPageSource, /CrawlerUploadTargetField/);
  assert.match(crawlerPageSource, /uploadDriveId/);
  assert.match(crawlerPageSource, /api\.setDriveTeaserEnabled/);
  assert.match(crawlerPageSource, /admin-crawler-preview-card-toggle/);
  assert.match(crawlerPageSource, /预览：开/);
  assert.match(crawlerPageSource, /预览：关/);
  assert.match(crawlerPageSource, /上传视频/);
  assert.match(crawlerPageSource, /aria-pressed=\{crawler\.teaserEnabled\}/);
  assert.doesNotMatch(crawlerPageSource, /crawlerUploadBlockedReason/);
  assert.doesNotMatch(crawlerPageSource, /disabled=\{uploading/);
  assert.doesNotMatch(crawlerPageSource, /crawlerStatusLabel/);
  assert.doesNotMatch(crawlerPageSource, /admin-crawler-preview-card-toggle \$\{crawler\.teaserEnabled/);
  assert.doesNotMatch(adminCss, /admin-crawler-preview-card-toggle\.is-on/);
  assert.doesNotMatch(crawlerPageSource, /admin-crawler-pipeline/);
  assert.doesNotMatch(adminCss, /admin-crawler-(pipeline|stage)/);
  assert.doesNotMatch(crawlerPageSource, /teaserEnabled: form\.teaserEnabled/);
  assert.doesNotMatch(crawlerPageSource, /aria-pressed=\{form\.teaserEnabled\}/);
  assert.match(crawlerPageSource, /UPLOAD_TARGET_KINDS/);
  assert.doesNotMatch(crawlerPageSource, /新建脚本/);
  assert.doesNotMatch(crawlerPageSource, /爬虫 ID/);
  assert.doesNotMatch(crawlerPageSource, /crawler-id/);
  assert.doesNotMatch(crawlerPageSource, /crawler-name/);
  // 脚本路径只读展示，不允许手动填写
  assert.doesNotMatch(crawlerPageSource, /crawler-script-path/);
  assert.doesNotMatch(crawlerPageSource, /Python 解释器/);
  assert.doesNotMatch(crawlerPageSource, /自定义配置 JSON/);
  assert.doesNotMatch(crawlerPageSource, /Bot/);
  // 项目不再内置任何爬虫：不允许出现内置 91 预设
  assert.doesNotMatch(crawlerPageSource, /builtin/);
  assert.doesNotMatch(crawlerPageSource, /内置 91/);
  assert.match(apiSource, /type AdminCrawler/);
  assert.match(apiSource, /uploadDriveId\?: string/);
  assert.match(apiSource, /teaserEnabled: boolean/);
  assert.doesNotMatch(apiSource, /teaserEnabled\?: boolean/);
  assert.match(apiSource, /"\/crawlers"/);
  assert.match(apiSource, /\/crawlers\/\$\{encodeURIComponent\(id\)\}\/upload/);
  assert.match(apiSource, /"\/crawlers\/import-file"/);
  assert.match(apiSource, /"\/crawlers\/import-url"/);
  assert.match(apiSource, /"\/crawlers\/test-script"/);
  assert.match(apiSource, /type CrawlerDryRunResult/);
  assert.match(apiSource, /id\?: string/);
  assert.match(apiSource, /new FormData\(\)/);
  assert.doesNotMatch(driveFormSource, /scriptcrawler/);
});

test("admin shell stays mounted while lazy admin pages load", () => {
  assert.match(appSource, /import \{ AdminLayout \} from "@\/admin\/AdminLayout";/);
  assert.doesNotMatch(appSource, /const AdminLayout\s*=\s*lazy/);
  assert.doesNotMatch(appSource, /<Suspense fallback=\{null\}>\s*<Routes>/);
  assert.match(appSource, /function PageSuspense\(\{ children \}: \{ children: ReactNode \}\)/);
  assert.match(appSource, /path="\/admin"[\s\S]*<AdminLayout \/>/);
  assert.match(
    appSource,
    /path="drives"[\s\S]*<PageSuspense>[\s\S]*<DrivesPage \/>[\s\S]*<\/PageSuspense>/
  );
});

test("drive cards use configured abbreviations and visible fallback icon colors", () => {
  assert.match(constantsSource, /googledrive:\s*"GD"/);
  assert.match(constantsSource, /function driveKindAbbr\(kind: string\)/);
  assert.match(constantsSource, /\.slice\(0, 2\)\.toUpperCase\(\)/);
  assert.match(drivesPageSource, /driveKindAbbr\(d\.kind\)/);
  assert.match(adminCss, /\.admin-drive-card__brand-icon\s*\{[^}]*background:\s*var\(--accent\);/s);
  assert.match(adminCss, /\.admin-drive-card__brand-icon\[data-kind="googledrive"\]\s*\{\s*background:\s*#4285f4;\s*\}/);
  assert.match(adminCss, /\.admin-drive-card__brand-icon\[data-kind="guangyapan"\]\s*\{\s*background:\s*var\(--drive-guangyapan\);/);
});

test("drive management exposes stop task controls", () => {
  assert.match(apiSource, /stopDriveTasks/);
  assert.match(apiSource, /\/drives\/\$\{encodeURIComponent\(id\)\}\/tasks\/stop/);
  assert.match(apiSource, /stopAllTasks/);
  assert.match(apiSource, /"\/tasks\/stop"/);
  assert.match(drivesPageSource, /is-stop/);
  assert.match(drivesPageSource, /停止所有任务/);
  assert.match(drivesPageSource, /停止所有网盘任务/);
});

test("drive detail primary actions use the rescan button color", () => {
  assert.match(
    drivesPageSource,
    /className="admin-btn is-primary"\s+onClick=\{\(\) => handleRescan\(d\)\}/
  );
  assert.match(
    drivesPageSource,
    /className="admin-btn is-primary"\s+onClick=\{\(\) => handleStopDriveTasks\(d\)\}/
  );
  assert.match(
    drivesPageSource,
    /className="admin-btn is-primary"\s+onClick=\{\(\) => openEdit\(d\)\}/
  );
});

test("drive rescan reports busy storage tasks instead of queueing duplicates", () => {
  assert.match(apiSource, /accepted:\s*boolean;\s*message\?:\s*string/);
  assert.match(apiSource, /scanGenerationStatus\?: DriveGenerationStatus/);
  assert.match(drivesPageSource, /当前存储有正在进行的任务，请稍后重试/);
  assert.match(drivesPageSource, /function isDriveBusy\(d: api\.AdminDrive\)/);
  assert.match(drivesPageSource, /d\.scanGenerationStatus/);
  assert.match(drivesPageSource, /status\?\.state \|\| "idle"/);
  assert.match(drivesPageSource, /scanningDriveIdsRef\.current\.has\(d\.id\)/);
  assert.match(drivesPageSource, /if \(!resp\.accepted\)/);
  assert.doesNotMatch(drivesPageSource, /disabled=\{!!scanningDriveId\}/);
});

test("nightly scan duplicate trigger uses full-scan busy message", () => {
  assert.match(apiSource, /status:\s*NightlyJobStatus;\s*message\?:\s*string/);
  assert.match(drivesPageSource, /当前有全量扫描任务正在进行，请稍后重试/);
  assert.match(drivesPageSource, /resp\.message \|\| NIGHTLY_BUSY_MESSAGE/);
  assert.match(constantsSource, /当前有全量扫描任务正在进行，请稍后重试/);
});

test("drive generation panel shows scan or crawler status first", () => {
  assert.match(driveComponentsSource, /label="扫盘"/);
  assert.match(driveComponentsSource, /status=\{d\.scanGenerationStatus\}/);
  assert.match(driveComponentsSource, /showCounts=\{false\}/);
  assert.match(driveComponentsSource, /status\?\.scannedCount/);
  assert.match(driveComponentsSource, /预计新增/);
  assert.match(apiSource, /scannedCount:\s*number/);
  assert.match(apiSource, /addedCount:\s*number/);
  assert.match(constantsSource, /if \(state === "scanning"\) return "扫盘中"/);
});

test("drive management has no spider91 storage branch", () => {
  assert.doesNotMatch(drivesPageSource, /spider91|91Spider/);
  assert.doesNotMatch(constantsSource, /spider91|91Spider/);
  assert.doesNotMatch(driveComponentsSource, /spider91|91Spider/);
});

test("drive detail selection is stored in the URL history", () => {
  assert.match(drivesPageSource, /useSearchParams/);
  assert.match(drivesPageSource, /searchParams\.get\("drive"\)/);
  assert.match(drivesPageSource, /function openDriveDetail\(id: string\)/);
  assert.match(drivesPageSource, /next\.set\("drive", id\)/);
  assert.match(drivesPageSource, /function closeDriveDetail/);
  assert.match(drivesPageSource, /next\.delete\("drive"\)/);
  assert.doesNotMatch(drivesPageSource, /setSelectedDriveId/);
});

test("drive discard confirmation matches delete confirmation modal styling", () => {
  const discardModals = Array.from(
    drivesPageSource.matchAll(/<ConfirmModal[\s\S]*?title="放弃未保存更改"[\s\S]*?\/>/g),
    (match) => match[0]
  );

  assert.equal(discardModals.length, 2);
  for (const modal of discardModals) {
    assert.match(modal, /danger/);
    assert.match(modal, /centerMessage/);
    assert.match(modal, /modalClassName="admin-modal--delete-confirm"/);
  }
});

test("new drive type selection alone is not treated as unsaved config", () => {
  assert.match(
    drivesPageSource,
    /const formDirty = form\.id\s*\?\s*!sameForm\(form, initialForm\)\s*:\s*hasCreateFormChanges\(form\);/
  );
  assert.match(drivesPageSource, /function handleCreateFormChange\(nextForm: FormState\)/);
  assert.match(
    drivesPageSource,
    /if \(!nextForm\.id && !hasCreateFormChanges\(nextForm\)\) \{\s*setInitialForm\(nextForm\);/
  );
  assert.match(drivesPageSource, /onChange=\{handleCreateFormChange\}/);

  const match = /function hasCreateFormChanges\(form: FormState\): boolean \{([\s\S]*?)\n\}/.exec(
    drivesPageSource
  );
  assert.ok(match, "create form dirty helper should be present");
  const helper = match[1];

  assert.match(helper, /form\.name\.trim\(\) !== ""/);
  assert.match(helper, /form\.rootId\.trim\(\) !== ""/);
  assert.match(helper, /Object\.values\(form\.creds\)\.some/);
  assert.doesNotMatch(helper, /form\.kind/);
});

test("drive generation actions can resume pending work after stop", () => {
  assert.match(driveComponentsSource, /thumbnailPendingCount/);
  assert.match(driveComponentsSource, /teaserPendingCount/);
  assert.match(driveComponentsSource, /fingerprintPendingCount/);
  assert.match(driveComponentsSource, /继续生成封面/);
  assert.match(driveComponentsSource, /继续生成预览视频/);
  assert.match(driveComponentsSource, /继续生成指纹/);
});

test("drive cards label fingerprint count as video fingerprint count", () => {
  assert.match(driveComponentsSource, /视频指纹数 \(就绪\/失败\)/);
  assert.doesNotMatch(driveComponentsSource, />指纹数 \(就绪\/失败\)</);
});
