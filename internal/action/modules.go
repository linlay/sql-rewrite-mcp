package action

import (
	"container/list"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"datascope-server/internal/store"
	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"
)

type Library struct {
	Name        string `json:"name" yaml:"name"`
	Title       string `json:"title" yaml:"title"`
	Description string `json:"description" yaml:"description"`
	Script      string `json:"script" yaml:"script"`
}

var libraryName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

type compiledModule struct {
	key     [32]byte
	program *goja.Program
	sites   []site
	bytes   int
}

// Only immutable programs are reused. Every invocation still owns a fresh VM,
// SQL bindings and transaction. Cache keys include the owning database and all
// source content, so a peer's YAML update cannot reuse stale dependencies.
var moduleCache = struct {
	sync.Mutex
	items map[[32]byte]*list.Element
	lru   *list.List
	bytes int
}{items: map[[32]byte]*list.Element{}, lru: list.New()}

func ValidateLibraries(libraries []Library) error {
	if len(libraries) > 64 {
		return fmt.Errorf("最多 64 个动作公共函数库")
	}
	seen := map[string]bool{}
	for _, library := range libraries {
		if !libraryName.MatchString(library.Name) || seen[library.Name] || library.Title == "" || len(library.Script) == 0 || len(library.Script) > 100000 {
			return fmt.Errorf("公共函数库 %s 需要唯一合法名称、标题和最多 100000 字节的脚本", library.Name)
		}
		seen[library.Name] = true
	}
	for _, library := range libraries {
		// Linking an unused library here validates its own imports without running
		// top-level JavaScript. Runtime calls only link their reachable modules.
		source := fmt.Sprintf("import * as library from %q; function run() { return library; }", library.Name)
		if _, _, err := compileWithLibraries(source, libraries, ""); err != nil {
			return fmt.Errorf("公共函数库 %s: %w", library.Name, err)
		}
	}
	return nil
}

func compileWithLibraries(source string, libraries []Library, database string) (*goja.Program, []site, error) {
	if len(libraries) == 0 {
		// Preserve the existing path and diagnostics for standalone scripts.
		return compile(source)
	}
	modules := map[string]string{}
	for _, library := range libraries {
		if !libraryName.MatchString(library.Name) || modules[library.Name] != "" {
			return nil, nil, fmt.Errorf("公共函数库名称无效或重复: %s", library.Name)
		}
		modules[library.Name] = library.Script
	}
	raw, _ := json.Marshal([]any{database, source, modules})
	key := sha256.Sum256(raw)
	moduleCache.Lock()
	if item := moduleCache.items[key]; item != nil {
		moduleCache.lru.MoveToFront(item)
		value := item.Value.(*compiledModule)
		moduleCache.Unlock()
		return value.program, value.sites, nil
	}
	moduleCache.Unlock()
	var mu sync.Mutex
	graph := map[string][]string{}
	plugin := api.Plugin{Name: "database-action-libraries", Setup: func(build api.PluginBuild) {
		build.OnResolve(api.OnResolveOptions{Filter: ".*"}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
			if args.Kind != api.ResolveJSImportStatement {
				return api.OnResolveResult{}, fmt.Errorf("仅支持静态 import / export，不能使用动态 import 或 require")
			}
			if !libraryName.MatchString(args.Path) {
				return api.OnResolveResult{}, fmt.Errorf("import 仅接受当前数据库的公共函数库名称: %s", args.Path)
			}
			if _, ok := modules[args.Path]; !ok {
				return api.OnResolveResult{}, fmt.Errorf("公共函数库不存在: %s", args.Path)
			}
			mu.Lock()
			graph[args.Importer] = append(graph[args.Importer], args.Path)
			mu.Unlock()
			return api.OnResolveResult{Path: args.Path, Namespace: "datascope-library"}, nil
		})
		build.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: "datascope-library"}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
			text := modules[args.Path]
			return api.OnLoadResult{Contents: &text, Loader: api.LoaderJS}, nil
		})
	}}
	result := api.Build(api.BuildOptions{
		Stdin:  &api.StdinOptions{Contents: source + "\n;export { run as __datascope_run };", Sourcefile: "action.js", Loader: api.LoaderJS},
		Bundle: true, Write: false, Format: api.FormatIIFE, GlobalName: "__datascope_action",
		Platform: api.PlatformNeutral, Target: api.ES2020, TreeShaking: api.TreeShakingFalse,
		LogLevel: api.LogLevelSilent, LogOverride: map[string]api.LogLevel{"unsupported-dynamic-import": api.LogLevelError, "unsupported-require-call": api.LogLevelError},
		Plugins: []api.Plugin{plugin},
	})
	if len(result.Errors) > 0 {
		messages := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Kind: api.ErrorMessage})
		return nil, nil, fmt.Errorf("动作模块编译失败: %s", strings.Join(messages, "\n"))
	}
	visited, visiting := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if visiting[name] {
			return fmt.Errorf("公共函数库存在循环依赖: %s", name)
		}
		if visited[name] {
			return nil
		}
		visiting[name] = true
		for _, next := range graph[name] {
			if err := visit(next); err != nil {
				return err
			}
		}
		delete(visiting, name)
		visited[name] = true
		return nil
	}
	names := make([]string, 0, len(graph))
	for name := range graph {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, nil, err
		}
	}
	code := string(result.OutputFiles[0].Contents) + "\nfunction run(args, ctx) { return __datascope_action.__datascope_run(args, ctx); }"
	program, sites, err := compile(code)
	if err != nil {
		return nil, nil, err
	}
	for _, site := range sites {
		if err := store.ValidateActionTemplate(site.sql, site.count, site.method == "exec"); err != nil {
			return nil, nil, err
		}
	}
	value := &compiledModule{key: key, program: program, sites: sites, bytes: len(raw) + len(code)}
	moduleCache.Lock()
	defer moduleCache.Unlock()
	if moduleCache.items[key] == nil && value.bytes <= 8<<20 {
		moduleCache.items[key] = moduleCache.lru.PushFront(value)
		moduleCache.bytes += value.bytes
		for moduleCache.lru.Len() > 128 || moduleCache.bytes > 8<<20 {
			item := moduleCache.lru.Back()
			old := item.Value.(*compiledModule)
			delete(moduleCache.items, old.key)
			moduleCache.bytes -= old.bytes
			moduleCache.lru.Remove(item)
		}
	}
	return program, sites, nil
}
