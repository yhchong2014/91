import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const apiSource = readFileSync(
  new URL("../src/admin/api.ts", import.meta.url),
  "utf8"
);
const tagsPageSource = readFileSync(
  new URL("../src/admin/TagsPage.tsx", import.meta.url),
  "utf8"
);
const adminCss = readFileSync(
  new URL("../src/styles/admin.css", import.meta.url),
  "utf8"
);
const videosPageSource = readFileSync(
  new URL("../src/admin/VideosPage.tsx", import.meta.url),
  "utf8"
);

test("admin tags keep builtin, user, and auto-generated tag management", () => {
  assert.match(apiSource, /export type TagMatchRules/);
  assert.match(apiSource, /matchRules\?: \{/);
  assert.match(apiSource, /keywords\?: string\[\]/);
  assert.doesNotMatch(apiSource, /\n\s+words\?: string\[\];/);
  assert.doesNotMatch(apiSource, /\n\s+excludes\?: string\[\];/);
  assert.match(apiSource, /matchAvCode\?: boolean/);
  assert.match(apiSource, /avCodePrefixes\?: string\[\]/);
  assert.match(apiSource, /export function updateTag/);
  assert.match(apiSource, /updateTag\(id: number, matchRules: TagMatchRules\)/);
  assert.doesNotMatch(apiSource, /export function startTagRetag/);
  assert.doesNotMatch(apiSource, /export function getTagJobStatus/);
  assert.doesNotMatch(apiSource, /autoGenerateTagsEnabled: boolean/);
  assert.doesNotMatch(apiSource, /startTagLlmRun/);
  assert.doesNotMatch(apiSource, /llmEnabled|llmPending/);
  assert.doesNotMatch(tagsPageSource, /编辑标签：/);
  assert.doesNotMatch(tagsPageSource, /<h1 className="admin-page__title">标签管理<\/h1>/);
  assert.match(tagsPageSource, /<div className="admin-tags-board">/);
  assert.match(tagsPageSource, /<aside className="admin-tags-filter-panel" aria-label="标签分类">/);
  assert.match(tagsPageSource, /<div className="admin-tags-main">/);
  assert.ok(
    tagsPageSource.indexOf('className="admin-tags-search"') <
      tagsPageSource.indexOf('className="admin-tags-filter-panel"'),
    "tag search should appear before source filter"
  );
  assert.match(tagsPageSource, /placeholder="搜索标签名或包含词"/);
  assert.doesNotMatch(tagsPageSource, /搜索标签名或规则词/);
  assert.match(tagsPageSource, /admin-tags-filter-tab__text/);
  assert.doesNotMatch(tagsPageSource, /admin-tags-filter-tab__count/);
  assert.doesNotMatch(tagsPageSource, /aria-label=\{`\$\{label\} \(\$\{count\}\)`\}/);
  assert.doesNotMatch(tagsPageSource, /aria-label=\{`全部 \(\$\{stats\.total\}\)`\}/);
  assert.match(tagsPageSource, /添加标签/);
  assert.match(tagsPageSource, /onClick=\{openCreateModal\}/);
  assert.match(tagsPageSource, /className="admin-btn"\s+onClick=\{openCreateModal\}/);
  assert.match(tagsPageSource, /const createLabelExists = useMemo/);
  assert.match(tagsPageSource, /tag\.label\.trim\(\)\.toLowerCase\(\) === cleanLabel/);
  assert.match(tagsPageSource, /if \(createLabelExists\) return;/);
  assert.match(tagsPageSource, /disabled=\{saving \|\| !label\.trim\(\) \|\| createLabelExists\}/);
  assert.match(tagsPageSource, /aria-label="输入标签名"/);
  assert.match(tagsPageSource, /aria-describedby=\{createLabelExists \? "admin-tag-create-warning" : undefined\}/);
  assert.match(tagsPageSource, /placeholder="输入标签名"/);
  assert.match(tagsPageSource, /className=\{`admin-tag-create-warning\$\{createLabelExists \? " is-visible" : ""\}`\}/);
  assert.match(tagsPageSource, /aria-hidden=\{!createLabelExists\}/);
  assert.match(tagsPageSource, /当前标签已存在/);
  assert.match(tagsPageSource, /\{saving \? "添加中\.\.\." : "确认"\}/);
  assert.doesNotMatch(tagsPageSource, /<label htmlFor="admin-tag-label">标签名<\/label>/);
  assert.doesNotMatch(tagsPageSource, /placeholder="例如：清纯"/);
  assert.doesNotMatch(tagsPageSource, /<Plus size=\{13\} \/> 新增标签/);
  assert.doesNotMatch(tagsPageSource, /className="admin-btn is-primary"\s+onClick=\{openCreateModal\}/);
  assert.match(tagsPageSource, /form="admin-create-tag-form"/);
  assert.match(tagsPageSource, /confirmText="确认"/);
  assert.doesNotMatch(tagsPageSource, /confirmText="确认删除"/);
  assert.doesNotMatch(tagsPageSource, /admin-card__title[\s\S]*新增标签/);
  assert.doesNotMatch(tagsPageSource, /系统不会再从文件名或标题自动创建标签/);
  assert.doesNotMatch(tagsPageSource, /包含词（子串）/);
  assert.doesNotMatch(tagsPageSource, /识别文件名和标题中的番号/);
  assert.doesNotMatch(tagsPageSource, /重新整理所有标签/);
  assert.doesNotMatch(tagsPageSource, /<RefreshCw size=\{13\} \/> 刷新/);
  assert.doesNotMatch(tagsPageSource, /自动生成标签/);
  assert.doesNotMatch(tagsPageSource, /autoGenerateTagsEnabled/);
  assert.doesNotMatch(tagsPageSource, /admin-tag-setting-toggle__switch/);
  assert.doesNotMatch(tagsPageSource, /role="switch"/);
  assert.doesNotMatch(tagsPageSource, /onClick=\{toggleAutoGenerateTags\}/);
  assert.doesNotMatch(tagsPageSource, /admin-tag-setting-toggle__hint/);
  assert.doesNotMatch(tagsPageSource, /admin-tag-setting-toggle__body/);
  assert.doesNotMatch(tagsPageSource, /关闭后扫描只匹配已有标签。/);
  assert.doesNotMatch(tagsPageSource, /<strong>\{autoGenerateTagsEnabled \? "开启" : "关闭"\}<\/strong>/);
  assert.doesNotMatch(tagsPageSource, /AI 辅助打标|AI 打标|tagging\.llm/);
  assert.match(tagsPageSource, /const TAG_SOURCE_FILTERS = \["builtin", "user", "generated"\]/);
  assert.match(tagsPageSource, /function tagSourceKey/);
  assert.match(tagsPageSource, /tag\.crawlerOwned \|\| tag\.source === "generated" \? "generated" : tag\.source/);
  assert.match(tagsPageSource, /source === "crawler" \|\| source === "generated"/);
  assert.match(tagsPageSource, /tagCardSourceLabel\(tag\)/);
  assert.match(tagsPageSource, /data-source=\{tagCardSourceKey\(tag\)\}/);
  assert.doesNotMatch(tagsPageSource, /admin-tag-card__id/);
  assert.doesNotMatch(tagsPageSource, /#\{tag\.id\}/);
  assert.match(tagsPageSource, /function tagCardSourceLabel/);
  assert.match(tagsPageSource, /function tagCardSourceKey/);
  assert.match(tagsPageSource, /tag\.crawlerOwned \|\| tag\.source === "crawler"/);
  assert.match(tagsPageSource, /return "爬虫脚本"/);
  assert.match(tagsPageSource, /return "crawler"/);
  assert.match(tagsPageSource, /tag\.source === "generated"/);
  assert.match(tagsPageSource, /return "AV"/);
  assert.match(tagsPageSource, /return "av"/);
  assert.doesNotMatch(tagsPageSource, /const displayAliases = tagDisplayAliases\(tag\);/);
  assert.doesNotMatch(tagsPageSource, /admin-tag-card__aliases/);
  assert.doesNotMatch(tagsPageSource, /admin-tag-card__alias-pill/);
  assert.doesNotMatch(tagsPageSource, /function tagDisplayAliases/);
  assert.match(tagsPageSource, /avCodePrefixes: joinRuleTerms\(rules\.avCodePrefixes\)/);
  assert.match(tagsPageSource, /tagRuleTerms\(t\)\.some/);
  assert.doesNotMatch(tagsPageSource, /admin-tag-card__keywords|admin-tag-card__keyword-pill|tagKeywordTerms/);
  assert.doesNotMatch(tagsPageSource, /function uniqueDisplayAliases/);
  assert.doesNotMatch(tagsPageSource, /系统内置车牌已自动参与匹配/);
  assert.match(tagsPageSource, /const TAG_DISPLAY_GROUP_ORDER: Record<string, number>/);
  assert.match(tagsPageSource, /function tagDisplayGroupKey/);
  assert.match(tagsPageSource, /function tagDisplayGroupRank/);
  assert.match(tagsPageSource, /if \(filterSource !== "all"\) return matches;/);
  assert.match(tagsPageSource, /tagDisplayGroupRank\(a\.tag\) - tagDisplayGroupRank\(b\.tag\)/);
  assert.match(tagsPageSource, /return rankDelta \|\| a\.index - b\.index;/);
  assert.doesNotMatch(tagsPageSource, /sourceLabel\(tag\.source,\s*tag\)/);
  assert.match(tagsPageSource, /return "自动生成"/);
  assert.doesNotMatch(tagsPageSource, /return "爬虫"/);
  assert.doesNotMatch(tagsPageSource, /return "系统"/);
  assert.doesNotMatch(tagsPageSource, /return "旧数据"/);
  assert.doesNotMatch(tagsPageSource, /tag\.source !== "system"/);
  assert.doesNotMatch(tagsPageSource, /tag\.source === "system"\) return/);
  assert.match(adminCss, /\.admin-tag-card\.is-selectable:focus-visible\s*\{[^}]*outline\s*:\s*2px solid var\(--border-accent\)/s);
  assert.doesNotMatch(adminCss, /\.admin-tag-card\.is-selectable:focus-within\s*\{[^}]*box-shadow/s);
  assert.match(adminCss, /\.admin-tag-card:not\(\.is-selectable\):hover\s*\{/);
});

test("admin tags batch delete runs deletions sequentially", () => {
  assert.match(tagsPageSource, /for \(const id of ids\) \{/);
  assert.match(tagsPageSource, /await api\.deleteTag\(id\);/);
  assert.doesNotMatch(
    tagsPageSource,
    /Promise\.allSettled\(\s*ids\.map\(\(id\) => api\.deleteTag\(id\)\)\s*\)/
  );
});

test("admin tag card delete action appears before edit action", () => {
  const actionsStart = tagsPageSource.indexOf('className="admin-tag-card__footer-actions"');
  const deleteIndex = tagsPageSource.indexOf('className="admin-tag-card__delete"', actionsStart);
  const editIndex = tagsPageSource.indexOf('className="admin-tag-card__edit"', actionsStart);
  const actionsEnd = tagsPageSource.indexOf("</div>", editIndex);
  const actionsSource = tagsPageSource.slice(actionsStart, actionsEnd);

  assert.ok(actionsStart >= 0, "tag card footer actions should exist");
  assert.ok(deleteIndex > actionsStart, "delete action should be inside tag card actions");
  assert.ok(editIndex > deleteIndex, "edit action should stay to the right of delete action");
  assert.doesNotMatch(actionsSource, /<Trash2\b|<Pencil\b/);
  assert.doesNotMatch(tagsPageSource, /import \{[^}]*\bPencil\b/);
});

test("admin tag dialogs use the lightweight modal style", () => {
  assert.match(
    tagsPageSource,
    /modalClassName="admin-modal--delete-confirm admin-modal--tag-dialog admin-modal--tag-delete-confirm"/
  );
  assert.match(
    tagsPageSource,
    /title="新增标签"[\s\S]*?className="admin-modal--tag-rules admin-modal--tag-dialog admin-modal--tag-create"/
  );
  assert.match(tagsPageSource, /className="admin-modal--tag-rules admin-modal--tag-dialog admin-modal--tag-create"/);
  assert.match(tagsPageSource, /restoreFocus=\{false\}/);
  assert.match(adminCss, /\.admin-modal--tag-dialog\s*\{[^}]*border\s*:\s*0/s);
  assert.match(adminCss, /\.admin-modal--tag-dialog \.admin-modal__header\s*\{[^}]*border-bottom\s*:\s*0/s);
  assert.match(adminCss, /\.admin-modal--tag-dialog \.admin-modal__footer\s*\{[^}]*border-top\s*:\s*0/s);
  assert.match(adminCss, /\.admin-modal--tag-create \.admin-modal__body\s*\{[^}]*padding-bottom\s*:\s*6px/s);
  assert.match(adminCss, /\.admin-modal--tag-create \.admin-modal__footer\s*\{[^}]*padding-top\s*:\s*6px/s);
  assert.match(adminCss, /\.admin-tag-card__source-badge\s*\{[^}]*font-weight\s*:\s*400/s);
  assert.match(adminCss, /\.admin-tag-card__source-badge\s*\{[^}]*background\s*:\s*var\(--tag-source-bg/s);
  assert.doesNotMatch(adminCss, /\.admin-tag-card__source-badge\s*\{[^}]*box-shadow/s);
  assert.doesNotMatch(adminCss, /--tag-source-border/);
  assert.match(adminCss, /\.admin-tag-card__source-badge\[data-source="user"\]\s*\{[^}]*--tag-source-bg\s*:\s*rgba\(74,\s*222,\s*128,\s*0\.16\)/s);
  assert.match(adminCss, /\.admin-tag-card__source-badge\[data-source="builtin"\]\s*\{[^}]*--tag-source-bg\s*:\s*rgba\(96,\s*165,\s*250,\s*0\.16\)/s);
  assert.match(adminCss, /\.admin-tag-card__source-badge\[data-source="av"\]/);
  assert.match(adminCss, /\.admin-tag-card__source-badge\[data-source="av"\][\s\S]*?--tag-source-bg\s*:\s*rgba\(251,\s*191,\s*36,\s*0\.17\)/s);
  assert.match(adminCss, /\.admin-tag-card__source-badge\[data-source="crawler"\]\s*\{[^}]*--tag-source-bg\s*:\s*rgba\(196,\s*181,\s*253,\s*0\.18\)/s);
  assert.match(adminCss, /\.admin-tag-create-row\s*\{[^}]*position\s*:\s*relative/s);
  assert.match(adminCss, /\.admin-tag-create-warning\s*\{[^}]*color\s*:\s*var\(--danger\)/s);
  assert.match(adminCss, /\.admin-tag-create-warning\s*\{[^}]*position\s*:\s*absolute/s);
  assert.doesNotMatch(adminCss, /\.admin-tag-create-warning\s*\{[^}]*min-height/s);
  assert.match(adminCss, /\.admin-tag-create-warning\s*\{[^}]*visibility\s*:\s*hidden/s);
  assert.match(adminCss, /\.admin-tag-create-warning\.is-visible\s*\{[^}]*visibility\s*:\s*visible/s);
  assert.match(adminCss, /\.admin-modal--tag-delete-confirm \.admin-confirm\s*\{[^}]*display\s*:\s*block/s);
});

test("admin tag edit dialog edits match rules directly", () => {
  const editModalStart = tagsPageSource.indexOf("function EditTagModal");
  const editModalSource = tagsPageSource.slice(editModalStart);
  assert.ok(editModalStart >= 0, "EditTagModal should exist");
  assert.match(editModalSource, /const \[draft, setDraft\] = useState\(\(\) => tagRuleDraft\(tag\)\)/);
  assert.match(editModalSource, /const parsedRules = matchRulesFromDraft\(nextDraft, isAV\);/);
  assert.match(editModalSource, /title=\{tag\.label\}/);
  assert.doesNotMatch(editModalSource, /footer=\{[\s\S]*?保存中|footer=\{[\s\S]*?取消/);
  assert.match(editModalSource, /inputId="admin-tag-rule-keywords"/);
  assert.match(editModalSource, /function KeywordPillEditor/);
  assert.match(editModalSource, /function PrefixPillEditor/);
  assert.match(editModalSource, /function RulePillEditor/);
  assert.match(editModalSource, /onCommit=\{\(value\) => void persistDraft\(\{ \.\.\.draft, keywords: value \}\)\}/);
  assert.match(editModalSource, /function singleRuleTerm/);
  assert.match(editModalSource, /inputLabel="添加包含词"/);
  assert.match(editModalSource, /inputLabel="添加车牌前缀"/);
  assert.match(editModalSource, /const showDuplicateWarning = pendingTerm !== "" && pendingExists;/);
  assert.match(editModalSource, /aria-describedby=\{showDuplicateWarning \? warningId : undefined\}/);
  assert.match(editModalSource, /当前包含词已存在/);
  assert.match(editModalSource, /当前车牌前缀已存在/);
  assert.match(editModalSource, /<div className="admin-form__row">\s*<KeywordPillEditor/);
  assert.match(editModalSource, /<div className="admin-form__row">\s*<PrefixPillEditor/);
  assert.doesNotMatch(editModalSource, /<label htmlFor="admin-tag-rule-keywords">包含词<\/label>/);
  assert.doesNotMatch(editModalSource, /番号前缀|<textarea/);
  assert.match(editModalSource, /className="admin-tag-rule-keyword-list"/);
  assert.match(editModalSource, /className="admin-tag-rule-keyword-pill"/);
  assert.match(editModalSource, /className="admin-tag-rule-keyword-input-row"/);
  assert.doesNotMatch(editModalSource, /admin-tag-rule-words|整词匹配/);
  assert.doesNotMatch(editModalSource, /admin-tag-rule-excludes|排除词/);
  assert.match(editModalSource, /inputId="admin-tag-rule-prefixes"/);
  assert.match(editModalSource, /onCommit=\{\(value\) => void persistDraft\(\{ \.\.\.draft, avCodePrefixes: value \}\)\}/);
  assert.match(editModalSource, /splitTerms=\{splitPrefixTerms\}/);
  assert.match(editModalSource, /allowEmpty/);
  assert.match(editModalSource, /await api\.updateTag\(tag\.id, parsedRules\);/);
  assert.doesNotMatch(editModalSource, /disabled=\{saving \|\| !canSave\}/);
  assert.doesNotMatch(editModalSource, /aliasDraft|aliases|editTagAliases|pendingAliasAdditions|duplicateAliasInputs/);
  assert.doesNotMatch(adminCss, /admin-tag-alias/);
  assert.match(adminCss, /\.admin-tag-rule-keyword-list\s*\{[^}]*padding\s*:\s*2px 0/s);
  assert.match(adminCss, /\.admin-tag-rule-keyword-list\s*\{[^}]*transform\s*:\s*translateY\(-6px\)/s);
  assert.match(adminCss, /\.admin-tag-rule-keyword-pill\s*\{[^}]*background\s*:\s*transparent/s);
  assert.match(adminCss, /\.admin-tag-rule-keyword-warning\s*\{[^}]*color\s*:\s*var\(--danger\)/s);
  assert.doesNotMatch(adminCss, /\.admin-modal--tag-dialog \.admin-modal__header::after/);
  assert.match(adminCss, /\.admin-modal--tag-dialog \.admin-modal__body\s*\{[^}]*padding\s*:\s*14px 20px 24px/s);
  assert.doesNotMatch(adminCss, /\.admin-tag-rule-keyword-input-row input\s*\{/);
  assert.match(adminCss, /\.admin-tag-rule-form textarea\s*\{[^}]*min-height\s*:\s*72px/s);
  assert.match(tagsPageSource, /function tagRuleDraft\(tag: api\.AdminTag\): RuleDraft/);
  assert.match(tagsPageSource, /function matchRulesFromDraft\(draft: RuleDraft, isAV: boolean\): api\.TagMatchRules/);
  assert.match(tagsPageSource, /function splitRuleTerms/);
  assert.match(tagsPageSource, /function splitPrefixTerms/);
});

test("admin videos render tag assignment source and evidence", () => {
  assert.match(apiSource, /tagSources\?: Record<string, string>/);
  assert.match(apiSource, /tagEvidence\?: Record<string, string>/);
  assert.match(videosPageSource, /data-source=\{v\.tagSources\?\.\[t\]/);
  assert.match(videosPageSource, /tagAssignmentSourceLabel/);
  assert.match(videosPageSource, /tagAssignmentTitle/);
  assert.match(videosPageSource, /video\.tagEvidence\?\.\[tag\.label\]/);
});
