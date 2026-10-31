package management

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// This parser belongs only to the offline SQLite upgrade. Runtime policies
// contain complete SQL; identity placeholders remain bind variables unchanged.
var legacyFragmentRef = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

func migrateLegacyDatabase(d map[string]any) error {
	if cfg, ok := d["config"].(map[string]any); ok {
		fragments, _ := cfg["fragments"].(map[string]any)
		if values, ok := cfg["fragments"].(map[string]string); ok {
			fragments = map[string]any{}
			for name, body := range values {
				fragments[name] = body
			}
		}
		var expand func(string, []string) (string, error)
		expand = func(text string, stack []string) (string, error) {
			var failure error
			out := legacyFragmentRef.ReplaceAllStringFunc(text, func(ref string) string {
				if failure != nil {
					return ref
				}
				name := legacyFragmentRef.FindStringSubmatch(ref)[1]
				body, ok := fragments[name].(string)
				if !ok {
					failure = fmt.Errorf("未定义的片段 {{%s}}", name)
					return ref
				}
				if slices.Contains(stack, name) {
					failure = fmt.Errorf("片段循环引用: %s → %s", strings.Join(stack, " → "), name)
					return ref
				}
				var result string
				result, failure = expand(body, append(stack, name))
				return result
			})
			if failure == nil && strings.Contains(out, "{{") {
				failure = errors.New("无法解析旧 SQL 片段引用")
			}
			return out, failure
		}
		tables, _ := cfg["tables"].([]any)
		// Expand every rule before publishing any output. A missing/cyclic rule
		// must never turn into an unrestricted or omitted table permission.
		for _, table := range tables {
			p, ok := table.(map[string]any)
			if !ok {
				return errors.New("旧表权限必须是对象")
			}
			for _, field := range []string{"sql", "deny_unless"} {
				if text, ok := p[field].(string); ok {
					value, err := expand(text, nil)
					if err != nil {
						return fmt.Errorf("表 %v %s: %w", p["table"], field, err)
					}
					p[field] = value
				}
			}
		}
		delete(cfg, "fragments")
	}
	// Original named queries remain recoverable in the untouched SQLite backup.
	delete(d, "business_tools")
	return normalizeDatabase(d)
}
