import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { test } from 'node:test';
import { chromium } from 'playwright-core';

const defaultChrome = '/usr/bin/google-chrome';

function localDateTime(date) {
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

async function waitForLayout(page) {
  await page.evaluate(() => new Promise((resolve) => {
    requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(resolve, 100)));
  }));
}

async function routeAliases(page) {
  return page.locator('.route-card input[data-field="alias"]').evaluateAll((inputs) => inputs.map((input) => input.value));
}

async function waitForUsageReady(page) {
  await page.waitForFunction(() => {
    const panel = document.querySelector('#usage-panel');
    const status = document.querySelector('#usage-status')?.textContent || '';
    return panel?.getAttribute('aria-busy') !== 'true' && !/^(Loading|Refreshing|Open Usage)/.test(status);
  }, { timeout: 20_000 });
  const status = await page.locator('#usage-status').textContent();
  assert.doesNotMatch(status || '', /Refresh failed|could not be loaded|could not be saved/i, status || '');
}

function dashboardEnvironment() {
  const dashboardUrl = process.env.DASHBOARD_URL;
  const managementKey = process.env.DASHBOARD_MANAGEMENT_KEY;
  const executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || defaultChrome;
  assert.ok(dashboardUrl, 'DASHBOARD_URL must point to a disposable local CPA instance');
  assert.ok(managementKey, 'DASHBOARD_MANAGEMENT_KEY must be set for that instance');
  assert.ok(existsSync(executablePath), `Chromium executable does not exist: ${executablePath}`);
  assert.ok(['127.0.0.1', 'localhost', '::1'].includes(new URL(dashboardUrl).hostname), 'DASHBOARD_URL must use loopback');
  return { dashboardUrl, managementKey, executablePath };
}

