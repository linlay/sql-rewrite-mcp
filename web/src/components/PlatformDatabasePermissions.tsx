import { useEffect, useState } from 'react';
import { Alert, Button, Checkbox, Form, InputNumber, Modal, Space, Table, Typography, message } from 'antd';
import { databases, managementCall, type Database } from '../api';
import type { PlatformUser } from '../Auth';
import { useI18n } from '../ui/preferences';

const permissionLabels: Record<string, string> = {
 'config.read': '查看配置', 'config.write': '修改库配置与说明', 'policy.write': '修改访问策略',
 'action.write': '编写业务操作（可写库）', 'mcp.write': '配置 MCP', 'knowledge.write': '管理业务知识', 'view.write': '管理视图',
 query: '权限查询', raw_query: '全量只读查询', 'knowledge.read': '读取业务知识',
 impersonate: '模拟业务身份', 'audit.read': '查看审计', grant: '分配权限',
};
interface Grant { user_id: number; database_id: string; permissions: string[]; business_user_id: number }
interface Entry { database: Database; grant?: Grant; error?: string }

export function PlatformDatabasePermissions({ user, onClose }: { user?: PlatformUser; onClose: () => void }) {
 const t = useI18n();
 const [entries, setEntries] = useState<Entry[]>([]);
 const [loading, setLoading] = useState(false);
 const [saving, setSaving] = useState(false);
 const [error, setError] = useState('');
 const [editing, setEditing] = useState<Entry>();
 const [form] = Form.useForm();
 useEffect(() => {
  setEntries([]); setEditing(undefined); setError('');
  if (!user) return;
  let live = true;
  setLoading(true);
  void databases.list().then(async items => {
   const rows = await Promise.all(items.map(async database => {
    try {
     const grants = await managementCall<Grant[]>('list_database_grants', { database_id: database.id });
     return { database, grant: grants.find(grant => grant.user_id === user.id) };
    } catch (e) { return { database, error: (e as Error).message }; }
   }));
   if (live) setEntries(rows);
  }).catch(e => { if (live) setError(e.message); }).finally(() => { if (live) setLoading(false); });
  return () => { live = false; };
 }, [user?.id]);
 const edit = (entry: Entry) => {
  setError(''); setEditing(entry);
  form.setFieldsValue({ business_user_id: entry.grant?.business_user_id || 0, permissions: entry.grant?.permissions || [] });
 };
 const save = async () => {
  if (!user || !editing || user.system_admin) return;
  try {
   const values = await form.validateFields(); setSaving(true); setError('');
   const grant: Grant = { ...values, user_id: user.id, database_id: editing.database.id };
   await managementCall('grant_database_permissions', grant as unknown as Record<string, unknown>);
   setEntries(old => old.map(row => row.database.id === grant.database_id ? { ...row, grant } : row));
   setEditing(undefined); message.success(t('已保存'));
  } catch (e) { setError((e as Error).message); } finally { setSaving(false); }
 };
 return <>
  <Modal title={t('数据库权限 · {0}', { '0': user?.username || '' })} open={!!user} onCancel={onClose} width={1040} footer={<Button onClick={onClose}>{t('关闭')}</Button>} styles={{ body: { maxHeight: '72vh', overflowY: 'auto' } }}>
   <Space orientation="vertical" style={{ width: '100%' }} size="middle">
    {error && !editing && <Alert type="error" showIcon title={error} />}
    {user?.system_admin && <Alert type="info" showIcon title={t('系统管理员拥有全部数据库权限，无需单独授权。')} />}
    <Table rowKey={row => row.database.id} loading={loading} dataSource={entries} scroll={{ x: 760 }} columns={[
     { title: t('数据库'), render: (_, row: Entry) => row.database.name, width: 200 },
     { title: t('业务身份'), render: (_, row: Entry) => row.error ? '—' : row.grant?.business_user_id || '—', width: 120 },
     { title: t('已授予权限'), render: (_, row: Entry) => row.error ? <Typography.Text type="danger">{row.error}</Typography.Text> : user?.system_admin ? t('全部权限') : row.grant?.permissions.map(permission => t(permissionLabels[permission] || permission)).join('、') || t('未授权') },
     { title: t('操作'), width: 120, render: (_, row: Entry) => <Button disabled={!!row.error || user?.system_admin} onClick={() => edit(row)}>{t('设置权限')}</Button> },
    ]} />
   </Space>
  </Modal>
  <Modal title={t('设置权限 · {0}', { '0': editing?.database.name || '' })} open={!!editing} onCancel={() => setEditing(undefined)} onOk={() => void save()} confirmLoading={saving} width={640}>
   {error && <Alert type="error" showIcon title={error} style={{ marginBottom: 16 }} />}
   <Form form={form} layout="vertical">
    <Form.Item name="business_user_id" label={t('业务身份编号（0 表示未绑定）')}><InputNumber min={0} precision={0} /></Form.Item>
    <Form.Item name="permissions" label={t('授予能力')}><Checkbox.Group options={Object.entries(permissionLabels).map(([value, label]) => ({ value, label: t(label) }))} /></Form.Item>
   </Form>
  </Modal>
 </>;
}
