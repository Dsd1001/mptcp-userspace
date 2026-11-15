'use strict';
let state = null;
let dirty = false;
let selectionDirty = false;
let messageTimer;
const $ = id => document.getElementById(id);
const create = (tag, text, cls) => {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = String(text);
  if (cls) node.className = cls;
  return node;
};
function call(name, ...args) {
  const api = window.go?.main?.App;
  if (!api || typeof api[name] !== 'function') return Promise.reject(new Error('Windows 后端尚未就绪'));
  return api[name](...args);
}
function toast(text, error=false) {
  const box=$('toast');box.textContent=String(text);box.classList.remove('hidden');box.classList.toggle('error',error);
  clearTimeout(messageTimer);messageTimer=setTimeout(()=>box.classList.add('hidden'),6500);
}
function bytes(n) {
  n=Number(n||0);const units=['B','KiB','MiB','GiB','TiB'];let i=0;
  while(Math.abs(n)>=1024 && i<units.length-1){n/=1024;i++;}
  return n.toFixed(i?1:0)+' '+units[i];
}
function input(type,value,attrs={}) {
  const n=create('input');n.type=type;if(type==='checkbox')n.checked=!!value;else n.value=value??'';
  Object.entries(attrs).forEach(([k,v])=>n.setAttribute(k,String(v)));
  n.addEventListener('input',()=>dirty=true);return n;
}
function button(text,handler,cls='') {
  const b=create('button',text,cls);b.type='button';b.onclick=()=>Promise.resolve().then(handler).catch(e=>toast(e.message||e,true));return b;
}
function addRelay(r={host:'',port:24001}) {
  const row=create('tr');
  for(const f of [input('text',r.host,{placeholder:'relay.example.com'}),input('number',r.port,{min:1,max:65535}),input('number',r.download_mbps,{min:.1,max:6553.5,step:.1,placeholder:'自动'}),input('number',r.upload_mbps,{min:.1,max:6553.5,step:.1,placeholder:'自动'})]){
    const td=create('td');td.append(f);row.append(td);
  }
  const td=create('td');td.append(button('删除',()=>{if($('relayRows').children.length<=2)throw new Error('至少保留两条路径');row.remove();dirty=true;},'danger'));row.append(td);$('relayRows').append(row);
}
function addInlet(i) {
  const row=create('tr');
  const fields=[input('text',i.id,{maxlength:64}),input('text',i.name,{maxlength:128}),input('number',i.listen_port,{min:1024,max:65535}),input('number',i.route_id,{min:1,max:4294967295}),input('number',i.weight||1,{min:1,max:16}),input('number',i.max_connections||512,{min:1,max:2048}),input('checkbox',i.tcp_enabled!==false),input('checkbox',i.uot_enabled===true)];
  fields.forEach(f=>{const td=create('td');td.append(f);row.append(td);});
  const td=create('td');td.append(button('删除',()=>{if($('inletRows').children.length<=1)throw new Error('至少保留一个入口');row.remove();dirty=true;},'danger'));row.append(td);$('inletRows').append(row);
}
function gatherConfig() {
  const relays=Array.from($('relayRows').children).map(row=>{
    const f=row.querySelectorAll('input');const r={host:f[0].value.trim(),port:Number(f[1].value)};
    if(f[2].value!=='')r.download_mbps=Number(f[2].value);if(f[3].value!=='')r.upload_mbps=Number(f[3].value);return r;
  });
  const inlets=Array.from($('inletRows').children).map(row=>{
    const f=row.querySelectorAll('input');return {id:f[0].value.trim(),name:f[1].value,listen_port:Number(f[2].value),route_id:Number(f[3].value),weight:Number(f[4].value),max_connections:Number(f[5].value),tcp_enabled:f[6].checked,uot_enabled:f[7].checked};
  });
  return {schema_version:1,kind:'shared_mpx4_tunnel',tunnel:{transport_key:$('transportKey').value.trim(),scheduler_mode:$('scheduler').value,relays},inlets};
}
function fillConfig(c) {
  if(!c?.tunnel)return;
  $('transportKey').value='';$('scheduler').value=c.tunnel.scheduler_mode||'auto';
  $('relayRows').replaceChildren();$('inletRows').replaceChildren();
  (c.tunnel.relays||[]).forEach(addRelay);(c.inlets||[]).forEach(addInlet);
}
function managedChoices(m) {
  if(selectionDirty)return;
  const box=$('managedChoices');box.replaceChildren();const selected=new Set(m.selected_ids||[]);
  for(const inlet of m.profiles||[]) {
    const label=create('label',undefined,'row');label.style.padding='9px 0';
    const check=create('input');check.type='checkbox';check.value=inlet.id;check.className='inlet-choice';check.checked=selected.has(inlet.id);check.onchange=()=>selectionDirty=true;
    label.append(check,create('span',inlet.name||inlet.id,'grow'),create('span','127.0.0.1:'+inlet.listen_port+' → '+inlet.route_id,'endpoint'),create('span',inlet.uot_enabled?'TCP / UoT':'TCP','badge'));box.append(label);
  }
}
function renderRuntime(s) {
  const e=s.event||{}, metrics=$('metrics');metrics.replaceChildren();
  const desired=s.settings?.configuration_source==='managed'?s.managed?.relay_count:s.settings?.tunnel?.tunnel?.relays?.length;
  for(const [label,value] of [['Carrier',String(e.paths||0)+' / '+(desired||0)],['业务 TCP',e.connections||0],['已发送',bytes(e.sent)],['已接收',bytes(e.received)]]){
    const p=create('div',undefined,'panel');p.append(create('small',label),create('h2',value));metrics.append(p);
  }
  const paths=$('paths');paths.replaceChildren();
  for(const p of e.path_stats||[]){const row=create('tr');for(const v of ['#'+p.id+' '+p.address,p.connected?'已连接':'离线',Number(p.rtt_ms||0).toFixed(1)+' ms',(Number(p.goodput_bps||0)*8/1e6).toFixed(1)+' Mbps',bytes(p.outstanding_bytes),bytes(p.queue_bytes)])row.append(create('td',v));paths.append(row);}
  const routes=$('routeStats');routes.replaceChildren();
  const byRoute=new Map((e.routes||[]).map(r=>[r.route_id,r]));
  for(const i of e.inlets||[]){const r=byRoute.get(i.route_id)||{};const row=create('tr');for(const v of [i.id+' / '+i.route_id,(i.active_connections||0)+' / '+(i.udp_connections||0),bytes(r.ready_data_bytes),bytes(r.receive_allocated_bytes),r.receive_pressure_resets||0,i.udp_dropped||0])row.append(create('td',v));routes.append(row);}
}
function setUnlessEditing(id,value,checked=false){const n=$(id);if(document.activeElement===n)return;if(checked)n.checked=!!value;else n.value=value||'';}
function applyState(s) {
  if(!s)return;state=s;const settings=s.settings||{},m=s.managed||{},locked=!!(s.running||s.busy),remote=settings.configuration_source==='managed';
  $('version').textContent='v'+s.version;$('status').textContent=s.status||'已停止';$('startStop').textContent=locked?'停止隧道':'连接隧道';
  $('problem').textContent=s.problem||'';$('problem').classList.toggle('hidden',!s.problem);
  $('source').value=remote?'managed':'local';$('localPanel').classList.toggle('hidden',remote);$('managedPanel').classList.toggle('hidden',!remote);$('saveLocal').classList.toggle('hidden',remote);
  if(!dirty)fillConfig(settings.tunnel);
  $('keyStatus').textContent=settings.transport_key_set?'密钥已由 DPAPI 保存；留空保留现有密钥。':'尚未保存隧道密钥。';
  $('managedStatus').textContent=(m.display_name?m.display_name+' · ':'')+(m.status||'未配置')+' · '+(m.relay_count||0)+' 条共享路径';$('managedEndpoint').textContent=m.display_endpoint||'';
  managedChoices(m);
  document.querySelectorAll('#localPanel input,#localPanel select,#localPanel button,#localPanel textarea').forEach(n=>n.disabled=locked);
  for(const id of ['source','saveLocal','doctor','provisioningURL','saveURL','clearManaged','saveSelection'])$(id).disabled=locked;
  document.querySelectorAll('.inlet-choice').forEach(n=>n.disabled=locked);
  $('sync').disabled=false;
  setUnlessEditing('resident',settings.background_resident,true);setUnlessEditing('autoUpdate',settings.automatic_updates,true);
  setUnlessEditing('remoteEnabled',settings.remote_enabled,true);setUnlessEditing('remoteServer',settings.remote_server);
  $('remoteStatus').textContent=(s.remote?.status||'关闭')+(settings.remote_device_id?' · '+settings.remote_device_id:'');
  $('updateStatus').textContent=s.update?.status||'';$('installUpdate').classList.toggle('hidden',!s.update?.available);
  $('logView').textContent=(s.logs||[]).join('\n')||'暂无日志';renderRuntime(s);
}
async function refresh(){applyState(await call('GetState'));}
function bind(id,fn){$(id).onclick=()=>Promise.resolve().then(fn).catch(e=>toast(e.message||String(e),true));}
function init(){
  for(const b of document.querySelectorAll('nav button'))b.onclick=()=>{for(const x of document.querySelectorAll('nav button'))x.classList.toggle('active',x===b);for(const id of ['configuration','runtime','management','logs'])$(id).classList.toggle('hidden',id!==b.dataset.tab);};
  bind('startStop',async()=>{await call(state?.running||state?.busy?'Stop':'Start');await refresh();});
  bind('doctor',async()=>toast(await call('Doctor')||'环境正常'));
  $('source').onchange=async()=>{try{await call('SetConfigurationSource',$('source').value);await refresh();}catch(e){toast(String(e),true);await refresh();}};
  $('transportKey').oninput=()=>dirty=true;$('scheduler').onchange=()=>dirty=true;
  bind('addRelay',()=>{if($('relayRows').children.length>=8)throw new Error('最多八条路径');addRelay();dirty=true;});
  bind('addInlet',()=>{const rows=gatherConfig().inlets;if(rows.length>=64)throw new Error('最多64个入口');let id=1,port=1081;while(rows.some(x=>x.route_id===id))id++;while(rows.some(x=>x.listen_port===port))port++;addInlet({id:'inlet-'+id,name:'入口 '+id,listen_port:port,route_id:id});dirty=true;});
  bind('saveLocal',async()=>{await call('SaveTunnelConfig',gatherConfig());dirty=false;await refresh();toast('共享隧道已保存');});
  bind('importButton',async()=>{await call('ImportTunnelJSON',$('importJSON').value);$('importJSON').value='';dirty=false;await refresh();toast('配置已校验并保存');});
  bind('saveURL',async()=>{await call('SetProvisioningURL',$('provisioningURL').value);$('provisioningURL').value='';selectionDirty=false;await refresh();});
  bind('sync',async()=>{await call('SyncProvisioning');selectionDirty=false;await refresh();toast('远端共享隧道已同步；运行中的流量不热切换');});
  bind('clearManaged',async()=>{await call('ClearProvisioning');selectionDirty=false;await refresh();});
  bind('saveSelection',async()=>{const ids=Array.from(document.querySelectorAll('.inlet-choice:checked')).map(x=>x.value);await call('SetManagedSelection',ids);selectionDirty=false;await refresh();});
  bind('saveRemote',async()=>{await call('SetRemoteManagement',$('remoteEnabled').checked,$('remoteServer').value);await refresh();});
  bind('pair',async()=>{await call('PairRemoteManagement',$('remoteServer').value,$('pairCode').value);$('pairCode').value='';await refresh();});
  bind('unpair',async()=>{if(!confirm('解除此设备远程配对？'))return;await call('UnpairRemoteManagement');await refresh();});
  $('resident').onchange=async()=>{try{await call('SetBackgroundResident',$('resident').checked);await refresh();}catch(e){toast(String(e),true);}};
  $('autoUpdate').onchange=async()=>{try{await call('SetAutomaticUpdates',$('autoUpdate').checked);await refresh();}catch(e){toast(String(e),true);}};
  bind('checkUpdate',async()=>{await call('CheckForUpdates',true);await refresh();});
  bind('installUpdate',async()=>{if(confirm('安装签名更新会重启客户端，继续？'))await call('InstallUpdate');});
  bind('copyLogs',async()=>{await navigator.clipboard.writeText($('logView').textContent);toast('日志已复制');});
  bind('clearLogs',async()=>{await call('ClearLogs');await refresh();});
  if(window.runtime?.EventsOn)window.runtime.EventsOn('state',applyState);
  refresh().catch(e=>toast(String(e),true));
}
window.addEventListener('DOMContentLoaded',init);
