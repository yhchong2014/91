import { useEffect, useId, useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  ChevronDown,
  Edit,
  RefreshCw,
  Search,
  CheckSquare,
  Square,
  Image,
  Trash2,
  Ban,
  RotateCcw,
} from "lucide-react";
import * as api from "./api";
import { useToast } from "./ToastContext";
import { Modal } from "./Modal";
import { ConfirmModal } from "./ConfirmModal";
import { formatBytes } from "./storageFormat";

const DESKTOP_VIDEOS_PAGE_SIZE = 50;
const MOBILE_VIDEOS_PAGE_SIZE = 20;
const VIDEOS_MOBILE_QUERY = "(max-width: 640px)";
const REGEN_PREVIEW_STATUS = "generating";
const REGEN_PREVIEW_POLL_INTERVAL_MS = 2000;
const REGEN_PREVIEW_TRACK_TIMEOUT_MS = 30 * 60 * 1000;

type TabKey = "current" | "blacklist";

type RegenPreviewState = {
  expiresAt: number;
  originalUpdatedAt: number;
};

const TABS: { key: TabKey; label: string }[] = [
  { key: "current", label: "当前视频" },
  { key: "blacklist", label: "拉黑视频" },
];

/**
 * 视频管理容器：顶部分段标签在「当前 / 隐藏 / 拉黑」三个视图间切换，
 * 激活标签同步到 URL ?tab=，标签上的计数来自 /videos/stats。
 */
export function VideosPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawTab = searchParams.get("tab");
  const activeTab: TabKey = rawTab === "blacklist" ? "blacklist" : "current";
  const [stats, setStats] = useState<api.VideoStats | null>(null);

  async function refreshStats() {
    try {
      setStats(await api.getVideoStats());
    } catch {
      // 计数仅用于标签徽标，失败不阻塞主流程。
    }
  }

  useEffect(() => {
    refreshStats();
  }, []);

  function selectTab(key: TabKey) {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        if (key === "current") next.delete("tab");
        else next.set("tab", key);
        return next;
      },
      { replace: true }
    );
  }

  const counts: Record<TabKey, number | undefined> = {
    current: stats?.current,
    blacklist: stats?.blacklisted,
  };

  return (
    <section>
      <header className="admin-page__header">
        <h1 className="admin-page__title">视频管理</h1>
      </header>

      <div className="admin-video-tabs" role="tablist" aria-label="视频管理标签页">
        {TABS.map((t) => (
          <button
            key={t.key}
            type="button"
            role="tab"
            aria-selected={activeTab === t.key}
            className={`admin-video-tab ${activeTab === t.key ? "is-active" : ""}`}
            onClick={() => selectTab(t.key)}
          >
            <span>{t.label}</span>
            {counts[t.key] !== undefined && (
              <span className="admin-video-tab__count">{counts[t.key]}</span>
            )}
          </button>
        ))}
      </div>

      {activeTab === "current" && <CurrentVideosTab onStatsChanged={refreshStats} />}
      {activeTab === "blacklist" && <BlacklistTab onStatsChanged={refreshStats} />}
    </section>
  );
}

// ---------- 当前视频 ----------

