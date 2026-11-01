# 业务操作 Action

Action 通过 API 保持数据库配置中的 `actions` 数组契约，实际保存到 `management/actions/<action>.yml`，并使用数据库当前 revision 控制并发保存。每项仅有 `name`、`title`、`description`、`confirm`、`parameters`、`script` 六个字段。

## 配置和权限

在「动作配置」启用独立写入连接，新增操作后保存。SQLite 使用原业务文件的独立 `mode=rw` 连接，不创建新文件，并关闭把不存在的双引号字段当作字符串的旧式回退；普通查询仍用 `mode=ro`、`query_only`。网络库配置独立写入账号，目标数据库、schema、TLS 必须与查询连接一致。密码可直接输入并加密保存，也可使用 `DATASCOPE_DB_` 环境变量引用；连接密码密文只进入 database.yml 的专用字段，密码明文不进入配置、接口返回或审计。

编写脚本、启停写入、配置或轮换写入账号需要平台权限 `action.write` 和 `config.read`。`mcp.write` 不允许修改这些字段。控制台试运行另外需要 `impersonate`，可填写业务身份编号。新增能力不会自动授予旧业务账号。

在「开放工具」启用 `list_actions`、`describe_action`、`invoke_action`，并为当前数据库的业务账号授予 `action`。业务账号可以只有 `action`，与 `query` 并列；该账号不能登录管理后台。`my.*` 始终应用数据库已有查询策略，不需要额外授予业务账号通用查询工具。

## 定义示例

下面使用示意表，实际应替换为数据库中的真实名称。金额采用整数分，参数整数限定在 JavaScript 安全整数范围。

```yaml
actions:
  - name: rename_item
    title: 修改本人记录名称
    description: 修改可见且归本人管理的记录名称
    confirm: true
    parameters:
      id: {type: integer, description: 记录编号, min: 1}
      name: {type: string, description: 新名称, maxLength: 100}
    script: |
      function run(args, ctx) {
        const row = my.one`SELECT id, owner_id FROM items WHERE id = ${args.id}`;
        if (!row) deny('记录不存在或无权查看');
        if (row.owner_id !== ctx.userId) deny('只能修改本人记录');
        const n = db.exec`UPDATE items SET name = ${args.name}
                          WHERE id = ${args.id} AND owner_id = ${ctx.userId}`;
        if (n !== 1) deny('记录已变化，请重试');
        const updated = my.one`SELECT id, name FROM items WHERE id = ${args.id}`;
        if (!updated || updated.name !== args.name) deny('后置检查失败');
        return updated;
      }
```

参数支持 `string`、`integer`、`number`、`boolean`，默认必填；可加 `optional: true`、`enum`、`min`、`max`、`maxLength`、`pattern`。拒绝未知参数和 null；校验错误附完整 JSON Schema。`maxLength` 按 Unicode 字符计算。`pattern` 使用 Go 正则语法。

| 脚本接口 | 行为 |
| --- | --- |
| `my.one` / `my.all` | SELECT 自动应用表、字段、行策略、denyUnless 和各表行数上限 |
| `db.one` / `db.all` | SELECT 读取可信业务数据，绕过查询策略 |
| `db.exec` | 执行单条 INSERT / UPDATE / DELETE，返回影响行数 |
| `deny(message)` | 中止事务，拒绝原因返回给调用方 |
| `ctx.userId` / `ctx.now` | 可信业务身份、本次操作的固定 UTC RFC3339 时间 |
| `return value` | 同步返回 JSON 值，最多 1 MiB |

所有 SQL 必须写成标签模板，如 ``db.one`SELECT ... WHERE id=${args.id}` ``。编译时固定每个模板的 SQL，插值只能作为参数值绑定；字符串拼接、动态模板、引号或注释内的插值均不能执行。表和列名直接写在模板中。

`one` 无记录时返回 null，`all` 返回数组。BIGINT、DECIMAL、NUMERIC 作为字符串返回；其他超出安全整数范围的整数也返回字符串。若驱动已把精确小数转换成浮点，拒绝返回，需在 SQL 中将该字段转为文本。`db.*` 读出的内容不自动脱敏，脚本作者应只返回业务需要的结果。

每次操作共享一个事务，前置/后置 SELECT 都能看到本事务的修改。正常返回才提交；deny、SQL 异常、JS 异常、超时均回滚，即使脚本捕获了 deny/SQL 异常也不能提交。限制为总时限 5 秒（包括连接建立）、50 条脚本 SQL（包括权限 guard）、每次 SELECT 最多 1000 行。脚本仅支持当前数据库公共函数库的静态模块导入，没有网络、文件、进程、动态 import、require 或计时器接口。

## 公共函数库与标准 import

