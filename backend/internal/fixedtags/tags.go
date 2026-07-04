// Package fixedtags 是内置标签规则包：
//
//   - builtin 标签保留 AV、奶子、女大、人妻、后入、制服、美臀、口交。
//   - 内置标签包只在初始化/旧版迁移时写入一次；之后删除标签或包含词不会自动补回。
//
// 每个标签携带 tagging.Rule 匹配规则（包含词），首次入库时
// 写进 tags.match_rules；之后管理员在后台的修改优先，这里的默认值不会覆盖。
package fixedtags

import (
	"strings"

	"github.com/video-site/backend/internal/tagging"
)

const SourceBuiltin = "builtin"

// Tag 是一条内置标签定义。Aliases 仅作展示同义词；实际匹配走 Rule。
type Tag struct {
	Label   string
	Source  string
	Aliases []string
	Rule    tagging.Rule
}

// Labels 保留旧包变量名：当前允许保留为 builtin 的全部内置标签名。
var Labels = []string{"AV", "奶子", "女大", "人妻", "后入", "制服", "美臀", "口交"}

var builtinTags = []Tag{
	{
		Label:  "AV",
		Source: SourceBuiltin,
		Rule:   tagging.Rule{MatchAVCode: true},
	},
	{
		Label:  "奶子",
		Source: SourceBuiltin,
		Rule: tagging.Rule{
			Keywords: []string{"奶子", "大奶", "巨乳", "美乳", "大胸", "揉胸", "揉奶"},
		},
	},
	{
		Label:  "女大",
		Source: SourceBuiltin,
		Rule: tagging.Rule{
			Keywords: []string{"女大", "大一", "大二", "大三", "大四", "学妹", "学姐", "研究生"},
		},
	},
	{
		Label:  "人妻",
		Source: SourceBuiltin,
		Rule: tagging.Rule{
			Keywords: []string{"人妻", "少妇", "已婚"},
		},
	},
	{
		Label:  "后入",
		Source: SourceBuiltin,
		Rule: tagging.Rule{
			Keywords: []string{"后入"},
		},
	},
	{
		Label:  "制服",
		Source: SourceBuiltin,
		Rule: tagging.Rule{
			Keywords: []string{"制服", "水手服", "空姐", "护士", "JK制服"},
		},
	},
	{
		Label:  "美臀",
		Source: SourceBuiltin,
		Rule: tagging.Rule{
			Keywords: []string{"屁股", "翘臀", "美臀", "蜜桃臀", "大屁股"},
		},
	},
	{
		Label:  "口交",
		Source: SourceBuiltin,
		Rule: tagging.Rule{
			Keywords: []string{"口交", "口爆", "口活", "深喉", "吞精"},
		},
	},
}

// All 返回全部内置标签定义。返回副本，调用方可安全修改。
func All() []Tag {
	out := make([]Tag, len(builtinTags))
	copy(out, builtinTags)
	return out
}

// IsBuiltinLabel reports whether label is one of the current builtin labels.
func IsBuiltinLabel(label string) bool {
	label = strings.TrimSpace(label)
	for _, builtin := range Labels {
		if strings.EqualFold(label, builtin) {
			return true
		}
	}
	return false
}

// RuleFor 返回某个内置标签的默认规则；不存在时返回零值。
func RuleFor(label string) tagging.Rule {
	for _, t := range All() {
		if t.Label == label {
			return t.Rule
		}
	}
	return tagging.Rule{}
}

// AliasesFor 保留旧包函数名：返回旧版 aliases 字段的兼容值（通常为空）。
func AliasesFor(label string) []string {
	for _, t := range All() {
		if t.Label == label {
			return append([]string(nil), t.Aliases...)
		}
	}
	return nil
}
