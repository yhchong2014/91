export type Kind = "quark" | "p115" | "p123" | "pikpak" | "wopan" | "guangyapan" | "onedrive" | "googledrive" | "localstorage" | "spider91";

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
  spider91: "91",
};

export function driveKindAbbr(kind: string): string {
  const explicit = kindAbbr[kind];
  if (explicit) return explicit;

  const trimmed = kind.trim();
  if (!trimmed) return "??";
  const compact = trimmed.replace(/[^a-zA-Z0-9]+/g, "");
  return (compact || trimmed).slice(0, 2).toUpperCase();
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
  spider91: "91 爬虫",
};

export type FormState = {
  id: string;
  kind: Kind;
  name: string;
  rootId: string;
  creds: Record<string, string>;
  spider91UploadDriveId: string;
};

export const emptyForm: FormState = {
  id: "",
  kind: "p115",
  name: "",
  rootId: "",
  creds: {},
  spider91UploadDriveId: "",
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
  if (kind === "spider91") return "/";
  return "0";
}

export function usesRootDirectoryID(kind: Kind): boolean {
  return kind !== "localstorage" && kind !== "spider91";
}

export function rootIdPlaceholder(kind: Kind): string {
  const rootId = defaultRootId(kind);
  return rootId ? `默认：${rootId}` : "留空表示根目录";
}

export function credentialHelp(kind: Kind, isEdit: boolean): string {
  const note = isEdit ? "如不修改凭证，留空即可，保存时会沿用旧值。" : "";
  switch (kind) {
    case "quark":
      return `在 pan.quark.cn 登录后，F12 → Network → 任意请求 → Request Headers 里复制整段 Cookie 粘贴到下方。${note}`;
    case "p115":
      return `登录 115.com 后复制 Cookie，形如 "UID=...; CID=...; SEID=...; KID=..."。${note}`;
    case "p123":
      return `推荐使用扫码登录自动获取 access_token；账号密码登录被 123网盘风控拦截时，也可以只填写 access_token。播放走 302 跳转到 123网盘返回的短期 CDN 地址。${note}`;
    case "pikpak":
      return `填写 PikPak 账号和密码即可。平台、设备 ID、验证码 token 和 refresh token 会由服务端自动处理并保存。${note}`;
    case "wopan":
      return `推荐使用扫码登录自动获取 access_token 和 refresh_token；也可以手工粘贴已有凭证。${note}`;
    case "guangyapan":
      return `推荐使用扫码登录自动获取 access_token 和 refresh_token；也可以手工粘贴已有 token。${note}`;
    case "onedrive":
      return `按 OpenList 默认应用在线挂载，只需要 refresh_token；保存时会自动刷新并保存 token。${note}`;
    case "googledrive":
      return isEdit
        ? "请参考OpenList文档中关于谷歌云盘的配置方法；如不修改凭证，留空即可，保存时会沿用旧值"
        : "请参考OpenList文档中关于谷歌云盘的配置方法";
    case "localstorage":
      return `填写服务器可访问的本地目录绝对路径，例如 /mnt/videos。系统会扫描该目录及子目录中的视频文件和 .strm 文件；.strm 可指向 HTTP/HTTPS 直链或本地视频路径（指向目录外需开启下方开关）。Docker 部署时请填写容器内路径。${note}`;
    case "spider91":
      return "91Spider 不再支持通过网盘添加或编辑。请到后台爬虫管理页面添加爬虫脚本。";
    default:
      return "";
  }
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
  help?: string;
};

export function credentialBoolValue(value: string | undefined, defaultValue: boolean): boolean {
  const normalized = (value ?? "").trim().toLowerCase();
  if (normalized === "") return defaultValue;
  if (normalized === "true" || normalized === "1" || normalized === "yes" || normalized === "on") return true;
  if (normalized === "false" || normalized === "0" || normalized === "no" || normalized === "off") return false;
  return defaultValue;
}

export function googleDriveUsesOnlineAPI(creds: Record<string, string> = {}): boolean {
  return credentialBoolValue(creds.use_online_api, true);
}

export function credentialFields(kind: Kind, creds: Record<string, string> = {}): CredentialField[] {
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
          label: "用户名 / 邮箱（可选）",
          placeholder: "user@example.com",
        },
        {
          key: "password",
          label: "密码（可选）",
          placeholder: "123网盘密码",
        },
        {
          key: "access_token",
          label: "access_token（推荐用于风控场景）",
          placeholder: "Bearer eyJ... 或直接粘贴 token",
          multiline: true,
          help: "扫码成功后会自动填入该字段；如果 token 过期，重新扫码后保存即可。",
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
          help: "扫码成功后会自动填入该字段；如果 token 过期，重新扫码后保存即可。",
        },
        {
          key: "refresh_token",
          label: "refresh_token",
          placeholder: "",
          required: true,
        },
        {
          key: "family_id",
          label: "family_id（家庭空间可选）",
          placeholder: "留空走个人空间",
        },
      ];
    case "guangyapan":
      return [
        {
          key: "root_path",
          label: "根目录路径（可选）",
          placeholder: "例如：影视/电影；留空使用上方根目录 ID",
          help: "如果填写 root_path，服务端会按路径解析光鸭目录，并优先作为扫描根目录。",
        },
        {
          key: "refresh_token",
          label: "refresh_token",
          placeholder: "推荐填写，服务端会自动刷新 access_token",
          multiline: true,
          help: "扫码成功后会自动填入该字段。",
        },
        {
          key: "access_token",
          label: "access_token",
          placeholder: "Bearer eyJ... 或直接粘贴 token",
          multiline: true,
          help: "扫码成功后会自动填入该字段；如果 token 过期，重新扫码后保存即可。",
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
          key: "use_online_api",
          label: "认证方式",
          placeholder: "",
          type: "select",
          defaultValue: "true",
          options: [
            { value: "true", label: "OpenList 在线 API" },
            { value: "false", label: "自建 Google OAuth 客户端" },
          ],
        },
        ...(googleDriveUsesOnlineAPI(creds)
          ? [
              {
                key: "api_url_address",
                label: "OpenList 在线 API URL",
                placeholder: "默认：https://api.oplist.org/googleui/renewapi",
                help: "留空时使用 OpenList 官方在线 API，填写后会使用自定义续期 API。",
              },
            ]
          : [
              {
                key: "client_id",
                label: "客户端 ID",
                placeholder: "xxxxxxxxxxxx-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx.apps.googleusercontent.com",
                required: true,
                help: "Google Cloud Console 中 OAuth 2.0 客户端的 Client ID",
              },
              {
                key: "client_secret",
                label: "客户端密钥",
                placeholder: "Google OAuth client secret",
                required: true,
                help: "Google Cloud Console 中同一个 OAuth 客户端的 Client Secret",
              },
            ]),
        {
          key: "refresh_token",
          label: "refresh_token",
          placeholder: "OpenList Google Drive refresh_token",
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
          help: "路径必须是后端服务器上的已有目录；保存后可手动重扫，系统会递归扫描支持的视频格式。",
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
          help: "开启后 .strm 可指向本目录之外的本地文件（如 rclone 挂载点）。注意：等于允许通过 .strm 读取服务器上任意文件，请只在自己完全掌控媒体目录时开启。Docker 部署时路径必须是容器内路径。",
        },
      ];
    case "spider91":
      return [
        {
          key: "proxy",
          label: "代理地址（可选）",
          placeholder: "http://127.0.0.1:7890",
          help: "支持 http://、https://、socks5://、socks5h://代理",
        },
      ];
  }
}
