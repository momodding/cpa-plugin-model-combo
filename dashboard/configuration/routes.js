const STRATEGIES=['priority','round-robin','random','last_known_good'];
const LKG_KEYS=['success_rate_threshold','minimum_samples','observation_window_minutes'];
const DEFAULT_LKG={success_rate_threshold:'0.9',minimum_samples:'20',observation_window_minutes:'60'};
function canonicalIntegerDraft(value){
  const raw=String(value===undefined||value===null?'':value);
  const trimmed=raw.trim();
  return /^-?\d+$/.test(trimmed)?{value:Number.parseInt(trimmed,10)}:{draft:raw};
}
function integerDraftValue(value){
  const draft=canonicalIntegerDraft(value);
  return Object.prototype.hasOwnProperty.call(draft,'value')?draft.value:Number.NaN;
}
function normalizeRoute(value,index){
  const route=value&&typeof value==='object'?value:{};
  const canonicalCooldown=Number.isInteger(route.cooldown_seconds)?route.cooldown_seconds:null;
  const legacyCooldown=Number.isInteger(route['cooldown-seconds'])?route['cooldown-seconds']:null;
  const cooldown=canonicalCooldown!==null?canonicalCooldown:(legacyCooldown!==null?legacyCooldown:60);
  const targets=Array.isArray(route.targets)?route.targets.map(target=>{
    const value=target&&typeof target==='object'?target:{};
    return{model:String(value.model||''),weight:String(Number.isInteger(value.weight)?value.weight:1)};
  }):(Array.isArray(route.models)?route.models.map(model=>({model:String(model||''),weight:'1'})):[]);
  const strategy=STRATEGIES.includes(route.strategy)?route.strategy:'priority';
  const normalized={
    alias:String(route.alias||('route-'+(index+1))),
    strategy,
    cooldown_seconds:String(cooldown),
    targets
  };
  if(strategy==='last_known_good'){
    normalized.success_rate_threshold=String(route.success_rate_threshold??DEFAULT_LKG.success_rate_threshold);
    normalized.minimum_samples=String(route.minimum_samples??DEFAULT_LKG.minimum_samples);
    normalized.observation_window_minutes=String(route.observation_window_minutes??DEFAULT_LKG.observation_window_minutes);
  }
  return normalized;
}
function serializeRoutes(routes){
  return routes.map(route=>{
    const serialized={
      alias:route.alias.trim(),
      strategy:route.strategy,
      cooldown_seconds:integerDraftValue(route.cooldown_seconds),
      targets:route.targets.map(target=>({model:target.model.trim(),weight:integerDraftValue(target.weight)}))
    };
    const lkg=route.strategy==='last_known_good';
    if(lkg||route.success_rate_threshold!==undefined)serialized.success_rate_threshold=Number(String(route.success_rate_threshold??DEFAULT_LKG.success_rate_threshold).trim());
    if(lkg||route.minimum_samples!==undefined)serialized.minimum_samples=integerDraftValue(route.minimum_samples??DEFAULT_LKG.minimum_samples);
    if(lkg||route.observation_window_minutes!==undefined)serialized.observation_window_minutes=integerDraftValue(route.observation_window_minutes??DEFAULT_LKG.observation_window_minutes);
    return serialized;
  });
}
function baseModel(value){
  const match=/^(.*)\([^()]+\)$/.exec(value);
  return (match?match[1]:value).trim().toLowerCase();
}
function validateRoutes(routes){
  const errors=[];
  const aliases=new Map();
  routes.forEach((route,index)=>{
    const label='Route '+(index+1);
    const aliasKey=route.alias.toLowerCase();
    if(!route.alias)errors.push(label+': alias is required.');
    else if(/\([^()]+\)$/.test(route.alias))errors.push(label+': aliases cannot include a thinking suffix.');
    else if(aliases.has(aliasKey))errors.push(label+': alias duplicates route '+aliases.get(aliasKey)+'.');
    else aliases.set(aliasKey,index+1);
    if(!STRATEGIES.includes(route.strategy))errors.push(label+': choose a strategy.');
    if(!Number.isInteger(route.cooldown_seconds)||route.cooldown_seconds<0)errors.push(label+': cooldown must be a non-negative integer.');
    if(route.strategy==='last_known_good'){
      if(!(Number(route.success_rate_threshold)>0&&Number(route.success_rate_threshold)<=1))errors.push(label+': success rate threshold must be above 0 and at most 1.');
      if(!Number.isInteger(integerDraftValue(route.minimum_samples))||integerDraftValue(route.minimum_samples)<1)errors.push(label+': minimum samples must be an integer of at least 1.');
      if(!Number.isInteger(integerDraftValue(route.observation_window_minutes))||integerDraftValue(route.observation_window_minutes)<1)errors.push(label+': observation window must be an integer of at least 1 minute.');
    }
    if(route.targets.length===0)errors.push(label+': add at least one target.');
    const models=new Map();
    route.targets.forEach((target,targetIndex)=>{
      const model=target.model;
      const modelKey=model.toLowerCase();
      if(!model)errors.push(label+', target '+(targetIndex+1)+': model is required.');
      else if(!Number.isInteger(target.weight)||target.weight<1||target.weight>1000000)errors.push(label+', target '+(targetIndex+1)+': weight must be an integer from 1 to 1000000.');
      else if(models.has(modelKey))errors.push(label+', target '+(targetIndex+1)+': model duplicates target '+models.get(modelKey)+'.');
      else models.set(modelKey,targetIndex+1);
    });
  });
  routes.forEach((route,index)=>route.targets.forEach((target,targetIndex)=>{
    const targetAlias=aliases.get(baseModel(target.model));
    if(targetAlias)errors.push('Route '+(index+1)+', target '+(targetIndex+1)+': target references route alias '+targetAlias+'.');
  }));
  return errors;
}
function fallbackDraftFromConfig(value){
  const fallback=value&&typeof value==='object'?value:{};
  const list=items=>Array.isArray(items)?items.map(String).join(', '):'';
  const integer=(number,fallbackValue)=>Number.isInteger(number)?String(number):fallbackValue;
  return{
    enabled:fallback.enabled!==false,
    fallback_on_status:list(fallback.fallback_on_status),
    no_fallback_on_status:list(fallback.no_fallback_on_status),
    retry_codes:list(fallback.retry_codes),
    no_retry_codes:list(fallback.no_retry_codes),
    max_attempts:integer(fallback.max_attempts,'0'),
		stream_fallback_before_payload:fallback.stream_fallback_before_payload===true
  };
}
function configFromResponse(config){
  const source=Array.isArray(config.routes)?config.routes:(Array.isArray(config['model-routes'])?config['model-routes']:[]);
  return{
    enabled:config.enabled!==false,
    priority:Number.isInteger(config.priority)?String(config.priority):'0',
    routes:source.map(normalizeRoute),
    fallback:fallbackDraftFromConfig(config.fallback)
  };
}
function parseCodeList(raw){
  return String(raw||'').split(',').map(value=>value.trim()).filter(Boolean);
}
function parseStatusList(raw){
  return parseCodeList(raw).map(value=>/^\d+$/.test(value)?Number.parseInt(value,10):Number.NaN);
}
function fallbackFromState(fallback){
  return{
    enabled:fallback.enabled,
    fallback_on_status:parseStatusList(fallback.fallback_on_status),
    no_fallback_on_status:parseStatusList(fallback.no_fallback_on_status),
    retry_codes:parseCodeList(fallback.retry_codes),
    no_retry_codes:parseCodeList(fallback.no_retry_codes),
    max_attempts:integerDraftValue(fallback.max_attempts),
		stream_fallback_before_payload:true
  };
}
function validateFallback(fallback){
  const errors=[];
  const statusInRange=value=>Number.isInteger(value)&&value>=100&&value<=599;
  const onStatus=parseStatusList(fallback.fallback_on_status);
  const noStatus=parseStatusList(fallback.no_fallback_on_status);
  if(!onStatus.every(statusInRange)||!noStatus.every(statusInRange))errors.push('Fallback statuses must be integers from 100 to 599.');
  const noStatusSet=new Set(noStatus);
  if(onStatus.some(value=>noStatusSet.has(value)))errors.push('A fallback status cannot appear in both allow and deny lists.');
  const retry=parseCodeList(fallback.retry_codes);
  const noRetry=new Set(parseCodeList(fallback.no_retry_codes));
  if(retry.some(code=>noRetry.has(code)))errors.push('A fallback error code cannot appear in both allow and deny lists.');
  for(const key of['max_attempts']){
    const value=integerDraftValue(fallback[key]);
    if(!Number.isInteger(value)||value<0)errors.push('Fallback '+key.replace(/_/g,' ')+' must be a non-negative integer.');
  }
  return errors;
}
function configurationPatchBody(state){
  return{
    enabled:state.enabled,
    priority:integerDraftValue(state.priority),
    routes:serializeRoutes(state.routes),
    fallback:fallbackFromState(state.fallback),
    'model-routes':null
  };
}
