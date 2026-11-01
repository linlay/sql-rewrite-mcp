# DataScope 数据库访问管理平台

Go + React 实现的数据库访问平台，支持 SQLite、MySQL、PostgreSQL、Oracle 和达梦 DM8 业务数据源，内置平台用户、数据库直接授权、SQL 权限改写、独立审计库，以及管理系统自身的 MCP 与逐库数据库 MCP。

## 启动

需要 Go 1.26、Node 22+。

```sh
cp .env.example .env
# 设置 ADMIN_PASSWORD（至少 6 字符），首次初始化平台管理员。
# 设置 OAUTH_DEMO_PASSWORD（至少 6 字符），首次初始化证券 demo 员工模拟账号。
make init          # 已有演示业务库时跳过；不会覆盖已有文件
make web-install
make build
make run-api       # 终端一，默认 8081；发布 OAuth 和管理 MCP
make run-mcp       # 终端二，默认 8080；提供数据库 MCP
make run-web       # 终端三，默认 8082；管理控制台
```

首次使用 `ADMIN_USERNAME`（默认 `admin`）与配置的 `ADMIN_PASSWORD` 登录。密码使用带随机盐的 PBKDF2-SHA256 保存。初始化完成后，这两个环境变量不会覆盖已有账号；密码通过用户管理重置。后台会话使用 HttpOnly、SameSite Cookie，密码重置和用户禁用会撤销会话，旧 OAuth JWT 也会失效。

两个 Go 服务必须使用相同的 `MANAGEMENT_DB_PATH`、`AUDIT_DB_PATH` 和 `DATABASE_ALLOWED_ROOTS`。更改监听端口时同步修改 `MCP_RESOURCE_URL`、`MCP_SERVER_URL`、`OAUTH_ISSUER`、`ADMIN_API_URL`。外部部署使用 HTTPS；HTTPS 反向代理后设置 `SESSION_COOKIE_SECURE=true`，或使用 HTTPS `OAUTH_ISSUER` 的默认推断。

可使用 `SQLITE_PATH` 指定初始 SQLite 业务数据库；仅接入网络数据库时设为显式空值，允许空数据源启动，再从控制台添加。多数据库配置、SQL 子集、达梦平台说明、Linux 构建与真实库验收见 [多数据库接入说明](docs/databases.md)。

## 模块与持久化

```text
internal/
  management/       全局身份与认证配置、逐库管理存储、内存会话
  guard/            统一授权检查与可信身份上下文
  rewrite/          AST 解析、表字段限制、行权限注入、参数检查
  store/            只读查询、独立 Action 写事务、结构读取、分页与限制
  action/           goja 脚本、标准静态 import/export、固定 SQL 模板与事务运行器
  audit/            请求、改写、执行、权限检查及操作审计
  application/      Web 与管理 MCP 共用的领域动作
  adminapi/         受保护的后台 HTTP API
  managementmcp/    管理平台自身的 MCP 工具
  mcpserver/        逐库数据库 MCP、知识读取、配置生成的业务工具
  oauth/            平台账号 OAuth 授权、资源隔离、JWT 校验
  platform/         启动装配与集成测试
```

默认内部状态：

| 文件 | 职责 |
| --- | --- |
| `data/management.db` | 平台用户、系统授权、数据源目录、全局认证提供方 / 策略、管理入口绑定、OAuth 客户端注册 |
| `data/audit.db` | 全局管理与认证审计 |
| `data/databases/<ID>/management/` | 逐库 YAML：连接、认证、账号、授权、表权限与结构、Actions、知识；仅保留当前 revision |
| `data/databases/<ID>/audit.db` | 此库的查询、SQL 改写、Action 与管理审计，异步写入 |
| `data/databases/<ID>/credentials.key` | 此库连接密码的 AES-256-GCM 密钥，与业务管理 YAML 目录一起备份 |
| `data/oauth/signing.pem` | 演示 OAuth 私钥，独立文件保存 |

