import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import { AppShell } from "@/components/AppShell";
import { VideoPlayer } from "@/components/VideoPlayer";
import { VideoActions } from "@/components/VideoActions";
import { VideoMetaHeader } from "@/components/VideoMetaHeader";
import { VideoInfoPanel } from "@/components/VideoInfoPanel";
import { RecommendedRail } from "@/components/RecommendedRail";
import {
  deleteVideo,
  fetchTags,
  fetchVideoDetail,
  recordView,
  updateVideoTags,
} from "@/data/videos";
import { resolveVideoReturnPath } from "@/lib/videoReturnPath";
import type { TagItem, VideoDetail } from "@/types";

export default function VideoDetailPage() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const location = useLocation();
  const locationState = location.state as { from?: unknown } | null;
  const [detail, setDetail] = useState<VideoDetail | null>(null);
  const [tags, setTags] = useState<TagItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [tagSaving, setTagSaving] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleteSource, setDeleteSource] = useState(false);
  const [deleteSaving, setDeleteSaving] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const detailTopRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!id) return;
    let active = true;
    window.scrollTo({ top: 0, behavior: "auto" });
    setLoading(true);
    Promise.all([fetchVideoDetail(id), fetchTags()]).then(([d, tagList]) => {
      if (!active) return;
      setDetail(d);
      setTags(tagList);
      setLoading(false);
      document.title = d ? `${d.title} · 91` : "视频不存在";
    });
    return () => {
      active = false;
    };
  }, [id]);

  useLayoutEffect(() => {
    if (loading || !detail) return;
    window.requestAnimationFrame(() => {
      detailTopRef.current?.scrollIntoView({
        block: "start",
        behavior: "auto",
      });
    });
  }, [loading, detail?.id]);

  async function handleTagsChange(nextTags: string[]) {
    if (!detail) return;
    setTagSaving(true);
    try {
      const updated = await updateVideoTags(detail.id, nextTags);
      setDetail({ ...detail, tags: updated.tags ?? [] });
    } finally {
      setTagSaving(false);
    }
  }

  function handleOpenDelete() {
    if (!detail || deleteSaving) return;
    setDeleteSource(false);
    setDeleteError("");
    setDeleteOpen(true);
  }

  function handleCloseDelete() {
    if (deleteSaving) return;
    setDeleteOpen(false);
    setDeleteError("");
  }

  async function handleConfirmDelete() {
    if (!detail || deleteSaving) return;
    setDeleteSaving(true);
    setDeleteError("");
    try {
      await deleteVideo(detail.id, { deleteSource });
      const from = typeof locationState?.from === "string" ? locationState.from : null;
      navigate(resolveVideoReturnPath(from), { replace: true });
    } catch {
      setDeleteError(
        deleteSource
          ? "删除失败。源文件未能删除时，管理库记录会保留。"
          : "删除失败，请稍后重试。"
      );
      setDeleteSaving(false);
    }
  }

  function handleFirstPlay() {
    if (!detail) return;
    // 失败静默忽略，不打扰用户播放体验
    recordView(detail.id).catch(() => undefined);
  }

  if (loading) {
    return (
      <AppShell mobileAutoHideNav>
        <div className="vd-page">
          <div className="vd-ambient" aria-hidden="true" />
          <div className="container vd-page__inner">
            <div
              className="vd-layout vd-skeleton"
              aria-busy="true"
              aria-label="视频详情加载中"
            >
              <div className="vd-main">
                <div className="vd-skeleton__player" />

                <div className="vd-skeleton__summary">
                  <div className="vd-skeleton__chips">
                    <span className="vd-skeleton__chip vd-skeleton__chip--source" />
                    <span className="vd-skeleton__chip" />
                    <span className="vd-skeleton__chip vd-skeleton__chip--plain" />
                    <span className="vd-skeleton__chip vd-skeleton__chip--plain" />
                  </div>
                  <div className="vd-skeleton__title" />
                  <div className="vd-skeleton__actions">
                    <span />
                    <span />
                    <span />
                  </div>
                </div>

                <div className="vd-skeleton__info">
                  <span className="vd-skeleton__section-head" />
                  <span className="vd-skeleton__line" />
                  <span className="vd-skeleton__line vd-skeleton__line--short" />
                  <div className="vd-skeleton__tag-row">
                    <span />
                    <span />
                    <span />
                  </div>
                </div>
              </div>

              <aside className="vd-rail vd-skeleton__rail">
                <div className="vd-rail__head">
                  <span className="vd-rail__head-icon" aria-hidden="true">
                    <span />
                    <span />
                  </span>
                  <span className="vd-skeleton__rail-head" />
                </div>
                <ul className="vd-rail__list vd-skeleton__rail-list">
                  {Array.from({ length: 6 }).map((_, index) => (
                    <li key={index} className="vd-skeleton__rail-item">
                      <span className="vd-skeleton__rail-thumb" />
                      <span className="vd-skeleton__rail-body">
                        <span className="vd-skeleton__rail-title" />
                        <span className="vd-skeleton__rail-title vd-skeleton__rail-title--short" />
                        <span className="vd-skeleton__rail-meta" />
                      </span>
                    </li>
                  ))}
                </ul>
              </aside>
            </div>
          </div>
        </div>
      </AppShell>
    );
  }

  if (!detail) {
    return (
      <AppShell mobileAutoHideNav>
        <div className="vd-page">
          <div className="container vd-page__inner">
            <div className="vd-empty">视频不存在或已被移除</div>
          </div>
        </div>
      </AppShell>
    );
  }

  return (
    <AppShell mobileAutoHideNav>
      <div className="vd-page">
        {/* Ambient 背景层：用海报作模糊底色，叠加渐变过渡到页面背景 */}
        <div
          className="vd-ambient"
          aria-hidden="true"
          style={{
            backgroundImage: detail.poster
              ? `url(${detail.poster})`
              : undefined,
          }}
        />

        <div className="container vd-page__inner">
          <div className="vd-layout">
            <div className="vd-main" ref={detailTopRef}>
              <div className="vd-player-wrap">
                <div className="vd-player">
                  <VideoPlayer
                    id={detail.id}
                    src={detail.videoSrc}
                    poster={detail.poster}
                    previewSrc={detail.previewSrc}
                    title={detail.title}
                    onFirstPlay={handleFirstPlay}
                  />
                </div>
              </div>

              <section className="vd-summary" aria-label="当前视频">
                <VideoMetaHeader video={detail} />

                <VideoActions
                  video={detail}
                  onDeleteVideo={handleOpenDelete}
                  deleteSaving={deleteSaving}
                />
              </section>

              <VideoInfoPanel
                video={detail}
                availableTags={tags}
                tagSaving={tagSaving}
                onTagsChange={handleTagsChange}
              />
            </div>

            <RecommendedRail videos={detail.relatedVideos} />
          </div>
        </div>
      </div>

      {deleteOpen && (
        <div className="vd-delete-modal" role="presentation">
          <div
            className="vd-delete-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="vd-delete-title"
          >
            <div className="vd-delete-head">
              <h2 id="vd-delete-title" className="vd-delete-title">
                删除视频
              </h2>
              <p className="vd-delete-text">
                确定删除「{detail.title}」吗？此操作会从管理库移除该视频。
              </p>
            </div>

            <label className="vd-delete-option">
              <input
                type="checkbox"
                checked={deleteSource}
                disabled={deleteSaving}
                onChange={(e) => setDeleteSource(e.target.checked)}
              />
              <span>
                <strong>同时删除网盘中的源文件</strong>
              </span>
            </label>

            {deleteError && <div className="vd-delete-error">{deleteError}</div>}

            <div className="vd-delete-actions">
              <button
                type="button"
                className="vd-delete-action vd-delete-cancel"
                onClick={handleCloseDelete}
                disabled={deleteSaving}
              >
                取消
              </button>
              <button
                type="button"
                className="vd-delete-action vd-delete-confirm"
                onClick={handleConfirmDelete}
                disabled={deleteSaving}
              >
                {deleteSaving ? "删除中..." : "删除"}
              </button>
            </div>
          </div>
        </div>
      )}
    </AppShell>
  );
}
