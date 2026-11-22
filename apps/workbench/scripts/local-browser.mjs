import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as pause } from 'node:timers/promises';

export async function until(fn, hint, ms = 20000) {
  const end = Date.now() + ms;
  do { const value = await fn(); if (value) return value; await pause(100); } while (Date.now() < end);
  throw Error('Timed out waiting for ' + hint);
}
export async function withLocalBrowser({url,out,chromePath,onResponse,onlyOrigin=false,signal,launchEnv,onChildStart,allowGPU=false},run){
const base = new URL(url || '');
if (base.protocol !== 'http:' || !['localhost','127.0.0.1','[::1]'].includes(base.hostname)) throw Error('A local HTTP fixture is required.');
const candidates = [chromePath, 'C:/Program Files/Google/Chrome/Application/chrome.exe', 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe', '/usr/bin/chromium', '/usr/bin/google-chrome', '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'];
const exe = candidates.find(v => v && existsSync(v));
if (!exe) throw Error('Set WORKFLOW_CHROME to a local Chromium executable.');
if (out) await mkdir(out, {recursive:true});
const dir = await mkdtemp(join(tmpdir(), 'xingtu-workflow-'));
// The benchmark exercises one owned origin. Any other host or literal IP
// must use an unusable proxy, including other implicit loopback exceptions.
const network = onlyOrigin ? ['--disable-quic','--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE '+base.hostname,'--proxy-server=http://127.0.0.1:0','--proxy-bypass-list=<-loopback>;'+base.host] : [];
const chrome = spawn(exe, ['--headless=new',...(allowGPU ? [] : ['--disable-gpu']),'--no-first-run','--disable-background-networking','--disable-sync','--no-default-browser-check','--remote-debugging-port=0','--user-data-dir=' + dir,...network,'about:blank'], {windowsHide:true,stdio:'ignore',env:launchEnv});
let childClosed = false;
const waitClosed = new Promise(resolve => chrome.once('close', () => { childClosed = true; resolve(); }));
chrome.once('error', () => {}); // Node emits close after a failed spawn as well.
if (onChildStart) chrome.once('spawn', () => onChildStart(chrome.pid));
const waitExit = new Promise(resolve => chrome.once('exit', resolve));
let socket, sequence = 0;
const pending = new Map();
function cdp(method, params = {}, sessionId) {
  signal?.throwIfAborted();
  const id = ++sequence;
  return new Promise((resolve, reject) => {
    const timeout = onlyOrigin ? setTimeout(() => {pending.delete(id);reject(Error('Chromium command timed out: '+method));},15000) : null;
    pending.set(id, {resolve, reject, timeout}); socket.send(JSON.stringify({id, method, params, sessionId}));
  });
}
async function openPage({width=1280,height=900,deferNavigation=false,language='zh-CN'}={}) {
  const {browserContextId} = await cdp('Target.createBrowserContext');
  const {targetId} = await cdp('Target.createTarget',{url:'about:blank',browserContextId});
  const {sessionId} = await cdp('Target.attachToTarget',{targetId,flatten:true});
  const command = (method, params) => cdp(method,params,sessionId);
  const evaluate = async (expression, ...args) => {
    const source = typeof expression === 'string' ? expression : '(' + expression.toString() + ')(...' + JSON.stringify(args) + ')';
    const {result,exceptionDetails} = await command('Runtime.evaluate',{expression:source,returnByValue:true,awaitPromise:true});
    if (exceptionDetails) throw Error(result?.description || 'Browser evaluation failed');
    return result.value;
  };
  const size = (width,height) => command('Emulation.setDeviceMetricsOverride',{width,height,deviceScaleFactor:1,mobile:false});
  await size(width,height); await command('Network.enable'); await command('Page.enable'); await command('Runtime.enable');
  // Existing end-to-end fixtures operate the Chinese interface. Locale tests start with a clean profile.
  if (language) await command('Page.addScriptToEvaluateOnNewDocument', {
    source: 'if (location.origin === ' + JSON.stringify(base.origin) + ') localStorage.setItem("xingtu.language.v1", ' + JSON.stringify(language) + ');',
  });
  if(!deferNavigation) await command('Page.navigate',{url:base.origin});
  const button = (phrase, exact = false) => until(() => evaluate((phrase,exact) => {
    const e = [...document.querySelectorAll('button')].find(e => e.getClientRects().length && !e.disabled && (exact ? e.textContent.trim() === phrase : e.textContent.includes(phrase)));
    if (!e) return false; e.scrollIntoView({block:'center'}); e.click(); return true;
  },phrase,exact), 'button ' + phrase);
  const fill = (selector,value) => until(() => evaluate((selector,value) => {
    const e = [...document.querySelectorAll(selector)].find(e => e.getClientRects().length && !e.disabled); if (!e) return false;
    const proto = e instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : e instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, 'value').set.call(e,value);
    e.dispatchEvent(new Event(e instanceof HTMLSelectElement ? 'change' : 'input',{bubbles:true})); return true;
  },selector,value), 'input ' + selector);
  const screenshot = async name => {
    if (!out) return;
    const {data} = await command('Page.captureScreenshot',{format:'png',fromSurface:true});
    await writeFile(join(out,name + '.png'),Buffer.from(data,'base64'));
  };
  const downloads = dir => cdp('Browser.setDownloadBehavior',{behavior:'allow',downloadPath:dir,browserContextId});
  const printPDF = async file => {
    const {data}=await command('Page.printToPDF',{printBackground:true,preferCSSPageSize:true,displayHeaderFooter:false});
    await writeFile(file,Buffer.from(data,'base64'));
  };
  // Send a physical click for browser features that require transient user activation (e.g. audio playback).
  const trustedButton = async text => {
    await command('Page.bringToFront');
    const pos = await until(() => evaluate(label => {
      const element = [...document.querySelectorAll('button')].find(b =>
        b.getClientRects().length && !b.disabled && b.textContent.trim() === label);
      if (!element) return null;
      element.scrollIntoView({block:'center', behavior:'instant'});
      const rect = element.getBoundingClientRect();
      return {x:rect.left + rect.width/2, y:rect.top + rect.height/2};
    }, text), 'visible button ' + text);
    await command('Input.dispatchMouseEvent', {type:'mousePressed', button:'left', clickCount:1, ...pos});
    await command('Input.dispatchMouseEvent', {type:'mouseReleased', button:'left', clickCount:1, ...pos});
  };
  // Browser-level GPU evidence only. Exclude command lines, profile locations
  // and the rest of the unrestricted SystemInfo response from the report.
  const renderingInfo = async () => {
    const {product} = await cdp('Browser.getVersion');
    const {gpu} = await cdp('SystemInfo.getInfo');
    return {
      product, renderer:String(gpu.auxAttributes?.glRenderer || '').slice(0,256),
      compositing:String(gpu.featureStatus?.gpu_compositing || 'unknown'),
      canvas2d:String(gpu.featureStatus?.['2d_canvas'] || 'unknown'),
    };
  };
  return {evaluate,button,trustedButton,fill,screenshot,size,downloads,printPDF,command,renderingInfo,close:()=>cdp('Target.disposeBrowserContext',{browserContextId})};
}
const abort = () => { if(chrome.exitCode===null && chrome.signalCode===null) chrome.kill(); };
signal?.addEventListener('abort',abort,{once:true});
try {
  signal?.throwIfAborted();
  const info = await until(async () => {
    signal?.throwIfAborted();
    if (childClosed) throw Error('Chromium stopped before DevTools was ready');
    try { return (await readFile(join(dir,'DevToolsActivePort'),'utf8')).trim().split(/\r?\n/); }
    catch { return null; }
  },'Chromium DevTools');
  socket = new WebSocket('ws://127.0.0.1:' + info[0] + info[1]);
  await new Promise((resolve,reject) => {socket.onopen=resolve;socket.onerror=reject;});
  socket.onclose = () => {for(const p of pending.values()){clearTimeout(p.timeout);p.reject(Error('Chromium connection closed'));}pending.clear();};
  socket.onmessage = ({data}) => {
    const e = JSON.parse(data);
    if(e.method==='Network.responseReceived')onResponse?.(e.params.response);
    const p=pending.get(e.id); if (!p) return; pending.delete(e.id); clearTimeout(p.timeout); if(e.error) p.reject(Error(e.error.message)); else p.resolve(e.result); };
  await run(openPage);
} finally {
  signal?.removeEventListener('abort',abort);
  if (socket?.readyState === WebSocket.OPEN) {
    // The renderer can already be exiting after cancellation. Process cleanup
    // below also runs when Chromium has stopped accepting CDP commands.
    try { socket.send(JSON.stringify({id:++sequence,method:'Browser.close'})); }
    catch {}
    socket.close();
  }
  if (onlyOrigin) {
    await Promise.race([waitClosed, pause(1500, undefined, {ref:false})]);
    if (!childClosed && chrome.pid && chrome.exitCode === null && chrome.signalCode === null) {
      chrome.kill('SIGKILL');
    }
    await waitClosed;
  } else {
    if (chrome.exitCode === null) { await Promise.race([waitExit,pause(1500)]); if(chrome.exitCode===null) chrome.kill(); }
    await Promise.race([waitExit,pause(1500)]);
  }
  await rm(dir,{recursive:true,force:true,maxRetries:6,retryDelay:120});
}

}
