'use strict';
const fs=require('node:fs/promises');
const path=require('node:path');
const {browserSession,saveUI,safeBase,redactMessage,evidenceDirectory}=require('./browser-helpers.cjs');
(async()=>{
  const evidence=evidenceDirectory('svntui-web-browser');
  const baseline=process.env.SVNTUI_COOKIE_BASELINE==='1';
  const report={same_browser_context:true,usability_check_only:true,baseline_reproduction:baseline,completed:false};
  try{await browserSession({url:process.env.SVNTUI_WEB_URL,evidence},async({page,context,base,assert})=>{
    const other=process.env.SVNTUI_OTHER_WEB_URL;const otherBase=safeBase(other);
    assert.notEqual(new URL(base).port,otherBase.port);assert.equal(new URL(base).hostname,otherBase.hostname);
    async function session(target){return target.evaluate(async()=>{const r=await fetch('/api/v1/session');const v=await r.json();return {status:r.status,ok:v.ok,code:v.error?.code||null};});}
    report.a_before=await session(page);assert.equal(report.a_before.status,200);
    const second=await context.newPage();await second.goto(other,{waitUntil:'domcontentloaded'});
    await second.waitForFunction(()=>location.hash==='',null,{timeout:15000});
    await second.waitForFunction(async()=>{try{return (await fetch('/api/v1/session')).ok;}catch{return false;}},null,{timeout:15000});
    report.b_after=await session(second);assert.equal(report.b_after.status,200);
    report.a_after=await session(page);report.collision_reproduced=report.a_after.status===401;
    report.ports={a:new URL(base).port,b:otherBase.port};
    report.cookie_names=(await context.cookies(base)).map(c=>({name:c.name,http_only:c.httpOnly,same_site:c.sameSite}));
    if(baseline)assert.equal(report.a_after.status,401,'expected baseline cookie overwrite reproduction');
    else assert.equal(report.a_after.status,200,'second loopback server invalidated first server session');
    await saveUI(page,evidence,'server-a');await saveUI(second,evidence,'server-b');
  });report.completed=true;}catch(e){report.error_type=e.name;report.error_message=redactMessage(e.message).slice(0,600);process.exitCode=1;}
  await fs.writeFile(path.join(evidence,'two-server-cookie-summary.json'),JSON.stringify(report,null,2));console.log(JSON.stringify(report,null,2));
})().catch(e=>{console.error(e.name+': two-server cookie check failed; credentials withheld');process.exitCode=1;});
