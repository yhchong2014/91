import { useId, useMemo, useState } from "react";
import { ChevronDown } from "lucide-react";
import { PasswordInput } from "../PasswordInput";
import { P123QRCodeLogin } from "./P123QRCodeLogin";
import { WopanQRCodeLogin } from "./WopanQRCodeLogin";
import { GuangYaPanQRCodeLogin } from "./GuangYaPanQRCodeLogin";
import {
  FormState,
  Kind,
  credentialFields,
  driveKindIconPath,
  usesRootDirectoryID,
} from "./constants";

type DriveOption = {
  kind: Kind;
  label: string;
  abbr: string;
};

const DRIVE_OPTIONS: DriveOption[] = [
  { kind: "p115", label: "115 网盘", abbr: "115" },
  { kind: "p123", label: "123网盘", abbr: "123" },
  { kind: "pikpak", label: "PikPak", abbr: "Pk" },
  { kind: "guangyapan", label: "光鸭网盘", abbr: "GY" },
  { kind: "onedrive", label: "OneDrive", abbr: "OD" },
  { kind: "googledrive", label: "Google Drive", abbr: "GD" },
  { kind: "quark", label: "夸克网盘", abbr: "Qk" },
  { kind: "wopan", label: "联通网盘", abbr: "Wo" },
  { kind: "localstorage", label: "本地存储", abbr: "Lo" },
  { kind: "webdav", label: "WebDAV", abbr: "Dv" },
];