DataScope 自有表使用单数大写下划线名称，字段以 `_` 结尾；业务库命名不受影响。YAML 配置字段统一使用小驼峰，如 `mcpEnabled`、`maxRows`、`denyUnless`、`businessUserId`，业务标识与脚本正文保留原值；HTTP JSON 接口格式保持不变。运行时会话、OAuth 授权码 / 刷新凭据和连接保留在内存。完整表结构、认证接口约定与发布清单见 [存储与认证改造](docs/storage-auth-refactor.md)。

常规查询使用业务数据库只读连接；业务 Action 使用显式启用的独立写入连接，见 [业务操作说明](docs/actions.md)。`DATABASE_ALLOWED_ROOTS` 指定允许登记的目录，macOS/Linux 多个目录用 `:` 分隔；默认 `data`。管理库、审计库及其符号链接、硬链接不能注册为业务数据源。原始查询也禁止 ATTACH 和修改连接状态的 PRAGMA，避免从已授权业务库访问内部状态。

控制台地址会随数据库、数据表、动作、知识及公共函数的选择更新。例如 `#/tables/employees?database=default`、`#/actions/update_customer_mobile?database=default` 和 `#/knowledge/securities-actions-guide?database=default`。工作台模式、详情视图、表数据分页与排序、已应用的日志筛选也保存在地址中；刷新、复制链接及浏览器前进后退会恢复对应选择。打开动作工作台链接只读取元数据，不自动试运行或正式调用；密码、Token、动作参数和自定义 SQL 不写入地址。未保存的配置在离开前仍会提示确认。

## 全站显示设置

右上角齿轮打开系统设置，统一切换简体中文 / English，以及跟随系统 / 浅色 / 深色外观；选择在当前浏览器保存，刷新和跨页面后保留，并同步到同源的其他标签页。界面语言同时作用于导航、表单、日志、登录页、标准组件及提示弹窗；数据库注释、知识正文、SQL、用户姓名和查询结果保持原文。「管理连接器」的 ZIP 下载也位于此处。

## 用户与数据库授权

没有角色表或角色继承。每个用户直接关联数据库，并授予能力：

| 权限 | 能力 |
| --- | --- |
| `config.read` | 查看连接和配置，使用配置管理页面 |
| `config.write` | 修改数据库名称 |
| `policy.write` | 修改表、字段、行权限策略 |
| `mcp.write` | 配置 MCP 地址、基础工具与知识发布开关 |
| `action.write` | 编写业务 Action、启停独立写入及维护写入账号 |
| `knowledge.read` | 通过数据库 MCP 读取本库全部已发布知识；无需 `query`，不受表策略影响 |
| `knowledge.write` | 编辑、发布知识 |
| `view.write` | 编辑、发布 HTML 视图及默认查询视图 |
| `query` | 以可信业务身份改写并执行查询 |
| `raw_query` | 全量只读查询及表数据浏览，绕过行字段策略，需单独授予 |
| `audit.read` | 查看当前数据库审计 |
| `grant` | 分配、撤销自身拥有的数据库权限 |
| `impersonate` | 在改写工作台模拟业务身份 |

初始化管理员是系统恢复入口，拥有全部能力。普通用户不能自行创建平台管理员，不能授予自身没有的能力，不能修改业务身份绑定。平台管理用户与业务员工账号完全独立。每个数据库可使用 DataScope 模拟的独立员工认证域；同名工号在不同数据库中不共享密码、业务身份和权限。演示库中的角色只决定演示业务行权限。

在“数据库配置 → 用户管理”中分开管理平台用户和当前数据库的业务员工账号。业务账号的**业务身份编号**是该数据源 `${userId}` 的可信值，例如证券演示库的 `employees.employee_id`。它不需要对应的平台账号，也不继承平台管理权限。平台用户的数据库授权用于后台管理及控制台；管理员可在有 `impersonate` 权限的改写工作台模拟业务身份。独立业务认证的 MCP 测试请通过业务连接器完成真实员工登录。

