package management

import (
	"encoding/json"
	"errors"
)

func canonicalKnowledgeJSON(raw string) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return "", err
	}
	if fields == nil {
		return "", errors.New("知识记录必须是 JSON 对象")
	}
	var values struct {
		Description *string `json:"description"`
		Summary     *string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return "", err
	}
	description := ""
	if values.Description != nil {
		description = *values.Description
	} else if values.Summary != nil {
		description = *values.Summary
	}
	fields["description"], _ = json.Marshal(description)
	delete(fields, "summary")
	delete(fields, "tables")
	data, err := json.Marshal(fields)
	return string(data), err
}
