import { createContext, useContext, useEffect } from 'react';
import { Modal } from 'antd';

export type Page = 'database' | 'tables' | 'actions' | 'views' | 'knowledge' | 'knowledge-workbench' | 'action-workbench' | 'sql' | 'table' | 'mcp' | 'audit';
export interface Route { page: Page; selection?: string; params: URLSearchParams }
const pages: Page[] = ['database', 'tables', 'actions', 'views', 'knowledge', 'knowledge-workbench', 'action-workbench', 'sql', 'table', 'mcp', 'audit'];
const aliases: Record<string, Page> = { data: 'table', discovery: 'tables', policies: 'tables', access: 'database' };

export function readRoute(hash = window.location.hash): Route {
 const separator = hash.indexOf('?');
 const path = (separator < 0 ? hash : hash.slice(0, separator)).replace(/^#\/?/, '');
 const params = new URLSearchParams(separator < 0 ? '' : hash.slice(separator + 1));
 const [rawPage, rawSelection] = path.split('/');
 let page = aliases[rawPage] ?? (pages.includes(rawPage as Page) ? rawPage as Page : 'database');
 if (page === 'database' && params.get('tab') === 'audit') { page = 'audit'; params.delete('tab'); }
 let selection: string | undefined;
 try { selection = rawSelection ? decodeURIComponent(rawSelection) : undefined; } catch { /* An invalid bookmark falls back to the page's default selection. */ }
 return { page, selection, params };
}

function routeHash(route: Route) {
 const query = route.params.toString();
 return `#/${route.page}${route.selection ? `/${encodeURIComponent(route.selection)}` : ''}${query ? `?${query}` : ''}`;
}

export const NavigationContext = createContext<Route | null>(null);
export function useRoute() {
 const route = useContext(NavigationContext);
 if (!route) throw new Error('NavigationContext is required');
 return route;
}

export function useRouteChoice(key: string, fallback: string, choices: string[]) {
 const route = useRoute();
 const raw = route.params.get(key);
 const value = raw && choices.includes(raw) ? raw : fallback;
 return [value, (next: string, replace = false) => setRouteParams({[key]: next === fallback ? undefined : next}, replace, true)] as const;
}

const indexKey = 'datascopeNavigationIndex';
let accepted = { hash: '', index: 0 };
let listener: (() => void) | undefined;
let pending = false;
let allowed: { hash: string; index: number } | undefined;
const blockers = new Map<symbol, () => Promise<boolean>>();

async function canLeave() {
 const active = [...blockers.values()];
 for (const block of active) if (!await block()) return false;
 return true;
}

function commit(hash: string, replace: boolean) {
 const index = replace ? accepted.index : accepted.index + 1;
 window.history[replace ? 'replaceState' : 'pushState']({ ...window.history.state, [indexKey]: index }, '', hash);
 accepted = { hash: window.location.hash, index };
 listener?.();
}

function write(route: Route, replace = false, confirmed = false) {
 const hash = routeHash(route);
 if (hash === window.location.hash) return;
 if (replace || confirmed || !blockers.size) { commit(hash, replace); return; }
 if (pending) return;
 pending = true;
 void canLeave().then(ok => { if (ok) commit(hash, false); }).finally(() => { pending = false; });
}

export function navigate(page: Page, selection?: string, params: Record<string, string | undefined> = {}, replace = false) {
 const query = new URLSearchParams();
 const database = readRoute().params.get('database');
 if (database) query.set('database', database);
 for (const [key, value] of Object.entries(params)) if (value !== undefined) query.set(key, value);
 write({ page, selection, params: query }, replace);
}

export function selectResource(selection?: string, replace = false) {
 write({ ...readRoute(), selection }, replace, true);
}

export function setRouteParams(values: Record<string, string | undefined>, replace = false, confirmed = false) {
 const route = readRoute();
 for (const [key, value] of Object.entries(values)) if (value === undefined) route.params.delete(key); else route.params.set(key, value);
 write(route, replace, confirmed);
}

// Track hash history entries so Back/Forward can be cancelled without dropping
// a draft or manufacturing a second navigation entry when the user confirms.
export function startNavigation(onChange: () => void) {
 listener = onChange;
 const initialIndex = window.history.state?.[indexKey] ?? 0;
 window.history.replaceState({ ...window.history.state, [indexKey]: initialIndex }, '', window.location.href);
 accepted = { hash: window.location.hash, index: initialIndex };
 const onLocation = () => {
  const hash = window.location.hash;
  if (hash === accepted.hash) return;
  let index = window.history.state?.[indexKey] as number | undefined;
  if (index === undefined) {
   index = accepted.index + 1;
   window.history.replaceState({ ...window.history.state, [indexKey]: index }, '', window.location.href);
  }
  if (allowed?.hash === hash && allowed.index === index) {
   allowed = undefined;
  } else if (blockers.size) {
   const target = { hash, index };
   const distance = accepted.index - index;
   if (distance) window.history.go(distance); else commit(accepted.hash, true);
   if (!pending) {
    pending = true;
    void canLeave().then(ok => {
     if (ok) {
      allowed = target;
      if (distance) window.history.go(-distance); else commit(target.hash, true);
     }
    }).finally(() => { pending = false; });
   }
   return;
  }
  accepted = { hash, index };
  listener?.();
 };
 window.addEventListener('hashchange', onLocation);
 window.addEventListener('popstate', onLocation);
 onChange();
 return () => {
  window.removeEventListener('hashchange', onLocation);
  window.removeEventListener('popstate', onLocation);
  listener = undefined;
 };
}

export function useNavigationBlocker(dirty: boolean, title: string, busy = false) {
 useEffect(() => {
  if (!dirty && !busy) return;
  const key = Symbol();
  blockers.set(key, () => busy ? Promise.resolve(false) : new Promise(resolve => {
   Modal.confirm({ title, onOk: () => { resolve(true); }, onCancel: () => { resolve(false); } });
  }));
  return () => { blockers.delete(key); };
 }, [dirty, title, busy]);
}
