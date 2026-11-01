package catalog

import (
	"datascope-server/internal/view"
	"fmt"
)

func (d Database) View(id string, published bool) (*view.Definition, error) {
	for _, v := range d.Views {
		if v.ID == id && (!published || v.Published) {
			return &v, nil
		}
	}
	return nil, fmt.Errorf("视图不存在或未发布: %s", id)
}

func validateViews(d Database) error {
	if len(d.Views) > 100 {
		return fmt.Errorf("每个数据库最多 100 个视图")
	}
	seen := map[string]bool{}
	for _, v := range d.Views {
		if seen[v.ID] {
			return fmt.Errorf("视图标识重复: %s", v.ID)
		}
		seen[v.ID] = true
		if err := v.Validate(); err != nil {
			return fmt.Errorf("视图 %s: %w", v.ID, err)
		}
	}
	if d.QueryViewID != "" {
		if _, err := d.View(d.QueryViewID, true); err != nil {
			return fmt.Errorf("默认查询视图: %w", err)
		}
	}
	for _, a := range d.Actions {
		if a.ViewID != "" {
			if _, err := d.View(a.ViewID, true); err != nil {
				return fmt.Errorf("动作 %s 关联视图: %w", a.Name, err)
			}
		}
	}
	return nil
}
