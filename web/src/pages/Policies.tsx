import { useI18n } from '../ui/preferences';
import { useEffect, useState } from 'react';
import { Alert, Card, Col, Descriptions, Row, Segmented, Space, Switch, Table, Tag, Typography } from 'antd';
import { api, type PoliciesResponse, type PolicyView, type Scheme } from '../api';
import { SchemeTag, SqlBlock } from '../components/shared';

const { Paragraph, Text } = Typography;

function RuleDetail({ policy }: { policy: PolicyView }) {
 const t=useI18n();
  if (policy.passthrough) {
    return <Text type="secondary">{t("passthrough: true，不改写，仅补全为 main.")}{policy.table}。</Text>;
  }
  return (
    <Space orientation="vertical" style={{ width: '100%' }}>
      <Text strong>{t("配置（YAML 原文）")}</Text>
      <SqlBlock sql={policy.sql ?? ''} format={false} />
      {policy.deny_unless && (
        <>
          <Text strong>{t("deny_unless（执行前求值，为假时拒绝：")}{policy.deny_message}）</Text>
          <SqlBlock sql={policy.deny_unless} format={false} />
        </>
      )}
      <Text strong>{t("编译后的 SQL（绑定变量：")}{policy.params.map((p) => `:${p}`).join('、')}）</Text>
      <SqlBlock sql={policy.compiled_sql ?? ''} />
    </Space>
  );
}

export function Policies() {
 const t=useI18n();
  const [data, setData] = useState<PoliciesResponse>();
  const [error, setError] = useState<string>();
  const [scheme, setScheme] = useState<Scheme | 'ALL'>('ALL');
  const [dualOnly, setDualOnly] = useState(false);

  useEffect(() => {
    api.policies().then(setData).catch((e: Error) => setError(e.message));
  }, []);

  if (error) return <Alert type="error" showIcon message={error} />;
  if (!data) return <Card loading />;

  const rows = data.policies.filter((p) => (scheme === 'ALL' || p.scheme === scheme) && (!dualOnly || p.divergent));

  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Alert
        type="info"
        showIcon
        message={t("改写原则：表结构确定，权限即确定")}
        description={
          <>
            {t("AI 生成的标准 SQL 进入 MCP server 后，FROM / JOIN 中的每张表都按配置替换为子查询，查询其余部分不变。规则来自")}{' '}
            <Text code>{data.path}</Text>{t("，用户上下文以绑定变量传入，不拼接进 SQL 文本。")}</>
        }
      />
      <Row gutter={[16, 16]}>
        {data.schemes.map((s) => (
          <Col xs={24} md={12} xl={6} key={s.scheme}>
            <Card size="small" hoverable onClick={() => setScheme(s.scheme)} className={scheme === s.scheme ? 'card-active' : undefined}>
              <Space>
                <SchemeTag scheme={s.scheme} />
                <Text strong>{data.policies.filter((p) => p.scheme === s.scheme).length} {t("张表")}</Text>
              </Space>
              <Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0 }}>
                {s.description}
              </Paragraph>
            </Card>
          </Col>
        ))}
      </Row>
      <Card size="small" title={t("绑定变量")}>
        <Descriptions
          size="small"
          column={1}
          bordered
          items={data.variables.map((v) => ({
            key: v.name,
            label: <Text code>{`\${${v.name}}`}</Text>,
            children: (
              <>
                {v.description}
                {v.name === 'asOf' && <Text type="secondary">{t("（当前")}{data.as_of}）</Text>}
              </>
            ),
          }))}
        />
      </Card>
      <Card
        size="small"
        title={t("逐表改写规则")}
        extra={
          <Space wrap>
            <Segmented
              value={scheme}
              onChange={(v) => setScheme(v as Scheme | 'ALL')}
              options={[{ value: 'ALL', label: t("全部") }, ...data.schemes.map((s) => ({ value: s.scheme, label: s.name }))]}
            />
            <Space>
              <Switch size="small" checked={dualOnly} onChange={setDualOnly} />
              <Text>{t("只看两种改写结果不同的表（")}{data.policies.filter((p) => p.divergent).length}）</Text>
            </Space>
          </Space>
        }
      >
        <Table<PolicyView>
          size="small"
          rowKey="table"
          dataSource={rows}
          pagination={false}
          expandable={{ expandedRowRender: (p) => <RuleDetail policy={p} /> }}
          columns={[
            {
              title: t("表"),
              dataIndex: 'table',
              width: 200,
              render: (_: string, p) => (
                <Space orientation="vertical" size={0}>
                  <span className="mono">{p.table}</span>
                  <Text type="secondary">{p.comment}</Text>
                </Space>
              ),
            },
            { title: t("方案"), dataIndex: 'scheme', width: 110, render: (s: Scheme) => <SchemeTag scheme={s} /> },
            {
              title: t("替换为"),
              dataIndex: 'sql',
              width: 360,
              render: (_: string, p) =>
                p.passthrough ? (
                  <Tag>{t("不改写")}</Tag>
                ) : (
                  <Space orientation="vertical" size={2}>
                    <Text code ellipsis={{ tooltip: p.sql }} style={{ maxWidth: 340 }}>
                      {p.sql}
                    </Text>
                    {p.deny_unless && <Tag color="red">deny_unless：{p.deny_message}</Tag>}
                  </Space>
                ),
            },
            { title: t("依据"), dataIndex: 'reason' },
            {
              title: t("另一种改写方式与判断"),
              dataIndex: 'alternative',
              render: (a: string | undefined, p) =>
                a ? (
                  <Space align="start">
                    {p.divergent ? <Tag color="orange">{t("结果不同")}</Tag> : <Tag>{t("结果等价")}</Tag>}
                    <span>{a}</span>
                  </Space>
                ) : (
                  <Text type="secondary">{t("唯一路径")}</Text>
                ),
            },
          ]}
        />
      </Card>
    </Space>
  );
}