function CurrentVideosTab({ onStatsChanged }: { onStatsChanged: () => void }) {
  const [list, setList] = useState<api.AdminVideo[]>([]);
  const [drives, setDrives] = useState<api.AdminDrive[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [keyword, setKeyword] = useState("");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [driveId, setDriveId] = useState("");
  const [page, setPage] = useState(1);
  const [total, setTotal] = useState(0);
  const [editing, setEditing] = useState<api.AdminVideo | null>(null);
  const [availableTags, setAvailableTags] = useState<api.AdminTag[]>([]);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [batchRegenOpen, setBatchRegenOpen] = useState(false);
  const [batchRegening, setBatchRegening] = useState(false);
  const [batchDeleteOpen, setBatchDeleteOpen] = useState(false);
  const [batchDeleting, setBatchDeleting] = useState(false);
  const [batchDeleteSource, setBatchDeleteSource] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<api.AdminVideo | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteSource, setDeleteSource] = useState(false);
  const [regenPreviewById, setRegenPreviewById] = useState<Record<string, RegenPreviewState>>({});
  const pageSize = useVideosPageSize();
  const { show } = useToast();

  async function refresh() {
    setLoading(true);
    setLoadError("");
    try {
      const [r, tagList, driveList] = await Promise.all([
        api.listVideos({ driveId, page, size: pageSize, keyword: searchKeyword }),
        api.listTags(),
        api.listDrives(),
      ]);
      setList(r.items ?? []);
      setTotal(r.total ?? 0);
      setAvailableTags(tagList);
      setDrives(driveList ?? []);
      setSelectedIds(new Set());
    } catch (e) {
      const message = e instanceof Error ? e.message : "加载失败";
      setLoadError(message);
      show(message, "error");
    } finally {
      setLoading(false);
    }
  }

  async function refreshListOnly() {
    try {
      const r = await api.listVideos({ driveId, page, size: pageSize, keyword: searchKeyword });
      setList(r.items ?? []);
      setTotal(r.total ?? 0);
    } catch {
      // Polling is only used to clear optimistic preview-generation state.
    }
  }

  const trackedRegenCount = Object.keys(regenPreviewById).length;
  const hasGeneratingPreview = list.some((v) => v.previewStatus === REGEN_PREVIEW_STATUS);

  useEffect(() => {
    refresh();
  }, [driveId, page, searchKeyword, pageSize]);

  useEffect(() => {
    setPage(1);
  }, [pageSize]);

  useEffect(() => {
    if (keyword === searchKeyword) return;
    const timer = window.setTimeout(() => {
      setSearchKeyword(keyword);
      setPage(1);
    }, 300);
    return () => window.clearTimeout(timer);
  }, [keyword]);

  useEffect(() => {
    if (trackedRegenCount === 0 && !hasGeneratingPreview) return;
    const timer = window.setInterval(() => {
      refreshListOnly();
    }, REGEN_PREVIEW_POLL_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [trackedRegenCount, hasGeneratingPreview, driveId, page, pageSize, searchKeyword]);

  useEffect(() => {
    if (trackedRegenCount === 0) return;
    const now = Date.now();
    setRegenPreviewById((current) => {
      const next = { ...current };
      let changed = false;
      const byId = new Map(list.map((v) => [v.id, v]));
      for (const [id, state] of Object.entries(current)) {
        const video = byId.get(id);
        const updatedAt = videoUpdatedAtMs(video);
        if (!video || now >= state.expiresAt || updatedAt > state.originalUpdatedAt) {
          delete next[id];
          changed = true;
        }
      }
      return changed ? next : current;
    });
  }, [list, trackedRegenCount]);

  const driveNameMap = new Map(drives.map((d) => [d.id, d.name || d.id]));

  const listItems = list;
  const totalPages = Math.max(1, Math.ceil(total / pageSize));

  async function handleRegen(v: api.AdminVideo) {
    try {
      await api.regenPreview(v.id);
      trackRegeneratingPreview([v]);
      show("已触发预览视频重生", "success");
    } catch (e) {
      show(e instanceof Error ? e.message : "触发失败", "error");
    }
  }

  async function handleBatchRegen() {
    if (selectedIds.size === 0) return;
    setBatchRegenOpen(true);
  }

  async function handleBatchDelete() {
    if (selectedIds.size === 0) return;
    setBatchDeleteSource(false);
    setBatchDeleteOpen(true);
  }

  async function confirmBatchRegen() {
    const ids = [...selectedIds];
    const videoById = new Map(listItems.map((v) => [v.id, v]));
    setBatchRegening(true);
    let success = 0;
    try {
      const results = await Promise.allSettled(ids.map((id) => api.regenPreview(id)));
      const acceptedVideos: api.AdminVideo[] = [];
      results.forEach((r, index) => {
        if (r.status === "fulfilled") {
          const video = videoById.get(ids[index]);
          if (video) acceptedVideos.push(video);
          success++;
        }
      });
      trackRegeneratingPreview(acceptedVideos);
      show(
        `批量触发完成，成功 ${success} / ${ids.length} 个`,
        success === ids.length ? "success" : "info"
      );
      setSelectedIds(new Set());
      setBatchRegenOpen(false);
    } finally {
      setBatchRegening(false);
    }
  }

  function trackRegeneratingPreview(videos: api.AdminVideo[]) {
    if (videos.length === 0) return;
    const startedAt = Date.now();
    setRegenPreviewById((current) => {
      const next = { ...current };
      for (const v of videos) {
        next[v.id] = {
          expiresAt: startedAt + REGEN_PREVIEW_TRACK_TIMEOUT_MS,
          originalUpdatedAt: videoUpdatedAtMs(v),
        };
      }
      return next;
    });
  }

  function isPreviewGenerating(v: api.AdminVideo) {
    return !!regenPreviewById[v.id] || v.previewStatus === REGEN_PREVIEW_STATUS;
  }

  async function confirmDeleteVideo() {
    if (!deleteTarget) return;
    const target = deleteTarget;
    setDeleting(true);
    try {
      const result = await api.deleteVideo(target.id, { deleteSource });
      setDeleteTarget(null);
      setDeleteSource(false);
      setSelectedIds((ids) => {
        const next = new Set(ids);
        next.delete(target.id);
        return next;
      });
      show(result.deletedSource ? "已删除视频，并清理源文件" : "已删除视频", "success");
      onStatsChanged();
      if (listItems.length === 1 && page > 1) {
        setPage((p) => Math.max(1, p - 1));
      } else {
        refresh();
      }
    } catch (e) {
      show(e instanceof Error ? e.message : "删除失败", "error");
    } finally {
      setDeleting(false);
    }
  }

  async function confirmBatchDelete() {
    const ids = [...selectedIds];
    if (ids.length === 0) return;
    setBatchDeleting(true);
    try {
      let success = 0;
      let deletedSources = 0;
      for (const id of ids) {
        try {
          const result = await api.deleteVideo(id, { deleteSource: batchDeleteSource });
          success++;
          if (result.deletedSource) deletedSources++;
        } catch {
          // Keep deleting the rest of the selected videos; report aggregate failure below.
        }
      }
      const failed = ids.length - success;
      if (failed === 0) {
        const extra = deletedSources > 0 ? `，其中 ${deletedSources} 个清理了源文件` : "";
        show(`批量删除完成，成功 ${success} 个${extra}`, "success");
      } else {
        show(
          `批量删除完成，成功 ${success} / ${ids.length} 个，失败 ${failed} 个`,
          success > 0 ? "info" : "error"
        );
      }
      setSelectedIds(new Set());
      setBatchDeleteOpen(false);
      setBatchDeleteSource(false);
      onStatsChanged();
      if (success >= listItems.length && page > 1) {
        setPage((p) => Math.max(1, p - 1));
      } else {
        refresh();
      }
    } finally {
      setBatchDeleting(false);
    }
  }

  const toggleSelectAll = () => {
    if (selectedIds.size === listItems.length && listItems.length > 0) {
      setSelectedIds(new Set());
    } else {
      setSelectedIds(new Set(listItems.map((v) => v.id)));
    }
  };

  const toggleSelect = (id: string) => {
    const next = new Set(selectedIds);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    setSelectedIds(next);
  };

  function handleSearchSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSearchKeyword(keyword);
    setPage(1);
  }

  return (
    <div className={`admin-videos-current${selectedIds.size > 0 ? " has-bulk-actions" : ""}`}>
      <div className="admin-page__actions admin-videos-filter admin-videos-filter--current">
        <DriveFilter drives={drives} driveId={driveId} onChange={(id) => { setDriveId(id); setPage(1); }} />
        <SearchBox keyword={keyword} onChange={setKeyword} onSubmit={handleSearchSubmit} />
        <button type="button" className="admin-btn admin-videos-filter__refresh" onClick={refresh} aria-label="刷新当前视频">
          <RefreshCw size={13} />
          <span className="admin-videos-filter__refresh-text">刷新</span>
        </button>
      </div>

      {!loading && selectedIds.size > 0 && (
        <div className="admin-videos-list-toolbar">
          <div className="admin-videos-bulk-actions">
            <span className="admin-videos-bulk-actions__count">已选择 {selectedIds.size} 项</span>
            <button type="button" className="admin-btn is-primary admin-videos-bulk-actions__btn" onClick={handleBatchRegen}>
              <RefreshCw size={13} /> 批量重生预览视频
            </button>
            <button type="button" className="admin-btn is-danger admin-videos-bulk-actions__btn" onClick={handleBatchDelete}>
              <Trash2 size={13} /> 批量删除
            </button>
          </div>
        </div>
      )}

      {loading ? (
        <LoadingState />
      ) : loadError ? (
        <ErrorState message={loadError} onRetry={refresh} />
      ) : listItems.length === 0 ? (
        <div className="admin-empty-state">
          <div className="admin-empty-state__icon">
            <Image size={48} />
          </div>
          <div className="admin-empty-state__text">
            {driveId
              ? "这个网盘下还没有可显示的视频，或未匹配到搜索结果。"
              : "还没有视频。先在「网盘管理」里配置好盘并触发扫描，或调整搜索词。"}
          </div>
        </div>
      ) : (
        <>
          <table className="admin-table is-selectable admin-videos-table">
            <thead>
              <tr>
                <th className="is-checkbox" style={{ width: "40px" }}>
                  <button
                    type="button"
                    className="admin-table-checkbox-btn"
                    onClick={toggleSelectAll}
                    aria-label={
                      selectedIds.size > 0 && selectedIds.size === listItems.length
                        ? "清空当前页选择"
                        : "选择当前页视频"
                    }
                  >
                    {selectedIds.size > 0 && selectedIds.size === listItems.length ? (
                      <CheckSquare size={16} />
                    ) : (
                      <Square size={16} />
                    )}
                  </button>
                </th>
                <th>标题</th>
                <th>作者</th>
                <th>时长</th>
                <th>预览视频</th>
                <th>来源</th>
                <th className="is-actions">操作</th>
              </tr>
            </thead>
            <tbody>
              {listItems.map((v) => {
                const isSelected = selectedIds.has(v.id);

                return (
                  <tr key={v.id} className={isSelected ? "is-selected" : ""}>
                    <td className="is-checkbox">
                      <button
                        type="button"
                        className={`admin-table-checkbox-btn ${isSelected ? "is-selected" : ""}`}
                        onClick={() => toggleSelect(v.id)}
                        aria-label={`${isSelected ? "取消选择" : "选择"}视频 ${v.title}`}
                      >
                        {isSelected ? (
                          <CheckSquare size={16} color="var(--accent)" />
                        ) : (
                          <Square size={16} color="var(--border-strong)" />
                        )}
                      </button>
                    </td>
                    <td data-label="标题">
                      <VideoTitleCell video={v} />
                    </td>
                    <td data-label="作者">{v.author || <span className="admin-text-faint">—</span>}</td>
                    <td data-label="时长">{formatDur(v.durationSeconds)}</td>
                    <td data-label="预览视频">
                      <PreviewStatus s={isPreviewGenerating(v) ? REGEN_PREVIEW_STATUS : v.previewStatus} />
                    </td>
                    <td data-label="来源" className="admin-mono-cell">
                      {driveNameMap.get(v.driveId) ?? v.driveId}
                    </td>
                    <td className="is-actions" data-label="操作">
                      <button type="button" className="admin-btn" onClick={() => setEditing(v)} title="编辑视频">
                        <Edit size={13} />
                      </button>{" "}
                      <button
                        type="button"
                        className="admin-btn"
                        onClick={() => handleRegen(v)}
                        disabled={isPreviewGenerating(v)}
                        title={isPreviewGenerating(v) ? "预览视频正在生成" : "重生预览视频"}
                      >
                        <RefreshCw size={13} className={isPreviewGenerating(v) ? "admin-spin" : undefined} />
                      </button>{" "}
                      <button
                        type="button"
                        className="admin-btn is-danger"
                        onClick={() => {
                          setDeleteSource(false);
                          setDeleteTarget(v);
                        }}
                        title="删除视频"
                      >
                        <Trash2 size={13} />
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          <Pagination page={page} totalPages={totalPages} pageSize={pageSize} onPage={setPage} />
        </>
      )}

      {editing && (
        <EditVideoModal
          video={editing}
          availableTags={availableTags}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            refresh();
          }}
        />
      )}
      <ConfirmModal
        open={batchRegenOpen}
        title="批量重生预览视频"
        message={`确定要为当前页选中的 ${selectedIds.size} 个视频重新生成预览视频吗？`}
        confirmText="确认重生"
        loading={batchRegening}
        onCancel={() => {
          if (!batchRegening) setBatchRegenOpen(false);
        }}
        onConfirm={confirmBatchRegen}
      />
      <ConfirmModal
        open={deleteTarget !== null}
        title="删除视频"
        message={deleteTarget ? `确定要删除「${deleteTarget.title}」吗？` : ""}
        confirmText="删除视频"
        danger
        centerMessage
        modalClassName="admin-modal--delete-confirm"
        loading={deleting}
        onCancel={() => {
          if (!deleting) {
            setDeleteTarget(null);
            setDeleteSource(false);
          }
        }}
        onConfirm={confirmDeleteVideo}
      >
        <DeleteSourceOption checked={deleteSource} disabled={deleting} onChange={setDeleteSource} note="开启后会先删除源文件，失败则不会删除管理库记录。" />
      </ConfirmModal>
      <ConfirmModal
        open={batchDeleteOpen}
        title="批量删除视频"
        message={`确定要删除当前页选中的 ${selectedIds.size} 个视频吗？`}
        confirmText="批量删除"
        danger
        centerMessage
        modalClassName="admin-modal--delete-confirm"
        loading={batchDeleting}
        onCancel={() => {
          if (!batchDeleting) {
            setBatchDeleteOpen(false);
            setBatchDeleteSource(false);
          }
        }}
        onConfirm={confirmBatchDelete}
      >
        <DeleteSourceOption checked={batchDeleteSource} disabled={batchDeleting} onChange={setBatchDeleteSource} note="开启后会先删除源文件，失败的视频会保留管理库记录。" />
      </ConfirmModal>
    </div>
  );
}

// ---------- 拉黑视频 ----------

function BlacklistTab({ onStatsChanged }: { onStatsChanged: () => void }) {
  const [list, setList] = useState<api.AdminDeletedVideo[]>([]);
  const [drives, setDrives] = useState<api.AdminDrive[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [keyword, setKeyword] = useState("");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [driveId, setDriveId] = useState("");
  const [page, setPage] = useState(1);
  const [total, setTotal] = useState(0);
  const [removeTarget, setRemoveTarget] = useState<api.AdminDeletedVideo | null>(null);
  const [removing, setRemoving] = useState(false);
  const pageSize = useVideosPageSize();
  const { show } = useToast();

  async function refresh() {
    setLoading(true);
    setLoadError("");
    try {
      const [r, driveList] = await Promise.all([
        api.listBlacklist({ driveId, page, size: pageSize, keyword: searchKeyword }),
        api.listDrives(),
      ]);
      setList(r.items ?? []);
      setTotal(r.total ?? 0);
      setDrives(driveList ?? []);
    } catch (e) {
      const message = e instanceof Error ? e.message : "加载失败";
      setLoadError(message);
      show(message, "error");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    refresh();
  }, [driveId, page, searchKeyword, pageSize]);

  useEffect(() => {
    setPage(1);
  }, [pageSize]);

  useEffect(() => {
    if (keyword === searchKeyword) return;
    const timer = window.setTimeout(() => {
      setSearchKeyword(keyword);
      setPage(1);
    }, 300);
    return () => window.clearTimeout(timer);
  }, [keyword]);

  const driveNameMap = new Map(drives.map((d) => [d.id, d.name || d.id]));
  const totalPages = Math.max(1, Math.ceil(total / pageSize));

  async function confirmRemove() {
    if (!removeTarget) return;
    const target = removeTarget;
    setRemoving(true);
    try {
      await api.removeBlacklist(target.id);
      setRemoveTarget(null);
      show("已移出黑名单，下次扫盘会重新入库", "success");
      onStatsChanged();
      if (list.length === 1 && page > 1) {
        setPage((p) => Math.max(1, p - 1));
      } else {
        refresh();
      }
    } catch (e) {
      show(e instanceof Error ? e.message : "操作失败", "error");
    } finally {
      setRemoving(false);
    }
  }

  function handleSearchSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSearchKeyword(keyword);
    setPage(1);
  }

  return (
    <>
      <div className="admin-page__actions admin-videos-filter admin-videos-filter--blacklist">
        <DriveFilter drives={drives} driveId={driveId} onChange={(id) => { setDriveId(id); setPage(1); }} />
        <SearchBox keyword={keyword} onChange={setKeyword} onSubmit={handleSearchSubmit} placeholder="搜索文件名" />
        <button type="button" className="admin-btn admin-videos-filter__refresh" onClick={refresh} aria-label="刷新拉黑视频">
          <RefreshCw size={13} />
          <span className="admin-videos-filter__refresh-text">刷新</span>
        </button>
      </div>

      {loading ? (
        <LoadingState />
      ) : loadError ? (
        <ErrorState message={loadError} onRetry={refresh} />
      ) : list.length === 0 ? (
        <div className="admin-empty-state">
          <div className="admin-empty-state__icon">
            <Ban size={48} />
          </div>
          <div className="admin-empty-state__text">黑名单为空。</div>
        </div>
      ) : (
        <>
          <div className="admin-videos-list-toolbar">
            <div className="admin-videos-summary">共 {total} 个拉黑视频</div>
          </div>
          <table className="admin-table admin-blacklist-table">
            <thead>
              <tr>
                <th>文件名</th>
                <th>来源</th>
                <th>大小</th>
                <th>拉黑时间</th>
                <th className="is-actions">操作</th>
              </tr>
            </thead>
            <tbody>
              {list.map((v) => (
                <tr key={v.id}>
                  <td data-label="文件名">
                    <div className="admin-blacklist-filecell">
                      <span className="admin-blacklist-filename">{v.fileName || <span className="admin-text-faint">（无文件名）</span>}</span>
                      {v.reason === "duplicate" && <span className="admin-blacklist-reason-pill">重复文件</span>}
                    </div>
                  </td>
                  <td data-label="来源" className="admin-mono-cell">
                    {driveNameMap.get(v.driveId) ?? v.driveId}
                  </td>
                  <td data-label="大小">{v.size > 0 ? formatBytes(v.size) : <span className="admin-text-faint">—</span>}</td>
                  <td data-label="拉黑时间">{formatDateTime(v.deletedAt)}</td>
                  <td className="is-actions" data-label="操作">
                    <button
                      type="button"
                      className="admin-btn admin-blacklist-restore-btn"
                      onClick={() => setRemoveTarget(v)}
                      title="移出黑名单"
                    >
                      <RotateCcw size={13} /> 移出黑名单
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <Pagination page={page} totalPages={totalPages} pageSize={pageSize} onPage={setPage} />
        </>
      )}

      <ConfirmModal
        open={removeTarget !== null}
        title="移出黑名单"
        message={
          removeTarget
            ? `确定把「${removeTarget.fileName || removeTarget.id}」移出黑名单吗？移出后它会在下次扫盘时被重新发现并入库。`
            : ""
        }
        confirmText="移出黑名单"
        centerMessage
        loading={removing}
        onCancel={() => {
          if (!removing) setRemoveTarget(null);
        }}
        onConfirm={confirmRemove}
      />
    </>
  );
}

// ---------- 共享小组件 ----------

function DriveFilter({
  drives,
  driveId,
  onChange,
}: {
  drives: api.AdminDrive[];
  driveId: string;
  onChange: (id: string) => void;
}) {
  return (
    <div className="admin-videos-filter__select-wrap">
      <select
        className="admin-videos-filter__select"
        value={driveId}
        onChange={(e) => onChange(e.target.value)}
      >
        <option value="">全部网盘</option>
        {drives.map((d) => (
          <option key={d.id} value={d.id}>
            {d.name || d.id}
          </option>
        ))}
      </select>
      <ChevronDown size={15} className="admin-videos-filter__select-icon" aria-hidden="true" />
    </div>
  );
}

function SearchBox({
  keyword,
  onChange,
  onSubmit,
  placeholder = "搜索标题 / 作者",
}: {
  keyword: string;
  onChange: (v: string) => void;
  onSubmit: (e: React.FormEvent) => void;
  placeholder?: string;
}) {
  return (
    <form className="admin-videos-filter__search" onSubmit={onSubmit}>
      <Search size={14} className="admin-videos-filter__search-icon" />
      <input
        aria-label={placeholder}
        value={keyword}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
      />
    </form>
  );
}

function Pagination({
  page,
  totalPages,
  pageSize,
  onPage,
}: {
  page: number;
  totalPages: number;
  pageSize: number;
  onPage: React.Dispatch<React.SetStateAction<number>>;
}) {
  return (
    <div className="admin-table-pagination">
      <button type="button" className="admin-btn" onClick={() => onPage(() => 1)} disabled={page <= 1}>
        首页
      </button>
      <button type="button" className="admin-btn" onClick={() => onPage((p) => Math.max(1, p - 1))} disabled={page <= 1}>
        上一页
      </button>
      <span className="admin-table-pagination__info">
        第 {page} / {totalPages} 页，每页 {pageSize} 个
      </span>
      <button
        type="button"
        className="admin-btn"
        onClick={() => onPage((p) => Math.min(totalPages, p + 1))}
        disabled={page >= totalPages}
      >
        下一页
      </button>
      <button type="button" className="admin-btn" onClick={() => onPage(() => totalPages)} disabled={page >= totalPages}>
        末页
      </button>
    </div>
  );
}

function LoadingState() {
  return (
    <div className="admin-loading-state">
      <RefreshCw size={20} className="admin-spin" />
      <span>加载中...</span>
    </div>
  );
}

function ErrorState({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className="admin-error-state">
      <strong>加载失败</strong>
      <span>{message}</span>
      <button type="button" className="admin-btn" onClick={onRetry}>
        <RefreshCw size={13} /> 重试
      </button>
    </div>
  );
}

function DeleteSourceOption({
  checked,
  disabled,
  onChange,
  note,
}: {
  checked: boolean;
  disabled: boolean;
  onChange: (v: boolean) => void;
  note: string;
}) {
  return (
    <label className="admin-delete-source-option">
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span>
        <strong>同时删除网盘中的源文件</strong>
        <small>{note}</small>
      </span>
    </label>
  );
}

function VideoTitleCell({ video: v }: { video: api.AdminVideo }) {
  return (
    <div className="admin-video-title-cell">
      <div className="admin-video-thumb-wrap" aria-hidden="true">
        {v.thumbnailUrl ? (
          <img className="admin-video-thumb" src={v.thumbnailUrl} alt="" loading="lazy" decoding="async" />
        ) : (
          <div className="admin-video-thumb-placeholder">
            <Image size={14} />
          </div>
        )}
      </div>
      <div className="admin-video-title-body">
        <div className="admin-video-title" title={v.title}>{v.title}</div>
        {fileMeta(v) && <div className="admin-video-filemeta">{fileMeta(v)}</div>}
        {(v.tags ?? []).length > 0 && (
          <div className="admin-pills admin-video-title-tags">
            {(v.tags ?? []).map((t) => (
              <span key={t} className="admin-pill">
                {t}
              </span>
            ))}
          </div>
        )}
        <VideoFileMetaPills video={v} />
      </div>
    </div>
  );
}

function PreviewStatus({ s }: { s: string }) {
  if (s === REGEN_PREVIEW_STATUS) return <span className="admin-status is-generating">生成中</span>;
  if (s === "ready") return <span className="admin-status is-ok">就绪</span>;
  if (s === "failed") return <span className="admin-status is-error">失败</span>;
  if (s === "disabled") return <span className="admin-status">已关闭</span>;
  if (s === "skipped") return <span className="admin-status">跳过</span>;
  return <span className="admin-status is-pending">待生成</span>;
}

function VideoFileMetaPills({ video }: { video: api.AdminVideo }) {
  const parts = fileMetaParts(video);
  if (parts.length === 0) return null;

  return (
    <div className="admin-video-filemeta-pills" aria-label="视频文件信息">
      {parts.map((part, index) => (
        <span key={`${part}-${index}`} className="admin-video-filemeta-pill">
          {part}
        </span>
      ))}
    </div>
  );
}

function formatDur(sec: number): string {
  if (!sec) return "—";
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m.toString().padStart(2, "0")}:${s.toString().padStart(2, "0")}`;
}

function formatDateTime(ms: number): string {
  if (!ms) return "—";
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return "—";
  const pad = (n: number) => n.toString().padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function videoUpdatedAtMs(video?: api.AdminVideo): number {
  if (!video?.updatedAt) return 0;
  const value = Date.parse(video.updatedAt);
  return Number.isFinite(value) ? value : 0;
}

function useVideosPageSize() {
  const [pageSize, setPageSize] = useState(() =>
    window.matchMedia(VIDEOS_MOBILE_QUERY).matches ? MOBILE_VIDEOS_PAGE_SIZE : DESKTOP_VIDEOS_PAGE_SIZE
  );

  useEffect(() => {
    const media = window.matchMedia(VIDEOS_MOBILE_QUERY);
    const update = () => {
      setPageSize(media.matches ? MOBILE_VIDEOS_PAGE_SIZE : DESKTOP_VIDEOS_PAGE_SIZE);
    };
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);

  return pageSize;
}

function EditVideoModal({
  video,
  availableTags,
  onClose,
  onSaved,
}: {
  video: api.AdminVideo;
  availableTags: api.AdminTag[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const idPrefix = useId();
  const [title, setTitle] = useState(video.title);
  const [author, setAuthor] = useState(video.author ?? "");
  const [selectedTags, setSelectedTags] = useState(video.tags ?? []);
  const [description, setDescription] = useState(video.description ?? "");
  const [durationSec, setDurationSec] = useState(String(video.durationSeconds || 0));
  const [saving, setSaving] = useState(false);
  const { show } = useToast();

  async function handleSave() {
    setSaving(true);
    try {
      await api.updateVideo(video.id, {
        title: title.trim(),
        author: author.trim(),
        tags: selectedTags,
        description,
        durationSeconds: Number(durationSec) || 0,
      });
      show("已保存", "success");
      onSaved();
    } catch (e) {
      show(e instanceof Error ? e.message : "保存失败", "error");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      open
      ariaLabel="编辑视频"
      onClose={onClose}
      footer={
        <>
          <button type="button" className="admin-btn" onClick={onClose}>
            取消
          </button>
          <button type="button" className="admin-btn is-primary" onClick={handleSave} disabled={saving}>
            {saving ? "保存中..." : "保存"}
          </button>
        </>
      }
    >
      <div className="admin-form">
        <div className="admin-form__row">
          <label htmlFor={`${idPrefix}-video-title`}>标题</label>
          <input id={`${idPrefix}-video-title`} value={title} onChange={(e) => setTitle(e.target.value)} />
        </div>
        <div className="admin-form__row">
          <label htmlFor={`${idPrefix}-video-author`}>作者</label>
          <input id={`${idPrefix}-video-author`} value={author} onChange={(e) => setAuthor(e.target.value)} />
        </div>
        <div className="admin-form__row">
          <div className="admin-form__label">标签</div>
          <div className="admin-tag-picker admin-video-tag-picker">
            {availableTags.map((tag) => (
              <label key={tag.id} className="admin-check admin-video-tag-option">
                <input
                  type="checkbox"
                  checked={selectedTags.includes(tag.label)}
                  onChange={() => setSelectedTags(toggleTag(selectedTags, tag.label))}
                />
                <span className="admin-video-tag-option__label" title={tag.label}>{tag.label}</span>
                <em className="admin-video-tag-option__count">{tag.count}</em>
              </label>
            ))}
          </div>
        </div>
        <div className="admin-form__row">
          <label htmlFor={`${idPrefix}-video-duration`}>时长（秒）</label>
          <input
            id={`${idPrefix}-video-duration`}
            value={durationSec}
            onChange={(e) => setDurationSec(e.target.value)}
            inputMode="numeric"
          />
        </div>
        <div className="admin-form__row">
          <label htmlFor={`${idPrefix}-video-description`}>描述</label>
          <textarea
            id={`${idPrefix}-video-description`}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </div>
        <dl className="admin-kv" style={{ marginTop: 8 }}>
          <dt>来源盘</dt>
          <dd>{video.driveId}</dd>
          <dt>文件信息</dt>
          <dd>{fileMeta(video) || "—"}</dd>
          <dt>预览视频</dt>
          <dd>
            <PreviewStatus s={video.previewStatus} />
          </dd>
        </dl>
        <details className="admin-form__help" style={{ marginTop: 8 }}>
          <summary>技术信息（排查用）</summary>
          <dl className="admin-kv" style={{ marginTop: 8 }}>
            <dt>内部视频 ID</dt>
            <dd style={{ fontFamily: "ui-monospace", fontSize: 12 }}>{video.id}</dd>
            <dt>网盘文件 ID</dt>
            <dd style={{ fontFamily: "ui-monospace", fontSize: 12 }}>{video.fileId}</dd>
          </dl>
        </details>
      </div>
    </Modal>
  );
}

function fileMeta(v: api.AdminVideo): string {
  return fileMetaParts(v).join(" · ");
}

function fileMetaParts(v: api.AdminVideo): string[] {
  return [normalizeExt(v.ext), v.quality, v.size > 0 ? formatBytes(v.size) : ""].filter(Boolean);
}

function normalizeExt(ext: string): string {
  const value = (ext ?? "").replace(/^\./, "").trim();
  return value ? value.toUpperCase() : "";
}

function toggleTag(tags: string[], label: string): string[] {
  return tags.includes(label) ? tags.filter((tag) => tag !== label) : [...tags, label];
}
