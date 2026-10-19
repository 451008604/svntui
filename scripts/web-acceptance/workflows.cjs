'use strict';
// Selectors come from current App.tsx accessible labels/classes; validate with actual probe.
const fs=require('node:fs/promises');
const path=require('node:path');
const {execFile}=require('node:child_process');
const {browserSession,saveUI,waitJob,evidenceDirectory,projectButton}=require('./browser-helpers.cjs');
const manifestPath=process.env.SVNTUI_FIXTURE_MANIFEST;
const uiNames={commit:'提交',add:'添加到版本控制',delete:'删除',move:'移动 / 重命名',revert:'还原',update:'更新',changelist:'变更分组',resolve:'解决冲突',cleanup:'清理',restore:'恢复 SafePoint'};
const acceptNames={working:'使用已整理的工作文件',base:'使用基础版本','mine-full':'使用完整本地版本','theirs-full':'使用完整远端版本'};

async function until(predicate,message,timeout=20000){
  const deadline=Date.now()+timeout; let last;
  while(Date.now()<deadline){try{const result=await predicate();if(result)return result;}catch(e){last=e;}
    await new Promise(resolve=>setTimeout(resolve,100));}
  throw new Error(message+(last?' ('+last.name+')':''));
}
function normalized(value){const result=value.replaceAll('\\','/').replace(/\/$/,'');return process.platform==='win32'?result.toLowerCase():result;}
// execFile has no input option; write exact JSON to the owned child's stdin.
async function nativeJSON(request){
  return new Promise((resolve,reject)=>{
    const child=execFile(process.env.SVNTUI_PYTHON||(process.platform==='win32'?'python':'python3'),
      [path.join(__dirname,'svn-fixture-check.py'),'--manifest',manifestPath],{windowsHide:true,maxBuffer:8*1024*1024},
      (error,stdout,stderr)=>error?reject(new Error('native check failed: '+stderr.slice(0,500))):(()=>{try{resolve(JSON.parse(stdout));}catch(e){reject(e);}})());
    child.stdin.end(JSON.stringify(request));
  });
}
async function readAPI(page,route){return page.evaluate(async route=>{
  const response=await fetch('/api/v1'+route,{credentials:'same-origin'});const payload=await response.json();
  if(!response.ok||!payload.ok)throw new Error('read failed '+response.status);
  return payload.data;
},route);}

