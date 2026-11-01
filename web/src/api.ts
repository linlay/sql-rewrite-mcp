// Typed client for the Go admin API (proxied at /api by the dev server).

export type Scheme = string;
export type Row = Record<string, unknown>;

export interface TableInfo {
  display_name?: string;
  name: string;
  comment: string;
  row_count: number;
  row_count_estimated?: boolean;
  scheme: Scheme;
}

export interface Column {
  display_name?: string;
  name: string;
  type: string;
  not_null: boolean;
  primary_key: boolean;
  default?: string;
  references?: string;
  comment: string;
}

export interface TableSchema {
  display_name?: string;
  name: string;
  comment: string;
  columns: Column[];
  indexes: string[];
  ddl: string;
}

export interface BrowseResult {
  table: string;
  columns: string[];
  rows: Row[];
  total: number;
  limit: number;
  offset: number;
}

export interface QueryResult {
  columns: string[];
  rows: Row[];
  row_count: number;
  truncated: boolean;
}

export interface TableRef {
  table: string;
  alias: string;
  scheme: Scheme;
}

export interface Guard {
  table: string;
  sql: string;
  params: string[];
  message: string;
}

export interface RewritePreview {
  rewritten_sql: string;
  tables: TableRef[];
  params: string[];
  guards: Guard[];
  binds: Record<string, unknown>;
}

export interface RewriteResult extends QueryResult, RewritePreview {}

export interface UserSummary {
  id: number;
  emp_no: string;
  name: string;
  role_code: string;
 department_code: string | null;
  branch_code: string | null;
  status: string;
  roles: string;
}

export interface SchemeInfo {
  scheme: Scheme;
  name: string;
  description: string;
}

export interface PolicyView {
  table: string;
  enabled?: boolean;
  columns: string[] | null;
  max_rows: number;
  scheme: Scheme;
  reason: string;
  alternative?: string;
  divergent: boolean;
  passthrough: boolean;
  sql?: string;
  deny_unless?: string;
  deny_message?: string;
  compiled_sql?: string;
  compiled_deny_unless?: string;
  params: string[];
  comment: string;
}

export interface VariableInfo {
  name: string;
  description: string;
}

export interface PoliciesResponse {
  path: string;
  schemes: SchemeInfo[];
  variables: VariableInfo[];
  policies: PolicyView[];
  as_of: string;
}

export interface McpTool {
  name: string;
  title?: string;
  description?: string;
  inputSchema?: { properties?: Record<string, { type?: string; description?: string }>; required?: string[] };
  outputSchema?: unknown;
  annotations?: { readOnlyHint?: boolean };
}

export interface ServerInfo {
  endpoint: string;
  protocol_version: string;
  latest_protocol_version: string;
  server: { name: string; title?: string; version: string };
  instructions: string;
  capabilities: unknown;
  tools: McpTool[];
  user_header: string;
 auth_mode: string;
  elapsed_ms: number;
}

export interface CallToolResult {
  content?: { type: string; text?: string }[];
  structuredContent?: unknown;
  isError?: boolean;
}

export interface CallResult {
  tool: string;
  user_id: number;
  arguments: Record<string, unknown>;
  result: CallToolResult;
  elapsed_ms: number;
}

export type CheckStatus = 'pass' | 'fail' | 'warn' | 'skip';

export interface Check {
  group: string;
  name: string;
  status: CheckStatus;
  detail?: string;
  expected?: unknown;
  actual?: unknown;
}

export interface Report {
  endpoint: string;
  protocol_version: string;
  debug_tools: string[];
  passed: number;
  failed: number;
  warned: number;
  skipped: number;
  checks: Check[];
  elapsed_ms: number;
}

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly body: Record<string, unknown>,
  ) {
    super(message);
  }
}

