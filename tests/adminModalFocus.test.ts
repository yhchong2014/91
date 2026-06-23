import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const modalSource = readFileSync(
  new URL("../src/admin/Modal.tsx", import.meta.url),
  "utf8"
);

test("admin modal does not reset focus when close handler identity changes", () => {
  assert.match(modalSource, /const onCloseRef = useRef\(onClose\);/);
  assert.match(modalSource, /onCloseRef\.current = onClose;/);
  assert.match(modalSource, /onCloseRef\.current\(\);/);
  assert.match(modalSource, /window\.clearTimeout\(focusTimer\);/);
  assert.match(modalSource, /\}, \[open\]\);/);
  assert.doesNotMatch(modalSource, /\}, \[open, onClose\]\);/);
});

test("admin modal backdrop clicks do not close dialogs", () => {
  assert.match(modalSource, /import \{ createPortal \} from "react-dom";/);
  assert.match(modalSource, /createPortal\(/);
  assert.match(modalSource, /document\.body/);
  assert.match(modalSource, /className="admin-modal-backdrop"/);
  assert.doesNotMatch(modalSource, /onMouseDown=\{\(e\) =>/);
  assert.doesNotMatch(modalSource, /e\.target === e\.currentTarget/);
});

test("admin modal supports titleless dialogs with aria labels", () => {
  assert.match(modalSource, /title\?: string;/);
  assert.match(modalSource, /ariaLabel\?: string;/);
  assert.match(modalSource, /aria-labelledby=\{title \? titleId : undefined\}/);
  assert.match(modalSource, /aria-label=\{title \? undefined : ariaLabel \?\? "对话框"\}/);
  assert.match(modalSource, /admin-modal__header\$\{title \? "" : " is-titleless"\}/);
  assert.match(modalSource, /\{title && <span id=\{titleId\}>\{title\}<\/span>\}/);
});
