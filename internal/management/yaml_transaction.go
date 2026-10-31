package management

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type registryChange struct{ ID, Name, Route string }
type configurationChange struct {
	Sources        map[string]*sourceState
	Registry       []registryChange
	Authentication *AuthConfig
}

func (s *Store) pendingPath() string { return s.Path + ".configuration-pending" }

// A durable intent coordinates YAML generations with the small global SQLite
// catalog/auth transaction. Readers in either process finish recovery first.
// This is an ephemeral commit journal, never an alternate configuration store.
func (s *Store) recoverPending(ctx context.Context) error {
	if _, err := os.Stat(s.pendingPath()); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return fileLock(ctx, s.Path+".config.lock", func() error { return s.recoverPendingLocked(ctx) })
}
func (s *Store) recoverPendingLocked(ctx context.Context) error {
	data, err := os.ReadFile(s.pendingPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var change configurationChange
	if err = json.Unmarshal(data, &change); err != nil {
		return errors.New("配置提交日志损坏，请恢复一致性备份")
	}
	return s.applyChange(ctx, change)
}
func (s *Store) commitChange(ctx context.Context, change configurationChange) error {
	// Validate every generation before the durable decision; validation failures
	// leave all existing configuration untouched.
	for id, state := range change.Sources {
		if !sourceID.MatchString(id) {
			return errors.New("非法数据源 ID")
		}
		parent := filepath.Join(filepath.Dir(s.Path), "databases", id)
		if err := os.MkdirAll(parent, 0700); err != nil {
			return err
		}
		stage, err := stageState(filepath.Join(parent, "management"), state)
		if err != nil {
			return err
		}
		if err = os.RemoveAll(stage); err != nil {
			return err
		}
	}
	data, err := json.Marshal(change)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.Path), ".configuration-intent-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), s.pendingPath()); err != nil {
		return err
	}
	if err = syncDir(filepath.Dir(s.Path)); err != nil {
		return err
	}
	if err = s.applyChange(ctx, change); err != nil {
		return fmt.Errorf("配置提交待恢复，下次读写将继续完成: %w", err)
	}
	return nil
}
func (s *Store) applyChange(ctx context.Context, change configurationChange) error {
	for id, state := range change.Sources {
		if !sourceID.MatchString(id) {
			return errors.New("配置提交日志包含非法数据源")
		}
		parent := filepath.Join(filepath.Dir(s.Path), "databases", id)
		if err := os.MkdirAll(parent, 0700); err != nil {
			return err
		}
		if err := fileLock(ctx, filepath.Join(parent, ".management.lock"), func() error {
			path := filepath.Join(parent, "management")
			if err := recoverPublication(path); err != nil {
				return err
			}
			return publishState(path, state)
		}); err != nil {
			return err
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range change.Registry {
		if _, err = tx.ExecContext(ctx, "DELETE FROM SOURCE_REGISTRY WHERE DATABASE_ID_=?", r.ID); err != nil {
			return err
		}
	}
	for _, r := range change.Registry {
		if _, err = tx.ExecContext(ctx, "INSERT INTO SOURCE_REGISTRY(DATABASE_ID_,NAME_,MCP_PATH_,STATE_) VALUES(?,?,?,'ACTIVE') ON CONFLICT(DATABASE_ID_) DO UPDATE SET NAME_=excluded.NAME_,MCP_PATH_=excluded.MCP_PATH_", r.ID, r.Name, r.Route); err != nil {
			return err
		}
	}
	if change.Authentication != nil {
		if err = writeAuthTx(ctx, tx, *change.Authentication); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if err = os.Remove(s.pendingPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDir(filepath.Dir(s.Path))
}
func writeAuthTx(ctx context.Context, tx *sql.Tx, cfg AuthConfig) error {
	for _, table := range []string{"AUTH_BINDING", "AUTH_POLICY", "AUTH_PROVIDER"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return err
		}
	}
	for _, p := range cfg.Providers {
		raw, _ := json.Marshal(p)
		if _, err := tx.ExecContext(ctx, "INSERT INTO AUTH_PROVIDER VALUES(?,?,?,?)", p.ID, p.Name, p.Type, string(raw)); err != nil {
			return err
		}
	}
	for _, p := range cfg.Policies {
		raw, _ := json.Marshal(p)
		if _, err := tx.ExecContext(ctx, "INSERT INTO AUTH_POLICY VALUES(?,?,?,?,?)", p.ID, p.Name, p.Mode, p.ProviderID, string(raw)); err != nil {
			return err
		}
	}
	for _, b := range cfg.Bindings {
		if b.TargetType == "DATABASE" {
			continue
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO AUTH_BINDING VALUES(?,?,?)", b.TargetType, b.TargetID, b.PolicyID); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO AUTH_SETTING VALUES(1,?,?) ON CONFLICT(ID_) DO UPDATE SET REVISION_=excluded.REVISION_,DEFAULT_POLICY_ID_=excluded.DEFAULT_POLICY_ID_", cfg.Revision+1, cfg.DefaultPolicyID)
	return err
}
