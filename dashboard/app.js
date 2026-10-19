applyHostTheme();
observeHostTheme();
initializeCustomRange();
applyUsagePreferences({});
initializeChartInteractions();
initializeDashboardResizeObserver();
initializeConfigurationActions();
scheduleMetricFit();
tabs.forEach(tab=>{tab.addEventListener('click',()=>{let name='configuration';if(tab.id==='usage-tab')name='usage';else if(tab.id==='health-tab')name='health';activateTab(name)});tab.addEventListener('keydown',handleTabKey)});
initializeConfigurationEvents();
initializeUsageEvents();
initializeHealthEvents();
initializeUsageTableEvents();
initializeUsageChartEvents();
initializePricingEvents();
document.addEventListener('visibilitychange',()=>{if(!usageState.active&&!healthState?.polling)return;if(document.hidden){stopUsagePolling();stopHealthPolling()}else{if(usageState.active)refreshUsage(false);if(healthState?.polling)refreshHealth()}});
window.addEventListener('resize',()=>{updateConfigurationActionsClearance();window.clearTimeout(usageState.resizeTimer);usageState.resizeTimer=window.setTimeout(scheduleResponsiveDashboardRender,120)});
document.addEventListener('keydown',event=>{
  if((event.ctrlKey||event.metaKey)&&event.key.toLowerCase()==='s'&&!workspaceEl.hidden&&!configurationPanelEl.hidden){event.preventDefault();if(state.dirty)saveConfiguration()}
});
if(managementKey())loadConfiguration();
else showFallbackKeyInput('CPAMC has no saved management key. Enter it for this browser session.');
