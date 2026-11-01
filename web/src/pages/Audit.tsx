import { useEffect, useMemo, useRef, useState } from 'react';
import dayjs from 'dayjs';
import { Alert, Button, Card, Col, Collapse, DatePicker, Descriptions, Empty, Form, Input, Row, Select, Space, Table, Tag, Typography } from 'antd';
import { managementCall, type AuditEvent, type AuditFilters, type AuditResult, type Database } from '../api';
import { useAuth } from '../Auth';
import { useI18n } from '../ui/preferences';
import { selectResource, setRouteParams, useRoute, useRouteChoice } from '../navigation';

interface FilterForm {
 database?:string;actor_id?:number;kind?:string;source?:string;status?:string;tool?:string;request_id?:string;query?:string;
 range?:[{toISOString:()=>string},{toISOString:()=>string}];
}
const sourceLabels:Record<string,string>={web:'管理后台','management-mcp':'管理 MCP','database-mcp':'数据库 MCP',discovery:'结构检查'};
const statusLabels:Record<string,string>={success:'成功',failed:'失败',running:'进行中',unknown:'结果未知'};
const kindLabels:Record<string,string>={'http.request':'后台请求','mcp.request':'MCP 请求','sql.execute':'SQL 执行','sql.raw':'全量只读查询','sql.rewrite':'SQL 改写','sql.validation':'SQL 校验','auth.login':'登录','auth.logout':'退出登录','database.create':'添加数据库','database.update':'修改数据库','database.annotate':'编辑表字段说明','database.discovery_applied':'确认结构变化','database.discovery_detected':'发现结构变化','database.discovery_check':'检查结构','database.discovery_failed':'结构检查失败','access.denied':'权限拒绝'};

