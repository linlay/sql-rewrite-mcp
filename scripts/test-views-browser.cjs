// Run with the isolated Go harness; never points at the user's live database.
// DATASCOPE_PLAYWRIGHT may identify an existing Playwright module directory.
const { chromium } = require(process.env.DATASCOPE_PLAYWRIGHT || 'playwright');
const { spawn } = require('node:child_process');
const { resolve } = require('node:path');
const { mkdir } = require('node:fs/promises');
const assert = require('node:assert/strict');

(async () => {
 const root = resolve(__dirname, '..');
 const server = spawn(process.execPath, [resolve(root,'web/node_modules/vite/bin/vite.js'),'--host','127.0.0.1'], { cwd: resolve(root, 'web'), env: process.env, stdio: 'ignore' });
 let browser, page;
 try {
  const base = 'http://127.0.0.1:18192';
  for (let n = 0; n < 100; n++) { try { if ((await fetch(base)).ok) break; } catch {} await new Promise(r => setTimeout(r, 150)); }
  browser = await chromium.launch({ headless: true, executablePath: process.env.DATASCOPE_CHROMIUM });
  const context = await browser.newContext({ viewport: { width: 1440, height: 1080 } });
  page = await context.newPage();
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  await page.goto(base);
  await page.getByLabel('用户名', { exact: true }).fill('admin');
  await page.getByLabel('密码', { exact: true }).fill('installer-test-password');
  await page.getByRole('button', { name: /登\s*录/, exact: true }).click();
  await page.getByRole('menuitem', { name: /视图配置/ }).click();
  await page.getByRole('button', { name: /证券账户详情.*securities_account/ }).click();
  await page.getByRole('button', { name: /^预\s*览$/, exact: true }).click();
  await page.frameLocator('iframe:visible').getByRole('heading', { name: '证券账户详情' }).waitFor();
  assert.equal(await page.locator('iframe').getAttribute('sandbox'), 'allow-scripts');
  await page.getByRole('button', { name: '业务预览', exact: true }).click();
  await page.getByLabel('业务用户名', { exact: true }).fill('E0001');
  await page.getByLabel('业务账号密码', { exact: true }).fill('business-test-password');
  await page.getByRole('button', { name: '获取业务 Token', exact: true }).click();
  await page.getByRole('button', { name: '获取视图列表', exact: true }).click();
  await page.getByLabel('选择视图', { exact: true }).click();
  await page.locator('.ant-select-item-option[title="证券账户详情"]').click();
  const businessDialog = page.getByRole('dialog', {name:'业务预览',exact:true});
  const frame = businessDialog.frameLocator('iframe');
  await frame.locator('#account-detail').waitFor({ state: 'visible' });
  await frame.locator('#metrics .metric strong').first().waitFor();
  assert.match(await frame.locator('#customer').innerText(), /880000000079/);
  assert.match(await frame.locator('#date').innerText(), /2026-10-20/);
  if(process.env.DATASCOPE_VIEW_SCREENSHOTS){await mkdir(process.env.DATASCOPE_VIEW_SCREENSHOTS,{recursive:true});await businessDialog.locator('iframe').screenshot({path:resolve(process.env.DATASCOPE_VIEW_SCREENSHOTS,'securities-account.png')});}
  await frame.getByRole('tab', { name: '资金流水', exact: true }).click();
  await frame.getByRole('columnheader', { name: '流水类型' }).waitFor();
  await frame.getByRole('tab', { name: '委托', exact: true }).click();
  await frame.getByRole('columnheader', { name: '委托编号' }).waitFor();
  await frame.getByRole('button', { name: '冻结账户', exact: true }).click();
  await frame.locator('input[name="reason"]').fill('隔离证券视图浏览器验收');
  await frame.getByRole('button', { name: '获取操作预览', exact: true }).click();
  await frame.getByText(/confirm_required/).waitFor();
  const popup = context.waitForEvent('page');
  await frame.getByRole('button', { name: '打开本人确认页', exact: true }).click();
  const confirmPage = await popup;
  await confirmPage.getByLabel('业务账号密码').fill('business-test-password');
  await confirmPage.getByRole('button', { name: '确认并执行' }).click();
  await confirmPage.getByText(/"status": "done"/).waitFor();
  await confirmPage.close();
  await frame.getByRole('button', { name: '查询同一请求结果', exact: true }).click();
  await frame.getByRole('button', { name: '解冻账户', exact: true }).waitFor();
  assert.equal(await frame.locator('#account-status').innerText(), '冻结');
  assert.equal(await frame.getByRole('button', { name: '演示出金', exact: true }).count(), 0);
  // Token changes must destroy the previous identity's rendered account.
  await page.getByRole('button', { name: '清除业务 Token', exact: true }).click();
  await businessDialog.locator('iframe').waitFor({ state: 'detached' });
  await page.getByLabel('业务用户名', {exact:true}).fill('E0121');
  await page.getByLabel('业务账号密码', {exact:true}).fill('business-test-password');
  await page.getByRole('button',{name:'获取业务 Token',exact:true}).click();
  await page.getByRole('button',{name:'获取视图列表',exact:true}).click();
  await page.getByLabel('选择视图',{exact:true}).click();
  await page.locator('.ant-select-item-option[title="证券账户详情"]').click();
  const supervisor=businessDialog.frameLocator('iframe');
  await supervisor.getByRole('button',{name:'解冻账户',exact:true}).click();
  await supervisor.locator('input[name="reason"]').fill('验证主管查询权限不代表写权限');
  await supervisor.getByRole('button',{name:'获取操作预览',exact:true}).click();
  await supervisor.locator(".notice.error").filter({hasText:"denied"}).waitFor();
  await page.getByRole('button',{name:'清除业务 Token',exact:true}).click();
  await businessDialog.locator('iframe').waitFor({state:'detached'});
  // Verify edit -> preview -> save preserves HTML, including hidden form fields.
  await page.getByRole('dialog', {name:'业务预览',exact:true}).getByRole('button', {name:'Close',exact:true}).click();
  await page.getByRole('button', { name: '编辑 HTML', exact: true }).click();
  await page.getByLabel('名称', { exact: true }).fill('证券账户详情（浏览器验证）');
  await page.getByRole('button', { name: /^预\s*览$/, exact: true }).click();
  await page.getByRole('button', { name: '保存视图', exact: true }).click();
  await page.getByText('视图配置已保存', { exact: true }).waitFor();
  await page.frameLocator('iframe:visible').getByRole('heading', { name: '证券账户详情' }).waitFor();
  const output = process.env.DATASCOPE_VIEW_SCREENSHOTS;
  if (output) { await mkdir(output, { recursive: true }); await page.screenshot({ path: resolve(output, 'view-configuration.png'), fullPage: true }); }
  assert.deepEqual(errors, []);
  console.log('PASS: resource loading, account SQL, tabs, action preview, human confirmation, receipt refresh, identity reset, supervisor write denial, preview/save');
 } catch(e) { if(page){console.log('Page state:',await page.locator('body').innerText());if(process.env.DATASCOPE_VIEW_SCREENSHOTS){await mkdir(process.env.DATASCOPE_VIEW_SCREENSHOTS,{recursive:true});await page.screenshot({path:resolve(process.env.DATASCOPE_VIEW_SCREENSHOTS,'failure.png'),fullPage:true});}}throw e; } finally { if (browser) await browser.close(); server.kill('SIGTERM'); }
})().catch(e => { console.error(e); process.exitCode = 1; });
