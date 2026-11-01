# 证券演示库 Action 完整案例

本套案例提供 **18 个 Action，覆盖 12 张现有业务表**；所有操作都需要本人确认。源定义为 [securities.yml](../internal/demoactions/securities.yml)，正常与拒绝案例共 **37 个**，完整调用参数在 [securities.json](../fixtures/actions/securities.json)。

## 哪些表可以改

| 表 | 允许修改的内容 | Action |
| --- | --- | --- |
| `customers` | 手机号、风险等级、投资者类型 | `update_customer_mobile / update_customer_risk_level / update_customer_investor_type` |
| `fund_accounts` | 冻结、解冻、调整经理 | `freeze_fund_account / unfreeze_fund_account / reassign_account_manager` |
| `employees` | 本人或管理员维护手机号 | `update_employee_mobile` |
| `branches` | 管理员维护地址 | `update_branch_address` |
| `departments` | 管理员维护地址 | `update_department_address` |
| `securities` | 管理员维护展示名称 | `rename_security` |
| `orders` | 撤销未成交余量、修正来源渠道 | `cancel_order_remainder / update_order_channel` |
| `stock_accounts` | 新增；删除无历史委托账户 | `open_stock_account / close_unused_stock_account` |
| `daily_balances + cash_flows` | 最新账期入金、出金、同客户账户划转 | `deposit_demo_cash / withdraw_demo_cash / transfer_demo_cash` |
| `daily_quotes + positions` | 最新收盘价更正并同步持仓估值 | `correct_latest_close_price` |

另外 5 张表的处理方式：`trades` 是已成交事实，不提供直接改写；`roles`、`employee_scopes` 涉及身份和授权，使用专门管理流程；`trading_calendar` 不回写历史交易日；`schema_comments` 的展示说明通过平台的元数据/注释配置维护。本次不提供任意表、任意字段的通用 UPDATE。

## 安装与账号

```sh
# 先核对 .env 的管理库、审计库、允许目录和已有管理员配置。
make install-demo-actions
```

安装只针对已登记的 SQLite 证券演示库。会备份管理库到 `data/legacy/action-install/`，保留自定义 Action、表字段行权限、现有账号密码及其他能力；启用证券库写连接和两个 Action 工具。默认只为以下已有业务账号追加 action 能力：

| 账号 | 业务身份 | 用法 |
| --- | --- | --- |
| E0001 | 1，客户经理 | 修改本人账户、完整管理客户资料、本人手机号、委托、演示资金 |
| E0121 | 121，营业部主管 | 演示“可查询不代表可修改”，不自动授予对范围内账户的写权限 |
| E0175 | 175，证券业务管理员 | 维护公共目录、调整经理、最新价格更正、跨经理的同客户划转 |

能力变化会使旧 OAuth 凭据失效，三个账号需重新授权连接器。所有账号密码保持原值。其他业务账号仍需管理员单独授予 action。

启动服务不会自动启用这些操作。重复运行安装命令不改版本、不重复授权；如果同名 Action 已被手工修改，安装默认拒绝覆盖，显式 `-replace` 才替换。

```sh
# 其他账号组合、其他已登记的证券库 ID：先载入目标部署的环境变量。
go run ./cmd/install-demo-actions -database default -accounts E0001,E0175
# 仅检查：
go run ./cmd/install-demo-actions -check
```

## HTML 视图案例

安装器同时安装通用查询、动作预览与结果、证券账户详情三个视图，并为 18 个 Action 关联动作视图。在「视图配置 → 业务预览」使用本人业务认证，通过 Resources 加载证券账户视图，完成查询、预览、本人确认及结果刷新。详见 [证券视图完整案例](views.md)。

## 使用流程

1. 用 query 查询当前身份可见记录与现有值，再 describe_action(name) 获取参数格式。
2. 在控制台「MCP 配置 → 业务操作 Action」点击试运行，选择下面对应的业务身份，复制 arguments。试运行始终回滚，无需输入该员工密码。
3. 正式调用 invoke_action 时生成唯一 request_id。返回 confirm_required 后展示真实预览与确认链接。
4. 由发起业务账号本人打开链接，输入原业务密码确认；模型没有确认接口。
5. 用原 name、arguments、request_id 再调用一次，获取已提交结果。发生未知结果时核对审计，禁止盲目换 ID 重试。