test('dashboard tabs retain their browser interactions after source assembly', { timeout: 120_000 }, async () => {
  const dashboardUrl = process.env.DASHBOARD_URL;
  const managementKey = process.env.DASHBOARD_MANAGEMENT_KEY;
  const executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || defaultChrome;

  assert.ok(dashboardUrl, 'DASHBOARD_URL must point to a disposable local CPA instance');
  assert.ok(managementKey, 'DASHBOARD_MANAGEMENT_KEY must be set for that instance');
  assert.equal(process.env.DASHBOARD_E2E_ALLOW_MUTATIONS, '1', 'set DASHBOARD_E2E_ALLOW_MUTATIONS=1 only for an isolated test instance');
  assert.ok(existsSync(executablePath), `Chromium executable does not exist: ${executablePath}`);

  const parsedUrl = new URL(dashboardUrl);
  assert.ok(['127.0.0.1', 'localhost', '::1'].includes(parsedUrl.hostname), 'DASHBOARD_URL must use loopback');

  const browser = await chromium.launch({
    executablePath,
    headless: true,
    args: ['--no-sandbox', '--disable-setuid-sandbox'],
  });
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  const pageErrors = [];
  const failedApiResponses = [];
  page.on('pageerror', (error) => pageErrors.push(error.message));
  page.on('response', (response) => {
    if (response.url().includes('/v0/management/') && response.status() >= 400) {
      failedApiResponses.push(`${response.status()} ${new URL(response.url()).pathname}`);
    }
  });

  try {
    await page.goto(dashboardUrl, { waitUntil: 'domcontentloaded' });
    await page.locator('#management-key').fill(managementKey);
    await page.locator('#connect').click();
    await page.locator('#workspace').waitFor({ state: 'visible' });
    await page.locator('#model-status[data-tone="ready"]').waitFor();

    assert.equal(await page.locator('[role="tab"]').count(), 3);
    assert.equal(await page.locator('#configuration-tab').getAttribute('aria-selected'), 'true');
    assert.deepEqual(await routeAliases(page), [], 'the disposable E2E instance should start without routes');

    for (let index = 0; index < 5; index++) {
      await page.locator('#add-route').click();
      let card = page.locator('.route-card').nth(index);
      await card.locator('input[data-field="alias"]').fill(`dashboard-e2e-${index}`);
      if (index === 0) {
        await card.locator('select[data-field="strategy"]').selectOption('round-robin');
        card = page.locator('.route-card').nth(index);
      }
      await card.locator('select[data-target-field="model"]').selectOption('dashboard-e2e-model-a');
      if (index === 0) await card.locator('input[data-target-field="weight"]').fill('3');
    }
    const modelOptions = await page.locator('.route-card').first().locator('select[data-target-field="model"] option').evaluateAll((options) => options.map((option) => option.value));
    assert.ok(modelOptions.includes('dashboard-e2e-model-a'));
    assert.ok(modelOptions.includes('dashboard-e2e-model-b'));

    let firstRoute = page.locator('.route-card').first();
    await firstRoute.locator('button[data-action="add-target"]').click();
    firstRoute = page.locator('.route-card').first();
    await firstRoute.locator('.target-row').nth(1).locator('select[data-target-field="model"]').selectOption('dashboard-e2e-model-b');
    await firstRoute.locator('.target-row').nth(1).locator('input[data-target-field="weight"]').fill('1');
    await firstRoute.locator('.target-row').nth(1).locator('button[data-action="target-up"]').click();
    firstRoute = page.locator('.route-card').first();
    assert.deepEqual(await firstRoute.locator('select[data-target-field="model"]').evaluateAll((selects) => selects.map((select) => select.value)), [
      'dashboard-e2e-model-b',
      'dashboard-e2e-model-a',
    ]);
    await firstRoute.locator('.target-row').first().locator('button[data-action="target-down"]').click();

    const beforeAliases = Array.from({ length: 5 }, (_, index) => `dashboard-e2e-${index}`);
    const moveUp = page.locator('.route-card[data-index="3"] button[data-action="up"]');
    await moveUp.scrollIntoViewIfNeeded();
    await waitForLayout(page);
    const scrollBefore = await page.evaluate(() => window.scrollY);
    await moveUp.click();
    await waitForLayout(page);
    const scrollAfter = await page.evaluate(() => window.scrollY);
    assert.ok(Math.abs(scrollBefore - scrollAfter) <= 1, `route reorder changed scrollY from ${scrollBefore} to ${scrollAfter}`);
    assert.deepEqual(await routeAliases(page), [beforeAliases[0], beforeAliases[1], beforeAliases[3], beforeAliases[2], beforeAliases[4]]);
    assert.equal(await page.evaluate(() => document.activeElement?.dataset.action), 'up');
    await page.keyboard.press('Enter');
    await waitForLayout(page);
    assert.deepEqual(await routeAliases(page), [beforeAliases[0], beforeAliases[3], beforeAliases[1], beforeAliases[2], beforeAliases[4]]);
    await page.locator('.route-card[data-index="1"] button[data-action="down"]').click();
    await page.locator('.route-card[data-index="2"] button[data-action="down"]').click();
    assert.deepEqual(await routeAliases(page), beforeAliases);

    await page.locator('#add-route').click();
    await page.locator('.route-card').last().locator('button[data-action="remove"]').click();
    assert.deepEqual(await routeAliases(page), beforeAliases);
    await page.locator('#save').click();
    try {
      await page.waitForFunction(() => document.querySelector('#save-state')?.textContent === 'Saved in CPA', null, { timeout: 20_000 });
    } catch (error) {
      throw new Error(`Save did not complete: state=${await page.locator('#save-state').textContent()}, toast=${await page.locator('#toast').textContent()}, pageErrors=${JSON.stringify(pageErrors)}, failedResponses=${JSON.stringify(failedApiResponses)}`, { cause: error });
    }

    await page.getByRole('tab', { name: 'Usage tracking' }).click();
    await page.locator('#usage-panel').waitFor({ state: 'visible' });
    await waitForUsageReady(page);
    for (const selector of [
      '#usage-range', '#usage-granularity', '#usage-router-model', '#usage-provider-model',
      '#usage-source', '#usage-service-tier', '#usage-result', '#usage-refresh',
      '#group-dimension', '#group-page-size', '#request-page-size', '#token-chart',
      '#model-chart', '#cost-chart', '#efficiency-chart',
    ]) assert.equal(await page.locator(selector).count(), 1, `${selector} should be present`);

    await page.locator('#usage-range').selectOption('custom');
    assert.equal(await page.locator('#usage-custom-range').isVisible(), true);
    const to = new Date();
    const from = new Date(to.getTime() - 60 * 60 * 1000);
    await page.locator('#usage-from').fill(localDateTime(from));
    await page.locator('#usage-from').dispatchEvent('change');
    await page.locator('#usage-to').fill(localDateTime(to));
    await page.locator('#usage-to').dispatchEvent('change');
    await page.locator('#usage-granularity').selectOption('day');
    await page.locator('#group-dimension').selectOption('provider');
    await page.locator('#group-page-size').selectOption('25');
    await page.locator('#request-page-size').selectOption('25');
    await page.locator('#usage-result').selectOption('success');
    await page.locator('#usage-refresh').click();
    await waitForUsageReady(page);

    const sortButton = page.locator('#group-headers button[data-sort-field]').first();
    await sortButton.click();
    await waitForUsageReady(page);
    await page.locator('#group-columns summary').click();
    const column = page.locator('#group-column-options input[data-column-kind="group"]').first();
    const columnKey = await column.getAttribute('data-column-key');
    await column.uncheck();
    assert.equal(await page.locator(`#group-table th[data-column="${columnKey}"]`).count(), 0);
    await page.locator(`#group-column-options input[data-column-kind="group"][data-column-key="${columnKey}"]`).check();
    await page.locator('#group-columns summary').click();

    const tokenLegend = page.locator('[data-token-series="input"]');
    await tokenLegend.click();
    assert.equal(await tokenLegend.getAttribute('aria-pressed'), 'false');
    await tokenLegend.click();
    assert.equal(await tokenLegend.getAttribute('aria-pressed'), 'true');
    await page.getByRole('button', { name: 'Zoom token trend in' }).click();
    await page.getByRole('button', { name: 'Reset token trend zoom' }).click();

    await page.locator('#usage-pricing').click();
    await page.locator('#pricing-dialog[open]').waitFor();
    await page.locator('#pricing-model-name').fill('dashboard-e2e-price-model');
    await page.locator('#pricing-add').click();
    const priceRow = page.locator('.price-row').first();
    assert.equal(await priceRow.locator('[data-price-field="name"]').inputValue(), 'dashboard-e2e-price-model');
    await priceRow.locator('[data-price-field="input"]').fill('1.25');
    await priceRow.locator('[data-price-field="output"]').fill('2.5');
    await priceRow.locator('[data-price-field="cache_read"]').fill('0.1');
    await priceRow.locator('[data-price-field="cache_creation"]').fill('0.2');
    await priceRow.locator('[data-price-field="accounting_mode"]').selectOption('input_excludes_cache');
    await page.locator('#pricing-save').click();
    await page.locator('#pricing-dialog[open]').waitFor({ state: 'detached' });
    await page.locator('#usage-pricing').click();
    await page.locator('.price-row').waitFor();
    const savedPrice = page.locator('.price-row').first();
    assert.equal(await savedPrice.locator('[data-price-field="name"]').inputValue(), 'dashboard-e2e-price-model');
    assert.equal(await savedPrice.locator('[data-price-field="input"]').inputValue(), '1.25');
    assert.equal(await savedPrice.locator('[data-price-field="accounting_mode"]').inputValue(), 'input_excludes_cache');
    await page.locator('#pricing-cancel').click();

    await page.locator('#usage-reset').click();
    await page.locator('#reset-dialog[open]').waitFor();
    await page.locator('#reset-confirmation').fill('reset');
    assert.equal(await page.locator('#reset-confirm').isEnabled(), true);
    await page.locator('#reset-confirm').click();
    await page.locator('#reset-dialog[open]').waitFor({ state: 'detached' });
    await waitForUsageReady(page);

    let requestCalls = 0;
    // These identity values represent historical records, not live CPA callback binding.
    const requestPage = {
      schema_version: 2, generated_at: new Date().toISOString(), price_book_revision: 0, total: 4, offset: 0, limit: 50,
      items: [
        { sequence: 101, router_request_id: 'legacy-first', request_id: '1234', request_endpoint: 'POST /v1/chat/completions', requested_model: 'route-a', requested_at: '2026-01-01T00:00:00Z', router_model: 'route-a', provider_model: 'provider-b', result: 'success', latency_ns: 3000000000, attempts: [
          { requested_at: '2026-01-01T00:00:00Z', router_model: 'route-a', provider_model: 'provider-a', result: 'http_429', latency_ns: 1000000000 },
          { requested_at: '2026-01-01T00:00:01Z', router_model: 'route-a', provider_model: 'provider-b', result: 'success', latency_ns: 2000000000 },
        ] },
        { sequence: 102, request_id: '5678', request_endpoint: 'POST /v1/responses', requested_model: 'provider-c', requested_at: '2026-01-01T00:00:02Z', attribution: 'direct', provider_model: 'provider-c', result: 'success', latency_ns: 1000000000, attempts: [
          { requested_at: '2026-01-01T00:00:02Z', attribution: 'direct', provider_model: 'provider-c', result: 'success', latency_ns: 1000000000 },
        ] },
        { sequence: 103, requested_at: '2026-01-01T00:00:03Z', attribution: 'direct', provider_model: 'provider-d', result: 'success', latency_ns: 1000000000 },
        { sequence: 104, router_request_id: 'legacy-router', router_model: 'old-route', result: 'success', attempts: [
          { provider_model: 'old-provider', result: 'success' },
        ] },
      ],
    };
    const requestRoute = async (route) => { requestCalls++; await route.fulfill({ contentType: 'application/json', body: JSON.stringify(requestPage) }); };
    const dashboardRoute = async (route) => {
      const response = await route.fetch();
      const dashboard = await response.json();
      await route.fulfill({ response, json: { ...dashboard, requests: requestPage } });
    };
    await page.route('**/usage/requests?**', requestRoute);
    await page.route('**/usage/dashboard?**', dashboardRoute);
    await page.locator('#request-page-size').selectOption('50');
    await waitForUsageReady(page);
    const toggle = page.getByRole('button', { name: 'Expand 2 executions for request', exact: true }).first();
    assert.deepEqual(await page.locator('#request-headers th').evaluateAll((cells) => cells.slice(0, 3).map((cell) => cell.textContent)), ['Request ID', 'API path', 'Requested model']);
    assert.equal(await page.locator('#usage-requests > tr').count(), 8);
    assert.equal(await page.locator('.request-attempt-row[hidden]').count(), 4);
    assert.equal(await page.locator('#request-page-status').textContent(), '1–4 of 4 requests');
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false');
    const controls = (await toggle.getAttribute('aria-controls')).split(' ');
    assert.equal(controls.length, 2);
    assert.deepEqual(await Promise.all(controls.map((id) => page.locator('#' + id).count())), [1, 1]);
    assert.equal(await page.getByRole('button', { name: 'Expand 1 executions for request', exact: true }).count(), 2, 'direct and legacy grouped requests are expandable');
    assert.equal(await page.locator('.request-parent-row').nth(2).locator('button').count(), 0, 'legacy standalone row has no children to expand');
    assert.deepEqual(await page.locator('.request-parent-row').first().locator('td').evaluateAll((cells) => cells.slice(0, 3).map((cell) => cell.textContent)), ['›1234 2 executions', 'POST /v1/chat/completions', 'route-a']);
    await toggle.click();
    const expandedToggle = page.getByRole('button', { name: 'Collapse 2 executions for request', exact: true });
    assert.equal(await expandedToggle.getAttribute('aria-expanded'), 'true');
    assert.equal(await expandedToggle.evaluate((element) => element === document.activeElement), true, 'toggle keeps focus after rerender');
    assert.equal(await page.locator('#' + controls[0]).isVisible(), true);
    assert.equal(await page.locator('.request-attempt-row[hidden]').count(), 2);
    assert.deepEqual(await page.locator('#' + controls.join(', #')).locator('.request-ordinal').allTextContents(), ['Execution 1', 'Execution 2']);
    assert.deepEqual(await page.locator('#' + controls.join(', #')).locator('td:nth-child(6)').allTextContents(), ['provider-a', 'provider-b']);
    assert.deepEqual(await page.locator('#' + controls.join(', #')).locator('.status-pill').allTextContents(), ['Failed · HTTP 429', 'Success']);
    assert.equal(await page.locator('.request-parent-row').first().locator('.status-pill').textContent(), 'Success');
    assert.equal(requestCalls, 1, 'tree toggle should not refetch requests');
    requestPage.items[0].sequence = 999;
    const refreshed = page.waitForResponse((response) => response.url().includes('/usage/dashboard?') && response.ok());
    await page.locator('#usage-refresh').click();
    assert.equal((await (await refreshed).json()).requests.items[0].sequence, 999);
    await waitForUsageReady(page);
    assert.equal(requestCalls, 1, 'full refresh should not fetch scoped requests');
    assert.equal(await page.getByRole('button', { name: 'Collapse 2 executions for request', exact: true }).getAttribute('aria-expanded'), 'true', 'expansion persists across refresh');
    assert.equal(await page.locator('.request-attempt-row[hidden]').count(), 2);
    await page.locator('#request-columns summary').click();
    const requestOption = (key) => page.locator(`#request-column-options input[data-column-key="${key}"]`);
    await requestOption('request_id').uncheck();
    assert.equal(await page.locator('#request-headers th[data-column="request_id"]').count(), 0);
    assert.equal(await page.locator('.request-parent-row').first().locator('td').first().textContent(), '›POST /v1/chat/completions 2 executions');
    assert.equal(await page.getByRole('button', { name: 'Collapse 2 executions for request', exact: true }).getAttribute('aria-expanded'), 'true');
    assert.match(await page.locator('#' + controls[0]).locator('td').first().textContent(), /^Execution 1POST \/v1\/chat\/completions$/);
    const savedPreferences = page.waitForResponse((response) => response.url().includes('/usage/preferences') && response.request().method() === 'PUT' && response.ok() && JSON.parse(response.request().postData()).hidden_request_columns.includes('request_endpoint'));
    await requestOption('request_endpoint').uncheck();
    assert.equal(await page.locator('#request-headers th[data-column="request_endpoint"]').count(), 0);
    assert.equal(await page.locator('.request-parent-row').first().locator('td').first().textContent(), '›route-a 2 executions');
    assert.match(await page.locator('#' + controls[0]).locator('td').first().textContent(), /^Execution 1route-a$/);
    await savedPreferences;
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.locator('#workspace').waitFor({ state: 'visible' });
    await page.getByRole('tab', { name: 'Usage tracking' }).click();
    await waitForUsageReady(page);
    await page.locator('#request-columns summary').click();
    assert.equal(await requestOption('request_id').isChecked(), false, 'hidden Request ID survives preference reload');
    assert.equal(await requestOption('request_endpoint').isChecked(), false, 'hidden API path survives preference reload');
    await page.locator('#request-page-size').selectOption('25');
    await waitForUsageReady(page);
    await page.locator('#request-page-size').selectOption('50');
    await waitForUsageReady(page);
    await page.getByRole('button', { name: 'Expand 2 executions for request', exact: true }).click();
    assert.equal(await page.getByRole('button', { name: 'Collapse 2 executions for request', exact: true }).evaluate((element) => element === document.activeElement), true);
    assert.match(await page.locator('.request-attempt-row').first().locator('td').first().textContent(), /^Execution 1route-a$/);
    await page.locator('.request-parent-row').nth(3).locator('button[data-request-toggle]').click();
    assert.equal(await page.locator('.request-parent-row').nth(3).locator('button[data-request-toggle]').getAttribute('aria-expanded'), 'true');
    const checkedKeys = await page.locator('#request-column-options input:checked').evaluateAll((inputs) => inputs.map((input) => input.dataset.columnKey));
    if (!(await requestOption('result').isChecked())) await requestOption('result').check();
    for (const key of checkedKeys.filter((key) => key !== 'result')) await requestOption(key).uncheck();
    assert.equal(await requestOption('result').isDisabled(), true, 'last visible column cannot be hidden');
    assert.equal(await page.locator('#request-headers th').count(), 1);
    assert.equal(await page.locator('.request-parent-row').first().locator('.status-pill').textContent(), 'Success');
    assert.equal(await page.locator('.request-attempt-row').first().locator('.status-pill').textContent(), 'Failed · HTTP 429');
    assert.match(await page.locator('.request-attempt-row').first().locator('td').textContent(), /^Execution 1Failed · HTTP 429$/);
    await page.getByRole('button', { name: 'Collapse 2 executions for request', exact: true }).click();
    await page.getByRole('button', { name: 'Expand 2 executions for request', exact: true }).click();
    assert.equal(await page.locator('.request-attempt-row').first().isVisible(), true);
    for (const key of checkedKeys) if (!(await requestOption(key).isChecked())) await requestOption(key).check();
    if (!checkedKeys.includes('result')) await requestOption('result').uncheck();
    await requestOption('request_id').check();
    await requestOption('request_endpoint').check();
    assert.equal(await page.locator('.request-parent-row').first().locator('td').first().textContent(), '›1234 2 executions');
    await page.unroute('**/usage/requests?**', requestRoute);
    await page.unroute('**/usage/dashboard?**', dashboardRoute);

    await page.setViewportSize({ width: 1440, height: 900 });
    await page.getByRole('tab', { name: 'Configuration' }).click();
    assert.equal(await page.locator('#configuration-panel').isVisible(), true);
    await page.getByRole('tab', { name: 'Usage tracking' }).click();
    assert.equal(await page.locator('#usage-panel').isVisible(), true);

    assert.deepEqual(pageErrors, [], 'dashboard should not raise browser runtime errors');
    assert.deepEqual(failedApiResponses, [], 'dashboard management requests should succeed');
  } finally {
    await browser.close();
  }
});

