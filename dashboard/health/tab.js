'use strict';

const HEALTH_API = '/v0/management/plugins/model-combo/health/list';
const HEALTH_RESET_API = '/v0/management/plugins/model-combo/health/reset';

let healthState = {
  polling: false,
  pollTimer: 0,
  lastRefresh: null,
};

function startHealthDashboard() {
  if (healthState.polling) return;
  healthState.polling = true;
  refreshHealth();
  scheduleHealthPoll();
}

function scheduleHealthPoll() {
  window.clearTimeout(healthState.pollTimer);
  healthState.pollTimer = window.setTimeout(() => {
    if (healthState.polling) {
      refreshHealth();
      scheduleHealthPoll();
    }
  }, 5000);
}

function stopHealthPolling() {
  healthState.polling = false;
  window.clearTimeout(healthState.pollTimer);
}

async function refreshHealth() {
  const healthLoadingEl = document.getElementById('health-loading');
  const healthGridEl = document.getElementById('health-grid');
  const healthEmptyEl = document.getElementById('health-empty');
  const healthErrorEl = document.getElementById('health-error');

  try {
    healthLoadingEl.hidden = false;
    healthGridEl.hidden = true;
    healthEmptyEl.hidden = true;
    healthErrorEl.hidden = true;

    const response = await requestManagementJSON(HEALTH_API);

    if (!response || !response.comboHealth) {
      throw new Error('Invalid health response format');
    }

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
    console.error('Failed to load health:', error);
    healthLoadingEl.hidden = true;
    healthGridEl.hidden = true;
    healthEmptyEl.hidden = true;
    healthErrorEl.hidden = false;
    healthErrorEl.textContent = error.message || 'Failed to load health data';
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

  const isCircuitOpen = health.circuitOpen === true;
  const failures = Number.isFinite(health.consecutiveFailures) ? health.consecutiveFailures : 0;
  const status = isCircuitOpen ? 'unhealthy' : failures > 0 ? 'warning' : 'healthy';
  const statusText = isCircuitOpen ? 'Circuit open' : failures > 0 ? 'Failures' : 'Healthy';
  const now = new Date();
  const coolUntil = new Date(health.coolUntil);
  const circuitOpenUntil = new Date(health.circuitOpenUntil);

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
  if (circuitOpenUntil > now) {
    appendHealthStat(stats, 'Circuit opens until', formatSecondsRemaining(Math.ceil((circuitOpenUntil - now) / 1000)));
  }
  if (Number.isFinite(health.circuitReopenCount) && health.circuitReopenCount > 0) {
    appendHealthStat(stats, 'Circuit reopen count', String(health.circuitReopenCount));
  }
  if (health.halfOpenOccupied === true) appendHealthStat(stats, 'Half-open', 'Active probe');

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
  if (healthRefreshBtn) healthRefreshBtn.addEventListener('click', refreshHealth);
}
