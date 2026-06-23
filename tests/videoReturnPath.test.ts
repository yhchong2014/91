import assert from "node:assert/strict";
import test from "node:test";

import {
  normalizeVideoReturnPath,
  routeToPath,
} from "../src/lib/videoReturnPath.ts";

const ORIGIN = "http://66.92.50.106:9191";

test("builds a return path from router location parts", () => {
  assert.equal(
    routeToPath({ pathname: "/list", search: "?tag=AV", hash: "#top" }),
    "/list?tag=AV#top"
  );
});

test("accepts public same-origin pages as video delete return paths", () => {
  assert.equal(normalizeVideoReturnPath("/", ORIGIN), "/");
  assert.equal(
    normalizeVideoReturnPath("/list?q=test&page=2", ORIGIN),
    "/list?q=test&page=2"
  );
});

test("rejects unsafe video delete return paths", () => {
  assert.equal(normalizeVideoReturnPath("/video/abc", ORIGIN), null);
  assert.equal(normalizeVideoReturnPath("/login", ORIGIN), null);
  assert.equal(
    normalizeVideoReturnPath("https://example.com/list", ORIGIN),
    null
  );
});
