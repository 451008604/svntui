'use strict';
// Deterministic network barriers/failures exercise real component navigation/state.
const fs=require('node:fs/promises');
const path=require('node:path');
const {execFile}=require('node:child_process');
const {browserSession,saveUI,redactMessage,evidenceDirectory}=require('./browser-helpers.cjs');
const manifestPath=process.env.SVNTUI_FIXTURE_MANIFEST;
function gate(){let signal;const item={seen:false,promise:new Promise(r=>{signal=r;})};item.resolve=()=>{item.seen=true;signal();};return item;}
async function until(fn,message,timeout=20000){const end=Date.now()+timeout;while(Date.now()<end){if(await fn())return;await new Promise(r=>setTimeout(r,100));}throw new Error(message);}
function normalize(v){v=v.replaceAll('\\','/').replace(/\/$/,'');return process.platform==='win32'?v.toLowerCase():v;}
function native(request){return new Promise((resolve,reject)=>{const child=execFile(process.env.SVNTUI_PYTHON||(process.platform==='win32'?'python':'python3'),[path.join(__dirname,'svn-fixture-check.py'),'--manifest',manifestPath],{windowsHide:true},(error,out)=>error?reject(new Error('native fixture check failed')):resolve(JSON.parse(out)));child.stdin.end(JSON.stringify(request));});}
(async()=>{
  const phase=process.env.SVNTUI_REGRESSION_PHASE||'scope';
  const manifest=JSON.parse(await fs.readFile(manifestPath,'utf8'));
  const evidence=evidenceDirectory('svntui-web-browser');
  const report={phase,real_browser_component_regression:true,network_injection_only:true,completed:false};
  const releases=[];
  try{await browserSession({url:process.env.SVNTUI_WEB_URL,evidence},async({page,assert})=>{
    try{
    async function read(route){return page.evaluate(async route=>{const r=await fetch('/api/v1'+route);const v=await r.json();if(!r.ok)throw new Error('API read failed');return v.data;},route);}
    const projects=await read('/projects');const a=projects.find(p=>normalize(p.path)===normalize(manifest.wc));assert.ok(a);
    const draftURL='/api/v1/projects/'+a.id+'/draft';
    const identity=()=>page.locator('.project-identity strong');
    const targets=()=>page.locator('.selected-targets code');
    async function choose(project){for(const button of await page.locator('nav.projects button').all())if(normalize(await button.getAttribute('title'))===normalize(project.path)){await button.click();break;}
      await until(async()=>(await identity().textContent())===project.name,'selected project identity');
      await until(async()=>await page.getByLabel('提交说明',{exact:true}).isEnabled(),'draft ready');}
    async function select(file){await page.getByRole('button',{name:'取消选择',exact:true}).click();await page.getByRole('checkbox',{name:'选择 '+file,exact:true}).check();}
    async function saved(file,message){await until(async()=>{const d=await read('/projects/'+a.id+'/draft');return d.message===message&&d.targets.includes(file);},'draft saved');
      await until(async()=>(await page.getByRole('status').textContent()).includes('已保存'),'saved status');}
    async function registerPeer({allowRemain=false}={}){await page.getByLabel('添加项目',{exact:true}).click();const modal=page.getByRole('dialog',{name:'添加本地工作副本',exact:true});
      await modal.getByLabel('工作副本路径',{exact:true}).fill(manifest.peer);await modal.getByLabel('项目名称（可选）',{exact:true}).fill('回归项目 B');
      const posted=page.waitForResponse(r=>r.url().endsWith('/api/v1/projects')&&r.request().method()==='POST');
      await modal.getByRole('button',{name:'验证并添加',exact:true}).click();assert.ok([200,201].includes((await posted).status()));
      if(!allowRemain)await until(async()=>!(await modal.isVisible()),'registration modal closed');
      const b=(await read('/projects')).find(p=>normalize(p.path)===normalize(manifest.peer));assert.ok(b);return b;}
    await choose(a);await until(async()=>(await page.getByRole('status').textContent()).includes('已保存'),'initial settled draft');
    if(phase==='scope'){
      const b=await registerPeer();await choose(a);await select('selected.txt');await page.getByLabel('提交说明',{exact:true}).fill('scope saved baseline');await saved('selected.txt','scope saved baseline');
      const captured=gate(),release=gate();releases.push(release);let held=false;
      await page.route('**'+draftURL,async route=>{if(route.request().method()==='PUT'&&!held){held=true;captured.resolve();await release.promise;}await route.continue();});
      await page.getByLabel('提交说明',{exact:true}).fill('scope pending draft');
      await page.getByRole('button',{name:'还原',exact:true}).click();await until(async()=>captured.seen,'delayed request reached');
      let navigationBlocked=false;
      for(const button of await page.locator('nav.projects button').all())if(normalize(await button.getAttribute('title'))===normalize(b.path)){
        navigationBlocked=!(await button.isEnabled());if(!navigationBlocked)await button.click();break;}
      release.resolve();
      await until(async()=>await page.getByRole('dialog',{name:/^还原 · /}).isVisible()||(await identity().textContent())===b.name,'settled operation or transition');
      const modal=page.getByRole('dialog',{name:/^还原 · /});
      if(await modal.isVisible()){
        const response=page.waitForResponse(r=>r.url().endsWith('/previews')&&r.request().method()==='POST');
        await modal.getByRole('button',{name:'生成预览',exact:true}).click();const observed=await response;
        report.observed_preview_project=observed.url().split('/projects/')[1].split('/')[0];
        report.originating_project=a.id;assert.equal(report.observed_preview_project,a.id,'operation retargeted from A to B');
        await modal.getByRole('button',{name:'取消',exact:true}).click();
      }
      report.navigation_blocked=navigationBlocked;
    }else if(phase==='registration'){
      const message='必须保留的未保存草稿 中文';await select('selected.txt');
      let failures=0;await page.route('**'+draftURL,async route=>{
        if(route.request().method()==='PUT'){failures++;await route.fulfill({status:409,contentType:'application/json',body:JSON.stringify({ok:false,data:null,error:{code:'draft_conflict',message:'验收模拟另一会话已保存新版本'},exit_code:1})});}
        else await route.continue();});
      await page.getByLabel('提交说明',{exact:true}).fill(message);await until(async()=>failures>0&&await page.getByRole('alert').count()>0,'visible draft failure');
      const b=await registerPeer({allowRemain:true});report.registration_succeeded=true;
      if((await identity().textContent())===b.name)await choose(a);
      assert.equal(await page.getByLabel('提交说明',{exact:true}).inputValue(),message,'failed-save draft discarded after registration');
      report.draft_preserved=true;
    }else if(phase==='draft-order'||phase==='draft-order-reverse'){
      await select('revert-me.txt');await page.getByRole('checkbox',{name:'选择 selected.txt',exact:true}).check();const message='restore stale target order';await page.getByLabel('提交说明',{exact:true}).fill(message);await saved('revert-me.txt',message);
      await native({op:'write',path:'revert-me.txt',text:'revert baseline\n'});
      const pendingStatus=new Set();
      const isStatus=request=>new URL(request.url()).pathname==='/api/v1/projects/'+a.id+'/status';
      page.on('request',request=>{if(isStatus(request))pendingStatus.add(request);});
      page.on('requestfinished',request=>pendingStatus.delete(request));page.on('requestfailed',request=>pendingStatus.delete(request));
      const captured=gate(),release=gate();releases.push(release);let held=false;
      const reverse=phase==='draft-order-reverse';report.response_order=reverse?'draft-before-status':'status-before-draft';let prematureDrop=false;
      if(reverse){
        page.on('request',request=>{if(held&&!release.seen&&request.method()==='PUT'&&new URL(request.url()).pathname===draftURL){
          try{if(!request.postDataJSON().targets.includes('revert-me.txt'))prematureDrop=true;}catch{}}});
        await page.route('**/api/v1/projects/'+a.id+'/status?*',async route=>{if(!release.seen){held=true;captured.resolve();report.held_status_requests=(report.held_status_requests||0)+1;await release.promise;}await route.continue();});
        const draftRead=page.waitForResponse(r=>new URL(r.url()).pathname===draftURL&&r.request().method()==='GET'&&r.status()===200);
        await page.reload({waitUntil:'domcontentloaded'});await until(async()=>captured.seen,'status request reached');await draftRead;
        await until(async()=>(await page.getByLabel('提交说明',{exact:true}).inputValue())===message,'draft arrives before status');
        assert.equal(prematureDrop,false,'inflight status was treated as an empty working copy');release.resolve();
      }else{
        await page.route('**'+draftURL,async route=>{if(route.request().method()==='GET'&&!held){held=true;captured.resolve();await release.promise;}await route.continue();});
        const statusRead=page.waitForResponse(r=>r.url().includes('/projects/'+a.id+'/status')&&r.status()===200);
        await page.reload({waitUntil:'domcontentloaded'});await until(async()=>captured.seen,'draft request reached');const status=(await (await statusRead).json()).data;
        assert.ok(status.entries.every(e=>e.path!=='revert-me.txt'),'native cleanup must precede loaded status');
        await until(async()=>pendingStatus.size===0&&await page.getByRole('checkbox',{name:'选择 selected.txt',exact:true}).isVisible()&&
          (await page.locator('.rail-bottom').textContent()).includes('本地服务已连接'),'status DOM and SSE startup refresh settled');
        release.resolve();
      }
      await until(async()=>(await page.getByLabel('提交说明',{exact:true}).inputValue())===message,'late draft applied');
      await until(async()=>(await page.getByRole('status').textContent()).includes('已保存'),'late draft settles');
      assert.ok(!(await targets().allTextContents()).includes('revert-me.txt'),'late draft restored stale path absent from status');
      const d=await read('/projects/'+a.id+'/draft');assert.ok(!d.targets.includes('revert-me.txt'));assert.ok(d.targets.includes('selected.txt'),'valid selected path must be retained');report.stale_target_removed=true;report.valid_target_retained=true;
    }else if(phase==='status-failure-navigation'){
      const b=await registerPeer();await choose(a);await select('selected.txt');
      await page.getByLabel('提交说明',{exact:true}).fill('R1 preserved targets');await saved('selected.txt','R1 preserved targets');
      await page.route('**/api/v1/projects/'+a.id+'/status?*',route=>route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({
        ok:false,data:null,error:{code:'svn_authentication',message:'验收模拟当前仓库远端状态不可用'},exit_code:1})}));
      const failedStatus=()=>until(async()=>(await page.getByRole('alert').allTextContents()).some(t=>t.includes('验收模拟当前仓库远端状态不可用')),'failed A status visible');
      await page.getByRole('button',{name:'刷新',exact:true}).click();await failedStatus();
      await choose(b);report.untouched_draft_allows_navigation=true;
      let draft=await read('/projects/'+a.id+'/draft');assert.deepEqual(draft.targets,['selected.txt']);
      await choose(a);await failedStatus();
      const message='R1 unavailable status but edited draft saved 中文';await page.getByLabel('提交说明',{exact:true}).fill(message);
      await saved('selected.txt',message);await choose(b);report.edited_saved_draft_allows_navigation=true;
      draft=await read('/projects/'+a.id+'/draft');assert.equal(draft.message,message);assert.deepEqual(draft.targets,['selected.txt']);
      await choose(a);await failedStatus();await registerPeer({allowRemain:true});
      await until(async()=>(await identity().textContent())===b.name,'successful registration can leave unavailable A');report.registration_navigation=true;
      await choose(a);await failedStatus();
      if(!(await page.locator('details.project-menu').evaluate(el=>el.open)))await page.getByLabel('项目操作',{exact:true}).click();
      await page.getByRole('button',{name:'清理工作副本',exact:true}).click();const operation=page.getByRole('dialog',{name:/^清理 · /});
      await operation.waitFor();const preview=page.waitForResponse(r=>r.url().endsWith('/previews')&&r.request().method()==='POST');
      await operation.getByRole('button',{name:'生成预览',exact:true}).click();const observed=await preview;assert.equal(observed.status(),200);
      assert.equal(new URL(observed.url()).pathname,'/api/v1/projects/'+a.id+'/previews');
      await operation.getByRole('button',{name:'取消',exact:true}).click();report.recovery_preview_available=true;
    }else if(phase==='escape'){
      await select('unselected.txt');await page.getByRole('button',{name:'还原',exact:true}).click();
      const modal=page.getByRole('dialog',{name:/^还原 · /});const captured=gate(),release=gate();releases.push(release);
      await page.route('**/api/v1/projects/'+a.id+'/previews',async route=>{captured.resolve();await release.promise;await route.continue();});
      await modal.getByRole('button',{name:'生成预览',exact:true}).click();await until(async()=>captured.seen,'delayed request reached');
      await modal.getByRole('button',{name:'正在预览…',exact:true}).waitFor();await page.keyboard.press('Escape');
      assert.equal(await modal.isVisible(),true,'Escape dismissed busy operation dialog');release.resolve();
      await modal.getByRole('button',{name:'确认执行',exact:true}).waitFor();await modal.getByRole('button',{name:'取消',exact:true}).click();report.busy_escape_guarded=true;
    }else throw new Error('unknown regression phase');
    await saveUI(page,evidence,'regression-result');
    }catch(e){await saveUI(page,evidence,'regression-failure').catch(()=>{});throw e;}
    finally{for(const release of releases)release.resolve();}
  });report.completed=true;}catch(e){report.error_type=e.name;report.error_message=redactMessage(e.message).slice(0,1000);process.exitCode=1;}
  finally{for(const release of releases)release.resolve();}
  await fs.writeFile(path.join(evidence,'regression-summary.json'),JSON.stringify(report,null,2));console.log(JSON.stringify(report,null,2));
})().catch(e=>{console.error(e.name+': regression setup failed; credentials withheld');process.exitCode=1;});