export function DriveForm({
  form,
  onChange,
  isEdit,
  onTypeSelected,
}: {
  form: FormState;
  onChange: (f: FormState) => void;
  isEdit: boolean;
  onTypeSelected?: () => void;
}) {
  const idPrefix = useId();
  const fields = useMemo(() => credentialFields(form.kind), [form.kind]);
  const [step, setStep] = useState<"type" | "form">(isEdit ? "form" : "type");
  const nameId = `${idPrefix}-drive-name`;
  const rootId = `${idPrefix}-drive-root`;

  function set<K extends keyof FormState>(k: K, v: FormState[K]) {
    onChange({ ...form, [k]: v });
  }
  function setCred(k: string, v: string) {
    onChange({ ...form, creds: { ...form.creds, [k]: v } });
  }
  function setKind(v: Kind) {
    onChange({
      ...form,
      kind: v,
      rootId: "",
      creds: {},
    });
  }
  function selectType(kind: Kind) {
    setKind(kind);
    setStep("form");
    onTypeSelected?.();
  }

  const selectedOption = DRIVE_OPTIONS.find((o) => o.kind === form.kind);
  const selectedIconSrc = selectedOption ? driveKindIconPath(selectedOption.kind) : "";

  if (step === "type" && !isEdit) {
    return (
      <div className="admin-drive-type-picker">
        <div className="admin-drive-type-grid">
          {DRIVE_OPTIONS.map((opt) => {
            const iconSrc = driveKindIconPath(opt.kind);
            return (
              <button
                key={opt.kind}
                type="button"
                className="admin-drive-type-card"
                data-kind={opt.kind}
                onClick={() => selectType(opt.kind)}
              >
                <span
                  className={`admin-drive-type-card__icon${iconSrc ? " has-image" : ""}`}
                  data-kind={opt.kind}
                >
                  {iconSrc ? (
                    <img
                      src={iconSrc}
                      alt=""
                      aria-hidden="true"
                      className="admin-drive-type-card__icon-img"
                    />
                  ) : (
                    opt.abbr
                  )}
                </span>
                <span className="admin-drive-type-card__label">{opt.label}</span>
              </button>
            );
          })}
        </div>
      </div>
    );
  }

  return (
    <div className="admin-form">
      {!isEdit && selectedOption && (
        <div className="admin-drive-selected-bar" data-kind={form.kind}>
          <span
            className={`admin-drive-selected-bar__icon${selectedIconSrc ? " has-image" : ""}`}
            data-kind={form.kind}
          >
            {selectedIconSrc ? (
              <img
                src={selectedIconSrc}
                alt=""
                aria-hidden="true"
                className="admin-drive-selected-bar__icon-img"
              />
            ) : (
              selectedOption.abbr
            )}
          </span>
          <div className="admin-drive-selected-bar__text">
            <span className="admin-drive-selected-bar__name">{selectedOption.label}</span>
          </div>
        </div>
      )}

      <div className="admin-form__section">
        <div className="admin-form__row">
          <label htmlFor={nameId}>名称</label>
          <input
            id={nameId}
            value={form.name}
            onChange={(e) => set("name", e.target.value)}
          />
        </div>

        {usesRootDirectoryID(form.kind) && (
          <div className="admin-form__row">
            <label htmlFor={rootId}>自定义网盘根目录(可选)</label>
            <input
              id={rootId}
              placeholder="根目录ID请参考OpenList文档"
              value={form.rootId}
              onChange={(e) => set("rootId", e.target.value)}
            />
          </div>
        )}
      </div>

      {fields.length > 0 && (
        <div className="admin-form__section">
          {form.kind === "p123" && (
            <P123QRCodeLogin
              onToken={(token) => setCred("access_token", token)}
            />
          )}

          {form.kind === "wopan" && (
            <WopanQRCodeLogin
              onCredentials={(credentials) =>
                onChange({
                  ...form,
                  creds: {
                    ...form.creds,
                    access_token: credentials.accessToken,
                    refresh_token: credentials.refreshToken,
                    ...(credentials.familyID ? { family_id: credentials.familyID } : {}),
                  },
                })
              }
            />
          )}

          {form.kind === "guangyapan" && (
            <GuangYaPanQRCodeLogin
              onCredentials={(credentials) =>
                onChange({
                  ...form,
                  creds: {
                    ...form.creds,
                    access_token: credentials.accessToken,
                    refresh_token: credentials.refreshToken,
                  },
                })
              }
            />
          )}

          {form.kind === "p123" && fields.length > 0 && (
            <div className="admin-form__method-label">方式二</div>
          )}

          {fields.map((f) => (
            <div key={f.key} className="admin-form__row">
              {f.type === "select" ? (
                <>
                  <label htmlFor={`${idPrefix}-credential-${f.key}`}>
                    {f.label}
                  </label>
                  <div className="admin-form-select-wrap">
                    <select
                      id={`${idPrefix}-credential-${f.key}`}
                      className="admin-form-select"
                      value={form.creds[f.key] ?? f.defaultValue ?? ""}
                      onChange={(e) => setCred(f.key, e.target.value)}
                    >
                      {(f.options ?? []).map((option) => (
                        <option key={option.value} value={option.value}>
                          {option.label}
                        </option>
                      ))}
                    </select>
                    <ChevronDown size={15} className="admin-form-select__icon" aria-hidden="true" />
                  </div>
                </>
              ) : (
                <>
                  <label htmlFor={`${idPrefix}-credential-${f.key}`}>
                    {f.label}
                  </label>
                  {f.multiline ? (
                    <textarea
                      id={`${idPrefix}-credential-${f.key}`}
                      value={form.creds[f.key] ?? ""}
                      onChange={(e) => setCred(f.key, e.target.value)}
                      required={f.required && !isEdit}
                    />
                  ) : isSecretCredential(f.key) ? (
                    <PasswordInput
                      id={`${idPrefix}-credential-${f.key}`}
                      value={form.creds[f.key] ?? ""}
                      onChange={(e) => setCred(f.key, e.target.value)}
                      required={f.required && !isEdit}
                    />
                  ) : (
                    <input
                      id={`${idPrefix}-credential-${f.key}`}
                      type="text"
                      value={form.creds[f.key] ?? ""}
                      onChange={(e) => setCred(f.key, e.target.value)}
                      required={f.required && !isEdit}
                    />
                  )}
                </>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function isSecretCredential(key: string): boolean {
  return /password|token|secret/i.test(key);
}
