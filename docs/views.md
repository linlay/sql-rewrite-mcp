# HTML 视图配置与证券案例

左侧「视图配置」位于「知识配置」与「MCP 配置」之间。视图是完整 HTML，可内嵌 CSS 和 JavaScript；每个数据库独立保存，类型固定为 `view`。支持新增、模板、编辑、发布、隔离预览和真实业务预览。

## 配置与存储

平台用户需要 `config.read` 和 `view.write`。通过 `configure_views` 保存 `database.views` 与 `database.query_view_id`，必须携带当前数据库 ID 和 revision。只更新视图及默认查询视图，不修改调用者提交的其他数据库字段。

```json
{
  "id": "customer_card",
  "name": "客户卡片",
  "description": "展示客户查询结果",
  "type": "view",
  "html": "<!doctype html><html><body><h1>客户卡片</h1></body></html>",
  "published": true
}
```

HTML 单项最多 256 KiB，每库最多 100 个视图。配置保存在 `data/databases/<ID>/management/views/<view-id>.yml`，默认查询引用及顺序保存在 `database.yml`。沿用整代 YAML 原子发布、revision、备份和审计。旧目录没有 views 时可直接读取，无需数据库迁移。配置克隆提供独立「视图配置」范围；复制 HTML 与默认查询引用，资源 URI 使用目标库 ID。部分克隆若使保留的 Action 引用悬空，会拒绝提交。

默认查询及动作引用必须指向已发布视图。删除、改名、撤下被引用的视图前，先清除或更换引用。

管理 MCP：`datascope_query` 的 `view.templates` 获取模板；`datascope_manage` 的 `view.save` 保存。现有 `database.get` 返回当前视图配置。

## MCP Resources

业务 MCP 声明 `resources` 能力：

- `resources/list`：分页列出当前库已发布视图，每页 50 项，返回 `nextCursor`。
- `resources/read`：使用 `ui://datascope/<database-id>/views/<view-id>` 读取完整 HTML。
- `mimeType` 为 `text/html`，列表和正文均携带 `_meta: {"type":"view"}`。
- 草稿、其他数据库 URI、停用数据库不可读取。缓存范围为 private。

`type` 是 DataScope 的扩展元数据，放在标准允许的 `_meta` 中，不增加 Resource 顶层协议字段。资源中只有 HTML，实际业务数据由认证后的工具调用提供。

`query` 接受可选 `view_id`；省略时使用默认查询视图。查询数据结构保持原样，工具结果 `_meta["datascope/viewUri"]` 指向展示资源。动作定义可设置 `view_id`，`describe_action` 返回 `view_id` / `view_uri`，`invoke_action` 返回同样的展示提示。该提示是宿主约定，不是 MCP Apps 的自动渲染声明；普通 MCP 客户端可直接读取 HTML，不会被强制打开视图。

控制台查询工作台可切换「结果视图」，视图内只读调用使用当前工作台的已授权身份。正式动作使用「业务预览」或动作工作台的「真实业务调用」，由本人业务认证后的 MCP 执行。

## HTML 与宿主交互

内置模板是自包含 HTML，可直接编辑。模板中的 `window.datascope` 提供：

```javascript
datascope.onData(({ input, result }) => {
  // input 为展示输入，result 为查询或动作结果；不会包含业务 Token。
});

const result = await datascope.callTool('query', {
  sql: 'SELECT * FROM fund_accounts WHERE fund_account_no = :arg_account',
  parameters: { arg_account: '880000000079' },
  max_rows: 200
});
```

支持 `query`、`list_tables`、`describe_table`、`list_actions`、`describe_action`、`invoke_action`，以及 `openLink(url)` 打开服务端实际返回的本人确认链接。新建交互页面可从内置模板保留桥接脚本，再替换正文。

宿主以 `sandbox="allow-scripts"` 的不透明来源 iframe 展示 HTML，禁用直连网络、表单提交、父页面和存储访问。父页面验证消息来源、通道和工具白名单，再用当前页面内存中的业务 Token 调用 MCP。每次认证切换清空原视图。外部宿主可实现同样的桥，协议为：

1. iframe 向父页面发送 `{type:"datascope:view:ready"}`。
2. 父页面向该 iframe 发送 `{type:"datascope:view:init",channel:<随机通道>,data:{input,result}}`。
3. iframe 发送 `{type:"datascope:view:request",channel,id,method:"tools/call",params:{name,arguments}}`，或 `method:"openLink",params:{url}`。
4. 父页面回应 `{type:"datascope:view:response",channel,id,result}`，失败时为 `error` 文本。

该桥是 DataScope HTML 的宿主适配接口，Resources 传输仍使用标准 MCP。现有连接器的静态 `sql` / `action` 视图继续可用；支持资源视图的客户端通过上述 Resources 发现新的动态配置。

## 安装证券完整案例

```sh
make install-demo-actions
```

安装器现同时安装三个已发布视图：`query`（通用查询）、`action`（动作预览与结果）、`securities_account`（证券账户详情）。未配置默认查询视图时设置为 `query`，18 个演示 Action 关联 `action`。重装幂等；已修改同名视图默认拒绝覆盖，`-replace` 才替换。现有密码、查询权限和自定义动作保留。若只需补充案例且不追加业务账号能力：

```sh
go run ./cmd/install-demo-actions -accounts ''
```

新环境先按照 README 初始化完整证券库。单独从模板创建证券 HTML 不会安装 Action 或授予业务能力。

## 证券演示步骤

1. 在「视图配置」选择证券账户视图，进入「业务预览」，使用 E0001 本人账号认证。获取视图列表后选择「证券账户详情」。可在输入 JSON 中指定 `{"fund_account_no":"880000000079"}`。
2. 查询显示客户编号、姓名、手机号、风险等级、投资者类型、账户状态，以及资金余额、可用余额、可取余额、持仓市值。金额概览将分转换为元，资金和持仓分别显示最新可见日期。
3. 切换持仓、委托和流水读取当前账号可见数据。表格将金额和价格转换为元并标注单位；最多 200 行，截断时给出提示。
4. 点击「冻结账户」，填写原因。服务端先执行并回滚，HTML 展示真实 `preview`、原 request_id 与本人确认入口。
5. 本人打开确认页认证并确认。回到视图点击「查询同一请求结果」，使用原 request_id 和 `result_only:true`；`done` 后自动刷新账户信息。
6. 页面显示冻结状态，入口改为解冻，入金/出金入口根据账户状态调整。最终权限和业务条件均由 Action 服务端校验。

其他完整演示：查询账户 `880000000927` 发起演示入金/出金；查询客户 `C00001359` 的账户修改手机号或风险等级；查询账户的委托列表，选择初始委托 `O0000002309` 撤销剩余数量。先读取当前状态，案例动作可能影响彼此的前置条件。

E0121 的查询范围更广，但不能直接修改其他经理账户。无 action 能力时可以读取账户，动作列表不可用；拥有 action 能力但缺少记录修改权时，服务端仍拒绝。

预览页面不自动重试写入。超时后保留 request_id，仅查询回执；MCP 重启后回执未知时核对审计，不能换 ID 盲目重试。演示仅修改合成业务数据，不调用银行接口。
