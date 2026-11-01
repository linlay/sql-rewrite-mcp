// Package action runs administrator-authored JavaScript with static SQL templates.
package action

import (
	"datascope-server/internal/store"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"unicode/utf8"
)

type Parameter struct {
	Type        string   `json:"type" yaml:"type"`
	Description string   `json:"description" yaml:"description"`
	Optional    bool     `json:"optional,omitempty" yaml:"optional,omitempty"`
	Enum        []any    `json:"enum,omitempty" yaml:"enum,omitempty"`
	Min         *float64 `json:"min,omitempty" yaml:"min,omitempty"`
	Max         *float64 `json:"max,omitempty" yaml:"max,omitempty"`
	MaxLength   *int     `json:"max_length,omitempty" yaml:"maxLength,omitempty"`
	Pattern     string   `json:"pattern,omitempty" yaml:"pattern,omitempty"`
}
type Definition struct {
	Name        string               `json:"name" yaml:"name"`
	Title       string               `json:"title" yaml:"title"`
	Description string               `json:"description" yaml:"description"`
	Confirm     bool                 `json:"confirm" yaml:"confirm"`
	Parameters  map[string]Parameter `json:"parameters" yaml:"parameters"`
	Script      string               `json:"script" yaml:"script"`
}

var namePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

func (d Definition) Schema() map[string]any {
	props := map[string]any{}
	required := []string{}
	for n, p := range d.Parameters {
		v := map[string]any{"type": p.Type, "description": p.Description}
		if len(p.Enum) > 0 {
			v["enum"] = p.Enum
		}
		if p.Min != nil {
			v["minimum"] = *p.Min
		}
		if p.Max != nil {
			v["maximum"] = *p.Max
		}
		if p.MaxLength != nil {
			v["maxLength"] = *p.MaxLength
		}
		if p.Pattern != "" {
			v["pattern"] = p.Pattern
		}
		props[n] = v
		if !p.Optional {
			required = append(required, n)
		}
	}
	sort.Strings(required)
	return map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required}
}
func (d Definition) Validate(libraries ...Library) error {
	if !namePattern.MatchString(d.Name) || d.Title == "" || d.Description == "" || len(d.Script) == 0 || len(d.Script) > 100000 {
		return fmt.Errorf("action 需要合法名称、标题、说明和最多 100000 字节的脚本")
	}
	if len(d.Parameters) > 50 {
		return fmt.Errorf("action 最多 50 个参数")
	}
	for n, p := range d.Parameters {
		if !namePattern.MatchString(n) || !slices.Contains([]string{"string", "integer", "number", "boolean"}, p.Type) {
			return fmt.Errorf("参数 %s 的名称或类型无效", n)
		}
		if p.Min != nil && (!finite(*p.Min) || p.Type != "integer" && p.Type != "number") || p.Max != nil && (!finite(*p.Max) || p.Type != "integer" && p.Type != "number") || p.Min != nil && p.Max != nil && *p.Min > *p.Max {
			return fmt.Errorf("参数 %s 的数值范围无效", n)
		}
		if p.MaxLength != nil && (*p.MaxLength < 0 || p.Type != "string") || p.Pattern != "" && p.Type != "string" {
			return fmt.Errorf("参数 %s 的字符串约束无效", n)
		}
		if p.Pattern != "" {
			if _, e := regexp.Compile(p.Pattern); e != nil {
				return fmt.Errorf("参数 %s: %w", n, e)
			}
		}
		for _, v := range p.Enum {
			q := p
			q.Enum = nil
			if _, e := q.check(v); e != nil {
				return fmt.Errorf("参数 %s 的 enum 无效: %w", n, e)
			}
		}
	}
	_, sites, e := compileWithLibraries(d.Script, libraries, "")
	if e != nil {
		return e
	}
	for _, s := range sites {
		if e = store.ValidateActionTemplate(s.sql, s.count, s.method == "exec"); e != nil {
			return e
		}
	}
	return nil
}
func finite(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }
func (p Parameter) check(v any) (any, error) {
	switch p.Type {
	case "string":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("应为 string")
		}
		if p.MaxLength != nil && utf8.RuneCountInString(s) > *p.MaxLength {
			return nil, fmt.Errorf("超过 max_length")
		}
		if p.Pattern != "" && !regexp.MustCompile(p.Pattern).MatchString(s) {
			return nil, fmt.Errorf("不符合 pattern")
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return nil, fmt.Errorf("应为 boolean")
		}
	case "number", "integer":
		var n float64
		switch x := v.(type) {
		case json.Number:
			var e error
			n, e = x.Float64()
			if e != nil {
				return nil, e
			}
		case float64:
			n = x
		case int:
			n = float64(x)
		case int64:
			n = float64(x)
		default:
			return nil, fmt.Errorf("应为 %s", p.Type)
		}
		if !finite(n) || p.Type == "integer" && (math.Trunc(n) != n || math.Abs(n) > 9007199254740991) {
			return nil, fmt.Errorf("需要有限数字，整数必须在 JS 安全范围内")
		}
		if p.Min != nil && n < *p.Min || p.Max != nil && n > *p.Max {
			return nil, fmt.Errorf("超出 min/max 范围")
		}
		v = n
	}
	if len(p.Enum) > 0 {
		raw, _ := json.Marshal(v)
		ok := false
		for _, x := range p.Enum {
			b, _ := json.Marshal(x)
			if string(b) == string(raw) {
				ok = true
			}
		}
		if !ok {
			return nil, fmt.Errorf("不在 enum 中")
		}
	}
	return v, nil
}
func (d Definition) Arguments(args map[string]any) (map[string]any, error) {
	out := map[string]any{}
	var err error
	for n := range args {
		if _, ok := d.Parameters[n]; !ok {
			err = fmt.Errorf("未知参数 %s", n)
			break
		}
	}
	if err == nil {
		for n, p := range d.Parameters {
			v, ok := args[n]
			if !ok {
				if !p.Optional {
					err = fmt.Errorf("缺少参数 %s", n)
					break
				}
				continue
			}
			v, e := p.check(v)
			if e != nil {
				err = fmt.Errorf("参数 %s: %w", n, e)
				break
			}
			out[n] = v
		}
	}
	if err != nil {
		schema, _ := json.Marshal(d.Schema())
		return nil, fmt.Errorf("%w；完整参数格式: %s", err, schema)
	}
	return out, nil
}
