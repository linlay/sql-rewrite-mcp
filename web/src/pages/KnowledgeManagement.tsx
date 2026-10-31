import { lazy, Suspense, useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Empty, Form, Input, Modal, Space, Spin, Switch, Tag, message } from 'antd';
import { managementCall, type Database, type Knowledge } from '../api';
import { CodeEditor } from '../components/CodeEditor';
import { useI18n } from '../ui/preferences';

const KnowledgeContent = lazy(() => import('../components/KnowledgeContent'));
type Fields = Pick<Knowledge, 'id' | 'name' | 'description' | 'tables' | 'published'> & { content: string };
const fieldsOf = (item: Pick<Knowledge, 'id' | 'name' | 'description' | 'tables' | 'published' | 'content'>): Fields => ({ id: item.id, name: item.name, description: item.description, tables: item.tables || [], published: item.published, content: item.content || '' });

export function KnowledgePage({ db }: { db: Database }) {
 const t = useI18n();
 const [items, setItems] = useState<Knowledge[]>([]);
 const [selected, setSelected] = useState<Knowledge>();
 const [draft, setDraft] = useState<Fields>();
 const [creating, setCreating] = useState(false);
 const [search, setSearch] = useState('');
 const [loading, setLoading] = useState(true);
 const [saving, setSaving] = useState(false);
 const [releaseRestrictions, setReleaseRestrictions] = useState(false);
 const [preview, setPreview] = useState(false);
 const [error, setError] = useState('');
 const [form] = Form.useForm<Fields>();
 const sequence = useRef(0);
 const listRef = useRef<HTMLUListElement>(null);
 const dirty = !!draft && (releaseRestrictions || creating || !selected || JSON.stringify(fieldsOf(draft)) !== JSON.stringify(fieldsOf(selected)));
 const locked = loading || saving;
 const canWrite = !!db.permissions?.includes('knowledge.write');
 const edit = (item?: Knowledge, isNew = false) => {
  setReleaseRestrictions(false); setSelected(isNew ? undefined : item); setCreating(isNew); setDraft(item ? fieldsOf(item) : undefined); setPreview(false); setError('');
  form.resetFields(); if (item) form.setFieldsValue(fieldsOf(item));
 };
 const reload = async (id?: string) => {
  const request = ++sequence.current; setLoading(true); setError('');
  try {
   const entries = await managementCall<Knowledge[]>('list_knowledge', { database_id: db.id });
   const target = entries.find(item => item.id === id) || entries[0];
   const full = target ? await managementCall<Knowledge>('get_knowledge', { database_id: db.id, id: target.id }) : undefined;
   if (request !== sequence.current) return;
   setItems(entries); edit(full);
  } catch (e) { if (request === sequence.current) setError((e as Error).message); }
  finally { if (request === sequence.current) setLoading(false); }
 };
 useEffect(() => { void reload(); return () => { sequence.current++; }; }, [db.id]);
 useEffect(() => {
  if (!dirty) return;
  const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; };
  window.addEventListener('beforeunload', warn); return () => window.removeEventListener('beforeunload', warn);
 }, [dirty]);
 useEffect(() => {
  const frame = window.requestAnimationFrame(() => listRef.current?.querySelector('.selected')?.scrollIntoView({ block: 'nearest' }));
  return () => window.cancelAnimationFrame(frame);
 }, [selected?.id, creating]);
 const navigate = (work: () => void | Promise<void>) => {
  if (locked) return;
  if (dirty) Modal.confirm({ title: t('放弃当前知识的未保存修改？'), onOk: work }); else void work();
 };
 const choose = async (item: Knowledge) => {
  const request = ++sequence.current; setLoading(true); setError('');
  try { const full = await managementCall<Knowledge>('get_knowledge', { database_id: db.id, id: item.id }); if (request === sequence.current) edit(full); }
  catch (e) { if (request === sequence.current) setError((e as Error).message); }
  finally { if (request === sequence.current) setLoading(false); }
 };
 const save = async () => {
  try {
   const values = await form.validateFields(); setSaving(true); setError('');
   const full = await managementCall<Knowledge>('save_knowledge', { ...values, tables: undefined, release_knowledge_restrictions: releaseRestrictions, database_id: db.id, revision: selected?.revision || 0 });
   setItems(old => old.some(item => item.id === full.id) ? old.map(item => item.id === full.id ? full : item) : [...old, full]); edit(full);
   if (!`${full.id} ${full.name} ${full.description}`.toLowerCase().includes(search.toLowerCase())) setSearch('');
   message.success(t('知识已保存'));
  } catch (e) { if (e instanceof Error) setError(e.message); }
  finally { setSaving(false); }
 };
 const visible = items.filter(item => `${item.id} ${item.name} ${item.description}`.toLowerCase().includes(search.toLowerCase()));
 return <div className="knowledge-workspace">
  <Card className="knowledge-list-panel" title={<Space>{t('知识列表')}<Tag>{items.length}</Tag></Space>} extra={<Button size="small" type="primary" disabled={!canWrite || locked || creating} onClick={() => navigate(() => { setSearch(''); edit({ id: crypto.randomUUID(), database_id: db.id, name: '', description: '', content: '', tables: [], published: false, revision: 0 }, true); })}>{t('新增知识')}</Button>}>
   <Input.Search aria-label={t('搜索知识列表')} placeholder={t('搜索名称、描述或 ID')} allowClear value={search} onChange={event => setSearch(event.target.value)} />
   <ul ref={listRef} className="knowledge-list">
    {creating && <li><button type="button" className="knowledge-list-item knowledge-management-item selected" aria-current="true"><strong title={draft?.name}>{draft?.name || t('未命名知识')}</strong><span className="knowledge-entry-id" title={draft?.id}>{draft?.id}</span><Tag className="knowledge-entry-status" color="orange">{t('未保存')}</Tag></button></li>}
    {visible.map(item => <li key={item.id}><button type="button" className={`knowledge-list-item knowledge-management-item${!creating && selected?.id === item.id ? ' selected' : ''}`} disabled={locked} aria-current={!creating && selected?.id === item.id ? 'true' : undefined} onClick={() => { if (creating || item.id !== selected?.id) navigate(() => choose(item)); }}>
     <strong title={item.name}>{item.name}</strong><span className="knowledge-entry-id" title={item.id}>{item.id}</span><Tag className="knowledge-entry-status" color={item.published ? 'green' : 'default'}>{item.published ? t('已发布') : t('草稿')}</Tag>
    </button></li>)}
    {!visible.length && !creating && !loading && <li><Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('暂无知识')} /></li>}
   </ul>
  </Card>
  <Card className="knowledge-editor-panel" loading={loading} title={<Space>{creating ? t('新增知识') : selected?.name || t('选择知识')}{dirty && <Tag color="orange">{t('未保存')}</Tag>}</Space>} extra={<Space wrap>
   <Button size="small" disabled={locked} onClick={() => navigate(() => reload(selected?.id))}>{t('刷新')}</Button>
   {draft && <Button size="small" disabled={locked} onClick={() => setPreview(!preview)}>{preview ? t('编辑正文') : t('预览正文')}</Button>}
   <Button size="small" type="primary" loading={saving} disabled={!canWrite || !dirty || loading} onClick={() => void save()}>{t('保存知识')}</Button>
  </Space>}>
   {error && <Alert type="error" showIcon title={error} style={{ marginBottom: 16 }} />}
   {draft ? <Form form={form} layout="vertical" disabled={!canWrite || locked} onValuesChange={(_, values) => setDraft({ ...values, tables: selected?.tables || [] })}>
    <div className="knowledge-fields-grid">
     <Form.Item name="id" label={t('知识 ID')} rules={[{ required: true, pattern: /^[^/]+$/ }]}><Input disabled={!!selected?.revision} /></Form.Item>
     <Form.Item name="name" label={t('名称')} rules={[{ required: true }]}><Input /></Form.Item>
     <Form.Item name="description" label={t('描述')} tooltip={t('作为知识列表和搜索结果的描述发送给模型，可填写用途、适用问题和主要内容。')} rules={[{ required: true }]}><Input.TextArea rows={3} aria-label={t('知识描述')} /></Form.Item>
     <div>
      {!!selected?.tables?.length && <Alert type={releaseRestrictions ? 'warning' : 'info'} showIcon title={t(releaseRestrictions ? '保存后解除旧关联限制' : '保留旧关联限制')} description={<>{selected.tables.join(', ')}<p>{t('旧记录仍按这些表的开放范围过滤；普通保存保留限制。解除后，本库有知识读取权限的用户可读取已发布正文。')}</p><Button size="small" disabled={!canWrite || locked} onClick={() => releaseRestrictions ? setReleaseRestrictions(false) : Modal.confirm({ title: t('解除旧关联限制？'), content: t('解除后，本库有知识读取权限的用户可读取已发布正文。保存知识后生效。'), onOk: () => setReleaseRestrictions(true) })}>{t(releaseRestrictions ? '保留限制' : '解除旧关联限制')}</Button></>} />}

      <Form.Item name="published" label={t('发布')} valuePropName="checked" tooltip={t('发布后才可在知识工作台和业务 MCP 中查询。')}><Switch /></Form.Item>
     </div>
    </div>
    <Form.Item name="content" label={t('正文')} rules={[{ required: true }]} hidden={preview}><CodeEditor language="markdown" height="440px" wrap readOnly={!canWrite || locked} /></Form.Item>
    {preview && <div className="knowledge-preview"><Suspense fallback={<Spin />}><KnowledgeContent content={draft.content || ''} /></Suspense></div>}
   </Form> : !error && <Empty description={t('从左侧选择知识，或新增一条知识。')} />}
  </Card>
 </div>;
}