新增数据库仅需填写显示名称及连接信息，ID 自动生成。网络数据库直接输入密码即可，密码通过独立密钥加密保存，不会写入配置历史或返回给客户端；旧环境变量引用继续兼容。新增数据库默认勾选表目录（list_tables）、表结构（describe_table）、权限查询（query）三个基础工具，MCP 总开关仍默认关闭，所有表默认禁止。先配置访问策略，再打开 MCP。查询用户只获得数据库名称等最小目录信息；连接路径与策略配置只向具备 `config.read` 的用户提供。配置页通常需要同时授予 `config.read` 和对应修改权限。

## 自动侦查、映射与结构变化

添加数据库会自动建立完整表字段快照，提取表和字段注释、类型、主键、非空、默认值、外键及索引名称；自动生成全部表的访问策略，默认禁止所有表，并固定当前字段列表。控制台提供「数据库配置」「表配置与权限」「业务知识」「SQL 控制台」「表数据」「操作日志」「MCP 配置」。数据库配置将基本信息与用户管理上下排列，不使用标签页；操作日志为独立入口，默认仅显示数据库、状态、时间范围和关键词；操作用户、操作类型、来源、请求 ID 和工具收在默认折叠的高级筛选中，并提示已生效条件数量。后端筛选后统计总数与分页；表配置与权限统一提供字段开放选择、行过滤、展示名称映射和人工说明。物理 SQL 名称保持不变。

表目录、字段列表和结构变更对比均不分页，通过内部滚动查看完整列表。表目录支持名称和说明搜索，并显示筛选后的表数及总数。「全部开放」「全部关闭」始终作用于当前数据库全部已登记的表，点击「保存表配置」后生效，保留已有字段范围和行权限；字段范围为空的表在批量开放时选择当前已登记字段，后续新增字段仍需确认开放。

管理 API 启动后自动为旧数据源建立基线，每隔 `DISCOVERY_INTERVAL`（默认 `5m`，设为 `0s` 关闭）检查结构；无需保持浏览器打开。「检查数据库变化」可随时检查。检测到结构差异只在扫描进程内存保存待确认的对比，不会立即更改已登记结构、人工说明或访问策略；页面提示后在对比弹窗确认，才采用新结构。确认时再次读取源结构，若数据库又有变化或配置版本已更新，拒绝应用过期对比。无变化时不新增结构版本，也不使正在编辑的配置过期。

确认采用新结构后，新增表默认禁止，新增字段不进入已有字段白名单，删除字段从白名单移除，删除表从当前策略移除；字段全部删除时关闭该表。显式勾选「包含未来新增字段」才会让该表后续字段自动可见。表或字段改名按删除和新增处理，不猜测关联。

数据库注释与人工说明独立保存：未覆盖的说明随源注释更新；人工值（包括显式空字符串）默认保留。数据库注释变化且与人工值不同时，报告同时展示旧值、新值、人工值并标记冲突。可以在变更对比弹窗逐项选择保留人工说明或采用数据库新注释，也可在说明编辑中取消覆盖。映射与有效说明同时用于数据浏览和数据库 MCP 的结构说明。权限 SQL 和 Action 不会被扫描器自动重写；结构变化导致的自定义 SQL 失效需管理员调整，期间结构检查和分页浏览仍可使用。

扫描使用只读连接，不执行全表 COUNT，不复制业务数据。通过「浏览数据」按页读取各表全部内容；全量数据浏览需 `raw_query`。本功能比较结构和注释，不比较业务记录的增删改。扫描失败保留上次完整快照，自动扫描失败会显示错误；本进程最近 20 次结构变化报告可在页面切换查看，重启清空扫描候选、检查状态和报告，可重新扫描；正式配置仅保留当前 revision，审计独立保留在 audit.db。[状态生命周期](docs/runtime-state.md) 说明进程与重启约定。映射编辑与扫描使用配置版本检查，避免覆盖其他窗口的修改。

Web 与管理 MCP 共用管理动作：`get_discovery`（`config.read`）、`discover_database` 和 `save_annotation`（另需 `config.write`），以及确认应用的 `apply_discovery`（还需 `policy.write`）。管理 MCP 分别使用 `datascope_query` 的 `discovery.get`、`datascope_manage` 的 `discovery.refresh` 、`discovery.apply` 和 `annotation.save`，参数见管理连接器参考。

