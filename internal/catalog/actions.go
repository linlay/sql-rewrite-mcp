package catalog

import (
	"context"
	"fmt"
	"reflect"

	"datascope-server/internal/action"

	"datascope-server/internal/management"
	"datascope-server/internal/store"
)

func validateActions(d Database) error {
	if err := action.ValidateLibraries(d.ActionLibraries); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, a := range d.Actions {
		if seen[a.Name] {
			return fmt.Errorf("action 名称重复: %s", a.Name)
		}
		seen[a.Name] = true
		if e := a.Validate(d.ActionLibraries...); e != nil {
			return fmt.Errorf("action %s: %w", a.Name, e)
		}
	}
	if d.Driver == "sqlite" {
		if d.WriteConnection != nil {
			return fmt.Errorf("SQLite 不接受写入网络账号")
		}
		return nil
	}
	if d.WriteConnection == nil {
		if d.WriteEnabled {
			return fmt.Errorf("启用写入需要独立的写入账号")
		}
		return nil
	}
	if e := store.ValidateConnection(d.Driver, d.WriteConnection); e != nil {
		return e
	}
	if d.Connection == nil {
		return fmt.Errorf("缺少查询连接")
	}
	a, b := *d.Connection, *d.WriteConnection
	a.Username = b.Username
	a.PasswordEnv = b.PasswordEnv
	a.CredentialID = b.CredentialID
	if !reflect.DeepEqual(a, b) {
		return fmt.Errorf("写入账号必须指向同一个数据库、schema 和 TLS 配置")
	}
	if d.WriteConnection.CredentialID != "" && d.WriteConnection.CredentialID != management.WriteCredentialID(d.ID) {
		return fmt.Errorf("写入凭据引用无效")
	}
	return nil
}
func (c *Catalog) OpenWriter(ctx context.Context, d *Database) (*store.Store, error) {
	if !d.WriteEnabled {
		return nil, fmt.Errorf("此数据库尚未启用 action 写入")
	}
	if e := validateActions(*d); e != nil {
		return nil, e
	}
	if d.Driver == "sqlite" {
		if c.Management == nil {
			return nil, fmt.Errorf("action 需要管理存储")
		}
		if e := c.Management.ValidatePath(d.Path); e != nil {
			return nil, e
		}
		st, e := store.OpenWriter(ctx, d.Path, c.Timeout)
		if st != nil {
			st.Audit = c.Management.AuditFor(d.ID)
		}
		return st, e
	}
	if d.WriteConnection == nil {
		return nil, fmt.Errorf("缺少写入账号")
	}
	var st *store.Store
	var e error
	if d.WriteConnection.CredentialID != "" {
		password, err := c.Management.DatabasePassword(ctx, d.WriteConnection.CredentialID)
		if err != nil {
			return nil, err
		}
		st, e = store.OpenConnectionWithPassword(ctx, d.Driver, "", d.WriteConnection, password, c.Timeout)
	} else {
		st, e = store.OpenConnection(ctx, d.Driver, "", d.WriteConnection, c.Timeout)
	}
	if st != nil {
		st.Audit = c.Management.AuditFor(d.ID)
	}
	return st, e
}
