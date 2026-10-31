import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Empty, Input, InputNumber, Space, Spin, Table, Tabs, Tag, Typography } from 'antd';
import type { ActionDescription, ActionPreview, ActionSchema, Database } from '../api';
import { useAuth } from '../Auth';
import { CodeEditor } from '../components/CodeEditor';
import BusinessActionWorkbench from '../components/BusinessActionWorkbench';
import { HelpTip } from '../components/HelpTip';
import { CallDetails, JsonData, invoke, type Invocation } from '../components/ToolCallTrace';
import { useI18n } from '../ui/preferences';

function argumentTemplate(schema?: ActionSchema) {
 const values: Record<string, unknown> = {};
 for (const name of schema?.required || []) {
  const field = schema!.properties[name];
  values[name] = field.enum?.[0] ?? (field.type === 'boolean' ? false : ['integer', 'number'].includes(field.type) ? field.minimum ?? 0 : '');
 }
 return JSON.stringify(values, null, 2);
}

export default function ActionWorkbench({ db }: { db: Database }) {
 const t = useI18n();
 return <Tabs key={db.id} destroyOnHidden items={[{key:'trial',label:t('试运行（回滚）'),children:<ActionWorkflow db={db} />},{key:'business',label:t('真实业务调用'),children:<BusinessActionWorkbench key={db.id} db={db} />}]} />;
}