在「动作配置 → 公共函数库」管理本库的复用代码，需要 `config.read` 和 `action.write`。公共库保存为 `management/action-libraries/<name>.yml`，字段为 `name`、`title`、`description`、`script`，采用小驼峰。例如 `employee.yml`：

```yaml
name: employee
title: 员工身份检查
description: 校验在职员工
script: |
  export function operator(ctx) {
    const user = db.one`SELECT e.employee_id, e.status, r.code AS role_code FROM employees e JOIN roles r ON r.id=e.role_id WHERE e.employee_id=${ctx.userId}`;
    if (!user || user.status !== 'active') deny('员工不存在或已停用');
    return user;
  }

  export function administrator(user) {
    if (user.role_code !== 'ADMIN') deny('需要管理员身份');
  }
```

动作的 `script` 使用 JavaScript ES Modules 静态语法；一条语句可导入多个函数，也可用 `as` 改名，或使用 `import * as employee from "employee"`。支持默认导出及模块间的静态导入、重新导出，不增加 YAML `uses` 字段：

```js
import { operator, administrator as requireAdmin } from "employee";

function run(args, ctx) {
  const user = operator(ctx);
  return { employeeId: user.employee_id };
}
```

模块名称只解析为当前数据库的公共库名称，不解析文件路径、URL 或 npm 包。保存时校验库与动作的语法、导出、缺失依赖、循环依赖以及全部静态 SQL；被引用的库不能直接删除。运行时只编译和装载本次动作可达的模块，不执行无关库。按模块加载，不按单个导出函数裁剪。源码及配置仍每次读取当前 YAML；不可变编译结果按数据库与全部源码内容缓存，上限 128 项和 8 MiB 源码预算。源码变化会使用新的编译结果。

每次调用创建新的 JS 执行环境，模块变量、用户身份和事务不跨请求共享。模块初始化阶段禁止执行 SQL；SQL 必须放在函数体中，由动作调用时执行。公共库与动作共享同一事务、身份、SQL 条数和超时限制；导入库中的 SQL 也进入既有静态模板校验、参数绑定和权限改写。公共库修改提高数据库 revision，并使待确认预览失效；即使手动修改库却未提高 revision，代码指纹变化也会拒绝旧确认。已完成的幂等回执继续复用。

配置克隆选择 Actions 时同时复制公共函数库，并检查导入库中的 SQL 与目标库结构。证券演示包的 18 个动作已改为引用 employee、account、customer 三个公共库；安装器按原有冲突与 `-replace` 规则处理库定义。

UPDATE / DELETE 必须有 WHERE，并建议再次包含所有关键权限和状态条件、检查影响行数。普通 INSERT、UPDATE、DELETE 经 AST 编译为各数据库的参数和引用形式；网络库暂不支持 DML 的 WITH、UPDATE FROM、RETURNING、UPSERT、冲突处理、DELETE ORDER/LIMIT。SQLite 也禁止事务控制、DDL、PRAGMA、ATTACH、REPLACE、RETURNING 和冲突处理。写入账号本身应只获得必需表的权限。

## MCP 调用和人工确认

```json
{"name":"rename_item","arguments":{"id":1,"name":"新名称"},"request_id":"unique-operation-uuid"}
```

`list_actions({})` 列出可用操作，`describe_action({name})` 返回单个动作的参数 Schema。`invoke_action` 返回 `done`、`denied` 或 `confirm_required`。

`confirm: true` 会先完整执行并回滚，返回 `preview.result`、`preview.writes`（SQL、绑定参数、影响行数）、`confirmation_token` 和 `confirmation_url`。确认预览保留 10 分钟。

业务连接器可显示：

````markdown
```view key=datascope/action
{"title":"确认修改记录","preview":{"result":{"id":1,"name":"新名称"},"writes":[]},"confirmation_url":"从 invoke_action 响应原样复制"}
```
````

必须原样复制服务端返回的预览和链接。人在视图点击「打开确认页」，使用发起操作的同一个业务账号登录并确认。独立页面要求密码、浏览器 CSRF Cookie 和 nonce；两个 MCP 都没有确认接口，模型仅持有令牌不能提交。确认页会展示真实的服务端预览。确认后，用原始 name、arguments、request_id 再次调用 `invoke_action` 获取结果。

正式执行前重新检查账号权限、账号版本、认证配置、数据库配置版本和 MCP 启用状态。再次执行脚本，并比较返回值及完整写入计划（SQL、绑定参数、影响行数）；变化则回滚，要求新的 request_id 重新预览。固定 ctx.now 沿用首次预览时间。

## 幂等、审计和故障恢复

request_id 在同一 MCP 运行实例内按数据库、发起账号隔离。相同 ID、操作和参数复用结果；同一 ID 换操作、参数或身份会拒绝。内存互斥锁与状态比较保护并发请求，结果未知的请求在该实例内不会自动重放。

