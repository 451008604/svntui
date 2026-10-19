'use strict';
// Repeat the exact baseline fixture; its natural final 'No newline' is a late match.
const fs=require('node:fs/promises');
const path=require('node:path');
const {browserSession,redactMessage,evidenceDirectory}=require('./browser-helpers.cjs');
async function until(fn,message,timeout=30000){const end=Date.now()+timeout;while(Date.now()<end){const v=await fn();if(v)return v;await new Promise(r=>setTimeout(r,50));}throw new Error(message);}
(async()=>{
  const evidence=evidenceDirectory('svntui-web-browser');
  const report={actual_paged_large_diff:true,completed:false,modes:{}};
  try{await browserSession({url:process.env.SVNTUI_WEB_URL,evidence},async({page,assert})=>{
    page.setDefaultTimeout(30000);const root=page.locator('.workspace .diff');
    await page.getByRole('button',{name:'工作区',exact:true}).waitFor();
    const files=()=>page.getByLabel('切换差异文件',{exact:true});
    await files().selectOption('foo@');await until(async()=>(await root.locator('.diff-body').textContent()).includes('at foo'),'small initial diff');
    const started=Date.now();const pending=page.waitForResponse(r=>new URL(r.url()).pathname.endsWith('/diff')&&new URL(r.url()).searchParams.getAll('target').includes('large.txt'));
    await files().selectOption('large.txt');const observed=await pending;report.http_status=observed.status();report.network_ms=Date.now()-started;
    const response=await observed.json();report.diff_bytes=Buffer.byteLength(response.data.text);assert.equal(report.http_status,200);
    await root.getByRole('navigation',{name:'差异分页',exact:true}).waitFor();
    await until(async()=>(await root.locator('[data-display-row]').count())>0,'bounded first page');report.initial_render_ms=Date.now()-started;
    async function range(){const text=(await root.getByRole('navigation',{name:'差异分页',exact:true}).textContent()).replace(/\s+/g,' ');
      const match=text.match(/显示\s*(\d+)[–-](\d+)\s*\/\s*(\d+)\s*行.*第\s*(\d+)\s*\/\s*(\d+)\s*页/);assert.ok(match,text);
      return {start:+match[1],end:+match[2],total:+match[3],page:+match[4],pages:+match[5]};}
    async function bounded(){const rows=await root.locator('[data-display-row]').count();const lines=await root.locator('.diff-line').count();const dom=await page.evaluate(()=>document.querySelectorAll('*').length);
      assert.ok(rows>0&&rows<=1000,'at most1000 displayed rows');assert.ok(lines<=2000,'at most two sides per displayed row');assert.ok(dom<20000,'no giant hidden DOM');
      return {display_rows:rows,diff_line_nodes:lines,dom_nodes:dom};}
    const search=()=>root.getByLabel('搜索差异',{exact:true});
    for(const [mode,label] of [['unified','统一'],['split','并排']]){
      const details={};report.modes[mode]=details;await search().fill('');const switched=Date.now();await root.getByRole('button',{name:label,exact:true}).click();
      await until(async()=>(await range()).page===1&&await root.locator(mode==='split'?'.split-lines':'.diff-lines').isVisible(),'mode first page');
      details.mode_switch_ms=Date.now()-switched;details.initial=await bounded();details.first_range=await range();assert.ok(details.first_range.total>100000);
      await root.getByLabel('下一页差异',{exact:true}).click();await until(async()=>(await range()).page===2,'manual next diff page');await bounded();
      await root.getByLabel('上一页差异',{exact:true}).click();await until(async()=>(await range()).page===1,'manual previous diff page');
      const lateStart=Date.now();await search().fill('No newline at end of file');
      await until(async()=>{const match=root.locator('.diff-line.match');return await match.count()===1&&(await match.textContent()).includes('No newline at end of file')&&(await range()).page>1;},'late-file search jumps to mounted page');
      details.late_search_ms=Date.now()-lateStart;details.late_range=await range();details.late=await bounded();
      assert.equal(details.late_range.page,details.late_range.pages);assert.equal((await root.getByLabel('差异搜索匹配数',{exact:true}).textContent()).trim(),'1/1');
      const source=Number(await root.locator('.diff-line.match').getAttribute('data-line'));assert.ok(source>150000);details.late_source_line=source;
      await page.screenshot({path:path.join(evidence,mode+'-late.png')});
      const wrappedStart=Date.now();await search().fill('baseline line');
      await until(async()=>(await range()).page===1&&(await root.getByLabel('差异搜索匹配数',{exact:true}).textContent()).startsWith('1/'),'first complete-input match');
      const initialRange=await range();const firstLine=Number(await root.locator('.diff-line.match').getAttribute('data-line'));
      assert.ok((await root.locator('.diff-line.match .line-no').allTextContents()).includes('1'));
      await root.getByLabel('上一个匹配',{exact:true}).click();
      await until(async()=>(await range()).page>initialRange.page,'previous wraps to late matching page');
      assert.ok((await root.locator('.diff-line.match .line-no').allTextContents()).includes('100000'));
      details.previous_match_range=await range();await bounded();
      await root.getByLabel('下一个匹配',{exact:true}).click();await until(async()=>Number(await root.locator('.diff-line.match').getAttribute('data-line'))===firstLine&&(await range()).page===1,'next wraps to first match');
      assert.ok((await root.locator('.diff-body').textContent()).includes('Index: large.txt'),'raw metadata retained');
      details.cross_page_match_roundtrip_ms=Date.now()-wrappedStart;details.search_total=(await root.getByLabel('差异搜索匹配数',{exact:true}).textContent()).trim();
    }
    const smallStart=Date.now();await files().selectOption('foo@');await until(async()=>(await root.locator('.diff-body').textContent()).includes('at foo'),'switch back to small diff');
    report.switch_small_ms=Date.now()-smallStart;assert.equal(await root.getByRole('navigation',{name:'差异分页',exact:true}).count(),0);report.small_dom_nodes=await page.evaluate(()=>document.querySelectorAll('*').length);assert.ok(report.small_dom_nodes<5000);
    await files().selectOption('large.txt');await until(async()=>await root.getByRole('navigation',{name:'差异分页',exact:true}).isVisible()&&(await range()).page===1,'large file page clamps on switch');
    report.back_to_large=await bounded();await page.screenshot({path:path.join(evidence,'file-switch-clamped.png')});
  });report.completed=true;}catch(e){report.error_type=e.name;report.error_message=redactMessage(e.message).slice(0,1200);process.exitCode=1;}
  await fs.writeFile(path.join(evidence,'large-paged-summary.json'),JSON.stringify(report,null,2));console.log(JSON.stringify(report,null,2));
})().catch(e=>{console.error(e.name+': bounded large diff setup failed; credentials withheld');process.exitCode=1;});
