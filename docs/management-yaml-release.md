# 业务管理配置 YAML 升级与本地验收

本次将业务管理配置从逐库 management.db 切换为拆分 YAML。全局 management.db、全局及逐库 audit.db 继续使用独立 SQLite。业务数据库结构、业务数据、HTTP/API JSON 字段及现有 SELECT、Action、Prompts 的职责保持原有约定。

## 升级命令

停止 API 和 MCP 后执行：

```sh
go run ./cmd/migrate-state -data-dir data
# 或使用构建产物：
./bin/migrate-state -data-dir /path/to/data
```

命令输入是上一个版本已拆分的 `data/databases/<id>/management.db`。更早的全局业务配置先使用上一版完成拆库。新版本服务不会自动迁移或使用旧业务 SQLite；没有完整 YAML 时拒绝启动。新建数据源直接写 YAML，不创建业务 management.db。

每库发布前：

1. 用 VACUUM INTO 生成包含已提交 WAL 状态的一致性快照，并执行 integrity_check。
2. 备份全局管理库、原逐库管理库、独立 credentials.key；原业务文件保持可恢复。
3. 读取当前连接、认证、账号、授权、表权限、结构说明、Actions 和知识，保留现有 ID、revision、密码哈希及密文。公共片段展开到完整 SQL，绑定变量保持不变，循环或缺失引用直接失败。旧命名查询只留在原始备份。
4. 写入完整暂存目录，逐文件 fsync，再回读比较全部序列化内容，通过后整体发布。数据库连接密文还需通过原密钥解密验证，密码和密文不进入迁移报告或 API 返回。
5. 写入逐库 `.management-yaml-migrated` 标记。重复执行只验证当前 YAML，必要时备份并清理已退役的派生字段，不重新导入历史 SQLite。已迁移库的 YAML 丢失时要求恢复 YAML 备份，不能悄悄恢复过期管理库。

## 实际目录和结构变化

```text
data/databases/<database_id>/
  management/
    database.yml
    authentication.yml
    account.yml
    grant.yml
    tables/<table>.yml
    actions/<action>.yml
    knowledges/<knowledge_id>.yml
  credentials.key
  audit.db
  management.db                  # 历史原文件，新运行时不打开
  .management-yaml-migrated       # 一次性升级标记
```

自有字段使用 `ID_`、`USER_ID_`、`DESCRIPTION_`、`REVISION_` 等大写下划线格式；业务字段、Action 参数名和业务标识保留原值和大小写。SQL、DDL、JavaScript 和知识正文使用多行 YAML。账号密码仍是哈希，数据库密码仍是独立密钥加密的密文。源码、接口或日志没有新增明文密码存储。

表的正式结构快照与人工字段说明进入对应 tables/ 文件。知识正文只保存一份，prompts/list 与 prompts/get 使用它。无 template/、fragment.yml、version/、state/ 或额外 prompts/。不再生成配置历史快照；当前配置保留 revision 和 ETag 并发检查。派生搜索及扫描候选的内存收敛由并行清理任务完成，正式结构说明仍持久化。

API 与 MCP 每次读取当前 YAML，使用同机文件锁协调。多文件更新通过完整目录发布恢复；涉及全局目录或认证的操作另有持久提交日志协调 SQLite 和 YAML。审计失败不影响业务。Action 回执、幂等预约及确认令牌仍只存在 MCP 内存，API 通过私有 Unix socket 共享，重启清空。

## 本地迁移结果

2026-10-31 已在本项目实际 data/ 完成升级，迁移前后数据库 revision 相同：

| 数据库 ID | 表权限 | Actions | 业务账号 | 平台逐库授权 | 知识 | revision |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| db_8496052b337ee994bbe0f779 | 37 | 0 | 1 | 1 | 0 | 5 |
| default | 17 | 18 | 100 | 0 | 11 | 48 |
| shop_demo | 3 | 0 | 0 | 0 | 0 | 46 |
| 合计 | 57 | 18 | 101 | 1 | 11 | — |

原始一致性快照位于 `data/legacy/yaml-20261031T100058.407684000Z/`，内含 `global-management.db`、各库 `management.db`、原密钥和迁移报告。后续在两个验收服务停止后，清理旧派生 YAML 字段前的配置副本位于 `data/legacy/yaml-20261031T124212.459154000Z/`。以上文件均为本地运行数据，不进入 Git。

## 验证证据

- 完整 `go test ./...` 通过，覆盖既有管理 API、MCP、OAuth、查询权限、Action 执行与 Unix socket 确认流程；共享工作区的配置克隆集成测试也已通过。
- `npm run build` 通过，含 TypeScript 检查。存储详情显示逐库 management/ YAML 目录与独立审计库路径。
- `node --test deploy/connectors/tests/*.test.cjs`：16 项通过。
- 新增管理存储测试覆盖：一致性迁移、SQL 片段展开、账号 ID / 哈希 / revision、授权与认证绑定、Action 参数名及脚本、知识多行正文、完整 int64 ID 精度、连接密文验证、回读校验、发布中断恢复、全局提交日志恢复、真实第二个 OS 进程读写及过期账号 revision 拒绝。
- 已迁移业务 management.db 被替换为不可读格式的测试中，新进程仍能认证并读取知识，重复迁移也不读取旧库；YAML 丢失时，一次性标记阻止重新导入历史 SQLite。
- 同一密钥下将连接密文移到其他已登记业务库，解密仍被数据库 ID 的 AAD 绑定拒绝；丢失密钥不能自动覆盖已有密文。
- 使用真实 data/ 同时启动 API（8081）和 MCP（8083），平台登录成功，3 个库的路径、表权限、Action 数量及 revision 与迁移报告一致。接口响应检查未发现密码哈希或连接密文字段，逐库 OAuth 资源发现正确。
- 文件句柄检查确认两个服务只打开全局 management.db、独立 audit.db 和业务数据文件，没有打开任何旧逐库 management.db。私有 actions.sock 已由 MCP 创建。验收启动的两个进程已停止。
- `install-demo-actions -check` 在迁移后检查成功：18 个证券案例及 E0001、E0121、E0175 三个已有账号；未重新安装或扩大授权。

## 发布与回退

发布时携带全局 management.db、完整逐库 management/、独立 credentials.key、OAuth signing.pem、部署配置及需保留的 audit.db。运行中 SQLite 备份必须使用一致性快照；逐库 YAML 在线备份可使用持锁的 Source.Backup，安装器已适配。

回退需停止新服务，恢复同一批次的全局和逐库 SQLite 快照与密钥，运行上一版二进制。升级后产生的 YAML 修改不会写回历史 SQLite，回退前单独保存。禁止新旧二进制同时运行。

配置克隆和运行状态清理由其他独立任务实现；本次 YAML 任务不改变知识关联或其他设置的产品设计。没有新建磁盘业务 SQLite 缓存或双写路径。
