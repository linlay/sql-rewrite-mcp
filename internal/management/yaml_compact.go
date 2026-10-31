package management

import (
	"os"
	"path/filepath"
)

// compactDerivedYAML is called only by the explicit offline upgrade command.
// It cleans early YAML generations that still contained operational scan data,
// keeping an exact one-time backup and preserving the formal revision.
func compactDerivedYAML(path, backup string, st *sourceState) (bool, error) {
	var root map[string]any
	if err := readDocument(filepath.Join(path, "database.yml"), &root); err != nil {
		return false, err
	}
	_, dirty := root["records"]
	if d, ok := root["database"].(map[string]any); ok {
		if discovery, ok := d["discovery"].(map[string]any); ok {
			for _, key := range []string{"pending", "checked_at", "attempted_at", "error", "reports"} {
				if _, exists := discovery[key]; exists {
					dirty = true
				}
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(path, "tables"))
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		var table map[string]any
		if err = readDocument(filepath.Join(path, "tables", entry.Name()), &table); err != nil {
			return false, err
		}
		policy, _ := table["policy"].(map[string]any)
		for _, key := range []string{"compiled_sql", "compiled_deny_unless", "params", "deny_unless_params"} {
			if _, exists := policy[key]; exists {
				dirty = true
			}
		}
	}
	if !dirty {
		return false, nil
	}
	if err = os.MkdirAll(backup, 0700); err != nil {
		return false, err
	}
	if err = os.CopyFS(filepath.Join(backup, "management"), os.DirFS(path)); err != nil {
		return false, err
	}
	if err = filepath.WalkDir(backup, func(p string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(p, 0700)
		}
		if err = os.Chmod(p, 0600); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		err = f.Sync()
		_ = f.Close()
		return err
	}); err != nil {
		return false, err
	}
	key, err := os.ReadFile(filepath.Join(filepath.Dir(path), "credentials.key"))
	if err == nil {
		if err = os.WriteFile(filepath.Join(backup, "credentials.key"), key, 0600); err != nil {
			return false, err
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err = syncDir(backup); err != nil {
		return false, err
	}
	if err = publishState(path, st); err != nil {
		return false, err
	}
	return true, nil
}
