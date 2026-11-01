# 业务管理配置下一阶段方案

状态：业务管理配置分目录 YAML、显式一次性迁移、配置克隆、SELECT/DML/Prompts 边界、内存 Action 回执与移除公共片段已落地。全局 management.db 和独立 audit.db 保持 SQLite。升级命令与验收见 [YAML 发布说明](management-yaml-release.md)，克隆操作见 [配置克隆](configuration-clone.md)。

## 存储与目录

全局 `data/management.db` 继续使用 SQLite，维护平台用户、全局认证与数据源目录。业务数据库结构和数据保持原有规范。各业务库已将可维护配置从 management.db 迁出，按下列目录组织 YAML：

```text
data/
  management.db
  audit.db
  databases/<database_id>/
    management/
      database.yml
      authentication.yml
      account.yml
      grant.yml
      tables/<table>.yml
      actions/<action>.yml
      action-libraries/<name>.yml
      knowledges/<knowledge_id>.yml
    audit.db
    credentials.key
```

YAML 字段统一采用小驼峰，如 `id`、`name`、`description`、`content`、`revision`、`businessUserId`、`maxRows`。SQLite 内部字段仍保留大写下划线命名，API JSON 契约保持不变。业务标识与参数名保持自身业务语义。SQL、JavaScript 和知识正文使用 YAML 多行文本。

不设 template/、公共 SQL fragment、version/、state/ 或独立 prompts/ 目录。每张表保存完整权限 SQL，允许重复；旧片段引用在迁移时展开并保留参数绑定。SQL 直接编写并可绑定变量，绑定变量不构成独立查询模板需求。DML 通过 Action 执行，Action 内可以 SELECT 做校验。BusinessTool/ToolParameter、business_tools 与公共片段字段已从运行时模型删除；旧格式解析仅存在于离线一次性迁移中，禁止运行时兼容。

保留当前 `revision` 做并发保存检查，不生成历史版本目录。已登记结构放入对应表文件，派生索引和缓存在内存重建。扫描候选、最近检查状态与本进程最近 20 次变化报告仅存内存，重启重新扫描；生命周期和分进程约定见 [运行时状态](runtime-state.md)。账号密码保留哈希，连接密码保留加密方式，credentials.key 继续独立存放。迁移必须先备份、逐项校验并保留可恢复的原文件。

## 知识复用 MCP Prompts

统一从 knowledges/ 维护知识，列表展示完整、多行 description，正文按需读取：

- prompts/list：name 使用知识 ID，title 使用显示名称，description 使用完整知识描述，不返回正文；按协议分页。
- prompts/get(name)：读取同一份正文，包装为 user 文本消息；一期无模板参数。
- 仅公开已发布且当前业务身份有权读取的知识；列表和读取均执行当前库的身份与权限检查。
- 知识工作台统一采用列出、选择和获取正文流程，展示请求、响应与耗时。
- Resources 留给后续 view，不作为本轮知识出口；业务 MCP 已退出 search_knowledge / read_knowledge，旧开关兼容映射到独立知识开关。

## 配置克隆

支持从源业务库克隆 management 配置到目标业务库：默认复制表与字段配置、查询权限、Action 和知识，四项均可单独选择。当前认证设置保留目标原值。目标库保留自己的连接、账号密码、管理授权、身份绑定、MCP 路由、审计和动作回执。

克隆前展示覆盖范围、检查源和目标配置版本及结构兼容性；整组保存必须保证一致性。知识 ID 可以保留，数据库归属使用目标 ID。配置克隆不等于复制业务数据或扩大账号授权。

## 动作幂等回执

回执按最新要求只放内存，不进入业务 YAML、全局 management.db 或 audit 表，也不随配置克隆。MCP 进程拥有缓存，API 通过本地 Unix socket 共享这一运行实例的内存。按数据库、身份、request_id 隔离，并发预约与状态转换互斥。

只保证同一实例运行期间防重复。重启清空，独立实例不共享，重启后不能依赖旧 request_id 防止重复执行。审计日志仍独立落盘用于追踪，审计写入失败不影响业务。
