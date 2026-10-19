'use strict';
// Task-owned browser only. No screenshots or trace are captured until token removal.
const fs = require('node:fs/promises');
const path = require('node:path');
const assert = require('node:assert/strict');
const fsSync = require('node:fs');
const os = require('node:os');
const playwrightModule = process.env.SVNTUI_PLAYWRIGHT || 'playwright';
const { chromium } = require(playwrightModule);
function evidenceDirectory(prefix='svntui-web') {
  return process.env.SVNTUI_BROWSER_EVIDENCE || fsSync.mkdtempSync(path.join(os.tmpdir(),prefix+'-'));
}

function safeBase(value) {
  const url = new URL(value);
  assert.equal(url.protocol, 'http:');
  assert.ok(['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname), 'acceptance URL must be loopback');
  assert.equal(url.username + url.password + url.search, '', 'no credentials or query allowed');
  url.hash = '';
  return url;
}


function redactMessage(value) {
  let text=String(value);
  const secrets=[];
  for(const key of ['SVNTUI_WEB_URL','SVNTUI_OTHER_WEB_URL','SVNTUI_STARTUP_TOKEN']) {
    const value=process.env[key];if(!value)continue;secrets.push(value);
    if(key.includes('URL'))try{const url=new URL(value);if(url.hash){secrets.push(url.hash.slice(1));const token=new URLSearchParams(url.hash.slice(1)).get('token');if(token)secrets.push(token);}}catch{}
  }
  for(const secret of secrets.sort((a,b)=>b.length-a.length))text=text.split(secret).join('[startup-credential-redacted]');
  return text.replace(/#token=[^\s"')]+/gi,'#token=[redacted]');
}

async function browserSession(options, workflow) {
  const supplied = new URL(options.url);
  const base = safeBase(options.url);
  const evidence = path.resolve(options.evidence || evidenceDirectory('svntui-web-browser'));
  await fs.mkdir(evidence, {recursive:true});
  const browser = await chromium.launch({
    executablePath: options.executablePath || process.env.SVNTUI_CHROMIUM,
    channel: options.channel || process.env.SVNTUI_BROWSER_CHANNEL || undefined,
    headless: options.headless !== false,
    args: options.args || JSON.parse(process.env.SVNTUI_CHROMIUM_ARGS || (process.platform==='linux' ? '["--no-sandbox","--disable-dev-shm-usage"]' : '[]')),
  });
  const context = await browser.newContext({viewport:{width:1440,height:1000}, locale:'zh-CN'});
  const page = await context.newPage();
  const responses = [];
  const exceptions = [];
  // Persist no URL fragment, query, response body, request body, cookie, or headers.
  page.on('response', r => {
    const u = new URL(r.url());
    if (u.origin === base.origin) responses.push({method:r.request().method(),path:u.pathname,status:r.status()});
  });
  page.on('pageerror', e => exceptions.push({name:e.name}));
  await page.addInitScript(() => {window.__svntui_injected = 0;});
  let authenticated = false;
  try {
    const initial = new URL(base);
    initial.hash = options.token ? 'token=' + encodeURIComponent(options.token) : supplied.hash;
    await page.goto(initial.toString(), {waitUntil:'domcontentloaded'});
    await page.waitForFunction(() => location.hash === '', null, {timeout:15000});
    await page.waitForFunction(async () => {
      try { return (await fetch('/api/v1/session',{credentials:'same-origin'})).ok; }
      catch { return false; }
    }, null, {timeout:15000});
    authenticated = true;
    const result = await workflow({page, context, browser, evidence, base:base.toString(), assert});
    assert.equal(await page.evaluate(() => window.__svntui_injected), 0, 'markup executed script');
    return result;
  } finally {
    await fs.writeFile(path.join(evidence,'browser-metadata.json'), JSON.stringify({
      browser:browser.version(), executable:options.executablePath || process.env.SVNTUI_CHROMIUM,
      platform:process.platform, arch:process.arch, node:process.version,
      browser_channel:options.channel || process.env.SVNTUI_BROWSER_CHANNEL || null, base_url:base.toString(),
      fragment_removed:authenticated, responses, page_errors:exceptions,
      trace_recorded:false,
    },null,2));
    await context.close();
    await browser.close();
  }
}

async function inspectUI(page) {
  return page.evaluate(() => ({
    title:document.title,
    headings:[...document.querySelectorAll('h1,h2,h3,[role=heading]')].map(e=>e.textContent.trim()),
    controls:[...document.querySelectorAll('button,input,textarea,select,a,[role=button],[role=tab]')].map(e=>({
      tag:e.tagName.toLowerCase(),role:e.getAttribute('role'),type:e.getAttribute('type'),
      name:e.getAttribute('aria-label') || e.labels?.[0]?.textContent?.trim() || e.textContent.trim(),
      placeholder:e.getAttribute('placeholder'),disabled:e.disabled || false,
    })),
    text:document.body.innerText,
    injection_sentinel:window.__svntui_injected,
  }));
}

async function saveUI(page, evidence, name) {
  assert.match(name,/^[a-z0-9-]+$/);
  const snapshot = await inspectUI(page);
  await fs.writeFile(path.join(evidence,name+'.json'), JSON.stringify(snapshot,null,2));
  await page.screenshot({path:path.join(evidence,name+'.png'),fullPage:true});
  return snapshot;
}

async function waitJob(page, jobId, timeout=30000) {
  const deadline = Date.now()+timeout;
  while (Date.now()<deadline) {
    const job = await page.evaluate(async id => {
      const response = await fetch('/api/v1/jobs/'+encodeURIComponent(id),{credentials:'same-origin'});
      const payload = await response.json();
      if (!response.ok || !payload.ok) throw new Error('job lookup failed');
      return payload.data;
    }, jobId);
    if (job.state !== 'running') return job;
    await new Promise(resolve=>setTimeout(resolve,100));
  }
  throw new Error('job did not finish within deadline');
}

// Exact API-returned canonical path keeps the locator stable while project rows change.
function projectButton(page, project) {
  return page.locator('nav.projects button').and(page.getByTitle(project.path,{exact:true}));
}

module.exports = {projectButton,browserSession, inspectUI, saveUI, waitJob, safeBase, redactMessage, evidenceDirectory};
