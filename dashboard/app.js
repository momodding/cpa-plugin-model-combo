applyHostTheme();
observeHostTheme();
initializeCustomRange();
applyUsagePreferences({});
initializeChartInteractions();
initializeDashboardResizeObserver();
initializeConfigurationActions();
scheduleMetricFit();
tabs.forEach(tab=>{tab.addEventListener('click',()=>activateTab(tab.id.replace(/-tab$/,'')));tab.addEventListener('keydown',handleTabKey)});
initializeConfigurationEvents();
initializeUsageEvents();
initializeUsageTableEvents();
initializeUsageChartEvents();
initializePricingEvents();
document.addEventListener('visibilitychange',()=>{if(usageState.active){if(document.hidden)stopUsagePolling();else refreshUsage(false)}if(!document.getElementById('quota-panel').hidden){if(document.hidden)stopQuotaPolling();else refreshQuota()}});
window.addEventListener('resize',()=>{updateConfigurationActionsClearance();window.clearTimeout(usageState.resizeTimer);usageState.resizeTimer=window.setTimeout(scheduleResponsiveDashboardRender,120)});
document.addEventListener('keydown',event=>{
  if((event.ctrlKey||event.metaKey)&&event.key.toLowerCase()==='s'&&!workspaceEl.hidden&&(!configurationPanelEl.hidden||!document.getElementById('quota-panel').hidden)){event.preventDefault();if(state.dirty)saveConfiguration()}
});
if(managementKey())loadConfiguration();
else showFallbackKeyInput('CPAMC has no saved management key. Enter it for this browser session.');