## 每个数据库独立的业务认证

在系统设置的全局认证配置中，先定义普通账号（ACCOUNT）、OAuth（OAUTH）、不需要认证（NONE）三类策略，再绑定平台接口与业务数据库。普通账号支持平台账号、逐库演示账号、企业账号验证接口；OAuth 支持演示与企业提供方。企业身份由配置的接口或 UserInfo 验证，不要求先创建 BUSINESS_ACCOUNT。

演示账号的兼容 `authentication` 配置用于设置登录页名称和员工目录校验：

```json
{"authentication":{"type":"local","label":"证券 demo 员工认证","employee_directory":true}}
```

- `local`：DataScope 模拟当前数据库的员工认证。账号单独存入当前库 `management/account.yml`，以 `(DATABASE_ID_, USERNAME_)` 隔离，密码独立哈希；业务库仍只读，不要求业务库自行提供账号表。未填写 `authentication` 时也使用独立业务账号，初始账号为空。
- `employee_directory: true`：额外校验当前业务库 `employees` 目录的工号、员工编号与在职状态，适用于证券 demo。其他数据库通常关闭，由其业务账号绑定对应 SQL 策略需要的业务身份编号。

平台管理员可以为每个数据库分别创建、停用业务账号、设置业务能力或重置密码，也可以通过管理 MCP 的 `business_account.list/save` 操作维护。业务账号可获得 `query`、`action`、`knowledge.read` 和单独授予的 `raw_query`；它不能登录管理后台或调用管理 MCP。管理业务账号及修改认证设置仅限平台管理员。

每个数据库有独立 OAuth issuer：`{OAUTH_ISSUER}/auth/databases/{database_id}`。业务 MCP 的资源发现元数据返回该 issuer，客户端随后发现其授权、注册、令牌端点并跳转该数据库的员工登录页。管理 MCP 默认使用平台根 issuer，企业 OAuth 模式下使用配置的企业 issuer。旧客户端仍从根授权入口发起业务授权时，会跳转相应数据库的员工认证入口。

令牌绑定精确 MCP resource、数据库账号域、账号版本和认证配置版本；密码重置、账号停用、业务能力或身份变更、认证设置变更都会使旧令牌和刷新凭据失效。修改认证设置后需在客户端重新授权连接器。相同工号可以存在于两个数据库甚至平台用户中，三个账号分别认证。

首次启动新版服务时，若初始业务库包含证券 demo 员工目录且尚未配置认证来源，设置 `OAUTH_DEMO_PASSWORD`（至少 6 字符），服务会一次性为目录员工建立证券库的独立模拟账号并启用目录校验。E0001 对应业务身份 1。此密码仅用于首次创建，不覆盖已有业务密码；以后在当前数据库的业务账号列表中重置。新增其他业务数据库会拥有另一套空的模拟账号域，不复制证券库员工或密码。平台账号仅用于管理后台和管理 MCP；业务 MCP 不接受平台账号或平台令牌。企业账号接口和企业 OAuth 的字段映射、回调及密钥配置见上述改造文档。

## 两类 MCP

### 管理 MCP

默认端点：`http://127.0.0.1:8081/mcp/admin`，使用平台账号完成 OAuth 授权。

管理入口收敛为三个工具，统一使用 `{action,args}`，由 `args.resourceType` 指定资源：

| 工具 | 用途 | 支持的资源 |
| --- | --- | --- |
| `datascope_query` | 查询配置、用户、授权、知识和审计 | database、policy、user、grant、knowledge、audit |
| `datascope_manage` | 创建与修改管理资源 | database、policy、mcp、user、grant、knowledge |
| `datascope_inspect` | 连接测试、策略校验、SQL 改写预览 | database、policy |

例如读取数据库配置：

```json
{"action":"get","args":{"resourceType":"database","database_id":"default"}}
```

