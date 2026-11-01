import { useI18n } from '../ui/preferences';
import { useRef, useState } from 'react';
import { Alert, Button, Card, Empty, Input, Segmented, Select, Space, Tag, Typography } from 'antd';
import type { TextAreaRef } from 'antd/es/input/TextArea';
import { FormatPainterOutlined, PlayCircleOutlined, ThunderboltOutlined } from '@ant-design/icons';
import { ApiError, api, type QueryResult, type RewritePreview, type ViewDefinition } from '../api';
import { ViewFrame } from '../components/ViewFrame';
import { ResultTable, SchemeTag, SqlBlock, UserPicker } from '../components/shared';
import { SqlDiff } from '../components/SqlDiff';
import { formatSQL } from '../sqlFormat';
import examples from '../sqlExamples.json';

const { Text } = Typography;

interface Outcome<T> {
  data?: T;
  error?: string;
  denied?: boolean;
}

function failure<T>(e: unknown): Outcome<T> {
  const err = e as ApiError;
  return { error: err.message, denied: Boolean(err.body?.denied) };
}

function QueryOutput({ outcome, loading }: { outcome?: Outcome<QueryResult>; loading: boolean }) {
 const t=useI18n();
  if (!outcome) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={loading ? t("正在执行…") : t("执行后在这里查看结果")} />;
  if (outcome.error) return <Alert type={outcome.denied ? 'warning' : 'error'} showIcon message={outcome.denied ? t("权限拒绝") : t("执行失败")} description={outcome.error} />;
  const result = outcome.data!;
  return <ResultTable columns={result.columns} rows={result.rows} loading={loading} />;
}

function ResultSummary({ outcome }: { outcome?: Outcome<QueryResult> }) {
 const t=useI18n();
  return outcome?.data ? <div className="sql-result-summary">
    <Tag color="blue">{outcome.data.row_count} {t("行")}</Tag>
    {outcome.data.truncated && <Text type="warning">{t("结果已截断")}</Text>}
  </div> : null;
}

