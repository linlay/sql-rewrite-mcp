package application

import (
	"context"
	"errors"

	"datascope-server/internal/catalog"
	"datascope-server/internal/guard"
	"datascope-server/internal/management"
)

func (s *Service) CloneConfiguration(ctx context.Context, in Input, commit bool) (*catalog.ClonePreview, error) {
	if _, ok := guard.User(ctx); !ok {
		return nil, management.ErrDenied
	}
	if _, business := guard.Business(ctx); business {
		return nil, management.ErrDenied
	}
	if in.Clone == nil {
		return nil, errors.New("需要 clone 参数")
	}
	return s.Catalog.CloneConfiguration(ctx, in.DatabaseID, *in.Clone, commit, func(scope catalog.CloneScope) error {
		if err := guard.Require(ctx, s.Management, in.Clone.SourceDatabaseID, "config.read"); err != nil {
			return err
		}
		if err := guard.Require(ctx, s.Management, in.DatabaseID, "config.read"); err != nil {
			return err
		}
		permissions := []string{}
		if scope.Authentication {
			u, _ := guard.User(ctx)
			if !u.SystemAdmin {
				return management.ErrDenied
			}
		}
		if scope.Tables {
			permissions = append(permissions, "config.write", "policy.write")
		}
		if scope.Policies {
			permissions = append(permissions, "policy.write")
		}
		if scope.Actions {
			permissions = append(permissions, "action.write")
		}
		if scope.Views {
			permissions = append(permissions, "view.write")
		}
		if scope.Knowledge {
			permissions = append(permissions, "knowledge.write")
		}
		for _, permission := range permissions {
			if err := guard.Require(ctx, s.Management, in.DatabaseID, permission); err != nil {
				return err
			}
		}
		return nil
	})
}