test('quota settings persist and unknown accounts display the configured default', { timeout: 120_000 }, async () => {
  const { dashboardUrl, managementKey, executablePath } = dashboardEnvironment();
  const browser = await chromium.launch({ executablePath, headless: true, args: ['--no-sandbox', '--disable-setuid-sandbox'] });
  const page = await browser.newPage();
  let config = { enabled: true, priority: 0, routes: [] };
  try {
    await page.route('**/v0/management/plugins/model-combo/accounts', route => route.fulfill({ json: { credentials: [] } }));
    await page.route('**/v0/management/plugins/model-combo/config', route => {
      if (route.request().method() === 'PATCH') config = route.request().postDataJSON();
      return route.fulfill({ json: config });
    });
    await page.route('**/v0/management/plugins/model-combo/validate', route => route.fulfill({ json: {} }));
    await page.route('**/v0/management/plugins/model-combo/quota-status', route => route.fulfill({ json: { running: true, active_keys: 1, credentials: [{ label: 'Test account', provider: 'test', state: 'unknown', reason: 'unsupported_provider', effective_percent: config.quota_polling?.default_unknown_quota_percent ?? 50, score_source: 'configured-default', windows: [] }] } }));
    await page.goto(dashboardUrl, { waitUntil: 'domcontentloaded' });
    if (await page.locator('#auth-dock').isVisible()) { await page.locator('#management-key').fill(managementKey); await page.locator('#connect').click(); }
    await page.locator('#workspace').waitFor({ state: 'visible' });
    await page.getByRole('tab', { name: 'Quota tracking' }).click();
    await page.locator('#quota-default').fill('35');
    await page.locator('#quota-save').click();
    await page.waitForFunction(() => document.querySelector('#quota-save-state').textContent === 'Saved in CPA');
    assert.equal(config.quota_polling.default_unknown_quota_percent, 35);
    await page.reload();
    if (await page.locator('#auth-dock').isVisible()) { await page.locator('#management-key').fill(managementKey); await page.locator('#connect').click(); }
    await page.locator('#workspace').waitFor({ state: 'visible' });
    await page.getByRole('tab', { name: 'Quota tracking' }).click();
    assert.equal(await page.locator('#quota-default').inputValue(), '35');
    await page.getByText('unknown · Effective 35% (configured-default) · unsupported_provider').waitFor();
  } finally { await browser.close(); }
});

