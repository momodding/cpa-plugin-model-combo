import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';
import { test } from 'node:test';

const here = dirname(fileURLToPath(import.meta.url));
const source = readFileSync(join(here, '../../dashboard/configuration/routes.js'), 'utf8');

const { normalizeRoute, serializeRoutes, validateRoutes, configFromResponse, configurationPatchBody, validateQuota } = vm.runInNewContext(
  `${source}\n({ normalizeRoute, serializeRoutes, validateRoutes, configFromResponse, configurationPatchBody, validateQuota })`,
  { DEFAULT_FALLBACK_ON_STATUS: '401, 402, 403, 404, 408, 429, 500, 502, 503, 504' },
);

const targets = [{ model: 'gpt-a', weight: 2 }];

test('GET normalization keeps random unchanged', () => {
  const route = normalizeRoute({ alias: 'r', strategy: 'random', cooldown_seconds: 30, targets }, 0);
  assert.equal(route.strategy, 'random');
  assert.equal(route.cooldown_seconds, '30');
  assert.equal('success_rate_threshold' in route, false);
});

test('GET normalization keeps last_known_good policy values unchanged', () => {
  const route = normalizeRoute({
    alias: 'r',
    strategy: 'last_known_good',
    cooldown_seconds: 60,
    success_rate_threshold: 0.75,
    minimum_samples: 5,
    observation_window_minutes: 15,
    targets,
  }, 0);
  assert.equal(route.strategy, 'last_known_good');
  assert.equal(route.success_rate_threshold, '0.75');
  assert.equal(route.minimum_samples, '5');
  assert.equal(route.observation_window_minutes, '15');
});

test('unedited last_known_good route serializes its stored values unchanged', () => {
  const loaded = normalizeRoute({
    alias: 'r',
    strategy: 'last_known_good',
    cooldown_seconds: 60,
    success_rate_threshold: 0.75,
    minimum_samples: 5,
    observation_window_minutes: 15,
    targets,
  }, 0);
  const [payload] = serializeRoutes([loaded]);
  assert.equal(payload.strategy, 'last_known_good');
  assert.equal(payload.success_rate_threshold, 0.75);
  assert.equal(payload.minimum_samples, 5);
  assert.equal(payload.observation_window_minutes, 15);
  assert.deepEqual([...validateRoutes(serializeRoutes([loaded]))], []);
});

test('all strategies validate', () => {
  for (const strategy of ['priority', 'round-robin', 'random', 'last_known_good', 'headroom']) {
    const route = normalizeRoute({ alias: 'r-' + strategy, strategy, cooldown_seconds: 60, targets }, 0);
    const payload = serializeRoutes([route]);
    assert.deepEqual([...validateRoutes(payload)], [], strategy);
  }
});

test('invalid last_known_good policy is rejected', () => {
  const base = { alias: 'r', strategy: 'last_known_good', cooldown_seconds: 60, targets };
  const cases = [
    [{ success_rate_threshold: 0 }, /success rate threshold/],
    [{ success_rate_threshold: 1.5 }, /success rate threshold/],
    [{ minimum_samples: 0 }, /minimum samples/],
    [{ observation_window_minutes: 0 }, /observation window/],
  ];
  for (const [override, message] of cases) {
    const route = normalizeRoute({ ...base, success_rate_threshold: 0.9, minimum_samples: 20, observation_window_minutes: 60, ...override }, 0);
    const errors = validateRoutes(serializeRoutes([route]));
    assert.equal(errors.length, 1, JSON.stringify(override));
    assert.match(errors[0], message);
  }
});

test('unknown strategy still rejected', () => {
  const route = normalizeRoute({ alias: 'r', strategy: 'lottery', cooldown_seconds: 60, targets }, 0);
  assert.equal(route.strategy, 'priority');
  const errors = validateRoutes([{ ...serializeRoutes([route])[0], strategy: 'lottery' }]);
  assert.match(errors.join('\n'), /choose a strategy/);
});