export let selectedDatabase = localStorage.getItem('datascope-database') || localStorage.getItem('sql-rewrite-database') || 'default';
export function selectDatabase(id: string) {
 selectedDatabase = id; localStorage.setItem('datascope-database', id); usersPromise = undefined;
}
export interface DatabaseConnection {
 host:string; port:number; database?:string; service_name?:string; schema:string; username:string; password_env?:string; credential_id?:string; tls_mode?:string;
}
export interface NewDatabase {id?:string; password?:string; name:string; driver:string; path?:string; connection?:DatabaseConnection}
export interface Annotation { display_name: string; comment: string | null }
export interface TableAnnotation extends Annotation { columns: Record<string, Annotation> }
export interface SchemaChange { table:string; column?:string; kind:'added'|'removed'|'modified'; property:string; before:string; after:string; manual?:string; conflict:boolean }
export interface SchemaReport { version:number; at:string; changes:SchemaChange[] }
export interface AnnotationResolution {table:string;column?:string;follow_source:boolean}
export interface PendingDiscovery {id:string;base_version:number;detected_at:string;tables:TableSchema[];changes:SchemaChange[]}
export interface Discovery { pending?:PendingDiscovery;version:number; checked_at:string; error?:string; tables:TableSchema[]; annotations:Record<string,TableAnnotation>; reports:SchemaReport[] }
export interface ActionParameter { type: "string"|"integer"|"number"|"boolean"; description: string; optional?: boolean; enum?: unknown[]; min?: number; max?: number; max_length?: number; pattern?: string }
export interface ActionDefinition { name: string; title: string; description: string; confirm: boolean; parameters: Record<string, ActionParameter>; script: string }
export interface ActionLibrary { name: string; title: string; description: string; script: string }
export interface ActionSchema { type: string; additionalProperties: boolean; required: string[]; properties: Record<string, {type:string;description?:string;enum?:unknown[];minimum?:number;maximum?:number;maxLength?:number;pattern?:string}> }
export interface ActionDescription { name:string;title:string;description:string;confirm:boolean;parameters?:ActionSchema }
export interface ActionPreview { status:string;message?:string;result?:unknown;preview?:{result:unknown;writes:{sql:string;rows:number;binds:unknown[]}[]};sql?:unknown[] }
export interface AuthProvider {id:string;name:string;type:string;enabled:boolean;issuer?:string;authorize_url?:string;token_url?:string;userinfo_url?:string;account_url?:string;client_id?:string;secret_env?:string;subject_claim?:string;username_claim?:string;business_id_claim?:string}
export interface AuthPolicy {id:string;name:string;mode:'ACCOUNT'|'OAUTH'|'NONE';provider_id?:string;permissions?:string[];business_user_id?:number}
export interface AuthConfig {revision:number;default_policy_id:string;providers:AuthProvider[];policies:AuthPolicy[];bindings:{target_type:'API'|'DATABASE';target_id:string;policy_id:string}[]}
export interface Database {
 management_path?:string; audit_path?:string; access?:{policy:AuthPolicy;provider:AuthProvider;revision:number;source?:'DATABASE'};
 actions?: ActionDefinition[];
 action_libraries?: ActionLibrary[];
 write_enabled?: boolean;
 write_connection?: DatabaseConnection;
 discovery?:Discovery;
 connection?:DatabaseConnection;
 authentication?: {type:"local";label?:string;employee_directory?:boolean;access?:AuthPolicy;source?:'GLOBAL'|'DATABASE';provider?:AuthProvider};
 permissions?: string[]; business_user_id?: number;
 id: string; name: string; driver: string; path: string; mcp_enabled: boolean; knowledge_enabled?:boolean; mcp_path?: string; mcp_endpoint?: string; tools: string[]; revision: number; etag?: string;
 config: { version: number; schemes: SchemeInfo[]; tables: PolicyView[] };
}
export const databases = {
 list: () => request<Database[]>('/api/databases'),
 applyDiscovery: (id:string,candidate:string,revision:number,resolutions:AnnotationResolution[]) => managementCall<Database>('apply_discovery',{database_id:id,id:candidate,revision,resolutions}),
 discover: (id:string) => managementCall<Database>('discover_database',{database_id:id}),
 annotate: (id:string,table:string,column:string,revision:number,annotation:Annotation) => managementCall<Database>('save_annotation',{database_id:id,table,column,revision,annotation}),
 get: (id: string) => request<Database>(`/api/databases/${encodeURIComponent(id)}`),
 test: (body: NewDatabase) => {const {password,...database}=body;return managementCall<{connected:boolean;table_count:number}>('test_database_connection', {database,password});},
 add: (body: NewDatabase) => post<Database>('/api/databases', body),
 save: (body: Database) => request<Database>(`/api/databases/${encodeURIComponent(body.id)}`, {method:'PUT', body: JSON.stringify({...body,discovery:undefined})}),
};
async function request<T>(path: string, init?: RequestInit): Promise<T> {
 if (path.startsWith('/api/') && !path.startsWith('/api/databases')) path = `/api/databases/${encodeURIComponent(selectedDatabase)}/${path.slice(5)}`;
  const response = await fetch(path, {
 credentials: "same-origin",
    ...init,
    headers: { 'Content-Type': 'application/json', ...init?.headers },
  });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
 if(response.status===401)window.dispatchEvent(new Event("platform-session-expired"));
    const message = typeof body.error === 'string' ? body.error : `${response.status} ${response.statusText}`;
    throw new ApiError(message, response.status, body);
  }
  return body as T;
}

