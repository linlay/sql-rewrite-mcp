> 当前发布采用逐库 management/ YAML 与独立 audit.db，以下旧版本表名仅作历史说明。新的文件布局、迁移命令及完整发布清单以 [存储与认证改造](storage-auth-refactor.md) 为准。

# 证券 Action 数据库结构与发布清单

## 本次结构变化结论

**证券业务数据库没有新增表、字段、索引、触发器或约束。** 已逐项比较当前 `data/securities_demo.sqlite3` 的 sqlite_schema 和仓库 `sql/schema.sqlite.sql`，17 张业务表及其索引结构完全一致。

18 个 Action 通过现有字段实现。需要告知的业务语义变化是：`fund_accounts.status` 允许演示状态 `frozen`，与原有 `active` 配对；该列原本就是未限定枚举的 TEXT，因此无需 ALTER TABLE。冻结后演示资金操作和开户/删户会拒绝执行，解冻恢复 active。初始数据中的账户仍为 active，只有本人确认冻结才会实际写入 frozen。

资金流水使用现有 `cash_flows` 的 DEPOSIT/WITHDRAWAL 类型，新增记录并同步现有 `daily_balances`；没有新建 withdrawals、account_events 或余额字段。价格更正使用现有 `daily_quotes` 和 `positions`，也没有新增表。安装过程不提交任何业务记录的修改；回归只在隔离库提交，完整业务数据复核也先复制再预览。

## 管理数据库的变化

Action 框架曾为 `management.sqlite` 新增下表，服务启动自动执行 CREATE IF NOT EXISTS，不需要手工迁移业务库：

```sql
CREATE TABLE IF NOT EXISTS action_requests(
  database_id TEXT NOT NULL,
  actor TEXT NOT NULL,
  request_id TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  revision INTEGER NOT NULL,
  state TEXT NOT NULL,
  data TEXT NOT NULL,
  token TEXT NOT NULL DEFAULT '',
  expires INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(database_id,actor,request_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS action_confirmation_token
  ON action_requests(token) WHERE token<>'';
```

本次安装案例包没有额外增加管理表。改变的是已有表中的数据：

| 位置 | 保存内容 |
| --- | --- |
| records，bucket=databases | default 数据库的18个 Action、write_enabled、list_actions/describe_action/invoke_action 启用状态及配置版本 |
| records，bucket=knowledge | 证券 Action 业务知识指南，ID 为 securities-actions-guide |
| record_versions | 上述配置和知识的历史版本 |
| business_accounts | E0001、E0121、E0175 追加 action 能力并增加账号版本，密码不变 |
| outbox_events / 独立 audit.sqlite | 配置、授权、调用、预览、SQL 与确认审计 |

## 发布新的演示环境（推荐）

只复制 `securities_demo.sqlite3` **不会附带 Action 定义**。发布新演示时，可携带以下文件，在新环境初始化管理库后安装：

| 必需内容 | 说明 |
| --- | --- |
| data/securities_demo.sqlite3 | 完整证券业务数据，结构仍以 sql/schema.sqlite.sql 为准 |
| 新版 admin-api、mcp-server、前端 dist | 两个服务都要升级，旧程序不识别 Action 工具与权限 |
| install-demo-actions 可执行程序 | `make build` / `make build-linux` 会同时生成；18个脚本已嵌入程序，无需运行时手工读取 YAML |
| .env.example 和目标环境配置 | 设置目标目录的 SQLITE_PATH、MANAGEMENT_DB_PATH、AUDIT_DB_PATH、DATABASE_ALLOWED_ROOTS、管理员及演示账号初始密码，以及公开 OAuth/MCP 地址 |
| docs/securities-actions.md、fixtures/actions/securities.json | 完整可用案例、原始调用参数、前置条件与拒绝案例 |

步骤：

1. 在目标位置放置业务库，使用目标环境的新管理库，启动新版后台完成数据库导入、员工账号初始化和自动建表。
2. 载入目标环境变量后执行 `install-demo-actions`，或源码部署执行 `make install-demo-actions`。
3. 安装器先检查证券库结构和三个已有员工账号，再备份管理库、保存 Action、启用写连接及 MCP、授予指定账号能力，并发布业务知识指南。
4. 启动新版 MCP 与前端，业务账号重新 OAuth 授权后使用。全部 Action 均为 confirm:true。

已有本地管理库还登记了其他数据源和平台/业务账号。发布一套新演示时，新建管理库并执行安装命令，可以只带证券案例，而无需把其他数据源配置和既有登录状态一起发布。新环境需要自行设置账号初始密码。

## 迁移现有完整环境

若目标是搬迁当前环境、保留全部配置和历史，则除业务库以外，还应一致性备份并迁移：全局 management.db / audit.db、各 data/databases/<ID>/management/ YAML 与 audit.db、相应 credentials.key、OAuth 签名密钥及相应部署配置。动作回执按当前方案只存 MCP 进程内存，重启清空；旧 ACTION_RECEIPT 表不再使用。

管理库保存的是登记时的绝对业务文件路径。迁移后应保持可访问的路径/挂载，或在目标环境重新登记数据源并运行安装器；不能只复制到另一目录而保留失效的连接路径。MCP、OAuth 对外地址及数据库资源地址也要与新部署一致。

运行中的 SQLite 必须使用 SQLite backup API、VACUUM INTO 或停服后的完整一致文件，不能漏掉 WAL。安装器自动备份的是管理库，位置在 data/legacy/action-install；它不是业务库和审计库的整套备份。
