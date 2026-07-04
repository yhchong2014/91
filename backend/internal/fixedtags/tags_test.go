package fixedtags

import (
	"testing"

	"github.com/video-site/backend/internal/tagging"
)

func packMatcher(t *testing.T) *tagging.Matcher {
	t.Helper()
	var rules []tagging.TagRule
	for _, tag := range All() {
		rules = append(rules, tagging.TagRule{Label: tag.Label, Rule: tag.Rule})
	}
	return tagging.NewMatcher(rules)
}

func TestPackMatchesConfiguredTerms(t *testing.T) {
	m := packMatcher(t)
	cases := map[string]string{
		"大一学妹研究生": "女大",
		"大奶揉胸揉奶":  "奶子",
		"少妇已婚":    "人妻",
		"水手服空姐护士": "制服",
		"翘臀蜜桃臀":   "美臀",
		"口活深喉吞精":  "口交",
		"后入":      "后入",
	}
	for text, want := range cases {
		got := m.MatchLabels(text)
		assertLabelSet(t, got, map[string]bool{want: true})
	}
}

func TestPackNoLongerMatchesRemovedDefaults(t *testing.T) {
	m := packMatcher(t)
	if got := m.MatchLabels("backshot oral-sex big boobs big ass wife college student 背后式 揉乳 大学生"); len(got) != 0 {
		t.Fatalf("removed defaults should not match: %#v", got)
	}
}

func TestPackDoesNotUseHighRiskSingleCharTerms(t *testing.T) {
	m := packMatcher(t)
	// 内置规则不配置 "奶"/"胸" 这类高误伤单字，避免子串误伤。
	if got := m.MatchLabels("牛奶广告拍摄花絮"); len(got) != 0 {
		t.Fatalf("误伤: %#v", got)
	}
}

func TestPackBuiltinTags(t *testing.T) {
	m := packMatcher(t)
	cases := map[string]string{
		"JK 制服少女":  "制服",
		"高冷空姐":     "制服",
		"SSNI-001": "AV",
	}
	for text, want := range cases {
		got := m.MatchLabels(text)
		found := false
		for _, label := range got {
			if label == want {
				found = true
			}
		}
		if !found {
			t.Errorf("MatchLabels(%q) = %#v, want contains %q", text, got, want)
		}
	}
}

func TestPackAVDoesNotMatchPlainAliasText(t *testing.T) {
	m := packMatcher(t)
	for _, text := range []string{"经典 AV 合集", "JAV合集", "番号整理", "番號整理"} {
		if got := m.MatchLabels(text); len(got) != 0 {
			t.Fatalf("MatchLabels(%q) = %#v, want none", text, got)
		}
	}
}

func TestAllHasNoDuplicateLabels(t *testing.T) {
	seen := map[string]bool{}
	for _, tag := range All() {
		if seen[tag.Label] {
			t.Fatalf("duplicate builtin label %q", tag.Label)
		}
		seen[tag.Label] = true
		if tag.Source != SourceBuiltin {
			t.Fatalf("tag %q source = %q, want %q", tag.Label, tag.Source, SourceBuiltin)
		}
		if tag.Rule.IsEmpty() {
			t.Fatalf("tag %q has empty rule", tag.Label)
		}
	}
	for _, label := range Labels {
		if !seen[label] {
			t.Fatalf("core builtin label %q missing from All()", label)
		}
	}
	if len(seen) != len(Labels) {
		t.Fatalf("builtin labels = %#v, want exactly %#v", seen, Labels)
	}
}

func assertLabelSet(t *testing.T, got []string, want map[string]bool) {
	t.Helper()
	gotSet := map[string]bool{}
	for _, label := range got {
		gotSet[label] = true
	}
	for label := range want {
		if !gotSet[label] {
			t.Errorf("missing label %q in %#v", label, got)
		}
	}
}
