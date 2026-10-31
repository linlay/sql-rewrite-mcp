(() => {
  'use strict';
  const app = document.getElementById('app');
  function confirmationURL(raw) {
    const url = new URL(raw);
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.pathname !== '/actions/confirm' || !/^[a-f0-9]{64}$/.test(url.searchParams.get('token') || '')) throw new Error('确认链接无效');
    return url.href;
  }
  function render(data) {
    app.replaceChildren();
    const title = document.createElement('h2'); title.textContent = data.title || '确认业务操作'; app.append(title);
    const note = document.createElement('p'); note.textContent = '以下为操作预览，数据库修改已回滚。请打开确认页，使用发起操作的业务账号登录并确认。'; app.append(note);
    const result = document.createElement('pre'); result.style.whiteSpace = 'pre-wrap'; result.textContent = JSON.stringify(data.preview, null, 2); app.append(result);
    const button = document.createElement('a'); button.textContent = '打开确认页'; button.target = '_blank'; button.rel = 'noopener noreferrer'; button.href = confirmationURL(data.confirmation_url); app.append(button);
    const help = document.createElement('p'); help.textContent = '确认有效期为 10 分钟。确认后，使用原 request_id 调用 invoke_action 并设置 result_only:true 查询回执；没有回执时请核对审计，不重试写入。'; app.append(help);
  }
  if (!window.viewHost?.onData) { app.textContent = '请通过 Platform VIEW 宿主打开此视图。'; return; }
  window.viewHost.onData((data) => { try { render(data); } catch (e) { app.replaceChildren(); app.textContent = e.message; } });
})();