`deploy/connectors/datascope-admin/` 提供独立管理连接器，附带 `datascope-admin` skill 和数据库、策略、用户授权、知识、审计操作参考。在管理界面的“右上角齿轮设置 → 管理连接器”点击“下载连接器 ZIP”，下载包中的 MCP 地址由部署的 `OAUTH_ISSUER` 配置生成；通过完整连接器包导入并挂载到 Agent，完成管理端 OAuth 授权后使用。下载接口 `GET /api/connectors/datascope-admin.zip` 需要登录，不包含任何账号凭据。使用源码目录打包时，需按实际部署地址修改包内 `mcp.json`。只导入 MCP 地址不会携带 skill。打包可在仓库根运行 `(cd deploy/connectors && zip -r /tmp/datascope-admin.zip datascope-admin)`。

工具 schema 保留简短的动作和资源枚举；资源与动作矩阵、详细参数和管理流程由配套 skill 描述，服务端严格拒绝不支持的组合、无关参数与错误类型。原有 25 个独立管理工具不再由管理 MCP 暴露；其他 MCP 客户端也需改用上述调用结构。Web 管理端的原业务动作接口保持不变。

管理 MCP 通过业务动作维护全局 SQLite 和逐库 YAML，不提供对内部库执行任意 SQL 的能力。Web 控制台与 MCP 复用同一应用服务，继续按原动作检查权限并记录审计。配置修改需要 `args.database.revision`，知识更新需要 `args.revision`，旧版本不能覆盖新配置。查询与诊断不保存业务配置，但仍记录审计；策略校验仍需 `policy.write`。

### 数据库 MCP

默认地址为 `http://127.0.0.1:8080/mcp/{mcp_path}`，保留原有分库地址及默认库 `/mcp` 入口。`admin` 为保留路径后缀。

每个数据库独立配置基础工具：`list_tables`、`describe_table`、`query`、`raw_query`、`customer_scope`、`list_actions`、`describe_action`、`invoke_action`。前五个沿用已有工具，其中 `customer_scope` 仅适用于证券演示库。

HTTP OAuth 的 audience 精确绑定完整地址，管理端令牌与不同数据库的令牌不能混用。业务 MCP 按数据库认证来源重新检查当前业务账号、能力和工具启停；管理 MCP 检查平台账号及管理授权。已有连接不能保留已撤销权限。

管理端登记的业务 MCP 统一要求 HTTP OAuth，使用当前数据库的独立业务账号。`header`、`none` 及 stdio 不提供平台账号访问业务 MCP 的替代入口。底层独立演示测试仍可使用无管理库的传输模式。

## SELECT 与绑定变量

SELECT 直接调用 query，使用当前数据库的表、字段与行权限。可同时提供 parameters 绑定 SQL 中的 :arg_名称，身份参数由服务端提供。例如：

```json
{"sql":"SELECT customer_no,name FROM customers WHERE customer_no=:arg_customer_no","parameters":{"arg_customer_no":"C0001"}}
```

不再提供命名查询、查询模板或公共 SQL 片段编辑器。旧命名查询不再注册为 MCP 工具；旧片段只在离线一次性迁移时展开为每张表完整 SQL，保留身份绑定；运行时不接受片段配置。

## HTML 视图

「视图配置」位于「知识配置」和「MCP 配置」之间。每库配置完整 HTML，通过标准 `resources/list` / `resources/read` 发布，`_meta.type` 为 `view`；查询和动作可以关联已发布视图。内置通用查询、动作预览和证券账户模板，证券安装器同时安装并关联案例。配置、宿主桥和完整演示见 [HTML 视图与证券案例](docs/views.md)。

## 业务操作 Action

「MCP 配置」中新增业务操作编辑与始终回滚的试运行；显式启用写入后，业务账号可通过 `list_actions` / `describe_action` / `invoke_action` 使用受控 JavaScript 操作。框架负责 SQL 参数绑定、权限查询、事务、超时、运行期内存幂等、预览、本人登录确认及审计。配置、完整示例和故障恢复见 [业务操作 Action](docs/actions.md)。证券演示库可运行 `make install-demo-actions` 安装 18 种现成操作；覆盖表、37 个正常/拒绝案例及账号说明见 [证券 Action 案例](docs/securities-actions.md)，数据库结构和发布方式见 [发布清单](docs/securities-actions-release.md)。

