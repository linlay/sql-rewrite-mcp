# 业务 Action

先用 datascope_query 的 database.get 读取当前配置和 revision。Action 共有 name、title、description、confirm、parameters、script 六个字段，保存于 database.actions。编写脚本、write_enabled、write_connection 和写入密码需要 action.write；mcp.write 无法代替。

- 列出动作：`datascope_query({action:"list",args:{resourceType:"action",database_id}})`，对应 list_actions。查看单个动作：`datascope_query({action:"describe",args:{resourceType:"action",database_id,name}})`，对应 describe_action(name)。需要 action.write，返回与业务 MCP 相同格式的元数据，不包含脚本。MCP 或写入关闭时仍可查看。
- 保存：`datascope_manage({action:"save",args:{resourceType:"action",database_id,database,write_password?}})`。仅修改 actions、action_libraries、write_enabled、write_connection，保留其他配置。公共库使用 `{name,title,description,script}`，通过 script 内的标准静态 `import { fn1, fn2 as alias } from "库名称"` 引用，库内用 export 导出；不增加 uses 字段。action_libraries 省略时保留现有库，显式空数组表示清空；存在引用时拒绝删除。网络写账号必须与查询账号指向同一库和 schema。write_password 直接加密保存，不能写进配置 JSON；已有密码留空保留。
- 试运行：`datascope_inspect({action:"try",args:{resourceType:"action",database_id,name,arguments,request_id,business_user_id}})`。需要 action.write 和 impersonate；始终回滚，显示 SQL 和结果。修改后先保存再试运行。
- 对业务 MCP 开放 list_actions / describe_action / invoke_action 另用 mcp.save；给业务账号授予 action 另用 business_account.save。不要自动扩大现有授权。

script 定义同步 `function run(args,ctx)`；`ctx.userId` 是业务身份，`ctx.now` 是固定时间。`my.one` / `my.all` 应用现有查询权限，`db.one` / `db.all` 读取可信业务数据，`db.exec` 执行 INSERT/UPDATE/DELETE 并返回影响行数。所有 SQL 使用标签模板，值写 `${args.name}`，不能拼接 SQL、传字符串、动态表名或在引号中插值。UPDATE/DELETE 必须有 WHERE，建议附关键权限和状态条件并检查影响行数。deny、异常、超时回滚；捕获 deny/SQL 错误也不能提交。金额用整数分，BIGINT/DECIMAL 为字符串。

业务调用使用 invoke_action 的 name、arguments、request_id。confirm:true 先完整执行并回滚，返回预览和 confirmation_url。模型只能展示预览/链接，由同一业务账号本人打开确认页登录并确认；管理 MCP 不能代替人工确认。确认后用相同 request_id 调用 invoke_action，指定 result_only:true 只读获取回执；没有回执返回 unknown，绝不再次执行。结果未知时先核对审计及业务记录，绝不盲目换 ID 重试。回执只在 MCP 运行实例内存中保留，重启清空，独立实例不共享；API 确认页通过本地 Unix socket 访问同一实例，不提供跨重启幂等。
