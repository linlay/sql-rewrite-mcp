# 业务知识

全部调用使用 resourceType:"knowledge"，且需要 database_id。

| 工具 | action | 其他必需参数 | 可选参数 | 语义与权限 |
| --- | --- | --- | --- | --- |
| datascope_query | list | 无 | query | 管理描述，含草稿，knowledge.write |
| datascope_query | get | id | 无 | 管理完整正文，含草稿，knowledge.write |
| datascope_query | search | query | 无 | 已发布可见描述，knowledge.read + query 权限 |
| datascope_query | read | id | 无 | 已发布可见正文，knowledge.read + query 权限 |
| datascope_manage | save | id、name、description、content、published、revision | release_knowledge_restrictions（明确解除旧限制） | 创建或完整更新，knowledge.write |

create 与 update 统一使用 save；新知识 revision:0，已有知识必须传 get 返回的最新 revision。save 完整替换该条知识，保留未要求修改的字段，显式提供 published:true/false，不能遗漏后意外下架。description/content 可为空字符串，正文最多 200000 字节，id 非空且不能含斜杠。新知识无需关联表。旧 tables 仅作为兼容限制保留：普通更新省略 tables 时保留原限制，不能为了简化编辑而清空。明确告知解除后本库有 knowledge.read + query 的用户可读已发布正文，并取得解除授权后才传 release_knowledge_restrictions:true。

创建草稿示例：

```json
{"action":"save","args":{"resourceType":"knowledge","database_id":"default","id":"join-rules","name":"关联规则","description":"订单明细关联时避免重复汇总","content":"汇总前按业务主键去重。","published":false,"revision":0}}
```

search/list 最多返回 50 条描述；不支持 limit/offset，不能声称是完整大规模枚举。get 与 read 的可见范围不同，不因 read 查不到就断言不存在。旧记录在明确解除前仍受原关联表可见性约束，字段受限或禁用的表可能令知识不可见。

写后用 get 验证正文、状态和 revision；仅在用户要求发布时设为 true。发布状态不意味着所有查询用户均可读取，读取权限继续由服务端判断。

知识 description 支持多行，list/search 会完整提供给模型，不截断描述，不包含正文。旧 summary 输入仍可兼容，description（包括空字符串）优先；新调用统一使用 description，输出不再返回 summary。

业务 MCP 的知识出口统一为 prompts/list（分页名称、ID、完整 description）和 prompts/get(name)（正文作为 user 文本消息）。不接受模板参数，Resources 留给 view。管理 MCP 的 list/get 用于编辑管理；工作台的 knowledge_prompt.list/get 复用相同读取与权限逻辑。