## 业务知识与渐进式披露

业务 YAML 分文件已落地，配置克隆的约定见 [业务管理配置下一阶段方案](docs/management-storage-plan.md)。该文档明确区分规划与当前实现，以下介绍当前可运行功能。

工具区另提供「动作工作台」：获取动作列表 → 选择动作查看参数 Schema → 填写参数与业务身份 → 试运行。各步显示请求、响应和耗时，试运行展示结果及 SQL 写入计划并始终回滚；同一 request_id 重用结果。查看需要 `action.write`，试运行另需 `impersonate` 和已启用写入。工作台使用平台权限模拟，正式提交仍通过业务 MCP 的 `invoke_action` 和人工确认流程，详见 [业务操作 Action](docs/actions.md)。

“知识管理”采用左侧列表、右侧编辑和保存，description 使用三行编辑，正文支持 Markdown 预览。业务知识统一通过标准 MCP Prompts 提供：prompts/list 分页返回名称、ID 和完整 description，prompts/get(name) 将同一正文包装为 user 文本消息；无提示词模板参数，不向业务 MCP 发布 search_knowledge/read_knowledge 工具。视图通过标准 MCP Resources 提供。

知识工作台按 prompts/list → 选择 name → prompts/get 展示参数、JSON 与耗时，并支持加载下一页及正文预览。工作台以当前平台的 knowledge.read 权限模拟；业务 MCP 使用本库业务身份执行同样的发布状态和可见性检查。知识只按数据库归属、发布状态与 knowledge.read 授权，不关联表，也不依赖 query；本库知识读者均可读取已发布的名称、描述与正文。知识 Prompts 开关与工具开关分别配置。旧知识工具开关兼容映射到知识开关，保存时清理旧工具项。

## 审计与一致性

审计通过有界内存队列异步写入独立 audit.db，文件不可写、队列满或关闭均不影响业务查询与 Action。记录包括 request ID、平台用户、业务身份、入口、工具、策略版本、原始 SQL、改写 SQL、执行 SQL、参数、时间、返回行数、截断和错误。

覆盖普通查询、改写预览、全量只读查询、分页浏览自动生成的 SQL，以及 `deny_unless` 的前置检查。SQL 解析失败、权限拒绝、超时也有记录。MCP HTTP 层在认证前记录 SQL 请求，因此撤销令牌后的拒绝请求仍可追踪。普通查询结果数据正文不保存；Action 另记录脚本返回值和写入计划；密码、会话令牌和 OAuth 凭据不进入操作审计。

管理变更提交后投递审计事件；事件 ID 去重。数据库配置、账号与知识保留当前 revision 用于并发保存检查，不再生成配置历史版本。审计写入失败会记录服务端警告和丢弃计数；进程异常退出时队列中的事件可能丢失。Action 幂等回执只保存在 MCP 进程内存，API 通过本地 Unix socket 访问同一运行实例的回执；同一实例存活期间防重复，MCP 重启后全部清空，独立实例不共享。重启后重试旧 request_id 可能重新执行，必须先核对业务数据。当前不自动删除审计，备份和保留周期由部署方安排。

## 旧版本迁移与备份

本次为不兼容升级。先停止 API 与 MCP，再执行 `go run ./cmd/migrate-state -data-dir data`（或 `make migrate-state`）。命令从现有逐库 `management.db` 生成一致性快照，在 `data/legacy/yaml-<时间>/` 留存原配置、历史版本及密钥，再逐项校验并发布拆分 YAML。原业务 `management.db` 保留为历史文件，升级后只读写 `management/`；服务启动不会自动迁移、读取或创建业务 SQLite。更早的全局业务配置须先用上一版完成逐库拆分。

API 与 MCP 在同机共享目录，读写使用文件锁；配置提交通过完整暂存目录及持久提交日志恢复，revision / ETag 检查拒绝过期保存。新建空环境仍可一次性导入初始规则，完成后 YAML 是逐库配置的唯一来源。详见 [YAML 升级与验收](docs/management-yaml-release.md)。
旧 OAuth 客户端注册可迁移到管理库；旧员工身份的授权码、刷新令牌不能映射成平台授权，需要重新登录。旧演示统一密码与无密码员工模拟不用于新平台。

