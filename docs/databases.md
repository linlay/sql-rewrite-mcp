# 多数据库接入与 Linux 部署

业务数据源可选择 SQLite、MySQL、PostgreSQL、Oracle、达梦 DM8。全局管理、OAuth 客户端注册和独立审计继续使用 SQLite，逐库管理配置使用分目录 YAML。网络数据源通过 Go `database/sql` 驱动接入，没有引入 ORM。

## 平台与版本

| 数据源 | 驱动 | 本次选定的兼容目标 |
| --- | --- | --- |
| SQLite | modernc.org/sqlite | 原有 SQLite 文件 |
| MySQL | github.com/go-sql-driver/mysql v1.9.3 | MySQL 8.0+ |
| PostgreSQL | github.com/jackc/pgx/v5 v5.7.6，stdlib 入口 | PostgreSQL 14+ |
| Oracle | github.com/sijms/go-ora/v2 v2.9.0 | Oracle 19c+，需要支持 OFFSET/FETCH |
| 达梦 | gitee.com/chunanyong/dm v1.8.23 | DM8；逐个实际版本、字符集和兼容模式验收 |

上述数据库版本为适配目标，真实连接测试通过后才能视为该环境已验收。编译成功不等于已经完成目标数据库的协议、权限或取消查询测试。

所有选定驱动可以在 `CGO_ENABLED=0` 下编译。macOS 开发时，DataScope 通过 TCP 连接远端 Linux 数据库；不要求在 Mac 上安装达梦或 Oracle 数据库服务。达梦 Go 驱动不同于 dmPython、ODBC 等客户端；本项目没有使用依赖平台原生客户端的接口。数据库服务端的系统与 CPU 支持范围由厂商对应发行版决定。

