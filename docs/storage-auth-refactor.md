# 本地存储、认证与管理界面

业务管理配置已切换为分目录 YAML。全局平台身份与认证提供方继续使用 SQLite，业务数据库结构和数据保持原状。本次是不兼容升级，必须停服后显式迁移；详见 [发布与验收](management-yaml-release.md)。

## 文件布局

```text
data/
  management.db                  # 平台用户、全局认证、数据源目录、OAuth 客户端
  audit.db                       # 全局审计，独立 SQLite
  databases/<database_id>/
    management/
      database.yml               # 连接、开关、当前 revision、结构扫描元数据
      authentication.yml         # 全局策略引用或本库独立认证连接
      account.yml                # 本地业务账号、密码哈希、账号 revision
      grant.yml                  # 平台逐库授权与业务身份绑定
      tables/<table>.yml          # 完整权限 SQL、结构快照、表和字段说明
      actions/<action>.yml        # 描述、参数与 JavaScript
      knowledges/<knowledge_id>.yml # 完整描述与正文，Prompts 使用同一份内容
    audit.db                     # 逐库审计，独立 SQLite
    credentials.key              # 独立 AES-256-GCM 密钥（如已生成）
    management.db                # 仅升级前历史文件，运行时不读取、不创建
  oauth/signing.pem
```

DataScope 自有 YAML 字段使用大写下划线并以 `_` 结尾，如 `REVISION_`、`USER_ID_`、`DESCRIPTION_`。API JSON 字段契约保持不变。业务表名、字段名、Action 参数名、业务标识保留原值及大小写；SQL、JavaScript、DDL 和知识正文采用 YAML 多行文本。

`database.yml` 的 `DATABASE_` 保存原数据库配置中其余设置；`READ_PASSWORD_CIPHERTEXT_`、`WRITE_PASSWORD_CIPHERTEXT_` 为专用密文字段，接口与审计均不返回。认证的 `AUTHENTICATION_` 与原 authentication JSON 对应，`POLICY_ID_` 保存逐库全局策略绑定，空值跟随全局默认。`account.yml` 文件使用 `ACCOUNTS_` 列表，`grant.yml` 使用 `GRANTS_` 列表。表文件的 `POLICY_`、`STRUCTURE_`、`ANNOTATION_` 分别保存查询权限、登记结构和人工说明；当前顺序由数据库中的 `TABLE_ORDER_` / `ACTION_ORDER_` 维护。

结构扫描状态、待确认结构及最近变化报告只存扫描进程内存；重启清空，需要重新扫描后确认。已登记结构与说明放入对应表文件。知识只保存到 knowledges/，不在 database.yml 复制正文。无 template/、fragment.yml、version/、state/ 或独立 prompts/。不再持久化配置历史，新配置仅保留 revision；升级前完整历史留在 SQLite 备份。

## 读写、并发与恢复

API 和 MCP 必须指向相同全局 management.db 与逐库目录，并运行于支持 flock / 原子 rename 的同机本地文件系统。每次读取重新加载当前 YAML，无磁盘 SQLite 缓存或双写。账号、授权、连接密码、知识搜索都从 YAML 读取；搜索是当前内容的内存派生检索，不生成 FTS 数据库。

保存先取得全局 `.config.lock`，从磁盘读取最新 revision，拒绝过期数据库 / 知识 / 账号保存。数据库配置额外使用 ETag，手工编辑 YAML 后旧编辑窗口也不能覆盖它。逐库 `.management.lock` 在目录之外，不会随目录发布而变化，读写进程共享此锁。

多文件保存先生成权限 0700 的暂存目录，逐个写入权限 0600 的 YAML，fsync 并回读比对全部内容。目录发布的 `.next` 是持久提交决定：旧目录移到 `.previous` 后发布完整新目录；进程中断时，下一个读写者持锁继续发布，再读取配置。未完成的暂存目录不会被读取。

源目录更新、全局认证配置与多个库的认证绑定还通过全局 `management.db.configuration-pending` 提交日志协调 YAML 和 SQLite。提交前校验所有生成目录，持久意图落盘后只能继续完成；重启或另一进程读取时自动恢复。故障时日志保留以便续作，磁盘恢复后重试；禁止手工删除仍待恢复的提交日志。正常提交后移除日志与临时目录。它不存 Action 回执，不是第二份运行时配置来源。

手工改配置应停止两个服务、整体备份后修改并增加 `REVISION_`；正常运行使用 Web 或管理 MCP，以获得锁、校验、并发检查与审计。

## 迁移、备份与回退

```sh
# 停止 API 与 MCP 后：
go run ./cmd/migrate-state -data-dir data
# 或 make migrate-state
```

输入是上一个版本已拆分的逐库 management.db。更早的全局业务记录先使用上一版命令完成拆库。新服务不自动升级业务 SQLite，遇到未迁移或缺失的 YAML 直接报错，不运行旧 SQLite 兼容路径。

