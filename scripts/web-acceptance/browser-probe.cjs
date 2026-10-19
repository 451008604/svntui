'use strict';
// Discovery only: later workflows must use observed accessible names, not invented IDs.
const {browserSession,saveUI, evidenceDirectory} = require('./browser-helpers.cjs');
(async()=>{
  if(!process.env.SVNTUI_WEB_URL) throw new Error('set SVNTUI_WEB_URL; optional SVNTUI_STARTUP_TOKEN stays in process memory');
  const result = await browserSession({url:process.env.SVNTUI_WEB_URL,token:process.env.SVNTUI_STARTUP_TOKEN,
    evidence:evidenceDirectory('svntui-web-probe')}, async ({page,evidence,assert})=>{
    const session = await page.evaluate(async()=>{
      const response=await fetch('/api/v1/session',{credentials:'same-origin'});
      const payload=await response.json();
      return {status:response.status,ok:payload.ok,version:payload.data?.version,has_csrf:!!payload.data?.csrf_token};
    });
    assert.equal(session.status,200); assert.equal(session.ok,true); assert.equal(session.has_csrf,true);
    await page.waitForFunction(()=>document.querySelector('input[aria-label="选择 selected.txt"]') ||
      [...document.querySelectorAll('[role=alert]')].some(e=>e.textContent.trim()),null,{timeout:30000});
    const snapshot=await saveUI(page,evidence,'initial-ui');
    assert.ok(snapshot.controls.some(e=>e.name==='选择 selected.txt'),'workspace did not load fixture rows');
    return {probe_only:true,workflow_acceptance_executed:false,session};
  });
  console.log(JSON.stringify(result,null,2));
})().catch(e=>{console.error(e.name+': '+(e.message.startsWith('set ') ? e.message : 'browser probe failed; inspect redacted metadata'));process.exitCode=1;});
