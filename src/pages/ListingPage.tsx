import { useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { AppShell } from "@/components/AppShell";
import { PromoStrip } from "@/components/PromoStrip";
import { SearchPanel } from "@/components/SearchPanel";
import { TagCloud } from "@/components/TagCloud";
import { SectionHeader } from "@/components/SectionHeader";
import { SortToolbar, type ViewMode } from "@/components/SortToolbar";
import { VideoGrid } from "@/components/VideoGrid";
import { Pagination } from "@/components/Pagination";
import { fetchListing } from "@/data/videos";
import type { SortKey, VideoItem } from "@/types";

const PAGE_SIZE_DEFAULT = 24;
const PAGE_SIZE_TAG = 12;
const LISTING_STATE_PREFIX = "video-site:list-state:";

type ListingState = {
  sort: SortKey;
  view: ViewMode;
  page: number;
  scrollY: number;
};

export default function ListingPage() {
  const [params] = useSearchParams();
  const keyword = params.get("q") ?? "";
  const tag = params.get("tag") ?? "";
  const listKey = useMemo(
    () => listingStateKey({ keyword, tag }),
    [keyword, tag]
  );
  const initialState = useMemo(() => readListingState(listKey), [listKey]);
  const activeListKeyRef = useRef(listKey);
  const hasLoadedListingRef = useRef(false);
  const pendingScrollYRef = useRef<number | null>(
    initialState ? initialState.scrollY : null
  );

  const [sort, setSort] = useState<SortKey>(initialState?.sort ?? "latest");
  const [view, setView] = useState<ViewMode>(initialState?.view ?? "grid");
  const [page, setPage] = useState(initialState?.page ?? 1);
  const [initialLoading, setInitialLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [items, setItems] = useState<VideoItem[]>([]);
  const [total, setTotal] = useState(0);
  const isFetching = initialLoading || refreshing;

  useEffect(() => {
    if (activeListKeyRef.current === listKey) return;
    activeListKeyRef.current = listKey;
    const saved = readListingState(listKey);
    setSort(saved?.sort ?? "latest");
    setView(saved?.view ?? "grid");
    setPage(saved?.page ?? 1);
    pendingScrollYRef.current = saved ? saved.scrollY : 0;
  }, [listKey]);

  useEffect(() => {
    document.title = keyword
      ? `搜索 "${keyword}" · 91`
      : tag
      ? `标签 ${tag} · 91`
      : "视频列表 · 91";

    let active = true;
    const isInitialLoad = !hasLoadedListingRef.current;
    if (isInitialLoad) {
      setInitialLoading(true);
    } else {
      setRefreshing(true);
    }
    fetchListing(page, tag ? PAGE_SIZE_TAG : PAGE_SIZE_DEFAULT, { q: keyword, tag, sort }).then((r) => {
      if (!active) return;
      setItems(r.items ?? []);
      setTotal(r.total ?? 0);
      hasLoadedListingRef.current = true;
      setInitialLoading(false);
      setRefreshing(false);
    });
    return () => {
      active = false;
    };
  }, [keyword, tag, sort, page]);

  useEffect(() => {
    const previous = window.history.scrollRestoration;
    window.history.scrollRestoration = "manual";
    return () => {
      window.history.scrollRestoration = previous;
    };
  }, []);

  useEffect(() => {
    let frame = 0;
    const save = () => {
      writeListingState(listKey, { sort, view, page, scrollY: window.scrollY });
    };
    const saveOnScroll = () => {
      if (frame) return;
      frame = window.requestAnimationFrame(() => {
        frame = 0;
        save();
      });
    };

    window.addEventListener("scroll", saveOnScroll, { passive: true });
    window.addEventListener("pagehide", save);
    save();
    return () => {
      if (frame) window.cancelAnimationFrame(frame);
      window.removeEventListener("scroll", saveOnScroll);
      window.removeEventListener("pagehide", save);
      save();
    };
  }, [listKey, sort, view, page]);

  useEffect(() => {
    if (isFetching) return;
    const scrollY = pendingScrollYRef.current;
    if (scrollY === null) return;
    pendingScrollYRef.current = null;
    window.requestAnimationFrame(() => {
      window.requestAnimationFrame(() => {
        window.scrollTo({ top: scrollY, behavior: "auto" });
      });
    });
  }, [isFetching, items.length, listKey]);

  const title = keyword
    ? `搜索结果：${keyword}`
    : tag
    ? `标签：${tag}`
    : "全部视频";

  return (
    <AppShell>
      <div className="container page-section">
        <PromoStrip />
        <SearchPanel />
        <TagCloud />
      </div>

      <div className="container page-section">
        <SectionHeader title={title} extra={`共 ${total} 个视频`} />
        <SortToolbar
          sort={sort}
          view={view}
          onSortChange={(nextSort) => {
            pendingScrollYRef.current = 0;
            setSort(nextSort);
            setPage(1);
            window.scrollTo({ top: 0, behavior: "smooth" });
          }}
          onViewChange={(nextView) => {
            setView(nextView);
          }}
        />
        <VideoGrid
          videos={items}
          loading={initialLoading}
          compact={view === "compact"}
          skeletonCount={12}
          emptyText="没有找到匹配的视频"
        />
        <Pagination
          page={page}
          pageSize={tag ? PAGE_SIZE_TAG : PAGE_SIZE_DEFAULT}
          total={total}
          onChange={(p) => {
            pendingScrollYRef.current = 0;
            setPage(p);
            window.scrollTo({ top: 0, behavior: "smooth" });
          }}
        />
      </div>
    </AppShell>
  );
}

function listingStateKey(filters: {
  keyword: string;
  tag: string;
}): string {
  const params = new URLSearchParams();
  if (filters.keyword) params.set("q", filters.keyword);
  if (filters.tag) params.set("tag", filters.tag);
  return `${LISTING_STATE_PREFIX}${params.toString()}`;
}

function readListingState(key: string): ListingState | null {
  try {
    const raw = window.sessionStorage.getItem(key);
    if (!raw) return null;
    const value = JSON.parse(raw) as Partial<ListingState>;
    return {
      sort: isSortKey(value.sort) ? value.sort : "latest",
      view: value.view === "compact" ? "compact" : "grid",
      page: typeof value.page === "number" && value.page > 0 ? value.page : 1,
      scrollY:
        typeof value.scrollY === "number" && value.scrollY > 0
          ? value.scrollY
          : 0,
    };
  } catch {
    return null;
  }
}

function writeListingState(key: string, state: ListingState) {
  try {
    window.sessionStorage.setItem(key, JSON.stringify(state));
  } catch {
    // Storage can be unavailable in private browsing modes.
  }
}

function isSortKey(value: unknown): value is SortKey {
  return (
    value === "latest" ||
    value === "hot" ||
    value === "recent"
  );
}
