'use strict';
const fs=require('node:fs/promises');
const path=require('node:path');
const os=require('node:os');
const {browserSession,redactMessage,evidenceDirectory}=require('./browser-helpers.cjs');
async function until(fn,message,timeout=15000){const end=Date.now()+timeout;while(Date.now()<end){if(await fn())return;await new Promise(r=>setTimeout(r,50));}throw new Error(message);}
(async()=>{
  const evidence=process.env.SVNTUI_BROWSER_EVIDENCE||await fs.mkdtemp(path.join(os.tmpdir(),'svntui-web-single-hit-'));
  const report={single_hit_page_jump:true,completed:false};
  try{await browserSession({url:process.env.SVNTUI_WEB_URL,evidence},async({page,assert})=>{
    const root=page.locator('.workspace .diff');
    async function currentPage(){const text=await root.getByRole('navigation',{name:'差异分页',exact:true}).textContent();const m=text.match(/第\s*(\d+)\s*\/\s*(\d+)\s*页/);assert.ok(m);return {page:+m[1],pages:+m[2]};}
    await page.getByRole('button',{name:'工作区',exact:true}).waitFor();
    await page.getByLabel('切换差异文件',{exact:true}).selectOption('large.txt');
    await root.getByRole('navigation',{name:'差异分页',exact:true}).waitFor();
    for(const [mode,label] of [['unified','统一'],['split','并排']]){
      await root.getByLabel('搜索差异',{exact:true}).fill('');await root.getByRole('button',{name:label,exact:true}).click();
      await root.getByLabel('搜索差异',{exact:true}).fill('No newline at end of file');
      await until(async()=>{const r=await currentPage();return r.page===r.pages&&await root.locator('.diff-line.match').count()===1;},'unique late match displayed');
      assert.equal((await root.getByLabel('差异搜索匹配数',{exact:true}).textContent()).trim(),'1/1');
      const last=(await currentPage()).pages;await root.getByLabel('上一页差异',{exact:true}).click();
      await until(async()=>(await currentPage()).page===last-1,'manual page leaves the unique match');
      await root.getByLabel('下一个匹配',{exact:true}).click();
      await until(async()=>(await currentPage()).page===last&&await root.locator('.diff-line.match').count()===1,'next must restore unique match page');
      await root.getByLabel('上一页差异',{exact:true}).click();await until(async()=>(await currentPage()).page===last-1,'manual page leaves match again');
      await root.getByLabel('上一个匹配',{exact:true}).click();
      await until(async()=>(await currentPage()).page===last&&await root.locator('.diff-line.match').count()===1,'previous must restore unique match page');
      report[mode]={match_count:1,last_page:last,next_restored:true,previous_restored:true};
      await page.screenshot({path:path.join(evidence,mode+'-single-hit.png')});
    }
  });report.completed=true;}catch(e){report.error_type=e.name;report.error_message=redactMessage(e.message);process.exitCode=1;}
  await fs.writeFile(path.join(evidence,'single-hit-summary.json'),JSON.stringify(report,null,2));console.log(JSON.stringify(report,null,2));
})().catch(e=>{console.error(e.name+': single-hit setup failed; credentials withheld');process.exitCode=1;});
