import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const tagsPageSource = readFileSync(
  new URL("../src/admin/TagsPage.tsx", import.meta.url),
  "utf8"
);

test("admin tags page limits visible tags by viewport", () => {
  assert.match(tagsPageSource, /const DESKTOP_TAGS_PAGE_SIZE = 25;/);
  assert.match(tagsPageSource, /const MOBILE_TAGS_PAGE_SIZE = 8;/);
  assert.match(tagsPageSource, /const TAGS_MOBILE_QUERY = "\(max-width: 640px\)";/);
  assert.match(tagsPageSource, /window\.matchMedia\(TAGS_MOBILE_QUERY\)/);
});

test("admin tags page renders only the current page", () => {
  assert.match(tagsPageSource, /filteredTags\.slice\(pageStartIndex, pageEndIndex\)/);
  assert.match(tagsPageSource, /pagedTags\.map\(\(tag\) =>/);
  assert.doesNotMatch(tagsPageSource, /filteredTags\.map\(\(tag\) =>/);
  assert.match(tagsPageSource, /全选本页/);
});
