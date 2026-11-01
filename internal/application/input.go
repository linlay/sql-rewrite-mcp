package application

import (
	"encoding/json"
	"fmt"
)

// DecodeInput rejects retired knowledge options on the HTTP management endpoint.
// MCP applies its own per-operation allowlist and strict nested-field decoding.
func DecodeInput(raw json.RawMessage) (Input, error) {
	var in Input
	if len(raw) == 0 {
		return in, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return in, err
	}
	for _, key := range []string{"tables", "release_knowledge_restrictions"} {
		if _, exists := fields[key]; exists {
			return in, fmt.Errorf("参数 %s 已移除；知识不关联表，读取仅需本库知识阅读权限", key)
		}
	}
	err := json.Unmarshal(raw, &in)
	return in, err
}
