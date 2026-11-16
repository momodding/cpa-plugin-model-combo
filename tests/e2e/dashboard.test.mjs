import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { test } from 'node:test';
import { chromium } from 'playwright-core';

const defaultChrome = '/usr/bin/google-chrome';

function dashboardEnvironment() {
  const dashboardUrl = process.env.DASHBOARD_URL;
  const managementKey = process.env.DASHBOARD_MANAGEMENT_KEY;
  const executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || defaultChrome;
  assert.ok(dashboardUrl, 'DASHBOARD_URL must point to a disposable local CPA instance');
  assert.ok(managementKey, 'DASHBOARD_MANAGEMENT_KEY must be set for that instance');
  assert.equal(process.env.DASHBOARD_E2E_ALLOW_MUTATIONS, '1', 'set DASHBOARD_E2E_ALLOW_MUTATIONS=1 only for an isolated test instance');
  assert.ok(existsSync(executablePath), `Chromium executable does not exist: ${executablePath}`);
  assert.ok(['127.0.0.1', 'localhost', '::1'].includes(new URL(dashboardUrl).hostname), 'DASHBOARD_URL must use loopback');
  return { dashboardUrl, managementKey, executablePath };
}

// Fixture format: CPAMC v1.27.0 src/utils/encryption.ts and src/services/storage/secureStorage.ts.
function cpamcStorage(value, version, host, userAgent) {
  const key = Buffer.from(`cli-proxy-api-webui::secure-storage|${host}${version === 1 ? `|${userAgent}` : ''}`);
  const bytes = Buffer.from(JSON.stringify(value));
  return `enc::v${version}::${Buffer.from(bytes.map((byte, index) => byte ^ key[index % key.length])).toString('base64')}`;
}

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

test('dashboard reuses CPAMC credentials and preserves manual recovery', { timeout: 120_000 }, async (t) => {
  const { dashboardUrl, managementKey, executablePath } = dashboardEnvironment();
  const { host, origin } = new URL(dashboardUrl);
  const fixtureUserAgent = 'CPAMC-session-fixture/1';
  const auth = (key) => ({ state: { apiBase: origin, managementKey: key, rememberPassword: Boolean(key) }, version: 0 });
  const stored = (value, version = 2) => cpamcStorage(value, version, host, fixtureUserAgent);
  const currentAuth = stored(auth(managementKey));
  const scenarios = [
    { name: 'v2 saved session', local: { 'cli-proxy-auth': currentAuth } },
    { name: 'v2 survives a changed user agent', local: { 'cli-proxy-auth': currentAuth }, userAgent: 'CPAMC-session-fixture/2' },
    { name: 'v1 saved session', local: { 'cli-proxy-auth': stored(auth(managementKey), 1) } },
    { name: 'v1 standalone key', session: { managementKey: stored(managementKey, 1) } },
    { name: 'legacy plaintext key', local: { managementKey } },
    { name: 'v2 overrides legacy entries', local: { 'cli-proxy-auth': currentAuth, managementKey: 'stale-fixture-key' }, session: { managementKey: 'stale-fixture-key', 'cli-proxy-auth': stored(auth('stale-fixture-key'), 1) } },
    { name: 'v2 session storage', session: { 'cli-proxy-auth': currentAuth } },
    { name: 'missing storage', fallback: true },
    { name: 'remembering disabled does not revive legacy keys', local: { 'cli-proxy-auth': stored(auth(undefined)), managementKey }, session: { managementKey }, fallback: true },
    { name: 'corrupt v2 does not revive legacy keys', local: { 'cli-proxy-auth': 'enc::v2::not-base64!', managementKey }, fallback: true },
    { name: 'corrupt v1 standalone key', session: { managementKey: 'enc::v1::not-base64!' }, fallback: true },
    { name: 'encoded non-JSON key', session: { managementKey: stored(managementKey).slice(0, -4) }, fallback: true },
    { name: 'unsupported saved session', local: { 'cli-proxy-auth': 'enc::v3::unsupported' }, fallback: true },
    { name: 'unsupported standalone key', session: { managementKey: 'enc::v3::unsupported' }, fallback: true },
    { name: 'rejected key is not retried after reload', local: { 'cli-proxy-auth': stored(auth('rejected-fixture-key')) }, fallback: true, rejected: true },
  ];
  const browser = await chromium.launch({ executablePath, headless: true, args: ['--no-sandbox', '--disable-setuid-sandbox'] });
  try {
    for (const scenario of scenarios) await t.test(scenario.name, async () => {
      const context = await browser.newContext({ userAgent: scenario.userAgent || fixtureUserAgent });
      try {
        const local = scenario.local || {};
        const session = scenario.session || {};
        await context.addInitScript(({ origin, local, session }) => {
          if (location.origin !== origin) return;
          for (const [key, value] of Object.entries(local)) localStorage.setItem(key, value);
          for (const [key, value] of Object.entries(session)) sessionStorage.setItem(key, value);
        }, { origin, local, session });
        const page = await context.newPage();
        const pageErrors = [];
        const managementRequests = [];
        page.on('pageerror', (error) => pageErrors.push(error.message));
        page.on('request', (request) => {
          if (new URL(request.url()).pathname.startsWith('/v0/management/')) managementRequests.push(request);
        });
        await page.goto(dashboardUrl, { waitUntil: 'domcontentloaded' });
        if (scenario.fallback) {
          await page.locator('#auth-dock').waitFor({ state: 'visible' });
          await page.waitForFunction(() => !document.getElementById('connect').disabled);
          assert.equal(await page.locator('#workspace').isVisible(), false);
          if (scenario.rejected) {
            assert.ok(managementRequests.length > 0);
            const requestCount = managementRequests.length;
            await page.reload({ waitUntil: 'domcontentloaded' });
            await page.locator('#auth-dock').waitFor({ state: 'visible' });
            assert.equal(managementRequests.length, requestCount, 'a rejected saved key must not be retried');
          } else assert.equal(managementRequests.length, 0, 'unavailable credentials must not reach the Management API');
          await page.locator('#management-key').fill(managementKey);
          await page.locator('#connect').click();
        }
        await page.locator('#workspace').waitFor({ state: 'visible' });
        await page.locator('#model-status[data-tone="ready"]').waitFor();
        assert.equal(await page.locator('#auth-dock').isVisible(), false);
        await page.getByRole('tab', { name: 'Usage tracking' }).click();
        await waitForUsageReady(page);
        if (!scenario.rejected) {
          assert.ok(managementRequests.length > 0);
          assert.ok(managementRequests.every((request) => request.headers().authorization === `Bearer ${managementKey}`));
        }
        const hostStorage = await page.evaluate(() => [localStorage, sessionStorage].map((storage) =>
          Object.fromEntries(['cli-proxy-auth', 'managementKey'].map((key) => [key, storage.getItem(key)]))));
        assert.deepEqual(hostStorage, [local, session].map((storage) =>
          Object.fromEntries(['cli-proxy-auth', 'managementKey'].map((key) => [key, storage[key] ?? null]))));
        assert.deepEqual(pageErrors, []);
      } finally {
        await context.close();
      }
    });
  } finally {
    await browser.close();
  }
});

