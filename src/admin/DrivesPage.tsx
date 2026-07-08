import { useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  ArrowLeft,
  ChevronRight,
  FolderTree,
  HardDrive,
  RefreshCw,
} from "lucide-react";
import * as api from "./api";
import { useToast } from "./ToastContext";
import { Modal } from "./Modal";
import { ConfirmModal } from "./ConfirmModal";
import { formatBytes } from "./storageFormat";
import { makeUniqueDriveId } from "./driveId";
import {
  FormState,
  driveKindAbbr,
  driveKindIconPath,
  emptyForm,
  idleNightlyStatus,
  nightlyButtonText,
  nightlyBusyText,
  usesRootDirectoryID,
  defaultRootId,
  credentialFields,
} from "./drive/constants";
import {
  StorageSummary,
  StatusTag,
  DriveCardMetrics,
  DriveGenerationPanel,
} from "./drive/DriveComponents";
import { DriveForm } from "./drive/DriveForm";
import { DeleteDriveModal } from "./drive/DeleteDriveModal";
import { SkipDirsPanel } from "./drive/SkipDirsPanel";
import { AdminEmptyVisual } from "./AdminEmptyVisual";

const DRIVE_BUSY_MESSAGE = "当前存储有正在进行的任务，请稍后重试";
const NIGHTLY_BUSY_MESSAGE = "当前有全量扫描任务正在进行，请稍后重试";

function isDriveBusy(d: api.AdminDrive) {
  return [
    d.scanGenerationStatus,
    d.thumbnailGenerationStatus,
    d.previewGenerationStatus,
    d.fingerprintGenerationStatus,
    d.transcodeGenerationStatus,
  ].some((status) => {
    const state = status?.state || "idle";
    return state !== "idle";
  });
}

