import { useI18n } from '../ui/preferences';
import { useEffect, useState } from 'react';
import { Alert, Button, Card, Checkbox, Input, Space, Switch, Tag, Typography, message } from 'antd';
import { databases, type Database } from '../api';
import { HelpTip } from '../components/HelpTip';

const { Text } = Typography;
const toolOptions = [
 {name:"list_actions",label:"动作列表",help:"列出可用 Action 的名称与说明，需要业务账号 action 能力。"},
 {name:"describe_action",label:"查看业务操作",help:"按名称读取单个 Action 的参数格式，需要业务账号 action 能力。"},
 {name:"invoke_action",label:"执行业务操作",help:"在独立事务中执行业务 Action。需要启用写入连接和业务账号 action 能力；人工确认在独立确认页完成。"},
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

  const [error, setError] = useState<string>();
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let live = true;
    databases.get(id).then((item) => {
      if (!live) return;
      setDB(item);
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
  const previewEndpoint = `${endpoint.origin}${prefix}${mcpPath}`;
  const originalTools=db.tools.filter(name=>!['search_knowledge','read_knowledge'].includes(name));
 const dirty = knowledgeEnabled!==(db.knowledge_enabled??db.tools.some(name=>['search_knowledge','read_knowledge'].includes(name))) || mcpPath !== (db.mcp_path || db.id) || enabled !== db.mcp_enabled || tools.length !== originalTools.length || tools.some((tool) => !originalTools.includes(tool));
  const save = async () => {
    setSaving(true);
    setError(undefined);
    try {

 const saved = await databases.save({ ...db, mcp_enabled: enabled, mcp_path: mcpPath, tools, knowledge_enabled:knowledgeEnabled });
      setDB(saved);
      setEnabled(saved.mcp_enabled);
      setTools(saved.tools);setKnowledgeEnabled(saved.knowledge_enabled??false);
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
      <Card title={t("MCP 配置 · {0}", {"0":(db.name)})} extra={
        <Space>
          {dirty && <Tag color="orange">{t("有未保存修改")}</Tag>}
          <Button type="primary" loading={saving} disabled={!db.permissions?.includes('mcp.write') || !dirty || !pathValid} onClick={save}>{t("保存并生效")}</Button>
        </Space>
      }>
        <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
          <Space>
            <Switch aria-label={t("开启 MCP")} checked={enabled} onChange={setEnabled} />
            <Text strong>{enabled ? t("开启 MCP") : t("关闭 MCP")}</Text>
          </Space>
          <Space orientation="vertical" style={{ width: '100%' }}>
            <Space size={4}><Text strong>{t("MCP 路径")}</Text><HelpTip label={t("MCP 路径")}>{t("路径在所有数据库间唯一，允许字母、数字、下划线和连字符，最多 64 位。保存后立即生效，旧的分库地址失效，测试入口自动更新。")}</HelpTip></Space>
            <Input aria-label={t("MCP 路径")} addonBefore={prefix} value={mcpPath} onChange={(e) => setMcpPath(e.target.value)} status={!pathValid ? 'error' : undefined} placeholder={t("自定义路径，例如 securities")} maxLength={64} />
            {!pathValid && <Text type="danger">{t("请输入字母或数字开头的路径，仅允许字母、数字、下划线和连字符，最多 64 位。")}</Text>}
            <Text copyable={pathValid ? { text: previewEndpoint } : false} className="mono">{previewEndpoint}</Text>
          </Space>
          <Space><Switch aria-label={t('知识 Prompts')} checked={knowledgeEnabled} onChange={setKnowledgeEnabled}/><Text>{t('知识 Prompts')}</Text><Text type="secondary">prompts/list · prompts/get</Text></Space>
          <Space orientation="vertical" style={{ width: '100%' }}>
            <Space size={4}><Text strong>{t("开放工具")}</Text><HelpTip label={t("开放工具")}>{t("按当前数据库独立启停。保存后工具目录与调用权限立即更新，下方测试会显示实际开放的工具。")}</HelpTip></Space>
            <Checkbox.Group value={tools} onChange={(values) => setTools(values as string[])} className="mcp-tool-options">
              {toolOptions.map((tool) => (
                <div className="mcp-tool-option" key={tool.name}>
                  <Checkbox value={tool.name}><span className="mono">{tool.name}</span> · {t(tool.label)}</Checkbox>
                  {tool.admin && <Tag color="orange">{t("管理员")}</Tag>}
                  <HelpTip label={tool.name}>{t(tool.help)}</HelpTip>
                </div>
              ))}
            </Checkbox.Group>
            {tools.includes('raw_query') && <Alert type="warning" showIcon title={t("原始查询绕过访问策略，需要单独授予全量查询权限。")} />}
          </Space>
          <section className="mcp-capability-section" aria-label={t('Resources · 视图')}>
           <Text strong>{t('Resources · 视图')}</Text>
           <div className="mcp-tool-option"><Text><span className="mono">resources/list</span> · {t('视图列表')}</Text><Text type="secondary" className="mcp-capability-description">{t('列出当前数据库已发布的 HTML 视图，type 为 view。')}</Text></div>
           <div className="mcp-tool-option"><Text><span className="mono">resources/read</span> · {t('读取视图')}</Text><Text type="secondary" className="mcp-capability-description">{t('按 URI 返回 HTML 正文，在视图配置中管理和发布。')}</Text></div>
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