export function AuditPage({databases}:{databases:Database[]}){
 const t=useI18n();
 const {user}=useAuth();
 const route=useRoute();
 const allowed=databases.filter(db=>user.system_admin||db.permissions?.includes('audit.read'));
 const defaultScope=user.system_admin?'':allowed[0]?.id??'';
 const [form]=Form.useForm<FilterForm>();
 const [refresh,setRefresh]=useState(0);
 const routeQuery=route.params.toString();
 const query=useMemo(()=>{
  const params=new URLSearchParams(routeQuery);
  const filters:AuditFilters={};
  const scope=params.get('scope')??defaultScope;
  if(scope==='__platform__'&&user.system_admin)filters.platform_only=true;
  else if(scope)filters.database_id=allowed.some(db=>db.id===scope)?scope:defaultScope||undefined;
  else if(!user.system_admin&&defaultScope)filters.database_id=defaultScope;
  for(const key of ['kind','source','status','tool'] as const){const value=params.get(key);if(value)filters[key]=value;}
  if(params.get('requestId'))filters.request_id=params.get('requestId')!;
  if(params.get('q'))filters.query=params.get('q')!;
  const actor=Number(params.get('actorId'));
  if(params.has('actorId')&&Number.isSafeInteger(actor)&&actor>=0)filters.actor_id=actor;
  for(const [urlKey,key] of [['from','started_from'],['to','started_to']] as const){const value=params.get(urlKey);if(value&&dayjs(value).isValid())filters[key]=dayjs(value).toISOString();}
  const page=Number(params.get('page')),size=Number(params.get('size'));
  return {filters,page:Number.isSafeInteger(page)&&page>0?page:1,size:[20,50,100,200].includes(size)?size:50,refresh};
 },[routeQuery,defaultScope,databases,user.system_admin,refresh]);
 const setQuery=(update:(previous:typeof query)=>typeof query)=>{
  const next=update(query),filters=next.filters;
  if(next.refresh!==query.refresh)setRefresh(next.refresh);
  setRouteParams({scope:filters.platform_only?'__platform__':filters.database_id||undefined,status:filters.status,kind:filters.kind,source:filters.source,tool:filters.tool,requestId:filters.request_id,actorId:filters.actor_id===undefined?undefined:String(filters.actor_id),q:filters.query,from:filters.started_from,to:filters.started_to,page:next.page===1?undefined:String(next.page),size:next.size===50?undefined:String(next.size)},false,true);
  selectResource(undefined,true);
 };
 const [result,setResult]=useState<AuditResult>();
 const [error,setError]=useState<string>();
 const [busy,setBusy]=useState(false);
 const [advancedView,setAdvancedView]=useRouteChoice('advanced','closed',['closed','open']);
 const advanced=advancedView==='open';
 const appliedFilterKey=JSON.stringify(query.filters);
 useEffect(()=>{
  const filters=query.filters;
  form.resetFields();
  form.setFieldsValue({database:filters.platform_only?'__platform__':filters.database_id||defaultScope,status:filters.status,actor_id:filters.actor_id,kind:filters.kind,source:filters.source,tool:filters.tool,request_id:filters.request_id,query:filters.query,range:filters.started_from&&filters.started_to?[dayjs(filters.started_from),dayjs(filters.started_to)]:undefined});
 },[appliedFilterKey,defaultScope]);
 const sequence=useRef(0);
 useEffect(()=>{
  const current=++sequence.current;
  if(!user.system_admin&&!query.filters.database_id){setBusy(false);return}
  setBusy(true);setError(undefined);
  void managementCall<AuditResult>('get_audit_page',{...query.filters,limit:query.size,offset:(query.page-1)*query.size}).then(page=>{
   if(current!==sequence.current)return;
   if(page.total>0&&(query.page-1)*query.size>=page.total){setQuery(old=>({...old,page:Math.ceil(page.total/query.size)}));return}
   setResult(page);
  }).catch(e=>{if(current===sequence.current){setError((e as Error).message);setResult(undefined)}}).finally(()=>{if(current===sequence.current)setBusy(false)});
  return()=>{sequence.current++};
 },[query,user.system_admin]);
 const apply=(values:FilterForm)=>{
  const filters:AuditFilters={};
  if(values.database==='__platform__')filters.platform_only=true;else if(values.database)filters.database_id=values.database;
  for(const key of ['kind','source','status','tool','request_id','query'] as const){const value=values[key]?.trim();if(value)filters[key]=value}
  if(values.actor_id!==undefined)filters.actor_id=values.actor_id;
  if(values.range?.[0]&&values.range?.[1]){filters.started_from=values.range[0].toISOString();filters.started_to=values.range[1].toISOString()}
  setQuery(old=>({...old,filters,page:1,refresh:old.refresh+1}));
 };
 const advancedCount=([query.filters.actor_id,query.filters.kind,query.filters.source,query.filters.tool,query.filters.request_id] as unknown[]).filter(value=>value!==undefined&&value!=='').length;
 const actorName=(id:number)=>id===0?t('系统任务'):(result?.actor_names[String(id)]||`${t('用户')} #${id}`);
 const dbName=(id:string)=>id?(databases.find(db=>db.id===id)?.name??id):t('平台操作');
 const detail=(event:AuditEvent)=><Space orientation="vertical" style={{width:'100%'}} size="middle">
  <Descriptions size="small" column={{xs:1,md:3}} items={[
   {key:'request',label:t('请求 ID'),children:<Typography.Text copyable>{event.request_id}</Typography.Text>},
   {key:'database',label:t('数据库'),children:dbName(event.database_id)},
   {key:'actor',label:t('操作用户'),children:actorName(event.actor_id)},
   {key:'subject',label:t('业务身份'),children:event.subject_id||'—'},
   {key:'tool',label:t('工具'),children:event.tool||'—'},
   {key:'version',label:t('策略版本'),children:event.policy_version||'—'},
  ]}/>
  {([[t("原始 SQL"),event.original_sql],[t("改写 SQL"),event.rewritten_sql],[t("执行 SQL"),event.executed_sql],[t("绑定参数"),event.binds?JSON.stringify(event.binds,null,2):''],[t("错误信息"),event.error],[t("操作详情"),event.detail?JSON.stringify(event.detail,null,2):'']] as const).filter(([,value])=>value).map(([label,value])=><div key={label} style={{width:'100%'}}><Typography.Text strong>{t(label)}</Typography.Text><pre className="audit-detail">{value}</pre></div>)}
 </Space>;
 return <Space orientation="vertical" style={{width:'100%'}} size="middle">
  <Card title={t('操作日志')}>
   <Form form={form} layout="vertical" initialValues={{database:defaultScope}} onFinish={apply}>
    <Row gutter={[16,0]}>
     <Col xs={24} sm={12} lg={6}><Form.Item name="database" label={t('数据库')}><Select aria-label={t('筛选数据库')} showSearch optionFilterProp="label" options={[...(user.system_admin?[{value:'',label:t('全部数据库与平台日志')},{value:'__platform__',label:t('仅平台操作')}]:[]),...allowed.map(db=>({value:db.id,label:db.name}))]}/></Form.Item></Col>
     <Col xs={24} sm={12} lg={4}><Form.Item name="status" label={t('状态')}><Select aria-label={t('筛选状态')} placeholder={t('全部状态')} allowClear options={Object.entries(statusLabels).map(([value,label])=>({value,label:t(label)}))}/></Form.Item></Col>
     <Col xs={24} lg={8}><Form.Item name="range" label={t('时间范围')}><DatePicker.RangePicker showTime style={{width:'100%'}} format="YYYY-MM-DD HH:mm:ss" placeholder={[t('开始时间'),t('结束时间')]}/></Form.Item></Col>
     <Col xs={24} lg={6}><Form.Item name="query" label={t('关键词')}><Input aria-label={t('筛选关键词')} placeholder={t('搜索 SQL、错误或操作详情')} allowClear maxLength={300}/></Form.Item></Col>
    </Row>
    <Collapse ghost activeKey={advanced?['filters']:[]} onChange={keys=>setAdvancedView(keys.includes('filters')?'open':'closed')} items={[{
     key:'filters',
     label:<Space>{t('高级筛选')}{advancedCount>0&&<Tag color="blue">{t('{count} 项生效',{count:advancedCount})}</Tag>}</Space>,
     children:<Row gutter={[16,0]}>
      <Col xs={24} sm={12} lg={8}><Form.Item name="actor_id" label={t('操作用户')}><Select aria-label={t('筛选操作用户')} placeholder={t('全部用户')} allowClear showSearch optionFilterProp="label" options={(result?.actors??[]).map(id=>({value:id,label:actorName(id)}))}/></Form.Item></Col>
      <Col xs={24} sm={12} lg={8}><Form.Item name="kind" label={t('操作类型')}><Select aria-label={t('筛选操作类型')} placeholder={t('全部类型')} allowClear showSearch optionFilterProp="label" options={(result?.kinds??[]).map(kind=>({value:kind,label:kindLabels[kind]?`${t(kindLabels[kind])} · ${kind}`:kind}))}/></Form.Item></Col>
      <Col xs={24} sm={12} lg={8}><Form.Item name="source" label={t('来源')}><Select aria-label={t('筛选来源')} placeholder={t('全部来源')} allowClear showSearch optionFilterProp="label" options={(result?.sources??[]).map(source=>({value:source,label:t(sourceLabels[source]??source)}))}/></Form.Item></Col>
      <Col xs={24} sm={12}><Form.Item name="request_id" label={t('请求 ID')}><Input aria-label={t('筛选请求 ID')} placeholder={t('关联同一次操作')} allowClear maxLength={200}/></Form.Item></Col>
      <Col xs={24} sm={12}><Form.Item name="tool" label={t('工具 / 动作')}><Input aria-label={t('筛选工具')} placeholder={t('例如 query、save_policy')} allowClear maxLength={200}/></Form.Item></Col>
     </Row>,
    }]}/>
    <Space style={{marginTop:12}}><Button type="primary" htmlType="submit" loading={busy}>{t('查询')}</Button><Button disabled={busy} onClick={()=>{form.resetFields();apply({database:defaultScope})}}>{t('重置')}</Button></Space>
   </Form>
  </Card>
  <Card title={t('日志记录')} extra={<Button loading={busy} onClick={()=>setQuery(old=>({...old,refresh:old.refresh+1}))}>{t('刷新')}</Button>}>
   {error&&<Alert type="error" showIcon title={t('日志查询失败')} description={error} style={{marginBottom:12}}/>}
   <Table<AuditEvent> rowKey="id" size="small" loading={busy} dataSource={result?.items??[]} scroll={{x:1100}} locale={{emptyText:<Empty description={t('没有符合条件的日志')}/>}} pagination={{current:query.page,pageSize:query.size,total:result?.total??0,showSizeChanger:true,pageSizeOptions:[20,50,100,200],showTotal:total=>t('共 {count} 条',{count:total}),onChange:(page,size)=>setQuery(old=>({...old,page:size===old.size?page:1,size}))}} columns={[
    {title:t('时间'),dataIndex:'started_at',width:180,render:value=>new Date(value).toLocaleString()},
    {title:t('数据库'),dataIndex:'database_id',width:150,render:dbName},
    {title:t('操作用户'),dataIndex:'actor_id',width:130,render:actorName},
    {title:t('来源'),dataIndex:'source',width:130,render:value=>t(sourceLabels[value]??value)},
    {title:t('操作类型'),dataIndex:'kind',render:kind=><Space orientation="vertical" size={0}><Typography.Text>{t(kindLabels[kind]??kind)}</Typography.Text>{kindLabels[kind]&&<Typography.Text type="secondary" className="mono">{kind}</Typography.Text>}</Space>},
    {title:t('状态'),dataIndex:'status',width:100,render:status=><Tag color={status==='success'?'green':status==='failed'?'red':status==='running'?'blue':'orange'}>{t(statusLabels[status]??status)}</Tag>},
    {title:t('返回行数'),dataIndex:'rows',width:90},
   ]} expandable={{expandedRowRender:detail,expandedRowKeys:route.selection?[route.selection]:[],onExpand:(expanded,event)=>selectResource(expanded?event.id:undefined)}}/>
  </Card>
 </Space>;
}
