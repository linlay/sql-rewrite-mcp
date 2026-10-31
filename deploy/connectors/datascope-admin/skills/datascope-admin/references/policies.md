# 访问策略与改写诊断

| 工具 | resourceType | action | args 中的参数 | 权限 |
| --- | --- | --- | --- | --- |
| datascope_query | policy | get | database_id | config.read |
| datascope_inspect | policy | validate | database_id、database | config.read + policy.write |
| datascope_manage | policy | save | database_id、database | config.read + policy.write |
| datascope_inspect | policy | preview_rewrite | database_id、sql；可选 business_user_id | query；指定模拟身份另需 impersonate |

policy.get 与 database.get 返回同样的数据库视图。按 [数据库](databases.md) 的字段清单构造 database，保留最新 database.revision 和其他字段，仅编辑 database.config。validate 不保存，save 只更新 config；两次调用传同一候选与版本。validate 成功后保存仍可能因并发修改失败。

config 字段：version 固定为 1；schemes 为 `{scheme,name,description}` 数组；tables 为表策略数组。优先保留当前 schemes。表策略的主要字段为 table、enabled、scheme、sql、passthrough、columns、max_rows；还有 reason、alternative、divergent、deny_unless、deny_message。新增或不确定的表策略先禁用，不能用遗漏 sql 表示全量开放。

- enabled:false 禁止该表。
- sql 保存每张表完整的 SELECT 权限 SQL，`${userId}` 来自可信业务身份，`${asOf}` 为服务端参考时间。不要将身份直接拼入 SQL。
- passthrough:true 显式允许不做行过滤，仅在用户要求该行为时设置。
- columns 为允许的字段列表；null 表示未配置字段限制，空数组及具体列表按服务端策略校验处理，不能随意互换。
- max_rows 为服务端行数限制。
- compiled_sql、compiled_deny_unless、params、deny_unless_params 为服务端编译信息，无需保存，仅在内存由服务端重新编译；不手工编辑生成权限规则。

预览示例：

```json
{"action":"preview_rewrite","args":{"resourceType":"policy","database_id":"default","sql":"SELECT * FROM items"}}
```

省略 business_user_id 时使用当前平台账号的业务身份绑定。需要模拟其他业务身份时传入真实正整数编号，服务端要求 impersonate；平台用户 ID 与业务身份编号不是同一概念。预览不执行业务查询，不表示任意身份都可以执行改写后的 SQL。

保存后回读策略；如需验证某条查询在当前账号下的权限改写，使用 preview_rewrite。实际业务执行使用数据库连接器。
