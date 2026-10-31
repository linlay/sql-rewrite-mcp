import { useRef } from 'react';
import { Input, InputNumber, Select, Space, Typography } from 'antd';
import type { PolicyView } from '../api';
import { useI18n } from '../ui/preferences';
import { CodeEditor } from '../components/CodeEditor';
import { HelpTip } from '../components/HelpTip';

export function TablePolicyEditor({ policy, disabled, onChange }: {
 policy: PolicyView; disabled: boolean;
 onChange: (patch: Partial<PolicyView>) => void;
}) {
 const t = useI18n();
 const hasGuard = useRef(!!policy.deny_unless);
 hasGuard.current ||= !!policy.deny_unless;
 return <section className="table-policy-editor">
  <Typography.Text strong>{t('行权限与查询限制')}</Typography.Text>
  <div className="policy-controls">
   <label className="policy-control"><span>{t('行权限')}</span><Select aria-label={t('行权限方式')} disabled={disabled} value={policy.passthrough ? 'pass' : 'rewrite'} options={[{ value: 'pass', label: t('无需行权限过滤') }, { value: 'rewrite', label: t('使用 SQL 过滤') }]} onChange={value => onChange({ passthrough: value === 'pass', sql: value === 'pass' ? '' : policy.sql || `SELECT * FROM main."${policy.table.replace(/"/g, '""')}"` })} /></label>
   <label className="policy-control"><span>{t('最多返回行数')}</span><InputNumber aria-label={t('最多返回行数')} disabled={disabled} min={1} max={1000} value={policy.max_rows || 1000} onChange={value => onChange({ max_rows: value ?? 200 })} /></label>
  </div>
  {!policy.passthrough && <div className="policy-sql-field">
   <Space size={4}><Typography.Text>{t('行权限 SQL')}</Typography.Text><HelpTip label={t('行权限 SQL')}>{t('用 main.表名引用原表，${userId} 表示当前员工，${asOf} 表示授权时间。每张表保存完整的权限 SQL。')}</HelpTip></Space>
   <CodeEditor language="sql" height="auto" minHeight="160px" wrap value={policy.sql} readOnly={disabled} onChange={sql => onChange({ sql })} />
  </div>}
  {hasGuard.current && <div className="policy-sql-field">
   <Typography.Text>{t('查询准入条件')}</Typography.Text>
   <Typography.Paragraph type="secondary">{t('已有条件不满足时拒绝整次查询；清空条件会解除这项限制。')}</Typography.Paragraph>
   <CodeEditor language="sql" height="auto" minHeight="100px" wrap value={policy.deny_unless} readOnly={disabled} onChange={deny_unless => onChange({ deny_unless })} />
   <Input aria-label={t('条件不满足时的提示')} placeholder={t('条件不满足时的提示')} disabled={disabled} value={policy.deny_message} onChange={event => onChange({ deny_message: event.target.value })} />
  </div>}
 </section>;
}
