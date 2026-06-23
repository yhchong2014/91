export const VIDEO_RETURN_PATH_STORAGE_KEY = "video-site:video-return-path";

type RouteLike = {
  pathname: string;
  search?: string;
  hash?: string;
};

export function routeToPath(route: RouteLike): string {
  return `${route.pathname}${route.search ?? ""}${route.hash ?? ""}`;
}

export function normalizeVideoReturnPath(path: string, origin = browserOrigin()): string | null {
  const raw = path.trim();
  if (!raw) return null;

  let url: URL;
  try {
    url = new URL(raw, origin ?? "http://localhost");
  } catch {
    return null;
  }

  if (origin && url.origin !== origin) return null;
  if (!url.pathname.startsWith("/")) return null;
  if (url.pathname === "/login") return null;
  if (url.pathname === "/video" || url.pathname.startsWith("/video/")) return null;

  return `${url.pathname}${url.search}${url.hash}` || "/";
}

export function isVideoReturnPath(path: string, origin = browserOrigin()): boolean {
  return normalizeVideoReturnPath(path, origin) !== null;
}

export function rememberVideoReturnPath(path: string) {
  const normalized = normalizeVideoReturnPath(path);
  if (!normalized || typeof window === "undefined") return;

  try {
    window.sessionStorage.setItem(VIDEO_RETURN_PATH_STORAGE_KEY, normalized);
  } catch {
    // sessionStorage 不可用时退回默认首页，不影响播放和删除流程。
  }
}

export function readVideoReturnPath(): string | null {
  if (typeof window === "undefined") return null;

  try {
    const saved = window.sessionStorage.getItem(VIDEO_RETURN_PATH_STORAGE_KEY);
    return saved ? normalizeVideoReturnPath(saved) : null;
  } catch {
    return null;
  }
}

export function resolveVideoReturnPath(candidate?: string | null): string {
  if (candidate) {
    const normalized = normalizeVideoReturnPath(candidate);
    if (normalized) return normalized;
  }
  return readVideoReturnPath() ?? "/";
}

function browserOrigin(): string | undefined {
  return typeof window === "undefined" ? undefined : window.location.origin;
}
