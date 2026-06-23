import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { Link, useLocation } from "react-router-dom";
import type { PreviewState, VideoItem } from "@/types";
import { formatCount } from "@/lib/format";
import { previewController } from "@/lib/previewController";
import {
  shouldInterceptPreviewTap,
  shouldStartInstantPreview,
} from "@/lib/previewIntent";
import { useInViewport } from "@/lib/useInViewport";
import { resolveVideoReturnPath, routeToPath } from "@/lib/videoReturnPath";
import { PreviewVideo } from "./PreviewVideo";

type Props = {
  videos: VideoItem[];
};

const HOVER_DELAY_MS = 300;

function useActivePreviewId(): string | null {
  return useSyncExternalStore(
    previewController.subscribe,
    previewController.getActiveId,
    () => null
  );
}

/**
 * 详情页右侧 / 移动端下方的"推荐视频"列表。
 *
 * 不直接复用 VideoCard：那个组件结构是上下两段（缩略图 + 标题/meta），而这里需要
 * 左右横排的紧凑布局，覆盖样式会很乱。本组件复用同一套预览相关基础设施
 * （previewController / previewIntent / useInViewport / PreviewVideo），
 * 行为与 VideoCard 一致：桌面 hover 300ms 后预览，手机首次点击播预览、再点跳详情。
 */
export function RecommendedRail({ videos }: Props) {
  if (!videos || videos.length === 0) return null;

  return (
    <aside className="vd-rail" aria-label="推荐视频">
      <header className="vd-rail__head">
        <span className="vd-rail__head-icon" aria-hidden="true">
          <span />
          <span />
        </span>
        <h2 className="vd-rail__head-title">推荐视频</h2>
      </header>
      <ul className="vd-rail__list">
        {videos.map((v) => (
          <RecommendedItem key={v.id} video={v} />
        ))}
      </ul>
    </aside>
  );
}

function RecommendedItem({ video }: { video: VideoItem }) {
  const [previewState, setPreviewState] = useState<PreviewState>("idle");
  const [shouldRenderPreview, setShouldRenderPreview] = useState(false);
  const [progress, setProgress] = useState(0);

  const rootRef = useRef<HTMLLIElement | null>(null);
  const hoverTimerRef = useRef<number | null>(null);
  const lastPointerTypeRef = useRef<string>("");
  const canHoverRef = useRef(true);
  const videoRef = useRef<HTMLVideoElement | null>(null);

  const activeId = useActivePreviewId();
  const inView = useInViewport(rootRef);
  const location = useLocation();
  const locationState = location.state as { from?: unknown } | null;
  const returnPath =
    typeof locationState?.from === "string"
      ? resolveVideoReturnPath(locationState.from)
      : resolveVideoReturnPath(routeToPath(location));

  // 全局预览换卡时立即清理
  useEffect(() => {
    if (activeId !== video.id && shouldRenderPreview) {
      cleanup();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeId, video.id]);

  // 离开视口立即停
  useEffect(() => {
    if (!inView && shouldRenderPreview) {
      cleanup();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inView]);

  // 卸载清理
  useEffect(() => {
    return () => {
      cleanup();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 检测当前设备是否支持 hover（鼠标 vs 触屏）
  useEffect(() => {
    const media = window.matchMedia("(hover: hover) and (pointer: fine)");
    const update = () => {
      canHoverRef.current = media.matches;
    };
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);

  function cleanup() {
    if (hoverTimerRef.current) {
      window.clearTimeout(hoverTimerRef.current);
      hoverTimerRef.current = null;
    }

    const el = videoRef.current;
    if (el) {
      try {
        el.pause();
        el.removeAttribute("src");
        el.load();
      } catch {
        // noop
      }
    }

    setShouldRenderPreview(false);
    setPreviewState("idle");
    setProgress(0);

    if (previewController.getActiveId() === video.id) {
      previewController.setActiveId(null);
    }
  }

  function startPreviewIntent() {
    if (!inView) return;
    if (hoverTimerRef.current) return;
    setPreviewState("intent");
    hoverTimerRef.current = window.setTimeout(() => {
      hoverTimerRef.current = null;
      startPreviewNow({ requireInView: true });
    }, HOVER_DELAY_MS);
  }

  function startPreviewNow(options: { requireInView: boolean }) {
    if (options.requireInView && !inView) return;
    if (hoverTimerRef.current) {
      window.clearTimeout(hoverTimerRef.current);
      hoverTimerRef.current = null;
    }
    previewController.setActiveId(video.id);
    setShouldRenderPreview(true);
    setPreviewState("loading");
  }

  function stopPreview() {
    cleanup();
  }

  function handlePointerEnter(event: React.PointerEvent<HTMLLIElement>) {
    lastPointerTypeRef.current = event.pointerType;
    if (shouldStartInstantPreview({ pointerType: event.pointerType })) return;
    startPreviewIntent();
  }

  function handlePointerLeave(event: React.PointerEvent<HTMLLIElement>) {
    if (shouldStartInstantPreview({ pointerType: event.pointerType })) return;
    stopPreview();
  }

  function handlePointerDown(event: React.PointerEvent<HTMLLIElement>) {
    lastPointerTypeRef.current = event.pointerType;
  }

  function handleClickCapture(event: React.MouseEvent<HTMLAnchorElement>) {
    const previewActive = activeId === video.id && shouldRenderPreview;
    if (
      !shouldInterceptPreviewTap({
        pointerType: lastPointerTypeRef.current,
        canHover: canHoverRef.current,
        previewActive,
      })
    ) {
      return;
    }
    event.preventDefault();
    event.stopPropagation();
    startPreviewNow({ requireInView: false });
  }

  return (
    <li
      ref={rootRef}
      className="vd-rail__item"
      onPointerEnter={handlePointerEnter}
      onPointerLeave={handlePointerLeave}
      onPointerDown={handlePointerDown}
      onFocus={startPreviewIntent}
      onBlur={stopPreview}
    >
      <Link
        to={video.href}
        state={{ from: returnPath }}
        className="vd-rail__link"
        onClickCapture={handleClickCapture}
      >
        <div className="vd-rail__thumb">
          <img src={video.thumbnail} alt={video.title} loading="lazy" />
          {shouldRenderPreview && (
            <PreviewVideo
              ref={videoRef}
              src={video.previewSrc}
              state={previewState}
              onCanPlay={() => setPreviewState("playing")}
              onError={() => setPreviewState("error")}
              onTimeUpdate={(p) => setProgress(p)}
            />
          )}
          {previewState === "loading" && (
            <span className="preview-loader" />
          )}
          {previewState === "error" && (
            <span className="preview-error">预览加载失败</span>
          )}
          {previewState === "playing" && (
            <div className="preview-progress" aria-hidden="true">
              <div
                className="preview-progress__bar"
                style={{ width: `${Math.min(100, progress * 100)}%` }}
              />
            </div>
          )}
          {video.duration && previewState !== "playing" && (
            <span className="vd-rail__duration">{video.duration}</span>
          )}
          {video.quality === "HD" && previewState !== "playing" && (
            <span className="vd-rail__hd">HD</span>
          )}
        </div>
        <div className="vd-rail__body">
          <h3 className="vd-rail__title" title={video.title}>
            {video.title}
          </h3>
          <div className="vd-rail__meta">
            {video.author && (
              <span className="vd-rail__author">{video.author}</span>
            )}
            <span>{formatCount(video.views)} 观看</span>
            {video.publishedAt && <span>{video.publishedAt}</span>}
          </div>
        </div>
      </Link>
    </li>
  );
}