test('dashboard tabs retain their browser interactions after source assembly', { timeout: 120_000 }, async () => {
  const { dashboardUrl, managementKey, executablePath } = dashboardEnvironment();

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

    assert.equal(await page.locator('[role="tab"]').count(), 2);
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
    await page.waitForFunction(() => document.querySelector('#save-state')?.textContent === 'Saved in CPA', { timeout: 20_000 });

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

test('account restrictions filter targets and reject stale selections', { timeout: 120_000 }, async () => {
  const { dashboardUrl, managementKey, executablePath } = dashboardEnvironment();
  const browser = await chromium.launch({ executablePath, headless: true, args: ['--no-sandbox', '--disable-setuid-sandbox'] });
  const page = await browser.newPage();
  const requests = [];
  let saved;
  try {
    await page.route('**/v0/management/plugins/model-router/accounts', route => route.fulfill({ json: { credentials: [
      { id: 'account-a', provider: 'Provider A', label: 'First', models: [{ id: 'model-a', entitlement: 'unknown' }] },
      { id: 'account-b', provider: 'Provider B', label: 'Second', models: [{ id: 'model-b', entitlement: 'unknown' }] },
    ] } }));
    await page.route('**/v0/management/plugins/model-router/config', route => {
      if (route.request().method() === 'PATCH') {
        saved = route.request().postDataJSON();
        return route.fulfill({ json: {} });
      }
      return route.fulfill({ json: { enabled: true, priority: 0, routes: [{ alias: 'auto', strategy: 'priority', cooldown_seconds: 60, allowed_accounts: ['missing'], targets: [{ model: 'model-a', weight: 1 }] }] } });
    });
    await page.route('**/v0/management/plugins/model-router/validate', route => route.fulfill({ json: {} }));
    page.on('request', request => requests.push(new URL(request.url()).pathname));
    await page.goto(dashboardUrl, { waitUntil: 'domcontentloaded' });
    if (await page.locator('#auth-dock').isVisible()) {
      await page.locator('#management-key').fill(managementKey);
      await page.locator('#connect').click();
    }
    await page.locator('#model-status[data-tone="ready"]').waitFor();
    const card = page.locator('.route-card').first();
    assert.match(await card.locator('fieldset').textContent(), /No accounts selected: all accounts allowed/i);
    await card.locator('[data-account-id="account-a"]').check();
    await page.locator('#save').click();
    assert.equal(saved, undefined, 'stale accounts must prevent saving');
    await card.locator('[data-account-id="missing"]').uncheck();
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
    await page.route('**/v0/management/plugins/model-router/accounts', route => route.fulfill({ json: { credentials: [] } }));
    await page.route('**/v0/management/plugins/model-router/config', route => route.fulfill({ json: { enabled: true, priority: 0, routes: [] } }));
    await page.route('**/v0/management/plugins/model-router/validate', route => route.fulfill({ json: {} }));
    await page.route('**/v0/management/plugins/model-router/usage/**', route => {
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
    assert.match(failed.time, /^Execution 1/);
    assert.equal(failed.provider_model, 'gpt-a');
    assert.equal(failed.credential, 'Work account');
    assert.equal(failed.request_endpoint, '/v1/chat/completions');
    assert.match(failed.result, /HTTP 429/);
    assert.match(succeeded.time, /^Execution 2/);
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
