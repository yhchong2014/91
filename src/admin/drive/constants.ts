import googledriveIcon from "./icons/googledrive.png";
import guangyapanIcon from "./icons/guangyapan.png";
import localstorageIcon from "./icons/localstorage.svg";
import onedriveIcon from "./icons/onedrive.png";
import p115Icon from "./icons/p115.png";
import p123Icon from "./icons/p123.png";
import pikpakIcon from "./icons/pikpak.png";
import quarkIcon from "./icons/quark.png";
import webdavIcon from "./icons/webdav.svg";
import wopanIcon from "./icons/wopan.png";

export type Kind = "quark" | "p115" | "p123" | "pikpak" | "wopan" | "guangyapan" | "onedrive" | "googledrive" | "localstorage" | "webdav";

export const kindAbbr: Record<string, string> = {
  quark: "Qk",
  p115: "115",
  p123: "123",
  pikpak: "Pk",
  wopan: "Wo",
  guangyapan: "GY",
  onedrive: "OD",
  googledrive: "GD",
  localstorage: "Lo",
  webdav: "Dv",
};

export const kindIconPath: Record<string, string> = {
  quark: quarkIcon,
  p115: p115Icon,
  p123: p123Icon,
  pikpak: pikpakIcon,
  wopan: wopanIcon,
  guangyapan: guangyapanIcon,
  onedrive: onedriveIcon,
  googledrive: googledriveIcon,
  localstorage: localstorageIcon,
  webdav: webdavIcon,
};

export function driveKindAbbr(kind: string): string {
  const explicit = kindAbbr[kind];
  if (explicit) return explicit;

  const trimmed = kind.trim();
  if (!trimmed) return "??";
  const compact = trimmed.replace(/[^a-zA-Z0-9]+/g, "");
  return (compact || trimmed).slice(0, 2).toUpperCase();
}

export function driveKindIconPath(kind: string): string {
  return kindIconPath[kind] || "";
}

export const kindLabel: Record<string, string> = {
  quark: "夸克网盘",
  p115: "115 网盘",
  p123: "123网盘",
  pikpak: "PikPak",
  wopan: "联通网盘",
  guangyapan: "光鸭网盘",
  onedrive: "OneDrive",
  googledrive: "Google Drive",
  localstorage: "本地存储",
  webdav: "WebDAV",
};

export type FormState = {
  id: string;
  kind: Kind;
  name: string;
  rootId: string;
  creds: Record<string, string>;
};

export const emptyForm: FormState = {
  id: "",
  kind: "p115",
  name: "",
  rootId: "",
  creds: {},
};

export const idleNightlyStatus = {
  state: "idle" as const,
  running: false,
  queued: false,
};

export function nightlyButtonText(status: { running: boolean; queued: boolean }, triggering: boolean) {
  if (triggering) return "触发中...";
  if (status.running) return "扫描运行中";
  if (status.queued) return "扫描已排队";
  return "扫描所有网盘";
}

export function nightlyBusyText(status: { running: boolean; queued: boolean }) {
  if (status.running || status.queued) return "当前有全量扫描任务正在进行，请稍后重试";
  return "";
}

export function generationStateLabel(state: string): string {
  if (state === "scanning") return "扫盘中";
  if (state === "uploading") return "上传中";
  if (state === "generating") return "生成中";
  if (state === "cooling") return "冷却中";
  if (state === "queued") return "排队中";
  return "空闲";
}

export function generationStateClass(state: string): string {
  if (state === "scanning" || state === "uploading" || state === "generating" || state === "cooling" || state === "queued") {
    if (state === "scanning" || state === "uploading") return "generating";
    return state;
  }
  return "idle";
}

export function generationDetail(status?: { state: string; cooldownUntil?: string; currentTitle?: string }): string {
  if (!status) return "";
  if (status.state === "cooling" && status.cooldownUntil) {
    return `剩余 ${formatCooldownRemaining(status.cooldownUntil)}`;
  }
  if (status.currentTitle) {
    return status.currentTitle;
  }
  return "";
}

export function generationTitle(status: { state: string; cooldownUntil?: string; currentTitle?: string } | undefined, detail: string): string | undefined {
  if (!status) return detail || undefined;
  if (status.state === "cooling" && status.cooldownUntil) {
    return `冷却至 ${formatClock(status.cooldownUntil)}`;
  }
  return status.currentTitle || detail || undefined;
}

export function formatCooldownRemaining(value: string): string {
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  const totalSeconds = Math.max(0, Math.ceil((d.getTime() - Date.now()) / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) return `${hours}小时${minutes}分`;
  if (minutes > 0) return `${minutes}分${seconds}秒`;
  return `${seconds}秒`;
}

export function formatClock(value: string): string {
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return d.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });
}