以下示例互相独立，基于初始证券库。实际操作前仍需查询当前记录；冻结/删除/归属变更会改变后续案例的前置条件。完整 JSON 中 prepare 描述需要先执行的前置操作；自动测试只在隔离库提交准备步骤。

## 18 个正常案例

### 更新本人完整管理客户手机号

业务身份：`1`；Action：`update_customer_mobile`。

```json
{
  "name": "update_customer_mobile",
  "arguments": {
    "customer_no": "C00001359",
    "mobile": "13900001359",
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 客户风险等级 C4 → C2

业务身份：`1`；Action：`update_customer_risk_level`。

```json
{
  "name": "update_customer_risk_level",
  "arguments": {
    "customer_no": "C00001359",
    "risk_level": "C2",
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 普通 → 专业投资者

业务身份：`1`；Action：`update_customer_investor_type`。

```json
{
  "name": "update_customer_investor_type",
  "arguments": {
    "customer_no": "C00001359",
    "investor_type": "专业",
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 冻结本人账户

业务身份：`1`；Action：`freeze_fund_account`。

```json
{
  "name": "freeze_fund_account",
  "arguments": {
    "fund_account_no": "880000000079",
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 先冻结再解冻

业务身份：`1`；Action：`unfreeze_fund_account`。

前置步骤：freeze_fund_account({"fund_account_no": "880000000079", "reason": "准备前置状态"})。

```json
{
  "name": "unfreeze_fund_account",
  "arguments": {
    "fund_account_no": "880000000079",
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 管理员把客户经理1调整为2

业务身份：`175`；Action：`reassign_account_manager`。

```json
{
  "name": "reassign_account_manager",
  "arguments": {
    "fund_account_no": "880000000079",
    "expected_manager_id": 1,
    "new_manager_id": 2,
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 修改本人联系方式

业务身份：`1`；Action：`update_employee_mobile`。

```json
{
  "name": "update_employee_mobile",
  "arguments": {
    "employee_id": 1,
    "mobile": "13900000001",
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 管理员维护分公司地址

业务身份：`175`；Action：`update_branch_address`。

```json
{
  "name": "update_branch_address",
  "arguments": {
    "code": "B001",
    "address": "上海市演示大道100号（合成地址）",
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 管理员维护营业部地址

业务身份：`175`；Action：`update_department_address`。

```json
{
  "name": "update_department_address",
  "arguments": {
    "code": "D001",
    "address": "上海市演示大道200号（合成地址）",
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 管理员维护证券展示名称

业务身份：`175`；Action：`rename_security`。

```json
{
  "name": "rename_security",
  "arguments": {
    "code": "688001",
    "name": "华辰科技（演示维护）",
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 撤销部分成交委托剩余200股

业务身份：`1`；Action：`cancel_order_remainder`。

```json
{
  "name": "cancel_order_remainder",
  "arguments": {
    "order_no": "O0000002309",
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 更正委托来源 COUNTER → APP

业务身份：`1`；Action：`update_order_channel`。

```json
{
  "name": "update_order_channel",
  "arguments": {
    "order_no": "O0000002309",
    "channel": "APP",
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 删除没有任何历史委托的深市账户

业务身份：`1`；Action：`close_unused_stock_account`。

```json
{
  "name": "close_unused_stock_account",
  "arguments": {
    "fund_account_no": "880000000164",
    "exchange": "SZ",
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 删除无委托账户后重新新增深市账号

业务身份：`1`；Action：`open_stock_account`。

前置步骤：close_unused_stock_account({"fund_account_no": "880000000164", "exchange": "SZ", "reason": "准备前置状态"})。

```json
{
  "name": "open_stock_account",
  "arguments": {
    "fund_account_no": "880000000164",
    "exchange": "SZ",
    "stock_account_no": "9000000164",
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 演示入金100元，同步日结与流水

业务身份：`1`；Action：`deposit_demo_cash`。

```json
{
  "name": "deposit_demo_cash",
  "arguments": {
    "fund_account_no": "880000000927",
    "amount_cents": 10000,
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 演示出金50元，同步日结与流水

业务身份：`1`；Action：`withdraw_demo_cash`。

```json
{
  "name": "withdraw_demo_cash",
  "arguments": {
    "fund_account_no": "880000000927",
    "amount_cents": 5000,
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 管理员在同客户两个账户间划转100元

业务身份：`175`；Action：`transfer_demo_cash`。

```json
{
  "name": "transfer_demo_cash",
  "arguments": {
    "source_fund_account_no": "880000000080",
    "target_fund_account_no": "880000000079",
    "amount_cents": 10000,
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

### 最新收盘价8722分改8800分，同步重估持仓

业务身份：`175`；Action：`correct_latest_close_price`。

```json
{
  "name": "correct_latest_close_price",
  "arguments": {
    "security_code": "688001",
    "trade_date": "2026-10-20",
    "expected_close": 8722,
    "close_price": 8800,
    "reason": "证券 Action 完整案例演示"
  },
  "request_id": "为本次操作生成新的 UUID"
}
```

## 记账和修改边界

- 金额使用整数分，入金/出金/划转都是合成数据记账，不调用银行，不收取手续费。参数 amount_cents 为 1–100000000。
- 只操作每个账户当前最新演示日结，初始数据为 2026-10-20；ctx.now 是调用时间，不把系统日期误当作账期。不会在历史记录中间插流水，不改后续日期的期初余额。
- 资金操作先检查流水尾余额与日结一致，使用条件 UPDATE 防止余额不足或并发覆盖，并同时写流水；随后验证日结公式和转入转出对账。划转检查同客户同币种、双方操作权限及资金守恒，四条写入整体提交。
- 客户资料被多个资金账户共享。普通经理只有管理该客户全部账户时才能修改；例如 C00001359 属于经理1完整管理，而 C00000061 跨多个经理，需业务管理员维护。
- 冻结只修改账户状态；演示模型未建立冻结资金/冻结证券字段，撤余单也只终结剩余委托，保留已成交数量，不编造退款或改历史持仓。
- 删除证券账户只允许没有任何历史委托的记录；重新开户可以使用新号码，主键由数据库生成，不破坏历史外键。
- 价格更正只允许最新行情日，使用 expected_close 防止过期覆盖，同步拓宽 high/low 并重估该证券同日所有持仓。数量、成本、成交和资金保持原有业务口径。

## 拒绝与一致性案例

| 案例 ID | 应拒绝的操作 |
| --- | --- |
| `deny_other_manager` | 拒绝操作其他经理账户 |
| `deny_scope_is_not_write` | 主管能查看但不能直接修改其他经理账户 |
| `deny_shared_customer` | 拒绝单个经理修改跨经理共享客户资料 |
| `deny_public_directory_write` | 公共表可查询不代表普通员工可维护 |
| `deny_other_employee` | 拒绝普通员工修改他人联系方式 |
| `deny_already_frozen` | 拒绝重复冻结 |
| `deny_frozen_withdrawal` | 冻结账户不能出金 |
| `deny_insufficient_withdrawable` | 拒绝超出可取余额 |
| `deny_cross_customer_transfer` | 拒绝跨客户划转 |
| `deny_stale_manager` | 拒绝使用过期的原经理编号 |
| `deny_wrong_target_role` | 拒绝把账户分配给主管而非客户经理 |
| `deny_duplicate_stock_account` | 拒绝重复开立同交易所账户 |
| `deny_stock_number_format` | 拒绝证券账号与交易所格式不匹配 |
| `deny_historical_revaluation` | 拒绝回写历史行情 |
| `deny_stale_close` | 拒绝使用过期价格更正行情 |
| `deny_unauthorized_revaluation` | 拒绝普通员工重估全库持仓 |
| `deny_resigned_operator` | 拒绝离职员工操作 |
| `deny_referenced_stock_account` | 拒绝删除已有历史委托引用的证券账户 |
| `deny_already_cancelled_order` | 已撤余单不能再次撤销 |

另有参数错误（非法手机、风险枚举、负数/小数/超限金额、空白原因、额外 confirmed 参数）、安装幂等与密码保持、本人确认/结果重放、并发入金不丢更新、划转在扣款后收款失败整笔回滚等回归。

```sh
go test ./internal/demoactions -v
# 可选：对指定完整证券数据库先复制、再执行无准备步骤的31个预览，最终验证副本哈希不变。
DATASCOPE_DEMO_ACTIONS_TEST_DB=/absolute/path/securities_demo.sqlite3 go test ./internal/demoactions -run TestFull -v -count=1
```
