import { lazy, Suspense, useEffect, useLayoutEffect, useState } from 'react';
import { Alert, Button, Form, Input, Layout, Menu, Modal, Select, Space, Spin, Tag, type MenuProps } from 'antd';
import { SettingOutlined, DatabaseOutlined, FilterOutlined, CodeOutlined, BookOutlined, ConsoleSqlOutlined, TableOutlined, AuditOutlined, ApiOutlined, FileSearchOutlined, PlayCircleOutlined, LayoutOutlined } from '@ant-design/icons';
import { SystemSettings } from './components/SystemSettings';
import { usePreferences } from './ui/preferences';
import { QueryConfiguration } from './pages/QueryConfiguration';
import { Actions } from './pages/Actions';
import { DataBrowser } from './pages/DataBrowser';
import { McpManagement } from './pages/McpManagement';
import { DatabaseSettings } from './pages/DatabaseSettings';
import { databases, selectDatabase, selectedDatabase, type Database } from './api';
import { SqlWorkbench } from './pages/SqlWorkbench';
import { AuditPage } from './pages/Audit';
import { useAuth } from './Auth';
import { ConnectionFields } from './components/ConnectionFields';
import { NavigationContext, navigate, readRoute, setRouteParams, startNavigation, type Page } from './navigation';

const ViewManagement = lazy(() => import('./pages/ViewManagement'));
const KnowledgeWorkbench = lazy(() => import('./pages/KnowledgeWorkbench'));
const ActionWorkbench = lazy(() => import('./pages/ActionWorkbench'));
const KnowledgePage = lazy(() => import('./pages/KnowledgeManagement').then(module => ({ default: module.KnowledgePage })));

const icons={views:<LayoutOutlined/>,database:<DatabaseOutlined/>,tables:<FilterOutlined/>,actions:<CodeOutlined/>,knowledge:<BookOutlined/>,'knowledge-workbench':<FileSearchOutlined/>,'action-workbench':<PlayCircleOutlined/>,sql:<ConsoleSqlOutlined/>,table:<TableOutlined/>,audit:<AuditOutlined/>,mcp:<ApiOutlined/>};
const titles:Record<Page,string>={views:'视图配置',database:'数据库配置',tables:'查询配置',actions:'动作配置',knowledge:'知识配置',sql:'查询工作台','knowledge-workbench':'知识工作台','action-workbench':'动作工作台',table:'表数据',audit:'操作日志',mcp:'MCP 配置'};
const navigationGroups: Page[][] = [
 ['database', 'tables', 'actions', 'knowledge', 'views', 'mcp'],
 ['sql', 'knowledge-workbench', 'action-workbench', 'table'],
 ['audit'],
];

