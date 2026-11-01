package management

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

func lockRetry() <-chan time.Time { return time.After(10 * time.Millisecond) }

type accountState struct {
	BusinessAccount
	PasswordHash string `json:"password_hash"`
}

type sourceState struct {
	BindingPolicyID string
	Database        map[string]any
	Credentials     map[string]string
	Accounts        []accountState
	Grants          []Grant
	Records         map[string][]Record
}

func emptyState() *sourceState {
	return &sourceState{Credentials: map[string]string{}, Accounts: []accountState{}, Grants: []Grant{}, Records: map[string][]Record{}}
}

// A stable sibling lock protects both readers and writers across processes.
// No generation is cached: API and MCP observe the same committed files.
func (s *Source) withState(ctx context.Context, write bool, fn func(*sourceState) error) error {
	for {
		if s.owner != nil {
			if err := s.owner.recoverPending(ctx); err != nil {
				return err
			}
		}
		retry := false
		err := fileLock(ctx, filepath.Join(filepath.Dir(s.Path), ".management.lock"), func() error {
			if s.owner != nil {
				if _, err := os.Stat(s.owner.pendingPath()); err == nil {
					retry = true
					return nil
				} else if !os.IsNotExist(err) {
					return err
				}
			}
			if err := recoverPublication(s.Path); err != nil {
				return err
			}
			state, err := readState(s.Path)
			if err != nil {
				return err
			}
			if err = fn(state); err != nil {
				return err
			}
			if write {
				return publishState(s.Path, state)
			}
			return nil
		})
		if err != nil || !retry {
			return err
		}
	}
}