const post = <T>(path: string, body: unknown) => request<T>(path, { method: 'POST', body: JSON.stringify(body) });

export interface BrowseParams {
  limit: number;
  offset: number;
  order?: string;
  desc?: boolean;
  q?: string;
  col?: string;
  val?: string;
}

let usersPromise: Promise<UserSummary[]> | undefined;

export const api = {
  tables: () => request<TableInfo[]>('/api/tables'),
  describe: (table: string) => request<TableSchema>(`/api/tables/${encodeURIComponent(table)}`),
  rows: (table: string, p: BrowseParams) => {
    const qs = new URLSearchParams({ limit: String(p.limit), offset: String(p.offset) });
    if (p.order) qs.set('order', p.order);
    if (p.desc) qs.set('desc', '1');
    if (p.q) qs.set('q', p.q);
    if (p.col) {
      qs.set('col', p.col);
      qs.set('val', p.val ?? '');
    }
    return request<BrowseResult>(`/api/tables/${encodeURIComponent(table)}/rows?${qs}`);
  },
  users: () => (usersPromise ??= request<UserSummary[]>('/api/employees').catch((e) => {
    usersPromise = undefined;
    throw e;
  })),
  policies: () => request<PoliciesResponse>('/api/policies'),
  rawQuery: (sql: string, maxRows: number, parameters: Record<string, unknown> = {}) => post<QueryResult>('/api/query', { sql, max_rows: maxRows, parameters }),
  rewrite: (sql: string, userId: number, maxRows: number, parameters: Record<string, unknown> = {}) =>
    post<RewriteResult>('/api/rewrite', { sql, user_id: userId, max_rows: maxRows, parameters }),
  previewRewrite: (sql: string, userId: number, parameters: Record<string, unknown> = {}) =>
    post<RewritePreview>('/api/rewrite', { sql, user_id: userId, preview_only: true, parameters }),
  mcpInfo: () => request<ServerInfo>('/api/mcp/info'),
  mcpCall: (name: string, args: Record<string, unknown>, _userId: number) =>
    post<CallResult>('/api/mcp/call', { name, arguments: args }),
  mcpVerify: () => post<Report>('/api/mcp/verify', {}),
};

export interface KnowledgePrompt {name:string;title:string;description:string}
export interface KnowledgePrompts {prompts:KnowledgePrompt[];nextCursor?:string}
export interface KnowledgePromptResult {description:string;messages:{role:string;content:{type:string;text:string}}[]}
export interface Knowledge {id:string;database_id:string;name:string;description:string;content?:string;published:boolean;revision:number}

export interface AuditEvent {id:string;request_id:string;actor_id:number;subject_id:number;database_id:string;source:string;tool:string;kind:string;status:string;original_sql?:string;rewritten_sql?:string;executed_sql?:string;binds?:unknown;error?:string;started_at:string;finished_at?:string;rows:number;truncated:boolean;detail?:unknown;policy_version:number}
export interface AuditFilters {database_id?:string;platform_only?:boolean;request_id?:string;actor_id?:number;kind?:string;source?:string;status?:string;tool?:string;query?:string;started_from?:string;started_to?:string}
export interface AuditResult {items:AuditEvent[];total:number;limit:number;offset:number;kinds:string[];sources:string[];actors:number[];actor_names:Record<string,string>}

// Global platform endpoints must never be rewritten into a selected database path.
export async function platformRequest<T>(path: string, init?: RequestInit): Promise<T> {
 const response=await fetch(path,{...init,credentials:'same-origin',headers:{'Content-Type':'application/json',...init?.headers}});
 const data=await response.json().catch(()=>({}));
 if(!response.ok){if(response.status===401&&!path.endsWith('/login'))window.dispatchEvent(new Event('platform-session-expired'));throw new ApiError(data.error||String(response.status),response.status,data);}
 return data as T;
}
export const managementCall=<T>(action:string,args:Record<string,unknown>={})=>platformRequest<T>('/api/management/call',{method:'POST',body:JSON.stringify({action,arguments:args})});
