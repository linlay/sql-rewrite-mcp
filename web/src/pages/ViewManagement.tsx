import { useEffect, useRef, useState } from 'react';
import { DownOutlined, PlusOutlined } from '@ant-design/icons';
import { Alert, Button, Card, Dropdown, Empty, Form, Input, Modal, Select, Space, Switch, Tag, Tooltip, message } from 'antd';
import { databases, managementCall, type Database, type ViewDefinition } from '../api';
import { CodeEditor } from '../components/CodeEditor';
import { ViewFrame } from '../components/ViewFrame';
import BusinessActionWorkbench from '../components/BusinessActionWorkbench';
import { selectResource, useNavigationBlocker, useRoute, useRouteChoice } from '../navigation';
import { useI18n } from '../ui/preferences';

const comparable = (v?:ViewDefinition) => v && JSON.stringify([v.id,v.name,v.description,v.type,v.html,v.published]);
const blank: ViewDefinition = { id: '', name: '', description: '', type: 'view', html: '<!doctype html>\n<html lang="zh-CN">\n<head><meta charset="utf-8"><title>新视图</title></head>\n<body><h1>新视图</h1></body>\n</html>', published: false };

export default function ViewManagement({ db, onSaved }: { db: Database; onSaved: () => void }) {
 const t = useI18n(), route = useRoute();
 const [current, setCurrent] = useState(db), [draft, setDraft] = useState<ViewDefinition>();
 const [selected, setSelected] = useState<string>(), [creating, setCreating] = useState(false);
 const [defaultView, setDefaultView] = useState(db.query_view_id || '');
 const [templates, setTemplates] = useState<ViewDefinition[]>([]), [search, setSearch] = useState('');
 const [mode, setMode] = useRouteChoice('view', 'edit', ['edit', 'preview']);
 const [businessOpen, setBusinessOpen] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState('');
 const listRef = useRef<HTMLUListElement>(null);
 const [remoteChanged, setRemoteChanged] = useState(false);
 const [form] = Form.useForm<ViewDefinition>();
 const items = current.views || [], original = items.find(v => v.id === selected);
 const dirty = defaultView !== (current.query_view_id || '') || !!draft && (creating || comparable(draft) !== comparable(original));
 useNavigationBlocker(dirty, t('放弃当前视图的未保存修改？'), busy);
 const edit = (v?: ViewDefinition, isNew = false, sync = true) => {
  setSelected(isNew ? undefined : v?.id); setCreating(isNew); setDraft(v ? { ...v } : undefined); setError(''); form.resetFields(); if (v) form.setFieldsValue(v);
  if (sync && !isNew) selectResource(v?.id, true);
 };
 useEffect(() => {
  if (db.id !== current.id || !dirty) {
   setCurrent(db); setDefaultView(db.query_view_id || ''); setRemoteChanged(false); edit(db.views?.find(v => v.id === route.selection) || db.views?.[0]);
  } else if (db.revision > current.revision) setRemoteChanged(true);
 }, [db.id, db.revision]);
 useEffect(() => {
  if (route.selection === selected || creating) return;
  edit(items.find(v => v.id === route.selection) || items[0], false, false);
 }, [route.selection]);
 useEffect(() => {
  let active = true; managementCall<ViewDefinition[]>('list_view_templates', { database_id: db.id }).then(v => { if (active) setTemplates(v); }).catch(e => { if (active) setError(e.message); });
  return () => { active = false; };
 }, [db.id]);
 useEffect(() => {
  if (!dirty) return;
  const warn = (e: BeforeUnloadEvent) => { e.preventDefault(); e.returnValue = ''; };
  window.addEventListener('beforeunload', warn); return () => window.removeEventListener('beforeunload', warn);
 }, [dirty]);
 const change = (fn: () => void) => { if (busy) return; if (dirty) Modal.confirm({ title: t('放弃当前视图的未保存修改？'), onOk: () => { setDefaultView(current.query_view_id || ''); fn(); } }); else fn(); };
 const persist = async (views: ViewDefinition[], next?: string) => {
  setBusy(true); setError('');
  try {
   const saved = await managementCall<Database>('configure_views', { database_id: db.id, database: { ...current, discovery: undefined, views, query_view_id: defaultView } });
   setCurrent(saved); setDefaultView(saved.query_view_id || ''); setRemoteChanged(false); edit(saved.views?.find(v => v.id === next) || saved.views?.[0]); onSaved(); message.success(t('视图配置已保存'));
  } catch(e) { setError((e as Error).message); } finally { setBusy(false); }
 };
 const save = async () => {
  try {
   const v = draft ? { ...await form.validateFields(), type: 'view' as const } : undefined;
   if (v && items.some(x => x.id === v.id && (creating || x.id !== selected))) throw new Error(t('视图标识重复'));
   await persist(v ? creating ? [...items, v] : items.map(x => x.id === selected ? v : x) : items, v?.id);
  } catch(e) { if (e instanceof Error) setError(e.message); }
 };
 const add = (template?: ViewDefinition) => change(() => {
  const v = { ...(template || blank), published: false }; if (v.id) { let n = 1; const base = v.id; while (items.some(x => x.id === v.id)) v.id = `${base.slice(0,54)}_copy${n++}`; }
  edit(v, true);
 });
 useEffect(() => {
  const frame = window.requestAnimationFrame(() => listRef.current?.querySelector('.selected')?.scrollIntoView({ block: 'nearest' }));
  return () => window.cancelAnimationFrame(frame);
 }, [selected, creating]);
 const reload = () => change(() => { void databases.get(db.id).then(next => { setCurrent(next); setDefaultView(next.query_view_id || ''); setRemoteChanged(false); edit(next.views?.find(v => v.id === selected) || next.views?.[0]); }).catch(e => setError(e.message)); });
 const visible = items.filter(v => `${v.id} ${v.name} ${v.description}`.toLowerCase().includes(search.toLowerCase()));
 return <>
  <div className="action-workspace">
   <Card className="action-list-panel" title={<Space>{t('视图列表')}<Tag>{items.length}</Tag></Space>} extra={<Dropdown trigger={['click']} disabled={busy || creating || !templates.length} menu={{ items: templates.map(v => ({ key: v.id, label: v.name })), onClick: ({ key }) => add(templates.find(v => v.id === key)) }}><Button size="small">{t('从模板创建')} <DownOutlined /></Button></Dropdown>}>
    <div className="action-list-toolbar">
     <Input aria-label={t('搜索视图')} placeholder={t('搜索名称、描述或标识')} value={search} onChange={e => setSearch(e.target.value)} allowClear />
     <Tooltip title={t('新增视图')}><Button type="primary" aria-label={t('新增视图')} icon={<PlusOutlined />} disabled={busy || creating} onClick={() => { setSearch(''); add(); }} /></Tooltip>
    </div>
    <ul ref={listRef} className="action-list" aria-label={t('视图列表')}>
     {creating && <li><button type="button" className="action-list-item view-list-item selected" aria-current="true"><strong title={draft?.name}>{draft?.name || t('新视图')}</strong><span className="view-entry-id" title={draft?.id}>{draft?.id}</span><Tag className="view-entry-status" color="orange">{t('未保存')}</Tag></button></li>}
     {visible.map(v => <li key={v.id}><button type="button" className={`action-list-item view-list-item${!creating && selected === v.id ? ' selected' : ''}`} aria-current={!creating && selected === v.id ? 'true' : undefined} disabled={busy} onClick={() => { if (creating || v.id !== selected) change(() => { edit(v, false, false); selectResource(v.id); }); }}><strong title={v.name}>{v.name}</strong><span className="view-entry-id" title={v.id}>{v.id}</span><Tag className="view-entry-status" color={v.published ? 'green' : 'default'}>{t(v.published ? '已发布' : '草稿')}</Tag></button></li>)}
     {!visible.length && !creating && <li><Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('暂无视图，可从模板创建。')} /></li>}
    </ul>
   </Card>
   <Card className="action-editor-panel" title={<Space>{creating ? t('新增视图') : original?.name || t('选择视图')}{dirty && <Tag color="orange">{t('未保存')}</Tag>}</Space>} extra={<Space wrap>
    <Button size="small" disabled={busy} onClick={reload}>{t('刷新')}</Button>
    {draft && <>
     <Button size="small" disabled={busy} onClick={() => setMode(mode === 'preview' ? 'edit' : 'preview')}>{t(mode === 'preview' ? '编辑 HTML' : '预览')}</Button>
     <Button size="small" disabled={busy || dirty || creating} onClick={() => setBusinessOpen(true)}>{t('业务预览')}</Button>
     <Button size="small" danger disabled={busy} onClick={() => Modal.confirm({ title: t('删除当前视图？'), onOk: () => creating ? edit(items[0]) : persist(items.filter(v => v.id !== selected)) })}>{t('删除')}</Button>
    </>}
    <Button size="small" type="primary" disabled={!dirty || remoteChanged} loading={busy} onClick={() => void save()}>{t('保存视图')}</Button>
   </Space>}>
    {(error || remoteChanged) && <Alert type={error ? 'error' : 'warning'} showIcon title={error || t('配置已更新，请重新载入后继续。')} style={{ marginBottom: 16 }} />}
    {draft ? <Form form={form} layout="vertical" disabled={busy} onValuesChange={(_, values) => setDraft({ ...values, type: 'view' })}>
     <div className="action-fields-grid">
      <Form.Item name="id" label={t('标识')} rules={[{ required: true, pattern: /^[a-zA-Z][a-zA-Z0-9_-]{0,63}$/ }]}><Input disabled={!creating} /></Form.Item>
      <Form.Item name="name" label={t('名称')} rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
      <Form.Item name="description" label={t('描述')}><Input.TextArea rows={3} maxLength={2000} /></Form.Item>
      <div><Form.Item name="published" label={t('发布')} valuePropName="checked"><Switch /></Form.Item><Form.Item label={t('默认查询视图')}><Select aria-label={t('默认查询视图')} allowClear value={defaultView || undefined} disabled={busy} placeholder={t('使用原始查询结果')} options={items.filter(v => v.published).map(v => ({ value: v.id, label: v.name }))} onChange={v => setDefaultView(v || '')} /></Form.Item></div>
     </div>
     <Form.Item name="html" label="HTML" rules={[{ required: true }]} hidden={mode === 'preview'}><CodeEditor language="html" height="440px" readOnly={busy} /></Form.Item>
     {mode === 'preview' && <ViewFrame html={draft.html} />}
    </Form> : <Empty description={t('从左侧选择视图，或从模板创建。')} />}
   </Card>
  </div>
  <Modal title={t('业务预览')} open={businessOpen} onCancel={() => setBusinessOpen(false)} footer={null} width={1200} styles={{ body: { maxHeight: '78vh', overflowY: 'auto' } }}><BusinessActionWorkbench db={current} viewsOnly /></Modal>
 </>;
}
