import assert from 'node:assert/strict';
import {until,withLocalBrowser} from './local-browser.mjs';

const env=process.env;
const required=k=>{ if(!env[k])throw Error('Missing browser fixture setting '+k);return env[k];};
const url=required('CONTENT_APPEAL_BROWSER_URL'), task=required('CONTENT_APPEAL_TASK_ID'),workspace=required('CONTENT_APPEAL_WORKSPACE_ID');
const phrase='请复核这次输出的审核决定及冻结额度。', reply='已登记独立审议，请财务核实原冻结。';
const ops=role=>({email:required('CONTENT_APPEAL_'+role+'_EMAIL'),password:required('CONTENT_APPEAL_'+role+'_PASSWORD')});
const owner=ops('USER'),moderator=ops('MODERATOR'),finance=ops('FINANCE');
const counts=new Map();
function observe(r){const path=new URL(r.url).pathname;if(path.includes('content-appeal')){const category=path.endsWith('/receipt')?'receipt':path.endsWith('/execute')?'execute':path.endsWith('/plans')?'preview':path.includes('finance')?'finance':'appeal';const label=category+':'+r.status;counts.set(label,(counts.get(label)||0)+1);}}
async function login(p,actor) {
  await p.fill('#auth-email',actor.email);await p.fill('#auth-password',actor.password);await p.button('进入创作空间',true);
  const local=await until(()=>p.evaluate(async()=>{const u=await fetch('/api/v1/bootstrap');if(!u.ok)return false;const b=await u.json();return b.mode==='demo' && b.storage==='postgres' && !!b.auth_enabled;}),'authenticated isolated PostgreSQL');
  assert.ok(local);
}
async function check(p,selector){await until(()=>p.evaluate(s=>{const e=document.querySelector(s);if(!e||e.disabled)return false;if(!e.checked)e.click();return e.checked;},selector),'confirm control');}
async function loseAck(p,operation) {
  await p.evaluate(operation=>{
    const original=window.fetch;
    window.fetch=async(...args)=>{
      const response=await original(...args);
      const path=new URL(args[0] instanceof Request?args[0].url:String(args[0]),location.origin).pathname;
      if(response.ok && args[1]?.method==='POST' && path.endsWith(operation)){
        window.fetch=original;throw new TypeError('fixture acknowledgement lost');
      }
      return response;
    };
  },operation);
}
await withLocalBrowser({url,out:env.CONTENT_APPEAL_BROWSER_SCREENSHOTS,chromePath:env.WORKFLOW_CHROME,onResponse:observe},async open=>{
  let stage='owner login',active;
  try{
    const first=active=await open();await login(first,owner);await first.button('任务中心');stage='source appeal';
    await until(()=>first.evaluate(()=>!!document.querySelector('.task-card')),'original task');
    await first.button('申请内容申诉',true);
    await until(()=>first.evaluate(t=>document.querySelector('.appeal-compose')?.textContent.includes(t),task),'form bound to original task');
    await first.fill('.appeal-compose textarea',phrase);await first.fill('.appeal-compose select','billing_concern');
    await loseAck(first,'/content-appeals');await first.button('提交内容申诉',true);await first.button('只读核对原回执',true);
    await until(()=>first.evaluate(v=>document.querySelector('.appeal-detail')?.textContent.includes(v),phrase),'owner receipt');
    await first.screenshot('appeal-owner-desktop');await first.close();
    stage='moderator plan';const mod=active=await open();await login(mod,moderator);await mod.button('运营核实');await mod.button('内容申诉审议',true);
    await mod.button('媒体输出');assert.ok(await until(()=>mod.evaluate(v=>document.querySelector('.appeal-detail')?.textContent.includes(v),phrase),'authorized statement'));
    await mod.fill('.appeal-compose select','refer_finance');await mod.fill('.appeal-compose input[placeholder]','ticket:moderation');await mod.fill('.appeal-compose textarea',reply);
    await mod.button('预演内容申诉处置',true);await check(mod,'.appeal-preview input[type=checkbox]');
    await mod.evaluate(()=>document.querySelector('.appeal-preview').scrollIntoView({block:'center'}));await mod.screenshot('appeal-moderator-preview');await mod.fill('.appeal-preview input[type=password]',moderator.password);
    await loseAck(mod,'/execute');stage='moderator receipt';await mod.button('确认内容申诉处置',true);await mod.button('只读核对原回执',true);
    await until(()=>mod.evaluate(()=>document.body.textContent.includes('已核对处置收据')),'moderator receipt');await mod.close();
    stage='finance referral';const fin=active=await open();await login(fin,finance);await fin.button('运营核实');await fin.button('申诉财务转交',true);
    await fin.button('关联财务核实工单',true);
    assert.ok(await fin.evaluate(s=>!document.body.textContent.includes(s),phrase),'private statement absent from finance queue');
    await fin.button('打开财务核实工单',true);await fin.fill('select[aria-label="处置动作"]','release');
    await fin.fill('input[aria-label="外部证据编号"]','ticket:finance-verified');await check(fin,'form.ops-fields .ops-check input[type=checkbox]');
    stage='finance release';await fin.button('生成预演 · 尚不执行',true);
    await check(fin,'section[aria-label="处置预演与确认"] input[type=checkbox]');await fin.fill('input[aria-label="当前账户密码"]',finance.password);
    await fin.button('确认执行本次处置',true);await until(()=>fin.evaluate(()=>document.body.textContent.includes('本工单已结束')),'actual finance completion');await fin.close();
    stage='owner reads final result';const final=active=await open();await login(final,owner);await final.button('我的内容申诉');await final.button('媒体输出');
    await until(()=>final.evaluate(()=>document.querySelector('.appeal-detail')?.textContent.includes('已释放 0.2 创作点')),'released original hold on owner detail');
    assert.ok(await final.evaluate(s=>document.querySelector('.appeal-detail')?.textContent.includes(s),reply));
    await final.size(390,844);await final.evaluate(()=>document.querySelector('.appeal-detail h3:last-of-type').scrollIntoView({block:'center'}));
    assert.ok(await final.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'narrow viewport');
    await final.screenshot('appeal-owner-release-mobile');await final.close();
    console.log('PASS: owner and moderator lost acknowledgements reconciled, independent finance release and final owner view verified.');
  }catch(e){
    const state=active && await active.evaluate(()=>{
      const marker=s=>!!document.querySelector(s);
      return {appealPage:marker('.appeal-page'),form:marker('.appeal-compose'),detail:marker('.appeal-detail'),preview:marker('.appeal-preview'),financial:marker('.ops-workbench'),alerts:document.querySelectorAll('[role=alert]').length,pending:marker('.appeal-notice[role=status]')};
    }).catch(()=>undefined);
    if(active && state && (state.appealPage || state.financial)){
      await active.evaluate(()=>document.querySelectorAll('input[type=password],input[type=email]').forEach(el=>el.style.visibility='hidden')).catch(()=>{});
      await active.screenshot('appeal-failure').catch(()=>{});
    }
    console.error('Content appeal browser stage',stage,'DOM',JSON.stringify(state),'HTTP',JSON.stringify(Object.fromEntries(counts)));throw e;
  }
});