export function SqlWorkbench({permissions,businessUserID,views = [],queryViewID = ''}:{permissions:string[];businessUserID:number;views?:ViewDefinition[];queryViewID?:string}) {
 const t=useI18n();
 const canRaw=permissions.includes("raw_query");const canImpersonate=permissions.includes("impersonate");
  const [parameters, setParameters] = useState('{}');
 const [resultView,setResultView] = useState(queryViewID);
 const [executedInput,setExecutedInput] = useState<Record<string,unknown>>({});
 const selectedView=views.find(v=>v.id===resultView&&v.published);
  const [sql, setSql] = useState(examples[1].sql);
  const editor = useRef<TextAreaRef>(null);
  // Execution and formatting always read the live editor; never fall back to
  // a cached state value or a previous rewrite when the editor is unavailable.
  const readSql = () => editor.current?.resizableTextArea?.textArea?.value ?? '';
  const [userId, setUserId] = useState(businessUserID);
  const [maxRows, setMaxRows] = useState(200);
  const [action, setAction] = useState<'raw' | 'preview' | 'execute'>();
  const busy = useRef(false);
  const [raw, setRaw] = useState<Outcome<QueryResult>>();
  const [rewritten, setRewritten] = useState<Outcome<RewritePreview>>();
  const [result, setResult] = useState<Outcome<QueryResult>>();
  const [view, setView] = useState<'diff' | 'sql'>('diff');
  const [rewriteSource, setRewriteSource] = useState('');

  const changeSql = (value: string) => {
    if (busy.current) return;
    setSql(value);
    setRaw(undefined);
    setRewritten(undefined);
    setResult(undefined);
  };
  const run = async (next: 'raw' | 'preview' | 'execute') => {
    const currentSql = readSql();
    if (busy.current || !currentSql.trim()) return;
    busy.current = true;
    setAction(next);
    if (next === 'raw') setRaw(undefined);
    else { setRewritten(undefined); setResult(undefined); setRewriteSource(currentSql); }
    try {
      const values = JSON.parse(parameters);
      if (!values || typeof values !== 'object' || Array.isArray(values)) throw new Error(t('绑定参数必须是 JSON 对象'));
      if (next === 'raw') setRaw({ data: await api.rawQuery(currentSql, maxRows, values) });
      else if (next === 'preview') setRewritten({ data: await api.previewRewrite(currentSql, userId, values) });
      else {
        // The server regenerates and validates the statement and binds for this identity.
        const data = await api.rewrite(currentSql, userId, maxRows, values);
        setRewritten({ data });
        setResult({ data });
        setExecutedInput({sql:currentSql,parameters:values,max_rows:maxRows});
      }
    } catch (e) {
      if (next === 'raw') setRaw(failure(e));
      else {
        const err = e as ApiError;
        const preview = err.body?.rewrite as RewritePreview | undefined;
        setRewritten(preview ? { data: preview } : failure(e));
        if (next === 'execute') setResult(failure(e));
      }
    } finally { busy.current = false; setAction(undefined); }
  };
  const disabled = Boolean(action);
  const selectedExample = examples.findIndex((example) => example.sql === sql);

  return <div className="sql-workbench-scroll">
      <div className="sql-workbench-grid">
        <section className="sql-pane" aria-label={t("原始区域")}>
          <Card size="small" className="sql-source-card" title={<div className="sql-source-title"><span>{t("原始 SQL")}</span>
            <Select size="small" aria-label={t("SQL 示例")} placeholder={t("选择 SQL 示例")} className="sql-example-picker" disabled={disabled} value={selectedExample < 0 ? undefined : selectedExample} options={examples.map((example, index) => ({ value: index, label:t(example.label) }))} onChange={(index) => changeSql(examples[index].sql)} />
          </div>} extra={<Space size={6}>
            <Button size="small" icon={<FormatPainterOutlined />} disabled={disabled || !sql.trim()} onClick={() => changeSql(formatSQL(readSql()))}>{t("一键美化")}</Button>
            <Button size="small" type="primary" icon={<PlayCircleOutlined />} disabled={disabled || !sql.trim() || !canRaw} loading={action === 'raw'} onClick={() => void run('raw')}>{t("原始 SQL 执行")}</Button>
          </Space>}>
            <div className="sql-context sql-original-context"><Tag>{canRaw?t("全量只读查询"):t("未授权全量查询")}</Tag><Text type="secondary">{t("⌘ / Ctrl + Enter 执行原始 SQL")}</Text>
              <Select size="small" aria-label={t("最大返回行数")} className="sql-row-limit" disabled={disabled} value={maxRows} onChange={(value) => { setMaxRows(value); setRaw(undefined); setResult(undefined); }} options={[50, 200, 1000].map((n) => ({ value: n, label: t("最多 {0} 行", {"0":(n)}) }))} />
            </div>
            <Input.TextArea ref={editor} aria-label={t("原始 SQL")} className="mono sql-editor" value={sql} disabled={disabled} onChange={(e) => changeSql(e.target.value)} style={{ height: 320, resize: 'vertical' }} onKeyDown={(e) => {
              if (canRaw && (e.metaKey || e.ctrlKey) && e.key === 'Enter') { e.preventDefault(); void run('raw'); }
            }} />
            <label><Text>{t('绑定参数 JSON（可选）')}</Text><Input.TextArea aria-label={t('绑定参数 JSON（可选）')} className="mono" rows={3} disabled={disabled} value={parameters} onChange={event => { setParameters(event.target.value); setRaw(undefined); setRewritten(undefined); setResult(undefined); }} /></label>
            <Text type="secondary">{t('SQL 使用 :arg_name，参数填写 {"arg_name":"值"}。仅接受文本、数字、布尔值或 null；身份和授权时间由服务端提供。')}</Text>
          </Card>
          <Card size="small" title={t("原始结果")} extra={<ResultSummary outcome={raw} />}><QueryOutput outcome={raw} loading={action === 'raw'} /></Card>
        </section>
        <section className="sql-pane" aria-label={t("改写区域")}>
          <Card size="small" className="sql-source-card" title={t("改写后的 SQL")} extra={<Space size={6}>
            <Button size="small" icon={<ThunderboltOutlined />} disabled={disabled || !sql.trim()} loading={action === 'preview'} onClick={() => void run('preview')}>{t("一键改写")}</Button>
            <Button size="small" type="primary" icon={<PlayCircleOutlined />} disabled={disabled || !sql.trim()} loading={action === 'execute'} onClick={() => void run('execute')}>{t("改写后 SQL 执行")}</Button>
          </Space>}>
            <div className="sql-context">
              <Text type="secondary">{t("权限身份")}</Text>
              <div className="sql-user-picker">
                <UserPicker size="small" disabled={disabled||!canImpersonate} value={userId} onChange={(id) => { if (busy.current) return; setUserId(id); setRewritten(undefined); setResult(undefined); }} style={{ minWidth: 0, width: '100%' }} />
              </div>
              <Segmented size="small" value={view} onChange={setView} options={[{ value: 'diff', label: 'Git diff' }, { value: 'sql', label: t("完整 SQL") }]} />
            </div>
            <div className="sql-preview">
              {rewritten?.error ? <Alert type={rewritten.denied ? 'warning' : 'error'} showIcon message={rewritten.denied ? t("改写拒绝") : t("改写失败")} description={rewritten.error} /> : rewritten?.data ? <>
                {view === 'diff' ? <SqlDiff original={rewriteSource} rewritten={rewritten.data.rewritten_sql} /> : <SqlBlock sql={rewritten.data.rewritten_sql} maxHeight={320} format={false} />}
              </> : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={action === 'preview' || action === 'execute' ? t("正在按员工权限改写…") : t("点击“一键改写”查看最终 SQL 与差异")} />}
            </div>
            {rewritten?.data && <Space orientation="vertical" size="small" style={{ width: '100%', marginTop: 12 }}>
              <Space wrap><Text type="secondary">{t("权限策略")}</Text>{rewritten.data.tables.map((t, i) => <Tag key={i}><span className="mono">{t.table}{t.alias !== t.table ? ` AS ${t.alias}` : ''}</span> <SchemeTag scheme={t.scheme} /></Tag>)}</Space>
              <Space wrap><Text type="secondary">{t("绑定变量")}</Text>{Object.entries(rewritten.data.binds).map(([key, value]) => <Tag key={key} className="mono">:{key} = {String(value)}</Tag>)}{Object.keys(rewritten.data.binds).length === 0 && <Text type="secondary">{t("无")}</Text>}</Space>
            </Space>}
          </Card>
          <Card size="small" title={t("改写后结果 · 员工 #{0}", {"0":(userId)})} extra={<Space><Select size="small" aria-label={t("结果视图")} style={{minWidth:140}} value={selectedView?.id||''} onChange={setResultView} options={[{value:'',label:t("结果表格")},...views.filter(v=>v.published).map(v=>({value:v.id,label:v.name}))]} /><ResultSummary outcome={result} /></Space>}>
            {!result && rewritten?.data ? <Alert type="info" showIcon message={t("SQL 已改写，尚未执行")} description={t("点击“改写后 SQL 执行”查看该员工权限范围内的结果。")} /> : selectedView && result?.data ? <ViewFrame html={selectedView.html} data={{input:executedInput,result:result.data}} onCall={async(name,args)=>{if(name !== 'query')throw new Error(t('动作请通过业务预览中的本人认证执行。'));return api.rewrite(String(args.sql||''),userId,Number(args.max_rows||200),(args.parameters||{}) as Record<string,unknown>);}} /> : <QueryOutput outcome={result} loading={action === 'execute'} />}
          </Card>
        </section>
      </div>
    </div>;
}