升级前备份配置目录、业务数据、旧 OAuth 状态及私钥。升级后整体备份全局 management.db、所有 management/ YAML、全局与逐库 audit.db、OAuth 私钥及各库 credentials.key。停服后整体复制，或使用持锁的 Source.Backup 导出逐库 YAML；SQLite 必须使用一致性快照，不能只复制正在写入的主文件。文件锁只适用于同机共享本地文件系统。

## 业务演示与验证

证券演示库仍支持客户、资金账户、证券账户、交易与持仓等权限改写。结构和说明见 [DDL](sql/schema.sqlite.sql)、[数据字典](docs/data-dictionary.md)、[业务权限说明](docs/permissions.md)。`make init-shop` 可生成三张表的小型商店库。

```sh
make test          # 连接器 VIEW 回归测试 + Go 全部测试 + 前端 TypeScript 检查
make build         # 两个服务器、数据生成器与前端构建
```

新增集成测试使用隔离临时库，覆盖后台登录、越权与身份冒用、跨资源令牌、权限即时撤销、密码重置撤销 JWT、真实管理 MCP、业务工具参数注入、知识描述与全文披露、并发版本冲突、内部库隔离、逐库迁移、企业认证、内存运行状态与审计故障隔离。旧证券演示验收通过独立测试运行，不作为后台公共接口开放。

## 标准连接器

`deploy/connectors/datascope/` 提供业务查询连接器，`deploy/connectors/datascope-admin/` 提供携带管理 skill 的管理连接器；两者独立授权。标准 MCP 连接器配置中，`connector.json` 定义连接器信息，`mcp.json` 定义服务地址与传输方式。使用前按实际部署地址修改 `mcp.json`；支持 MCP 的客户端可直接配置服务地址并完成 OAuth 授权。

### 业务查询连接器的 SQL VIEW

业务查询连接器附带 `sql` 显示视图（`view.json`、`views/`），供 Agent Platform 在对话中渲染可修改变量的查询结果。模型在回复中写不带 `query=` 的数据块，正文给出 SQL 原文和变量定义；SQL 中的变量用裸写的 `{{name}}` 占位：

````markdown
```view key=datascope/sql
{"title":"客户日终余额",
 "sql":"SELECT c.customer_no,c.name,b.fund_account_no,b.closing_balance/100.0 AS closing_balance_yuan FROM customers c JOIN daily_balances b ON b.customer_no=c.customer_no WHERE c.customer_type={{ctype}} AND b.trade_date={{day}} AND b.closing_balance>={{min}} ORDER BY b.closing_balance DESC",
 "variables":{
   "ctype":{"type":"string","label":"客户类型","default":"INDIVIDUAL","options":[{"value":"INDIVIDUAL","label":"个人"},{"value":"CORPORATE","label":"机构"}]},
   "day":{"type":"date","label":"交易日","default":"2026-10-20"},
   "min":{"type":"integer","label":"最低余额（分）","default":0}},
 "maxRows":200}
```
````

- 变量类型为 string、integer、number、date（`YYYY-MM-DD`）和 boolean，每个变量必须有 `default`；`options` 以下拉框展示可选值。
- 视图打开后立即用默认值填入 SQL 并通过 `query` 工具执行；用户修改变量后重新填写并查询。输入留空表示使用默认值，“恢复默认”清除全部修改。修改的值不保存，历史中只保留原始块。
- 视图按类型生成 SQL 字面量：字符串加单引号并转义，负数加括号，布尔写为 1/0；占位符不能写在引号或注释里，未声明的占位符直接报错。最终 SQL 仍由服务端解析并按权限改写，视图只调用 `query`，不使用 `raw_query`。
- 数据库需启用 `query` 工具，并在该数据库的独立业务账号域中为员工账号配置业务身份。连接器 ID 当前为默认库的 `datascope`；按库拼装的连接器下载尚未提供。
