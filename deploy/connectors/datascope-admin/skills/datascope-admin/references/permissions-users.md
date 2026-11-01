# 用户与数据库授权

下表列出 args 参数，另外提供 resourceType。user_id 是平台用户正整数 ID，business_user_id 是业务库中的身份编号；两者不可互换。

| 工具 | resourceType | action | 参数 |
| --- | --- | --- | --- |
| datascope_query | user | list | 无 |
| datascope_manage | user | create | username、name、password |
| datascope_manage | user | enable / disable | user_id |
| datascope_manage | user | reset_password | user_id、password |
| datascope_query | grant | list | database_id |
| datascope_manage | grant | grant | database_id、user_id、permissions、business_user_id |
| datascope_manage | grant | revoke | database_id、user_id |

user 所有动作需要 users.manage。创建用户不自动授予数据库权限，不创建系统管理员。密码至少 6 个字符；按用户给出的安全方式处理，不生成并公开可预测的默认密码。disable 和 reset_password 撤销旧会话与令牌；enable 不恢复已撤销的凭据。

grant.list 需要 grant。授予或撤销通过服务端检查操作者权限，不能授予自己没有的权限。只有系统管理员可改变业务身份绑定。grant 的 permissions 是**完整替换列表**，不是增量追加。先 list，保留用户没有要求撤销的权限和当前 business_user_id；未绑定身份为 0。要求显式提供 business_user_id 是为了避免缺省零值意外改变已有绑定。普通授权操作者必须原样保留绑定；需修改绑定时由有权限的系统管理员处理。

权限键：config.read（读配置）、config.write（改名称）、policy.write（改策略）、mcp.write（配置 MCP）、knowledge.read（读已发布知识）、knowledge.write（维护知识）、query（改写查询）、raw_query（绕过行字段策略的只读查询）、audit.read（审计）、grant（授权）、impersonate（模拟业务身份）。知识读取独立于 query：本库有 knowledge.read 即可读取全部已发布知识；业务 MCP 还需开启 MCP 与知识 Prompts。

撤销全部数据库权限示例：

```json
{"action":"revoke","args":{"resourceType":"grant","database_id":"default","user_id":42}}
```

revoke 清空该用户该数据库的全部权限，保留业务身份绑定。若只撤销部分权限，先读取现有列表，再用 grant 提交剩余完整列表。操作后用 user.list 或 grant.list 核对，不附带更改其他数据库授权。


## 逐库模拟业务账号

平台用户用于管理；业务账号用于指定数据库 MCP 的员工认证，二者独立。database.authentication.type 仅支持 local，省略配置时也使用当前库独立业务账号。新增数据库账号为空。不要为了员工使用业务 MCP 而创建平台管理用户。

- `datascope_query`：`action=list`，`args={resourceType:"business_account",database_id:"default"}`。
- `datascope_manage`：`action=save`，`args={resourceType:"business_account",database_id:"default",account:{database_id:"default",username:"E0001",name:"员工姓名",business_user_id:1,permissions:["query","knowledge.read"],active:true},password:"至少6字符的初始密码",revision:0}`。
- 更新账号先列表读取其 revision，再 save；password 留空保留原密码，active=false 停用。每次更新都会撤销旧访问令牌和刷新令牌。
- 仅平台系统管理员可修改业务账号及数据库 authentication；list 需要当前库 grant 权限。业务账号仅支持 query、knowledge.read、raw_query、action，不能获得管理能力。
- employee_directory=true 时，工号和业务身份必须对应当前数据库的 employees 目录，离职员工不能认证。其他数据库可关闭目录校验。
- 同名工号可以属于多个 database，各自密码、身份编号和权限互不继承。
