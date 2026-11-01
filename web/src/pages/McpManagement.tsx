import { useI18n } from '../ui/preferences';
import { useEffect, useState } from 'react';
import { Alert, Button, Card, Checkbox, Input, Space, Switch, Tag, Typography, message } from 'antd';
import { databases, managementCall, type Database } from '../api';

const { Text } = Typography;
const toolOptions = [
 {name:"list_actions",label:"动作列表",help:"列出可用 Action 的名称与说明，需要业务账号 action 能力。"},
 {name:"describe_action",label:"查看业务操作",help:"按名称读取单个 Action 的参数格式，需要业务账号 action 能力。"},
 {name:"invoke_action",label:"执行业务操作",help:"在独立事务中执行业务 Action。勾选并保存后启用动作执行，需要业务账号 action 能力；人工确认在独立确认页完成。"},
 { name: 'list_tables', label: '表目录', help: '列出当前数据库允许访问的数据表。' },
  { name: 'describe_table', label: '表结构', help: '查看指定表允许访问的字段、类型和访问方案。' },
  { name: 'query', label: '权限查询', help: '只读查询，应用表、字段、行权限和返回行数限制。规则在“查询配置”中配置。' },
  { name: 'raw_query', label: '原始查询', admin: true, help: '绕过表、字段和行权限改写，仍保持只读。需要当前数据库的全量查询权限。' },
  { name: 'customer_scope', label: '客户范围', admin: true, help: '核对证券样例库的客户可见范围，需要当前数据库的全量查询权限。' },
];

