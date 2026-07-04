package tagging

import (
	"reflect"
	"testing"
)

func TestMatcherKeywordSubstring(t *testing.T) {
	m := NewMatcher([]TagRule{
		{Label: "臀", Rule: Rule{Keywords: []string{"翘臀", "蜜桃臀", "臀"}}},
	})
	if got := m.MatchLabels("白丝女友的翘臀特写"); !reflect.DeepEqual(got, []string{"臀"}) {
		t.Fatalf("labels = %#v, want 臀", got)
	}
	if got := m.MatchLabels("健身臀部教程"); !reflect.DeepEqual(got, []string{"臀"}) {
		t.Fatalf("单字包含词未按子串命中: %#v", got)
	}
}

func TestMatcherShortKeywordUsesSubstring(t *testing.T) {
	m := NewMatcher([]TagRule{
		{Label: "AV", Rule: Rule{Keywords: []string{"av"}}},
	})
	if got := m.MatchLabels("wave surfing travel"); !reflect.DeepEqual(got, []string{"AV"}) {
		t.Fatalf("短词包含词未按子串命中: %#v", got)
	}
}

func TestMatcherKeywordMatchesWithoutExcludes(t *testing.T) {
	m := NewMatcher([]TagRule{
		{Label: "人妻", Rule: Rule{Keywords: []string{"人妻", "老婆", "太太"}}},
	})
	if got := m.MatchLabels("老婆饼制作教学"); !reflect.DeepEqual(got, []string{"人妻"}) {
		t.Fatalf("正常命中丢失: %#v", got)
	}
}

func TestMatcherCompactCrossDelimiter(t *testing.T) {
	m := NewMatcher([]TagRule{
		{Label: "奶子", Rule: Rule{Keywords: []string{"big boobs"}}},
	})
	if got := m.MatchLabels("big-boobs collection"); !reflect.DeepEqual(got, []string{"奶子"}) {
		t.Fatalf("compact 跨分隔符匹配失败: %#v", got)
	}
}

func TestMatcherFieldsEvidence(t *testing.T) {
	m := NewMatcher([]TagRule{
		{Label: "丝袜", Rule: Rule{Keywords: []string{"黑丝"}}},
	})
	matches := m.Match(
		Field{Name: "标题", Text: "普通标题"},
		Field{Name: "文件名", Text: "黑丝女友.mp4"},
	)
	if len(matches) != 1 {
		t.Fatalf("matches = %#v, want 1", matches)
	}
	if matches[0].Field != "文件名" || matches[0].Term != "黑丝" {
		t.Fatalf("evidence = %q, want 文件名:黑丝", matches[0].Evidence())
	}
}

func TestMatcherAVCodeRule(t *testing.T) {
	m := NewMatcher([]TagRule{
		{Label: "AV", Rule: Rule{MatchAVCode: true}},
	})
	cases := []struct {
		text string
		term string
	}{
		{"FC2PPV-3259498.mp4", "FC2PPV-3259498"},
		{"hhd800.com@FC2-PPV-4745791.mp4", "FC2-PPV-4745791"},
		{"MIMK-284D.mp4", "MIMK-284D"},
	}
	for _, c := range cases {
		matches := m.Match(Field{Name: "文件名", Text: c.text})
		if len(matches) != 1 || matches[0].Label != "AV" {
			t.Fatalf("Match(%q) = %#v, want AV", c.text, matches)
		}
		if matches[0].Term != c.term {
			t.Fatalf("Match(%q) term = %q, want %q", c.text, matches[0].Term, c.term)
		}
	}
	if got := m.MatchLabels("没有番号的标题"); len(got) != 0 {
		t.Fatalf("误报番号: %#v", got)
	}
	for _, text := range []string{"[44x.me]IDBD-786 中文字幕.mp4", "Carib-041515-853-FHD.mp4", "cc-1750027.mp4", "390JAC-233.mp4"} {
		if got := m.MatchLabels(text); len(got) != 0 {
			t.Fatalf("MatchLabels(%q) = %#v, want no AV match", text, got)
		}
	}
}