export function App() {
 const {user,logout}=useAuth();
  const [route, setRoute] = useState(readRoute);
  const {dark,t}=usePreferences();
 const [settingsOpen,setSettingsOpen]=useState(false);
 const [items,setItems]=useState<Database[]>([]);
 const [dbID,setDbID]=useState(() => {
  const id = readRoute().params.get('database') || selectedDatabase;
  if(id!==selectedDatabase)selectDatabase(id); return id;
 });
 const [adding,setAdding]=useState(false);
 const [creating,setCreating]=useState(false);
 const [testing,setTesting]=useState(false);
 const [testResult,setTestResult]=useState<string>();
 const [error,setError]=useState<string>();
 const [form]=Form.useForm();
 const refresh=()=>databases.list().then(setItems).catch(e=>setError(e.message));
 useEffect(()=>{void refresh();},[]);
 const switchDB=(id:string)=>navigate(route.page, undefined, {database:id});
 useEffect(()=>{if(items.length && !items.some(d=>d.id===dbID)) navigate(route.page,undefined,{database:items[0].id},true);},[items,dbID]);
 const connectionValues=async()=>{
 const values=await form.validateFields();
 if(values.driver==='sqlite') return {name:values.name,driver:values.driver,path:values.path};
 const connection={...values.connection};if(values.driver==='mysql') connection.schema=connection.database;
 return {name:values.name,driver:values.driver,connection,password:values.password};
 };
 const testConnection=async()=>{
  try { const values=await connectionValues();setTesting(true);setError(undefined);setTestResult(undefined);const result=await databases.test(values);setTestResult(t("连接成功，发现 {0} 张表。", {"0":(result.table_count)})); }
  catch(e) {if(e instanceof Error)setError(e.message);}finally{setTesting(false);}
 };
 const add=async()=>{
  try { const values=await connectionValues();setCreating(true);const d=await databases.add(values);await refresh();navigate('tables',undefined,{database:d.id});setAdding(false);form.resetFields();setTestResult(undefined);setError(undefined); }
  catch(e) { if(e instanceof Error) setError(e.message); } finally {setCreating(false);}
 };

  useLayoutEffect(() => startNavigation(() => {
   const next = readRoute();
   const id = next.params.get('database') || selectedDatabase;
   if(id!==selectedDatabase)selectDatabase(id); setDbID(id); setRoute(next);
   setRouteParams({database:id}, true);
  }), []);


  const current=items.find(d=>d.id===dbID);
 const can=(p:string)=>user.system_admin||!!current?.permissions?.includes(p);
 const visiblePages=(Object.keys(titles) as Page[]).filter(p=>p==='audit'?(user.system_admin||items.some(d=>d.permissions?.includes('audit.read'))):p==='database'?(user.system_admin||can('config.read')||can('grant')):p==='tables'?can('config.read'):p==='actions'?can('config.read')&&can('action.write'):p==='views'?can('config.read')&&can('view.write'):p==='knowledge'?can('knowledge.write'):p==='knowledge-workbench'?can('knowledge.read'):p==='action-workbench'?can('action.write'):p==='table'?can('raw_query'):p==='sql'?can('query'):(can('mcp.write')||can('action.write')));
 const navigationItems: NonNullable<MenuProps['items']> = [];
 navigationGroups.forEach((group, index) => {
  const pages = group.filter(page => visiblePages.includes(page));
  if (!pages.length) return;
  if (navigationItems.length) navigationItems.push({ type: 'divider', key: `section-${index}` });
  navigationItems.push(...pages.map(key => ({ key, icon: icons[key], label: t(titles[key]) })));
 });
 const activePage=!current&&user.system_admin&&route.page!=='audit'?'database':visiblePages.includes(route.page)?route.page:visiblePages[0];
 useEffect(()=>{if(current&&activePage&&activePage!==route.page)navigate(activePage,undefined,{database:dbID},true);},[current,activePage,route.page,dbID]);
 const openTable = (table: string, filter?: { col: string; val: string }) => {
    navigate('table', table, filter ? { col: filter.col, val: filter.val } : {});
  };

  return (
    <NavigationContext.Provider value={route}>
      <Layout style={{ minHeight: '100vh', background: 'transparent' }}>
        <Layout.Header className="app-header">
          <Space><span className="app-title">DataScope</span><Select aria-label={t("切换数据库")} value={dbID} onChange={switchDB} style={{width:190}} options={items.map(d=>({value:d.id,label:d.name}))}/>{user.system_admin&&<Button onClick={()=>{setError(undefined);setTestResult(undefined);setAdding(true);}}>{t("＋ 数据库")}</Button>}</Space>
          <Space><Tag color="blue">{user.name||user.username}</Tag><Button onClick={logout}>{t('退出')}</Button><Button aria-label={t('系统设置')} title={t('系统设置')} icon={<SettingOutlined/>} onClick={()=>setSettingsOpen(true)}/></Space>
        </Layout.Header>
        <Layout className="workspace-layout" style={{background:'transparent'}}>
        <Layout.Sider className="workspace-sidebar" theme={dark?'dark':'light'} width={208} breakpoint="lg" collapsedWidth={56}>
         <div className="workspace-label">{current?.name||'数据库管理'}</div>
         <Menu mode="inline" theme={dark?'dark':'light'} selectedKeys={[activePage]} onClick={({key})=>navigate(key as Page)} items={navigationItems}/>
        </Layout.Sider>
        <Layout.Content className="app-content" key={activePage==='audit'?'audit':dbID}>
 {activePage==='database'&&<DatabaseSettings db={current} onSaved={()=>void refresh()}/>}
 {activePage==='audit'&&<AuditPage databases={items}/>}
 {activePage==='views'&&current&&<Suspense fallback={<Spin/>}><ViewManagement db={current} onSaved={()=>void refresh()}/></Suspense>}
 {activePage==='knowledge'&&current&&<Suspense fallback={<Spin/>}><KnowledgePage db={current}/></Suspense>}
 {activePage==='knowledge-workbench'&&current&&<Suspense fallback={<Spin/>}><KnowledgeWorkbench databaseID={dbID}/></Suspense>}
 {activePage==='action-workbench'&&current&&<Suspense fallback={<Spin/>}><ActionWorkbench db={current}/></Suspense>}
 {!activePage&&<Alert type="info" title={t("当前账号尚未获得数据库权限，请联系系统管理员。")}/>}
 {error && <Alert type="error" title={error} closable onClose={()=>setError(undefined)}/>}
          {activePage === 'table' && <DataBrowser table={route.selection} onOpenTable={openTable} />}
          {activePage === 'sql' && <SqlWorkbench views={current?.views} queryViewID={current?.query_view_id} permissions={current?.permissions||[]} businessUserID={current?.business_user_id||0} />}
          {activePage === 'actions' && current && <Actions db={current} onSaved={()=>void refresh()}/> }
          {activePage === 'tables' && <QueryConfiguration id={dbID} onSaved={()=>{void refresh();}} />}
          {activePage === 'mcp' && <McpManagement id={dbID} onSaved={()=>{void refresh();}} />}
        </Layout.Content>
        </Layout>
      </Layout>
      <Modal title={t("添加数据库")} centered styles={{body:{maxHeight:'calc(100dvh - 240px)',overflowY:'auto',paddingRight:8}}} open={adding} onCancel={()=>{setAdding(false);form.resetFields();}} onOk={add} confirmLoading={creating} okText={t("添加数据库")} footer={(_, {OkBtn,CancelBtn})=><Space><Button onClick={testConnection} loading={testing} disabled={creating}>{t("测试连接")}</Button><CancelBtn/><OkBtn/></Space>} destroyOnHidden>
       {error && <Alert type="error" title={error}/>}
       {testResult&&<Alert type="success" title={testResult}/>}
       <Form form={form} layout="vertical" initialValues={{driver:'sqlite'}} onValuesChange={()=>setTestResult(undefined)}><Form.Item name="name" label={t("显示名称")} rules={[{required:true}]}><Input placeholder={t("例如：业务数据库")}/></Form.Item><ConnectionFields form={form}/></Form>
       <p>{t("添加后自动侦查全部表、字段与注释，生成映射和结构快照。选择开放范围后，再开启 MCP。")}</p>
      </Modal>
      <SystemSettings open={settingsOpen} onClose={()=>setSettingsOpen(false)}/>
    </NavigationContext.Provider>
  );
}
