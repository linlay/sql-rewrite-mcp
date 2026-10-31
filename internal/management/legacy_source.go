package management

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// legacySource is used only by the explicit, offline migration.
type legacySource struct{ DB *sql.DB }

func legacyRows(ctx context.Context, db *sql.DB, query string, scan func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err = scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
func (s *legacySource) load(ctx context.Context) (map[string]any, error) {
	var count int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM SOURCE_CONFIG").Scan(&count); err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, errors.New("旧业务管理库必须恰有一个 SOURCE_CONFIG")
	}
	var id, raw string
	if err := s.DB.QueryRowContext(ctx, "SELECT ID_,DATA_ FROM SOURCE_CONFIG").Scan(&id, &raw); err != nil {
		return nil, err
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, err
	}
	if d == nil || d["id"] != id {
		return nil, errors.New("旧数据库配置 ID 不一致")
	}
	cfg, ok := d["config"].(map[string]any)
	if !ok {
		cfg = map[string]any{}
		d["config"] = cfg
	}
	tables := []any{}
	err := legacyRows(ctx, s.DB, "SELECT TABLE_NAME_,SQL_,DATA_ FROM QUERY_POLICY ORDER BY TABLE_NAME_", func(rows *sql.Rows) error {
		var name, query, raw string
		if err := rows.Scan(&name, &query, &raw); err != nil {
			return err
		}
		var policy map[string]any
		if err := json.Unmarshal([]byte(raw), &policy); err != nil {
			return err
		}
		if policy == nil || policy["table"] != name {
			return errors.New("旧表权限标识不一致")
		}
		text, _ := policy["sql"].(string)
		if text != query {
			return errors.New("旧表权限 SQL 与记录内容不一致")
		}
		tables = append(tables, policy)
		return nil
	})
	if err != nil {
		return nil, err
	}
	cfg["tables"] = tables
	fragments := map[string]any{}
	if err = legacyRows(ctx, s.DB, "SELECT NAME_,SQL_ FROM QUERY_FRAGMENT ORDER BY NAME_", func(rows *sql.Rows) error {
		var name, text string
		if err := rows.Scan(&name, &text); err != nil {
			return err
		}
		fragments[name] = text
		return nil
	}); err != nil {
		return nil, err
	}
	cfg["fragments"] = fragments
	for field, purpose := range map[string]string{"connection": "READ", "write_connection": "WRITE"} {
		var raw string
		err := s.DB.QueryRowContext(ctx, "SELECT CONFIG_JSON_ FROM CONNECTION_CONFIG WHERE PURPOSE_=?", purpose).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var connection any
		if err = json.Unmarshal([]byte(raw), &connection); err != nil {
			return nil, err
		}
		d[field] = connection
	}
	actions := []any{}
	if err = legacyRows(ctx, s.DB, "SELECT NAME_,TITLE_,DESCRIPTION_,CONFIRM_,PARAMETER_JSON_,SCRIPT_ FROM ACTION_DEFINITION ORDER BY rowid", func(rows *sql.Rows) error {
		var name, title, description, parameters, script string
		var confirm bool
		if err := rows.Scan(&name, &title, &description, &confirm, &parameters, &script); err != nil {
			return err
		}
		var params any
		if err := json.Unmarshal([]byte(parameters), &params); err != nil {
			return err
		}
		actions = append(actions, map[string]any{"name": name, "title": title, "description": description, "confirm": confirm, "parameters": params, "script": script})
		return nil
	}); err != nil {
		return nil, err
	}
	if len(actions) > 0 {
		d["actions"] = actions
	}
	return d, nil
}
