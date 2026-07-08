import { useEffect, useMemo, useState } from "react";
import { Film, RefreshCw, Search, Trash2 } from "lucide-react";
import * as api from "./api";
import { useToast } from "./ToastContext";
import { ConfirmModal } from "./ConfirmModal";
import { Modal } from "./Modal";
import { AdminEmptyVisual } from "./AdminEmptyVisual";

const DESKTOP_TAGS_PAGE_SIZE = 25;
const MOBILE_TAGS_PAGE_SIZE = 8;
const TAGS_MOBILE_QUERY = "(max-width: 640px)";
const TAG_SOURCE_FILTERS = ["builtin", "user", "generated"];
const TAG_DISPLAY_GROUP_ORDER: Record<string, number> = {
  builtin: 0,
  user: 1,
  crawler: 2,
  av: 3,
};

type DeleteConfirmState =
  | { kind: "single"; tag: api.AdminTag }
  | { kind: "bulk"; ids: number[] }
  | null;

export function TagsPage() {
  const [tags, setTags] = useState<api.AdminTag[]>([]);
  const [label, setLabel] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [saving, setSaving] = useState(false);
  const [deletingId, setDeletingId] = useState<number | null>(null);
  const [deleteConfirm, setDeleteConfirm] = useState<DeleteConfirmState>(null);
  const [searchQuery, setSearchQuery] = useState("");
  const [filterSource, setFilterSource] = useState<string>("all");
  const [selectMode, setSelectMode] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [bulkDeleting, setBulkDeleting] = useState(false);
  const [editingTag, setEditingTag] = useState<api.AdminTag | null>(null);
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const pageSize = useTagsPageSize();
  const [page, setPage] = useState(1);
  const { show } = useToast();

  async function refresh() {
    setLoading(true);
    setLoadError("");
    try {
      setTags(await api.listTags());
    } catch (e) {
      const message = e instanceof Error ? e.message : "加载标签失败";
      setLoadError(message);
      show(message, "error");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    refresh();
  }, []);

  async function handleCreate() {
    const cleanLabel = label.trim();
    if (!cleanLabel) return;
    if (createLabelExists) return;
    setSaving(true);
    try {
      const r = await api.createTag(cleanLabel);
      show(`已添加标签「${r.label}」`, "success");
      setLabel("");
      setCreateModalOpen(false);
      await refresh();
    } catch (e) {
      show(e instanceof Error ? e.message : "添加标签失败", "error");
    } finally {
      setSaving(false);
    }
  }

  function handleDelete(tag: api.AdminTag) {
    setDeleteConfirm({ kind: "single", tag });
  }

  function openCreateModal() {
    setLabel("");
    setCreateModalOpen(true);
  }

  function toggleSelectMode() {
    setSelectMode((m) => !m);
    setSelected(new Set());
  }

  function toggleSelect(id: number) {
    setSelected((prev) => {
      const next = new Set(prev);
      next.has(id) ? next.delete(id) : next.add(id);
      return next;
    });
  }

  async function handleBulkDelete() {
    const ids = [...selected];
    if (ids.length === 0) return;
    setDeleteConfirm({ kind: "bulk", ids });
  }

  async function confirmDelete() {
    if (!deleteConfirm) return;

    if (deleteConfirm.kind === "single") {
      const tag = deleteConfirm.tag;
      setDeletingId(tag.id);
      try {
        const r = await api.deleteTag(tag.id);
        show(`已删除标签，并从 ${r.removedVideos} 个视频移除`, "success");
        setDeleteConfirm(null);
        await refresh();
      } catch (e) {
        show(e instanceof Error ? e.message : "删除标签失败", "error");
      } finally {
        setDeletingId(null);
      }
      return;
    }

    const ids = deleteConfirm.ids;
    setBulkDeleting(true);
    try {
      let success = 0;
      for (const id of ids) {
        try {
          await api.deleteTag(id);
          success++;
        } catch {
          // Keep deleting the rest of the selected tags; report aggregate failure below.
        }
      }
      const failed = ids.length - success;
      show(
        failed ? `批量删除完成，成功 ${success} / ${ids.length} 个，失败 ${failed} 个` : `已删除 ${success} 个标签`,
        failed ? (success > 0 ? "info" : "error") : "success"
      );
      setSelected(new Set());
      setSelectMode(false);
      setDeleteConfirm(null);
      await refresh();
    } finally {
      setBulkDeleting(false);
    }
  }

  const stats = useMemo(() => {
    const sourceCounts: Record<string, number> = {};
    let total = 0;

    tags.forEach((t) => {
      if (!isSupportedTag(t)) return;
      total++;
      const key = tagSourceKey(t);
      sourceCounts[key] = (sourceCounts[key] ?? 0) + 1;
    });

    return {
      total,
      sourceCounts,
    };
  }, [tags]);

  const filteredTags = useMemo(() => {
    const query = searchQuery.trim().toLowerCase();
    const matches = tags.filter((t) => {
      if (!isSupportedTag(t)) return false;
      const matchesSearch =
        !query ||
        t.label.toLowerCase().includes(query) ||
        tagRuleTerms(t).some((term) => term.toLowerCase().includes(query));
      const matchesSource = filterSource === "all" || tagSourceKey(t) === filterSource;
      return matchesSearch && matchesSource;
    });

    if (filterSource !== "all") return matches;

    return matches
      .map((tag, index) => ({ tag, index }))
      .sort((a, b) => {
        const rankDelta = tagDisplayGroupRank(a.tag) - tagDisplayGroupRank(b.tag);
        return rankDelta || a.index - b.index;
      })
      .map(({ tag }) => tag);
  }, [tags, searchQuery, filterSource]);
  const hasActiveSearch = searchQuery.trim().length > 0;
  const searchEmpty = hasActiveSearch && !loading && !loadError && filteredTags.length === 0;

  const totalPages = Math.max(1, Math.ceil(filteredTags.length / pageSize));
  const currentPage = Math.min(page, totalPages);
  const pageStartIndex = (currentPage - 1) * pageSize;
  const pageEndIndex = pageStartIndex + pageSize;
  const pagedTags = useMemo(
    () => filteredTags.slice(pageStartIndex, pageEndIndex),
    [filteredTags, pageStartIndex, pageEndIndex]
  );
  const pageStart = filteredTags.length === 0 ? 0 : pageStartIndex + 1;
  const pageEnd = Math.min(filteredTags.length, pageEndIndex);

  useEffect(() => {
    setPage(1);
  }, [searchQuery, filterSource, pageSize]);

  useEffect(() => {
    setPage((p) => Math.min(Math.max(1, p), totalPages));
  }, [totalPages]);

  const deletablePageTags = useMemo(
    () => pagedTags,
    [pagedTags]
  );
  const allSelected =
    deletablePageTags.length > 0 && deletablePageTags.every((t) => selected.has(t.id));
  const createLabelExists = useMemo(() => {
    const cleanLabel = label.trim().toLowerCase();
    if (!cleanLabel) return false;
    return tags.some((tag) => tag.label.trim().toLowerCase() === cleanLabel);
  }, [label, tags]);

  function selectPageTags() {
    setSelected((prev) => {
      const next = new Set(prev);
      deletablePageTags.forEach((t) => next.add(t.id));
      return next;
    });
  }

  return (
    <section className={`admin-tags-page${selectMode ? " has-bulk-actions" : ""}${searchEmpty ? " is-search-empty" : ""}`}>
      <div className="admin-tags-layout">
        <div className="admin-tags-main">
          <div className="admin-tags-toolbar">
            <div className="admin-tags-search">
              <Search className="admin-tags-search__icon" size={14} />
              <input
                aria-label="搜索标签名或包含词"
                type="text"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder="搜索标签名或包含词"
              />
            </div>

            <aside className="admin-tags-filter-panel" aria-label="标签分类">
              <div className="admin-tags-filter-tabs">
                <button
                  type="button"
                  className={`admin-tags-filter-tab ${filterSource === "all" ? "is-active" : ""}`}
                  onClick={() => setFilterSource("all")}
                  aria-label="全部"
                >
                  <span className="admin-tags-filter-tab__text">全部</span>
                </button>
                {TAG_SOURCE_FILTERS.filter((source) => (stats.sourceCounts[source] ?? 0) > 0).map((source) => {
                  const label = sourceLabel(source);
                  return (
                    <button
                      key={source}
                      type="button"
                      className={`admin-tags-filter-tab ${filterSource === source ? "is-active" : ""}`}
                      onClick={() => setFilterSource(source)}
                      aria-label={label}
                    >
                      <span className="admin-tags-filter-tab__text">{label}</span>
                    </button>
                  );
                })}
              </div>
            </aside>

            <div className="admin-tags-toolbar-actions">
              <button
                type="button"
                className="admin-btn"
                onClick={openCreateModal}
              >
                新增标签
              </button>
              <button
                type="button"
                className={`admin-btn ${selectMode ? "is-primary" : ""}`}
                onClick={toggleSelectMode}
              >
                {selectMode ? "退出批量" : "批量删除"}
              </button>
            </div>
          </div>

          {searchEmpty ? (
            <AdminEmptyVisual
              variant="no-results"
              text="未查询到"
              className="admin-empty-state admin-empty-state--plain admin-tags-empty-search"
            />
          ) : (
            <div className="admin-tags-board">
              <div className="admin-tags-cards">
                {loading ? (
                  <div className="admin-loading-state">
                    <RefreshCw size={20} className="admin-spin" />
                    <span>加载中...</span>
                  </div>
                ) : loadError ? (
                  <div className="admin-error-state">
                    <strong>标签加载失败</strong>
                    <span>{loadError}</span>
                    <button type="button" className="admin-btn" onClick={refresh}>
                      <RefreshCw size={13} /> 重试
                    </button>
                  </div>
                ) : filteredTags.length === 0 ? (
                  <div className="admin-card admin-empty">
                    没有找到匹配的标签。
                  </div>
                ) : (
                <>
                  <div className="admin-tags-grid">
                    {pagedTags.map((tag) => {
                      const selectable = selectMode;
                      const isSelected = selected.has(tag.id);
                      const cardClass = `admin-tag-card${selectable ? " is-selectable" : ""}${
                        selectable && isSelected ? " is-selected" : ""
                      }`;
                      const cardContent = (
                        <>
                          <div className="admin-tag-card__head">
                            <span className="admin-tag-card__title">{tag.label}</span>
                            <span className="admin-tag-card__source-badge" data-source={tagCardSourceKey(tag)}>
                              {tagCardSourceLabel(tag)}
                            </span>
                          </div>

                          <div className="admin-tag-card__footer">
                            <span className="admin-tag-card__count">
                              <Film size={13} />
                              <strong>{tag.count}</strong> 视频
                            </span>
                            <div className="admin-tag-card__footer-actions">
                              {!selectMode && (
                                <button
                                  type="button"
                                  className="admin-tag-card__delete"
                                  onClick={() => handleDelete(tag)}
                                  disabled={deletingId === tag.id}
                                  aria-label={`删除标签 ${tag.label}`}
                                >
                                  <span>{deletingId === tag.id ? "删除中" : "删除"}</span>
                                </button>
                              )}
                              {!selectMode && (
                                <button
                                  type="button"
                                  className="admin-tag-card__edit"
                                  onClick={() => setEditingTag(tag)}
                                  aria-label={`编辑标签 ${tag.label}`}
                                >
                                  <span>编辑</span>
                                </button>
                              )}
                            </div>
                          </div>
                        </>
                      );
                      return selectable ? (
                        <button
                          key={tag.id}
                          type="button"
                          className={cardClass}
                          onClick={() => toggleSelect(tag.id)}
                          aria-pressed={isSelected}
                          aria-label={`${isSelected ? "取消选中" : "选中"}标签 ${tag.label}`}
                        >
                          {cardContent}
                        </button>
                      ) : (
                        <div key={tag.id} className={cardClass}>
                          {cardContent}
                        </div>
                      );
                    })}
                  </div>

                  {totalPages > 1 && (
                    <div className="admin-table-pagination admin-tags-pagination">
                      <button
                        type="button"
                        className="admin-btn"
                        onClick={() => setPage(1)}
                        disabled={currentPage <= 1}
                      >
                        首页
                      </button>
                      <button
                        type="button"
                        className="admin-btn"
                        onClick={() => setPage((p) => Math.max(1, p - 1))}
                        disabled={currentPage <= 1}
                      >
                        上一页
                      </button>
                      <span className="admin-table-pagination__info">
                        第 {currentPage} / {totalPages} 页，显示 {pageStart}-{pageEnd} / {filteredTags.length}，每页 {pageSize} 个
                      </span>
                      <button
                        type="button"
                        className="admin-btn"
                        onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
                        disabled={currentPage >= totalPages}
                      >
                        下一页
                      </button>
                      <button
                        type="button"
                        className="admin-btn"
                        onClick={() => setPage(totalPages)}
                        disabled={currentPage >= totalPages}
                      >
                        末页
                      </button>
                    </div>
                  )}
                </>
                )}
              </div>
            </div>
          )}
        </div>
      </div>
      {selectMode && (
        <div className="admin-tags-bulk-toolbar" role="region" aria-label="标签批量操作">
          <div className="admin-tags-bulk-actions">
            <span className="admin-tags-bulk-actions__count">已选择 {selected.size} 项</span>
            <button
              type="button"
              className="admin-btn admin-tags-bulk-actions__btn admin-tags-bulk-actions__select-page"
              onClick={selectPageTags}
              disabled={deletablePageTags.length === 0 || allSelected}
            >
              全选本页
            </button>
            <button
              type="button"
              className="admin-btn admin-tags-bulk-actions__btn"
              onClick={() => setSelected(new Set())}
              disabled={selected.size === 0}
            >
              取消选中
            </button>
            <button
              type="button"
              className="admin-btn is-danger admin-tags-bulk-actions__btn"
              onClick={handleBulkDelete}
              disabled={selected.size === 0 || bulkDeleting}
            >
              <Trash2 size={13} /> {bulkDeleting ? "删除中..." : "删除选中"}
            </button>
          </div>
        </div>
      )}
      <Modal
        open={createModalOpen}
        title="新增标签"
        className="admin-modal--tag-rules admin-modal--tag-dialog admin-modal--tag-create"
        onClose={() => {
          if (!saving) setCreateModalOpen(false);
        }}
        footer={
          <>
            <button
              type="button"
              className="admin-btn"
              onClick={() => setCreateModalOpen(false)}
              disabled={saving}
            >
              取消
            </button>
            <button
              type="submit"
              form="admin-create-tag-form"
              className="admin-btn is-primary"
              disabled={saving || !label.trim() || createLabelExists}
            >
              {saving ? "添加中..." : "确认"}
            </button>
          </>
        }
      >
        <form
          id="admin-create-tag-form"
          className="admin-form admin-tag-rule-form"
          onSubmit={(e) => {
            e.preventDefault();
            handleCreate();
          }}
        >
          <div className="admin-form__row admin-tag-create-row">
            <input
              id="admin-tag-label"
              aria-label="输入标签名"
              aria-describedby={createLabelExists ? "admin-tag-create-warning" : undefined}
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder="输入标签名"
            />
            <span
              className={`admin-tag-create-warning${createLabelExists ? " is-visible" : ""}`}
              id="admin-tag-create-warning"
              aria-hidden={!createLabelExists}
            >
              当前标签已存在
            </span>
          </div>
        </form>
      </Modal>
      {editingTag && (
        <EditTagModal
          tag={editingTag}
          onClose={() => setEditingTag(null)}
          onChanged={async () => {
            await refresh();
          }}
        />
      )}
      <ConfirmModal
        open={!!deleteConfirm}
        title={deleteConfirm?.kind === "bulk" ? "删除选中标签" : "删除标签"}
        message={
          deleteConfirm?.kind === "bulk"
            ? `确定要删除选中的 ${deleteConfirm.ids.length} 个标签吗？`
            : `确定要删除标签「${deleteConfirm?.tag.label ?? ""}」吗？`
        }
        confirmText="确认"
        danger
        centerMessage
        modalClassName="admin-modal--delete-confirm admin-modal--tag-dialog admin-modal--tag-delete-confirm"
        loading={deletingId !== null || bulkDeleting}
        restoreFocus={false}
        onCancel={() => {
          if (deletingId === null && !bulkDeleting) setDeleteConfirm(null);
        }}
        onConfirm={confirmDelete}
      />
    </section>
  );
}

