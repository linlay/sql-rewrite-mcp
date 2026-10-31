package management

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// CloneSnapshot exposes configuration through the storage boundary, never
// accounts, grants, credentials or the filesystem's private state format.
type CloneSnapshot struct {
	Database  json.RawMessage
	Knowledge []Record
	Token     string
}

type CloneReplacement struct {
	Database  json.RawMessage
	Knowledge []Record // nil preserves knowledge; a non-nil empty slice clears it.
}

// CloneConfiguration serializes preview/commit with every management writer.
// The callback must not save configuration. A commit publishes one complete
// target YAML generation using the same recovery mechanism as other saves.
func (s *Store) CloneConfiguration(ctx context.Context, sourceID, targetID, sourceToken, targetToken string, commit bool, fn func(CloneSnapshot, CloneSnapshot) (CloneReplacement, error)) error {
	if sourceID == targetID || sourceID == "" || targetID == "" {
		return errors.New("来源库和目标库必须不同且不能为空")
	}
	return fileLock(ctx, s.Path+".config.lock", func() error {
		if err := s.recoverPendingLocked(ctx); err != nil {
			return err
		}
		source, err := s.Source(ctx, sourceID)
		if err != nil {
			return err
		}
		target, err := s.Source(ctx, targetID)
		if err != nil {
			return err
		}
		var src, dst *sourceState
		if err = source.withState(ctx, false, func(st *sourceState) error { src = st; return nil }); err != nil {
			return err
		}
		if err = target.withState(ctx, false, func(st *sourceState) error { dst = st; return nil }); err != nil {
			return err
		}
		snapshot := func(st *sourceState) CloneSnapshot {
			raw, _ := json.Marshal(st)
			db, _ := json.Marshal(st.Database)
			return CloneSnapshot{Database: db, Knowledge: append([]Record{}, st.Records["knowledge"]...), Token: fmt.Sprintf("%x", sha256.Sum256(raw))}
		}
		a, b := snapshot(src), snapshot(dst)
		if commit && (sourceToken == "" || targetToken == "" || a.Token != sourceToken || b.Token != targetToken) {
			return errors.New("来源库或目标库配置已更新，请重新预览后确认")
		}
		replacement, err := fn(a, b)
		if err != nil || !commit {
			return err
		}
		var next map[string]any
		if err = json.Unmarshal(replacement.Database, &next); err != nil {
			return err
		}
		if next == nil {
			return errors.New("缺少目标配置")
		}
		// Defense in depth: a clone cannot modify connections, identities,
		// authentication, MCP routes, enable flags or any new future setting.
		allowed := map[string]bool{"config": true, "discovery": true, "actions": true, "revision": true}
		for k, value := range next {
			if !allowed[k] && !reflect.DeepEqual(value, dst.Database[k]) {
				return fmt.Errorf("克隆不能修改目标配置 %s", k)
			}
		}
		for k, value := range dst.Database {
			if !allowed[k] && !reflect.DeepEqual(value, next[k]) {
				return fmt.Errorf("克隆不能移除目标配置 %s", k)
			}
		}
		if err = normalizeDatabase(next); err != nil {
			return err
		}
		dst.Database = next
		if replacement.Knowledge != nil {
			for _, record := range replacement.Knowledge {
				var k struct {
					DatabaseID string `json:"database_id"`
					ID         string `json:"id"`
				}
				if err = json.Unmarshal([]byte(record.Data), &k); err != nil {
					return err
				}
				if k.DatabaseID != targetID || record.Key != targetID+"/"+k.ID {
					return errors.New("克隆知识归属无效")
				}
			}
			dst.Records["knowledge"] = replacement.Knowledge
		}
		if err = target.withState(ctx, true, func(st *sourceState) error { *st = *dst; return nil }); err != nil {
			return err
		}
		s.auditChange(ctx, target.Audit, targetID, "database.clone", map[string]any{"source_database_id": sourceID, "target_database_id": targetID})
		return nil
	})
}
