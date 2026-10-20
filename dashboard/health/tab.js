'use strict';

const HEALTH_API = '/v0/management/plugins/model-combo/health/list';
const HEALTH_RESET_API = '/v0/management/plugins/model-combo/health/reset';

let healthState = {
  active: false,
  polling: false,
  pollTimer: 0,
  inFlight: false,
  controller: null,
  generation: 0,
  lastRefresh: null,
};

function startHealthDashboard() {
  healthState.active = true;
  if (document.hidden || healthState.polling) return;
  healthState.polling = true;
  refreshHealth();
}

function scheduleHealthPoll() {
  window.clearTimeout(healthState.pollTimer);
  healthState.pollTimer = 0;
  if (!healthState.polling || document.hidden) return;
  healthState.pollTimer = window.setTimeout(() => refreshHealth(), 5000);
}

function stopHealthPolling() {
  healthState.active = false;
  healthState.polling = false;
  healthState.generation++;
  window.clearTimeout(healthState.pollTimer);
  healthState.pollTimer = 0;
  if (healthState.controller) healthState.controller.abort();
  healthState.controller = null;
  healthState.inFlight = false;
}

function resumeHealthPolling() {
  if (healthState.active && !document.hidden) startHealthDashboard();
}

async function refreshHealth() {
  if (healthState.inFlight) return;

  const healthLoadingEl = document.getElementById('health-loading');
  const healthGridEl = document.getElementById('health-grid');
  const healthEmptyEl = document.getElementById('health-empty');
  const healthErrorEl = document.getElementById('health-error');
  const healthPanelEl = document.getElementById('health-panel');
  const generation = ++healthState.generation;
  const controller = new AbortController();
  healthState.inFlight = true;
  healthState.controller = controller;

  try {
    healthLoadingEl.hidden = false;
    healthGridEl.hidden = true;
    healthEmptyEl.hidden = true;
    healthErrorEl.hidden = true;
    healthPanelEl.setAttribute('aria-busy', 'true');

    const response = await requestManagementJSON(HEALTH_API, { signal: controller.signal });
    if (generation !== healthState.generation || controller.signal.aborted) return;
    if (!response || !response.comboHealth) throw new Error('Invalid health response format');

    const comboHealth = response.comboHealth;
    const count = response.count || 0;
    if (count === 0) {
      healthEmptyEl.hidden = false;
      healthGridEl.hidden = true;
    } else {
      renderHealthCards(comboHealth);
      healthGridEl.hidden = false;
      healthEmptyEl.hidden = true;
    }
    healthLoadingEl.hidden = true;
    healthErrorEl.hidden = true;
    healthState.lastRefresh = new Date();
  } catch (error) {
    if (generation !== healthState.generation || controller.signal.aborted) return;
    console.error('Failed to load health:', error);
    healthLoadingEl.hidden = true;
    healthGridEl.hidden = true;
    healthEmptyEl.hidden = true;
    healthErrorEl.hidden = false;
    healthErrorEl.textContent = error.message || 'Failed to load health data';
  } finally {
    if (generation !== healthState.generation) return;
    healthState.inFlight = false;
    healthState.controller = null;
    healthPanelEl.setAttribute('aria-busy', 'false');
    scheduleHealthPoll();
  }
}

function renderHealthCards(comboHealth) {
  const healthGridEl = document.getElementById('health-grid');
  healthGridEl.textContent = '';

  for (const [comboKey, groupMap] of Object.entries(comboHealth)) {
    for (const [groupKey, candidateMap] of Object.entries(groupMap)) {
      for (const [candidateKey, health] of Object.entries(candidateMap)) {
        healthGridEl.appendChild(createHealthCard(comboKey, groupKey, candidateKey, health));
      }
    }
  }
}

function appendHealthStat(container, label, value) {
  const stat = document.createElement('div');
  stat.className = 'health-stat';
  const labelEl = document.createElement('span');
  labelEl.className = 'health-stat-label';
  labelEl.textContent = label;
  const valueEl = document.createElement('span');
  valueEl.className = 'health-stat-value';
  valueEl.textContent = value;
  stat.append(labelEl, valueEl);
  container.appendChild(stat);
}

