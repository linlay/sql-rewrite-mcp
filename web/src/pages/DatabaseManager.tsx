import { useState } from 'react';
import { Button, Form, Input, InputNumber, Modal, Select, Space, Tooltip, Typography } from 'antd';
import type { Database, PolicyView } from '../api';
import { useI18n } from '../ui/preferences';
import { CodeEditor } from '../components/CodeEditor';
import { HelpTip } from '../components/HelpTip';

// Row policies share the draft and save operation with table/column access.
export function TablePolicyEditor({ db, policy, disabled, onChange, onOptionsOpen }: {
 db: Database; policy: PolicyView; disabled: boolean;
 onChange: (patch: Partial<PolicyView>) => void; onOptionsOpen: (open: boolean) => void;
}) {
 const t = useI18n();
 const [optionsOpen, setOptionsOpen] = useState(false);
 const [form] = Form.useForm();
 const close = () => { setOptionsOpen(false); onOptionsOpen(false); };
 const open = () => { form.setFieldsValue(policy); setOptionsOpen(true); onOptionsOpen(true); };
 const apply = async () => {
  const values = await form.validateFields();
  onChange({ scheme: values.scheme, reason: values.reason || '', deny_unless: values.deny_unless || '', deny_message: values.deny_message || '' });
  close();
 };
 return <section className="table-policy-editor">
  <div className="policy-heading"><Typography.Text strong>{t('行权限与查询限制')}</Typography.Text><Button size="small" onClick={open}>{t('其他设置')}</Button></div>
  <div className="policy-controls">
   <label className="policy-control"><span>{t('行权限')}</span><Select aria-label={t('行权限方式')} disabled={disabled} value={policy.passthrough ? 'pass' : 'rewrite'} options={[{ value: 'pass', label: t('无需行权限过滤') }, { value: 'rewrite', label: t('使用 SQL 过滤') }]} onChange={value => onChange({ passthrough: value === 'pass', sql: value === 'pass' ? '' : policy.sql || `SELECT * FROM main."${policy.table.replace(/"/g, '""')}"` })} /></label>
   <label className="policy-control"><span>{t('最多返回行数')}</span><InputNumber aria-label={t('最多返回行数')} disabled={disabled} min={1} max={1000} value={policy.max_rows || 1000} onChange={value => onChange({ max_rows: value ?? 200 })} /></label>
  </div>
  <div className={`policy-sql-grid${!policy.passthrough && policy.deny_unless ? ' has-guard' : ''}`}>
   {!policy.passthrough && <div className="policy-sql-field">
    <Space size={4}><Typography.Text>{t('行权限 SQL')}</Typography.Text><HelpTip label={t('行权限 SQL')}>{t('用 main.表名引用原表，${userId} 表示当前员工，${asOf} 表示授权时间。每张表保存完整的权限 SQL。')}</HelpTip></Space>
    <CodeEditor language="sql" height="auto" minHeight="160px" wrap value={policy.sql} readOnly={disabled} onChange={sql => onChange({ sql })} />
   </div>}
   {!!policy.deny_unless && <div className="policy-sql-field">
    <Space size={4}><Typography.Text>{t('前置权限检查')}</Typography.Text><Tooltip title={policy.deny_message || t('检查不通过时拒绝本次查询。')}><Typography.Text type="secondary">{t('已启用')}</Typography.Text></Tooltip></Space>
    <CodeEditor language="sql" height="auto" minHeight="160px" wrap value={policy.deny_unless} readOnly={disabled} onChange={deny_unless => onChange({ deny_unless })} />
   </div>}
  </div>
  <Modal title={t('其他权限设置')} open={optionsOpen} onCancel={close} onOk={() => void apply()} okButtonProps={{ disabled }} width={860} styles={{ body: { maxHeight: '72vh', overflowY: 'auto' } }}>
   <Form form={form} layout="vertical" disabled={disabled}>
    <div className="policy-options-grid">
     <Form.Item name="scheme" label={t('方案标签')} tooltip={t('仅用于分类展示，不决定实际行权限。')}><Select options={db.config.schemes.map(scheme => ({ value: scheme.scheme, label: scheme.name }))} /></Form.Item>
     <Form.Item name="reason" label={t('权限规则说明')}><Input /></Form.Item>
    </div>
    <Form.Item name="deny_unless" label={t('前置权限检查（可选）')} tooltip={t('填写返回布尔结果的 SQL 表达式；不通过时整次查询被拒绝。')}><CodeEditor language="sql" height="150px" wrap readOnly={disabled} /></Form.Item>
    <Form.Item name="deny_message" label={t('拒绝访问提示')}><Input /></Form.Item>
   </Form>
  </Modal>
 </section>;
}
