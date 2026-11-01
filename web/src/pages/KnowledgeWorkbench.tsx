import { lazy, Suspense, useEffect, useRef, useState } from 'react';
import { Button, Card, Empty, Input, Space, Spin, Tabs, Tag, Typography } from 'antd';
import type { KnowledgePrompt, KnowledgePrompts, KnowledgePromptResult } from '../api';
import { HelpTip } from '../components/HelpTip';
import { CallDetails, JsonData, invoke, type Invocation } from '../components/ToolCallTrace';
import { useI18n } from '../ui/preferences';
import { selectResource, useRoute, useRouteChoice } from '../navigation';

const KnowledgeContent = lazy(() => import('../components/KnowledgeContent'));
export default function KnowledgeWorkbench({ databaseID }: { databaseID: string }) {
 // A database change starts a fresh workflow, discarding pending responses.
 return <KnowledgeWorkflow key={databaseID} databaseID={databaseID} />;
}

function KnowledgeWorkflow({ databaseID }: { databaseID: string }) {
 const t = useI18n();
 const route=useRoute();
 const [query, setQuery] = useState('');
 const [knowledgeID, setKnowledgeID] = useState('');
 const [searchResult, setSearchResult] = useState<Invocation<KnowledgePrompts>>();
 const [entries,setEntries]=useState<KnowledgePrompt[]>([]);
 const [readResult, setReadResult] = useState<Invocation<KnowledgePromptResult>>();
 const [searching, setSearching] = useState(false);
 const [reading, setReading] = useState(false);
 const [searchTab, setSearchTab] = useRouteChoice('listView','list',['list','json']);
 const [readTab, setReadTab] = useRouteChoice('detailView','json',['json','preview']);
 const selectedRequest=useRef('');
 const searchSequence = useRef(0);
 const readSequence = useRef(0);
 useEffect(() => () => { selectedRequest.current=''; searchSequence.current++; readSequence.current++; }, []);

 const selectID = (id: string, replace=false) => {
  readSequence.current++;
  selectedRequest.current=id;
  setKnowledgeID(id); setReadResult(undefined); setReading(false);selectResource(id||undefined,replace);setReadTab('json',true);
 };
 const search = async (cursor?:string,clear=true) => {
  const request = ++searchSequence.current;
  if(!cursor){if(clear)selectID('');setEntries([])} setSearching(true); setSearchResult(undefined); if(clear)setSearchTab('list',true);
  const result = await invoke<KnowledgePrompts>(databaseID, 'list_knowledge_prompts', cursor?{cursor}:{});
  if (request !== searchSequence.current) return;
  setSearchResult(result); if(result.data)setEntries(previous=>cursor?[...previous,...result.data!.prompts]:result.data!.prompts); setSearching(false);
 };
 const read = async (name=knowledgeID.trim()) => {
  if (!name) return;
  const request = ++readSequence.current;
  setReading(true); setReadResult(undefined);
  const result = await invoke<KnowledgePromptResult>(databaseID, 'get_knowledge_prompt', { name });
  if (request !== readSequence.current) return;
  setReadResult(result); setReading(false);
 };
 useEffect(()=>{
  const name=route.selection||'';
  if(name===selectedRequest.current)return;
  selectedRequest.current=name;readSequence.current++;setKnowledgeID(name);setReadResult(undefined);setReading(false);
  if(name){void search(undefined,false);void read(name);}
 },[route.selection]);
 const items = searchResult?.data ? entries.filter(item=>`${item.name} ${item.title} ${item.description}`.toLowerCase().includes(query.toLowerCase())) : undefined;
 const document = readResult?.data;

 return <Space orientation="vertical" style={{ width: '100%' }} size="middle">
  <Space wrap size="small">
   <Tag>{t('MCP 流程模拟')}</Tag>
   <Typography.Text type="secondary">{t('查询列表 → 选择 ID → 读取正文')}</Typography.Text>
   <Space size={4}><Typography.Text type="secondary">{t('当前平台权限')}</Typography.Text><HelpTip label={t('模拟身份')}>{t('复用业务 MCP 的知识检索逻辑，使用当前平台账号权限。业务账号的实际可见范围需通过业务 MCP 登录验证。')}</HelpTip></Space>
  </Space>
  <div className="knowledge-call-workspace">
   <Card size="small" className="knowledge-call-panel knowledge-search-step" title={t('1. 查询知识列表')} extra={<Typography.Text code>prompts/list</Typography.Text>}>
    <form className="knowledge-call-form" onSubmit={event => { event.preventDefault(); void search(); }}>
     <Input aria-label={t('查询知识')} placeholder={t('筛选已加载的知识描述')} value={query} allowClear onChange={event => setQuery(event.target.value)} />
     <Button type="primary" htmlType="submit" loading={searching}>{t('查询列表')}</Button>
    </form>
    {searchResult && <CallDetails invocation={searchResult} />}
    <Spin spinning={searching}>
     {items ? <Tabs size="small" activeKey={searchTab} onChange={setSearchTab} items={[
      { key: 'list', label: <Space size="small">{t('知识列表')}<Tag>{items.length}</Tag></Space>, children: <>
       <ul className="knowledge-call-results">
        {items.map(item => <li key={item.name} className={knowledgeID === item.name ? 'selected' : undefined}>
         <div className="knowledge-call-entry-heading"><strong>{item.title}</strong><Button size="small" onClick={() => selectID(item.name)}>{t('使用此 ID')}</Button></div>
         <Typography.Text className="mono" type="secondary" copyable={{ text: item.name }}>{item.name}</Typography.Text>
         <p className="knowledge-call-description">{item.description}</p>
        </li>)}
       </ul>
       {!items.length && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('没有匹配的已发布知识')} />}
       {searchResult?.data?.nextCursor && <Button loading={searching} onClick={()=>void search(searchResult.data!.nextCursor)}>{t('加载下一页')}</Button>}
      </> },
      { key: 'json', label: t('返回 JSON'), children: <JsonData value={searchResult?.data} /> },
     ]} /> : !searchResult?.error && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('点击查询列表，查看模型先收到的名称、ID 和 description。')} />}
    </Spin>
   </Card>
   <Card size="small" className="knowledge-call-panel knowledge-read-step" title={t('2. 按 ID 读取正文')} extra={<Typography.Text code>prompts/get</Typography.Text>}>
    <form className="knowledge-call-form" onSubmit={event => { event.preventDefault(); void read(); }}>
     <Input aria-label={t('知识 ID')} placeholder={t('从列表选择或输入知识 ID')} value={knowledgeID} disabled={!items || searching} allowClear onChange={event => selectID(event.target.value,true)} />
     <Button type="primary" htmlType="submit" disabled={!items || searching || !knowledgeID.trim()} loading={reading}>{t('读取正文')}</Button>
    </form>
    {readResult && <CallDetails invocation={readResult} />}
    <Spin spinning={reading}>
     {document ? <Tabs size="small" activeKey={readTab} onChange={setReadTab} items={[
      { key: 'json', label: t('返回 JSON'), children: <JsonData value={document} /> },
      { key: 'preview', label: t('正文预览'), children: <><Typography.Title level={5}>{entries.find(item=>item.name===knowledgeID)?.title||knowledgeID}</Typography.Title><Suspense fallback={<Spin />}><KnowledgeContent content={document.messages.map(message=>message.content.text).join('\n\n')} /></Suspense></> },
     ]} /> : !readResult?.error && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('先查询列表，再选择 ID 并点击读取正文。')} />}
    </Spin>
   </Card>
  </div>
 </Space>;
}