test('account restrictions filter targets and reject stale selections', { timeout: 120_000 }, async () => {
  const { dashboardUrl, managementKey, executablePath } = dashboardEnvironment();
  const browser = await chromium.launch({ executablePath, headless: true, args: ['--no-sandbox', '--disable-setuid-sandbox'] });
  const page = await browser.newPage();
  const requests = [];
  let saved;
  try {
    await page.route('**/v0/management/plugins/model-combo/accounts', route => route.fulfill({ json: { credentials: [
      { id: 'account-a', provider: 'Provider A', label: 'First', models: [{ id: 'model-a', entitlement: 'unknown' }] },
      { id: 'account-b', provider: 'Provider B', label: 'Second', models: [{ id: 'model-b', entitlement: 'unknown' }] },
    ] } }));
    await page.route('**/v0/management/plugins/model-combo/config', route => {
      if (route.request().method() === 'PATCH') {
        saved = route.request().postDataJSON();
        return route.fulfill({ json: {} });
      }
      return route.fulfill({ json: { enabled: true, priority: 0, routes: [{ alias: 'auto', strategy: 'priority', cooldown_seconds: 60, allowed_accounts: ['missing'], targets: [{ model: 'model-a', weight: 1 }] }] } });
    });
    await page.route('**/v0/management/plugins/model-combo/validate', route => route.fulfill({ json: {} }));
    page.on('request', request => requests.push(new URL(request.url()).pathname));
    await page.goto(dashboardUrl, { waitUntil: 'domcontentloaded' });
    if (await page.locator('#auth-dock').isVisible()) {
      await page.locator('#management-key').fill(managementKey);
      await page.locator('#connect').click();
    }
    await page.locator('#model-status[data-tone="ready"]').waitFor();
    const card = page.locator('.route-card').first();
    assert.match(await card.locator('fieldset').textContent(), /Only the selected accounts are used/i);
    await card.locator('[data-account-id="account-a"]').check();
    await page.locator('#save').click();
    assert.equal(saved, undefined, 'stale accounts must prevent saving');
    await card.locator('[data-account-id="missing"]').uncheck();
    assert.match(await card.locator('fieldset').textContent(), /No accounts selected|Only the selected accounts/i);
    const options = card.locator('select[data-target-field="model"] option');
    assert.match(await options.filter({ hasText: 'model-a' }).textContent(), /entitlement unknown/);
    assert.equal(await options.filter({ hasText: 'model-b' }).count(), 0);
    await card.locator('[data-account-id="account-b"]').check();
    assert.equal(await options.filter({ hasText: 'model-b' }).count(), 1);
    assert.match(await card.locator('[data-account-id="account-b"]').locator('..').textContent(), /model-b \(unknown\)/);
    await page.locator('#save').click();
    assert.equal(saved, undefined, 'an account supporting no target must prevent saving');
    await card.locator('button[data-action="add-target"]').click();
    await card.locator('.target-row').nth(1).locator('select[data-target-field="model"]').selectOption('model-b');
    await page.locator('#save').click();
    await page.waitForFunction(() => document.querySelector('#save-state').textContent === 'Saved in CPA');
    assert.deepEqual(saved.routes[0].allowed_accounts, ['account-a', 'account-b']);
    assert.ok(!requests.includes('/v1/models') && !requests.includes('/v0/management/api-keys'));
  } finally {
    await browser.close();
  }
});