function ActionWorkflow({ db }: { db: Database }) {
 const t = useI18n();
 const { user } = useAuth();
 const [list, setList] = useState<Invocation<{ actions: ActionDescription[] }>>();
 const [detail, setDetail] = useState<Invocation<ActionDescription>>();
 const [trial, setTrial] = useState<Invocation<ActionPreview>>();
 const [selected, setSelected] = useState('');
 const [filter, setFilter] = useState('');
 const [args, setArgs] = useState('{}');
 const [identity, setIdentity] = useState<number | null>(db.business_user_id || 1);
 const [requestID, setRequestID] = useState('');
 const [validation, setValidation] = useState('');
 const [listing, setListing] = useState(false);
 const [describing, setDescribing] = useState(false);
 const [running, setRunning] = useState(false);
 const [listTab, setListTab] = useState('list');
 const [detailTab, setDetailTab] = useState('parameters');
 const [resultTab, setResultTab] = useState('preview');
 const listSequence = useRef(0), detailSequence = useRef(0), runSequence = useRef(0);
 useEffect(() => () => { listSequence.current++; detailSequence.current++; runSequence.current++; }, []);
 const canImpersonate = user.system_admin || !!db.permissions?.includes('impersonate');
 const resetTrial = () => {
  runSequence.current++; setRunning(false); setTrial(undefined); setValidation(''); setRequestID(crypto.randomUUID());
 };
 const loadList = async () => {
  const request = ++listSequence.current; detailSequence.current++;
  resetTrial(); setSelected(''); setDetail(undefined); setDescribing(false); setList(undefined); setListing(true); setListTab('list');
  const result = await invoke<{ actions: ActionDescription[] }>(db.id, 'list_actions', {});
  if (request !== listSequence.current) return;
  setList(result); setListing(false);
 };
 const describe = async (name: string) => {
  const request = ++detailSequence.current;
  resetTrial(); setSelected(name); setDetail(undefined); setDescribing(true); setArgs('{}'); setDetailTab('parameters');
  const result = await invoke<ActionDescription>(db.id, 'describe_action', { name });
  if (request !== detailSequence.current) return;
  setDetail(result); setDescribing(false);
  if (result.data) setArgs(argumentTemplate(result.data.parameters));
 };
 const run = async () => {
  if (!detail?.data || !db.write_enabled || !canImpersonate || running) return;
  let arguments_: Record<string, unknown>;
  try {
   arguments_ = JSON.parse(args);
   if (!arguments_ || typeof arguments_ !== 'object' || Array.isArray(arguments_)) throw new Error(t('调用参数必须是 JSON 对象'));
   if (!identity || !Number.isSafeInteger(identity) || identity <= 0) throw new Error(t('需要正整数业务身份'));
   if (!requestID.trim() || requestID.length > 128 || requestID !== requestID.trim()) throw new Error(t('request_id 必填且最多 128 字符'));
  } catch (error) { setValidation((error as Error).message); return; }
  const request = ++runSequence.current;
  setValidation(''); setTrial(undefined); setRunning(true); setResultTab('preview');
  const result = await invoke<ActionPreview>(db.id, 'try_action', { name: detail.data.name, arguments: arguments_, business_user_id: identity, request_id: requestID });
  if (request !== runSequence.current) return;
  setTrial(result); setRunning(false);
 };
 const items = list?.data?.actions;
 const visible = items?.filter(item => `${item.name} ${item.title} ${item.description}`.toLowerCase().includes(filter.toLowerCase()));
 const action = detail?.data;
 const schema = action?.parameters;
 const writes = trial?.data?.preview?.writes || [];

 return <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
  <Space wrap size="small"><Tag>{t('MCP 流程模拟')}</Tag><Typography.Text type="secondary">{t('动作列表 → 参数说明 → 试运行')}</Typography.Text><Space size={4}><Tag color="blue">{t('始终回滚')}</Tag><HelpTip label={t('动作工作台')}>{t('以平台权限查看 describe_action 的同格式说明，使用 try_action 模拟执行。需要模拟业务身份权限；切换到真实业务调用，通过本人业务认证执行正式操作。')}</HelpTip></Space></Space>
  <div className="action-workbench">
   <Card size="small" className="knowledge-call-panel action-browse-step" title={t('1. 获取动作列表')} extra={<Typography.Text code>list_actions</Typography.Text>}>
    <div className="knowledge-call-form"><Input aria-label={t('搜索动作')} placeholder={t('搜索名称、标题、说明')} allowClear value={filter} onChange={event => setFilter(event.target.value)} /><Button type="primary" loading={listing} onClick={() => void loadList()}>{t('获取列表')}</Button></div>
    {list && <CallDetails invocation={list} />}
    <Spin spinning={listing}>
     {items ? <Tabs size="small" activeKey={listTab} onChange={setListTab} items={[
      { key: 'list', label: <Space size="small">{t('动作列表')}<Tag>{items.length}</Tag></Space>, children: <ul className="action-workbench-list">
       {visible?.map(item => <li key={item.name}><button type="button" className={`action-workbench-item${selected === item.name ? ' selected' : ''}`} aria-current={selected === item.name ? 'true' : undefined} onClick={() => void describe(item.name)}>
        <div><strong>{item.title}</strong>{item.confirm && <Tag>{t('人工确认')}</Tag>}</div><span className="mono">{item.name}</span><p>{item.description}</p>
       </button></li>)}
       {!visible?.length && <li><Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('暂无匹配动作')} /></li>}
      </ul> },
      { key: 'json', label: t('返回 JSON'), children: <JsonData value={list.data} /> },
     ]} /> : !list?.error && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('获取列表后，选择动作查看参数。')} />}
    </Spin>
   </Card>
   <Card size="small" className="knowledge-call-panel action-trial-step" title={t('2. 参数与试运行')} extra={<Typography.Text code>try_action</Typography.Text>}>
    <Spin spinning={describing}>
     {detail && <><Typography.Text code>describe_action</Typography.Text><CallDetails invocation={detail} /></>}
     {action ? <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <div><Typography.Text strong>{action.title}</Typography.Text><Typography.Text code>{action.name}</Typography.Text><p className="action-workbench-description">{action.description}</p></div>
      <Tabs size="small" activeKey={detailTab} onChange={setDetailTab} items={[
       { key: 'parameters', label: t('参数说明'), children: <Table size="small" rowKey="name" pagination={false} scroll={{ x: 480 }} dataSource={Object.entries(schema?.properties || {}).map(([name, field]) => ({ ...field, name, required: schema?.required.includes(name) }))} columns={[
        { title: t('参数'), dataIndex: 'name', render: (value: string) => <span className="mono">{value}</span> },
        { title: t('类型'), dataIndex: 'type' },
        { title: t('必填'), dataIndex: 'required', render: (value: boolean) => t(value ? '是' : '否') },
        { title: t('说明与限制'), render: (_, field) => <div>{field.description}<div className="action-parameter-constraints">{[field.enum && `enum: ${JSON.stringify(field.enum)}`, field.minimum !== undefined && `min: ${field.minimum}`, field.maximum !== undefined && `max: ${field.maximum}`, field.maxLength !== undefined && `maxLength: ${field.maxLength}`, field.pattern && `pattern: ${field.pattern}`].filter(Boolean).join(' · ')}</div></div> },
       ]} /> },
       { key: 'json', label: t('返回 JSON'), children: <JsonData value={action} /> },
      ]} />
      {!db.write_enabled && <Alert type="info" showIcon title={t('当前数据库未允许执行动作，请在动作配置中开启。')} />}
      {!canImpersonate && <Alert type="info" showIcon title={t('当前账号没有模拟业务身份权限，可查看参数，不能试运行。')} />}
      <div className="action-trial-inputs">
       <label><span>{t('业务身份编号')}</span><InputNumber aria-label={t('业务身份编号')} value={identity} min={1} max={Number.MAX_SAFE_INTEGER} precision={0} disabled={running} onChange={value => { setIdentity(value); resetTrial(); }} /></label>
       <label><span>request_id</span><Space.Compact block><Input aria-label="request_id" value={requestID} maxLength={128} disabled={running} onChange={event => { setRequestID(event.target.value); setTrial(undefined); setValidation(''); }} /><Button disabled={running} onClick={resetTrial}>{t('新请求')}</Button></Space.Compact></label>
      </div>
      <div className="action-trial-arguments"><div className="action-trial-arguments-heading"><Typography.Text>{t('调用参数 JSON')}</Typography.Text><Button size="small" disabled={running} onClick={() => { setArgs(argumentTemplate(schema)); resetTrial(); }}>{t('填入参数模板')}</Button></div><CodeEditor language="json" height="200px" value={args} onChange={value => { setArgs(value); resetTrial(); }} readOnly={running} /></div>
      {validation && <Alert type="error" showIcon title={validation} />}
      <Space wrap><Button type="primary" onClick={() => void run()} loading={running} disabled={!db.write_enabled || !canImpersonate}>{t(trial?.data ? '再次获取结果' : '试运行（回滚）')}</Button><Typography.Text type="secondary">{t('同一 request_id 重用结果；修改参数或身份后自动生成新请求。')}</Typography.Text></Space>
      {trial && <div className="action-trial-result">
       <CallDetails invocation={trial} failed={trial.data?.status === 'denied'} />
       {trial.data?.status === 'denied' && <Alert type="warning" showIcon title={trial.data.message || t('执行被拒绝')} />}
       {trial.data && <Tabs size="small" activeKey={resultTab} onChange={setResultTab} items={[
        { key: 'preview', label: t('试运行结果'), children: <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
         <Tag color={trial.data.status === 'preview' ? 'blue' : 'error'}>{t(trial.data.status === 'preview' ? '预览完成，事务已回滚' : '执行被拒绝')}</Tag>
         {trial.data.result !== undefined && <JsonData value={trial.data.result} />}
         <Typography.Text strong>{t('SQL 写入计划')} · {writes.length}</Typography.Text>
         {writes.map((write, index) => <div key={index}><Typography.Text type="secondary">{t('影响 {0} 行', { '0': write.rows })}</Typography.Text><JsonData value={write} /></div>)}
        </Space> },
        { key: 'json', label: t('返回 JSON'), children: <JsonData value={trial.data} /> },
       ]} />}
      </div>}
     </Space> : !detail?.error && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('先获取动作列表，再选择动作。')} />}
    </Spin>
   </Card>
  </div>
 </Space>;
}
