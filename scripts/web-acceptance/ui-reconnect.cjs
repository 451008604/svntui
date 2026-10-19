'use strict';
const fs=require('node:fs/promises');
const path=require('node:path');
const {browserSession,saveUI,waitJob,redactMessage,evidenceDirectory}=require('./browser-helpers.cjs');
async function until(fn,message,timeout=30000){const end=Date.now()+timeout;while(Date.now()<end){if(await fn())return;await new Promise(r=>setTimeout(r,100));}throw new Error(message);}
(async()=>{
  const evidence=evidenceDirectory('svntui-web-browser');
  const report={actual_ui_reconnect:true,completed:false};
  try{await browserSession({url:process.env.SVNTUI_WEB_URL,evidence},async({page,context,assert})=>{
    try{
    await page.getByRole('button',{name:'工作区',exact:true}).waitFor();
    await until(async()=>await page.getByLabel('提交说明',{exact:true}).isEnabled(),'initial draft ready before cleanup');
    await until(async()=>(await page.locator('.rail-bottom').textContent()).includes('本地服务已连接'),'initial SSE connection');
    let snapshots=0;page.on('response',response=>{if(new URL(response.url()).pathname==='/api/v1/jobs'&&response.request().method()==='GET'&&response.status()===200)snapshots++;});
    await page.getByLabel('项目操作',{exact:true}).click();await page.getByRole('button',{name:'清理工作副本',exact:true}).click();
    const operation=page.getByRole('dialog',{name:/^清理 · /});await operation.waitFor({state:'visible'});
    const [previewResponse]=await Promise.all([page.waitForResponse(r=>r.url().endsWith('/previews')&&r.request().method()==='POST'),
      operation.getByRole('button',{name:'生成预览',exact:true}).click()]);assert.equal(previewResponse.status(),200);
    const [execution]=await Promise.all([page.waitForResponse(r=>r.url().endsWith('/operations')&&r.request().method()==='POST'),
      operation.getByRole('button',{name:'确认执行',exact:true}).click()]);assert.ok([200,202].includes(execution.status()));
    const accepted=await execution.json();assert.equal(accepted.ok,true);
    const job=await waitJob(page,accepted.data.id);assert.equal(job.state,'succeeded');
    const drawer=page.getByRole('dialog',{name:'活动与恢复',exact:true});
    await until(async()=>(await drawer.textContent()).includes('已完成'),'finished job in actual drawer');
    await saveUI(page,evidence,'connected-job');
    // Chromium offline emulation does not close an already-open SSE socket.
    // Fail the next real SSE handshake after reload, then allow its native retry.
    let blockEvents=true,failedConnections=0;
    await page.route('**/api/v1/events',async route=>{
      if(blockEvents){failedConnections++;await route.abort('connectionreset');}
      else await route.continue();
    });
    await page.reload({waitUntil:'domcontentloaded'});
    await until(async()=>failedConnections>0&&(await page.locator('.rail-bottom').textContent()).includes('事件连接恢复中'),'SSE failed handshake visible');
    await page.getByRole('button',{name:'查看活动与 SafePoints',exact:true}).click();
    await until(async()=>(await drawer.textContent()).includes('已完成'),'job snapshot available while SSE disconnected');
    await saveUI(page,evidence,'disconnected');
    const before=snapshots;blockEvents=false;
    await until(async()=>(await page.locator('.rail-bottom').textContent()).includes('本地服务已连接')&&snapshots>before,'SSE automatic retry snapshot',45000);
    const jobs=await page.evaluate(async()=>{const r=await fetch('/api/v1/jobs');return (await r.json()).data;});
    assert.equal(jobs.filter(j=>j.id===job.id).length,1);assert.equal(jobs.find(j=>j.id===job.id).state,'succeeded');
    assert.ok((await drawer.textContent()).includes('已完成'));await saveUI(page,evidence,'reconnected');
    Object.assign(report,{job_id:job.id,method:'failed SSE handshake after UI reload followed by automatic EventSource retry',failed_connections:failedConnections,snapshots_before:before,snapshots_after:snapshots,terminal_state:'succeeded'});
    }catch(e){await saveUI(page,evidence,'reconnect-failure').catch(()=>{});throw e;}
  });report.completed=true;}catch(e){report.error_type=e.name;report.error_message=redactMessage(e.message).slice(0,500);process.exitCode=1;}
  await fs.writeFile(path.join(evidence,'reconnect-summary.json'),JSON.stringify(report,null,2));console.log(JSON.stringify(report,null,2));
})().catch(e=>{console.error(e.name+': reconnect setup failed; credentials withheld');process.exitCode=1;});
