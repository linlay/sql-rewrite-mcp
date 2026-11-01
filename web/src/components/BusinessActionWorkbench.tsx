import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Input, Modal, Select, Space, Typography } from 'antd';
import { platformRequest, type ActionDescription, type ActionPreview, type Database, type ViewResource } from '../api';
import { ViewFrame, type ViewData } from './ViewFrame';
import { CodeEditor } from './CodeEditor';
import { JsonData } from './ToolCallTrace';
import { useI18n } from '../ui/preferences';
import { readRoute, selectResource, useNavigationBlocker, useRoute } from '../navigation';

type Info = { mode: string; provider_type: string; authorization_url: string; redirect_uri: string; resource: string };
type Response = ActionPreview & { confirmation_url?: string };
type ToolResult<T> = { isError?: boolean; structuredContent?: T; content?: { type: string; text?: string }[] };

export default function BusinessActionWorkbench({ db, viewsOnly = false }: { db: Database; viewsOnly?: boolean }) {
 const t = useI18n();
 const route=useRoute();
 const selectedRequest=useRef('');
 const path = `/api/databases/${encodeURIComponent(db.id)}/business-mcp`;
 const [info, setInfo] = useState<Info>();
 const [resources, setResources] = useState<ViewResource[]>([]), [viewURI,setViewURI] = useState(''), [viewHTML,setViewHTML] = useState('');
 const [viewInput,setViewInput] = useState('{}'), [viewData,setViewData] = useState<ViewData>();
 const [viewLoading,setViewLoading] = useState(false);
 const [token, setToken] = useState('');
 const [username, setUsername] = useState(''), [password, setPassword] = useState('');
 const [actions, setActions] = useState<ActionDescription[]>([]), [detail, setDetail] = useState<ActionDescription>();
 const [args, setArgs] = useState('{}'), [requestID, setRequestID] = useState(crypto.randomUUID());
 const [attempted, setAttempted] = useState(false), [result, setResult] = useState<Response>();
 const [error, setError] = useState(''), [busy, setBusy] = useState(false);
 useNavigationBlocker(false,t('请先核对上一请求的结果。新请求可能再次写入，不会自动重试上一请求。'),busy && attempted);
 const submitted = useRef(false);
 const active = useRef(true), pending = useRef(false), popup = useRef<Window | null>(null);
 const oauth = useRef<{ state: string; verifier: string; clientID: string }>();
 const post = <T,>(body: Record<string, unknown>, bearer = token) => platformRequest<T>(path, { method: 'POST', headers: bearer ? { Authorization: `Bearer ${bearer}` } : {}, body: JSON.stringify(body) });
 const work = async (fn: () => Promise<void>) => {
  if (pending.current) return;
  pending.current = true; setBusy(true); setError('');
  try { await fn(); } catch (e) { if (active.current) setError((e as Error).message); }
  finally { pending.current = false; if (active.current) setBusy(false); }
 };
 const call = async <T,>(name: string, arguments_: Record<string, unknown> = {}): Promise<T> => {
  const out = await post<ToolResult<T>>({ operation: 'call', name, arguments: arguments_ });
  const text = out.content?.filter(c => c.type === 'text').map(c => c.text || '').join('\n');
  if (out.isError) throw new Error(text || t('执行被拒绝'));
  return out.structuredContent ?? JSON.parse(text || '{}') as T;
 };
 useEffect(() => {
  active.current = true;
  void work(async () => { const value = await platformRequest<Info>(path); if (active.current) setInfo(value); });
  return () => { active.current = false; oauth.current = undefined; popup.current?.close(); };
 }, [path]);
 useEffect(() => {
  const receive = (event: MessageEvent) => {
   const state = oauth.current;
   if (!state || event.origin !== window.location.origin || event.source !== popup.current || event.data?.type !== 'datascope-business-oauth' || event.data.state !== state.state) return;
   oauth.current = undefined;
   void work(async () => {
    if (!event.data.code || event.data.error) throw new Error(t('业务 OAuth 授权失败，请重新登录'));
    const value = await post<{ access_token: string }>({ operation: 'oauth_token', code: event.data.code, client_id: state.clientID, verifier: state.verifier }, '');
    if (active.current) setToken(value.access_token);
   });
  };
  window.addEventListener('message', receive); return () => window.removeEventListener('message', receive);
 }, [path]);
 const login = () => work(async () => {
  const credential = password; setPassword('');
  const value = await post<{ access_token: string }>({ operation: 'account_token', username, password: credential }, '');
  setPassword(''); if (active.current) setToken(value.access_token);
 });
 const authorize = () => {
  if (!info || pending.current) return;
  popup.current?.close(); popup.current = window.open('about:blank', 'datascope-business-oauth', 'width=640,height=780');
  if (!popup.current) { setError(t('请允许打开业务登录窗口')); return; }
  void work(async () => {
   const verifier = Array.from(crypto.getRandomValues(new Uint8Array(32)), n => n.toString(16).padStart(2, '0')).join('');
   const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier));
   const challenge = btoa(String.fromCharCode(...new Uint8Array(digest))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
   const client = await post<{ client_id: string }>({ operation: 'oauth_register' }, '');
   const state = crypto.randomUUID(); oauth.current = { state, verifier, clientID: client.client_id };
   const query = new URLSearchParams({ response_type: 'code', client_id: client.client_id, redirect_uri: info.redirect_uri, resource: info.resource, scope: 'sql:query', code_challenge: challenge, code_challenge_method: 'S256', state });
   if (active.current && popup.current) popup.current.location.href = `${info.authorization_url}?${query}`;
  });
 };
 const load = () => work(async () => {
  const out = await call<{ actions: ActionDescription[] }>('list_actions'); setActions(out.actions); setResult(undefined);
  const name=readRoute().selection;
  if(name){const item=await call<ActionDescription>('describe_action',{name});selectedRequest.current=name;setDetail(item);setArgs('{}');}else setDetail(undefined);
 });
 const describe = (name: string) => work(async () => { const out = await call<ActionDescription>('describe_action', { name }); selectedRequest.current=name;setDetail(out); setArgs('{}'); setResult(undefined); selectResource(name); });
 const execute = () => {
  if (submitted.current || pending.current) return;
  let values: Record<string, unknown>;
  try { values = JSON.parse(args); if (!values || typeof values !== 'object' || Array.isArray(values)) throw new Error(t('调用参数必须是 JSON 对象')); }
  catch (e) { setError((e as Error).message); return; }
  Modal.confirm({ title: t('正式调用业务 Action？'), content: t('不需要人工确认的动作会立即写入业务库；需要确认的动作将先返回回滚预览，再由本人打开确认页登录执行。'), okText: t('正式调用'), okButtonProps: { danger: true }, onOk: () => work(async () => {
   if (submitted.current) return;
   submitted.current = true; setAttempted(true); setResult(undefined);
   const out = await call<Response>('invoke_action', { name: detail!.name, arguments: values, request_id: requestID });
   if (active.current) setResult(out);
  }) });
 };
 const lookup = () => work(async () => { const out = await call<Response>('invoke_action', { name: detail!.name, arguments: {}, request_id: requestID, result_only: true }); if (active.current) setResult(out); });
 const reset = () => Modal.confirm({ title: t('开始新的业务请求？'), content: t('请先核对上一请求的结果。新请求可能再次写入，不会自动重试上一请求。'), onOk: () => { submitted.current = false; setAttempted(false); setRequestID(crypto.randomUUID()); setResult(undefined); setError(''); } });
 const viewCall = async (name:string,args:Record<string,unknown> = {}) => {
  if(name==='invoke_action'&&!args.result_only){
   const description=await call<ActionDescription>('describe_action',{name:args.name});
   if(!description.confirm)await new Promise<void>((resolve,reject)=>Modal.confirm({title:t('正式调用业务 Action？'),content:description.description,okText:t('正式调用'),okButtonProps:{danger:true},onOk:()=>resolve(),onCancel:()=>reject(new Error(t('已取消')))}));
  }
  return call(name,args);
 };
 const ready = info?.mode === 'NONE' || !!token.trim();
 useEffect(() => { setResources([]);setViewURI('');setViewHTML('');setViewData(undefined); }, [token]);
 const loadViews = () => work(async () => { const all:ViewResource[]=[];let cursor:string|undefined;do{const out: {resources:ViewResource[];nextCursor?:string}=await post({operation:'resources/list',cursor});all.push(...out.resources);cursor=out.nextCursor;}while(cursor);setResources(all.filter(v=>v._meta?.type==='view')); });
 const openView = async (uri:string) => {
  if (viewLoading) return; setViewLoading(true);setError('');setViewURI(uri);setViewHTML('');
  try { const input = JSON.parse(viewInput); if (!input || typeof input !== 'object' || Array.isArray(input)) throw new Error(t('调用参数必须是 JSON 对象'));
   const out = await post<{contents:{text?:string;mimeType?:string}[]}>({operation:'resources/read',uri});
   const html = out.contents.find(c=>c.mimeType==='text/html')?.text; if (!html) throw new Error(t('资源未返回 HTML')); setViewData({input});setViewHTML(html);
  }catch(e){setError((e as Error).message)}finally{setViewLoading(false)}
 };
 useEffect(()=>{
  if(viewsOnly||!ready||busy||attempted)return;
  const name=route.selection||'';
  if(name===selectedRequest.current)return;
  if(!name){selectedRequest.current='';setDetail(undefined);setResult(undefined);return;}
  selectedRequest.current=name;void load();
 },[route.selection,ready,busy,attempted,viewsOnly]);
 return <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
  <Alert type="warning" showIcon title={t(viewsOnly ? '业务预览' : '真实业务调用')} description={t('使用本人业务认证，通过真实 MCP 调用。Token 仅保留在当前页面内存。MCP 重启清空回执；结果未知时只查询回执并核对审计，不自动重试写入。')} />
  <Card size="small" title={t('业务身份认证')}>
   <Space orientation="vertical" style={{ width: '100%' }}>
    <Typography.Text>{info?.mode} · {info?.provider_type}</Typography.Text>
    {info?.mode === 'NONE' ? <Typography.Text>{t('按当前库无需认证入口的身份和能力执行。')}</Typography.Text> : <>
     {info?.mode === 'ACCOUNT' && <Space wrap><Input aria-label={t('业务用户名')} placeholder={t('业务用户名')} autoComplete="off" disabled={busy} value={username} onChange={e => setUsername(e.target.value)} /><Input.Password aria-label={t('业务账号密码')} placeholder={t('业务账号密码')} autoComplete="off" disabled={busy} value={password} onChange={e => setPassword(e.target.value)} /><Button disabled={busy || !username || !password} onClick={() => void login()}>{t('获取业务 Token')}</Button></Space>}
     {info?.provider_type === 'DEMO_OAUTH' && <Button disabled={busy} onClick={authorize}>{t('使用业务账号 OAuth 登录')}</Button>}
     <Input.Password aria-label={t('本人业务 Token')} placeholder={t('粘贴本人业务 Token')} autoComplete="off" disabled={busy} value={token} onChange={e => setToken(e.target.value)} />
     <Button size="small" disabled={busy || !token} onClick={() => { setToken(''); setPassword(''); }}>{t('清除业务 Token')}</Button>
    </>}
   </Space>
  </Card>
  {error && <Alert type="error" showIcon title={error} />}
  {viewsOnly && <Card size="small" title={t('Resources · 视图')} extra={<Button disabled={busy || !ready || viewLoading} loading={busy} onClick={()=>void loadViews()}>{t('获取视图列表')}</Button>}>
   <Space orientation="vertical" style={{width:'100%'}}>
    <Select aria-label={t('选择视图')} style={{width:'100%'}} value={viewURI||undefined} disabled={!ready||busy||viewLoading} placeholder={t('选择视图')} options={resources.map(v=>({value:v.uri,label:v.title||v.name}))} onChange={uri=>void openView(uri)} />
    <Typography.Text>{t('视图输入 JSON')}</Typography.Text><CodeEditor language="json" height="100px" value={viewInput} onChange={setViewInput} readOnly={viewLoading} />
    <Button disabled={!ready||!viewURI||busy} loading={viewLoading} onClick={()=>void openView(viewURI)}>{t('载入视图')}</Button>
    {viewHTML&&ready&&<ViewFrame html={viewHTML} data={viewData} onCall={viewCall} />}
   </Space>
  </Card>}
  {!viewsOnly && <Card size="small" title={t('动作列表与参数')} extra={<Button disabled={busy || !ready || attempted} loading={busy} onClick={() => void load()}>{t('获取列表')}</Button>}>
   <Space orientation="vertical" style={{ width: '100%' }}>
    <Select aria-label={t('选择业务动作')} style={{ width: '100%' }} disabled={busy || attempted} value={detail?.name||route.selection} placeholder={t('选择业务动作')} options={actions.map(a => ({ value: a.name, label: `${a.title} (${a.name})` }))} onChange={name => void describe(name)} />
    {detail && <><Typography.Paragraph>{detail.description}</Typography.Paragraph><JsonData value={detail.parameters} /><CodeEditor language="json" height="160px" value={args} readOnly={busy || attempted} onChange={setArgs} /><Typography.Text code>request_id: {requestID}</Typography.Text>
     {info?.mode === 'NONE' && detail.confirm && <Alert type="warning" showIcon title={t('此动作需要本人确认，请先配置账号或 OAuth 认证。匿名身份不能由其他账号代签。')} />}
     <Space wrap><Button danger type="primary" disabled={busy || !ready || attempted || info?.mode === 'NONE' && detail.confirm} onClick={execute}>{t('正式调用')}</Button><Button disabled={busy || !ready || !attempted} onClick={() => void lookup()}>{t('查询同一请求结果')}</Button><Button disabled={busy} onClick={reset}>{t('新请求')}</Button></Space>
    </>}
   </Space>
  </Card>
  }
  {result && <Card size="small" title={t('业务调用结果')}><Space orientation="vertical" style={{ width: '100%' }}>
   <Typography.Text strong>{result.status}</Typography.Text>
   {result.status === 'confirm_required' && <><Alert type="info" showIcon title={t('预览已回滚，等待本人确认')} /><Button href={result.confirmation_url} target="_blank" rel="noopener noreferrer">{t('打开确认页并登录确认')}</Button></>}
   {detail?.view_uri && <Button disabled={busy||viewLoading} loading={viewLoading} onClick={async()=>{ setViewLoading(true); try {const out=await post<{contents:{text?:string}[]}>({operation:'resources/read',uri:detail.view_uri});setViewHTML(out.contents[0]?.text||'');setViewData({input:{name:detail.name,arguments:JSON.parse(args),request_id:requestID},result});}catch(e){setError((e as Error).message)}finally{setViewLoading(false)} }}>{t('打开关联视图')}</Button>}
   {viewHTML&&!viewsOnly&&<ViewFrame html={viewHTML} data={viewData} onCall={viewCall}/>}
   <JsonData value={result} />
  </Space></Card>}
 </Space>;
}
