import { useEffect, useState } from 'react';
import { Alert, Button, Checkbox, Descriptions, Modal, Select, Space, Spin, Typography, message } from 'antd';
import { databases, managementCall, type Database } from '../api';
import { useAuth } from '../Auth';
import { useI18n } from '../ui/preferences';

type Scope = { tables: boolean; policies: boolean; actions: boolean; knowledge: boolean; views: boolean; authentication: boolean };
type Preview = {
 source_database_id: string; source_name: string; target_database_id: string; target_name: string;
 source_revision: number; target_revision: number; source_token: string; target_token: string;
 scope: Scope; confirmation: string; applied: boolean;
 items: { scope: keyof Scope | 'action_libraries'; source_count: number; target_count: number; names: string[] }[];
 issues: string[]; warnings: string[];
};
const defaults: Scope = { tables: true, policies: true, actions: true, knowledge: true, views: true, authentication: false };
const labels: Record<keyof Scope, string> = { tables: '表与字段配置', policies: 'SELECT 查询权限', actions: 'Actions', knowledge: '知识', views: '视图配置', authentication: '认证配置（默认不选）' };

export function DatabaseClone({ db, onSaved }: { db: Database; onSaved: () => void }) {
 const t = useI18n();
 const { user } = useAuth();
 const can = (p: string) => user.system_admin || !!db.permissions?.includes(p);
 const allowed: Scope = { tables: can('config.write') && can('policy.write'), policies: can('policy.write'), actions: can('action.write'), knowledge: can('knowledge.write'), views: can('view.write'), authentication: user.system_admin };
 const [open, setOpen] = useState(false);
 const [sources, setSources] = useState<Database[]>([]);
 const [source, setSource] = useState<string>();
 const [scope, setScope] = useState<Scope>(defaults);
 const [preview, setPreview] = useState<Preview>();
 const [acknowledged, setAcknowledged] = useState(false);
 const [loading, setLoading] = useState(false);
 const [saving, setSaving] = useState(false);
 const [error, setError] = useState<string>();
 useEffect(() => { setOpen(false); setSource(undefined); setPreview(undefined); }, [db.id]);
 useEffect(() => {
  if (!open) return;
  let active = true;
  setError(undefined);
  databases.list().then(items => { if (active) setSources(items.filter(d => d.id !== db.id && (user.system_admin || d.permissions?.includes('config.read')))); })
   .catch(e => { if (active) setError((e as Error).message); });
  return () => { active = false; };
 }, [open, db.id, user.system_admin]);
 useEffect(() => {
  let active = true;
  setPreview(undefined); setAcknowledged(false); setError(undefined);
  if (!open || !source || !Object.values(scope).some(Boolean)) { setLoading(false); return; }
  setLoading(true);
  managementCall<Preview>('preview_clone_configuration', { database_id: db.id, clone: { source_database_id: source, scope } })
   .then(value => { if (active) setPreview(value); })
   .catch(e => { if (active) setError((e as Error).message); })
   .finally(() => { if (active) setLoading(false); });
  return () => { active = false; };
 }, [open, source, scope, db.id, db.revision]);
 const current = preview && preview.source_database_id === source && preview.target_database_id === db.id && JSON.stringify(preview.scope) === JSON.stringify(scope);
 const submit = async () => {
  if (!current || !acknowledged || preview.issues.length) return;
  setSaving(true); setError(undefined);
  try {
   await managementCall('clone_configuration', { database_id: db.id, clone: {
    source_database_id: source, scope, source_revision: preview.source_revision, target_revision: preview.target_revision,
    source_token: preview.source_token, target_token: preview.target_token, confirmation: preview.confirmation, confirmed: true,
   } });
   message.success(t('配置已克隆')); setOpen(false); onSaved();
  } catch (e) { setError((e as Error).message); setAcknowledged(false); setPreview(undefined); }
  finally { setSaving(false); }
 };
 return <>
  <Button disabled={!Object.values(allowed).some(Boolean)} onClick={() => { setScope({ ...allowed, authentication: false }); setSource(undefined); setOpen(true); }}>{t('克隆配置')}</Button>
  <Modal title={t('克隆配置')} open={open} width={720} onCancel={() => { if (!saving) setOpen(false); }} closable={!saving} maskClosable={!saving}
   footer={<Space><Button disabled={saving} onClick={() => setOpen(false)}>{t('取消')}</Button>
    {!preview && source && !loading && <Button disabled={saving} onClick={() => setScope({ ...scope })}>{t('重新检查')}</Button>}
    <Button type="primary" danger loading={saving} disabled={loading || !current || !acknowledged || !!preview?.issues.length} onClick={() => void submit()}>{t('确认覆盖并克隆')}</Button></Space>}>
   <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
    <Typography.Text>{t('目标库：{name}', { name: db.name })}</Typography.Text>
    <Select aria-label={t('来源库')} placeholder={t('选择来源库')} value={source} style={{ width: '100%' }} disabled={saving}
     options={sources.map(d => ({ value: d.id, label: `${d.name} (${d.id})` }))} onChange={setSource} />
    <Space wrap>{(Object.keys(labels) as (keyof Scope)[]).map(key => <Checkbox key={key} checked={scope[key]} disabled={saving || !allowed[key]}
     onChange={e => setScope({ ...scope, [key]: e.target.checked })}>{t(labels[key])}</Checkbox>)}</Space>
    {scope.authentication && <Alert type="warning" showIcon title={t('认证配置将被替换')} description={t('复制认证方式、全局提供方引用或本库独立连接；环境变量密钥仅复制引用。保留目标账号、授权、身份绑定和密钥。旧业务 Token 和待确认操作将失效。')} />}
    <Typography.Text type="secondary">{t('同名表的开放字段、名称与注释和查询权限按所选项覆盖；目标独有表保留。Actions、知识按所选范围整组替换，包含删除目标独有项。')}</Typography.Text>
    {error && <Alert type="error" showIcon title={t('操作失败')} description={error} />}
    {loading && <Spin description={t('正在检查目标库真实结构与引用')} />}
    {current && <>
     <Typography.Text>{t('来源 revision {source} → 目标 revision {target}', { source: preview.source_revision, target: preview.target_revision })}</Typography.Text>
     <Descriptions bordered size="small" column={1} items={preview.items.map(item => ({ key: item.scope, label: t(item.scope === 'action_libraries' ? '公共函数' : labels[item.scope]), children: <>
      <div>{t('来源 {source} 项，覆盖目标 {target} 项', { source: item.source_count, target: item.target_count })}</div>
      <Typography.Paragraph style={{ marginBottom: 0, maxHeight: 90, overflowY: 'auto', overflowWrap: 'anywhere' }}>{item.names.join(', ') || t('来源为空，将清空所选目标配置')}</Typography.Paragraph>
     </> }))} />
     {!!preview.issues.length && <Alert type="error" showIcon title={t('结构或引用不一致，无法克隆')} description={<ul style={{ maxHeight: 180, overflowY: 'auto' }}>{preview.issues.map((issue, i) => <li key={i}>{issue}</li>)}</ul>} />}
     <Alert type="warning" showIcon title={t('请验证目标库业务行为')} description={t('仅检查可确定的结构与静态引用，不能证明任意 JavaScript / SQL 的业务兼容性；请在目标库验证 Action 行为。')} />
    </>}
    <Alert type="info" title={t('保留目标库设置')} description={t('不复制业务数据。保留连接、密码、密钥、ID、显示名称、MCP 路径及启用状态、写入开关、账号、人员授权、身份绑定、审计和 Action 内存回执。知识 ID 可保留，归属改为目标库。未选择认证配置时保留认证。')} />
    <Checkbox checked={acknowledged} disabled={!current || !!preview?.issues.length || saving} onChange={e => setAcknowledged(e.target.checked)}>{t('我已核对覆盖范围，确认替换目标库所选配置')}</Checkbox>
   </Space>
  </Modal>
 </>;
}