func fileLock(ctx context.Context, path string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	// Nonblocking polling keeps cancellation effective while another process saves.
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-lockRetry():
		}
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// .management-next is the durable commit decision. Recovery always rolls it
// forward before allowing a reader in. An incomplete temporary tree is ignored.
func recoverPublication(path string) error {
	parent := filepath.Dir(path)
	next, previous := path+".next", path+".previous"
	if _, err := os.Stat(next); err == nil {
		if _, err = os.Stat(path); err == nil {
			if err = os.RemoveAll(previous); err != nil {
				return err
			}
			if err = os.Rename(path, previous); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err = os.Rename(next, path); err != nil {
			return err
		}
		if err = syncDir(parent); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if _, oldErr := os.Stat(previous); oldErr == nil {
			return errors.New("YAML 发布状态损坏：缺少已提交的配置，请恢复备份")
		}
	}
	return os.RemoveAll(previous)
}

func stageState(path string, state *sourceState) (string, error) {
	files, err := stateFiles(state)
	if err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(filepath.Dir(path), ".management-stage-")
	if err != nil {
		return "", err
	}
	valid := false
	defer func() {
		if !valid {
			_ = os.RemoveAll(stage)
		}
	}()
	for _, dir := range []string{"tables", "actions", "action-libraries", "knowledges"} {
		if err = os.Mkdir(filepath.Join(stage, dir), 0700); err != nil {
			return "", err
		}
	}
	for name, value := range files {
		b, err := marshalDocument(value)
		if err != nil {
			return "", err
		}
		f, err := os.OpenFile(filepath.Join(stage, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		_, err = f.Write(b)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	// Validate the entire serialized generation before making it visible.
	actual, err := readState(stage)
	if err != nil {
		return "", err
	}
	actualFiles, err := stateFiles(actual)
	if err != nil {
		return "", err
	}
	wantJSON, _ := json.Marshal(files)
	gotJSON, _ := json.Marshal(actualFiles)
	if !bytes.Equal(wantJSON, gotJSON) {
		return "", errors.New("YAML 逐项回读校验失败")
	}
	for _, dir := range []string{"tables", "actions", "action-libraries", "knowledges", "."} {
		if err = syncDir(filepath.Join(stage, dir)); err != nil {
			return "", err
		}
	}
	valid = true
	return stage, nil
}

func publishState(path string, state *sourceState) error {
	stage, err := stageState(path, state)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err = os.Rename(stage, path+".next"); err != nil {
		return err
	}
	if err = syncDir(filepath.Dir(path)); err != nil {
		return err
	}
	return recoverPublication(path)
}

func jsonValue(v any) any {
	b, _ := json.Marshal(v)
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	_ = dec.Decode(&out)
	return yamlNumbers(out)
}

// YAML must retain full SQLite int64 IDs, including values above 2^53.
func yamlNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		n, _ := x.Float64()
		return n
	case map[string]any:
		for k, value := range x {
			x[k] = yamlNumbers(value)
		}
		return x
	case []any:
		for i, value := range x {
			x[i] = yamlNumbers(value)
		}
		return x
	default:
		return v
	}
}

// Dynamic dictionary keys are business identifiers, never DataScope fields.
func dictionary(field string, v any) bool {
	_, mapping := v.(map[string]any)
	return mapping && (field == "parameters" || field == "columns" || field == "annotations" || field == "enum")
}

func convertFields(v any, encode, dynamic bool) any {
	return convertDocumentFields(v, encode, dynamic, false)
}

// JSON keeps its existing snake_case contract; persisted YAML uses lowerCamelCase.
func camelField(field string) string {
	parts := strings.Split(field, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

func snakeField(field string) string {
	var out strings.Builder
	for i, ch := range field {
		if ch >= 'A' && ch <= 'Z' {
			if i > 0 && (field[i-1] >= 'a' && field[i-1] <= 'z' || field[i-1] >= '0' && field[i-1] <= '9' || i+1 < len(field) && field[i+1] >= 'a' && field[i+1] <= 'z') {
				out.WriteByte('_')
			}
			ch += 'a' - 'A'
		}
		out.WriteRune(ch)
	}
	return out.String()
}

// Legacy field spelling is accepted only by the explicit offline migration.
func convertDocumentFields(v any, encode, dynamic, legacy bool) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, value := range x {
			field, target := k, k
			if !dynamic {
				if encode {
					target = camelField(k)
					if legacy {
						target = strings.ToUpper(k) + "_"
					}
				} else {
					field = snakeField(k)
					if legacy {
						field = strings.ToLower(strings.TrimSuffix(k, "_"))
					}
					target = field
				}
			}
			if !dynamic && field == "enum" {
				out[target] = value
			} else {
				out[target] = convertDocumentFields(value, encode, !dynamic && dictionary(field, value), legacy)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, value := range x {
			out[i] = convertDocumentFields(value, encode, false, legacy)
		}
		return out
	default:
		return v
	}
}

func marshalDocument(v any) ([]byte, error) {
	var node yaml.Node
	if err := node.Encode(convertFields(jsonValue(v), true, false)); err != nil {
		return nil, err
	}
	var literals func(*yaml.Node)
	literals = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i < len(n.Content); i += 2 {
				k, value := n.Content[i].Value, n.Content[i+1]
				if value.Kind == yaml.ScalarNode && value.Tag == "!!str" && (strings.Contains(value.Value, "\n") || k == "sql" || k == "script" || k == "content" || k == "ddl" || k == "denyUnless") {
					value.Style = yaml.LiteralStyle
				}
			}
		}
		for _, child := range n.Content {
			literals(child)
		}
	}
	literals(&node)
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	err := enc.Encode(&node)
	_ = enc.Close()
	return b.Bytes(), err
}

func readDocument(path string, dest any) error {
	return readDocumentFormat(path, dest, false)
}

func readMigrationDocument(path string, dest any) error {
	return readDocumentFormat(path, dest, true)
}

func readDocumentFormat(path string, dest any, allowLegacy bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("YAML %s 必须是普通文件", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var raw any
	if err = dec.Decode(&raw); err != nil {
		return fmt.Errorf("YAML %s 无效", path)
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return fmt.Errorf("YAML %s 只能包含一个文档", path)
	}
	legacy := false
	if allowLegacy {
		if root, ok := raw.(map[string]any); ok {
			for key := range root {
				if strings.HasSuffix(key, "_") && key == strings.ToUpper(key) {
					legacy = true
					break
				}
			}
		}
	}
	converted := convertDocumentFields(raw, false, false, legacy)
	// Reject noncanonical, ambiguous, or duplicate field spellings. YAML itself
	// rejects duplicate keys; a normalized comparison enforces the public format.
	// Compare the field mapping directly: serializing and parsing YAML again
	// adds no validation and repeats most of the work for every file read.
	normalized := convertDocumentFields(converted, true, false, legacy)
	a, _ := json.Marshal(raw)
	c, _ := json.Marshal(normalized)
	if !bytes.Equal(a, c) {
		return fmt.Errorf("YAML %s 字段必须采用小驼峰格式；旧配置请停服后执行 migrate-state", path)
	}
	data, err := json.Marshal(converted)
	if err != nil {
		return err
	}
	jdec := json.NewDecoder(bytes.NewReader(data))
	jdec.DisallowUnknownFields()
	if err = jdec.Decode(dest); err != nil {
		return fmt.Errorf("YAML %s 内容类型无效", path)
	}
	return nil
}

func documentName(id string) (string, error) {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return "", errors.New("YAML 文件标识无效")
	}
	return id + ".yml", nil
}

func stateFiles(state *sourceState) (map[string]any, error) {
	files := map[string]any{"account.yml": map[string]any{"accounts": state.Accounts}, "grant.yml": map[string]any{"grants": state.Grants}}
	d, _ := jsonValue(state.Database).(map[string]any)
	if d == nil {
		d = map[string]any{}
	}
	if err := normalizeDatabase(d); err != nil {
		return nil, err
	}
	auth := d["authentication"]
	delete(d, "authentication")
	files["authentication.yml"] = map[string]any{"authentication": auth, "policy_id": state.BindingPolicyID}
	// Credentials get explicit envelopes; operational records never enter YAML.
	files["database.yml"] = map[string]any{"database": d, "read_password_ciphertext": state.Credentials["READ"], "write_password_ciphertext": state.Credentials["WRITE"]}
	for bucket, records := range state.Records {
		if bucket != "knowledge" && len(records) > 0 {
			return nil, fmt.Errorf("非正式配置不能持久化: %s", bucket)
		}
	}
	if state.Database == nil {
		files["database.yml"].(map[string]any)["database"] = nil
	}
	tables := map[string]map[string]any{}
	get := func(name string) map[string]any {
		if tables[name] == nil {
			tables[name] = map[string]any{"name": name}
		}
		return tables[name]
	}
	if cfg, ok := d["config"].(map[string]any); ok {
		if policies, ok := cfg["tables"].([]any); ok {
			for _, p := range policies {
				policy, ok := p.(map[string]any)
				if !ok {
					return nil, errors.New("查询策略必须是对象")
				}
				name, _ := policy["table"].(string)
				if _, exists := get(name)["policy"]; exists {
					return nil, errors.New("表权限重复")
				}
				get(name)["policy"] = policy
			}
		}
		if list, ok := cfg["tables"].([]any); ok {
			order := []string{}
			for _, v := range list {
				order = append(order, v.(map[string]any)["table"].(string))
			}
			cfg["table_order"] = order
		} else if _, ok := cfg["tables"]; ok {
			cfg["table_order"] = nil
		}
		delete(cfg, "tables")
	}
	if discovery, ok := d["discovery"].(map[string]any); ok {
		if list, ok := discovery["tables"].([]any); ok {
			for _, item := range list {
				table, ok := item.(map[string]any)
				if !ok {
					return nil, errors.New("表结构必须是对象")
				}
				name, _ := table["name"].(string)
				if _, exists := get(name)["structure"]; exists {
					return nil, errors.New("表结构重复")
				}
				get(name)["structure"] = table
			}
		}
		if annotations, ok := discovery["annotations"].(map[string]any); ok {
			for name, a := range annotations {
				get(name)["annotation"] = a
			}
		}
		if list, ok := discovery["tables"].([]any); ok {
			order := []string{}
			for _, v := range list {
				order = append(order, v.(map[string]any)["name"].(string))
			}
			discovery["table_order"] = order
		} else if _, ok := discovery["tables"]; ok {
			discovery["table_order"] = nil
		}
		delete(discovery, "tables")
		if _, ok := discovery["annotations"].(map[string]any); ok {
			discovery["annotations"] = map[string]any{}
		}
	}
	for name, table := range tables {
		filename, err := documentName(name)
		if err != nil {
			return nil, err
		}
		files[filepath.Join("tables", filename)] = table
	}
	for _, collection := range []struct{ field, dir, order string }{
		{"actions", "actions", "action_order"},
		{"action_libraries", "action-libraries", "action_library_order"},
	} {
		order := []string{}
		if items, ok := d[collection.field].([]any); ok {
			for _, item := range items {
				value, ok := item.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%s 必须是对象", collection.field)
				}
				name, _ := value["name"].(string)
				filename, err := documentName(name)
				if err != nil {
					return nil, err
				}
				file := filepath.Join(collection.dir, filename)
				if _, exists := files[file]; exists {
					return nil, fmt.Errorf("%s 名称重复", collection.field)
				}
				files[file] = value
				order = append(order, name)
			}
		}
		if _, exists := d[collection.field]; exists {
			d[collection.order] = order
		}
		delete(d, collection.field)
	}
	for _, r := range state.Records["knowledge"] {
		var value map[string]any
		if err := json.Unmarshal([]byte(r.Data), &value); err != nil {
			return nil, err
		}
		id, _ := value["id"].(string)
		if id == "" {
			id = strings.TrimPrefix(r.Key, fmt.Sprint(d["id"])+"/")
		}
		filename, err := documentName(id)
		if err != nil {
			return nil, err
		}
		if _, exists := files[filepath.Join("knowledges", filename)]; exists {
			return nil, errors.New("知识 ID 重复")
		}
		files[filepath.Join("knowledges", filename)] = map[string]any{"key": r.Key, "knowledge": value}
	}
	return files, nil
}

func readState(path string) (*sourceState, error) {
	return readStateUsing(path, readDocument)
}

func readStateUsing(path string, read func(string, any) error) (*sourceState, error) {
	state := emptyState()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return state, nil
	}
	entries, e := os.ReadDir(path)
	if e != nil {
		return nil, e
	}
	allowed := map[string]bool{"database.yml": false, "authentication.yml": false, "account.yml": false, "grant.yml": false, "tables": true, "actions": true, "action-libraries": true, "knowledges": true}
	for _, entry := range entries {
		dir, ok := allowed[entry.Name()]
		if !ok || entry.IsDir() != dir {
			return nil, fmt.Errorf("意外的管理配置项: %s", entry.Name())
		}
	}
	var root struct {
		Database map[string]any `json:"database"`
		Read     string         `json:"read_password_ciphertext"`
		Write    string         `json:"write_password_ciphertext"`
		Records  []struct {
			Bucket string          `json:"bucket"`
			Key    string          `json:"key"`
			Data   json.RawMessage `json:"data"`
		} `json:"records"`
	}
	if err := read(filepath.Join(path, "database.yml"), &root); err != nil {
		return nil, err
	}
	state.Database = root.Database
	if root.Read != "" {
		state.Credentials["READ"] = root.Read
	}
	if root.Write != "" {
		state.Credentials["WRITE"] = root.Write
	}
	for _, v := range state.Credentials {
		if _, err := base64.StdEncoding.DecodeString(v); err != nil {
			return nil, errors.New("连接密码密文格式无效")
		}
	}
	for _, r := range root.Records {
		if r.Bucket != "discovery_checks" && r.Bucket != "discovery_pending" {
			return nil, fmt.Errorf("意外的旧辅助记录: %s", r.Bucket)
		}
	}
	var auth struct {
		PolicyID       string `json:"policy_id"`
		Authentication any    `json:"authentication"`
	}
	if err := read(filepath.Join(path, "authentication.yml"), &auth); err != nil {
		return nil, err
	}
	state.BindingPolicyID = auth.PolicyID
	if auth.Authentication != nil && state.Database == nil {
		return nil, errors.New("认证连接缺少数据库配置")
	}
	if auth.Authentication != nil && state.Database != nil {
		state.Database["authentication"] = auth.Authentication
	}
	var accounts struct {
		Accounts []accountState `json:"accounts"`
	}
	var grants struct {
		Grants []Grant `json:"grants"`
	}
	if err := read(filepath.Join(path, "account.yml"), &accounts); err != nil {
		return nil, err
	}
	state.Accounts = accounts.Accounts
	if err := read(filepath.Join(path, "grant.yml"), &grants); err != nil {
		return nil, err
	}
	state.Grants = grants.Grants
	for _, dir := range []string{"tables", "actions", "action-libraries", "knowledges"} {
		entries, err := os.ReadDir(filepath.Join(path, dir))
		if os.IsNotExist(err) && dir == "action-libraries" {
			if state.Database != nil {
				if _, declared := state.Database["action_library_order"]; declared {
					return nil, errors.New("公共函数库目录缺失，请恢复配置")
				}
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		actions := map[string]any{}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
				return nil, fmt.Errorf("意外的配置文件: %s", entry.Name())
			}
			var v map[string]any
			if err = read(filepath.Join(path, dir, entry.Name()), &v); err != nil {
				return nil, err
			}
			switch dir {
			case "tables":
				if state.Database == nil {
					return nil, errors.New("表缺少数据库配置")
				}
				name, _ := v["name"].(string)
				if entry.Name() != name+".yml" {
					return nil, errors.New("表文件名不匹配")
				}
				if p, ok := v["policy"]; ok {
					policy, ok := p.(map[string]any)
					if !ok || policy["table"] != name {
						return nil, errors.New("表权限标识无效")
					}
					cfg, _ := state.Database["config"].(map[string]any)
					if cfg == nil {
						return nil, errors.New("表权限缺少配置")
					}
					list, _ := cfg["tables"].([]any)
					cfg["tables"] = append(list, p)
				}
				if discovery, ok := state.Database["discovery"].(map[string]any); ok {
					if table, ok := v["structure"]; ok {
						structure, ok := table.(map[string]any)
						if !ok || structure["name"] != name {
							return nil, errors.New("表结构标识无效")
						}
						list, _ := discovery["tables"].([]any)
						discovery["tables"] = append(list, table)
					}
					if a, ok := v["annotation"]; ok {
						annotations, _ := discovery["annotations"].(map[string]any)
						if annotations == nil {
							annotations = map[string]any{}
							discovery["annotations"] = annotations
						}
						annotations[name] = a
					}
				}
			case "actions", "action-libraries":
				name, _ := v["name"].(string)
				if entry.Name() != name+".yml" {
					return nil, errors.New("Action 文件名不匹配")
				}
				actions[name] = v
			case "knowledges":
				key, _ := v["key"].(string)
				value, ok := v["knowledge"].(map[string]any)
				if !ok {
					return nil, errors.New("知识内容无效")
				}
				if id, ok := value["id"].(string); ok && entry.Name() != id+".yml" {
					return nil, errors.New("知识文件名不匹配")
				}
				b, _ := json.Marshal(value)
				state.Records["knowledge"] = append(state.Records["knowledge"], Record{key, string(b)})
			}
		}
		if (dir == "actions" || dir == "action-libraries") && state.Database != nil {
			field, orderField := "actions", "action_order"
			if dir == "action-libraries" {
				field, orderField = "action_libraries", "action_library_order"
			}
			if order, ok := state.Database[orderField].([]any); ok {
				list := []any{}
				for _, item := range order {
					name, _ := item.(string)
					a, ok := actions[name]
					if !ok {
						return nil, errors.New("Action 顺序引用不存在")
					}
					list = append(list, a)
					delete(actions, name)
				}
				if len(actions) > 0 {
					return nil, errors.New("Action 未登记顺序")
				}
				state.Database[field] = list
				delete(state.Database, orderField)
			} else if len(actions) > 0 {
				return nil, errors.New("Action 缺少顺序")
			}
		}
	}

	if state.Database != nil {
		for _, field := range []string{"config", "discovery"} {
			m, ok := state.Database[field].(map[string]any)
			if !ok {
				continue
			}
			order, exists := m["table_order"]
			if !exists {
				continue
			}
			delete(m, "table_order")
			if order == nil {
				m["tables"] = nil
				continue
			}
			byName := map[string]any{}
			if list, ok := m["tables"].([]any); ok {
				for _, v := range list {
					t, ok := v.(map[string]any)
					if !ok {
						return nil, errors.New("表内容必须是对象")
					}
					key := "name"
					if field == "config" {
						key = "table"
					}
					name, ok := t[key].(string)
					if !ok {
						return nil, errors.New("表标识必须是字符串")
					}
					byName[name] = v
				}
			}
			ordered := []any{}
			sequence, ok := order.([]any)
			if !ok {
				return nil, errors.New("表顺序必须是列表")
			}
			for _, v := range sequence {
				name, ok := v.(string)
				if !ok {
					return nil, errors.New("表标识必须是字符串")
				}
				item, ok := byName[name]
				if !ok {
					return nil, errors.New("表顺序引用不存在")
				}
				ordered = append(ordered, item)
				delete(byName, name)
			}
			if len(byName) > 0 {
				return nil, errors.New("表未登记顺序")
			}
			m["tables"] = ordered
		}
	}
	if state.Database != nil {
		if err := normalizeDatabase(state.Database); err != nil {
			return nil, err
		}
	}
	return state, nil
}

// Backup exports one consistent YAML generation and its independently stored key.
func (s *Source) Backup(ctx context.Context, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return errors.New("备份目标已存在")
	}
	return s.withState(ctx, false, func(st *sourceState) error {
		if err := os.MkdirAll(dest, 0700); err != nil {
			return err
		}
		if err := publishState(filepath.Join(dest, "management"), st); err != nil {
			return err
		}
		key, err := os.ReadFile(filepath.Join(filepath.Dir(s.Path), "credentials.key"))
		if os.IsNotExist(err) && len(st.Credentials) == 0 {
			return nil
		}
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dest, "credentials.key"), key, 0600)
	})
}
