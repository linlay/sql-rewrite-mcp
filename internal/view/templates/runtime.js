// DataScope HTML bridge v1. The host holds credentials; the frame receives data only.
(() => {
 'use strict';
 let lastData = '';
 let channel = '', listener = () => {}, current, sequence = 0;
 const pending = new Map();
 function rpc(method, params) {
  if (!channel) return Promise.reject(new Error('请在支持 DataScope 视图的宿主中打开。'));
  const id = ++sequence;
  return new Promise((resolve, reject) => {
   const timer = setTimeout(() => { pending.delete(id); reject(new Error('调用超时；写入结果未知时请仅查询原请求回执。')); }, 45000);
   pending.set(id, { resolve, reject, timer });
   parent.postMessage({ type: 'datascope:view:request', channel, id, method, params }, '*');
  });
 }
 addEventListener('message', event => {
  if (event.source !== parent) return;
  const m = event.data;
  if (m?.type === 'datascope:view:init' && (!channel || channel === m.channel)) {
   channel = m.channel; const json = JSON.stringify(m.data || {}); if (json !== lastData) { lastData = json; current = m.data || {}; listener(current); } return;
  }
  if (m?.type !== 'datascope:view:response' || m.channel !== channel) return;
  const item = pending.get(m.id); if (!item) return;
  clearTimeout(item.timer); pending.delete(m.id);
  if (m.error) item.reject(new Error(m.error)); else item.resolve(m.result);
 });
 const el = (tag, text, cls) => { const n = document.createElement(tag); if (text !== undefined) n.textContent = text; if (cls) n.className = cls; return n; };
 function table(target, result, rowActions) {
  target.replaceChildren();
  if (!result?.rows?.length) { target.append(el('div', '暂无可见记录', 'empty')); return; }
  const t = el('table'), head = el('tr');
  for (const key of result.columns) head.append(el('th', key));
  if (rowActions) head.append(el('th', '操作'));
  t.append(head);
  for (const row of result.rows) {
   const tr = el('tr');
   for (const key of result.columns) tr.append(el('td', row[key] === null ? '—' : String(row[key] ?? '')));
   if (rowActions) { const td = el('td'); rowActions(td, row); tr.append(td); }
   t.append(tr);
  }
  target.append(t);
  if (result.truncated) target.append(el('p', '结果已截断，请缩小查询范围。', 'muted'));
 }
 function button(text, action) { const b = el('button', text); b.type = 'button'; b.onclick = action; return b; }
 function notice(target, text, error = false) { target.textContent = text; target.className = text ? 'notice' + (error ? ' error' : '') : 'status'; }
 function ask(text) { return new Promise(resolve => { const dialog = el('dialog'), p = el('p',text), buttons = el('div',undefined,'toolbar'); const done = value => {dialog.close();dialog.remove();resolve(value);}; buttons.append(button('取消',()=>done(false)),button('确认',()=>done(true)));dialog.append(p,buttons);dialog.addEventListener('cancel',e=>{e.preventDefault();done(false);},{once:true});document.body.append(dialog);dialog.showModal(); }); }
 const ds = window.datascope = {
  onData(fn) { listener = fn; if (current) fn(current); },
  callTool(name, args = {}) { return rpc('tools/call', { name, arguments: args }); },
  openLink(url) { return rpc('openLink', { url }); },
  el, table, button, notice,
  money(value) { return (Number(value || 0) / 100).toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 }); },
 };
 // A shared action form is UI only: the server validates schema, ownership and writes.
 ds.actionForm = (root, onCommitted = () => {}) => {
  let name = '', args = {}, requestID = '', attempted = false, busy = false, controls = {}, description;
  const heading = el('h2', '业务操作'), form = el('form'), status = el('div'), preview = el('pre'), links = el('div', undefined, 'toolbar');
  const submit = button('获取操作预览', async () => {
   if (attempted || busy || !form.reportValidity()) return;
   args = {};
   for (const [key, control] of Object.entries(controls)) {
    const field = description.parameters.properties[key];
    if (!control.value && !description.parameters.required.includes(key)) continue;
    args[key] = field.type === 'boolean' ? control.value === 'true' : ['integer', 'number'].includes(field.type) ? Number(control.value) : control.value;
   }
   // Keep the original ID even on transport failure. Never automatically replay writes.
   attempted = true; requestID = crypto.randomUUID(); await invoke(false);
  });
  const lookup = button('查询同一请求结果', () => invoke(true));
  const reset = button('新请求', async () => { if (busy || !await ask('请先核对上一请求结果。确定开始新请求？')) return; attempted = false; requestID = ''; links.replaceChildren(); preview.textContent = ''; notice(status, ''); update(); });
  const actions = el('div', undefined, 'toolbar'); actions.append(submit, lookup, reset);
  root.append(heading, form, status, preview, links, actions);
  function update() { submit.disabled = busy || attempted || !description; lookup.disabled = busy || !attempted; reset.disabled = busy; for (const c of Object.values(controls)) c.disabled = busy || attempted; }
  async function invoke(resultOnly) {
   if (busy || resultOnly && !attempted) return;
   busy = true; update(); notice(status, '正在读取结果…');
   try {
    const out = await ds.callTool('invoke_action', { name, arguments: resultOnly ? {} : args, request_id: requestID, ...(resultOnly ? { result_only: true } : {}) });
    render(out);
    if (out.status === 'done') await onCommitted();
   } catch (e) { notice(status, e.message + '\nrequest_id: ' + requestID, true); }
   finally { busy = false; update(); }
  }
  function render(out) {
   links.replaceChildren(); preview.textContent = JSON.stringify(out.preview || out.result || out, null, 2);
   notice(status, out.status + (out.message ? ' · ' + out.message : '') + '\nrequest_id: ' + requestID, ['denied', 'failed', 'unknown'].includes(out.status));
   if (out.status === 'confirm_required' && out.confirmation_url) links.append(button('打开本人确认页', () => ds.openLink(out.confirmation_url).catch(e => notice(status, e.message, true))));
  }
  return {
   async open(actionName, defaults = {}, existing) {
    if (attempted && !await ask('请先核对上一请求的结果。切换操作后请保留原 request_id 以查询回执。')) return false;
    busy = true; description = undefined; update(); form.replaceChildren(); links.replaceChildren(); preview.textContent = ''; notice(status, '正在读取动作参数…');
    try {
     description = await ds.callTool('describe_action', { name: actionName }); name = actionName; heading.textContent = description.title; controls = {}; attempted = false; requestID = '';
     for (const [key, field] of Object.entries(description.parameters.properties)) {
      const label = el('label', field.description || key); let c;
      if (field.enum || field.type === 'boolean') {
       c = el('select'); if (!description.parameters.required.includes(key)) c.append(el('option', ''));
       for (const value of field.enum || [false, true]) { const option = el('option', String(value)); option.value = String(value); c.append(option); }
      } else { c = el('input'); c.type = ['integer', 'number'].includes(field.type) ? 'number' : 'text'; if (c.type === 'number') { c.step = field.type === 'integer' ? '1' : 'any'; if (field.minimum !== undefined) c.min = String(field.minimum); if (field.maximum !== undefined) c.max = String(field.maximum); } if (field.maxLength !== undefined) c.maxLength = field.maxLength; /* Patterns use the server regexp dialect and are validated there. */ }
      c.name = key; c.required = description.parameters.required.includes(key); c.value = defaults[key] === undefined ? c.value : String(defaults[key]); label.append(c); form.append(label); controls[key] = c;
     }
     notice(status, description.description);
     if (existing?.request_id) { requestID = existing.request_id; attempted = true; args = defaults; if (existing.response) render(existing.response); }
     return true;
    } catch (e) { notice(status, e.message, true); return false; }
    finally { busy = false; update(); }
   }
  };
 };
 addEventListener('DOMContentLoaded', () => parent.postMessage({ type: 'datascope:view:ready' }, '*'));
})();