export function defaultRootId(kind: Kind): string {
  if (kind === "pikpak") return "";
  if (kind === "guangyapan") return "";
  if (kind === "onedrive") return "root";
  if (kind === "googledrive") return "root";
  if (kind === "localstorage") return "/";
  if (kind === "webdav") return "/";
  return "0";
}

export function usesRootDirectoryID(kind: Kind): boolean {
  return kind !== "localstorage" && kind !== "webdav";
}

export function rootIdPlaceholder(kind: Kind): string {
  const rootId = defaultRootId(kind);
  return rootId ? `默认：${rootId}` : "留空表示根目录";
}

export type CredentialField = {
  key: string;
  label: string;
  placeholder: string;
  type?: "text" | "select";
  options?: Array<{ value: string; label: string }>;
  multiline?: boolean;
  required?: boolean;
  defaultValue?: string;
};

export function credentialFields(kind: Kind): CredentialField[] {
  switch (kind) {
    case "quark":
      return [
        {
          key: "cookie",
          label: "Cookie",
          placeholder: "__pus=...; __puus=...; ...",
          multiline: true,
          required: true,
        },
      ];
    case "p115":
      return [
        {
          key: "cookie",
          label: "Cookie",
          placeholder: "UID=xxx; CID=xxx; SEID=xxx; KID=xxx",
          multiline: true,
          required: true,
        },
      ];
    case "p123":
      return [
        {
          key: "username",
          label: "手机号/邮箱",
          placeholder: "手机号或邮箱",
        },
        {
          key: "password",
          label: "密码",
          placeholder: "123网盘密码",
        },
      ];
    case "pikpak":
      return [
        {
          key: "username",
          label: "用户名 / 邮箱",
          placeholder: "user@example.com",
          required: true,
        },
        {
          key: "password",
          label: "密码",
          placeholder: "PikPak 密码",
          required: true,
        },
      ];
    case "wopan":
      return [
        {
          key: "access_token",
          label: "access_token",
          placeholder: "",
          required: true,
        },
        {
          key: "refresh_token",
          label: "refresh_token",
          placeholder: "",
          required: true,
        },
      ];
    case "guangyapan":
      return [
        {
          key: "refresh_token",
          label: "refresh_token",
          placeholder: "推荐填写，服务端会自动刷新 access_token",
          multiline: true,
        },
        {
          key: "access_token",
          label: "access_token",
          placeholder: "Bearer eyJ... 或直接粘贴 token",
          multiline: true,
        },
      ];
    case "onedrive":
      return [
        {
          key: "refresh_token",
          label: "refresh_token",
          placeholder: "OpenList OneDrive refresh_token",
          multiline: true,
          required: true,
        },
      ];
    case "googledrive":
      return [
        {
          key: "client_id",
          label: "客户端 ID",
          placeholder: "xxxxxxxxxxxx-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx.apps.googleusercontent.com",
          required: true,
        },
        {
          key: "client_secret",
          label: "客户端密钥",
          placeholder: "Google OAuth client secret",
          required: true,
        },
        {
          key: "refresh_token",
          label: "refresh_token",
          placeholder: "Google OAuth refresh_token",
          multiline: true,
          required: true,
        },
      ];
    case "localstorage":
      return [
        {
          key: "path",
          label: "本地目录路径",
          placeholder: "/mnt/videos",
          required: true,
        },
        {
          key: "strm_allow_outside_root",
          label: ".strm 允许指向目录外",
          placeholder: "",
          type: "select",
          defaultValue: "false",
          options: [
            { value: "false", label: "关闭（默认，仅允许目录内路径）" },
            { value: "true", label: "开启（允许任意本地路径）" },
          ],
        },
      ];
    case "webdav":
      return [
        {
          key: "address",
          label: "WebDAV 地址",
          placeholder: "https://dav.example.com/dav",
          required: true,
        },
        {
          key: "username",
          label: "用户名",
          placeholder: "WebDAV 用户名",
        },
        {
          key: "password",
          label: "密码",
          placeholder: "WebDAV 密码",
        },
        {
          key: "root_path",
          label: "根路径",
          placeholder: "/ （默认根目录，可指定子目录）",
        },
        {
          key: "tls_insecure_skip_verify",
          label: "跳过 TLS 证书验证",
          placeholder: "",
          type: "select",
          defaultValue: "false",
          options: [
            { value: "false", label: "关闭（默认，验证证书）" },
            { value: "true", label: "开启（自签名证书时使用）" },
          ],
        },
      ];
  }
}
