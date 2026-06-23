import type { VideoDetail, VideoItem } from "@/types";

// 真实后端接口调用。未配置网盘时，各接口返回空数据。
export function fetchHomeVideos(excludeIds?: string[]): Promise<VideoItem[]> {
  const qs = new URLSearchParams();
  for (const id of excludeIds ?? []) {
    if (id.trim()) qs.append("exclude", id.trim());
  }
  const suffix = qs.toString() ? `?${qs.toString()}` : "";
  return apiGet<VideoItem[]>(`/api/home${suffix}`).catch(() => []);
}

export function fetchListing(
  page: number,
  pageSize: number,
  params?: { q?: string; tag?: string; sort?: string; includeTotal?: boolean }
): Promise<{ items: VideoItem[]; total: number }> {
  const qs = new URLSearchParams({
    page: String(page),
    size: String(pageSize),
  });
  if (params?.q) qs.set("q", params.q);
  if (params?.tag) qs.set("tag", params.tag);
  if (params?.sort) qs.set("sort", params.sort);
  if (params?.includeTotal === false) qs.set("count", "false");
  return apiGet<{ items: VideoItem[]; total: number }>(
    `/api/list?${qs.toString()}`
  ).catch(() => ({ items: [], total: 0 }));
}

export function fetchVideoDetail(id: string): Promise<VideoDetail | null> {
  return apiGet<VideoDetail>(`/api/video/${encodeURIComponent(id)}`).catch(
    () => null
  );
}

export function updateVideoTags(
  id: string,
  tags: string[]
): Promise<VideoItem> {
  return apiJSON<VideoItem>(`/api/video/${encodeURIComponent(id)}/tags`, {
    method: "PUT",
    body: JSON.stringify({ tags }),
  });
}

export function hideVideo(id: string): Promise<{ ok: boolean }> {
  return apiJSON<{ ok: boolean }>(
    `/api/video/${encodeURIComponent(id)}/hide`,
    { method: "POST" }
  );
}

export function deleteVideo(
  id: string,
  options: { deleteSource?: boolean } = {}
): Promise<{ ok: boolean; deletedSource: boolean }> {
  return apiJSON<{ ok: boolean; deletedSource: boolean }>(
    `/admin/api/videos/${encodeURIComponent(id)}`,
    {
      method: "DELETE",
      body: JSON.stringify({ deleteSource: !!options.deleteSource }),
    }
  );
}

export function recordView(id: string): Promise<{ views: number }> {
  return apiJSON<{ views: number }>(
    `/api/video/${encodeURIComponent(id)}/view`,
    { method: "POST" }
  );
}

export type UploadVideoInput = {
  file: File;
  title: string;
  tags: string[];
};

export function uploadVideo(input: UploadVideoInput): Promise<VideoItem> {
  const body = new FormData();
  body.append("file", input.file);
  if (input.title.trim()) {
    body.append("title", input.title.trim());
  }
  for (const tag of input.tags) {
    body.append("tags", tag);
  }
  return apiForm<VideoItem>("/api/upload", body);
}

export type TagItem = { id: string; label: string; count?: number };

const TAG_CACHE_TTL_MS = 30_000;
let cachedTags: TagItem[] | null = null;
let cachedTagsAt = 0;
let pendingTags: Promise<TagItem[]> | null = null;

export function fetchTags(): Promise<TagItem[]> {
  const now = Date.now();
  if (cachedTags && now - cachedTagsAt < TAG_CACHE_TTL_MS) {
    return Promise.resolve(cachedTags);
  }
  if (pendingTags) return pendingTags;
  pendingTags = apiGet<TagItem[]>("/api/tags")
    .then((tags) => {
      cachedTags = tags;
      cachedTagsAt = Date.now();
      return tags;
    })
    .catch(() => cachedTags ?? [])
    .finally(() => {
      pendingTags = null;
    });
  return pendingTags;
}

/** 短视频模式单条记录。比 VideoItem 多 videoSrc / poster。 */
export type ShortsItem = VideoItem & {
  videoSrc: string;
  poster: string;
};

/** 短视频"取下一批"接口的响应。 */
export type ShortsNextResponse = {
  items: ShortsItem[];
  total: number;
  /** true 表示这批返回少于 count，前端播放完毕后应清空 seenIds 开新一轮 */
  roundComplete: boolean;
};

/**
 * 拉取短视频流的下一批候选。把当前轮已看过的 video id 列表传给后端，
 * 服务器从未在列表中的视频里随机抽 count 条返回。
 *
 * 失败时返回空批 + roundComplete=false，由调用方决定是否重试。
 */
export function fetchShortsNext(
  seenIds: string[],
  count: number
): Promise<ShortsNextResponse> {
  return apiJSON<ShortsNextResponse>("/api/shorts/next", {
    method: "POST",
    body: JSON.stringify({ seenIds, count }),
  }).catch(() => ({ items: [], total: 0, roundComplete: false }));
}

async function apiGet<T>(path: string): Promise<T> {
  const res = await fetch(path, { credentials: "include" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

async function apiJSON<T>(path: string, init: RequestInit): Promise<T> {
  const res = await fetch(path, {
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    ...init,
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

async function apiForm<T>(path: string, body: FormData): Promise<T> {
  const res = await fetch(path, {
    method: "POST",
    credentials: "include",
    body,
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}