迁移使用 VACUUM INTO 生成包含已提交 WAL 状态的一致性快照，先执行完整性检查，再转换。`data/legacy/yaml-<UTC时间>/` 保存全局管理快照、每个源的原管理快照、credentials.key 和非敏感迁移报告；原业务 management.db 也保留。迁移保留 ID、当前 revision、密码哈希、认证连接和绑定、授权、Action 脚本和参数、结构说明及知识内容。公共片段展开进每张表的完整 SQL，`${user_id}` 等参数绑定保持原文。旧命名查询不恢复为新功能，原内容留在历史快照。

连接密码使用原密文和原逐库密钥，发布前验证能够解密；不会在日志、迁移报告或接口打印密码。每份新 YAML 发布前逐项回读比对，失败不覆盖旧配置。重复运行只校验已存在的 YAML，不再导入旧业务 SQLite；全局旧 DATABASE 认证绑定在迁移完成后移除，绑定只由 authentication.yml 提供。

备份完整环境包括业务数据、全局 management.db、全体 management/、逐库 credentials.key、OAuth signing.pem，以及需要保留的全局和逐库 audit.db。先停服后整体复制；在线逐库 YAML 备份使用持锁的 `Source.Backup`，演示 Action 安装器已使用它。SQLite 在线备份必须使用快照，不能只复制 WAL 模式的主文件。

回退必须停止新服务，恢复同一次备份的全局管理快照、逐库 SQLite 与密钥，并运行上一版二进制。升级后的 YAML 修改不会自动写回历史 SQLite，回退前应单独保存新增设置；不要让新旧二进制同时访问此目录。

## 全局管理、认证与业务权限

全局 management.db 保持 `PLATFORM_USER`、`PLATFORM_PERMISSION`、`SOURCE_REGISTRY`、`AUTH_PROVIDER`、`AUTH_POLICY`、`AUTH_SETTING`、管理入口的 `AUTH_BINDING`、`OAUTH_CLIENT`、`SCHEMA_VERSION`。逐库账号、授权、配置和历史不再进入全局库。历史输入表格式见 [旧业务管理 SQL](schema/source-management.sql)，当前全局结构见 [全局 SQL](schema/global-management.sql)。

业务认证复用全局提供方（`GLOBAL`），或在本库 authentication.yml 保存完整独立连接（`DATABASE`）。独立提供方缺失或验证失败直接拒绝，不回退。本库认证变更只影响本库，不改全局认证，也不使平台管理会话退出；修改全局认证 revision 仍使依赖它的旧令牌失效。

| 认证方式 | 提供方 | 身份来源 |
| --- | --- | --- |
| ACCOUNT | PLATFORM_ACCOUNT | 平台用户，仅管理入口 |
| ACCOUNT | BUSINESS_ACCOUNT | 当前库 account.yml |
| ACCOUNT | ENTERPRISE_ACCOUNT_API | 企业账号密码验证接口 |
| OAUTH | DEMO_OAUTH | 当前库本地业务账号 |
| OAUTH | ENTERPRISE_OAUTH | 企业授权、令牌、UserInfo 地址 |
| NONE | 无 | 配置的固定业务身份及能力 |

企业提供方字段、HTTPS 与 loopback HTTP 限制保持现有接口约定。企业账号接口接收 POST JSON username/password，返回可信 sub、username、employee_id、name、active；身份与能力不由客户端自行指定。`subject_claim` 默认 sub，`business_id_claim` 默认 employee_id。企业 OAuth 使用 code + S256 PKCE、一次性 state，再请求 UserInfo。企业密钥仍只引用 `DATASCOPE_AUTH_*` 环境变量。

`POST <OAUTH_ISSUER>/auth/databases/<id>/token` 接受本库账号密码，签发绑定数据库和认证配置的一小时 Bearer Token。账号停用、密码重置、权限与身份修改会增加账号 revision 并撤销旧令牌。平台管理入口必须保留有效认证。平台用户逐库权限由 grant.yml 维护，业务账号与平台用户相互独立。

SELECT 直接使用 SQL、绑定参数和查询权限；DML 通过 Action，脚本内部可以 SELECT 校验。知识只由 prompts/list 与 prompts/get 读取同一 knowledges/ 内容，仍按发布状态和当前库身份过滤；Resources 留给未来 view。配置克隆及知识关联的产品改造不在本轮范围。

## Action 内存和审计

Action 回执、幂等预约、确认令牌、执行结果只由 MCP 进程内存持有，API 通过权限 0600 的私有 Unix socket 共享。重启清空，不写入 YAML、global management.db 或 audit 表。多个独立实例不共享回执。详见 [Action](actions.md)。

审计仍独立使用 SQLite `AUDIT_EVENT`，见 [审计 SQL](schema/audit.sql)。有界内存队列异步投递 SQL、权限、认证、配置和 Action 事件；队列满、文件不可写、关闭或写入失败不回滚或拒绝业务。查询不保存结果正文，密码和令牌不进入审计。

管理界面的存储详情现在显示 YAML 管理目录和独立审计路径。业务认证、查询权限、Action 和知识页面保留现有流程，不新增模板、公共片段或额外设置入口。配置克隆见 [配置克隆](configuration-clone.md)。