回执、确认令牌及执行结果只保存在内存，不写入管理库、YAML 或 audit 表。MCP 重启即清空，不提供跨重启防重保证；重启后重试旧请求可能重新执行，应先核对业务数据。缓存上限为 10000 条、64 MiB，达到上限拒绝新增，不通过自动淘汰回执破坏运行期防重。

MCP 进程持有唯一内存缓存，API 的确认页和平台试运行通过权限为 0600 的本机 Unix socket 访问它。两个服务的 ACTION_STATE_SOCKET 必须一致，默认是全局管理库同级 runtime/actions.sock；多个独立部署使用各自路径。socket 不包含回执数据。MCP 内存服务不可用时，Action 操作报错，不回退到另一个独立缓存。平台账号仍不能代替业务账号正式执行或确认。

每条 SQL 的内容、绑定参数、影响行数及错误继续异步进入 audit.db。审计不可写不阻止业务提交；它只用于追踪，不承担幂等控制。旧版本 ACTION_RECEIPT 表不再读取或写入，新库不创建该表；已有逐库表保留作历史备份。

控制台「试运行」始终回滚，包括 confirm:false 的操作，显示 SQL 审计和执行预览。管理 MCP 使用 `datascope_manage` 的 `{action:"save",args:{resourceType:"action",database_id,database,write_password?}}` 保存 action、写连接和启用状态；使用 `datascope_inspect` 的 `{action:"try",args:{resourceType:"action",database_id,name,arguments,request_id,business_user_id}}` 试运行，始终回滚。读取当前配置与 revision 后再保存。

工具区的「动作工作台」独立于代码编辑。在回滚试运行页签，先手动获取动作列表，再选择动作查看完整参数 JSON Schema、约束和人工确认标志，填写 JSON 参数与业务身份后试运行。列表、说明和试运行分别展示实际请求参数、返回 JSON 与耗时；结果展示返回值及 SQL 写入计划。`request_id` 可编辑，同一 ID 重用历史预览，修改参数或身份会自动生成新 ID，也可点击“新请求”重新预览。列表与说明分别通过平台 `list_actions` / `describe_action(name)` 返回与业务 MCP 相同格式的元数据，不返回脚本；需要 `action.write`，未开启 MCP 或写入时仍可查看。试运行沿用 `try_action`，另外要求 `impersonate` 和已启用写入，任何 confirm 设置均回滚；真实业务调用页签通过本人业务认证和业务 MCP 的 `invoke_action` 发起正式操作并处理人工确认，见下文。管理 MCP 可用 `datascope_query({action:"list",args:{resourceType:"action",database_id}})` 获取列表，再用 `datascope_query({action:"describe",args:{resourceType:"action",database_id,name}})` 读取参数说明。

SQLite 已有事务、权限、幂等、确认和并发端到端回归；网络库使用共享连接和 AST 编译，并有各驱动 SQL 编译/绑定测试。接入真实 MySQL、PostgreSQL、Oracle、达梦写账号后，仍需在目标库验收其账号权限、字段精度、锁和超时行为；测试未配置实际写账号时不代表已做真实库写入验收。

证券演示库已提供可显式安装的 18 个操作，见 [完整案例](securities-actions.md)；涉及的结构、状态值和发布步骤见 [发布清单](securities-actions-release.md)。

## 工作台正式调用

动作工作台提供“试运行（回滚）”与“真实业务调用”。前者使用平台 action.write + impersonate 权限，始终回滚；后者仅允许平台动作编辑者使用本人业务认证访问真实业务 MCP，平台身份不会变成业务身份。

- NONE：使用库配置的匿名身份和能力，不能凭空获得 action；人工确认也不能冒充本地账号。
- ACCOUNT：复用既有 /auth/databases/{id}/token 端点，账号密码换取当前库 Token；也可粘贴本人已有 Token。
- OAUTH：内置 OAuth 复用既有注册、PKCE 授权码和 Token 端点，独立窗口由本人登录；企业 OAuth 使用本人已取得的业务 Bearer Token，不新增企业认证协议。

Token 仅保留在当前页面内存，不进入 localStorage、配置、请求追踪展示或日志。离开页签销毁认证状态。平台会话只控制工作台入口，业务 MCP 每次独立验证 Token、数据库、工具开关和 action 能力。

正式调用需要明确点击并确认。confirm_required 展示回滚 preview 和现有确认页链接，由本人打开确认页登录执行；工作台不调用确认接口。查询结果使用同一 request_id 调用 invoke_action，并指定 result_only:true；只查询当前业务身份的内存回执。网络故障或 MCP 重启后缺失回执返回 unknown，用户应核对审计和业务数据，工作台不自动重试写入。