test('GET config -> unedited save builds exact PATCH payload', () => {
  const remote = {
    enabled: true,
    priority: 3,
    routes: [
      { alias: 'rand', strategy: 'random', cooldown_seconds: 30, targets: [{ model: 'm-a', weight: 1 }] },
      { alias: 'lkg', strategy: 'last_known_good', cooldown_seconds: 60, success_rate_threshold: 0.75, minimum_samples: 5, observation_window_minutes: 15, targets: [{ model: 'm-b', weight: 2 }] },
    ],
  };
  const loaded = configFromResponse(remote);
  const body = configurationPatchBody(loaded);
  assert.deepEqual(JSON.parse(JSON.stringify(body)), {
    enabled: true,
    priority: 3,
    routes: [
      { alias: 'rand', strategy: 'random', cooldown_seconds: 30, targets: [{ model: 'm-a', weight: 1 }] },
      { alias: 'lkg', strategy: 'last_known_good', cooldown_seconds: 60, targets: [{ model: 'm-b', weight: 2 }], success_rate_threshold: 0.75, minimum_samples: 5, observation_window_minutes: 15 },
    ],
    fallback: { enabled: true, fallback_on_status: [401, 402, 403, 404, 408, 429, 500, 502, 503, 504], no_fallback_on_status: [], retry_codes: [], no_retry_codes: [], max_attempts: 0, stream_fallback_before_payload: true },
    quota_polling: { refresh_seconds: 60, idle_seconds: 600, ttl_seconds: 60, max_stale_seconds: 300, fetch_timeout_seconds: 5, max_concurrent: 3, cold_wait_ms: 1500, default_unknown_quota_percent: 50 },
    'model-routes': null,
  });
  assert.deepEqual([...validateRoutes(body.routes)], []);
});

test('toggling enabled changes only the enabled field of the PATCH body', () => {
  const remote = {
    enabled: true,
    priority: 7,
    routes: [
      { alias: 'lkg', strategy: 'last_known_good', cooldown_seconds: 45, success_rate_threshold: 0.8, minimum_samples: 10, observation_window_minutes: 30, targets: [{ model: 'm-b', weight: 1 }] },
    ],
  };
  const state = configFromResponse(remote);
  const before = configurationPatchBody(state);
  state.enabled = false;
  const after = configurationPatchBody(state);
  assert.equal(before.enabled, true);
  assert.equal(after.enabled, false);
  assert.deepEqual(JSON.parse(JSON.stringify({ ...after, enabled: true })), JSON.parse(JSON.stringify(before)));
  assert.equal(after.priority, 7);
  assert.deepEqual(JSON.parse(JSON.stringify(after.routes)), JSON.parse(JSON.stringify(before.routes)));
  assert.equal(after.routes[0].success_rate_threshold, 0.8);
  assert.equal(after.routes[0].minimum_samples, 10);
  assert.equal(after.routes[0].observation_window_minutes, 30);
  assert.equal(after['model-routes'], null);
});

test('allowed accounts retain priority and clearing removes restrictions', () => {
  const route = normalizeRoute({ alias: 'r', targets, allowed_accounts: ['second', 'first'] }, 0);
  assert.deepEqual(Array.from(serializeRoutes([route])[0].allowed_accounts), ['second', 'first']);
  route.allowed_accounts = [];
  assert.equal('allowed_accounts' in serializeRoutes([route])[0], false);
});

test('quota defaults, bounds and edits survive configuration serialization', () => {
  const state = configFromResponse({ quota_polling: { default_unknown_quota_percent: 35 } });
  assert.equal(state.quota_polling.default_unknown_quota_percent, '35');
  assert.equal(configurationPatchBody(state).quota_polling.default_unknown_quota_percent, 35);
  for (const value of ['0', '50', '100']) { state.quota_polling.default_unknown_quota_percent = value; assert.equal(validateQuota(state.quota_polling).length, 0); }
  for (const value of ['', '-1', '101', 'NaN']) { state.quota_polling.default_unknown_quota_percent = value; assert.ok(validateQuota(state.quota_polling).length); }
});

test('quota polling requires valid positive integer intervals and TTL within max stale', () => {
  const quota = configFromResponse({}).quota_polling;
  quota.ttl_seconds = '301';
  assert.match(validateQuota(quota).join(' '), /TTL must not exceed/);
  quota.ttl_seconds = '60'; quota.max_concurrent = '0';
  assert.match(validateQuota(quota).join(' '), /positive integer/);
});
