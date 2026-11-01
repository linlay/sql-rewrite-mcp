# 克隆业务库配置

在「数据库配置 → 基本信息」点击「克隆配置」。当前选中的数据库是目标库；选择来源库和复制范围后自动检查目标库真实结构，展示覆盖数量、名称和阻止项。核对摘要并勾选确认后执行。

默认选择前四项；认证配置默认不选，可明确勾选：

| 范围 | 覆盖方式 | 目标权限（另需来源和目标 config.read） |
| --- | --- | --- |
| 表与字段配置 | 同名表的开放状态、字段白名单、表与字段名称及注释；目标独有表保留 | config.write、policy.write |
| SELECT 查询权限 | 同名表的完整权限 SQL、绑定变量、前置检查、行数限制及说明；目标独有表保留 | policy.write |
| Actions | 替换整个集合，移除目标独有 Action；空来源清空目标集合 | action.write |
| 知识 | 替换整个集合，保留知识 ID、正文与发布状态，无表依赖，归属改为目标 ID | knowledge.write |
| 认证配置（默认不选） | 复制认证方式、全局提供方引用或本库独立连接；保留目标业务能力、默认身份和员工目录校验 | 仅系统管理员 |

表与字段的实际结构只从目标库连接读取。来源登记的表或字段、开放白名单、注释引用如果在目标库缺失，明确列出并禁止克隆；尚未登记的目标表应先通过已有结构同步入口登记。不会把来源结构快照写成目标真实结构。预览不写配置。

保留目标自己的 ID、显示名称、驱动和连接、读写账号与加密凭据、密钥、MCP 路径、MCP/知识启用状态、工具列表、写入开关、本地账号和密码哈希、平台逐库授权、业务身份绑定、审计日志、Action 内存回执。克隆不复制业务数据，不自动启用写入，不增加人员授权。未选择认证时保留目标认证。选择认证时只复制配置和密钥环境变量引用，不复制私钥、密码、环境变量值、账号或人员授权。全局提供方引用保持引用；本库独立连接作为目标库独立配置。继承的全局策略解析为明确的本库认证策略，同时保留目标业务能力和默认身份。认证 revision 递增，旧业务 Token 和待确认操作按现有撤销语义失效。

检查所有结果 Action 的编译和静态 SQL 模板、知识关联表与查询权限的已知依赖。SQLite 以 EXPLAIN 检查 SQL 引用，不执行 SELECT 或 DML；网络数据库检查已知表、可确定的字段引用以及现有可移植 SQL 编译规则。检查不能静态证明任意 JavaScript / SQL 的业务兼容性，仍需在目标业务环境验证 Action 行为。

确认要求源、目标 revision 和完整配置指纹保持不变；完整指纹包含知识和权限等不递增数据库 revision 的修改，以及源目标解析后的认证策略、提供方和全局认证 revision。确认摘要还绑定所选范围、覆盖摘要和目标实际结构。执行重新检查权限、结构和静态依赖，再通过管理存储的公共克隆事务一次发布目标 YAML 整目录。验证、回调或暂存失败不会留下部分克隆；发布中断复用已有整目录恢复机制。被覆盖的知识 revision 递增，避免旧编辑窗口覆盖克隆结果。成功操作记入目标审计，审计失败不阻断功能。

## 应用/API

复用 `POST /api/management/call`，当前数据库放在 `arguments.database_id`，来源及范围放入 `arguments.clone`。

```json
{
  "action": "preview_clone_configuration",
  "arguments": {
    "database_id": "target",
    "clone": {
      "source_database_id": "source",
      "scope": {"tables": true, "policies": true, "actions": true, "knowledge": true, "authentication": false}
    }
  }
}
```

省略 scope 默认四项全部复制；显式空范围被拒绝。预览返回 `items`、`issues`、`warnings`、源目标 `*_revision`、`*_token` 和 `confirmation`。必须向用户展示覆盖摘要；`issues` 非空不能执行。

执行动作 `clone_configuration` 使用同样的来源和范围，补充预览原样返回的 `source_revision`、`target_revision`、`source_token`、`target_token`、`confirmation`，以及 `confirmed:true`。执行成功返回 `applied:true`。配置或结构变化后需重新预览。

管理 MCP 复用相同应用操作：`datascope_inspect` 的 `database.preview_clone_configuration` 与 `datascope_manage` 的 `database.clone_configuration`，args 为 `resourceType:"database"`、`database_id` 和 `clone`。业务 MCP 不提供克隆入口。
