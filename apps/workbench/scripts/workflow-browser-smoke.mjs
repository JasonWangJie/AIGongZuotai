import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { until, withLocalBrowser } from './local-browser.mjs';
const withVideo=process.env.WORKFLOW_BROWSER_VIDEO==='1', withAttempts=process.env.WORKFLOW_BROWSER_ATTEMPTS==='1';
let activePage;const http=new Map();
const email = 'workflow-' + randomUUID() + '@example.invalid', password = 'Local-' + randomUUID();
async function login(page, register) {
  if (register) { await page.button('创建账户',true); await page.fill('#auth-name','画布验收'); }
  await page.fill('#auth-email',email); await page.fill('#auth-password',password);
  await page.button(register ? '创建我的账户' : '进入创作空间',true);
  await page.button('创作草稿'); await page.button('工作流画布',true);
  const safe = await page.evaluate(async () => { const r = await fetch('/api/v1/bootstrap'); if (!r.ok) return false; const b = await r.json(); return b.mode === 'demo' && b.storage === 'postgres' && b.auth_enabled; });
  assert.ok(safe,'isolated demo PostgreSQL login required before generation');
}
async function discardCommittedResponse(page, action) {
  await page.evaluate(action => {
    const send = window.fetch;
    window.fetch = async (...args) => {
      const response = await send(...args);
      if (args[1]?.method === 'POST' && new URL(response.url).pathname.endsWith('/' + action) && response.ok) {
        window.fetch = send;
        throw new TypeError('local browser fixture discarded the committed response');
      }
      return response;
    };
  }, action);
}
async function explicitAttemptScenario(page) {
  await page.size(1280,900);
  const original = await page.evaluate(() => ({ picture: document.querySelector('.wf-selected img').src,
    imageTask: [...document.querySelectorAll('.wf-run details')].find(e=>e.querySelector('summary')?.textContent.startsWith('图像 Task'))?.querySelector('code')?.textContent }));
  await page.button('查看重跑与取消',true); await page.fill('.wf-attempt-panel fieldset select','frames');
  await until(() => page.evaluate(() => document.querySelector('.wf-range')?.textContent.includes('沿用原运行结果')), 'authorized impact range');
  await page.fill('.wf-attempt-panel textarea','调整逆光，保持原主题。');
  await discardCommittedResponse(page,'rerun'); await page.button('确认创建新尝试',true); await page.button('只读核对原回执',true);
  await until(() => page.evaluate(() => document.querySelector('.wf-attempts')?.textContent.includes('第 2 次尝试')), 'new attempt and its source');
  await page.button('确认生成图片',true);
  await until(() => page.evaluate(() => document.querySelector('.wf-choice img')?.naturalWidth > 0), 'newly generated candidate',35000);
  const changed = await page.evaluate(() => [...document.querySelectorAll('.wf-run details')].find(e=>e.querySelector('summary')?.textContent.startsWith('图像 Task'))?.querySelector('code')?.textContent);
  assert.ok(changed && changed!==original.imageTask,'independent child task');
  await page.button('查看重跑与取消',true); await page.button('准备取消此运行',true);
  await discardCommittedResponse(page,'cancel'); await page.button('确认取消此运行',true); await page.button('只读核对原回执',true);
  await until(()=>page.evaluate(()=>document.querySelector('.wf-status')?.textContent==='流程已取消'),'cancellation receipt');
  await page.size(390,844); await page.evaluate(()=>document.querySelector('.wf-attempts').scrollIntoView({block:'center'})); await page.screenshot('workflow-attempt-cancelled-mobile');
  await page.button('查看原运行与成果',true);
  await until(()=>page.evaluate(()=>document.querySelector('.wf-status')?.textContent==='已完成' && document.querySelector('.wf-selected img')?.naturalWidth>0),'read original completed attempt');
  assert.equal(await page.evaluate(()=>document.querySelector('.wf-selected img').src),original.picture);
  console.log('PASS: explicit rerun preview, edited image prompt and new child, lost rerun/cancel responses reconciled by GET, original result preserved.');
}
function observe(response) {
      const path = new URL(response.url).pathname;
      if (path.includes('/workflow-')) {
        const leaf = path.split('/').at(-1), category = ['input','execute','confirm','receipt','workflow-runs','workflow-definitions'].includes(leaf) ? leaf : 'detail';
        const label = category + ':' + response.status; http.set(label, (http.get(label) || 0) + 1);
      }
    }