func TestMatcherAVCodeCustomPrefixes(t *testing.T) {
	m := NewMatcher([]TagRule{
		{Label: "AV", Rule: Rule{MatchAVCode: true, AVCodePrefixes: append(DefaultAVCodePrefixes(), "IDBD", "JAC")}},
	})
	for _, text := range []string{"[44x.me]IDBD-786 中文字幕.mp4", "JAC-233.mp4"} {
		if got := m.MatchLabels(text); !reflect.DeepEqual(got, []string{"AV"}) {
			t.Fatalf("MatchLabels(%q) = %#v, want [AV]", text, got)
		}
	}
}

func TestMatcherAVCodeKnownPrefixes(t *testing.T) {
	m := NewMatcher([]TagRule{
		{Label: "AV", Rule: Rule{MatchAVCode: true}},
	})
	prefixes := []string{
		"SSNI", "SSIS", "SNIS", "SOE", "IPX", "IPZ", "IPTD",
		"ABP", "ABW", "ONEZ", "MIDE", "MIDV", "MIAA", "MIMK",
		"ATID", "SHKD", "RBD", "FSDSS", "STAR", "MUD", "HND",
		"HMN", "WANZ", "CREAM", "VAGU", "JUL", "JUQ", "JUR",
		"OBA", "NKK", "JUFE", "FC2PPV", "SIRO", "300MIUM",
		"259LUXU", "CAWD", "SABA", "ZIZ", "PPPD", "EBOD",
		"EBWH", "BOBB", "CJOD", "PRED", "VEC", "IBW", "LBJ",
		"IMPA", "DDK", "MVG", "HUNT", "NTRD", "SDDE", "DASS",
		"MKMP", "BF", "BFDM",
	}
	for _, prefix := range prefixes {
		code := prefix + "-101"
		if prefix == "FC2PPV" {
			code = "FC2PPV-4768873"
		}
		if got := m.MatchLabels(code + ".mp4"); !reflect.DeepEqual(got, []string{"AV"}) {
			t.Fatalf("MatchLabels(%q) = %#v, want [AV]", code, got)
		}
	}
}

func TestRuleFromAliasesUsesKeywords(t *testing.T) {
	rule := RuleFromAliases("臀", []string{"翘臀", "ass", "屁股"})
	want := []string{"臀", "翘臀", "ass", "屁股"}
	if !reflect.DeepEqual(rule.Keywords, want) {
		t.Fatalf("keywords = %#v, want %#v", rule.Keywords, want)
	}
}

func TestSeriesExtraction(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"ABP-123 完整版", "ABP"},
		{"FC2-PPV-1234567", "FC2PPV"},
		{"FC2PPV-4162750", "FC2PPV"},
		{"hhd800.com@FC2-PPV-4745791", "FC2PPV"},
		{"MIMK-284D", "MIMK"},
		{"300MIUM-873", "300MIUM"},
		{"259LUXU-1823", "259LUXU"},
		{"[44x.me]idbd-786", ""},
		{"390JAC-233", ""},
		{"Carib-041515-853-FHD", ""},
		{"cc-1750027", ""},
		{"没有番号", ""},
	}
	for _, c := range cases {
		if got := SeriesInText(c.text); got != c.want {
			t.Errorf("SeriesInText(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestAutoSeriesExtractionUsesCuratedPrefixes(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{"ABP-123", "ABP"},
		{"FC2PPV-4162750", "FC2PPV"},
		{"ADN-778-FHD", ""},
		{"390JAC-233", ""},
		{"FC2-1234567", ""},
		{"cc-1750027", ""},
		{"IMG_1234", ""},
		{"FINAL168045", ""},
		{"MOV202405", ""},
	}
	for _, c := range cases {
		if got := AutoSeriesOf(c.code); got != c.want {
			t.Errorf("AutoSeriesOf(%q) = %q, want %q", c.code, got, c.want)
		}
	}
}

func TestIsAVCode(t *testing.T) {
	for _, code := range []string{
		"ABP-123",
		"FC2-PPV-1234567",
		"FC2PPV-3259498",
		"MIMK-284D",
		"300MIUM-873",
		"259LUXU-1823",
	} {
		if !IsAVCode(code) {
			t.Errorf("IsAVCode(%q) = false, want true", code)
		}
	}
	for _, text := range []string{"普通标题", "av", "2024-01", "cc-1750027", "Carib-041515-853-FHD", "390JAC-233", "ADN-778-FHD"} {
		if IsAVCode(text) {
			t.Errorf("IsAVCode(%q) = true, want false", text)
		}
	}
}
