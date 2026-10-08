let state = null;
let draftRelays = [];
let toastTimer = null;
let localDraftDirty = false;

const $ = (id) => document.getElementById(id);
const appApi = () => window.go?.main?.App;

function call(name, ...args) {
  const api = appApi();
  if (!api || typeof api[name] !== 'function') return Promise.reject(new Error('Windows 后端尚未就绪'));
  return api[name](...args);
}

function showToast(message, error=false) {
  const el = $('toast');
  el.textContent = message || (error ? '操作失败' : '已完成');
  el.classList.remove('hidden', 'error');
  if (error) el.classList.add('error');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.add('hidden'), 3600);
}

function formatBytes(value) {
  let n = Number(value || 0);
  const units = ['B','KiB','MiB','GiB','TiB'];
  let i = 0;
  while (Math.abs(n) >= 1024 && i < units.length-1) { n /= 1024; i++; }
  return `${n.toFixed(i === 0 ? 0 : n >= 100 ? 0 : n >= 10 ? 1 : 2)} ${units[i]}`;
}
function formatRate(bps) {
  const mbps = Number(bps || 0) * 8 / 1_000_000;
  return `${mbps.toFixed(mbps >= 100 ? 0 : mbps >= 10 ? 1 : 2)} Mbps`;
}
function esc(text) {
  return String(text ?? '').replace(/[&<>'"]/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[ch]));
}
function schedulerLabel(mode) {
  return ({auto:'Auto',aggregate:'Aggregate',protect:'Protect',weighted:'Weighted'})[mode] || mode || 'Auto';
}
function schedulerHint(mode) {
  return ({auto:'通用自适应策略',aggregate:'更积极地并行使用健康 Carrier',protect:'优先隔离不稳定路径并保留探测',weighted:'容量先验 + 实时 RTT/队列/Delivery 反馈'})[mode] || '';
}

async function refreshState() {
  try { applyState(await call('GetState')); } catch (e) { showToast(String(e), true); }
}

function applyState(next) {
  state = next;
  $('versionBadge').textContent = `v${next.version || 'dev'}`;
  $('statusText').textContent = next.status || (next.running ? '运行中' : '已停止');
  const dot = $('statusDot');
  dot.className = 'status-dot ' + (next.problem && !next.running ? 'error' : next.running ? 'running' : next.busy ? 'busy' : 'idle');
  $('problemText').textContent = next.problem || '';
  $('problemText').classList.toggle('hidden', !next.problem);

  const settings = next.settings || {};
  const profile = settings.profile || {};
  const managed = next.managed || {};
  const source = settings.configuration_source || 'local';
  const endpointPort = source === 'managed' && managed.profiles?.length
    ? (managed.profiles.find(p => (managed.selected_ids || []).includes(p.id))?.listen_port || managed.profiles[0].listen_port)
    : (profile.listen_port || 1081);
  $('localEndpoint').textContent = `127.0.0.1:${endpointPort}`;
  $('startStopBtn').textContent = next.running || next.busy ? '停止转发' : '启动转发';
  $('startStopBtn').classList.toggle('danger-ghost', next.running || next.busy);
  $('startStopBtn').classList.toggle('primary', !(next.running || next.busy));

  const ev = next.event || {};
  $('connectionsValue').textContent = Number(ev.connections || 0).toLocaleString();
  $('sentValue').textContent = formatBytes(ev.sent || 0);
  $('receivedValue').textContent = formatBytes(ev.received || 0);
  $('lossValue').textContent = `${Number(ev.retransmits || 0).toLocaleString()} / ${Number(ev.dropped || 0).toLocaleString()}`;

  document.querySelectorAll('#sourceSegment .segment').forEach(btn => btn.classList.toggle('active', btn.dataset.source === source));
  $('localConfigPane').classList.toggle('hidden', source !== 'local');
  $('managedConfigPane').classList.toggle('hidden', source !== 'managed');

  if (!localDraftDirty) loadLocalForm(profile, settings.transport_key_set);
  renderManaged(managed, next.running || next.busy);
  renderDiagnostics(next);
  renderResources(next);
  renderLogs(next.logs || []);
  renderSettings(next);
  setLocked(next.running || next.busy);
}

function loadLocalForm(profile, keySet) {
  $('listenPort').value = profile.listen_port || 1081;
  $('tcpEnabled').checked = !!profile.tcp_enabled;
  $('udpEnabled').checked = !!profile.udp_enabled;
  $('uotEnabled').checked = !!profile.uot_enabled;
  $('schedulerMode').value = profile.scheduler_mode || 'auto';
  $('schedulerHint').textContent = schedulerHint($('schedulerMode').value);
  $('transportKey').value = '';
  $('transportKey').placeholder = keySet ? '已通过 DPAPI 保存；留空保持不变' : '64 位十六进制 Transport Key';
  $('transportKeyState').textContent = keySet ? 'Transport Key 已安全保存。输入新值才会替换。' : '尚未保存 Transport Key。';
  draftRelays = JSON.parse(JSON.stringify(profile.relays || []));
  while (draftRelays.length < 2) draftRelays.push({host:'',port:24001});
  renderRelays();
}

function renderRelays() {
  const weighted = $('schedulerMode').value === 'weighted';
  $('schedulerHint').textContent = schedulerHint($('schedulerMode').value);
  $('relayList').innerHTML = draftRelays.map((relay, i) => `
    <div class="relay-row" data-index="${i}">
      <div class="relay-index">${i+1}</div>
      <label class="field"><span>Host / IP</span><input data-k="host" value="${esc(relay.host || '')}" placeholder="relay.example.com"></label>
      <label class="field"><span>端口</span><input data-k="port" type="number" min="1" max="65535" value="${Number(relay.port || 24001)}"></label>
      <label class="field weighted-field ${weighted?'':'hidden-field'}"><span>下行 Mbps</span><input data-k="download_mbps" type="number" min="0.1" max="6553.5" step="0.1" value="${relay.download_mbps ?? ''}" placeholder="必填"></label>
      <label class="field weighted-field ${weighted?'':'hidden-field'}"><span>上行 Mbps</span><input data-k="upload_mbps" type="number" min="0.1" max="6553.5" step="0.1" value="${relay.upload_mbps ?? ''}" placeholder="自动"></label>
      <button class="icon-button remove-relay" title="删除" ${draftRelays.length<=2?'disabled':''}>×</button>
    </div>`).join('');
  $('addRelayBtn').disabled = draftRelays.length >= 8;
  document.querySelectorAll('.relay-row input').forEach(input => input.addEventListener('input', e => {
    const row = e.target.closest('.relay-row'); const i = Number(row.dataset.index); const k = e.target.dataset.k;
    let value = e.target.value;
    if (['port','download_mbps','upload_mbps'].includes(k)) value = value === '' ? null : Number(value);
    draftRelays[i][k] = value;
    localDraftDirty = true;
  }));
  document.querySelectorAll('.remove-relay').forEach(btn => btn.addEventListener('click', e => {
    const i = Number(e.target.closest('.relay-row').dataset.index);
    if (draftRelays.length > 2) { draftRelays.splice(i,1); localDraftDirty = true; renderRelays(); }
  }));
}

function gatherLocalProfile() {
  return {
    listen_port: Number($('listenPort').value),
    tcp_enabled: $('tcpEnabled').checked,
    udp_enabled: $('udpEnabled').checked,
    uot_enabled: $('uotEnabled').checked,
    scheduler_mode: $('schedulerMode').value,
    relays: draftRelays.map(r => ({
      host: String(r.host || '').trim(),
      port: Number(r.port || 0),
      ...(r.download_mbps != null && r.download_mbps !== '' ? {download_mbps:Number(r.download_mbps)} : {}),
      ...(r.upload_mbps != null && r.upload_mbps !== '' ? {upload_mbps:Number(r.upload_mbps)} : {}),
    })),
    transport_key: $('transportKey').value.trim(),
  };
}

function renderManaged(m, locked) {
  $('managedEndpointLabel').textContent = m.display_endpoint || '尚未配置';
  $('managedSummary').classList.toggle('empty', !m.kind);
  $('managedSummary').innerHTML = m.kind ? `
    <strong>${esc(m.display_name || (m.kind === 'bundle' ? 'Bundle' : 'Profile'))}</strong>
    <span class="badge ${m.from_cache?'learning':'active'}">${m.from_cache?'LKG':'LIVE'}</span>
    <div class="managed-profile-meta">${esc(m.kind)} · revision ${esc(m.revision || '—')} ${m.bundle_mode ? '· '+esc(m.bundle_mode) : ''} ${m.fetched_at ? '· '+esc(m.fetched_at) : ''} · ${esc(m.status || '')}</div>
  ` : '保存 Provisioning URL 后同步 Profile / Bundle。';
  const selected = new Set(m.selected_ids || []);
  $('managedProfiles').innerHTML = (m.profiles || []).map(p => `
    <label class="managed-profile">
      <input class="managed-select" type="${m.bundle_mode==='single_select'?'radio':'checkbox'}" name="managedProfile" value="${esc(p.id)}" ${selected.has(p.id)?'checked':''} ${locked?'disabled':''}>
      <div class="managed-profile-main">
        <div class="managed-profile-title">${esc(p.name || p.id)}</div>
        <div class="managed-profile-meta">127.0.0.1:${p.listen_port} · ${schedulerLabel(p.scheduler_mode)} · ${p.tcp_enabled?'TCP ':''}${p.udp_enabled?'UDP ':''}${p.uot_enabled?'UoT ':''}${p.background_resident?'· 后台常驻':''}</div>
      </div>
      <span class="badge neutral">Userspace</span>
    </label>`).join('');
}

function renderDiagnostics(s) {
  const ev = s.event || {};
  const scheduler = ev.effective_scheduler_mode || ev.configured_scheduler_mode || s.settings?.profile?.scheduler_mode || 'auto';
  $('schedulerStatus').textContent = `Scheduler ${schedulerLabel(scheduler)}`;
  const bundle = s.managed?.kind === 'bundle';
  $('bundleStatus').classList.toggle('hidden', !bundle);
  if (bundle) $('bundleStatus').textContent = `${s.managed.display_name || 'Bundle'} · ${(s.managed.selected_ids || []).length} 个 Profile · ${s.status || ''}`;

  const groups = [];
  if (Object.keys(s.profile_events || {}).length) {
    for (const p of s.managed?.profiles || []) {
      const pev = s.profile_events[p.id]; if (!pev) continue;
      groups.push({title:`${p.name || p.id} · 127.0.0.1:${p.listen_port}`, event:pev, status:s.profile_status?.[p.id], error:s.profile_errors?.[p.id]});
    }
  } else if ((ev.path_stats || []).length) {
    groups.push({title:'当前 Session', event:ev, status:s.running?'运行中':'连接中'});
  }
  $('emptyPaths').classList.toggle('hidden', groups.length > 0);
  $('pathGroups').innerHTML = groups.map(g => `
    <div class="path-group">
      <div class="path-group-title">${esc(g.title)} <span class="badge ${g.status==='运行中'?'active':'neutral'}">${esc(g.status || '')}</span>${g.error?`<span class="problem">${esc(g.error)}</span>`:''}</div>
      <div class="path-list">${(g.event.path_stats || []).map(pathCard).join('') || '<div class="empty-state">正在等待路径统计…</div>'}</div>
    </div>`).join('');
}

function pathCard(p) {
  const role = String(p.role || '').toLowerCase();
  const roleClass = role === 'active' ? 'active' : role === 'probe' ? 'probe' : role === 'learning' ? 'learning' : 'neutral';
  return `<div class="path-card">
    <div class="path-header">
      <span class="path-dot ${p.connected?'online':''}"></span>
      <span class="path-name">${esc(p.id)} · ${esc(p.address || 'Carrier')}</span>
      ${role?`<span class="badge ${roleClass}">${esc(role.toUpperCase())}</span>`:''}
      <span class="path-spacer"></span><span class="path-state">${p.connected?'在线':'重连中'}</span>
    </div>
    <div class="path-metrics">RTT ${Number(p.rtt_ms||0).toFixed(1)} ms · Goodput ${formatRate(p.goodput_bps||0)} · 队列 ${formatBytes(p.queue_bytes)} · 在途 ${formatBytes(p.outstanding_bytes)} · 错误 ${Number(p.errors||0)}</div>
    <div class="path-extra">
      ${p.measured_delivery_bps?`<span>测得 ${formatRate(p.measured_delivery_bps)}</span>`:''}
      ${p.delivery_samples?`<span>样本 ${p.delivery_samples}</span>`:''}
      ${p.dial_attempts?`<span>拨号 ${p.dial_attempts}</span>`:''}
      ${p.budget_bytes?`<span>预算 ${formatBytes(p.budget_bytes)}</span>`:''}
    </div>
    ${(p.role_reason||p.last_error)?`<div class="path-reason">${esc(p.role_reason || p.last_error)}</div>`:''}
  </div>`;
}

function renderResources(s) {
  let r = s.event?.resources || null;
  if (!r) {
    for (const ev of Object.values(s.profile_events || {})) { if (ev.resources) { r = ev.resources; break; } }
  }
  const streamItems = r ? [
    ['Active Streams', r.active_streams], ['Closing Streams', r.closing_streams], ['Local Connections', r.local_connections],
    ['Opening', r.lifecycle_opening], ['Open Bidirectional', r.lifecycle_open_bidirectional], ['Half Closed', r.lifecycle_half_closed],
    ['Idle > 30s', r.data_idle_over_30s], ['Idle > 5m', r.data_idle_over_5m]
  ] : [];
  const windowItems = r ? [
    ['Pending Frames', `${r.pending_frames||0} / ${r.pending_frame_limit||0}`],
    ['DATA Pending', r.data_pending_frames], ['Control Pending', r.control_pending_frames], ['Window Waiters', r.window_blocked_writers],
    ['Receive Credit', `${formatBytes(r.receive_credit_bytes)} / ${formatBytes(r.receive_credit_limit_bytes)}`],
    ['Receive Allocated', `${formatBytes(r.receive_allocated_bytes)} / ${formatBytes(r.receive_allocated_limit_bytes)}`]
  ] : [];
  $('streamResources').innerHTML = resourceHTML(streamItems);
  $('windowResources').innerHTML = resourceHTML(windowItems);
}
function resourceHTML(items) {
  if (!items.length) return '<div class="inline-note">运行后显示资源状态</div>';
  return items.map(([k,v])=>`<div class="resource-item"><span>${esc(k)}</span><strong>${esc(v ?? 0)}</strong></div>`).join('');
}

function renderLogs(logs) { $('logView').textContent = logs.length ? logs.join('\n') : '尚无日志'; $('logView').scrollTop = $('logView').scrollHeight; }

function renderSettings(s) {
  const settings = s.settings || {};
  $('backgroundResident').checked = !!settings.background_resident;
  $('automaticUpdates').checked = !!settings.automatic_updates;
  $('remoteEnabled').checked = !!s.remote?.enabled;
  $('remoteServer').value = s.remote?.server || settings.remote_server || '';
  $('remoteStatus').textContent = `${s.remote?.status || '关闭'}${s.remote?.device_id ? ' · '+s.remote.device_id : ''}`;
  $('updateStatus').textContent = s.update?.status || '';
  $('installUpdateBtn').classList.toggle('hidden', !s.update?.available);
  $('installUpdateBtn').textContent = s.update?.version ? `安装 ${s.update.version}` : '安装更新';
  const managedControlsResident = settings.configuration_source === 'managed' && !!s.managed?.kind;
  $('backgroundResident').disabled = managedControlsResident;
}

function setLocked(locked) {
  document.querySelectorAll('#localConfigPane input,#localConfigPane select,#localConfigPane button').forEach(el => el.disabled = locked || (el.classList.contains('remove-relay') && draftRelays.length<=2));
  $('sourceSegment').querySelectorAll('button').forEach(el => el.disabled = locked);
  $('saveProvisioningBtn').disabled = locked;
  $('clearProvisioningBtn').disabled = locked;
  $('saveManagedSelectionBtn').disabled = locked;
}

async function runAction(promise, ok) {
  try { const result = await promise; if (ok) showToast(ok); await refreshState(); return result; }
  catch (e) { showToast(String(e), true); await refreshState(); throw e; }
}

function bind() {
  $('startStopBtn').addEventListener('click', async () => { try { if (state?.running || state?.busy) await runAction(call('Stop'), '已停止'); else await runAction(call('Start'), '正在启动'); } catch {} });
  $('copyEndpointBtn').addEventListener('click', () => navigator.clipboard.writeText($('localEndpoint').textContent).then(()=>showToast('本地入口已复制')));
  $('doctorBtn').addEventListener('click', async()=>{ try { const m=await call('Doctor'); showToast(m||'环境正常'); } catch(e){showToast(String(e),true);} });

  document.querySelectorAll('#sourceSegment .segment').forEach(btn => btn.addEventListener('click', async()=>{ try { await runAction(call('SetConfigurationSource', btn.dataset.source)); } catch {} }));
  ['listenPort','tcpEnabled','udpEnabled','uotEnabled','transportKey'].forEach(id => $(id).addEventListener('input',()=>localDraftDirty=true));
  $('schedulerMode').addEventListener('change',()=>{localDraftDirty=true;renderRelays();});
  $('addRelayBtn').addEventListener('click',()=>{if(draftRelays.length<8){draftRelays.push({host:'',port:24001});localDraftDirty=true;renderRelays();}});
  $('saveLocalBtn').addEventListener('click',async()=>{try{await call('SaveLocalProfile',gatherLocalProfile());localDraftDirty=false;showToast('本地配置已保存');await refreshState();}catch(e){showToast(String(e),true);}});

  $('saveProvisioningBtn').addEventListener('click',async()=>{try{await runAction(call('SetProvisioningURL',$('provisioningURL').value),'Provisioning 地址已保存');$('provisioningURL').value='';}catch{}});
  $('syncProvisioningBtn').addEventListener('click',async()=>{try{await runAction(call('SyncProvisioning'),'Provisioning 已同步');}catch{}});
  $('clearProvisioningBtn').addEventListener('click',async()=>{try{await runAction(call('ClearProvisioning'),'远端配置已清除');}catch{}});
  $('saveManagedSelectionBtn').addEventListener('click',async()=>{const ids=[...document.querySelectorAll('.managed-select:checked')].map(x=>x.value);try{await runAction(call('SetManagedSelection',ids),'Profile 选择已保存');}catch{}});

  $('backgroundResident').addEventListener('change',async e=>{try{await runAction(call('SetBackgroundResident',e.target.checked));}catch{}});
  $('automaticUpdates').addEventListener('change',async e=>{try{await runAction(call('SetAutomaticUpdates',e.target.checked));}catch{}});
  $('checkUpdatesBtn').addEventListener('click',async()=>{try{await runAction(call('CheckForUpdates',true));}catch{}});
  $('installUpdateBtn').addEventListener('click',async()=>{try{await call('InstallUpdate');}catch(e){showToast(String(e),true);}});

  $('saveRemoteBtn').addEventListener('click',async()=>{try{await runAction(call('SetRemoteManagement',$('remoteEnabled').checked,$('remoteServer').value),'远程管理设置已保存');}catch{}});
  $('remoteEnabled').addEventListener('change',async e=>{try{await runAction(call('SetRemoteManagement',e.target.checked,$('remoteServer').value));}catch{}});
  $('pairRemoteBtn').addEventListener('click',async()=>{try{await runAction(call('PairRemoteManagement',$('remoteServer').value,$('pairCode').value),'远程管理已配对');$('pairCode').value='';}catch{}});
  $('unpairRemoteBtn').addEventListener('click',async()=>{try{await runAction(call('UnpairRemoteManagement'),'已解除远程管理配对');}catch{}});

  $('copyLogsBtn').addEventListener('click',()=>navigator.clipboard.writeText($('logView').textContent).then(()=>showToast('日志已复制')));
  $('clearLogsBtn').addEventListener('click',async()=>{await call('ClearLogs');await refreshState();});
}

window.addEventListener('DOMContentLoaded', async () => {
  bind();
  if (window.runtime?.EventsOn) window.runtime.EventsOn('state', (s) => applyState(s));
  await refreshState();
});
