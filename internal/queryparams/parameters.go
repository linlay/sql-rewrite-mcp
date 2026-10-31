// Package queryparams defines the shared direct-SQL parameter contract.
package queryparams

import (
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"strings"

	parser "github.com/rqlite/sql"
)

var namePattern = regexp.MustCompile(`^arg_[A-Za-z][A-Za-z0-9_]{0,59}$`)

func Validate(parameters map[string]any) error {
	if len(parameters) > 100 {
		return fmt.Errorf("绑定参数最多 100 个")
	}
	for name, value := range parameters {
		if !namePattern.MatchString(name) {
			return fmt.Errorf("业务参数必须使用 arg_ 前缀，不能覆盖身份参数")
		}
		switch v := value.(type) {
		case nil, string, bool:
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return fmt.Errorf("参数 %s 必须是有限数字", name)
			}
		default:
			return fmt.Errorf("参数 %s 必须是标量", name)
		}
	}
	return nil
}

// Args inspects bind nodes, never SQL text or quoted strings. The caller first
// validates the read-only statement and uses the store's native bind compiler.
func Args(query string, parameters map[string]any) ([]any, error) {
	if err := Validate(parameters); err != nil {
		return nil, err
	}
	stmt, err := parser.NewParser(strings.NewReader(query)).ParseStatement()
	if err != nil {
		return nil, err
	}
	args := []any{}
	seen := map[string]bool{}
	_, err = parser.Walk(parser.VisitFunc(func(n parser.Node) (parser.Node, error) {
		if b, ok := n.(*parser.BindExpr); ok {
			name := strings.TrimPrefix(b.Name, ":")
			value, exists := parameters[name]
			if !strings.HasPrefix(b.Name, ":") || !namePattern.MatchString(name) || !exists {
				return n, fmt.Errorf("不支持或缺少绑定参数 %s", b.Name)
			}
			if !seen[name] {
				args = append(args, sql.Named(name, value))
				seen[name] = true
			}
		}
		return n, nil
	}), stmt)
	return args, err
}