function EditTagModal({
  tag,
  onClose,
  onChanged,
}: {
  tag: api.AdminTag;
  onClose: () => void;
  onChanged: () => void | Promise<void>;
}) {
  const [draft, setDraft] = useState(() => tagRuleDraft(tag));
  const [saving, setSaving] = useState(false);
  const { show } = useToast();
  const isAV = isAVTag(tag);

  async function persistDraft(nextDraft: RuleDraft) {
    const parsedRules = matchRulesFromDraft(nextDraft, isAV);
    if (!isAV && !hasRuleTerms(parsedRules)) {
      show("至少保留一个包含词", "error");
      return;
    }
    setSaving(true);
    try {
      await api.updateTag(tag.id, parsedRules);
      setDraft(nextDraft);
      show("标签已保存", "success");
      await onChanged();
    } catch (e) {
      show(e instanceof Error ? e.message : "保存标签失败", "error");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      open
      title={tag.label}
      className="admin-modal--tag-rules admin-modal--tag-dialog"
      onClose={onClose}
      restoreFocus={false}
    >
      <div className="admin-form admin-tag-rule-form">
        {isAV ? (
          <div className="admin-form__row">
            <PrefixPillEditor
              value={draft.avCodePrefixes}
              onCommit={(value) => void persistDraft({ ...draft, avCodePrefixes: value })}
              disabled={saving}
            />
          </div>
        ) : (
          <div className="admin-form__row">
            <KeywordPillEditor
              value={draft.keywords}
              onCommit={(value) => void persistDraft({ ...draft, keywords: value })}
              disabled={saving}
            />
          </div>
        )}
      </div>
    </Modal>
  );
}

function KeywordPillEditor({
  value,
  onCommit,
  disabled,
}: {
  value: string;
  onCommit: (value: string) => void;
  disabled: boolean;
}) {
  return (
    <RulePillEditor
      value={value}
      onCommit={onCommit}
      disabled={disabled}
      inputId="admin-tag-rule-keywords"
      inputLabel="添加包含词"
      listLabel="当前包含词"
      emptyText="暂无包含词"
      duplicateText="当前包含词已存在"
      warningId="admin-tag-rule-keyword-warning"
      removeLabelPrefix="移除包含词"
      splitTerms={splitRuleTerms}
    />
  );
}

function PrefixPillEditor({
  value,
  onCommit,
  disabled,
}: {
  value: string;
  onCommit: (value: string) => void;
  disabled: boolean;
}) {
  return (
    <RulePillEditor
      value={value}
      onCommit={onCommit}
      disabled={disabled}
      inputId="admin-tag-rule-prefixes"
      inputLabel="添加车牌前缀"
      listLabel="当前车牌前缀"
      emptyText="暂无车牌前缀"
      duplicateText="当前车牌前缀已存在"
      warningId="admin-tag-rule-prefix-warning"
      removeLabelPrefix="移除车牌前缀"
      splitTerms={splitPrefixTerms}
      allowEmpty
    />
  );
}

function RulePillEditor({
  value,
  onCommit,
  disabled,
  inputId,
  inputLabel,
  listLabel,
  emptyText,
  duplicateText,
  warningId,
  removeLabelPrefix,
  splitTerms,
  allowEmpty = false,
}: {
  value: string;
  onCommit: (value: string) => void;
  disabled: boolean;
  inputId: string;
  inputLabel: string;
  listLabel: string;
  emptyText: string;
  duplicateText: string;
  warningId: string;
  removeLabelPrefix: string;
  splitTerms: (value: string) => string[];
  allowEmpty?: boolean;
}) {
  const [input, setInput] = useState("");
  const terms = splitTerms(value);
  const pendingTerm = singleRuleTerm(input, splitTerms);
  const pendingExists = terms.some((term) => term.toLowerCase() === pendingTerm.toLowerCase());
  const showDuplicateWarning = pendingTerm !== "" && pendingExists;
  const canRemoveTerm = allowEmpty || terms.length > 1;

  function commitTerms(nextTerms: string[]) {
    onCommit(joinRuleTerms(splitTerms(nextTerms.join("\n"))));
  }

  function addInputTerm() {
    if (!pendingTerm || pendingExists) return;
    commitTerms([...terms, pendingTerm]);
    setInput("");
  }

  function removeTerm(term: string) {
    if (!canRemoveTerm) return;
    commitTerms(terms.filter((item) => item !== term));
  }

  return (
    <div className="admin-tag-rule-keyword-editor">
      <div className="admin-tag-rule-keyword-list" aria-label={listLabel}>
        {terms.length > 0 ? (
          terms.map((term) => (
            <span key={term} className="admin-tag-rule-keyword-pill">
              <span>{term}</span>
              <button
                type="button"
                onClick={() => removeTerm(term)}
                disabled={disabled || !canRemoveTerm}
                aria-label={`${removeLabelPrefix} ${term}`}
              >
                移除
              </button>
            </span>
          ))
        ) : (
          <span className="admin-tag-rule-keyword-empty">{emptyText}</span>
        )}
      </div>
      <div className="admin-tag-rule-keyword-input-row">
        <input
          id={inputId}
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== "Enter") return;
            e.preventDefault();
            addInputTerm();
          }}
          disabled={disabled}
          aria-describedby={showDuplicateWarning ? warningId : undefined}
          aria-label={inputLabel}
          placeholder={inputLabel}
        />
        <button
          type="button"
          className="admin-btn"
          onClick={addInputTerm}
          disabled={disabled || !pendingTerm || pendingExists}
        >
          添加
        </button>
      </div>
      {showDuplicateWarning && (
        <span className="admin-tag-rule-keyword-warning" id={warningId}>
          {duplicateText}
        </span>
      )}
    </div>
  );
}

