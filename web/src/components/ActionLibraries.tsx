import { useEffect, useState } from 'react';
import { Alert, Button, Empty, Form, Input, Modal, Select, Space, Typography, message } from 'antd';
import { databases, managementCall, type ActionLibrary, type Database } from '../api';
import { CodeEditor } from './CodeEditor';
import { useI18n } from '../ui/preferences';
import { setRouteParams, useNavigationBlocker, useRoute } from '../navigation';

const example: ActionLibrary = { name: '', title: '', description: '', script: 'export function helper(value) {\n  return value;\n}\n' };

export function ActionLibraries({ db, disabled, onSaved }: { db: Database; disabled: boolean; onSaved: (db: Database) => void }) {
 const t = useI18n();
 const route=useRoute();
 const requested=route.params.get('library');
 const open=requested!==null;
 const [base, setBase] = useState(db);
 const [selectedName, setSelectedName] = useState<string>();
 const [draft, setDraft] = useState<ActionLibrary>();
 const [creating, setCreating] = useState(false);
 const [busy, setBusy] = useState(false);
 const [error, setError] = useState('');
 const [form] = Form.useForm<ActionLibrary>();
 const items = base.action_libraries || [];
 const selected = items.find(item => item.name === selectedName);
 const dirty = !!draft && (creating || JSON.stringify(draft) !== JSON.stringify(selected));
 const canWrite = !!base.permissions?.includes('action.write');
 useNavigationBlocker(open&&dirty,t('放弃公共函数的未保存修改？'),open&&busy);
 const edit = (item?: ActionLibrary, isNew = false, replace=true) => {
  setCreating(isNew); setSelectedName(isNew ? undefined : item?.name); setDraft(item); setError('');
  form.resetFields(); if (item) form.setFieldsValue(item);
  if(!isNew)setRouteParams({library:item?.name||''},replace,true);
 };
 useEffect(()=>{
  if(requested===null){setDraft(undefined);setCreating(false);return;}
  if(requested===(selectedName||'')&&draft&&!creating)return;
  const next=base.id===db.id&&base.revision>=db.revision?base:db;
  setBase(next);edit(next.action_libraries?.find(item=>item.name===requested)||next.action_libraries?.[0]);
 },[requested,db.id]);
 const navigate = (work: () => void | Promise<void>) => {
  if (busy) return;
  if (dirty) Modal.confirm({ title: t('放弃公共函数的未保存修改？'), onOk: work }); else void work();
 };
 const persist = async (libraries: ActionLibrary[], name?: string) => {
  setBusy(true); setError('');
  try {
   const next = await managementCall<Database>('configure_actions', { database_id: base.id, database: { ...base, action_libraries: libraries, discovery: undefined } });
   setBase(next); edit(next.action_libraries?.find(item => item.name === name) || next.action_libraries?.[0]);
   onSaved(next); message.success(t('公共函数已保存'));
  } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
 };
 const save = async () => {
  try {
   const value = await form.validateFields();
   if (items.some(item => item.name === value.name && (creating || item.name !== selectedName))) throw new Error(t('公共函数名称重复'));
   await persist(creating ? [...items, value] : items.map(item => item.name === selectedName ? value : item), value.name);
  } catch (e) { if (e instanceof Error) setError(e.message); }
 };
 const reload = () => navigate(async () => {
  setBusy(true); setError('');
  try { const next = await databases.get(base.id); setBase(next); edit(next.action_libraries?.find(item => item.name === selectedName) || next.action_libraries?.[0]); onSaved(next); }
  catch (e) { setError((e as Error).message); } finally { setBusy(false); }
 });
 return <>
  <Button size="small" disabled={disabled} onClick={() => { setBase(db); edit(db.action_libraries?.[0],false,false); }}>{t('公共函数')}</Button>
  <Modal title={t('动作公共函数')} open={open} width={1040} onCancel={() => navigate(() => setRouteParams({library:undefined},false,true))} footer={<Space>
   <Button disabled={busy} onClick={reload}>{t('重新载入')}</Button>
   <Button disabled={busy} onClick={() => navigate(() => setRouteParams({library:undefined},false,true))}>{t('关闭')}</Button>
   <Button type="primary" loading={busy} disabled={!canWrite || !dirty} onClick={() => void save()}>{t('保存公共函数')}</Button>
  </Space>} styles={{ body: { maxHeight: '74vh', overflowY: 'auto' } }}>
   <Space orientation="vertical" style={{ width: '100%' }} size="middle">
    <Typography.Paragraph type="secondary">{t('使用 export 导出函数，在动作或其他库中通过 import { fn1, fn2 as alias } from "库名称" 引用。仅加载当前数据库被引用的模块。')}</Typography.Paragraph>
    <Space wrap>
     <Select aria-label={t('选择公共函数')} style={{ width: 300 }} value={selectedName} placeholder={t('选择公共函数')} disabled={busy} options={items.map(item => ({ value: item.name, label: `${item.title} · ${item.name}` }))} onChange={name => navigate(() => edit(items.find(item => item.name === name),false,false))} />
     <Button disabled={!canWrite || busy || creating} onClick={() => navigate(() => edit({ ...example }, true))}>{t('新增公共函数')}</Button>
     {draft && <Button danger disabled={!canWrite || busy} onClick={() => Modal.confirm({ title: t('删除公共函数？'), content: t('仍被动作或其他库引用的公共库不能删除。'), onOk: () => creating ? edit(items[0]) : persist(items.filter(item => item.name !== selectedName)) })}>{t('删除')}</Button>}
    </Space>
    {error && <Alert type="error" showIcon title={error} />}
    {draft ? <Form name="action-library" form={form} layout="vertical" disabled={!canWrite || busy} onValuesChange={(_, values) => setDraft(values)}>
     <div className="action-fields-grid">
      <Form.Item name="name" label={t('库名称')} rules={[{ required: true, pattern: /^[a-zA-Z][a-zA-Z0-9_-]{0,63}$/ }]}><Input disabled={!creating} /></Form.Item>
      <Form.Item name="title" label={t('标题')} rules={[{ required: true }]}><Input /></Form.Item>
      <Form.Item name="description" label={t('说明')}><Input /></Form.Item>
     </div>
     <Form.Item name="script" label="JavaScript · export" rules={[{ required: true }]}><CodeEditor height="420px" readOnly={!canWrite || busy} /></Form.Item>
    </Form> : <Empty description={t('选择或新增一个公共函数。')} />}
   </Space>
  </Modal>
 </>;
}
