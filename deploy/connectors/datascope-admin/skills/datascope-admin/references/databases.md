# 数据库与 MCP

所有调用使用 `{action,args}`；下表参数均位于 args，另加对应 `resourceType`。除列出的可选参数外，其余参数必需；不接受 null 或无关参数。数字使用 JSON number，布尔值使用 true/false。

| 工具 | resourceType | action | 参数 | 权限 |
| --- | --- | --- | --- | --- |
| datascope_query | database | list | 无 | 返回当前用户有权限的数据库 |
| datascope_query | database | get | database_id | config.read |
| datascope_manage | database | create | database；可选 password | databases.create |
| datascope_manage | database | update | database_id、database | config.read + config.write，仅改名称 |
| datascope_manage | database | save | database_id、database | config.read + 实际变更部分的权限 |
| datascope_manage | mcp | save | database_id、database | config.read + mcp.write |
| datascope_inspect | database | test_connection | database_id | config.read |
| datascope_inspect | database | preview_clone_configuration | database_id、clone | 来源 config.read + 目标所选项写权限 |
| datascope_manage | database | clone_configuration | database_id、clone | 同预览；重新校验 |

发现数据库：

```json
{"action":"list","args":{"resourceType":"database"}}
```

读取配置：

```json
{"action":"get","args":{"resourceType":"database","database_id":"default"}}
```

读取结果将数据库字段直接放在返回对象顶层，并附带 mcp_endpoint、permissions、business_user_id。构造 `args.database` 时只复制数据库字段：id、name、driver、path、connection、mcp_enabled、mcp_path、tools、config、revision，以及返回时存在的 etag。不要把整份返回对象直接作为 database 传回。

创建数据库只需传 `database:{name,driver:"sqlite",path}，id 可省略，由服务端自动生成`，path 是 DataScope 服务端已有 SQLite 文件的路径，必须在服务端允许的数据根内。客户端本机路径不一定属于服务端。创建后服务端生成策略，默认关闭 MCP、禁用所有表，并预选 list_tables、describe_table、query 三个基础工具；回读实际配置再准备策略，不能假设传入策略已生效。

已有数据库修改时，database.id 必须等于 database_id，database.revision 必须是最近回读的正整数版本。update 只采用 database.name；mcp.save 只采用 database 中的 mcp_enabled、mcp_path、tools；policy.save 见策略文档。其他字段不随局部动作修改。使用完整 database.save 时保留当前配置，服务端按名称、策略、MCP 部分的实际差异逐项检查 config.write/policy.write/mcp.write；文件路径与驱动保持原值。

配置 MCP 前先完成策略并验证。基础 tools 允许 list_tables、describe_table、query、raw_query、customer_scope、list_actions、describe_action、invoke_action；raw_query 绕过行字段策略且需要单独权限，customer_scope 仅适用于证券样例库，按用户需要选择。mcp_path 是地址路径段，admin 为保留名称。不要在没有用户需求时开放全量查询。


保存后重新 database.get 核对 revision 和目标字段。test_connection 检查保存配置下的数据库连接，不证明 OAuth 登录、所有业务 SQL 或数据库 MCP 均可用。管理地址的授权不能复用于数据库地址。


数据库 authentication 可设为 `{type:"local",label:"员工认证",employee_directory:false}`，由 DataScope 为本库模拟独立的员工账号认证；省略配置时也使用本库独立业务账号，不支持平台账号登录业务 MCP。通过 database.save 保存完整数据库配置及最新 revision，authentication 只能由系统管理员修改。authentication_revision 由服务端维护，不要自行设置或回退。每个数据库有各自 issuer，MCP 资源发现自动返回相应登录入口；账号通过 business_account.list/save 单独维护，不创建平台用户。企业认证提供方由全局系统设置维护；`authentication.access` 可为本库单独指定认证策略：`{id:"database-auth",name:"本库业务认证",mode:"NONE"|"ACCOUNT"|"OAUTH",provider_id:"提供方ID",permissions:["query"],business_user_id:0}`。NONE 不指定 provider_id；ACCOUNT 支持 BUSINESS_ACCOUNT / ENTERPRISE_ACCOUNT_API；OAUTH 支持 DEMO_OAUTH / ENTERPRISE_OAUTH。策略保存在本库 management/authentication.yml，未设置时沿用全局绑定。账号密码可经 `POST /auth/databases/<database_id>/token` 换取一小时有效的 Bearer Token。


## 网络数据库

支持 mysql、postgresql、oracle、dameng，创建时使用 `connection` 对象代替 `path`：host、schema、username；port 可省略，自动采用对应默认端口；MySQL/PG 还需 database，Oracle 需 service_name。MySQL schema 与 database 相同。密码可直接通过 args.password 传入，服务端加密保存；不要将密码放入 database 或 connection。旧 connection.password_env 继续兼容，必须为 DATASCOPE_DB_ 开头的大写变量名。两种密码来源二选一，禁止 DSN。MySQL/PG 默认 TLS verify-full，可信开发网络可显式 tls_mode=disable。

