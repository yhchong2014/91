package tagging

import (
	"strings"
	"unicode"
)

// Rule 是单个标签的匹配规则，持久化在 tags.match_rules JSON 列。
//
//   - Keywords：子串匹配。只要标题/文件名/作者/目录名包含该词就算命中。
//   - MatchAVCode：识别文本中的番号（如 ABP-123、FC2-PPV-1234567）。通常由 AV
//     归并标签开启；其它标签一般不需要。
//   - AVCodePrefixes：MatchAVCode 开启时使用的车牌前缀列表；为空时使用内置列表。
type Rule struct {
	Keywords       []string `json:"keywords,omitempty"`
	MatchAVCode    bool     `json:"matchAvCode,omitempty"`
	AVCodePrefixes []string `json:"avCodePrefixes,omitempty"`
}

// IsEmpty 表示该规则没有任何显式配置（调用方可用 label+旧版 aliases 兜底）。
func (r Rule) IsEmpty() bool {
	return len(r.Keywords) == 0 && len(r.AVCodePrefixes) == 0 && !r.MatchAVCode
}

// RuleFromAliases 把"标签名 + 旧版别名"转换成包含词规则。
func RuleFromAliases(label string, aliases []string) Rule {
	var rule Rule
	seen := map[string]struct{}{}
	for _, candidate := range append([]string{label}, aliases...) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		key := strings.ToLower(candidate)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		rule.Keywords = append(rule.Keywords, candidate)
	}
	return rule
}

// TagRule 是编译输入：一个标签名及其规则。
type TagRule struct {
	Label string
	Rule  Rule
}

// Field 是一段带名字的待匹配文本（如 标题/文件名/作者/目录）。
type Field struct {
	Name string
	Text string
}

// Match 是一次命中：Label 命中的标签，Field/Term 记录证据（在哪个字段命中了哪个词）。
type Match struct {
	Label string
	Field string
	Term  string
}

// Evidence 返回可读的命中证据，如 "文件名:翘臀"。
func (m Match) Evidence() string {
	if m.Field == "" {
		return m.Term
	}
	return m.Field + ":" + m.Term
}

type compiledTerm struct {
	raw     string // 原词（用于证据展示）
	lower   string
	compact string
}

type compiledRule struct {
	label       string
	keywords    []compiledTerm // 子串匹配
	matchAVCode bool
	avCodes     *AVCodeMatcher
}

// Matcher 是把全部标签规则一次性编译后的匹配器。构建一次可对任意多段文本
// 复用，避免旧实现里"每个文件查一遍全量标签"的开销。
type Matcher struct {
	rules []compiledRule
}

// NewMatcher 编译标签规则。空规则的标签会被跳过（调用方应先用 RuleFromAliases 兜底）。
func NewMatcher(tagRules []TagRule) *Matcher {
	m := &Matcher{rules: make([]compiledRule, 0, len(tagRules))}
	seen := map[string]struct{}{}
	for _, tr := range tagRules {
		label := strings.TrimSpace(tr.Label)
		if label == "" {
			continue
		}
		key := strings.ToLower(label)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		cr := compiledRule{label: label, matchAVCode: tr.Rule.MatchAVCode}
		if cr.matchAVCode {
			prefixes := tr.Rule.AVCodePrefixes
			if len(prefixes) == 0 {
				prefixes = DefaultAVCodePrefixes()
			}
			cr.avCodes = NewAVCodeMatcher(prefixes)
		}
		for _, kw := range tr.Rule.Keywords {
			term, ok := compileTerm(kw)
			if !ok {
				continue
			}
			cr.keywords = append(cr.keywords, term)
		}
		if len(cr.keywords) == 0 && !cr.matchAVCode {
			continue
		}
		m.rules = append(m.rules, cr)
	}
	return m
}

// Labels 返回编译进匹配器的全部标签名（按规则顺序）。
func (m *Matcher) Labels() []string {
	out := make([]string, 0, len(m.rules))
	for _, r := range m.rules {
		out = append(out, r.label)
	}
	return out
}

// Match 依次在各字段上运行全部规则，返回去重后的命中列表（每个标签只保留
// 第一处证据）。字段顺序即证据优先级，调用方应把"标题"放在最前。
func (m *Matcher) Match(fields ...Field) []Match {
	if m == nil || len(m.rules) == 0 {
		return nil
	}
	norms := make([]normText, 0, len(fields))
	for _, f := range fields {
		norms = append(norms, normalizeText(f.Text))
	}
	var out []Match
	matched := map[string]struct{}{}
	for _, rule := range m.rules {
		for i, f := range fields {
			if strings.TrimSpace(f.Text) == "" {
				continue
			}
			term, ok := rule.matchField(f.Text, norms[i])
			if !ok {
				continue
			}
			if _, dup := matched[strings.ToLower(rule.label)]; !dup {
				matched[strings.ToLower(rule.label)] = struct{}{}
				out = append(out, Match{Label: rule.label, Field: f.Name, Term: term})
			}
			break
		}
	}
	return out
}

// MatchLabels 是 Match 的便捷包装：单段匿名文本，只要标签名列表。
func (m *Matcher) MatchLabels(text string) []string {
	matches := m.Match(Field{Text: text})
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match.Label)
	}
	return out
}

func (r compiledRule) matchField(raw string, norm normText) (string, bool) {
	if r.matchAVCode {
		if code := r.avCodes.Find(raw); code != "" {
			return code, true
		}
	}
	if len(r.keywords) == 0 {
		return "", false
	}
	for _, kw := range r.keywords {
		if strings.Contains(norm.lower, kw.lower) || strings.Contains(norm.compact, kw.compact) {
			return kw.raw, true
		}
	}
	return "", false
}

// ---------- 文本归一化 ----------

type normText struct {
	lower   string
	compact string
}

func normalizeText(s string) normText {
	lower := strings.ToLower(s)
	return normText{
		lower:   lower,
		compact: compactText(lower),
	}
}

func compileTerm(s string) (compiledTerm, bool) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return compiledTerm{}, false
	}
	lower := strings.ToLower(raw)
	compact := compactText(lower)
	if compact == "" {
		return compiledTerm{}, false
	}
	return compiledTerm{raw: raw, lower: lower, compact: compact}, true
}

func compactText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
