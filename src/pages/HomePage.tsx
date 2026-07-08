import { useCallback, useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { AppShell } from "@/components/AppShell";
import { PromoStrip } from "@/components/PromoStrip";
import { SearchPanel } from "@/components/SearchPanel";
import { TagCloud } from "@/components/TagCloud";
import { SectionHeader } from "@/components/SectionHeader";
import { SortToolbar, type ViewMode } from "@/components/SortToolbar";
import { VideoGrid } from "@/components/VideoGrid";
import { Pagination } from "@/components/Pagination";
import { AdminEmptyVisual } from "@/admin/AdminEmptyVisual";
import { fetchHomeVideos, fetchListing } from "@/data/videos";
import type { SortKey, VideoItem } from "@/types";

const DESKTOP_COUNT = 12;
const MOBILE_COUNT = 8;
const HOME_SEARCH_PAGE_SIZE = 24;
const LATEST_POOL_SIZE = 96;
const HOME_RECENT_KEY = "home.random.recentVideoIds";
const HOME_RECENT_LIMIT = 72;
const HOME_LATEST_CURSOR_KEY = "home.latest.cursor";

function useIsMobile() {
  const [mobile, setMobile] = useState(window.innerWidth <= 640);
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 640px)");
    const handler = () => setMobile(mq.matches);
    mq.addEventListener("change", handler);
    return () => mq.removeEventListener("change", handler);
  }, []);
  return mobile;
}

// 模块级缓存：SPA 生命周期内保持，刷新页面时重置
let cachedRanking: VideoItem[] | null = null;
let cachedLatestPool: VideoItem[] | null = null;
let cachedLatestBatch: VideoItem[] | null = null;

function loadRecentHomeVideoIds(): string[] {
  try {
    const raw = window.localStorage.getItem(HOME_RECENT_KEY);
    const parsed = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed)
      ? parsed.filter((id): id is string => typeof id === "string" && id.length > 0)
      : [];
  } catch {
    return [];
  }
}

function rememberHomeVideos(items: VideoItem[]) {
  const merged = [...items.map((item) => item.id), ...loadRecentHomeVideoIds()];
  const seen = new Set<string>();
  const recent: string[] = [];
  for (const id of merged) {
    if (!id || seen.has(id)) continue;
    seen.add(id);
    recent.push(id);
    if (recent.length >= HOME_RECENT_LIMIT) break;
  }
  try {
    window.localStorage.setItem(HOME_RECENT_KEY, JSON.stringify(recent));
  } catch {
    // localStorage 不可用时只影响连续刷新去重，不影响首页展示。
  }
}

function loadLatestCursor(poolLength: number): number {
  if (poolLength <= 0) return 0;
  try {
    const raw = window.localStorage.getItem(HOME_LATEST_CURSOR_KEY);
    const parsed = raw ? Number.parseInt(raw, 10) : 0;
    return Number.isFinite(parsed) && parsed >= 0 ? parsed % poolLength : 0;
  } catch {
    return 0;
  }
}

function saveLatestCursor(cursor: number) {
  try {
    window.localStorage.setItem(HOME_LATEST_CURSOR_KEY, String(cursor));
  } catch {
    // localStorage 不可用时只影响跨刷新循环进度，不影响展示。
  }
}

function nextLatestBatch(items: VideoItem[], count: number): VideoItem[] {
  if (items.length === 0 || count <= 0) return [];
  if (items.length <= count) {
    saveLatestCursor(0);
    return items;
  }

  const start = loadLatestCursor(items.length);
  const batch: VideoItem[] = [];
  for (let i = 0; i < count; i += 1) {
    batch.push(items[(start + i) % items.length]);
  }
  saveLatestCursor((start + count) % items.length);
  return batch;
}

function cacheNextLatestBatch(items: VideoItem[], count: number): VideoItem[] {
  const batch = nextLatestBatch(items, count);
  cachedLatestBatch = batch;
  return batch;
}

