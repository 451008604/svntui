'use strict';
// API-only assertions. These are never counted as UI operation acceptance.
const fs=require('node:fs/promises');
const path=require('node:path');
const http=require('node:http');
const {execFile}=require('node:child_process');
const {browserSession,waitJob,evidenceDirectory}=require('./browser-helpers.cjs');
const manifestPath=process.env.SVNTUI_FIXTURE_MANIFEST;
function native(request){return new Promise((resolve,reject)=>{
  const child=execFile(process.env.SVNTUI_PYTHON||(process.platform==='win32'?'python':'python3'),
    [path.join(__dirname,'svn-fixture-check.py'),'--manifest',manifestPath],{windowsHide:true,maxBuffer:8*1024*1024},
    (error,stdout)=>error?reject(new Error('native verification failed')):(()=>{try{resolve(JSON.parse(stdout));}catch(e){reject(e);}})());
  child.stdin.end(JSON.stringify(request));});}
function normalize(v){v=v.replaceAll('\\','/').replace(/\/$/,'');return process.platform==='win32'?v.toLowerCase():v;}
(async()=>{
  if(!manifestPath||!process.env.SVNTUI_WEB_URL)throw new Error('fixture/server environment required');
  const manifest=JSON.parse(await fs.readFile(manifestPath,'utf8'));
  const evidence=evidenceDirectory('svntui-web-protocol');
  await fs.mkdir(evidence,{recursive:true});
  const report={api_protocol_only:true,ui_operations_executed:false,fixture:manifestPath,checks:[],completed:false};
  async function save(){await fs.writeFile(path.join(evidence,'protocol-summary.json'),JSON.stringify(report,null,2));}
  try{
    await browserSession({url:process.env.SVNTUI_WEB_URL,token:process.env.SVNTUI_STARTUP_TOKEN,evidence},async({page,context,browser,base,assert})=>{
      const csrf=await page.evaluate(async()=>{const r=await fetch('/api/v1/session');return (await r.json()).data.csrf_token;});
      async function call(route,{method='GET',body,csrfValue=csrf}={}){
        return page.evaluate(async({route,method,body,csrfValue})=>{
          const headers={'Content-Type':'application/json'};if(csrfValue)headers['X-CSRF-Token']=csrfValue;
          const r=await fetch('/api/v1'+route,{method,headers,credentials:'same-origin',body:body===undefined?undefined:JSON.stringify(body)});
          return {status:r.status,payload:await r.json()};
        },{route,method,body,csrfValue});}
      async function check(name,fn){const record={name,state:'running'};report.checks.push(record);await save();
        try{record.details=await fn();record.state='passed';}catch(e){record.state='failed';record.error_type=e.name;throw e;}finally{await save();}}
      const project=(await call('/projects')).payload.data.find(p=>normalize(p.path)===normalize(manifest.wc));assert.ok(project);
      const prefix='/projects/'+encodeURIComponent(project.id);
      async function preview(body){const r=await call(prefix+'/previews',{method:'POST',body});assert.equal(r.status,200);assert.equal(r.payload.ok,true);return r.payload.data;}
      async function execute(p,confirmation){return call(prefix+'/operations',{method:'POST',body:{preview_id:p.id,confirm:true,...(confirmation===undefined?{}:{confirmation})}});}
      await check('session-single-use-cookie-and-unauthenticated',async()=>{
        assert.equal(new URL(page.url()).hash,'');const cookies=await context.cookies(base);assert.ok(cookies.some(c=>c.httpOnly&&c.sameSite==='Strict'));
        const fresh=await browser.newContext();try{assert.equal((await fresh.request.get(new URL('/api/v1/session',base).toString())).status(),401);}finally{await fresh.close();}
        const initial=new URL(process.env.SVNTUI_WEB_URL);const token=process.env.SVNTUI_STARTUP_TOKEN||new URLSearchParams(initial.hash.slice(1)).get('token');
        assert.ok(token,'startup token required to verify replay rejection');const replay=await call('/session',{method:'POST',body:{token}});assert.equal(replay.status,401);
        return {fragment_removed:true,http_only:true,same_site:'Strict',unauthenticated_status:401,replay_status:401};});
      await check('csrf-and-origin-host-rejection',async()=>{
        const body={action:'changelist',targets:['selected.txt'],changelist:'must-not-apply'};
        assert.equal((await call(prefix+'/previews',{method:'POST',body,csrfValue:''})).status,403);
        assert.equal((await call(prefix+'/previews',{method:'POST',body,csrfValue:'wrong-csrf'})).status,403);
        assert.equal((await context.request.get(new URL('/api/v1/projects',base).toString(),{headers:{Origin:'https://attacker.invalid'}})).status(),403);
        const address=new URL(base);const hostStatus=await new Promise((resolve,reject)=>{const req=http.request({hostname:address.hostname,port:address.port,path:'/api/v1/session',headers:{Host:'attacker.invalid',Origin:address.origin}},res=>{res.resume();res.on('end',()=>resolve(res.statusCode));});req.on('error',reject);req.end();});
        assert.equal(hostStatus,403);return {missing_csrf:403,wrong_csrf:403,foreign_origin:403,foreign_host:403};});
      await check('path-containment-and-input-allowlist',async()=>{
        const targets=['../outside/secret.txt','.svn/wc.db',...(manifest.symlink.available?['escape-link/secret.txt']:[])];
        for(const target of targets){const r=await call(prefix+'/diff?target='+encodeURIComponent(target));assert.ok([400,403].includes(r.status),target);assert.equal(r.payload.ok,false);}
        assert.equal((await call(prefix+'/previews',{method:'POST',body:{action:'shell',targets:[]}})).status,400);
        assert.equal((await call(prefix+'/previews',{method:'POST',body:{action:'cleanup',targets:[],command:'echo forbidden'}})).status,400);
        return {denied_targets:targets,unknown_action:400,unknown_field:400};});
      if(manifest.large_bytes>1048576){await check('large-diff-output-contract',async()=>{
        const expected=await native({op:'diff-bytes',path:'large.txt'});
        const response=await call(prefix+'/diff?target=large.txt');
        if(expected.bytes>8*1024*1024){
          assert.equal(response.status,413);assert.equal(response.payload.ok,false);assert.equal(response.payload.error?.code,'output_limit');
          assert.match(response.payload.error.message,/limit|trunc|过大|超过|截断/i);
          return {status:response.status,code:response.payload.error.code,native_diff_bytes:expected.bytes};
        }
        assert.equal(response.status,200);assert.equal(response.payload.ok,true);
        const bytes=Buffer.byteLength(response.payload.data.text);assert.ok(bytes>1048576&&bytes<=8*1048576);
        return {status:200,diff_bytes:bytes,within_shared_svn_bound:8*1048576};});}
      if(manifest.mode==='full')return;
      await check('stale-content-preview-rejected',async()=>{
        const p=await preview({action:'revert',targets:['selected.txt']});await native({op:'append',path:'selected.txt',text:'protocol stale content\n'});
        const before=await native({op:'read',path:'selected.txt'});assert.equal((await execute(p,'YES')).status,409);
        assert.equal((await native({op:'read',path:'selected.txt'})).sha256,before.sha256);return {status:409,content_preserved:true};});
      await check('stale-property-preview-rejected',async()=>{
        const p=await preview({action:'revert',targets:['selected.txt']});await native({op:'propset',path:'selected.txt',name:'fixture:protocol',value:'after-preview'});
        assert.equal((await execute(p,'YES')).status,409);assert.equal((await native({op:'propget',path:'selected.txt',name:'fixture:protocol'})).value,'after-preview');return {status:409,property_preserved:true};});
      await check('high-risk-explicit-yes-required',async()=>{
        const p=await preview({action:'revert',targets:['selected.txt']});assert.equal(p.risk,'high');const before=await native({op:'read',path:'selected.txt'});
        assert.equal((await execute(p)).status,400);assert.equal((await execute(p,'NO')).status,400);
        assert.equal((await native({op:'read',path:'selected.txt'})).sha256,before.sha256);return {missing:400,wrong:400,content_preserved:true};});
      await check('duplicate-preview-execution-one-job-one-commit',async()=>{
        const before=await native({op:'status'});const p=await preview({action:'commit',targets:['selected.txt'],message:'protocol exactly once 中文 @'});
        const results=await Promise.all([execute(p),execute(p)]);
        for(const result of results)assert.ok([200,202].includes(result.status));
        assert.equal(results[0].payload.data.id,results[1].payload.data.id);const id=results[0].payload.data.id;
        const job=await waitJob(page,id,60000);assert.equal(job.state,'succeeded',job.error?.message||job.output);
        const repeated=await execute(p);assert.equal(repeated.payload.data.id,id);const after=await native({op:'status'});const log=await native({op:'log'});
        assert.equal(after.head,before.head+1);assert.deepEqual(log.paths.map(p=>p.path),['/selected.txt']);
        const jobs=(await call('/jobs')).payload.data;assert.equal(jobs.filter(j=>j.id===id).length,1);
        return {job_id:id,head_before:before.head,head_after:after.head,changed_paths:log.paths};});
    });
    report.completed=true;
  }catch(e){report.error_type=e.name;process.exitCode=1;}
  finally{await save();console.log(JSON.stringify(report,null,2));}
})().catch(e=>{console.error(e.name+': protocol setup failed; credentials withheld');process.exitCode=1;});
