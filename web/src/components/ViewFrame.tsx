import { useEffect, useMemo, useRef } from 'react';

export interface ViewData { input?: Record<string, unknown>; result?: unknown }
export type ViewCall = (name: string, args: Record<string, unknown>) => Promise<unknown>;

// Opaque-origin frames cannot access platform cookies, storage or network.
// Every tool request is forwarded by the host with its existing business bearer.
export function ViewFrame({ html, data, onCall }: { html: string; data?: ViewData; onCall?: ViewCall }) {
 const frame = useRef<HTMLIFrameElement>(null);
 const latest = useRef({ data, onCall }); latest.current = { data, onCall };
 const channel = useMemo(() => crypto.randomUUID(), [html]);
 const links = useRef(new Set<string>());
 const document = useMemo(() => {
  const policy = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; connect-src 'none'; form-action 'none'; base-uri 'none'";
  return `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="${policy}"><meta name="referrer" content="no-referrer"></head><body>${html}</body></html>`;
 }, [html]);
 const initialize = () => frame.current?.contentWindow?.postMessage({ type: 'datascope:view:init', channel, data: latest.current.data || {} }, '*');
 useEffect(() => { initialize(); }, [data, channel]);
 useEffect(() => {
  links.current.clear();
  const remember = (value: unknown) => {
   if (value && typeof value === 'object' && 'confirmation_url' in value && typeof value.confirmation_url === 'string') links.current.add(value.confirmation_url);
  };
  remember(latest.current.data?.result);
  const receive = async (event: MessageEvent) => {
   if (event.source !== frame.current?.contentWindow) return;
   const m = event.data;
   if (m?.type === 'datascope:view:ready') { initialize(); return; }
   if (m?.type !== 'datascope:view:request' || m.channel !== channel || !Number.isSafeInteger(m.id)) return;
   const source = frame.current?.contentWindow;
   try {
    let result: unknown;
    if (m.method === 'tools/call') {
     const name = m.params?.name, args = m.params?.arguments || {};
     if (!['query', 'list_tables', 'describe_table', 'list_actions', 'describe_action', 'invoke_action'].includes(name)) throw new Error('此视图不能调用该工具');
     if (!args || typeof args !== 'object' || Array.isArray(args)) throw new Error('调用参数必须是对象');
     if (!latest.current.onCall) throw new Error('预览未连接业务身份，请在“业务预览”中认证后使用。');
     result = await latest.current.onCall(name, args); remember(result);
    } else if (m.method === 'openLink') {
     if (!links.current.has(m.params?.url)) throw new Error('只能打开当前业务响应中的本人确认链接');
     const url = new URL(m.params.url, window.location.origin);
     if (!['https:', 'http:'].includes(url.protocol) || url.pathname !== '/actions/confirm') throw new Error('确认链接无效');
     const opened = window.open(url.href, '_blank', 'noopener,noreferrer');
     // Some browsers return null even after opening a noopener window.
     if (opened) opened.opener = null;
     result = { opened: true };
    } else throw new Error('视图请求不受支持');
    source?.postMessage({ type: 'datascope:view:response', channel, id: m.id, result }, '*');
   } catch (e) { source?.postMessage({ type: 'datascope:view:response', channel, id: m.id, error: (e as Error).message }, '*'); }
  };
  window.addEventListener('message', receive);
  return () => window.removeEventListener('message', receive);
 }, [channel]);
 return <iframe ref={frame} title="HTML 视图预览" sandbox="allow-scripts" referrerPolicy="no-referrer" srcDoc={document} onLoad={initialize} style={{ width: '100%', minHeight: 740, border: '1px solid var(--border-color, #dce5e1)', borderRadius: 12, background: '#f4f7f6' }} />;
}
