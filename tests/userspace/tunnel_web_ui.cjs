'use strict';
// Browser rendering and UI contract acceptance. Controller UI talks to a real
// disposable controller process. Windows UI uses an isolated Wails API fixture;
// native Windows tests separately validate DPAPI, launch input and installer.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const http = require('node:http');
const net = require('node:net');
const crypto = require('node:crypto');
const {spawn} = require('node:child_process');
const {chromium} = require('playwright');
const args = process.argv.slice(2);
const option = name => {const index=args.indexOf(name);if(index<0||!args[index+1])throw new Error('missing '+name);return args[index+1];};
const out=path.resolve(option('--output'));
const controller=path.resolve(option('--controller'));
const root=path.resolve(__dirname,'../..');
const version=fs.readFileSync(path.join(root,'macos/VERSION'),'utf8').trim();
const key='ab'.repeat(32);
const config={schema_version:1,kind:'shared_mpx4_tunnel',tunnel:{transport_key:key,scheduler_mode:'aggregate',relays:Array.from({length:6},(_,i)=>({host:'192.0.2.'+(i+1),port:24001}))},inlets:[{id:'hkbn',name:'HKBN',listen_port:1081,route_id:1},{id:'hkt',name:'HKT',listen_port:1082,route_id:2,uot_enabled:true},{id:'icable',name:'iCable',listen_port:1083,route_id:3}]};
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
async function port(){const s=net.createServer();await new Promise(r=>s.listen(0,'127.0.0.1',r));const n=s.address().port;await new Promise(r=>s.close(r));return n;}
async function main(){
  fs.mkdirSync(out,{recursive:true});
  const temporary=fs.mkdtempSync(path.join(os.tmpdir(),'tunnel-ui-'));
  const n=await port(),base='http://127.0.0.1:'+n;
  const password=crypto.randomBytes(24).toString('hex');
  const log=fs.openSync(path.join(out,'controller.log'),'w');
  const child=spawn(controller,[],{env:{...process.env,MPX_PROVISION_LISTEN:'127.0.0.1:'+n,MPX_PROVISION_PUBLIC_BASE_URL:base,MPX_PROVISION_DATA:path.join(temporary,'tunnels.json'),MPX_PROVISION_ADMIN_USER:'admin',MPX_PROVISION_ADMIN_PASSWORD:password},stdio:['ignore',log,log]});
  let browser,server;
  const errors=[];
  try {
    let ready=false;
    for(let i=0;i<100;i++){if(child.exitCode!==null)throw new Error('controller exited before readiness');try{const r=await fetch(base+'/healthz');if(r.ok){ready=true;break;}}catch{}await sleep(50);}
    assert(ready,'controller readiness');
    browser=await chromium.launch({headless:true});
    const context=await browser.newContext({viewport:{width:1280,height:1000},httpCredentials:{username:'admin',password}});
    const page=await context.newPage();page.on('pageerror',e=>errors.push(String(e)));
    await page.goto(base+'/admin');
    await page.waitForFunction(()=>document.getElementById('version').textContent.includes('API 4'));
    await page.locator('#newTunnel').click();
    await page.locator('#tName').fill('Shared tunnel acceptance');
    await page.locator('details').evaluate(n=>n.open=true);
    await page.locator('#jsonEditor').fill(JSON.stringify(config));
    await page.locator('#loadJSON').click();
    assert.equal(await page.locator('#relayRows tr').count(),6);
    assert.equal(await page.locator('#inletRows tr').count(),3);
    assert.equal(await page.locator('#tKey').getAttribute('type'),'password');
    const created=page.waitForResponse(r=>r.url().endsWith('/admin/api/tunnels')&&r.request().method()==='POST');
    await page.locator('#saveTunnel').click();
    const response=await created;assert.equal(response.status(),201);
    const record=await response.json();
    await page.locator('#editor').waitFor({state:'hidden'});
    await page.screenshot({path:path.join(out,'controller-tunnels.png'),fullPage:true});
    const envelope=await context.request.get(record.api_url);assert.equal(envelope.status(),200);
    const raw=await envelope.text();assert(!raw.includes(key)&&!raw.includes('HKBN'),'encrypted public response');
    await page.locator('nav button[data-tab="devices"]').click();
    await page.locator('#newDevice').click();await page.locator('#dName').fill('Acceptance Windows');
    await page.locator('#dTunnel').selectOption(record.id);
    await page.locator('#deviceForm button[type="submit"]').click();
    await page.locator('#secretDialog[open]').waitFor();
    assert((await page.locator('#secretValue').inputValue()).length>8,'pairing code');
    await page.locator('#closeSecret').click();assert.equal(await page.locator('#secretValue').inputValue(),'');
    await page.screenshot({path:path.join(out,'controller-devices.png'),fullPage:true});
    await page.locator('nav button[data-tab="settings"]').click();
    await page.screenshot({path:path.join(out,'controller-system.png'),fullPage:true});
    assert.equal(await page.evaluate(()=>localStorage.length),0,'no secrets in localStorage');
    await context.close();

    const fixtureConfig=structuredClone(config);fixtureConfig.tunnel.transport_key='';
    const fixture={version:version,source_id:'synthetic-ui-fixture',status:'已停止',running:false,busy:false,problem:'',settings:{configuration_source:'local',tunnel:fixtureConfig,transport_key_set:true,background_resident:false,automatic_updates:false,remote_enabled:false,remote_server:''},managed:{kind:'tunnel_profile',display_name:'Shared tunnel acceptance',status:'共享隧道已同步',relay_count:6,selected_ids:['hkbn','hkt','icable'],profiles:config.inlets.map(i=>({...i,relay_count:6}))},event:{paths:6,connections:18,sent:44<<20,received:120<<20,path_stats:config.tunnel.relays.map((r,i)=>({id:i+1,address:r.host+':'+r.port,connected:true,rtt_ms:12+i,goodput_bps:1e7,outstanding_bytes:65536,queue_bytes:32768})),routes:config.inlets.map(i=>({route_id:i.route_id,weight:1,streams:6,ready_data_bytes:32768,receive_allocated_bytes:81920,receive_limit_bytes:64<<20,receive_pressure_resets:0})),inlets:config.inlets.map(i=>({id:i.id,route_id:i.route_id,listen_port:i.listen_port,active_connections:6,udp_connections:i.id==='hkt'?2:0,udp_dropped:0}))},remote:{status:'远程管理未启用'},update:{status:'尚未检查',available:false},logs:['合成验收数据；没有访问用户设备或生产服务']};
    server=http.createServer((req,res)=>{
      const name=({'/':'index.html','/index.html':'index.html','/app.js':'app.js','/app.css':'app.css'})[req.url];
      if(!name){res.writeHead(404);res.end();return;}
      res.setHeader('Content-Type',name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':'text/html');
      res.end(fs.readFileSync(path.join(root,'windows/frontend/dist',name)));
    });
    await new Promise(r=>server.listen(0,'127.0.0.1',r));
    const windowsContext=await browser.newContext({viewport:{width:1280,height:1000}});
    await windowsContext.addInitScript(s=>{
      window.__calls=[];window.__fixture=s;
      const state=()=>structuredClone(window.__fixture);
      const api=new Proxy({}, {get:(_,method)=>async(...args)=>{
        window.__calls.push({method,args});
        if(method==='GetState')return state();
        if(method==='SetConfigurationSource')window.__fixture.settings.configuration_source=args[0];
        if(method==='SetManagedSelection')window.__fixture.managed.selected_ids=args[0];
        if(method==='SaveTunnelConfig'){
          if(args[0].kind!=='shared_mpx4_tunnel')throw Error('legacy configuration');
          window.__fixture.settings.tunnel=structuredClone(args[0]);window.__fixture.settings.tunnel.tunnel.transport_key='';
        }
        if(method==='Start'){window.__fixture.running=true;window.__fixture.status='运行中';}
        if(method==='Stop'){window.__fixture.running=false;window.__fixture.status='已停止';}
        return null;
      }});
      window.go={main:{App:api}};window.runtime={EventsOn:()=>{}};
    },fixture);
    const windowsPage=await windowsContext.newPage();windowsPage.on('pageerror',e=>errors.push(String(e)));
    await windowsPage.goto('http://127.0.0.1:'+server.address().port+'/');
    await windowsPage.waitForFunction(()=>document.querySelectorAll('#relayRows tr').length===6);
    assert.equal(await windowsPage.locator('#inletRows tr').count(),3);
    assert.equal(await windowsPage.locator('#transportKey').inputValue(),'');
    await windowsPage.screenshot({path:path.join(out,'windows-local.png'),fullPage:true});
    await windowsPage.locator('#saveLocal').click();
    await windowsPage.waitForFunction(()=>window.__calls.some(c=>c.method==='SaveTunnelConfig'));
    const saved=await windowsPage.evaluate(()=>window.__calls.find(c=>c.method==='SaveTunnelConfig').args[0]);
    assert.equal(saved.tunnel.relays.length,6);assert.equal(saved.inlets.length,3);
    assert.equal(saved.kind,'shared_mpx4_tunnel');assert(!('profiles' in saved));
    await windowsPage.locator('#startStop').click();
    await windowsPage.waitForFunction(()=>document.getElementById('startStop').textContent==='停止隧道');
    assert(await windowsPage.locator('#addRelay').isDisabled());
    await windowsPage.locator('#startStop').click();
    await windowsPage.locator('#source').selectOption('managed');
    await windowsPage.waitForFunction(()=>!document.getElementById('managedPanel').classList.contains('hidden'));
    await windowsPage.locator('.inlet-choice[value="icable"]').uncheck();
    await windowsPage.locator('#saveSelection').click();
    const chosen=await windowsPage.evaluate(()=>window.__calls.filter(c=>c.method==='SetManagedSelection').at(-1).args[0]);
    assert.deepEqual(chosen,['hkbn','hkt']);
    await windowsPage.screenshot({path:path.join(out,'windows-managed.png'),fullPage:true});
    for(const tab of ['runtime','management','logs']){await windowsPage.locator('nav button[data-tab="'+tab+'"]').click();await windowsPage.screenshot({path:path.join(out,'windows-'+tab+'.png'),fullPage:true});}
    assert.equal(await windowsPage.evaluate(()=>localStorage.length),0);
    assert.deepEqual(errors,[],'browser JavaScript errors');
    fs.writeFileSync(path.join(out,'acceptance.json'),JSON.stringify({passed:true,controller:'real process / Basic Auth / create six-relay three-inlet tunnel / encrypted API / device assignment',windows:'Chromium Wails API fixture: editing, one-tunnel payload, selection, run lock and five views; native Windows acceptance is a separate job',javascript_errors:errors},null,2)+'\n');
    console.log('PASS: controller real-process UI and Windows frontend contract/rendering');
  } finally {
    if(browser)await browser.close();if(server)await new Promise(r=>server.close(r));
    child.kill('SIGTERM');await Promise.race([new Promise(r=>child.once('exit',r)),sleep(2000)]);
    if(child.exitCode===null)child.kill('SIGKILL');fs.closeSync(log);fs.rmSync(temporary,{recursive:true,force:true});
  }
}
main().catch(error=>{console.error(error.stack||error);process.exitCode=1;});
