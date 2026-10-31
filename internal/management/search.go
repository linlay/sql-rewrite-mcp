package management

import (
	"context"
	"encoding/json"
	"strings"
)

// Search is derived from the current YAML generation; no disk index is created.
func (s *Store) SearchKnowledge(ctx context.Context, query string) (map[string]bool, error) {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) > 20 {
		terms = terms[:20]
	}
	if len(terms) == 0 {
		return nil, nil
	}
	records, e := s.Records(ctx, "knowledge")
	if e != nil {
		return nil, e
	}
	out := map[string]bool{}
	for _, r := range records {
		var v struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Content     string `json:"content"`
		}
		if e = json.Unmarshal([]byte(r.Data), &v); e != nil {
			return nil, e
		}
		text := strings.ToLower(v.Name + "\n" + v.Description + "\n" + v.Content)
		match := true
		for _, term := range terms {
			match = match && strings.Contains(text, term)
		}
		if match {
			out[r.Key] = true
		}
		if len(out) >= 500 {
			break
		}
	}
	return out, nil
}