export function DrivesPage() {
  const [list, setList] = useState<api.AdminDrive[]>([]);
  const [storage, setStorage] = useState<api.AdminDriveStorage | null>(null);
  const [nightlyStatus, setNightlyStatus] =
    useState<api.NightlyJobStatus>(idleNightlyStatus);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [modalOpen, setModalOpen] = useState(false);
  const [discardConfirmOpen, setDiscardConfirmOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<api.AdminDrive | null>(null);
  const [form, setForm] = useState<FormState>(emptyForm);
  const [initialForm, setInitialForm] = useState<FormState>(emptyForm);
  const [createDriveTypeSelected, setCreateDriveTypeSelected] = useState(false);
  const [saving, setSaving] = useState(false);
  const [deletingId, setDeletingId] = useState("");
  const [regenFailedId, setRegenFailedId] = useState("");
  const [regenFailedThumbId, setRegenFailedThumbId] = useState("");
  const [regenFailedFingerprintId, setRegenFailedFingerprintId] = useState("");
  const [togglingTeaserId, setTogglingTeaserId] = useState("");
  const [togglingTranscodeId, setTogglingTranscodeId] = useState("");
  const [scanningAll, setScanningAll] = useState(false);
  const [stoppingAll, setStoppingAll] = useState(false);
  const [trackingNightly, setTrackingNightly] = useState(false);
  const [scanningDriveIds, setScanningDriveIds] = useState<Record<string, boolean>>({});
  const scanningDriveIdsRef = useRef(new Set<string>());
  const [stoppingDriveId, setStoppingDriveId] = useState("");
  const [searchParams, setSearchParams] = useSearchParams();
  const selectedDriveId = searchParams.get("drive") || null;
  const { show } = useToast();
  const pollConnectionLost = useRef(false);
  const nightlyBusy = scanningAll || nightlyStatus.running || nightlyStatus.queued;
  const formDirty = form.id
    ? !sameForm(form, initialForm)
    : hasCreateFormChanges(form);

  function openDriveDetail(id: string) {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set("drive", id);
      return next;
    });
  }

  function closeDriveDetail(options?: { replace?: boolean }) {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.delete("drive");
      return next;
    }, options);
  }

  async function refresh() {
    setLoading(true);
    setLoadError("");
    try {
      const [data, storageData, jobStatus] = await Promise.all([
        api.listDrives(),
        api.getDriveStorage(),
        api.getNightlyJobStatus().catch(() => null),
      ]);
      setList(data ?? []);
      setStorage(storageData);
      if (jobStatus) setNightlyStatus(jobStatus);
    } catch (e) {
      const message = e instanceof Error ? e.message : "加载失败";
      setLoadError(message);
      show(message, "error");
    } finally {
      setLoading(false);
    }
  }

  async function refreshDriveList() {
    try {
      const [data, jobStatus] = await Promise.all([
        api.listDrives(),
        api.getNightlyJobStatus().catch(() => null),
      ]);
      setList(data ?? []);
      if (jobStatus) setNightlyStatus(jobStatus);
      if (pollConnectionLost.current) {
        pollConnectionLost.current = false;
        show("连接已恢复，网盘数据已更新", "success");
      }
    } catch {
      if (!pollConnectionLost.current) {
        pollConnectionLost.current = true;
        show("连接中断，网盘数据可能不是最新", "error");
      }
    }
  }

  useEffect(() => {
    refresh();
  }, []);

  useEffect(() => {
    const timer = window.setInterval(() => {
      if (!document.hidden && !modalOpen) {
        refreshDriveList();
      }
    }, 5000);
    return () => window.clearInterval(timer);
  }, [modalOpen]);

  useEffect(() => {
    if (!trackingNightly) return;
    const timer = window.setInterval(async () => {
      try {
        const status = await api.getNightlyJobStatus();
        setNightlyStatus(status);
        if (status.running || (!status.queued && !status.running)) {
          setTrackingNightly(false);
        }
      } catch {
        // The normal drive polling already reports connection loss.
      }
    }, 2000);
    return () => window.clearInterval(timer);
  }, [trackingNightly]);

  function openCreate() {
    const nextForm = { ...emptyForm };
    setForm(nextForm);
    setInitialForm(nextForm);
    setCreateDriveTypeSelected(false);
    setModalOpen(true);
  }

  function openEdit(d: api.AdminDrive) {
    const nextForm: FormState = {
      id: d.id,
      kind: d.kind,
      name: d.name,
      rootId: d.rootId,
      creds:
        d.kind === "localstorage"
          ? { strm_allow_outside_root: (d.strmAllowOutsideRoot ?? false) ? "true" : "false" }
          : {},
    };
    setForm(nextForm);
    setInitialForm(nextForm);
    setModalOpen(true);
  }

  function requestCloseDriveModal() {
    if (saving) return;
    if (formDirty) {
      setDiscardConfirmOpen(true);
      return;
    }
    setModalOpen(false);
  }

  function discardDriveChanges() {
    setDiscardConfirmOpen(false);
    setModalOpen(false);
    setForm(initialForm);
  }

  function handleCreateFormChange(nextForm: FormState) {
    setForm(nextForm);
    if (!nextForm.id && !hasCreateFormChanges(nextForm)) {
      setInitialForm(nextForm);
    }
  }

  async function handleSave() {
    const name = form.name.trim();
    if (!form.kind) {
      show("请选择网盘类型", "error");
      return;
    }
    if (!name) {
      show("请填写网盘名称", "error");
      return;
    }
    if (!form.id) {
      if (form.kind === "p123") {
        const hasScannedToken = Boolean((form.creds.access_token ?? "").trim());
        const hasUsername = Boolean((form.creds.username ?? "").trim());
        const hasPassword = Boolean((form.creds.password ?? "").trim());
        if (!hasScannedToken && (!hasUsername || !hasPassword)) {
          show("请使用方式一扫码登录，或填写方式二的手机号/邮箱和密码", "error");
          return;
        }
      }
      const missingField = credentialFields(form.kind).find(
        (field) =>
          field.required &&
          !((form.creds[field.key] ?? field.defaultValue ?? "").trim())
      );
      if (missingField) {
        show(`请填写${missingField.label}`, "error");
        return;
      }
    }
    const existing = list.find((x) => x.id === form.id);
    const driveID = existing
      ? form.id
      : makeUniqueDriveId(form.kind, name, list);
    const rootId = usesRootDirectoryID(form.kind)
      ? form.rootId.trim() || defaultRootId(form.kind)
      : defaultRootId(form.kind);
    setSaving(true);
    try {
      const resp = await api.upsertDrive({
        id: driveID,
        kind: form.kind,
        name,
        rootId,
        credentials: form.creds,
      });

      if (resp.warning) {
        show(`已保存，但 driver 初始化失败：${resp.warning}`, "error");
      } else {
        show("已保存", "success");
      }
      setModalOpen(false);
      setInitialForm(form);
      refresh();
    } catch (e) {
      show(e instanceof Error ? e.message : "保存失败", "error");
    } finally {
      setSaving(false);
    }
  }

  async function confirmDeleteDrive() {
    if (!deleteTarget) return;
    const d = deleteTarget;
    setDeletingId(d.id);
    try {
      const resp = await api.deleteDrive(d.id, { deleteVideos: true });
      show(`已删除，并清理 ${resp.deletedVideos ?? 0} 个视频`, "success");
      setDeleteTarget(null);
      if (selectedDriveId === d.id) {
        closeDriveDetail({ replace: true });
      }
      refresh();
    } catch (e) {
      show(e instanceof Error ? e.message : "删除失败", "error");
    } finally {
      setDeletingId("");
    }
  }

  async function handleRescan(d: api.AdminDrive) {
    if (nightlyBusy) {
      show(nightlyBusyText(nightlyStatus) || NIGHTLY_BUSY_MESSAGE, "info");
      return;
    }
    if (isDriveBusy(d) || scanningDriveIdsRef.current.has(d.id)) {
      show(DRIVE_BUSY_MESSAGE, "info");
      return;
    }
    scanningDriveIdsRef.current.add(d.id);
    setScanningDriveIds((prev) => ({ ...prev, [d.id]: true }));
    try {
      const resp = await api.rescan(d.id);
      if (!resp.accepted) {
        if (resp.status) {
          setNightlyStatus(resp.status);
        }
        show(resp.message || DRIVE_BUSY_MESSAGE, "info");
        refreshDriveList();
        return;
      }
      show("已触发扫描，可稍后刷新视频列表查看", "success");
      refreshDriveList();
    } catch (e) {
      show(e instanceof Error ? e.message : "触发失败", "error");
    } finally {
      scanningDriveIdsRef.current.delete(d.id);
      setScanningDriveIds((prev) => {
        const next = { ...prev };
        delete next[d.id];
        return next;
      });
    }
  }

  async function handleRunNightly() {
    if (nightlyBusy) {
      show(nightlyBusyText(nightlyStatus) || NIGHTLY_BUSY_MESSAGE, "info");
      return;
    }
    setScanningAll(true);
    try {
      const resp = await api.runNightlyJob();
      setNightlyStatus(resp.status);
      if (resp.accepted) {
        setTrackingNightly(!resp.status.running);
        show("已触发扫描所有网盘，耗时较长，可在任务状态和 backend 日志观察进度", "success");
      } else {
        show(resp.message || NIGHTLY_BUSY_MESSAGE, "info");
      }
    } catch (e) {
      show(e instanceof Error ? e.message : "触发失败", "error");
    } finally {
      setScanningAll(false);
    }
  }

  async function handleStopAllTasks() {
    if (stoppingAll) return;
    setStoppingAll(true);
    try {
      const resp = await api.stopAllTasks();
      setNightlyStatus(resp.status);
      setTrackingNightly(false);
      show(
        resp.stoppedDrives > 0
          ? `已停止 ${resp.stoppedDrives} 个网盘的当前任务`
          : "没有正在运行的网盘任务",
        "success"
      );
      refreshDriveList();
    } catch (e) {
      show(e instanceof Error ? e.message : "停止失败", "error");
    } finally {
      setStoppingAll(false);
    }
  }

  async function handleStopDriveTasks(d: api.AdminDrive) {
    if (stoppingDriveId) return;
    setStoppingDriveId(d.id);
    try {
      const resp = await api.stopDriveTasks(d.id);
      show(
        resp.stopped
          ? `已停止「${d.name || d.id}」的当前任务`
          : `「${d.name || d.id}」没有正在运行的任务`,
        "success"
      );
      refreshDriveList();
    } catch (e) {
      show(e instanceof Error ? e.message : "停止失败", "error");
    } finally {
      setStoppingDriveId("");
    }
  }

  async function handleRegenFailed(d: api.AdminDrive) {
    setRegenFailedId(d.id);
    try {
      await api.regenFailedPreviews(d.id);
      show("已触发预览视频生成", "success");
      refresh();
    } catch (e) {
      show(e instanceof Error ? e.message : "触发失败", "error");
    } finally {
      setRegenFailedId("");
    }
  }

  async function handleRegenFailedThumbnails(d: api.AdminDrive) {
    setRegenFailedThumbId(d.id);
    try {
      await api.regenFailedThumbnails(d.id);
      show("已触发封面生成", "success");
      refresh();
    } catch (e) {
      show(e instanceof Error ? e.message : "触发失败", "error");
    } finally {
      setRegenFailedThumbId("");
    }
  }

  async function handleRegenFailedFingerprints(d: api.AdminDrive) {
    setRegenFailedFingerprintId(d.id);
    try {
      await api.regenFailedFingerprints(d.id);
      show("已触发指纹生成", "success");
      refresh();
    } catch (e) {
      show(e instanceof Error ? e.message : "触发失败", "error");
    } finally {
      setRegenFailedFingerprintId("");
    }
  }

  async function handleToggleTeaser(d: api.AdminDrive) {
    const next = !d.teaserEnabled;
    setTogglingTeaserId(d.id);
    setList((prev) =>
      prev.map((item) =>
        item.id === d.id ? { ...item, teaserEnabled: next } : item
      )
    );
    try {
      const resp = await api.setDriveTeaserEnabled(d.id, next);
      show(
        resp.teaserEnabled
          ? `已开启「${d.name || d.id}」的预览视频生成`
          : `已关闭「${d.name || d.id}」的预览视频生成`,
        "success"
      );
      setList((prev) =>
        prev.map((item) =>
          item.id === d.id ? { ...item, teaserEnabled: resp.teaserEnabled } : item
        )
      );
      refreshDriveList();
    } catch (e) {
      setList((prev) =>
        prev.map((item) =>
          item.id === d.id ? { ...item, teaserEnabled: d.teaserEnabled } : item
        )
      );
      show(e instanceof Error ? e.message : "切换失败", "error");
    } finally {
      setTogglingTeaserId("");
    }
  }

  async function handleStartTranscode(d: api.AdminDrive) {
    setTogglingTranscodeId(d.id);
    try {
      const resp = await api.startDriveTranscode(d.id);
      if (resp.accepted) {
        show(`已开始「${d.name || d.id}」的视频转码`, "success");
      } else {
        show(resp.message || "转码任务未能开启", "info");
      }
      refreshDriveList();
    } catch (e) {
      show(e instanceof Error ? e.message : "开启失败", "error");
    } finally {
      setTogglingTranscodeId("");
    }
  }

  async function handleStopTranscode(d: api.AdminDrive) {
    setTogglingTranscodeId(d.id);
    try {
      const resp = await api.stopDriveTranscode(d.id);
      show(
        resp.stopped
          ? `已停止「${d.name || d.id}」的视频转码`
          : `「${d.name || d.id}」没有正在运行的转码任务`,
        "success"
      );
      refreshDriveList();
    } catch (e) {
      show(e instanceof Error ? e.message : "停止失败", "error");
    } finally {
      setTogglingTranscodeId("");
    }
  }

  const selectedDrive = useMemo(() => {
    return selectedDriveId ? list.find((d) => d.id === selectedDriveId) : null;
  }, [selectedDriveId, list]);

  if (selectedDriveId && !selectedDrive) {
    const title = loading || loadError ? "网盘详情" : "网盘不存在";

    return (
      <section className="admin-drives-page">
        <header className="admin-drive-detail__header-bar">
          <button
            type="button"
            className="admin-drive-detail__back-btn"
            onClick={() => closeDriveDetail({ replace: true })}
            title="返回网盘列表"
          >
            <ArrowLeft size={16} />
          </button>
          <div className="admin-drive-detail__title-wrap">
            <h1 className="admin-drive-detail__title">{title}</h1>
          </div>
        </header>

        {loading ? (
          <div className="admin-loading-state">
            <RefreshCw size={20} className="admin-spin" />
            <span>加载中...</span>
          </div>
        ) : loadError ? (
          <div className="admin-error-state">
            <strong>网盘数据加载失败</strong>
            <span>{loadError}</span>
            <button type="button" className="admin-btn" onClick={refresh}>
              <RefreshCw size={13} /> 重试
            </button>
          </div>
        ) : (
          <div className="admin-card admin-empty">
            未找到这个网盘，可能已被删除或配置尚未加载。
          </div>
        )}
      </section>
    );
  }

  // --- Detail view ---
  if (selectedDriveId && selectedDrive) {
    const d = selectedDrive;
    const driveStorage = storage?.drives[d.id];

    return (
      <section className="admin-drives-page">
        <header className="admin-drive-detail__header-bar">
          <button
            type="button"
            className="admin-drive-detail__back-btn"
            onClick={() => closeDriveDetail({ replace: true })}
            title="返回网盘列表"
          >
            <ArrowLeft size={16} />
          </button>
          <div className="admin-drive-detail__title-wrap">
            <h1 className="admin-drive-detail__title">{d.name || d.id}</h1>
          </div>
        </header>

        <div className="admin-drive-detail-layout">
          <div>
            <div className="admin-detail-card">
              <header className="admin-detail-card__title">
                <div className="admin-detail-card__title-left">
                  <HardDrive size={16} />
                  <span>基本信息</span>
                </div>
              </header>

              <div className="admin-detail-grid">
                <div className="admin-detail-row">
                  <span className="admin-detail-label">网盘 ID</span>
                  <span className="admin-detail-value admin-mono-cell">{d.id}</span>
                </div>
                {usesRootDirectoryID(d.kind) && (
                  <div className="admin-detail-row">
                    <span className="admin-detail-label">自定义网盘根目录(可选)</span>
                    <span className="admin-detail-value admin-mono-cell">{d.rootId}</span>
                  </div>
                )}
              </div>
              {d.lastError && (
                <div className="admin-detail-error">{d.lastError}</div>
              )}

              <div className="admin-detail-actions">
                <div className="admin-task-controls" aria-label="当前网盘任务控制">
                  <button
                    type="button"
                    className="admin-btn"
                    onClick={() => handleRescan(d)}
                    aria-disabled={nightlyBusy || isDriveBusy(d) || !!scanningDriveIds[d.id]}
                    title={
                      nightlyBusy
                        ? nightlyBusyText(nightlyStatus) || NIGHTLY_BUSY_MESSAGE
                        : isDriveBusy(d) || scanningDriveIds[d.id]
                        ? DRIVE_BUSY_MESSAGE
                        : undefined
                    }
                  >
                    {scanningDriveIds[d.id] ? "触发中..." : "开始扫盘"}
                  </button>
                  <button
                    type="button"
                    className="admin-btn"
                    onClick={() => handleStopDriveTasks(d)}
                    disabled={!!stoppingDriveId}
                    title="停止此网盘当前的扫描、封面、预览视频和视频指纹生成任务。"
                  >
                    {stoppingDriveId === d.id ? "停止中..." : "停止任务"}
                  </button>
                </div>
                <button type="button" className="admin-btn" onClick={() => openEdit(d)}>
                  编辑凭证
                </button>
                <button type="button" className="admin-btn admin-detail-actions__danger" onClick={() => setDeleteTarget(d)}>
                  删除网盘
                </button>
              </div>
            </div>

            <SkipDirsPanel
              drive={d}
              onSaved={(saved) => {
                setList((prev) =>
                  prev.map((item) =>
                    item.id === saved.id ? { ...item, skipDirIds: saved.skipDirIds } : item
                  )
                );
                refreshDriveList();
              }}
            />
          </div>

          <div>
            <DriveGenerationPanel
              d={d}
              regenFailedId={regenFailedId}
              regenFailedThumbId={regenFailedThumbId}
              regenFailedFingerprintId={regenFailedFingerprintId}
              togglingTeaserId={togglingTeaserId}
              togglingTranscodeId={togglingTranscodeId}
              onToggleTeaser={() => handleToggleTeaser(d)}
              onRegenFailed={() => handleRegenFailed(d)}
              onRegenFailedThumbnails={() => handleRegenFailedThumbnails(d)}
              onRegenFailedFingerprints={() => handleRegenFailedFingerprints(d)}
              onStartTranscode={() => handleStartTranscode(d)}
              onStopTranscode={() => handleStopTranscode(d)}
            />

            <div className="admin-detail-card">
              <header className="admin-detail-card__title">
                <div className="admin-detail-card__title-left">
                  <FolderTree size={16} />
                  <span>本地存储占用</span>
                </div>
              </header>
              <div className="admin-local-storage-metrics">
                <div className="admin-local-storage-metric">
                  <span>封面</span>
                  <strong>{formatBytes(driveStorage?.thumbnailBytes ?? 0)}</strong>
                </div>
                <div className="admin-local-storage-metric">
                  <span>预览视频</span>
                  <strong>{formatBytes(driveStorage?.teaserBytes ?? 0)}</strong>
                </div>
                <div className="admin-local-storage-metric">
                  <span>合计</span>
                  <strong>{formatBytes(driveStorage?.totalBytes ?? 0)}</strong>
                </div>
              </div>
            </div>
          </div>
        </div>

        <Modal
          open={modalOpen}
          title="编辑网盘"
          className="admin-modal--drive-form"
          onClose={requestCloseDriveModal}
          footer={
            <>
              <button type="button" className="admin-btn" onClick={requestCloseDriveModal}>
                取消
              </button>
              <button
                type="button"
                className="admin-btn"
                onClick={handleSave}
                disabled={saving}
              >
                {saving ? "确认中..." : "确认"}
              </button>
            </>
          }
        >
          <DriveForm
            form={form}
            onChange={setForm}
            isEdit={true}
          />
        </Modal>
        <DeleteDriveModal
          drive={deleteTarget}
          deleting={deletingId === deleteTarget?.id}
          onCancel={() => {
            if (!deletingId) {
              setDeleteTarget(null);
            }
          }}
          onConfirm={confirmDeleteDrive}
        />
        <ConfirmModal
          open={discardConfirmOpen}
          title="放弃未保存更改"
          message="当前网盘配置有未保存的更改，确定要放弃吗？"
          confirmText="放弃更改"
          danger
          centerMessage
          modalClassName="admin-modal--delete-confirm"
          onCancel={() => setDiscardConfirmOpen(false)}
          onConfirm={discardDriveChanges}
        />
      </section>
    );
  }

  // --- List view ---
  return (
    <section className="admin-drives-page admin-drives-page--list">
      <header className="admin-page__header">
        <div className="admin-page__actions admin-drive-list-actions">
          <div className="admin-task-controls" aria-label="所有网盘任务控制">
            <button
              type="button"
              className="admin-btn"
              onClick={handleRunNightly}
              disabled={scanningAll}
              title={nightlyBusyText(nightlyStatus) || "立即扫描所有网盘。耗时较长，期间不要重复触发。"}
            >
              {nightlyButtonText(nightlyStatus, scanningAll)}
            </button>
            <button
              type="button"
              className="admin-btn"
              onClick={handleStopAllTasks}
              disabled={stoppingAll}
              title="停止所有网盘当前的扫描、封面、预览视频和视频指纹生成任务。"
            >
              {stoppingAll ? "停止中..." : "停止所有任务"}
            </button>
          </div>
          <button type="button" className="admin-btn" onClick={openCreate}>
            添加网盘
          </button>
        </div>
      </header>

      {storage && <StorageSummary storage={storage} />}

      {loading ? (
        <div className="admin-loading-state">
          <RefreshCw size={20} className="admin-spin" />
          <span>加载中...</span>
        </div>
      ) : loadError ? (
        <div className="admin-error-state">
          <strong>网盘数据加载失败</strong>
          <span>{loadError}</span>
          <button type="button" className="admin-btn" onClick={refresh}>
            <RefreshCw size={13} /> 重试
          </button>
        </div>
      ) : list.length > 0 ? (
        <div className="admin-drives-grid">
          {list.map((d) => {
            const iconSrc = driveKindIconPath(d.kind);
            return (
              <button
                type="button"
                key={d.id}
                className="admin-drive-card"
                onClick={() => openDriveDetail(d.id)}
                aria-label={`管理网盘 ${d.name || d.id}`}
              >
                <div className="admin-drive-card__header">
                  <div className="admin-drive-card__title">
                    <span
                      className={`admin-drive-card__brand-icon${iconSrc ? " has-image" : ""}`}
                      data-kind={d.kind}
                    >
                      {iconSrc ? (
                        <img
                          src={iconSrc}
                          alt=""
                          aria-hidden="true"
                          className="admin-drive-card__brand-icon-img"
                        />
                      ) : (
                        driveKindAbbr(d.kind)
                      )}
                    </span>
                    <span>{d.name || d.id}</span>
                  </div>
                  <StatusTag status={d.status} error={d.lastError} hasCred={d.hasCredential} />
                </div>

                <DriveCardMetrics d={d} />

                <div className="admin-drive-card__footer">
                  <span>本地占用: {formatBytes(storage?.drives[d.id]?.totalBytes ?? 0)}</span>
                  <span className="admin-drive-card__manage-link">
                    管理 <ChevronRight size={14} />
                  </span>
                </div>
              </button>
            );
          })}
        </div>
      ) : (
        <AdminEmptyVisual
          variant="empty"
          text="请先添加网盘"
          className="admin-drive-empty-state"
        />
      )}

      <Modal
        open={modalOpen}
        title={form.id && list.find((x) => x.id === form.id) ? "编辑网盘" : "添加网盘"}
        className="admin-modal--drive-form"
        onClose={requestCloseDriveModal}
        footer={form.id || createDriveTypeSelected ? (
          <>
            <button type="button" className="admin-btn" onClick={requestCloseDriveModal}>
              取消
            </button>
            <button
              type="button"
              className="admin-btn is-primary"
              onClick={handleSave}
              disabled={saving}
            >
              {saving ? "保存中..." : "保存"}
            </button>
          </>
        ) : undefined}
      >
        <DriveForm
          form={form}
          onChange={handleCreateFormChange}
          isEdit={!!list.find((x) => x.id === form.id)}
          onTypeSelected={() => setCreateDriveTypeSelected(true)}
        />
      </Modal>
      <DeleteDriveModal
        drive={deleteTarget}
        deleting={deletingId === deleteTarget?.id}
        onCancel={() => {
          if (!deletingId) {
            setDeleteTarget(null);
          }
        }}
        onConfirm={confirmDeleteDrive}
      />
      <ConfirmModal
        open={discardConfirmOpen}
        title="放弃未保存更改"
        message="当前网盘配置有未保存的更改，确定要放弃吗？"
        confirmText="放弃更改"
        danger
        centerMessage
        modalClassName="admin-modal--delete-confirm"
        onCancel={() => setDiscardConfirmOpen(false)}
        onConfirm={discardDriveChanges}
      />
    </section>
  );
}

function sameForm(a: FormState, b: FormState): boolean {
  return (
    a.id === b.id &&
    a.kind === b.kind &&
    a.name === b.name &&
    a.rootId === b.rootId &&
    sameRecord(a.creds, b.creds)
  );
}

function sameRecord(a: Record<string, string>, b: Record<string, string>): boolean {
  const keys = new Set([...Object.keys(a), ...Object.keys(b)]);
  for (const key of keys) {
    if ((a[key] ?? "") !== (b[key] ?? "")) return false;
  }
  return true;
}

function hasCreateFormChanges(form: FormState): boolean {
  if (form.name.trim() !== "") return true;
  if (form.rootId.trim() !== "") return true;
  return Object.values(form.creds).some((value) => value.trim() !== "");
}
