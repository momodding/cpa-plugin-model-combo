const QUOTA_API='/v0/management/plugins/model-combo/quota-status';
let quotaTimer=0, quotaGeneration=0, quotaController=null;
function stopQuotaPolling(){
  window.clearTimeout(quotaTimer);quotaTimer=0;quotaGeneration++;
  if(quotaController)quotaController.abort();
  quotaController=null;
}
function quotaText(tag,text){const el=document.createElement(tag);el.textContent=text;return el}
function quotaTime(value){if(!value)return '—';const date=new Date(value);return Number.isNaN(date.getTime())?'—':date.toLocaleString()}
function renderQuota(payload){
  const results=document.getElementById('quota-results');
  results.replaceChildren();
  const credentials=Array.isArray(payload.credentials)?payload.credentials:[];
  document.getElementById('quota-status').textContent=credentials.length?`Polling ${payload.running?'active':'inactive'} · ${payload.active_keys||0} active accounts · Last tick: ${quotaTime(payload.last_tick)}`:'No quota accounts are currently tracked. Headroom routes activate polling when used.';
  for(const account of credentials){
    const section=document.createElement('section');section.className='fallback-settings';
    section.append(quotaText('h3',`${account.label||account.auth_index||'Account'} · ${account.provider||'Unknown provider'}`));
    const source=account.score_source==='configured-default'?'configured-default':'observed';
    const score=Number.isFinite(account.effective_percent)?`${account.effective_percent}%`:'unavailable';
    section.append(quotaText('p',`${account.state||'unknown'} · Effective ${score} (${source}) · ${account.reason||'no reported issue'}`));
    section.append(quotaText('p',`Source: ${account.source||'unavailable'} · Fetched: ${quotaTime(account.fetched_at)} · Age: ${Number.isFinite(account.age_seconds)?account.age_seconds+' seconds':'unknown'}`));
    const windows=Array.isArray(account.windows)?account.windows:[];
    if(windows.length){
      const list=document.createElement('ul');
      for(const window of windows){const remaining=Number.isFinite(window.remaining)?`${(window.remaining*100).toFixed(1)}%`:'unknown';list.append(quotaText('li',`${window.name||'Window'}${window.scope?' ('+window.scope+')':''}: ${remaining} remaining · Reset ${quotaTime(window.reset_at)}`))}
      section.append(list);
    }else section.append(quotaText('p','No current quota windows.'));
    results.append(section);
    section.append(quotaText('p',`Backoff until: ${quotaTime(account.backoff_until)}`));
  }
  const selections=Array.isArray(payload.last_selection)?payload.last_selection:[];
  document.getElementById('quota-selection').textContent=selections.length?'Last selection: '+selections.map(item=>[item.route,item.model,item.label,Number.isFinite(item.score_percent)?`${item.score_percent}%`:'',item.state,item.reason].filter(value=>value!==undefined&&value!=='').join(' · ')).join('; '):'No headroom selection recorded.';
}
async function refreshQuota(){
  stopQuotaPolling();
  if(document.hidden||document.getElementById('quota-panel').hidden)return;
  const generation=quotaGeneration, controller=new AbortController();quotaController=controller;
  document.getElementById('quota-status').textContent='Loading quota status…';
  try{
    const payload=await requestManagementJSON(QUOTA_API,{signal:controller.signal});
    if(generation!==quotaGeneration)return;
    renderQuota(payload);
  }catch(error){
    if(generation!==quotaGeneration)return;
    document.getElementById('quota-results').replaceChildren();
    document.getElementById('quota-selection').textContent='';
    document.getElementById('quota-status').textContent='Quota status unavailable. Try again when the management service is available.';
  }finally{
    if(generation===quotaGeneration){quotaController=null;quotaTimer=window.setTimeout(refreshQuota,15000)}
  }
}