await withLocalBrowser({url:process.env.WORKFLOW_BROWSER_URL,out:process.env.WORKFLOW_BROWSER_SCREENSHOTS,chromePath:process.env.WORKFLOW_CHROME,onResponse:observe},async openPage=>{try {
  const first = activePage = await openPage();
  await login(first,true); await first.button(withVideo ? '四步视频模板' : '使用模板');
  await first.fill('.wf-editor > label input','晨雾分镜');
  await first.fill('.wf-inspector input','文字分镜');
  await first.button('生成候选图');
  await first.fill('.wf-inspector input','候选画面');
  await first.fill('.wf-inspector textarea','画面光线柔和，保留远处的山脊。');
  await first.fill('.wf-inspector > label:last-child select','');
  await first.fill('.wf-inspector > label:last-child select','brief');
  await first.button('选定画面');
  await first.fill('.wf-inspector input','人工选片');
  if (withVideo) {
    await first.button('生成首帧视频'); await first.fill('.wf-inspector input','交付视频');
    assert.ok(await first.evaluate(()=>[...document.querySelectorAll('.wf-inspector label')].some(l=>l.textContent.includes('视频规格与时长') && l.querySelector('select')?.selectedOptions[0]?.textContent.includes('秒'))),'frozen video duration option');
  }
  await first.screenshot(withVideo ? 'workflow-video-editor-desktop' : 'workflow-editor-desktop');
  await first.button('保存固定版本'); await first.fill('.wf-next textarea','晨雾中山谷的第一束光'); await first.button('启动此版本');
  await first.button('确认生成文本',true); await first.button('确认生成图片',true);
  await until(() => first.evaluate(() => document.querySelector('.wf-choice img')?.naturalWidth > 0),'approved private thumbnail',35000);
  await first.screenshot(withVideo ? 'workflow-video-approval-desktop' : 'workflow-approval-desktop');
  await first.close();
  const second = activePage = await openPage();
  await login(second,false); await second.button('项目运行',true); await second.button('晨雾分镜');
  await until(()=>second.evaluate(()=>!!document.querySelector('.wf-choice input[type=radio]')),'persisted pending selection');
  await second.button('查看固定版本'); await until(()=>second.evaluate(()=>document.querySelector('.wf-fixed')?.textContent.includes('人工选片')),'frozen workflow version');
  await second.evaluate(()=>{ const labels=[...document.querySelectorAll('.wf-choice label')]; labels[labels.length-1].click(); });
  await second.button('确认所选图片');
  await until(()=>second.evaluate(()=>document.querySelector('.wf-selected img')?.naturalWidth > 0),'selected private asset');
  if (withVideo) {
    await until(()=>second.evaluate(()=>document.querySelector('.wf-status')?.textContent==='等待确认生成视频'),'separate video confirmation');
    await second.button('确认生成视频',true);
    await until(()=>second.evaluate(()=>{const v=document.querySelector('.wf-film video');return v && v.readyState >= 1 && v.videoWidth > 0 && v.duration > 0 && Number.isFinite(v.duration);}), 'decodable private video metadata', 40000);
    assert.ok(await second.evaluate(()=> {const u=new URL(document.querySelector('.wf-film a').href);return u.origin===location.origin && u.pathname.startsWith('/api/v1/library/') && u.pathname.endsWith('/content');}),'private project video');
  }
  assert.equal(await second.evaluate(()=>document.querySelector('.wf-status')?.textContent),'已完成');
  assert.ok(await second.evaluate(()=>[...document.querySelectorAll('.wf-run summary')].filter(e=>/文本 Run|图像 Task/.test(e.textContent)).length===2),'linked children');
  await second.size(390,844); await second.evaluate(()=>document.querySelector('.wf-selected').scrollIntoView({block:'center'}));
  assert.ok(await second.evaluate(()=> document.documentElement.scrollWidth <= innerWidth && document.querySelector('.wf-detail').scrollWidth <= document.querySelector('.wf-detail').clientWidth + 1),'narrow layout overflow');
  assert.ok(await second.evaluate(()=>{const image=document.querySelector('.wf-selected img'), r=image.getBoundingClientRect();return document.elementFromPoint(r.left+r.width/2,r.top+r.height/2)===image;}),'selected picture is visible on mobile');
  await second.screenshot(withVideo ? 'workflow-video-first-frame-mobile' : 'workflow-completed-mobile');
  if (withVideo) {
    await second.evaluate(()=>document.querySelector('.wf-film video').scrollIntoView({block:'center'}));
    assert.ok(await second.evaluate(()=>{const v=document.querySelector('.wf-film video'), b=v.getBoundingClientRect();return document.elementFromPoint(b.x+b.width/2,b.y+b.height/2)===v;}),'private video player visible on narrow screen');
    await second.screenshot('workflow-video-completed-mobile');
  }
  if (withAttempts) await explicitAttemptScenario(second);
  await second.close();
  if (withVideo) console.log('PASS: video template, frozen duration, original first frame, separate action, authenticated video with decodable metadata and mobile player.');
  console.log('PASS: Chromium workflow editor, explicit text/image dispatch, approved private thumbnail, independent login, fixed version, child links, manual selection and narrow viewport.');
} catch (error) {
  if (activePage) {
    await activePage.screenshot('workflow-failure').catch(() => {});
    const visible = await activePage.evaluate(() => ({ stage: document.querySelector('.wf-status')?.textContent, alerts: [...document.querySelectorAll('.workflow-workbench [role=alert] > p')].map(p => p.textContent) })).catch(() => ({}));
    console.error('Workflow browser state', JSON.stringify({...visible, workflowHttp: Object.fromEntries(http)}));
  }
  throw error;
}
});