export default function HomePage() {
  const [rankingVideos, setRankingVideos] = useState<VideoItem[]>(cachedRanking ?? []);
  const [latestVideos, setLatestVideos] = useState<VideoItem[]>(cachedLatestBatch ?? []);
  const [rankingLoading, setRankingLoading] = useState(cachedRanking === null);
  const [latestLoading, setLatestLoading] = useState(cachedLatestBatch === null);
  const [refreshing, setRefreshing] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [searchPage, setSearchPage] = useState(1);
  const [searchItems, setSearchItems] = useState<VideoItem[]>([]);
  const [searchTotal, setSearchTotal] = useState(0);
  const [searchLoading, setSearchLoading] = useState(false);
  const [searchSort, setSearchSort] = useState<SortKey>("latest");
  const [searchView, setSearchView] = useState<ViewMode>("grid");
  const isMobile = useIsMobile();
  const activeSearchQuery = searchQuery.trim();

  const refreshHome = useCallback(async () => {
    setRefreshing(true);
    setRankingLoading(true);
    setLatestLoading(true);

    const excludeIds = loadRecentHomeVideoIds();
    const [rankingItems, latestResult] = await Promise.all([
      fetchHomeVideos(excludeIds),
      fetchListing(1, LATEST_POOL_SIZE, { sort: "latest", includeTotal: false }),
    ]);

    rememberHomeVideos(rankingItems);
    cachedRanking = rankingItems;
    cachedLatestPool = latestResult.items;
    const latestBatch = cacheNextLatestBatch(latestResult.items, DESKTOP_COUNT);
    setRankingVideos(rankingItems);
    setLatestVideos(latestBatch);
    setRankingLoading(false);
    setLatestLoading(false);
    setRefreshing(false);
  }, []);

  const handleSearch = useCallback((keyword: string) => {
    setSearchQuery(keyword);
    setSearchPage(1);
  }, []);

  useEffect(() => {
    document.title = activeSearchQuery ? `搜索 "${activeSearchQuery}" · 91` : "首页 · 91";
  }, [activeSearchQuery]);

  useEffect(() => {
    let active = true;

    if (cachedRanking === null) {
      setRankingLoading(true);
      const excludeIds = loadRecentHomeVideoIds();
      fetchHomeVideos(excludeIds)
        .then((rankingItems) => {
          if (!active) return;
          rememberHomeVideos(rankingItems);
          cachedRanking = rankingItems;
          setRankingVideos(rankingItems);
        })
        .finally(() => {
          if (active) setRankingLoading(false);
        });
    }

    if (cachedLatestPool === null) {
      setLatestLoading(true);
      fetchListing(1, LATEST_POOL_SIZE, { sort: "latest", includeTotal: false })
        .then((latestResult) => {
          if (!active) return;
          cachedLatestPool = latestResult.items;
          setLatestVideos(cacheNextLatestBatch(latestResult.items, DESKTOP_COUNT));
        })
        .finally(() => {
          if (active) setLatestLoading(false);
        });
    } else {
      setLatestVideos(cachedLatestBatch ?? cacheNextLatestBatch(cachedLatestPool, DESKTOP_COUNT));
      setLatestLoading(false);
    }

    return () => { active = false; };
  }, []);

  useEffect(() => {
    if (!activeSearchQuery) {
      setSearchItems([]);
      setSearchTotal(0);
      setSearchLoading(false);
      return;
    }

    let active = true;
    setSearchLoading(true);
    fetchListing(searchPage, HOME_SEARCH_PAGE_SIZE, {
      q: activeSearchQuery,
      sort: searchSort,
    })
      .then((result) => {
        if (!active) return;
        setSearchItems(result.items ?? []);
        setSearchTotal(result.total ?? 0);
      })
      .finally(() => {
        if (active) setSearchLoading(false);
      });
    return () => {
      active = false;
    };
  }, [activeSearchQuery, searchPage, searchSort]);

  const displayCount = isMobile ? MOBILE_COUNT : DESKTOP_COUNT;
  const ranking = rankingVideos.slice(0, displayCount);
  const latest = latestVideos.slice(0, displayCount);
  const homeLoading = rankingLoading || latestLoading;
  const hasActiveSearch = activeSearchQuery.length > 0;
  const searchTotalPages = Math.max(1, Math.ceil(searchTotal / HOME_SEARCH_PAGE_SIZE));
  const hasAnyVideos = ranking.length > 0 || latest.length > 0;
  const showEmptyHome = !homeLoading && !hasAnyVideos;

  return (
    <AppShell mobileAutoHideNav>
      <div className="container page-section home-discovery-section">
        <PromoStrip />
        <SearchPanel value={searchQuery} onSearch={handleSearch} />
        {!hasActiveSearch && (
          hasAnyVideos ? (
            <TagCloud />
          ) : (
            <div className="tag-cloud-container is-reserved" aria-hidden="true" />
          )
        )}
      </div>

      {hasActiveSearch ? (
        <div className="container page-section home-primary-section">
          <SortToolbar
            sort={searchSort}
            view={searchView}
            onSortChange={(nextSort) => {
              setSearchSort(nextSort);
              setSearchPage(1);
              window.scrollTo({ top: 0, behavior: "smooth" });
            }}
            onViewChange={setSearchView}
          />
          {searchLoading ? (
            <VideoGrid videos={searchItems} loading compact={searchView === "compact"} skeletonCount={12} />
          ) : searchItems.length === 0 ? (
            <AdminEmptyVisual
              variant="no-results"
              text="未查询到"
              className="admin-empty-state admin-empty-state--plain home-empty-state"
            />
          ) : (
            <VideoGrid videos={searchItems} compact={searchView === "compact"} skeletonCount={12} />
          )}
          {!searchLoading && searchTotalPages > 1 && (
            <Pagination
              page={searchPage}
              pageSize={HOME_SEARCH_PAGE_SIZE}
              total={searchTotal}
              onChange={(p) => {
                setSearchPage(p);
                window.scrollTo({ top: 0, behavior: "smooth" });
              }}
            />
          )}
        </div>
      ) : showEmptyHome ? (
        <div className="container page-section home-primary-section">
          <AdminEmptyVisual
            variant="empty"
            text="当前库中没有视频"
            className="admin-empty-state admin-empty-state--plain home-empty-state"
          />
        </div>
      ) : (
        <>
          <div className="container page-section home-primary-section">
            <SectionHeader title="随机推荐" />
            <VideoGrid
              videos={ranking}
              loading={rankingLoading}
              priorityCount={Math.min(4, displayCount)}
              skeletonCount={displayCount}
            />
          </div>

          <div className="container page-section">
            <SectionHeader title="最新视频" />
            <VideoGrid videos={latest} loading={latestLoading} skeletonCount={displayCount} />
          </div>
        </>
      )}

      {!hasActiveSearch && (
        <button
          type="button"
          className={`home-refresh ${refreshing ? "is-refreshing" : ""}`}
          onClick={refreshHome}
          disabled={refreshing}
          aria-label="刷新首页"
          title="刷新首页"
        >
          <RefreshCw size={18} />
        </button>
      )}
    </AppShell>
  );
}