export function McpManagement({ id, onSaved }: { id: string; onSaved: () => void }) {
 const t=useI18n();
  const [db, setDB] = useState<Database>();
  const [enabled, setEnabled] = useState(false);
 const [knowledgeEnabled,setKnowledgeEnabled]=useState(false);
  const [tools, setTools] = useState<string[]>([]);
  const [mcpPath, setMcpPath] = useState('');
  const [writeUsername, setWriteUsername] = useState('');
  const [writePassword, setWritePassword] = useState('');

  const [error, setError] = useState<string>();
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let live = true;
    databases.get(id).then((item) => {
      if (!live) return;
      setDB(item);
      setWriteUsername(item.write_connection?.username || '');
      setWritePassword('');
      setEnabled(item.mcp_enabled);
      setTools(item.tools.filter(name=>!['search_knowledge','read_knowledge'].includes(name)));
 setKnowledgeEnabled(item.knowledge_enabled??item.tools.some(name=>['search_knowledge','read_knowledge'].includes(name)));
      setMcpPath(item.mcp_path || item.id);
    }).catch((e: Error) => { if (live) setError(e.message); });
    return () => { live = false; };
  }, [id]);

  if (!db) return <Card loading={!error}>{error && <Alert type="error" title={error} />}</Card>;

  const pathValid = /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(mcpPath);
  const endpoint = new URL(db.mcp_endpoint || `/mcp/${db.mcp_path || db.id}`, window.location.origin);
  const prefix = endpoint.pathname.slice(0, endpoint.pathname.lastIndexOf('/') + 1);
  const originalTools=db.tools.filter(name=>!['search_knowledge','read_knowledge'].includes(name));
 const canConfigureActions = !!db.permissions?.includes('action.write');
 const writeEnabled = canConfigureActions ? tools.includes('invoke_action') : !!db.write_enabled;
 const needsWriteAccount = writeEnabled && db.driver !== 'sqlite';
 const writeAccountValid = !needsWriteAccount || !!writeUsername.trim() && (!!db.write_connection || !!writePassword);
 const writeAccountChanged = needsWriteAccount && (writeUsername !== (db.write_connection?.username || '') || !!writePassword);
 const dirty = writeEnabled !== !!db.write_enabled || writeAccountChanged || knowledgeEnabled!==(db.knowledge_enabled??db.tools.some(name=>['search_knowledge','read_knowledge'].includes(name))) || mcpPath !== (db.mcp_path || db.id) || enabled !== db.mcp_enabled || tools.length !== originalTools.length || tools.some((tool) => !originalTools.includes(tool));
  const save = async () => {
    setSaving(true);
    setError(undefined);
    try {

 const writeConnection = needsWriteAccount && writeAccountChanged ? {
  ...db.connection!, username: writeUsername.trim(),
  password_env: writePassword ? undefined : db.write_connection?.password_env,
  credential_id: writePassword ? undefined : db.write_connection?.credential_id,
 } : db.write_connection;
 const saved = await managementCall<Database>('save_database', {
  database_id: db.id,
  database: { ...db, discovery: undefined, mcp_enabled: enabled, mcp_path: mcpPath, tools, knowledge_enabled: knowledgeEnabled, write_enabled: writeEnabled, write_connection: writeConnection },
  write_password: needsWriteAccount ? writePassword : '',
 });
      setDB(saved);
      setEnabled(saved.mcp_enabled);
      setWriteUsername(saved.write_connection?.username || '');
      setWritePassword('');
      setTools(saved.tools.filter(name => !['search_knowledge', 'read_knowledge'].includes(name)));setKnowledgeEnabled(saved.knowledge_enabled??false);
      setMcpPath(saved.mcp_path || saved.id);
      message.success(t("MCP 配置已保存"));
      onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Card title={<Space>{t("MCP 配置")}<Switch size="small" aria-label={t("开启 MCP")} disabled={!db.permissions?.includes('mcp.write') || saving} checked={enabled} onChange={setEnabled} /></Space>} extra={
        <Space>
          {dirty && <Tag color="orange">{t("有未保存修改")}</Tag>}
          <Button type="primary" loading={saving} disabled={!db.permissions?.includes('mcp.write') || !dirty || !pathValid || !writeAccountValid} onClick={save}>{t("保存并生效")}</Button>
        </Space>
      }>
        <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
          <Space orientation="vertical" style={{ width: '100%' }}>
            <Input aria-label={t("MCP 路径")} disabled={!db.permissions?.includes('mcp.write') || saving} addonBefore={prefix} value={mcpPath} onChange={(e) => setMcpPath(e.target.value)} status={!pathValid ? 'error' : undefined} placeholder={t("自定义路径，例如 securities")} maxLength={64} />
            {!pathValid && <Text type="danger">{t("请输入字母或数字开头的路径，仅允许字母、数字、下划线和连字符，最多 64 位。")}</Text>}
            <div className="mcp-current-address"><Text type="secondary">{t("当前地址")}：</Text><Text copyable={{ text: endpoint.href }} className="mono">{endpoint.href}</Text></div>
          </Space>
          <section className="mcp-capability-section" aria-label={t("Tools · 工具")} >
            <Text strong>{t("Tools · 工具")}</Text>
            <Checkbox.Group disabled={!db.permissions?.includes('mcp.write') || saving} value={tools} onChange={(values) => setTools(values as string[])} className="mcp-tool-options">
              {toolOptions.map((tool) => (
                <div className="mcp-tool-option" key={tool.name}>
                  <Checkbox value={tool.name} disabled={!db.permissions?.includes('mcp.write') || saving || tool.name === 'invoke_action' && !canConfigureActions}><span className="mono">{tool.name}</span> · {t(tool.label)}</Checkbox>
                  {tool.admin && <Tag color="orange">{t("管理员")}</Tag>}
                  <Text type="secondary" className="mcp-capability-description">{t(tool.help)}</Text>
                </div>
              ))}
            </Checkbox.Group>
            {needsWriteAccount && <Space orientation="vertical" style={{ width: '100%' }}>
              <Text strong>{t('写入账号')}</Text>
              <Input aria-label={t('写入账号')} autoComplete="off" placeholder={t('写入账号')} value={writeUsername} disabled={!canConfigureActions || saving} onChange={event => setWriteUsername(event.target.value)} />
              <Input.Password aria-label={t('连接密码')} autoComplete="new-password" placeholder={db.write_connection ? t('留空保留原密码') : t('连接密码')} value={writePassword} disabled={!canConfigureActions || saving} onChange={event => setWritePassword(event.target.value)} />
            </Space>}
            {tools.includes('raw_query') && <Alert type="warning" showIcon title={t("原始查询绕过访问策略，需要单独授予全量查询权限。")} />}
          </section>
          <section className="mcp-capability-section" aria-label={t('Resources · 视图')}>
           <Text strong>{t('Resources · 视图')}</Text>
           <div className="mcp-tool-option"><Text><span className="mono">resources/list</span> · {t('视图列表')}</Text><Text type="secondary" className="mcp-capability-description">{t('列出当前数据库已发布的 HTML 视图，type 为 view。')}</Text></div>
           <div className="mcp-tool-option"><Text><span className="mono">resources/read</span> · {t('读取视图')}</Text><Text type="secondary" className="mcp-capability-description">{t('按 URI 返回 HTML 正文，在视图配置中管理和发布。')}</Text></div>
          </section>
          <section className="mcp-capability-section" aria-label={t('Prompts · 提示词')}>
            <Space><Text strong>{t('Prompts · 提示词')}</Text><Switch size="small" aria-label={t('知识 Prompts')} disabled={!db.permissions?.includes('mcp.write') || saving} checked={knowledgeEnabled} onChange={setKnowledgeEnabled} /></Space>
            <div className="mcp-tool-option">
              <Text><span className="mono">prompts/list</span> · {t('知识列表')}</Text>
              <Text type="secondary" className="mcp-capability-description">{t('列出当前用户可访问的已发布知识名称和描述，不返回正文。')}</Text>
            </div>
            <div className="mcp-tool-option">
              <Text><span className="mono">prompts/get</span> · {t('读取知识')}</Text>
              <Text type="secondary" className="mcp-capability-description">{t('按名称读取知识正文，作为 Prompt 消息返回；与知识列表一起启停。')}</Text>
            </div>
          </section>
        </Space>
      </Card>
      {error && <Alert type="error" showIcon title={t("MCP 配置保存失败")} description={error} closable onClose={() => setError(undefined)} />}
      {db.mcp_enabled ? <Alert type="info" showIcon title={t("此数据库使用独立业务认证")} description={db.access?.policy.mode === 'NONE' ? t('无需认证') : `${db.access?.policy.mode === 'ACCOUNT' ? t('账号密码（Token）') : 'OAuth'} · ${db.access?.provider.name || t('本库业务账号')}`}  /> : (
        <Alert type="info" showIcon title={t("MCP 已关闭，开启并保存后可测试。")} />
      )}
    </Space>
  );
}