未保存连接测试：`datascope_inspect`，`{action:"test",args:{resourceType:"connection",database:{name,driver,connection},password:"实际密码"}}`，仅系统管理员可用。已有数据源继续使用 database.test_connection。创建后仍需回读，所有表默认禁止、MCP 关闭。connection 与驱动/目标登记后固定；保存策略需保留读取到的连接配置。

网络查询与权限 SQL使用受限 SELECT 子集。策略中的 main.表名映射到配置的 schema；不支持的语法会拒绝，不能绕过改写执行原 SQL。详细范围见仓库 docs/databases.md。

## 自动侦查与人工映射

新建数据源自动提取全部表和字段并建立快照，全部表默认禁止、字段为当前显式白名单。管理 API 默认每 5 分钟自动检查结构变化；DISCOVERY_INTERVAL=0s 可关闭。扫描只读源库，不复制业务数据。表内容继续使用后台分页浏览。

- 读取：`datascope_query`，action=`get`，args=`{"resourceType":"discovery","database_id":"目标 ID"}`。返回数据库配置、discovery.tables 原始结构、annotations 人工覆盖和 reports 最近 20 次结构变化报告。
- 检查结构变化：`datascope_manage`，action=`refresh`，args=`{"resourceType":"discovery","database_id":"目标 ID"}`。需要 config.read + config.write；返回 discovery.pending 候选及 changes；没有确认前不改变结构或权限。扫描失败保留旧快照。
- 确认更新：`datascope_manage`，action=`apply`，args=`{"resourceType":"discovery","database_id":"目标 ID","id":"pending.id","revision":当前配置版本,"resolutions":[{"table":"表名","column":"字段名","follow_source":true}]}`。需要 config.read + config.write + policy.write。resolutions 可省略，默认保留人工说明；follow_source=true 对对应注释采用数据库新值。确认前必须向用户展示对比并获得确认；服务端重新检查源结构，拒绝过期候选。新增表关闭、字段白名单不自动扩张。
- 保存映射：`datascope_manage`，action=`save`，args=`{"resourceType":"annotation","database_id":"目标 ID","table":"物理表名","column":"物理字段名","revision":当前数据库版本,"annotation":{"display_name":"展示名称","comment":"人工说明"}}`。column 省略或为空表示表。comment=null 表示跟随源注释，comment="" 表示明确清空说明。需要 config.read + config.write；整项替换当前对象的映射，保留既有值时需带回 display_name 和 comment。

结构快照和人工覆盖不能通过 database.save 修改。扫描会保留人工覆盖，源注释变化导致冲突时展示旧值、新值和人工值；取消该项 comment 覆盖即可采用数据库新说明。扫描不自动修复行过滤 SQL 或业务工具，也不比较业务记录内容。


认证连接来源：`authentication.source` 为 `GLOBAL`（或省略）时引用全局提供方；为 `DATABASE` 时必须同时给出 `authentication.provider` 和 `authentication.access`，且 `access.provider_id` 与本库提供方 ID 一致。独立提供方字段与全局 AuthProvider 相同，支持 BUSINESS_ACCOUNT、DEMO_OAUTH、ENTERPRISE_ACCOUNT_API、ENTERPRISE_OAUTH，禁止 PLATFORM_ACCOUNT。无需认证时使用 GLOBAL 且省略 provider / provider_id。本库连接只保存在该库的 management/authentication.yml，失败不回退；企业密钥使用 DATASCOPE_AUTH_* 环境变量名。保存仍需要系统管理员和最新数据库版本。OAuth 资源发现自动发布最终选定的 issuer。

知识使用独立 knowledge_enabled 开关，通过 prompts/list 和 prompts/get 提供，不是 tools 里的查询模板。旧 search_knowledge/read_knowledge 工具项仅用于读取旧配置时映射开关，业务 MCP 不再发布它们。

## 克隆配置

当前 database_id 是目标库。先调用 `datascope_inspect`，`action:"preview_clone_configuration"`，args 包含 `resourceType:"database"`、`database_id` 和 `clone:{source_database_id,scope:{tables:true,policies:true,actions:true,knowledge:true}}`。省略 scope 默认四项全部选择。返回覆盖摘要、issues、warnings、source_revision、target_revision、source_token、target_token、confirmation。展示摘要并得到用户确认；issues 非空不能执行。

确认后调用 `datascope_manage`，`action:"clone_configuration"`，使用相同 args.clone，补齐上述六个版本/确认字段及 `confirmed:true`。配置或目标结构变化需要重新预览。同名表配置覆盖、目标独有表保留；Actions 和知识整组替换，包含删除目标独有项。知识 ID 保留、归属改为目标库。保留目标连接/密码/密钥、ID/名称、认证、账号、人员授权和身份绑定、MCP 与写入开关、审计和回执；不复制业务数据。静态依赖检查不证明 JavaScript / SQL 的业务兼容性。
