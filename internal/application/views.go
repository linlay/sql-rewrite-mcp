package application

import (
	"context"
	"datascope-server/internal/guard"
	"datascope-server/internal/management"
	"datascope-server/internal/view"
	"fmt"
)

func (s *Service) ConfigureViews(ctx context.Context, in Input) (any, error) {
	if err := guard.Require(ctx, s.Management, in.DatabaseID, "view.write"); err != nil {
		return nil, err
	}
	if err := guard.Require(ctx, s.Management, in.DatabaseID, "config.read"); err != nil {
		return nil, err
	}
	if in.Database == nil || in.Database.ID != in.DatabaseID {
		return nil, fmt.Errorf("需要标识一致的 database")
	}
	d, err := s.Catalog.Get(in.DatabaseID)
	if err != nil {
		return nil, err
	}
	if d.Revision != in.Database.Revision {
		return nil, fmt.Errorf("配置已变更，请刷新后重试")
	}
	d.Views, d.QueryViewID = in.Database.Views, in.Database.QueryViewID
	if err = s.Catalog.Save(ctx, *d); err != nil {
		return nil, err
	}
	d, err = s.Catalog.Get(in.DatabaseID)
	if err != nil {
		return nil, err
	}
	return s.DatabaseView(ctx, *d), nil
}

// ReadView returns only published resources in the caller's own database realm.
// It deliberately contains no rendered business data or credentials.
func (s *Service) ReadView(ctx context.Context, database, id string) (*view.Definition, error) {
	a, ok := guard.Business(ctx)
	if !ok || a.DatabaseID != database || !a.Active {
		return nil, management.ErrDenied
	}
	if err := guard.Require(ctx, s.Management, database, "query"); err != nil {
		if err = guard.Require(ctx, s.Management, database, "action"); err != nil {
			return nil, err
		}
	}
	d, err := s.Catalog.Get(database)
	if err != nil {
		return nil, err
	}
	if !d.MCPEnabled {
		return nil, management.ErrDenied
	}
	return d.View(id, true)
}