function useTagsPageSize() {
  const [pageSize, setPageSize] = useState(() =>
    window.matchMedia(TAGS_MOBILE_QUERY).matches
      ? MOBILE_TAGS_PAGE_SIZE
      : DESKTOP_TAGS_PAGE_SIZE
  );

  useEffect(() => {
    const media = window.matchMedia(TAGS_MOBILE_QUERY);
    const update = () => {
      setPageSize(media.matches ? MOBILE_TAGS_PAGE_SIZE : DESKTOP_TAGS_PAGE_SIZE);
    };
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);

  return pageSize;
}

type RuleDraft = {
  keywords: string;
  avCodePrefixes: string;
};

function tagRuleDraft(tag: api.AdminTag): RuleDraft {
  const rules = tag.matchRules ?? {};
  return {
    keywords: joinRuleTerms(rules.keywords),
    avCodePrefixes: joinRuleTerms(rules.avCodePrefixes),
  };
}

function matchRulesFromDraft(draft: RuleDraft, isAV: boolean): api.TagMatchRules {
  if (isAV) {
    return {
      matchAvCode: true,
      avCodePrefixes: splitPrefixTerms(draft.avCodePrefixes),
    };
  }
  return {
    keywords: splitRuleTerms(draft.keywords),
  };
}

function tagRuleTerms(tag: api.AdminTag): string[] {
  return ruleTerms(tag.matchRules ?? {});
}

function hasRuleTerms(rules: api.TagMatchRules): boolean {
  return [
    ...(rules.keywords ?? []),
    ...(rules.avCodePrefixes ?? []),
  ].length > 0;
}

function ruleTerms(rules: api.TagMatchRules): string[] {
  return [
    ...(rules.keywords ?? []),
    ...(rules.avCodePrefixes ?? []),
  ];
}

function joinRuleTerms(terms?: string[]): string {
  return (terms ?? []).join("\n");
}

function splitRuleTerms(value: string): string[] {
  return uniqueTerms(value.split(/[\n,，、;；]+/));
}

function singleRuleTerm(value: string, splitTerms: (value: string) => string[] = splitRuleTerms): string {
  return splitTerms(value)[0] ?? "";
}

function splitPrefixTerms(value: string): string[] {
  return uniqueTerms(value.split(/[\s,，、;；]+/).map((term) => term.toUpperCase()));
}

function uniqueTerms(terms: string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const term of terms) {
    const clean = term.trim();
    const key = clean.toLowerCase();
    if (!clean || seen.has(key)) continue;
    seen.add(key);
    out.push(clean);
  }
  return out;
}