function createHealthCard(comboKey, groupKey, candidateKey, health) {
  const card = document.createElement('div');
  card.className = 'health-card';

  const circuitOpenUntil = new Date(health.circuitOpenUntil);
  const isCircuitOpen = health.circuitOpen === true && Number.isFinite(circuitOpenUntil.getTime()) && circuitOpenUntil.getTime() > Date.now();
  const failures = Number.isFinite(health.consecutiveFailures) ? health.consecutiveFailures : 0;
  const status = isCircuitOpen ? 'unhealthy' : failures > 0 ? 'warning' : 'healthy';
  const statusText = isCircuitOpen ? 'Circuit open' : failures > 0 ? 'Failures' : 'Healthy';
  const now = new Date();
  const coolUntil = new Date(health.coolUntil);

  const header = document.createElement('div');
  header.className = 'health-card-header';
  const identifiers = document.createElement('div');
  const title = document.createElement('div');
  title.className = 'health-card-title';
  title.textContent = `${comboKey} / ${groupKey}`;
  const target = document.createElement('div');
  target.className = 'health-card-target';
  target.textContent = `Target: ${candidateKey}`;
  identifiers.append(title, target);
  const badge = document.createElement('span');
  badge.className = `health-status-badge ${status}`;
  badge.textContent = statusText;
  header.append(identifiers, badge);

  const stats = document.createElement('div');
  stats.className = 'health-card-stats';
  appendHealthStat(stats, 'Consecutive failures', String(failures));
  if (coolUntil > now) {
    appendHealthStat(stats, 'Cooling until', formatSecondsRemaining(Math.ceil((coolUntil - now) / 1000)));
  }
  if (isCircuitOpen) {
    appendHealthStat(stats, 'Circuit opens until', formatSecondsRemaining(Math.ceil((circuitOpenUntil - now) / 1000)));
  }
  if (Number.isFinite(health.circuitReopenCount) && health.circuitReopenCount > 0) {
    appendHealthStat(stats, 'Circuit reopen count', String(health.circuitReopenCount));
  }
  if (health.circuitOpen === true && !isCircuitOpen) appendHealthStat(stats, 'Half-open', health.halfOpenOccupied === true ? 'Active probe' : 'Probe eligible');
  else if (health.halfOpenOccupied === true) appendHealthStat(stats, 'Half-open', 'Active probe');

  const actions = document.createElement('div');
  actions.className = 'health-card-actions';
  const reset = document.createElement('button');
  reset.className = 'secondary';
  reset.type = 'button';
  reset.textContent = 'Reset group health';
  reset.addEventListener('click', () => resetHealth(reset, comboKey, groupKey));
  actions.appendChild(reset);

  card.append(header, stats, actions);
  return card;
}

function formatSecondsRemaining(seconds) {
  if (seconds <= 0) return '0s';
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  return `${Math.floor(seconds / 3600)}h`;
}

async function resetHealth(button, comboKey, groupKey) {
  if (!window.confirm(`Reset health for ${comboKey} / ${groupKey}? This clears health state for every target in this group.`)) return;

  try {
    button.disabled = true;
    await requestManagementJSON(HEALTH_RESET_API, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        comboAliasKey: comboKey,
        groupKey,
      }),
    });
    await refreshHealth();
  } catch (error) {
    showToast(`Failed to reset health: ${error.message}`, 'error');
  } finally {
    button.disabled = false;
  }
}

function initializeHealthEvents() {
  const healthRefreshBtn = document.getElementById('health-refresh');
  if (healthRefreshBtn && !healthRefreshBtn.dataset.healthEventsBound) {
    healthRefreshBtn.addEventListener('click', refreshHealth);
    healthRefreshBtn.dataset.healthEventsBound = 'true';
  }
}
