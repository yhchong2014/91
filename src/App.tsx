import { Suspense, lazy, useEffect, type ReactNode } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { SkyStarfield } from "@/components/SkyStarfield";
import { AdminLayout } from "@/admin/AdminLayout";
import { RequireAuth } from "@/admin/RequireAuth";
import { rememberVideoReturnPath, routeToPath } from "@/lib/videoReturnPath";

const HomePage = lazy(() => import("@/pages/HomePage"));
const ListingPage = lazy(() => import("@/pages/ListingPage"));
const ShortsPage = lazy(() => import("@/pages/ShortsPage"));
const UploadPage = lazy(() => import("@/pages/UploadPage"));
const VideoDetailPage = lazy(() => import("@/pages/VideoDetailPage"));

const LoginPage = lazy(() =>
  import("@/admin/LoginPage").then((module) => ({ default: module.LoginPage }))
);
const DrivesPage = lazy(() =>
  import("@/admin/DrivesPage").then((module) => ({ default: module.DrivesPage }))
);
const CrawlersPage = lazy(() =>
  import("@/admin/CrawlersPage").then((module) => ({
    default: module.CrawlersPage,
  }))
);
const VideosPage = lazy(() =>
  import("@/admin/VideosPage").then((module) => ({ default: module.VideosPage }))
);
const TagsPage = lazy(() =>
  import("@/admin/TagsPage").then((module) => ({ default: module.TagsPage }))
);
const ThemePage = lazy(() =>
  import("@/admin/ThemePage").then((module) => ({ default: module.ThemePage }))
);

function PageSuspense({ children }: { children: ReactNode }) {
  return <Suspense fallback={null}>{children}</Suspense>;
}

function VideoReturnPathRecorder() {
  const location = useLocation();

  useEffect(() => {
    rememberVideoReturnPath(routeToPath(location));
  }, [location.pathname, location.search, location.hash]);

  return null;
}

export default function App() {
  return (
    <>
      {/* 星空蓝主题的固定位置星星层，仅在 data-theme="sky" 下可见 */}
      <SkyStarfield />
      <VideoReturnPathRecorder />
      <Routes>
        <Route
          path="/login"
          element={
            <PageSuspense>
              <LoginPage />
            </PageSuspense>
          }
        />

        {/* 主站需要登录 */}
        <Route
          path="/"
          element={
            <RequireAuth>
              <PageSuspense>
                <HomePage />
              </PageSuspense>
            </RequireAuth>
          }
        />
        <Route
          path="/list"
          element={
            <RequireAuth>
              <PageSuspense>
                <ListingPage />
              </PageSuspense>
            </RequireAuth>
          }
        />
        <Route
          path="/shorts"
          element={
            <RequireAuth>
              <PageSuspense>
                <ShortsPage />
              </PageSuspense>
            </RequireAuth>
          }
        />
        <Route
          path="/upload"
          element={
            <RequireAuth>
              <PageSuspense>
                <UploadPage />
              </PageSuspense>
            </RequireAuth>
          }
        />
        <Route
          path="/video/:id"
          element={
            <RequireAuth>
              <PageSuspense>
                <VideoDetailPage />
              </PageSuspense>
            </RequireAuth>
          }
        />

        {/* 管理后台也需要登录 */}
        <Route
          path="/admin"
          element={
            <RequireAuth>
              <AdminLayout />
            </RequireAuth>
          }
        >
          <Route index element={<Navigate to="/admin/drives" replace />} />
          <Route
            path="drives"
            element={
              <PageSuspense>
                <DrivesPage />
              </PageSuspense>
            }
          />
          <Route
            path="crawlers"
            element={
              <PageSuspense>
                <CrawlersPage />
              </PageSuspense>
            }
          />
          <Route
            path="videos"
            element={
              <PageSuspense>
                <VideosPage />
              </PageSuspense>
            }
          />
          <Route
            path="tags"
            element={
              <PageSuspense>
                <TagsPage />
              </PageSuspense>
            }
          />
          <Route
            path="theme"
            element={
              <PageSuspense>
                <ThemePage />
              </PageSuspense>
            }
          />
        </Route>

        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </>
  );
}