(async()=>{
  if(!manifestPath||!process.env.SVNTUI_WEB_URL)throw new Error('fixture and server URL environment required');
  const manifest=JSON.parse(await fs.readFile(manifestPath,'utf8'));
  const mode=process.env.SVNTUI_ACCEPTANCE_MODE||manifest.mode||'full';
  const phase=process.env.SVNTUI_ACCEPTANCE_PHASE||mode;
  const evidence=evidenceDirectory('svntui-web-workflows');
  const summary={kind:'actual UI workflows with native SVN postconditions',phase,mode,fixture:manifestPath,
    selector_basis:'App.tsx plus actual mac-workspace-probe DOM',resume_at:process.env.SVNTUI_ACCEPTANCE_START_AT||null,steps:[],completed:false};
  const secretURL=new URL(process.env.SVNTUI_WEB_URL);
  const token=process.env.SVNTUI_STARTUP_TOKEN||new URLSearchParams(secretURL.hash.slice(1)).get('token')||'';
  const cleanError=e=>({name:e.name,message:String(e.message).split(token||'__no_secret__').join('[redacted]').replace(/#token=[^\s]+/g,'#token=[redacted]').slice(0,1500)});
  await fs.mkdir(evidence,{recursive:true});
  async function persist(){await fs.writeFile(path.join(evidence,'workflow-summary.json'),JSON.stringify(summary,null,2));}
  try{
    await browserSession({url:process.env.SVNTUI_WEB_URL,token:process.env.SVNTUI_STARTUP_TOKEN,evidence},async({page,context,base,assert})=>{
      let project;
      const endpoint=()=>'/projects/'+encodeURIComponent(project.id);
      const checkpointPath=process.env.SVNTUI_ACCEPTANCE_CHECKPOINT||path.join(manifest.root,'draft-checkpoint.json');
      const buttons=name=>page.getByRole('button',{name,exact:true});
      const drawer=()=>page.getByRole('dialog',{name:'活动与恢复',exact:true});
      let resumeReached=!process.env.SVNTUI_ACCEPTANCE_START_AT;
      async function step(name,fn){
        if(name===process.env.SVNTUI_ACCEPTANCE_START_AT)resumeReached=true;
        if(!resumeReached&&name!=='project-session-initial'){summary.steps.push({name,state:'not_run_in_resume'});await persist();return;}
        const record={name,state:'running'};summary.steps.push(record);await persist();
        try{record.details=await fn();record.state='passed';await saveUI(page,evidence,String(summary.steps.length).padStart(2,'0')+'-'+name);}
        catch(e){record.state='failed';record.error=cleanError(e);await saveUI(page,evidence,'failure-'+String(summary.steps.length)).catch(()=>{});throw e;}
        finally{await persist();}}
      async function closeDrawer(){if(await drawer().isVisible())await page.getByLabel('关闭活动',{exact:true}).click();}
      async function refresh(){await closeDrawer();
        const response=page.waitForResponse(r=>r.url().includes('/api/v1/projects/'+project.id+'/status')&&r.request().method()==='GET'&&r.status()===200);
        await buttons('刷新').click();await response;
        await until(async()=>!(await page.getByRole('heading',{name:'正在读取工作副本…',exact:true}).isVisible()),'workspace load');}
      async function workspace(){await closeDrawer();await buttons('工作区').click();
        await until(async()=>await page.getByLabel('提交说明',{exact:true}).isEnabled(),'draft available');
        await page.getByLabel('搜索文件',{exact:true}).fill('');await page.getByLabel('状态筛选',{exact:true}).selectOption('');}
      async function select(files){await workspace();await buttons('取消选择').click();
        for(const file of files)await page.getByRole('checkbox',{name:'选择 '+file,exact:true}).check();
        await until(async()=>JSON.stringify(await page.locator('.selected-targets code').allTextContents())===JSON.stringify(files),'exact selected paths');}
      function operationDialog(action){return page.getByRole('dialog',{name:new RegExp('^'+uiNames[action]+' · ')});}
      async function openOperation(action,options={}){
        if(options.open){await options.open();}
        else if(action==='commit')await page.getByRole('button',{name:/^预览提交/}).click();
        else if(action==='cleanup'){if(!(await page.locator('details.project-menu').evaluate(el=>el.open)))await page.getByLabel('项目操作',{exact:true}).click();await buttons('清理工作副本').click();}
        else await buttons({add:'添加',delete:'删除',move:'移动',revert:'还原',changelist:'变更分组',update:'更新所选'}[action]).click();
        const dialog=operationDialog(action);await dialog.waitFor({state:'visible'});
        if(options.path!==undefined)await dialog.getByLabel('待删除的版本化相对路径',{exact:true}).fill(options.path);
        if(options.newPath!==undefined)await dialog.getByLabel('新的相对路径',{exact:true}).fill(options.newPath);
        if(options.message!==undefined)await dialog.getByLabel('提交说明',{exact:true}).fill(options.message);
        if(options.changelist!==undefined)await dialog.getByLabel('分组名称',{exact:true}).fill(options.changelist);
        if(options.remove)await dialog.getByLabel('从当前分组移除',{exact:true}).check();
        if(options.revision!==undefined)await dialog.getByLabel('目标修订',{exact:false}).fill(String(options.revision));
        return dialog;
      }
      async function preview(dialog){const pending=page.waitForResponse(r=>r.url().endsWith('/previews')&&r.request().method()==='POST');
        await dialog.getByRole('button',{name:'生成预览',exact:true}).click();const response=await pending;const envelope=await response.json();
        assert.equal(response.status(),200,JSON.stringify(envelope.error));assert.equal(envelope.ok,true);return envelope.data;}
      async function execute(dialog,previewData,expectedState="succeeded"){
        if(previewData.risk==='high'){
          const yes=dialog.getByLabel('输入 YES 确认',{exact:true});await yes.fill('NO');assert.equal(await dialog.getByRole('button',{name:'确认执行',exact:true}).isEnabled(),false);
          await yes.fill('YES');}
        const pending=page.waitForResponse(r=>r.url().endsWith('/operations')&&r.request().method()==='POST');
        await dialog.getByRole('button',{name:'确认执行',exact:true}).click();const response=await pending;const envelope=await response.json();
        assert.ok([200,202].includes(response.status()),JSON.stringify(envelope.error));assert.equal(envelope.ok,true);
        const job=await waitJob(page,envelope.data.id,60000);assert.equal(job.state,expectedState,job.error?.message||job.output);
        await drawer().waitFor({state:'visible'});return job;
      }
      async function operate(action,options={}){const dialog=await openOperation(action,options);const frozen=await preview(dialog);
        if(options.targets)assert.deepEqual(frozen.targets,options.targets);
        const job=await execute(dialog,frozen,options.expectedState);if(!options.keepDrawer)await closeDrawer();return job;}
      async function draftSaved(targets,message){await until(async()=>{
        const draft=await readAPI(page,endpoint()+'/draft');return draft.message===message&&JSON.stringify(draft.targets)===JSON.stringify(targets)?draft:false;
      },'draft persisted');await until(async()=>(await page.getByRole('status').textContent()).includes('已保存'),'draft saved indicator');}

      await step('project-session-initial',async()=>{
        const projects=await readAPI(page,'/projects');project=projects.find(p=>normalized(p.path)===normalized(manifest.wc));
        assert.ok(project,'main WC must be registered by CLI startup');await workspace();
        assert.equal(new URL(page.url()).hash,'');assert.equal(await page.evaluate(()=>window.__svntui_injected),0);
        const cookies=await context.cookies(base);assert.ok(cookies.some(c=>c.httpOnly&&c.sameSite==='Strict'),'HttpOnly Strict session cookie');
        return {project_id:project.id,cookie_policy_checked:true};});

      if(phase==='draft-read'){
        await step('draft-after-server-restart',async()=>{
          const saved=JSON.parse(await fs.readFile(checkpointPath,'utf8'));
          assert.notEqual(new URL(base).port,new URL(saved.base).port,'restart should use a new port');
          await until(async()=>(await page.getByLabel('提交说明',{exact:true}).inputValue())===saved.message,'restored draft message');
          for(const file of saved.targets)assert.equal(await page.getByRole('checkbox',{name:'选择 '+file,exact:true}).isChecked(),true);
          const draft=await readAPI(page,endpoint()+'/draft');assert.deepEqual(draft.targets,saved.targets);assert.equal(draft.message,saved.message);
          return {old_port:new URL(saved.base).port,new_port:new URL(base).port,targets:draft.targets,revision:draft.revision};});
        return;
      }
      if(phase==='full'){
        assert.equal(manifest.mode,'full','conflict workflows need a full fixture');
        await step('conflict-types-and-text-artifacts',async()=>{
          await page.getByRole('navigation',{name:'项目视图',exact:true}).getByRole('button',{name:/^冲突处理/}).click();const conflicts=await readAPI(page,endpoint()+'/conflicts');
          assert.deepEqual(new Set(conflicts.flatMap(c=>c.kinds)),new Set(['text','property','tree']));
          await page.locator('.conflict-list button').filter({hasText:'text-conflict.txt'}).click();
          for(const label of ['BASE · 基础版本','MINE · 本地版本','THEIRS · 远端版本'])await page.getByText(label,{exact:true}).waitFor();
          assert.ok((await page.locator('.conflict-columns').textContent()).includes('local text'));
          assert.ok((await page.locator('.conflict-columns').textContent()).includes('remote text'));
          return {conflicts:conflicts.map(c=>({path:c.path,kinds:c.kinds,accepts:c.accepts}))};});
        await step('text-marker-protection-and-resolution',async()=>{
          await buttons(acceptNames.working).click();const dialog=operationDialog('resolve');
          const workingPreview=await preview(dialog);
          assert.equal(workingPreview.action,'resolve');
          const pending=page.waitForResponse(r=>r.url().endsWith('/operations')&&r.request().method()==='POST');
          await dialog.getByRole('button',{name:'确认执行',exact:true}).click();
          const response=await pending;assert.ok([200,202].includes(response.status()));
          const accepted=await response.json();const failed=await waitJob(page,accepted.data.id,60000);
          assert.equal(failed.state,'failed','working with markers must fail execution');
          assert.equal((await nativeJSON({op:'status'})).entries['text-conflict.txt'].item,'conflicted');
          await closeDrawer();
          await page.locator('.conflict-list button').filter({hasText:'text-conflict.txt'}).click();
          const job=await operate('resolve',{open:()=>buttons(acceptNames['mine-full']).click()});
          assert.equal((await nativeJSON({op:'read',path:'text-conflict.txt'})).text,'local text\n');
          assert.notEqual((await nativeJSON({op:'status'})).entries['text-conflict.txt']?.item,'conflicted');return {job_id:job.id,marker_working_job:failed.id,marker_working_state:failed.state,marker_error_code:failed.error?.code};});
        for(const kind of ['property','tree'])await step(kind+'-working-resolution',async()=>{
          const file=manifest.conflicts[kind];await page.locator('.conflict-list button').filter({hasText:file}).click();
          assert.equal(await buttons(acceptNames['mine-full']).count(),0);assert.equal(await buttons(acceptNames['theirs-full']).count(),0);
          if(kind==='property')await nativeJSON({op:'propset',path:file,name:'fixture:color',value:'external-prepared'});
          else await nativeJSON({op:'write',path:file,text:'tree reconciled externally\n'});
          const job=await operate('resolve',{open:()=>buttons(acceptNames.working).click()});
          const status=(await nativeJSON({op:'status'})).entries[file]||{};
          assert.notEqual(status.props,'conflicted');assert.notEqual(status['tree-conflicted'],'true');
          if(kind==='property')assert.equal((await nativeJSON({op:'propget',path:file,name:'fixture:color'})).value,'external-prepared');
          else assert.equal((await nativeJSON({op:'read',path:file})).text,'tree reconciled externally\n');
          return {job_id:job.id};});
        await step('no-conflicts-remain',async()=>{await refresh();await page.getByRole('navigation',{name:'项目视图',exact:true}).getByRole('button',{name:/^冲突处理/}).click();
          await page.getByRole('heading',{name:'没有未解决冲突',exact:true}).waitFor();assert.equal((await readAPI(page,endpoint()+'/conflicts')).length,0);});
        return;
      }
      assert.equal(manifest.mode,'basic','daily workflows require a basic fixture');
      const commitTarget=manifest.commit_target;
      assert.ok(commitTarget,'regenerate fixture for nested special-path commit target');
      await step('filters-selection-diff-and-hostile-literal',async()=>{
        await workspace();await page.getByLabel('搜索文件',{exact:true}).fill('foo');
        await buttons('选择可见项').click();const selected=await page.locator('.selected-targets code').allTextContents();
        assert.ok(selected.includes('foo')&&selected.includes('foo@')&&selected.includes(commitTarget));
        await page.getByLabel('状态筛选',{exact:true}).selectOption('modified');
        const visible=await page.locator('.changes-table tbody .path-cell span').allTextContents();assert.ok(visible.every(p=>p.includes('foo')));
        await buttons('取消选择').click();await page.getByLabel('状态筛选',{exact:true}).selectOption('');await page.getByLabel('搜索文件',{exact:true}).fill('');
        const row=page.getByRole('row').filter({has:page.getByRole('checkbox',{name:'选择 foo@',exact:true})});await row.click();
        const diff=page.locator('.workspace .diff');await until(async()=>(await diff.locator('.diff-body').textContent()).includes('local modification'),'focused diff');
        await diff.getByRole('button',{name:'并排',exact:true}).click();await diff.locator('.split-lines').waitFor();
        await diff.getByLabel('搜索差异',{exact:true}).fill('local modification');await diff.locator('mark').first().waitFor();
        await diff.getByLabel('下一个匹配',{exact:true}).click();await diff.getByRole('button',{name:'统一',exact:true}).click();
        if(manifest.markup_path)assert.ok((await page.locator('.path-cell span').allTextContents()).includes(manifest.markup_path));
        assert.equal(await page.locator('img[src="x"]').count(),0);assert.equal(await page.evaluate(()=>window.__svntui_injected),0);
        return {filtered_paths:visible,markup_literal:!!manifest.markup_path};});
      const message='Web UI selected nested @ 中文 <img src=x onerror=window.__svntui_injected=1>';
      await step('draft-save-and-reload',async()=>{
        await select([commitTarget]);await page.getByLabel('提交说明',{exact:true}).fill(message);await draftSaved([commitTarget],message);
        await page.reload({waitUntil:'domcontentloaded'});await workspace();
        assert.equal(await page.getByLabel('提交说明',{exact:true}).inputValue(),message);
        assert.equal(await page.getByRole('checkbox',{name:'选择 '+commitTarget,exact:true}).isChecked(),true);
        const draft=await readAPI(page,endpoint()+'/draft');assert.equal(draft.message,message);return {targets:draft.targets,revision:draft.revision};});
      if(phase==='draft-write'){
        await fs.writeFile(checkpointPath,JSON.stringify({base,targets:[commitTarget],message},null,2));summary.checkpoint=checkpointPath;return;
      }
      await step('selected-special-nested-commit-only',async()=>{
        const before=await nativeJSON({op:'status'});const content=await nativeJSON({op:'read',path:commitTarget});
        const job=await operate('commit',{targets:[commitTarget]});const after=await nativeJSON({op:'status'});const log=await nativeJSON({op:'log'});
        assert.equal(after.head,before.head+1);assert.deepEqual(log.paths.map(p=>p.path),['/'+commitTarget]);assert.equal(log.message,message);
        assert.equal((await nativeJSON({op:'cat',path:commitTarget})).sha256,content.sha256);assert.equal(after.entries['unselected.txt'].item,'modified');
        assert.equal(after.entries['nested/second.txt'].item,'modified');assert.ok(!after.entries[commitTarget]);
        await until(async()=>{const d=await readAPI(page,endpoint()+'/draft');return d.message===''&&d.targets.length===0;},'successful commit clears draft');
        await until(async()=>(await page.getByLabel('提交说明',{exact:true}).inputValue())==='','UI draft clears');
        return {job_id:job.id,revision:after.head,changed_paths:log.paths};});
      await step('failed-commit-preserves-draft-and-content',async()=>{
        const message='失败提交应保留草稿 中文';const original=await nativeJSON({op:'read',path:'selected.txt'});
        const peer=await nativeJSON({op:'peer-change',path:'selected.txt',text:'peer out of date commit barrier\n',message:'browser peer commit-failure barrier'});
        await select(['selected.txt']);await page.getByLabel('提交说明',{exact:true}).fill(message);await draftSaved(['selected.txt'],message);
        const job=await operate('commit',{expectedState:'failed',targets:['selected.txt']});
        assert.equal((await nativeJSON({op:'status'})).head,peer.head);assert.equal((await nativeJSON({op:'read',path:'selected.txt'})).sha256,original.sha256);
        const draft=await readAPI(page,endpoint()+'/draft');assert.equal(draft.message,message);assert.deepEqual(draft.targets,['selected.txt']);
        assert.equal(await page.getByLabel('提交说明',{exact:true}).inputValue(),message);
        await select(['selected.txt']);await operate('revert');await select([]);await page.getByLabel('提交说明',{exact:true}).fill('');await draftSaved([],'');
        return {failed_job_id:job.id,draft_preserved:true,content_preserved:true,head:peer.head};});
      await step('project-register-remove-keeps-wc',async()=>{
        await page.getByLabel('添加项目',{exact:true}).click();const dialog=page.getByRole('dialog',{name:'添加本地工作副本',exact:true});
        await dialog.getByLabel('工作副本路径',{exact:true}).fill(manifest.peer);await dialog.getByLabel('项目名称（可选）',{exact:true}).fill('浏览器 Peer');
        await dialog.getByRole('button',{name:'验证并添加',exact:true}).click();await until(async()=>!(await dialog.isVisible()),'project added');
        await until(async()=>(await page.locator('.project-identity strong').textContent())==='浏览器 Peer','peer selected');
        if(!(await page.locator('details.project-menu').evaluate(el=>el.open)))await page.getByLabel('项目操作',{exact:true}).click();await buttons('移除项目登记').click();
        await page.getByRole('dialog',{name:'移除项目登记',exact:true}).getByRole('button',{name:'移除登记',exact:true}).click();
        const remaining=await until(async()=>{const list=await readAPI(page,'/projects');return list.every(p=>normalized(p.path)!==normalized(manifest.peer))&&list;},'peer registration removed');
        project=remaining.find(p=>normalized(p.path)===normalized(manifest.wc));assert.ok(project,'main WC still registered');
        await until(async()=>await page.locator('nav.projects button').count()===remaining.length,'post-removal project DOM');
        assert.equal((await fs.stat(path.join(manifest.peer,'.svn'))).isDirectory(),true);
        await projectButton(page,project).click();
        await until(async()=>(await page.locator('.project-identity .path').getAttribute('title'))===project.path,'remaining project selected');
        await workspace();return {peer_retained:true};});
      await step('changelist-add-and-remove',async()=>{
        await select(['unselected.txt']);await operate('changelist',{changelist:'Web UI 分组'});
        assert.ok((await nativeJSON({op:'status'})).changelists['Web UI 分组'].includes('unselected.txt'));
        await select(['unselected.txt']);await operate('changelist',{remove:true});
        assert.ok(!Object.values((await nativeJSON({op:'status'})).changelists).flat().includes('unselected.txt'));});
      await step('unversioned-add',async()=>{
        await select(['untracked.txt']);const job=await operate('add');assert.equal((await nativeJSON({op:'status'})).entries['untracked.txt'].item,'added');return {job_id:job.id};});
      await step('move-special-name',async()=>{
        await nativeJSON({op:'append',path:'move-me.txt',text:'move UI setup\n'});await refresh();await select(['move-me.txt']);
        const job=await operate('move',{newPath:'moved 中文@ file.txt'});const s=await nativeJSON({op:'status'});
        assert.equal(s.entries['move-me.txt'].item,'deleted');assert.equal(s.entries['moved 中文@ file.txt'].item,'added');return {job_id:job.id};});
      await step('modified-delete-protection',async()=>{
        await nativeJSON({op:'append',path:'delete-me.txt',text:'delete UI setup\n'});await refresh();await select(['delete-me.txt']);
        const before=await nativeJSON({op:'read',path:'delete-me.txt'});const dialog=await openOperation('delete');const frozen=await preview(dialog);
        assert.equal(frozen.risk,'high');await dialog.getByLabel('输入 YES 确认',{exact:true}).fill('YES');
        const pending=page.waitForResponse(r=>r.url().endsWith('/operations')&&r.request().method()==='POST');
        await dialog.getByRole('button',{name:'确认执行',exact:true}).click();const response=await pending;assert.ok([200,202].includes(response.status()));
        const accepted=await response.json();const job=await waitJob(page,accepted.data.id,60000);assert.equal(job.state,'failed');
        assert.equal((await nativeJSON({op:'read',path:'delete-me.txt'})).sha256,before.sha256);assert.equal((await nativeJSON({op:'status'})).entries['delete-me.txt'].item,'modified');
        await closeDrawer();return {job_id:job.id,content_preserved:true};});
      await step('clean-versioned-path-delete',async()=>{
        if(!(await page.locator('details.project-menu').evaluate(el=>el.open)))await page.getByLabel('项目操作',{exact:true}).click();
        const job=await operate('delete',{open:()=>buttons('删除版本化路径…').click(),path:'delete-clean.txt'});
        assert.equal((await nativeJSON({op:'status'})).entries['delete-clean.txt'].item,'deleted');return {job_id:job.id};});
      await step('stale-preview-through-ui',async()=>{
        await select(['unselected.txt']);const dialog=await openOperation('revert');const p=await preview(dialog);
        assert.equal(p.risk,'high');await nativeJSON({op:'append',path:'unselected.txt',text:'changed after preview\n'});
        const original=await nativeJSON({op:'read',path:'unselected.txt'});await dialog.getByLabel('输入 YES 确认',{exact:true}).fill('YES');
        const pending=page.waitForResponse(r=>r.url().endsWith('/operations')&&r.request().method()==='POST');await dialog.getByRole('button',{name:'确认执行',exact:true}).click();
        const response=await pending;assert.equal(response.status(),409);await dialog.getByRole('button',{name:'生成预览',exact:true}).waitFor();
        assert.equal((await nativeJSON({op:'read',path:'unselected.txt'})).sha256,original.sha256);
        await dialog.getByRole('button',{name:'取消',exact:true}).click();return {http_status:409,content_preserved:true};});
      let revertedBefore,revertJob;
      await step('revert-creates-safepoint',async()=>{
        revertedBefore=await nativeJSON({op:'read',path:'revert-me.txt'});await select(['revert-me.txt']);revertJob=await operate('revert',{keepDrawer:true});
        assert.ok(revertJob.safepoint_id);assert.equal((await nativeJSON({op:'read',path:'revert-me.txt'})).text,'revert baseline\n');return {job_id:revertJob.id,safepoint_id:revertJob.safepoint_id};});
      await step('safepoint-list-inspect-and-restore',async()=>{
        await drawer().getByRole('button',{name:'SafePoints',exact:true}).click();
        await drawer().locator('.safe-item').filter({hasText:revertJob.safepoint_id}).click();await drawer().getByRole('heading',{name:'保护点清单',exact:true}).waitFor();
        assert.ok((await drawer().locator('.manifest').textContent()).includes('revert-me.txt'));
        const job=await operate('restore',{open:()=>drawer().getByRole('button',{name:'预览恢复此 SafePoint',exact:true}).click(),keepDrawer:true});
        assert.equal((await nativeJSON({op:'read',path:'revert-me.txt'})).sha256,revertedBefore.sha256);
        await drawer().getByRole('button',{name:'操作审计',exact:true}).click();await until(async()=>(await drawer().locator('.job-card').count())>0,'audit records');
        await closeDrawer();return {job_id:job.id,restored_sha256:revertedBefore.sha256};});
      await step('selected-update-to-explicit-revision',async()=>{
        const baseline=Array.from({length:20},(_,i)=>`update line ${String(i+1).padStart(2,'0')} baseline\n`);
        const local=[...baseline];local[0]='update local first\n';const remote=[...baseline];remote[19]='update remote last\n';
        await nativeJSON({op:'write',path:'update-target.txt',text:local.join('')});
        const peer=await nativeJSON({op:'peer-change',path:'update-target.txt',text:remote.join(''),message:'browser peer explicit revision update'});
        await refresh();await select(['update-target.txt']);const job=await operate('update',{revision:peer.head});
        const text=(await nativeJSON({op:'read',path:'update-target.txt'})).text;assert.ok(text.startsWith('update local first\n'));assert.ok(text.endsWith('update remote last\n'));
        assert.notEqual((await nativeJSON({op:'status'})).entries['update-target.txt'].item,'conflicted');return {job_id:job.id,requested_revision:peer.head};});
      await step('whole-workspace-update-head',async()=>{
        const peer=await nativeJSON({op:'peer-change',path:'history.txt',text:'history remote final\n',message:'browser peer HEAD update'});
        await workspace();const job=await operate('update',{open:()=>buttons('更新工作副本').click()});
        assert.equal((await nativeJSON({op:'read',path:'history.txt'})).text,'history remote final\n');return {job_id:job.id,head:peer.head};});
      await step('cleanup-workspace',async()=>{const job=await operate('cleanup');return {job_id:job.id};});
      await step('history-page-two-and-revision-diff',async()=>{
        await buttons('提交历史').click();await page.locator('.history-entry').first().waitFor();
        const first=await page.locator('.history-entry .revision').allTextContents();assert.equal(first.length,50);
        assert.ok((await page.locator('.history-entry p').allTextContents()).some(m=>m.includes('<img src=x onerror=window.__svntui_injected=1>')));
        assert.equal(await page.locator('img[src="x"]').count(),0);
        await buttons('下一页').click();await page.getByText('第 2 页',{exact:true}).waitFor();
        await until(async()=>{const next=await page.locator('.history-entry .revision').allTextContents();return next.length>0&&!next.some(r=>first.includes(r));},'history second cursor page');
        await page.locator('.history-entry').first().click();await until(async()=>(await page.locator('.history-detail .diff-body').textContent()).includes('history revision'),'revision diff loaded');
        return {first_page_count:first.length,second_page_count:await page.locator('.history-entry').count()};});
      await step('history-author-text-target-and-dates',async()=>{
        const log=await nativeJSON({op:'log',revision:120});const day=new Date(log.date);const since=new Date(day.getTime()-86400000).toISOString().slice(0,10);const untilDay=new Date(day.getTime()+86400000).toISOString().slice(0,10);
        await page.getByLabel('工作副本相对路径',{exact:true}).fill('history.txt');await page.getByLabel('作者',{exact:true}).fill('history-0');
        await page.getByLabel('提交说明包含',{exact:true}).fill('needle 中文');await page.getByLabel('开始日期',{exact:true}).fill(since);await page.getByLabel('结束日期',{exact:true}).fill(untilDay);
        const pending=page.waitForResponse(r=>r.url().includes('/history?')&&r.status()===200);await buttons('应用筛选').click();await pending;
        await page.getByText('第 1 页',{exact:true}).waitFor();await page.locator('.history-entry').first().waitFor();
        for(const entry of await page.locator('.history-entry').all()){const text=await entry.textContent();assert.ok(text.includes('history-0')&&text.includes('needle 中文'));}
        return {since,until:untilDay,matched:await page.locator('.history-entry').count()};});
    });
    summary.completed=true;
  }catch(e){summary.error=cleanError(e);process.exitCode=1;}
  finally{await persist();console.log(JSON.stringify({phase,mode,completed:summary.completed,steps:summary.steps.map(s=>({name:s.name,state:s.state})),error:summary.error},null,2));}
})().catch(e=>{console.error(e.name+': workflow startup failed; credentials withheld');process.exitCode=1;});