function isAVTag(tag: api.AdminTag): boolean {
  return tag.label.trim().toUpperCase() === "AV";
}

function sourceLabel(source: string): string {
  if (source === "crawler" || source === "generated") return "自动生成";
  if (source === "builtin") return "内置";
  if (source === "user") return "自定义";
  return source || "未知";
}

function tagCardSourceLabel(tag: api.AdminTag): string {
  if (tag.crawlerOwned || tag.source === "crawler") return "爬虫脚本";
  if (tag.source === "generated") return "AV";
  return sourceLabel(tag.source);
}

function tagCardSourceKey(tag: api.AdminTag): string {
  if (tag.crawlerOwned || tag.source === "crawler") return "crawler";
  if (tag.source === "generated") return "av";
  return tag.source || "";
}

function tagDisplayGroupKey(tag: api.AdminTag): string {
  if (tag.source === "builtin" || tag.source === "user") return tag.source;
  if (tag.crawlerOwned || tag.source === "crawler") return "crawler";
  if (tag.source === "generated") return "av";
  return tag.source || "";
}

function tagDisplayGroupRank(tag: api.AdminTag): number {
  return TAG_DISPLAY_GROUP_ORDER[tagDisplayGroupKey(tag)] ?? 99;
}

function tagSourceKey(tag: api.AdminTag): string {
  return tag.crawlerOwned || tag.source === "generated" ? "generated" : tag.source;
}

function isSupportedTag(tag: api.AdminTag): boolean {
  return tag.source === "builtin" || tag.source === "user" || tag.source === "generated" || tag.crawlerOwned === true;
}