参考：[达梦 Go 编程指南](https://eco.dameng.com/document/dm/zh-cn/pm/go-rogramming-guide.html)、[达梦 Linux Go 连接示例](https://eco.dameng.com/document/dm/zh-cn/start/GO_DM_NEW.html)、[Go DM 模块来源](https://gitee.com/chunanyong/dm)。这里使用的是便于 Go Modules 引入的驱动分发仓库；生产部署前应核对目标 DM8 版本与厂商驱动要求。

## 连接配置

在管理界面点击“＋ 数据库”，填写显示名称、数据库类型、连接地址和账号密码，再测试连接、添加。地址与端口在同一行；切换类型自动填入 MySQL 3306、PostgreSQL 5432、Oracle 1521、达梦 5236，也可改为自定义端口。数据库 ID 由服务端自动生成，改显示名称不会改变权限归属或连接器身份。新增数据源关闭 MCP，所有表默认禁止访问。配置访问策略、业务账号后再开放 MCP。

每个数据源只登记一个 schema 的基础表；第一版不隐式开放视图、同义词、其他 schema 或跨数据库访问。逻辑表名不区分大小写；同一个 schema 中存在仅大小写不同的表时，登记会被拒绝。字段白名单使用结构读取返回的精确字段名。未加引号的字段和别名按目标库规则折叠大小写；带引号的字段和别名保留大小写。

MySQL 的 database 和 schema 填写相同值；PostgreSQL 分别指定 database 和 schema；Oracle 使用 service_name（不使用 SID），schema 为目标 owner；达梦指定 schema，不借用 Oracle 驱动，也不假设兼容模式等于原生 Oracle。

例：通过管理 API `POST /api/databases` 登记达梦（需要已登录的系统管理员）：

```json
{
  "name": "财务达梦库",
  "driver": "dameng",
  "password": "实际数据库密码",
  "connection": {
    "host": "10.0.0.20",
    "port": 5236,
    "schema": "FINANCE",
    "username": "DATASCOPE_READER"
  }
}
```

其他连接示例：

```json
{"driver":"mysql","connection":{"host":"10.0.0.21","port":3306,"database":"business","schema":"business","username":"datascope_reader","password_env":"DATASCOPE_DB_MYSQL_PASSWORD","tls_mode":"verify-full"}}
{"driver":"postgresql","connection":{"host":"db.example.internal","port":5432,"database":"business","schema":"public","username":"datascope_reader","password_env":"DATASCOPE_DB_POSTGRESQL_PASSWORD","tls_mode":"verify-full"}}
{"driver":"oracle","connection":{"host":"10.0.0.23","port":1521,"service_name":"FREEPDB1","schema":"BUSINESS","username":"DATASCOPE_READER","password_env":"DATASCOPE_DB_ORACLE_PASSWORD"}}
```

管理端默认直接输入数据库密码。测试连接只在内存中使用密码；接入成功后用 AES-256-GCM 加密，保存在当前库 `management/database.yml` 的 `READ_PASSWORD_CIPHERTEXT_` 或 `WRITE_PASSWORD_CIPHERTEXT_`，与连接设置一起发布。API/MCP 与审计只出现凭据引用，不返回明文或密文。两个服务读取相同的 YAML 与独立 `credentials.key`，文件权限为 0600。备份必须同时保存 YAML 和密钥；已有密文但密钥缺失时，禁止重新生成密钥。

仍兼容已有 `connection.password_env` 方式，例如 `.env` 中的 `DATASCOPE_DB_DAMENG_PASSWORD='实际密码'`，API 和 MCP 注入同名同值后使用；不能引用管理员密码等其他环境变量。直接密码放在创建请求顶层的 `password`，与 `connection.password_env` 二选一，不能把密码放入 connection。客户端可省略 id 和 port，服务端生成 ID 并补齐对应默认端口；已有手工 ID 继续兼容。

连接目标登记后固定，普通策略保存不会改变地址、schema、驱动或密码引用。更换目标或凭据引用时应新建数据源并重新配置权限，避免旧权限静默应用到另一套表。

MySQL、PostgreSQL 默认启用证书与主机名校验；可信开发网络可显式设置 `tls_mode: "disable"`。PostgreSQL 额外允许 `require`（加密但不验证身份）。当前 Oracle/达梦适配器只提供 TCP 连接，尚未接入钱包、厂商加密或客户端证书配置；需要这些能力的环境应先完成对应适配再部署。达梦驱动日志关闭，不保存 DSN。

未保存配置的连接测试使用管理动作 `test_database_connection`，参数为 `{database: {...}, password: "实际密码"}`（使用环境变量方式时不传 password），仅限系统管理员。管理 MCP 对应 `datascope_inspect` 的 `connection.test`。已有数据源继续使用 `database.test_connection`。

## SQL 与权限改写范围

网络数据源第一版接收受限 SELECT 方言。解析器仍以 SQLite 的语法树为内部表示，再由 `internal/dialect` 校验并生成目标 SQL；这不是各数据库全部原生语法的支持声明。数据库报错时不会回退到原 SQL。

- 支持单条 SELECT、WHERE、显式 JOIN ON、子查询、非递归 CTE、GROUP BY/HAVING、ORDER BY、UNION，以及非负整数 LIMIT/OFFSET。部分集合或 JOIN 形式按库拒绝。
- 支持 COUNT、SUM、AVG、MIN、MAX、ABS、ROUND、LOWER、UPPER、LENGTH、COALESCE、NULLIF、SUBSTR。内置函数的类型与空值语义仍以目标数据库为准。
- 暂不支持递归 CTE、窗口函数、用户函数、表值函数、CAST、专有运算符、索引提示、MATERIALIZED、存储过程、写入型 CTE、SELECT INTO 和 FOR UPDATE。
- Oracle 自动移除表别名的 AS，常量查询从 SYS.DUAL 读取，LIMIT/OFFSET 生成 OFFSET/FETCH。
- MySQL 每个连接使用 ANSI_QUOTES 和 NO_BACKSLASH_ESCAPES，使字符串规则与内部解析一致。
- PostgreSQL 固定 search_path 为 pg_catalog，业务表始终显式限定 schema。

权限 SQL 使用逻辑命名空间 `main.表名`，例如：

```sql
SELECT * FROM main.orders WHERE owner_id = ${userId}
```

`main` 在执行前映射为当前数据源配置的 schema。客户端查询使用未限定的表名与显式表别名。`${userId}` 和 `${asOf}` 只能由服务端提供；业务工具参数仍使用 `:arg_参数名`。生成 SQL 按出现顺序绑定参数，包括同一参数多次出现的情况。网络库的 `${asOf}` 使用带类型的时间绑定，SQLite 保持原来的字符串格式。

查询预览、策略保存、前置检查和业务工具验证都经过方言校验。`raw_query` 不进行业务权限改写，但同样只接受这一只读 SELECT 子集。演示库的 customer_scope 和员工目录校验仅适用于 SQLite 证券演示库；网络数据源使用平台托管的逐库业务账号。

网络表列表的行数来自数据库统计信息，界面显示“约”；浏览页的匹配总数通过 COUNT 查询。结构中的 DDL 是元数据摘要，不是完整的建表脚本。达梦 LOB 转换时每字段限制为 1,048,576 字符（CLOB）或字节（BLOB），超限明确报错。

## 只读运行与 Linux 构建

数据库账号只授予登记 schema 中需要的表的 SELECT 权限，不使用 SYSDBA、数据库超级用户或业务写入账号。查询执行还建立只读事务：MySQL/PG 使用只读事务选项；Oracle/达梦显式执行并检查 SET TRANSACTION READ ONLY，避免驱动不支持或忽略只读设置错误。无法建立只读事务时拒绝登记和查询。

服务端设置连接、读取、语句执行超时和返回行数上限；不同驱动的服务端取消效果仍应使用真实库验收。行数上限仅限制返回结果，不等同于限制数据库扫描或聚合计算量。

```sh
# macOS 开发环境交叉构建 Linux x86_64
make build-linux
```

当前 Linux 发布目标为 x86_64（amd64）。输出位于 `bin/linux-amd64/`，包含 admin-api 与 mcp-server；前端位于 `web/dist/`。两个进程继续共享同机的管理库和审计库，按照原部署方式配置前端与反向代理。

仅使用网络数据库时，在两个进程设置 `SQLITE_PATH=`，或传 `-db ''`。首次启动可保持空数据源列表，再从控制台登记。后续启动不要求存在 default 数据源或证券演示文件。首次初始化仍须配置 ADMIN_PASSWORD；仅证券演示库初始化才需要 OAUTH_DEMO_PASSWORD。

## 真实数据库验收

单元测试覆盖参数、SQL 边界、拒绝规则和权限改写语义；真实库测试单独启用，不会自动修改数据库或建立对象。

在专用测试 schema 使用管理员预先创建以下三行数据，再给 DataScope 只读账号 SELECT 权限。MySQL、PG、Oracle、DM 均可使用以下基础 DDL（在目标 schema 中执行，Oracle/DM 常规未引用标识符会存为大写）：

```sql
CREATE TABLE datascope_records (
  id INTEGER PRIMARY KEY,
  owner_id INTEGER NOT NULL,
  secret VARCHAR(100)
);
INSERT INTO datascope_records VALUES (1, 1, 'first');
INSERT INTO datascope_records VALUES (2, 2, 'second');
INSERT INTO datascope_records VALUES (3, 1, 'third');
COMMIT;
```

设置连接 JSON，密码仍用独立环境变量：

```sh
export DATASCOPE_DB_DAMENG_PASSWORD='测试账号密码'
export DATASCOPE_INTEGRATION_DAMENG='{"host":"10.0.0.20","port":5236,"schema":"TEST_SCOPE","username":"DATASCOPE_READER","password_env":"DATASCOPE_DB_DAMENG_PASSWORD"}'
make test-network
```

其他变量为 DATASCOPE_INTEGRATION_MYSQL、DATASCOPE_INTEGRATION_POSTGRESQL、DATASCOPE_INTEGRATION_ORACLE，内容为对应 connection 对象。未配置的数据库显示 SKIP，不能计为验收通过。测试覆盖元数据、默认拒绝、行与字段权限、JOIN/CTE、前置拒绝、分页与写入拒绝。上线前额外验证目标版本的 TLS、字符集、日期/DECIMAL/LOB、连接中断以及服务端超时取消。