test('request details render one expandable parent per trace with ordered executions', { timeout: 120_000 }, async () => {
  const { dashboardUrl, managementKey, executablePath } = dashboardEnvironment();
  const browser = await chromium.launch({ executablePath, headless: true, args: ['--no-sandbox', '--disable-setuid-sandbox'] });
  const page = await browser.newPage({ viewport: { width: 1600, height: 900 } });
  const pageErrors = [];
  page.on('pageerror', (error) => pageErrors.push(error.message));
  const now = Date.now();
  const at = (seconds) => new Date(now - 60_000 + seconds * 1000).toISOString();
  const counters = { requests: 4, failed_requests: 2, input_tokens: 0, output_tokens: 0, reasoning_tokens: 0, cached_tokens: 0, cache_read_tokens: 0, effective_cache_read_tokens: 0, cache_creation_tokens: 0, total_tokens: 0 };
  const child = (sequence, extra) => ({ sequence, requested_at: at(sequence), attribution: 'routed', router_model: 'auto', trace_id: 'aaaa1111', request_id: `exec-${sequence}`, provider: 'openai', source: 'openai', result: 'success', ...extra });
  const items = [
    {
      sequence: 1, requested_at: at(1), attribution: 'routed', router_model: 'auto', trace_id: 'aaaa1111', lifecycle_request_id: 'life-1',
      request_endpoint: '/v1/chat/completions', requested_model: 'auto', result: 'success', attempt_count: 2,
      attempts: [
        child(1, { provider_model: 'gpt-a', credential_label: 'Work account', result: 'http_429', failed: true, status_code: 429 }),
        child(2, { provider_model: 'gpt-b', credential_label: '', auth_id: 'raw-credential-id-must-stay-hidden' }),
      ],
    },
    {
      sequence: 3, requested_at: at(3), attribution: 'routed', router_model: 'auto', trace_id: 'bbbb2222', lifecycle_request_id: 'life-2',
      request_endpoint: '/v1/responses', requested_model: 'auto', result: 'unknown', attempt_count: 1,
      correlation_warning: 'trace shared by several inbound requests',
      attempts: [child(3, { trace_id: 'bbbb2222', provider_model: 'gpt-c', credential_label: 'Backup' })],
    },
    {
      sequence: 4, requested_at: at(4), attribution: 'routed', router_model: 'auto', trace_id: 'cccc3333', lifecycle_request_id: 'life-3',
      request_endpoint: '/v1/chat/completions', requested_model: 'auto', result: 'failed',
    },
  ];
  const preferenceWrites = [];
  try {
    await page.route('**/v0/management/plugins/model-combo/accounts', route => route.fulfill({ json: { credentials: [] } }));
    await page.route('**/v0/management/plugins/model-combo/config', route => route.fulfill({ json: { enabled: true, priority: 0, routes: [] } }));
    await page.route('**/v0/management/plugins/model-combo/validate', route => route.fulfill({ json: {} }));
    await page.route('**/v0/management/plugins/model-combo/usage/**', route => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      if (path.endsWith('/preferences')) {
        if (request.method() === 'PUT') {
          preferenceWrites.push(request.postDataJSON());
          return route.fulfill({ json: request.postDataJSON() });
        }
        return route.fulfill({ json: { hidden_request_columns: ['trace_id'] } });
      }
      if (path.endsWith('/dashboard')) {
        const generatedAt = new Date().toISOString();
        return route.fulfill({ json: {
          schema_version: 2, generated_at: generatedAt, price_book_revision: 0,
          overview: { generated_at: generatedAt, summary: counters, costs: {}, series: [], router_models: [], provider_models: [], sources: [], service_tiers: [], results: [] },
          groups: { generated_at: generatedAt, dimension: 'provider_model', total: 0, offset: 0, limit: 50, items: [] },
          requests: { generated_at: generatedAt, total: items.length, offset: 0, limit: 50, items },
        } });
      }
      return route.fulfill({ json: {} });
    });
    await page.goto(dashboardUrl, { waitUntil: 'domcontentloaded' });
    if (await page.locator('#auth-dock').isVisible()) {
      await page.locator('#management-key').fill(managementKey);
      await page.locator('#connect').click();
    }
    await page.locator('#workspace').waitFor({ state: 'visible' });
    await page.getByRole('tab', { name: 'Usage tracking' }).click();
    await waitForUsageReady(page);

    const columns = await page.locator('#request-headers th').evaluateAll((headers) => headers.map((header) => header.dataset.column));
    assert.ok(!columns.includes('trace_id'), 'a persisted hidden trace column must be accepted and hidden');
    for (const key of ['request_endpoint', 'requested_model', 'credential']) assert.ok(columns.includes(key), `${key} column should be listed`);
    const cells = async (row) => {
      const texts = await row.locator('td').allTextContents();
      return Object.fromEntries(columns.map((key, index) => [key, texts[index].trim()]));
    };
    const parents = page.locator('#usage-requests tr.request-parent-row');
    const children = page.locator('#usage-requests tr.request-attempt-row');
    assert.equal(await parents.count(), 3);
    assert.equal(await children.count(), 3);
    assert.equal(await children.first().isVisible(), false, 'executions start collapsed');

    const first = await cells(parents.nth(0));
    assert.equal(first.request_endpoint, '/v1/chat/completions');
    assert.equal(first.requested_model, 'auto');
    assert.equal(first.credential, 'Work account, Unavailable');
    assert.match(first.provider_model, /gpt-a, gpt-b/);
    assert.match(first.result, /Success/);

    const toggle = parents.nth(0).locator('button[data-request-toggle]');
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false');
    await toggle.click();
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true');
    assert.equal(await children.nth(0).isVisible(), true);
    assert.equal(await children.nth(1).isVisible(), true);
    assert.equal(await children.nth(2).isVisible(), false, 'other traces stay collapsed');
    const failed = await cells(children.nth(0));
    const succeeded = await cells(children.nth(1));
    assert.equal(await children.nth(0).locator('.request-ordinal').textContent(), 'Execution 1');
    assert.equal(failed.provider_model, 'gpt-a');
    assert.equal(failed.credential, 'Work account');
    assert.equal(failed.request_endpoint, '/v1/chat/completions');
    assert.match(failed.result, /HTTP 429/);
    assert.equal(await children.nth(1).locator('.request-ordinal').textContent(), 'Execution 2');
    assert.equal(succeeded.credential, 'Unavailable');
    assert.doesNotMatch(await page.locator('#usage-requests').textContent(), /raw-credential-id-must-stay-hidden/);

    const ambiguous = parents.nth(1);
    assert.match(await ambiguous.locator('.request-warning[role="note"]').textContent(), /Correlation warning: trace shared by several inbound requests/);
    assert.match((await cells(ambiguous)).result, /Unknown/);

    const lifecycleOnly = parents.nth(2);
    assert.equal(await lifecycleOnly.locator('button[data-request-toggle]').count(), 0, 'a parent without executions has nothing to expand');
    const orphan = await cells(lifecycleOnly);
    assert.equal(orphan.provider_model, 'No executions recorded');
    assert.equal(orphan.request_endpoint, '/v1/chat/completions');
    assert.match(orphan.result, /Failed/);

    const refreshed = page.waitForResponse((response) => response.url().includes('/usage/dashboard'));
    await page.locator('#usage-refresh').click();
    await refreshed;
    await waitForUsageReady(page);
    assert.equal(await page.locator('#usage-requests tr.request-parent-row').nth(0).locator('button[data-request-toggle]').getAttribute('aria-expanded'), 'true', 'expansion survives refresh');
    assert.equal(await children.nth(0).isVisible(), true);

    await page.locator('#usage-requests tr.request-parent-row').nth(0).locator('button[data-request-toggle]').focus();
    await page.keyboard.press('Enter');
    assert.equal(await children.nth(0).isVisible(), false, 'Enter collapses the executions');
    assert.equal(await page.evaluate(() => document.activeElement?.dataset.requestToggle), 'trace:aaaa1111', 'focus stays on the toggle');

    await page.locator('#request-columns summary').click();
    await page.locator('#request-column-options input[data-column-key="credential"]').uncheck();
    await page.waitForFunction(() => !document.querySelector('#request-table th[data-column="credential"]'));
    for (let attempt = 0; attempt < 40 && !preferenceWrites.length; attempt++) await page.waitForTimeout(50);
    const saved = preferenceWrites.at(-1);
    assert.deepEqual([...saved.hidden_request_columns].sort(), ['credential', 'trace_id']);
    assert.deepEqual(pageErrors, [], 'dashboard should not raise browser runtime errors');
  } finally {
    await browser.close();
  }
});
