const assert = require('node:assert/strict');
const { join } = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const {
  HARDEST_PROFILE,
  buildManifest,
  hashSourcePair,
  loadSpaceModulesFromHtml,
} = require('./benchmark.cjs');

const htmlPath = join(__dirname, 'space-shooter.html');

function loadModules() {
  return loadSpaceModulesFromHtml(htmlPath);
}

function makeManifest(engineHash = '0'.repeat(64)) {
  return buildManifest({
    seed: 20260920,
    profile: 'hardest',
    difficulty: HARDEST_PROFILE,
    engineHash,
    engineVersion: 'test-engine',
    mode: 'cli',
  });
}

function makeState(tick = 0) {
  return {
    tick,
    sim_ms: tick * (1000 / 60),
    wave: 1,
    difficulty: HARDEST_PROFILE,
    player: { x: 480, y: 540, w: 20, h: 18, lives: 3, cooldown_ms: 0, invulnerability_ms: 0 },
    active_command: null,
    enemy_fire_in_ms: 237.5,
    threat_counts: { enemies: 1, bullets: 1, shown: 2, total: 2 },
    nearest_threats: [
      { kind: 'bullet', x: 480, y: 520, vx: 0, vy: 169, w: 8, h: 8 },
      { kind: 'enemy', x: 480, y: 100, vx: 0, vy: 20, w: 32, h: 24 },
    ],
    recent_commands: [],
    recent_hits: [],
  };
}

function makeForecast(core) {
  return {
    expected_delay_ms: 259,
    latency_samples: 1,
    latency_spread_ms: 0,
    horizon_ms: 759,
    prefix: { authorized_remaining_ms: 120, contact_ms: 80 },
    assumptions: { enemy_motion: 'current_linear', future_spawns_included: false },
    candidates: core.ACTION_IDS.map((id) => ({
      id,
      short: {
        endpoint: { x: 480, y: 540 },
        contact_ms: id === 'down' ? 0 : null,
        clearance_px: id === 'down' ? 0 : 22,
        edge_distances_px: { left: 470, right: 470, top: 531, bottom: 62 },
        shot_eta_ms: id === 'hold' ? 300 : null,
      },
      medium: {
        endpoint: { x: 480, y: 540 },
        contact_ms: id === 'down' ? 0 : null,
        clearance_px: id === 'down' ? 0 : 18,
        edge_distances_px: { left: 470, right: 470, top: 531, bottom: 62 },
        shot_eta_ms: id === 'hold' ? 300 : null,
      },
    })),
  };
}

function begin(controllerApi, controller, core, tick = 0) {
  const request = controllerApi.beginDecision(controller, {
    tick,
    wall_ms: tick * (1000 / 60),
    state: makeState(tick),
    forecast: makeForecast(core),
    checkpoint: { tick, state_hash: `checkpoint-${tick}` },
  });
  assert.equal(request.schema_version, 1);
  assert.ok(Number.isInteger(request.sequence));
  return request;
}

function validReply(request, overrides = {}) {
  return {
    schema_version: 1,
    run_id: request.run_id,
    epoch: request.epoch,
    sequence: request.sequence,
    decision_id: `decision-${request.sequence}`,
    movement: 'hold',
    fire: 'cease',
    lease: 'short',
    intent: 'position',
    valid_choice: true,
    api_ok: true,
    confidence: { movement: 0.5, fire: 0.5, lease: 0.5, intent: 0.5 },
    ...overrides,
  };
}

function finish(controllerApi, controller, request, wall_ms) {
  controllerApi.finishDecision(controller, { sequence: request.sequence, wall_ms });
}

function ruleValue(rules, path, fallbackPath = []) {
  const read = (segments) => segments.reduce((value, key) => (value == null ? undefined : value[key]), rules);
  return read(path) ?? read(fallbackPath);
}

function playerShotEvents(events) {
  return events.filter((event) => event.type === 'shot' && (event.decision_id || event.payload?.decision_id || event.payload?.owner === 'player'));
}

function shotDecisionId(event) {
  return event.decision_id ?? event.payload?.decision_id ?? null;
}

function near(actual, expected, tolerance, label = 'value') {
  assert.ok(Math.abs(actual - expected) <= tolerance, `${label}: ${actual} not within ${tolerance} of ${expected}`);
}

function cleanForecastGame(core, overrides = {}) {
  const game = core.createGame(makeManifest());
  game.enemies = [];
  game.enemyBullets = [];
  game.playerBullets = [];
  game.enemyFireClock_s = 99;
  game.player.x = overrides.x ?? 200;
  game.player.y = overrides.y ?? 300;
  game.player.cooldown_s = overrides.cooldown_s ?? 0;
  game.player.invincible_s = overrides.invincible_s ?? 0;
  return game;
}

function observeForForecast(core, game, context = {}) {
  return core.observeGame(game, {
    detail: true,
    expected_delay_ms: context.expected_delay_ms ?? 100,
    latency_samples: 0,
    latency_spread_ms: 0,
    active_command: null,
    recent_commands: [],
    recent_hits: [],
    ...context,
  });
}

function candidate(observed, id) {
  const found = observed.forecast.candidates.find((item) => item.id === id);
  assert.ok(found, `missing candidate ${id}`);
  return found;
}

function bullet({ id, x = 200, y = 300, vx = 0, vy = 0 }) {
  return { id, x, y, vx, vy, radius: 4, fast: false, from_enemy_id: 'fixture-enemy' };
}

function scoutEnemy({ id = 'enemy-fixture', x = 260, y = 300 }) {
  return { id, type: 'scout', x, y, vx: 0, phase: 0, w: 32, h: 24, hp: 2, maxHp: 2 };
}

function classListStub() {
  const names = new Set();
  return {
    add: (name) => names.add(name), remove: (name) => names.delete(name), contains: (name) => names.has(name),
    toggle: (name, force) => { const on = force === undefined ? !names.has(name) : Boolean(force); if (on) names.add(name); else names.delete(name); return on; },
  };
}

// Exercise the actual adapter with a stub DOM and no bootstrap or animation.
// Lifecycle tests supply mocked transport; no test can reach a real HTTP service.
function loadBrowserAdapter(modules, game, controller, options = {}) {
  const elements = new Map();
  const element = (id) => {
    if (!elements.has(id)) elements.set(id, {
      textContent: '', innerHTML: '', width: 960, height: 620,
      getContext: () => ({}), addEventListener() {}, classList: classListStub(),
    });
    return elements.get(id);
  };
  const sandbox = {
    SpaceDecisionCore: modules.core,
    SpaceDjevController: modules.controller,
    document: { getElementById: element },
    window: { addEventListener() {} },
    location: { protocol: options.fetch ? 'http:' : 'file:' },
    performance: { now: () => options.clock?.now ?? 100 },
    fetch: options.fetch || (() => { throw new Error('browser unit tests must never call HTTP'); }),
    TextEncoder, AbortController,
    crypto: require('node:crypto').webcrypto,
    setTimeout: (fn, ms) => {
      const timer = setTimeout(fn, ms);
      options.t?.after(() => clearTimeout(timer));
      return timer;
    },
    clearTimeout,
    requestAnimationFrame() { throw new Error('browser unit tests must never start animation'); },
    fixtureGame: game, fixtureController: controller,
  };
  const adapterScript = modules.html.match(/<script>\s*([\s\S]*?)<\/script>/)[1];
  const bootstrap = /    syncDifficultyControls\(\);\s*bootstrap\(\);\s*requestAnimationFrame\(frame\);\s*\}\)\(\);\s*$/;
  assert.ok(bootstrap.test(adapterScript), 'adapter bootstrap must be excluded from this offline harness');
  vm.runInNewContext(adapterScript.replace(bootstrap, `
    game = fixtureGame;
    controller = fixtureController;
    runStartPerf = 0;
    globalThis.adapter = {
      currentObservation, updateRecentFromEvents, updatePanel, setPaused,
      restartRun, maybeBeginDecision, endRun, processTick, enqueueEvents, drainTrace,
      showIntro, startFromIntro, onKeyDown, onKeyUp, updateManual, update, setAutopilot, weaponText,
      getState: () => ({ game, controller, runId, qualification, lastApi,
        completionEvents, history, nextRequestAllowedWallMs, introActive, autopilot, runState, manualBombRequests }),
    };
  })();`), sandbox);
  element('space-decision-core').textContent = modules.coreScript;
  element('space-djev-controller').textContent = modules.controllerScript;
  return { adapter: sandbox.adapter, element };
}

function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

const jsonResponse = (body) => ({ ok: true, json: async () => body });
const plain = (value) => JSON.parse(JSON.stringify(value));

async function waitUntil(predicate) {
  for (let attempt = 0; attempt < 200; attempt += 1) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.fail('mock browser work did not settle');
}

function browserTransport() {
  const calls = [];
  let starts = 0;
  const transport = {
    calls, deferStarts: false, deferEvents: false, deferEnds: false,
    fetch: (route, init) => {
      const call = { route, body: JSON.parse(init.body), ...deferred() };
      calls.push(call);
      if (route === '/api/run/start') {
        const run_id = `browser-run-${++starts}`;
        if (!transport.deferStarts) call.resolve(jsonResponse({ schema_version: 1, run_id, trace_path: `runs/${run_id}` }));
      } else if (route === '/api/run/event') {
        if (!transport.deferEvents) call.resolve(jsonResponse({ schema_version: 1, run_id: call.body.run_id, acked_event_id: call.body.events.at(-1).event_id }));
      } else if (route === '/api/run/end') {
        if (!transport.deferEnds) call.resolve(jsonResponse({ schema_version: 1, run_id: call.body.run_id, complete: true }));
      } else assert.equal(route, '/api/decision');
      return call.promise;
    },
    decisions: () => calls.filter((call) => call.route === '/api/decision'),
    ends: () => calls.filter((call) => call.route === '/api/run/end'),
    events: (runId) => calls.filter((call) => call.route === '/api/run/event' && call.body.run_id === runId).flatMap((call) => call.body.events),
    accept: (call) => call.resolve(jsonResponse(validReply(call.body))),
  };
  return transport;
}

const keyEvent = (key, extra = {}) => ({ key, repeat: false, target: { tagName: 'BODY' }, preventDefault() {}, ...extra });

test('the intro screen gates the run: no trace or decision until the player starts, Enter starts the autopilot', async (t) => {
  const bridge = browserTransport();
  const clock = { now: 0 };
  const { adapter, element } = loadBrowserAdapter(loadModules(), null, null, { fetch: bridge.fetch, clock, t });
  adapter.showIntro();
  assert.equal(adapter.getState().introActive, true);
  assert.ok(element('intro').classList.contains('show'));
  const preview = adapter.getState().game;
  adapter.update(0);
  adapter.update(500);
  adapter.onKeyDown(keyEvent('j'));
  adapter.onKeyDown(keyEvent('b'));
  adapter.updatePanel();
  assert.equal(preview.tick, 0, 'the preview game does not run');
  assert.equal(bridge.calls.length, 0, 'no run start, trace, or decision request before the player chooses');
  assert.equal(adapter.getState().runState, null);
  assert.equal(element('hud-mode').textContent, 'INTRO');

  const starting = adapter.startFromIntro('auto') ?? null;
  assert.ok(starting, 'starting returns the run start');
  await starting;
  assert.equal(adapter.getState().introActive, false);
  assert.ok(!element('intro').classList.contains('show'));
  assert.equal(adapter.getState().autopilot, true);
  assert.ok(bridge.calls.some((call) => call.route === '/api/run/start'));
  await waitUntil(() => bridge.decisions().length === 1);
  bridge.accept(bridge.decisions()[0]);
  await waitUntil(() => !adapter.getState().controller.pending);
  await adapter.endRun('aborted');

  // Enter on the intro starts the autopilot too; M starts a manual run.
  adapter.showIntro();
  adapter.onKeyDown(keyEvent('Enter'));
  assert.equal(adapter.getState().introActive, false);
  assert.equal(adapter.getState().autopilot, true);
  await adapter.getState().runState.startPromise;
  await waitUntil(() => bridge.decisions().length === 2);
  bridge.accept(bridge.decisions()[1]);
  await waitUntil(() => !adapter.getState().controller.pending);
  await adapter.endRun('aborted');
  adapter.showIntro();
  adapter.onKeyDown(keyEvent('m'));
  assert.equal(adapter.getState().autopilot, false);
  assert.equal(adapter.getState().qualification.valid, false, 'manual play is never benchmark-qualified');
  await adapter.getState().runState.startPromise;
  await adapter.endRun('aborted');
  assert.equal(bridge.decisions().length, 2, 'a manual run sends no decision requests');
});

test('restart (as the headless recorder clicks it) dismisses the intro and starts an autopilot run', async (t) => {
  const bridge = browserTransport();
  const { adapter, element } = loadBrowserAdapter(loadModules(), null, null, { fetch: bridge.fetch, clock: { now: 0 }, t });
  adapter.showIntro();
  await adapter.restartRun();
  assert.equal(adapter.getState().introActive, false);
  assert.ok(!element('intro').classList.contains('show'));
  assert.equal(adapter.getState().autopilot, true);
  await waitUntil(() => bridge.decisions().length === 1);
  bridge.accept(bridge.decisions()[0]);
  await waitUntil(() => !adapter.getState().controller.pending);
  await adapter.endRun('aborted');
});

test('manual keys take over from the autopilot and fire and bomb through the core command', async (t) => {
  const bridge = browserTransport();
  const { adapter } = loadBrowserAdapter(loadModules(), null, null, { fetch: bridge.fetch, clock: { now: 0 }, t });
  await adapter.restartRun();
  bridge.accept(bridge.decisions()[0]);
  await waitUntil(() => !adapter.getState().controller.pending);
  const { game } = adapter.getState();
  game.enemyBullets = [{ id: 'near', x: game.player.x + 30, y: game.player.y - 40, vx: 0, vy: 0, radius: 4 }];
  const charges = game.bomb.charges;

  adapter.onKeyDown(keyEvent('b'));
  assert.equal(adapter.getState().autopilot, false, 'a manual key takes over');
  assert.equal(adapter.getState().qualification.valid, false);
  adapter.onKeyDown(keyEvent('b', { repeat: true }));
  adapter.onKeyUp(keyEvent('b'));
  assert.equal(adapter.getState().manualBombRequests, 1, 'key repeat does not queue more bombs');
  adapter.updateManual(1 / 60);
  assert.equal(game.bomb.charges, charges - 1, 'one detonation through the core bomb rule');
  assert.equal(game.counters.bombsUsed, 1);
  assert.ok(!game.enemyBullets.some((b) => b.id === 'near'), 'the blast cleared the nearby bullet');
  adapter.updateManual(1 / 60);
  assert.equal(game.bomb.charges, charges - 1, 'one press, one bomb');

  const shotsBefore = game.counters.playerShots;
  game.player.cooldown_s = 0;
  adapter.onKeyDown(keyEvent('j'));
  adapter.updateManual(1 / 60);
  assert.ok(game.counters.playerShots > shotsBefore, 'holding J fires the gun');
  adapter.onKeyUp(keyEvent('j'));
  const x = game.player.x;
  adapter.onKeyDown(keyEvent('ArrowLeft'));
  adapter.updateManual(1 / 60);
  adapter.onKeyUp(keyEvent('ArrowLeft'));
  assert.ok(Math.abs(game.player.x - (x - 280 / 60)) < 1e-6, 'arrows move at manual speed');

  adapter.onKeyDown(keyEvent('p'));
  assert.equal(adapter.getState().autopilot, true, 'P hands control back to the autopilot');
  await adapter.endRun('aborted');
});

test('the HUD weapon text names each upgrade', () => {
  const { adapter } = loadBrowserAdapter(loadModules(), null, null);
  assert.equal(adapter.weaponText(0), 'Lv 0 · 1-way · 1×salvo · 4 max');
  assert.equal(adapter.weaponText(3), 'Lv 3 · 5-way · rapid · 3×salvo · 6 max');
  assert.equal(adapter.weaponText(4), 'Lv 4 · 5-way · rapid · splash · 3×salvo · 6 max');
  assert.equal(adapter.weaponText(5), 'Lv 5 · 5-way · rapid · splash · 8×salvo · 8 max');
});

test('run integrity shows recording only after the start acknowledgement', async (t) => {
  const bridge = browserTransport();
  bridge.deferStarts = true;
  const { adapter, element } = loadBrowserAdapter(loadModules(), null, null, { fetch: bridge.fetch, clock: { now: 0 }, t });
  const starting = adapter.restartRun();
  await waitUntil(() => bridge.calls.some((call) => call.route === '/api/run/start'));
  adapter.updatePanel();
  assert.equal(element('qualification').textContent, 'pending trace');
  bridge.calls.find((call) => call.route === '/api/run/start').resolve(jsonResponse({ schema_version: 1, run_id: 'integrity-start', trace_path: 'test-trace' }));
  await starting;
  adapter.updatePanel();
  assert.equal(element('qualification').textContent, 'recording');
  bridge.accept(bridge.decisions()[0]);
  await waitUntil(() => !adapter.getState().controller.pending);
  await adapter.endRun('aborted');
});

test('run integrity reports the ending reason only after a complete end acknowledgement', async (t) => {
  const bridge = browserTransport();
  bridge.deferEnds = true;
  const { adapter, element } = loadBrowserAdapter(loadModules(), null, null, { fetch: bridge.fetch, clock: { now: 0 }, t });
  await adapter.restartRun();
  bridge.accept(bridge.decisions()[0]);
  await waitUntil(() => !adapter.getState().controller.pending);
  const ending = adapter.endRun('death');
  await waitUntil(() => bridge.ends().length === 1);
  adapter.updatePanel();
  assert.notEqual(element('qualification').textContent, 'trace complete · death');
  const end = bridge.ends()[0];
  end.resolve(jsonResponse({ schema_version: 1, run_id: end.body.run_id, complete: true }));
  await ending;
  adapter.updatePanel();
  assert.equal(element('qualification').textContent, 'trace complete · death');
});

for (const pauseAt of ['before_start_ack', 'after_start_ack']) {
  test(`run integrity preserves invalid reasons through start and end acknowledgements (${pauseAt})`, async (t) => {
    const bridge = browserTransport();
    bridge.deferStarts = true;
    const { adapter, element } = loadBrowserAdapter(loadModules(), null, null, { fetch: bridge.fetch, clock: { now: 0 }, t });
    const starting = adapter.restartRun();
    await waitUntil(() => bridge.calls.some((call) => call.route === '/api/run/start'));
    if (pauseAt === 'before_start_ack') adapter.setPaused(true);
    bridge.calls.find((call) => call.route === '/api/run/start').resolve(jsonResponse({ schema_version: 1, run_id: 'integrity-invalid', trace_path: 'test-trace' }));
    await starting;
    if (pauseAt === 'after_start_ack') {
      bridge.accept(bridge.decisions()[0]);
      await waitUntil(() => !adapter.getState().controller.pending);
      adapter.setPaused(true);
    }
    adapter.updatePanel();
    assert.equal(element('qualification').textContent, 'invalid · paused');
    await adapter.endRun('aborted');
    adapter.updatePanel();
    assert.equal(element('qualification').textContent, 'invalid · paused');
    assert.deepEqual(bridge.ends()[0].body.terminal.qualification_violations, ['paused']);
  });
}

test('browser finalizes first and second runs once each with exact final checkpoints', async (t) => {
  const modules = loadModules();
  const bridge = browserTransport();
  const clock = { now: 0 };
  const { adapter } = loadBrowserAdapter(modules, null, null, { fetch: bridge.fetch, clock, t });
  for (const [index, steps] of [[0, 7], [1, 13]]) {
    await adapter.restartRun();
    bridge.accept(bridge.decisions()[index]);
    await waitUntil(() => !adapter.getState().controller.pending);
    const { game, runId } = adapter.getState();
    for (let tick = 0; tick < steps; tick += 1) modules.core.stepGame(game, null);
    clock.now += 300;
    const expectedHash = modules.core.hashGame(game);
    await adapter.endRun('aborted');
    await adapter.endRun('aborted');
    assert.equal(bridge.ends().length, index + 1, 'each run must finalize exactly once');
    const terminal = bridge.ends().at(-1).body.terminal;
    assert.equal(terminal.tick, steps);
    const finalCheckpoint = bridge.events(runId).filter((event) => event.type === 'checkpoint').at(-1);
    assert.equal(finalCheckpoint.tick, steps);
    assert.equal(finalCheckpoint.payload.checkpoint.tick, steps);
    assert.equal(finalCheckpoint.payload.hash, expectedHash);
    assert.equal(finalCheckpoint.sim_ms, terminal.sim_ms);
    assert.ok(bridge.ends().at(-1).body.last_event_id >= finalCheckpoint.event_id);
  }
});

for (const outcome of ['success', 'rejected', 'error']) {
  test(`old same-sequence decision ${outcome} after restart cannot mutate new authority or trace`, async (t) => {
    const modules = loadModules();
    const bridge = browserTransport();
    const clock = { now: 0 };
    const { adapter } = loadBrowserAdapter(modules, null, null, { fetch: bridge.fetch, clock, t });
    await adapter.restartRun();
    const oldCall = bridge.decisions()[0];
    const oldGame = adapter.getState().game;
    for (let tick = 0; tick < 7; tick += 1) modules.core.stepGame(oldGame, null);
    const oldHash = modules.core.hashGame(oldGame);
    clock.now = 120;
    await adapter.restartRun();
    const newCall = bridge.decisions()[1];
    assert.equal(oldCall.body.sequence, 1);
    assert.equal(newCall.body.sequence, 1);
    const before = adapter.getState();
    const pending = before.controller.pending;
    const counters = plain(before.controller.counters);
    const history = plain(before.history);
    if (outcome === 'error') oldCall.reject(new Error('old transport failed'));
    else oldCall.resolve(jsonResponse(validReply(oldCall.body, outcome === 'rejected' ? { api_ok: false, valid_choice: false } : {})));
    await new Promise((resolve) => setTimeout(resolve, 20));
    const after = adapter.getState();
    assert.ok(after.controller.pending === pending, 'old completion must not settle sequence 1 of the new run');
    assert.deepEqual(plain(after.controller.counters), counters);
    assert.deepEqual(plain(after.history), history);
    assert.equal(after.controller.queued, null);
    assert.equal(after.nextRequestAllowedWallMs, 0);
    assert.equal(after.completionEvents.length, 0);
    assert.equal(after.lastApi.latency_ms, null);
    assert.equal(bridge.events(after.runId).some((event) => ['response_received', 'response_rejected'].includes(event.type)), false);
    bridge.accept(newCall);
    await waitUntil(() => !after.controller.pending);
    await adapter.endRun('aborted');
    await waitUntil(() => bridge.ends().length === 2);
    const oldEnd = bridge.ends().find((call) => call.body.run_id === oldCall.body.run_id);
    assert.equal(oldEnd.body.terminal.tick, 7);
    assert.equal(oldEnd.body.terminal.wall_ms, 120);
    const finalCheckpoint = bridge.events(oldCall.body.run_id).filter((event) => event.type === 'checkpoint').at(-1);
    assert.equal(finalCheckpoint.payload.hash, oldHash);
    assert.equal(finalCheckpoint.tick, 7);
  });
}

test('late run-start response after restart cannot install old identity or start an old decision', async (t) => {
  const modules = loadModules();
  const bridge = browserTransport();
  bridge.deferStarts = true;
  const { adapter } = loadBrowserAdapter(modules, null, null, { fetch: bridge.fetch, clock: { now: 0 }, t });
  const firstStart = adapter.restartRun();
  await waitUntil(() => bridge.calls.filter((call) => call.route === '/api/run/start').length === 1);
  const secondStart = adapter.restartRun();
  await waitUntil(() => bridge.calls.filter((call) => call.route === '/api/run/start').length === 2);
  const starts = bridge.calls.filter((call) => call.route === '/api/run/start');
  starts[1].resolve(jsonResponse({ schema_version: 1, run_id: 'new-start', trace_path: 'new-trace' }));
  await secondStart;
  const current = adapter.getState().controller;
  const pending = current.pending;
  starts[0].resolve(jsonResponse({ schema_version: 1, run_id: 'old-start', trace_path: 'old-trace' }));
  await firstStart;
  assert.equal(adapter.getState().runId, 'new-start');
  assert.equal(current.run_id, 'new-start');
  assert.equal(current.pending, pending);
  assert.equal(bridge.decisions().length, 1);
  bridge.accept(bridge.decisions()[0]);
  await waitUntil(() => !current.pending);
  await adapter.endRun('aborted');
  await waitUntil(() => bridge.ends().length === 2);
  assert.equal(bridge.ends().find((call) => call.body.run_id === 'old-start').body.terminal.tick, 0);
});

for (const outcome of ['ack', 'error']) {
  test(`old trace ${outcome} after restart keeps new-run events and qualification isolated`, async (t) => {
    const modules = loadModules();
    const bridge = browserTransport();
    bridge.deferEvents = true;
    const { adapter } = loadBrowserAdapter(modules, null, null, { fetch: bridge.fetch, clock: { now: 0 }, t });
    await adapter.restartRun();
    const oldEvent = bridge.calls.find((call) => call.route === '/api/run/event');
    await adapter.restartRun();
    const newRunId = adapter.getState().runId;
    if (outcome === 'error') oldEvent.reject(new Error('old trace failed'));
    else oldEvent.resolve(jsonResponse({ schema_version: 1, run_id: oldEvent.body.run_id, acked_event_id: oldEvent.body.events.at(-1).event_id }));
    await new Promise((resolve) => setTimeout(resolve, 20));
    assert.equal(adapter.getState().qualification.valid, true);
    bridge.deferEvents = false;
    for (const call of bridge.calls.filter((item) => item.route === '/api/run/event' && item !== oldEvent)) {
      call.resolve(jsonResponse({ schema_version: 1, run_id: call.body.run_id, acked_event_id: call.body.events.at(-1).event_id }));
    }
    for (const call of bridge.decisions()) bridge.accept(call);
    await waitUntil(() => !adapter.getState().controller.pending);
    await adapter.endRun('aborted');
    await waitUntil(() => bridge.ends().length === 2);
    const events = bridge.events(newRunId);
    assert.equal(events.some((event) => event.type === 'qualification_invalidated'), false);
    const ids = [...new Set(events.map((event) => event.event_id))];
    assert.deepEqual(ids, Array.from({ length: ids.at(-1) }, (_, index) => index + 1));
    assert.equal(events.filter((event) => event.type === 'terminal').length, 1);
  });
}

test('browser death checkpoint captures the completed fatal tick and does not count finalization time', async (t) => {
  const modules = loadModules();
  const bridge = browserTransport();
  const clock = { now: 0 };
  const { adapter } = loadBrowserAdapter(modules, null, null, { fetch: bridge.fetch, clock, t });
  await adapter.restartRun();
  bridge.accept(bridge.decisions()[0]);
  await waitUntil(() => !adapter.getState().controller.pending);
  const { game, runId } = adapter.getState();
  for (let tick = 0; tick < 7; tick += 1) modules.core.stepGame(game, null);
  game.player.lives = 1;
  game.enemyBullets = [bullet({ id: 'fatal-fixture', x: game.player.x, y: game.player.y })];
  clock.now = 134;
  adapter.processTick();
  const terminalHash = modules.core.hashGame(game);
  clock.now = 900;
  await adapter.endRun('death');
  await waitUntil(() => bridge.ends().length === 1);
  const terminal = bridge.ends()[0].body.terminal;
  assert.equal(terminal.tick, 8);
  assert.equal(terminal.wall_ms, 134);
  const checkpoint = bridge.events(runId).filter((event) => event.type === 'checkpoint').at(-1);
  assert.equal(checkpoint.tick, 8);
  assert.equal(checkpoint.payload.checkpoint.tick, 8);
  assert.equal(checkpoint.payload.checkpoint.player.lives, 0);
  assert.equal(checkpoint.payload.hash, terminalHash);
  assert.equal(bridge.events(runId).filter((event) => event.type === 'terminal').at(-1).tick, 8);
});

test('requests offer all nine fixed-medium paths, both fire choices, and all three intents atomically', () => {
  const { core, controller: api } = loadModules();
  const controller = api.createController({ run_id: 'intent-request', epoch: 1 });
  const request = begin(api, controller, core);
  assert.equal(request.prompt_version, 'djev-authoritative-v6');
  assert.equal(request.context_version, 'djev-observation-v6');
  assert.deepEqual(Object.keys(request.questions), ['path', 'fire', 'intent', 'bomb']);
  assert.deepEqual(Object.keys(request.questions.path.criteria), [
    'hold__medium', 'left__medium', 'right__medium', 'up__medium', 'down__medium',
    'up_left__medium', 'up_right__medium', 'down_left__medium', 'down_right__medium',
  ]);
  assert.deepEqual(Object.keys(request.questions.fire.criteria), ['shoot', 'cease']);
  assert.deepEqual(Object.keys(request.questions.intent.criteria), ['evade', 'recover', 'position']);
  assert.deepEqual(Object.keys(request.questions.bomb.criteria), ['hold', 'detonate']);
});

test('missing or unknown intent atomically ends current authority and cannot move or shoot', () => {
  const { core, controller: api } = loadModules();
  for (const intent of ['', 'unknown', 'Recover', 0, false, {}]) {
    const controller = api.createController({ run_id: 'invalid-intent', epoch: 1 });
    const previous = begin(api, controller, core);
    api.receiveDecision(controller, validReply(previous, { movement: 'left', fire: 'shoot', intent: 'evade' }), { tick: 0, wall_ms: 10 });
    finish(api, controller, previous, 10);
    api.commandForTick(controller, { tick: 0, wall_ms: 11 });
    const request = begin(api, controller, core, 1);
    const reply = validReply(request, { movement: 'right', fire: 'shoot', intent });
    if (intent === undefined) delete reply.intent;
    const received = api.receiveDecision(controller, reply, { tick: 1, wall_ms: 30 });
    assert.equal(received.events[0].type, 'response_rejected');
    assert.equal(received.events[0].reason, 'unknown_intent');
    finish(api, controller, request, 30);
    const result = api.commandForTick(controller, { tick: 1, wall_ms: 31 });
    assert.equal(result.command, null);
    assert.equal(result.events.some((event) => event.type === 'command_applied'), false);
    assert.equal(result.events.find((event) => event.type === 'command_ended').intent, 'evade');
    assert.equal(controller.lastAppliedIntent, 'evade', 'invalid replies must not replace factual history');
    const game = cleanForecastGame(core);
    core.stepGame(game, result.command);
    assert.equal(game.player.x, 200);
    assert.equal(game.player.y, 300);
    assert.equal(game.playerBullets.length, 0);
  }
});

test('every valid intent follows response, application, shot, observation, and lease-ended history', () => {
  const { core, controller: api } = loadModules();
  for (const intent of ['evade', 'recover', 'position']) {
    const controller = api.createController({ run_id: 'intent-history', epoch: 1 });
    assert.equal(controller.lastAppliedIntent, null);
    const request = begin(api, controller, core);
    const received = api.receiveDecision(controller, validReply(request, { fire: 'shoot', intent }), { tick: 0, wall_ms: 10 });
    assert.equal(received.events[0].intent, intent);
    assert.equal(controller.queued.decision.intent, intent);
    assert.equal(controller.lastAppliedIntent, null, 'receiving is not applying');
    finish(api, controller, request, 10);
    const applied = api.commandForTick(controller, { tick: 0, wall_ms: 11 });
    assert.equal(applied.command.intent, intent);
    assert.equal(applied.events.find((event) => event.type === 'command_applied').command.intent, intent);
    assert.equal(controller.lastAppliedIntent, intent);
    const game = cleanForecastGame(core);
    game.enemies = [scoutEnemy({ x: 800, y: 50 })];
    core.stepGame(game, applied.command);
    assert.equal(game.playerBullets[0].authorizedBy.intent, intent);
    assert.equal(game.playerBullets[0].authorizedBy.decision_id, 'decision-1');
    const restored = core.restoreGame(core.serializeGame(game));
    assert.equal(restored.playerBullets[0].authorizedBy.intent, intent);
    const observed = observeForForecast(core, game, {
      last_intent: controller.lastAppliedIntent,
      active_command: { ...applied.command, remaining_ms: 230 },
      recent_commands: [{ ...applied.command, source: 'djev', elapsed_ms: 16.7, dx: 0, dy: 0 }],
    });
    assert.equal(observed.state.last_intent, intent);
    assert.equal(observed.state.active_command.intent, intent);
    assert.equal(observed.state.recent_commands[0].intent, intent);
    const expired = api.commandForTick(controller, { tick: 15, wall_ms: 261 });
    assert.equal(expired.command, null);
    assert.equal(expired.events.find((event) => event.type === 'command_ended').intent, intent);
    assert.equal(expired.events.find((event) => event.type === 'command_ended').confidence?.intent, 0.5);
    assert.equal(controller.lastAppliedIntent, intent, 'lease expiry retains applied history');
    core.stepGame(game, expired.command);
    assert.equal(game.playerBullets[0].authorizedBy.intent, intent, 'expiry does not rewrite shot provenance');
  }
});

test('queued, stale, or invalidated decisions never invent or retain an unapplied intent', () => {
  const { core, controller: api } = loadModules();
  const controller = api.createController({ run_id: 'intent-reset', epoch: 1 });
  const game = cleanForecastGame(core);
  assert.equal(observeForForecast(core, game).state.last_intent, null);
  assert.equal(api.commandForTick(controller, { tick: 0, wall_ms: 0 }).command, null);
  assert.equal(controller.lastAppliedIntent, null);
  const request = begin(api, controller, core);
  api.receiveDecision(controller, validReply(request, { intent: 'recover' }), { tick: 0, wall_ms: 590 });
  finish(api, controller, request, 590);
  assert.equal(api.commandForTick(controller, { tick: 0, wall_ms: 601 }).command, null);
  assert.equal(controller.lastAppliedIntent, null, 'queued reply expired before application');

  const next = begin(api, controller, core, 40);
  api.receiveDecision(controller, validReply(next, { intent: 'position', lease: 'medium' }), { tick: 40, wall_ms: 680 });
  finish(api, controller, next, 680);
  api.commandForTick(controller, { tick: 40, wall_ms: 681 });
  assert.equal(controller.lastAppliedIntent, 'position');
  const invalidated = api.invalidateController(controller, 'paused');
  assert.equal(invalidated.events.find((event) => event.type === 'command_ended').intent, 'position');
  assert.equal(controller.lastAppliedIntent, null);
  assert.equal(controller.active, null);
  api.receiveDecision(controller, validReply(next, { intent: 'evade' }), { tick: 40, wall_ms: 682 });
  assert.equal(controller.lastAppliedIntent, null);
  assert.equal(api.commandForTick(controller, { tick: 40, wall_ms: 683 }).command, null);
  assert.equal(api.createController({ run_id: 'new-run', epoch: 1 }).lastAppliedIntent, null);
});

test('intent is optional on observed historical spans and never defaults neutral history to a model intent', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core);
  const observed = observeForForecast(core, game, {
    active_command: { movement: 'hold', fire: 'cease', lease: 'short', remaining_ms: 50 },
    recent_commands: [
      { movement: 'left', fire: 'cease', lease: 'short', source: 'djev' },
      { movement: 'hold', fire: 'cease', lease: null, source: 'neutral' },
    ],
  });
  assert.equal(observed.state.last_intent, null);
  assert.equal(Object.hasOwn(observed.state.active_command, 'intent'), false);
  assert.equal(observed.state.recent_commands.some((span) => Object.hasOwn(span, 'intent')), false);
});

test('browser sends applied intent history and renders the bomb call without starting a run', () => {
  const modules = loadModules();
  const { core, controller: api } = modules;
  const game = cleanForecastGame(core);
  const controller = api.createController({ run_id: 'browser-intent', epoch: 1 });
  const { adapter, element } = loadBrowserAdapter(modules, game, controller);
  for (const [index, intent, probability] of [[0, 'evade', 0], [1, 'recover', 0.73], [2, 'position', undefined]]) {
    const request = begin(api, controller, core, index);
    const received = api.receiveDecision(controller, validReply(request, { movement: 'left', intent, lease: 'medium', confidence: { intent: probability } }), { tick: index, wall_ms: 80 + index });
    adapter.updateRecentFromEvents(received.events);
    finish(api, controller, request, 80 + index);
    const applied = api.commandForTick(controller, { tick: index, wall_ms: 90 + index });
    adapter.updateRecentFromEvents(applied.events);
    const observed = adapter.currentObservation();
    assert.equal(observed.state.last_intent, intent);
    assert.equal(observed.state.active_command.intent, intent);
    assert.equal(observed.state.recent_commands.at(-1).intent, intent);
    adapter.updatePanel();
    for (const text of [element('selected').textContent, element('executed').textContent, element('history').innerHTML]) {
      // Intent is no longer shown (djev is not asked for it); the readout is the bomb call and path confidence.
      assert.ok(text.includes('bomb hold · p=—'), text);
    }
  }
  game.terminal = { reason: 'death' };
  adapter.updatePanel();
  assert.equal(element('neutral-reason').textContent, 'ended · no active command');
  game.terminal = null;
  adapter.setPaused(true);
  assert.equal(adapter.currentObservation().state.last_intent, null);
  assert.equal(adapter.currentObservation().state.active_command, null);
});

test('inline modules expose the frozen authoritative runtime contract', () => {
  const { core, controller, coreScript, controllerScript, engineHash } = loadModules();
  assert.equal(engineHash, hashSourcePair(coreScript, controllerScript));
  assert.deepEqual([...core.ACTION_IDS], ['hold', 'left', 'right', 'up', 'down', 'up_left', 'up_right', 'down_left', 'down_right']);
  assert.deepEqual({ ...core.LEASE_TICKS }, { short: 15, medium: 30 });
  assert.equal(core.DT_MS, 1000 / 60);
  assert.deepEqual({ ...core.HARDEST_DIFFICULTY }, HARDEST_PROFILE);
  assert.equal(ruleValue(core.RULES, ['enemy_fire_interval', 'baseline_s'], ['enemy_fire_baseline_s']), 0.95);
  assert.equal(ruleValue(core.RULES, ['leases_ticks', 'short'], ['lease_ticks', 'short']), 15);
  for (const name of ['createGame', 'stepGame', 'observeGame', 'serializeGame', 'restoreGame', 'hashGame', 'gameStatus']) {
    assert.equal(typeof core[name], 'function', `SpaceDecisionCore.${name}`);
  }
  for (const name of ['createController', 'beginDecision', 'receiveDecision', 'commandForTick', 'finishDecision', 'invalidateController']) {
    assert.equal(typeof controller[name], 'function', `SpaceDjevController.${name}`);
  }
});

test('difficulty settings preserve the hardest profile and doubled baseline fire rate', () => {
  const { engineHash, core } = loadModules();
  const manifest = makeManifest(engineHash);
  assert.deepEqual(manifest.difficulty, {
    bulletDensity: 4,
    enemyDensity: 3,
    fastBulletRatio: 0.85,
    fastBulletSpeed: 2.4,
  });
  assert.equal(manifest.rules.enemy_fire_baseline_s, 0.95);
  assert.equal(manifest.rules.hardest_enemy_fire_interval_s, 0.2375);
  assert.equal(manifest.rules.player_speed_px_s, 112);
  assert.equal(manifest.rules.player_lives, 3);
  assert.equal(manifest.rules.shot_cooldown_s, 0.17);
  const game = core.createGame(manifest);
  const status = core.gameStatus(game);
  assert.equal(typeof status.counters, 'object');
  assert.equal(Array.isArray(status.counters), false);
  assert.equal(status.tick, 0);
  assert.equal(status.sim_ms, 0);
  assert.equal(status.lives, 3);
  assert.equal(status.wave, 1);
  assert.equal(status.game_over, false);
  const observed = core.observeGame(game, {
    detail: true,
    expected_delay_ms: 260,
    active_command: null,
    recent_commands: [],
    recent_hits: [],
  });
  assert.equal(observed.state.difficulty.bulletDensity, 4);
});

test('all movement and lease pairs stay visible even when candidates collide or hit walls', () => {
  const { core } = loadModules();
  const game = core.createGame(makeManifest());
  const observed = core.observeGame(game, {
    detail: true,
    expected_delay_ms: 260,
    active_command: null,
    recent_commands: [],
    recent_hits: [],
  });
  assert.equal(observed.forecast.candidates.length, 9);
  assert.deepEqual(observed.forecast.candidates.map((candidate) => candidate.id), core.ACTION_IDS);
  for (const candidate of observed.forecast.candidates) {
    assert.ok(candidate.short);
    assert.ok(candidate.medium);
    assert.ok(Object.hasOwn(candidate.short, 'contact_ms'));
    assert.ok(Object.hasOwn(candidate.medium, 'contact_ms'));
  }
});

test('forecast reports candidate-path bullet contacts on global time after response delay', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core, { x: 200, y: 300 });
  game.enemyBullets = [bullet({ id: 'global-crossing', x: 240, y: 300 })];
  const observed = observeForForecast(core, game, { expected_delay_ms: 200 });
  assert.equal(observed.forecast.prefix.contact_ms, null);
  near(candidate(observed, 'right').short.contact_ms, 396.4, 1.5, 'right short contact_ms');
});

test('forecast keeps raw prefix collision separate and does not mask later clear candidate paths', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core, { x: 200, y: 300 });
  game.enemyBullets = [bullet({ id: 'prefix-only', x: 200, y: 267, vy: 400 })];
  const observed = observeForForecast(core, game, { expected_delay_ms: 300 });
  assert.ok(observed.forecast.prefix.contact_ms > 20 && observed.forecast.prefix.contact_ms < 60);
  assert.equal(candidate(observed, 'right').short.contact_ms, null);
  assert.equal(candidate(observed, 'up').medium.contact_ms, null);
});

test('forecast ignores contacts only during known current invulnerability, not after expiry', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core, { x: 200, y: 300, invincible_s: 0.25 });
  game.enemyBullets = [
    bullet({ id: 'ignored-during-current-invuln', x: 200, y: 183, vy: 1000 }),
    bullet({ id: 'counted-after-current-invuln', x: 200, y: -67, vy: 1000 }),
  ];
  const observed = observeForForecast(core, game, { expected_delay_ms: 50 });
  near(candidate(observed, 'hold').short.contact_ms, 350, 2, 'known invulnerability expiry contact_ms');
});

test('forecast does not invent future invulnerability from prefix collisions to hide later candidate contact', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core, { x: 200, y: 300, invincible_s: 0 });
  game.enemyBullets = [
    bullet({ id: 'prefix-damage-would-happen', x: 200, y: 250, vy: 1000 }),
    bullet({ id: 'later-still-counts', x: 200, y: -67, vy: 1000 }),
  ];
  const observed = observeForForecast(core, game, { expected_delay_ms: 100 });
  assert.ok(observed.forecast.prefix.contact_ms > 20 && observed.forecast.prefix.contact_ms < 60);
  near(candidate(observed, 'hold').short.contact_ms, 350, 2, 'future invulnerability must not mask later contact');
});

test('forecast keeps stopped neutral tail through horizon for short-lease contacts', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core, { x: 200, y: 300 });
  game.enemyBullets = [bullet({ id: 'tail-crossing', x: 228, y: -167, vy: 1000 })];
  const observed = observeForForecast(core, game, { expected_delay_ms: 100 });
  near(candidate(observed, 'right').short.contact_ms, 450, 2, 'short stopped-tail contact_ms');
});

test('forecast exposes factual enemy_clearance_px for every movement and lease independent of invulnerability', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core, { x: 200, y: 300, invincible_s: 1.0 });
  game.enemies = [scoutEnemy({ x: 260, y: 300 })];
  const observed = observeForForecast(core, game, { expected_delay_ms: 100 });
  for (const item of observed.forecast.candidates) {
    for (const lease of ['short', 'medium']) {
      assert.equal(Object.hasOwn(item[lease], 'enemy_clearance_px'), true, `${item.id} ${lease} missing enemy_clearance_px`);
      assert.equal(Number.isFinite(item[lease].enemy_clearance_px), true, `${item.id} ${lease} enemy_clearance_px must be finite`);
      assert.ok(item[lease].enemy_clearance_px >= 0, `${item.id} ${lease} enemy_clearance_px must be non-negative`);
    }
  }
});

test('a dangerous valid recover action moves into contact unchanged and starts the requested lease', () => {
  const { core, controller: controllerApi } = loadModules();
  const controller = controllerApi.createController({ run_id: 'unit-run', epoch: 3 });
  const request = begin(controllerApi, controller, core, 0);
  controllerApi.receiveDecision(controller, validReply(request, {
    decision_id: 'decision-danger',
    movement: 'down',
    fire: 'shoot',
    lease: 'short',
    intent: 'recover',
    confidence: { movement: 0.2, fire: 0.9, lease: 0.7 },
  }), { tick: 0, wall_ms: 121 });
  finish(controllerApi, controller, request, 121);
  const applied = controllerApi.commandForTick(controller, { tick: 0, wall_ms: 122 });
  assert.equal(applied.command.movement, 'down');
  assert.equal(applied.command.fire, 'shoot');
  assert.equal(applied.command.intent, 'recover');
  assert.equal(applied.command.decision_id, 'decision-danger');
  assert.equal(applied.command.start_tick, 0);
  assert.equal(applied.command.end_tick, 15);
  const game = cleanForecastGame(core);
  game.enemyBullets = [bullet({ id: 'danger-below', x: 200, y: 314 })];
  const stepped = core.stepGame(game, applied.command);
  near(game.player.y, 301.8666666666667, 1e-9);
  assert.equal(game.player.x, 200);
  assert.equal(game.player.lives, 2, 'the locally dangerous API choice must not be vetoed');
  assert.ok(stepped.events.some((event) => event.type === 'hit'));
});

test('the coordinator preserves an authorized movement when every route is clear', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core);
  const proposal = { movement: 'down', fire: 'shoot', decision_id: 'proposal-safe', sequence: 7, source: 'djev' };
  const result = core.coordinateAction(game, proposal);

  assert.equal(result.effective_command.movement, 'down');
  assert.equal(result.effective_command.fire, 'shoot');
  assert.equal(result.effective_command.decision_id, 'proposal-safe');
  assert.equal(result.use_bomb, false);
  assert.equal(result.events.length, 0);
});

test('the coordinator replaces a dangerous proposed route with a safe route', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core);
  game.enemyBullets = [bullet({ id: 'below-ship', x: 200, y: 318 })];
  const proposal = { movement: 'down', fire: 'shoot', decision_id: 'proposal-danger', sequence: 8, source: 'djev' };
  const result = core.coordinateAction(game, proposal);
  const override = result.events.find((event) => event.type === 'safety_override');

  assert.notEqual(result.effective_command.movement, 'down');
  assert.equal(result.effective_command.fire, 'shoot');
  assert.equal(result.effective_command.decision_id, 'proposal-danger');
  assert.equal(result.effective_command.source, 'local_safety');
  assert.equal(override.proposed_movement, 'down');
  assert.equal(override.effective_movement, result.effective_command.movement);
  assert.equal(override.safe_route_available, true);
});

test('without a fresh proposal the coordinator dodges an unsafe hold and never authorizes fire', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core);
  game.enemyBullets = [bullet({ id: 'incoming', x: 200, y: 278, vy: 100 })];
  const result = core.coordinateAction(game, null);

  assert.ok(result.effective_command);
  assert.notEqual(result.effective_command.movement, 'hold');
  assert.equal(result.effective_command.fire, 'cease');
  assert.equal(result.effective_command.source, 'local_safety');
  assert.equal(result.effective_command.decision_id, null);
});

test('without a fresh proposal and with safe hold the coordinator leaves movement neutral', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core);
  const result = core.coordinateAction(game, null);

  assert.equal(result.effective_command, null);
  assert.equal(result.events.length, 0);
});

test('known invulnerability prevents the coordinator from overriding an intersecting proposal', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core, { invincible_s: 1 });
  game.enemyBullets = [bullet({ id: 'covered-by-invulnerability', x: 200, y: 300 })];
  const proposal = { movement: 'down', fire: 'shoot', decision_id: 'proposal-invulnerable', sequence: 9, source: 'djev' };
  const result = core.coordinateAction(game, proposal);

  assert.equal(result.effective_command.movement, 'down');
  assert.equal(result.events.some((event) => event.type === 'safety_override'), false);
});

test('when all routes collide the coordinator reports that no safe escape exists', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core);
  game.enemies = [scoutEnemy({ id: 'body-overlap', x: 200, y: 300 })];
  const proposal = { movement: 'hold', fire: 'cease', decision_id: 'proposal-trapped', sequence: 10, source: 'djev' };
  const result = core.coordinateAction(game, proposal);

  const trapped = result.events.find((event) => event.type === 'safety_escape_unavailable');
  assert.ok(trapped);
  assert.equal(trapped.safe_route_available, false);
  assert.equal(trapped.candidate_assessments.length, 9);
  assert.ok(trapped.candidate_assessments.every((candidate) => candidate.contact_ms !== null));
});

test('route ranking prefers clearance, then wall room, then stable action order', () => {
  const { core } = loadModules();
  const routes = [
    { id: 'left', safe: true, contact_ms: null, clearance_px: 20, wall_room_px: 4 },
    { id: 'right', safe: true, contact_ms: null, clearance_px: 19, wall_room_px: 100 },
    { id: 'up', safe: true, contact_ms: null, clearance_px: 20, wall_room_px: 7 },
    { id: 'down', safe: true, contact_ms: null, clearance_px: 20, wall_room_px: 7 },
    { id: 'hold', safe: true, contact_ms: null, clearance_px: 18, wall_room_px: 90 },
  ];
  const trappedRoutes = [
    { id: 'right', safe: false, contact_ms: 80, clearance_px: 5, wall_room_px: 20 },
    { id: 'left', safe: false, contact_ms: 80, clearance_px: 5, wall_room_px: 20 },
    { id: 'up', safe: false, contact_ms: 40, clearance_px: 40, wall_room_px: 100 },
  ];

  assert.equal(core._private.rankRouteOptions(routes, true).id, 'up');
  assert.equal(core._private.rankRouteOptions(trappedRoutes, false).id, 'left');
});

test('a failed safety assessment preserves an authorized route and records the failure', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core);
  game.enemyBullets = null;
  const proposal = { movement: 'left', fire: 'shoot', decision_id: 'proposal-fallback', sequence: 11, source: 'djev' };
  const result = core.coordinateAction(game, proposal);

  assert.equal(result.effective_command.movement, 'left');
  assert.equal(result.effective_command.decision_id, 'proposal-fallback');
  assert.equal(result.use_bomb, false);
  assert.equal(result.events[0].type, 'safety_calculation_failed');
});

test('medium forecasts report move contact, two-step escape options, and endpoint crowding', () => {
  const { core } = loadModules();
  const open = cleanForecastGame(core, { x: 480, y: 400 });
  for (const candidate of core.observeGame(open, { expected_delay_ms: 0 }).forecast.candidates) {
    assert.equal(candidate.medium.move_contact_ms, null);
    assert.equal(candidate.medium.escape_options, 9);
    assert.equal(candidate.medium.crowd_count, 0);
  }

  const game = cleanForecastGame(core, { x: 200, y: 300 });
  game.enemyBullets = [
    bullet({ id: 'blocks-left', x: 144, y: 300 }),
    bullet({ id: 'below-right', x: 256, y: 380 }),
    bullet({ id: 'above-right', x: 256, y: 220 }),
  ];
  const byId = Object.fromEntries(core.observeGame(game, { expected_delay_ms: 0 }).forecast.candidates.map((c) => [c.id, c.medium]));
  assert.ok(byId.left.move_contact_ms > 0);
  assert.equal(byId.left.escape_options, 0);
  assert.equal(byId.hold.move_contact_ms, null);
  assert.ok(byId.hold.escape_options > 0 && byId.hold.escape_options < 9);
  assert.equal(byId.right.move_contact_ms, null);
  assert.equal(byId.right.crowd_count, 2);
  assert.equal(byId.hold.crowd_count, 3);
});

test('missiles launch only under a shoot command, home onto an enemy, and destroy it', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core, { x: 480, y: 550 });
  game.enemies = [scoutEnemy({ id: 'far-target', x: 700, y: 150 })];
  const cease = { movement: 'hold', fire: 'cease', decision_id: 'd-cease', sequence: 1, source: 'djev' };
  core.stepGame(game, cease);
  assert.equal(game.playerMissiles.length, 0);
  core.stepGame(game, null);
  assert.equal(game.playerMissiles.length, 0);

  const shoot = { movement: 'hold', fire: 'shoot', decision_id: 'd-shoot', sequence: 2, source: 'djev' };
  const events = [];
  for (let i = 0; i < 240 && game.enemies.length; i += 1) events.push(...core.stepGame(game, shoot).events);
  const launches = events.filter((e) => e.type === 'missile_launch');
  assert.ok(launches.length >= 1);
  assert.equal(launches[0].target_id, 'far-target');
  assert.equal(launches[0].decision_id, 'd-shoot');
  assert.ok(events.some((e) => e.type === 'missile_hit' && e.enemy_id === 'far-target'));
  assert.ok(events.some((e) => e.type === 'enemy_destroyed' && e.enemy_id === 'far-target' && ['missile', 'gun'].includes(e.source)));
  assert.ok(game.playerMissiles.length <= core.MISSILE.max_active);
});

test('missile launches respect the interval and active cap', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core, { x: 480, y: 550 });
  game.enemies = [{ ...scoutEnemy({ id: 'tank', x: 480, y: -400 }), hp: 99, maxHp: 99 }];
  const shoot = { movement: 'hold', fire: 'shoot', decision_id: 'd', sequence: 1, source: 'djev' };
  let launches = 0;
  for (let i = 0; i < 180; i += 1) {
    launches += core.stepGame(game, shoot).events.filter((e) => e.type === 'missile_launch').length;
    assert.ok(game.playerMissiles.length <= core.MISSILE.max_active);
  }
  assert.ok(launches <= Math.floor(3 / core.MISSILE.launch_interval_s) + 1);
});

test('a bomb clears threats within its radius once per decision and consumes one of three charges', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core, { x: 480, y: 400 });
  game.enemyBullets = [bullet({ id: 'near', x: 480, y: 300 }), bullet({ id: 'far', x: 480, y: 50 })];
  game.enemies = [scoutEnemy({ id: 'near-enemy', x: 600, y: 400 }), scoutEnemy({ id: 'far-enemy', x: 60, y: 60 })];
  assert.equal(game.bomb.charges, 3);
  assert.deepEqual(JSON.parse(JSON.stringify(core.observeGame(game, { expected_delay_ms: 0 }).state.bomb)), { charges: 3, max_charges: 6, radius_px: 200, bullets_in_radius: 1, enemies_in_radius: 1, sacrifice_jets: 0 });

  const hold = { movement: 'hold', fire: 'cease', bomb: 'hold', decision_id: 'd0', sequence: 1, source: 'djev' };
  core.stepGame(game, hold);
  assert.equal(game.bomb.charges, 3);

  const detonate = { movement: 'hold', fire: 'cease', bomb: 'detonate', decision_id: 'd1', sequence: 2, source: 'djev' };
  const { events } = core.stepGame(game, detonate);
  const blast = events.find((e) => e.type === 'bomb_detonated');
  assert.deepEqual([...blast.removed_bullet_ids], ['near']);
  assert.deepEqual([...blast.destroyed_enemy_ids], ['near-enemy']);
  assert.equal(blast.charges_after, 2);
  assert.deepEqual(game.enemyBullets.map((b) => b.id), ['far']);
  assert.deepEqual(game.enemies.map((e) => e.id), ['far-enemy']);

  core.stepGame(game, detonate);
  assert.equal(game.bomb.charges, 2, 'the same decision must not detonate twice across its lease');
  for (const id of ['d2', 'd3']) core.stepGame(game, { ...detonate, decision_id: id });
  assert.equal(game.bomb.charges, 0);
  assert.ok(core.stepGame(game, { ...detonate, decision_id: 'd4' }).events.some((e) => e.type === 'bomb_unavailable'));
  assert.equal(game.counters.bombsUsed, 3);

  // With bombs gone, an escort jet self-destructs for the same blast.
  game.wingmen = 2;
  game.enemyBullets = [bullet({ id: 'late', x: 480, y: 330 })];
  const sacrifice = core.stepGame(game, { ...detonate, decision_id: 'd5' }).events.find((e) => e.type === 'bomb_detonated');
  assert.equal(sacrifice.source, 'jet_sacrifice');
  assert.deepEqual([...sacrifice.removed_bullet_ids], ['late']);
  assert.equal(game.wingmen, 1);
  assert.equal(game.bomb.charges, 0);
  assert.equal(game.counters.jetSacrifices, 1);
});

test('clearing every tenth wave grants one extra life, up to five', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core, { x: 480, y: 550 });
  const clearWave = (wave) => { game.wave = wave; game.waveActive = true; game.enemies = []; return core.stepGame(game, null).events; };
  assert.equal(game.player.lives, 3);
  assert.ok(!clearWave(5).some((e) => e.type === 'extra_life'));
  assert.ok(clearWave(10).some((e) => e.type === 'extra_life' && e.lives === 4));
  assert.equal(game.wave, 11);
  clearWave(20);
  assert.equal(game.player.lives, 5);
  clearWave(30);
  assert.equal(game.player.lives, 5, 'lives cap at five');
});

test('a new wave keeps bullets in flight instead of clearing them', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core, { x: 480, y: 550 });
  game.waveActive = true;
  game.enemies = [];
  game.enemyBullets = [bullet({ id: 'old-wave-bullet', x: 100, y: 100, vy: 50 })];
  game.playerBullets = [{ id: 'old-shot', x: 300, y: 300, vx: 0, vy: -580, radius: 3 }];
  core.stepGame(game, null);
  assert.equal(game.wave, 2);
  assert.ok(game.enemyBullets.some((b) => b.id === 'old-wave-bullet'));
  assert.ok(game.playerBullets.some((b) => b.id === 'old-shot'));
});

test('flying into pickups adds bombs up to six and raises weapon level to spread shots and the missile caps', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core, { x: 480, y: 400 });
  game.pickupClock_s = { bomb: 99, weapon: 99 };
  const pickup = (id, kind) => ({ id, kind, x: 480, y: 400, vx: 0, vy: 0, age_s: 0, hue: 10 });
  for (let i = 0; i < 4; i += 1) {
    game.pickups = [pickup(`b${i}`, 'bomb')];
    const { events } = core.stepGame(game, null);
    assert.ok(events.some((e) => e.type === 'pickup_collected' && e.kind === 'bomb'));
  }
  assert.equal(game.bomb.charges, core.BOMB.max_charges, 'bombs cap at six');

  const shoot = { movement: 'hold', fire: 'shoot', decision_id: 'd', sequence: 1, source: 'djev' };
  const shotsAt = (level) => {
    game.weaponLevel = level;
    game.player.cooldown_s = 0;
    return core.stepGame(game, shoot).events.filter((e) => e.type === 'shot').length;
  };
  assert.equal(shotsAt(0), 1);
  for (let i = 0; i < 7; i += 1) {
    game.weaponLevel = Math.min(game.weaponLevel, core.WEAPON.max_level);
    game.pickups = [pickup(`w${i}`, 'weapon')];
    core.stepGame(game, null);
  }
  assert.equal(game.weaponLevel, core.WEAPON.max_level);
  assert.equal(shotsAt(core.WEAPON.max_level), 5);

  game.enemies = [{ ...scoutEnemy({ id: 'sponge', x: 480, y: 60 }), hp: 999, maxHp: 999 }];
  game.enemyBullets = [];
  const flying = (n) => Array.from({ length: n }, (_, i) => ({ id: `m${i}`, x: 10, y: 600, heading: 0, age_s: 0, target_id: 'sponge', decision_id: 'd' }));
  const launchesWith = (active) => {
    game.playerMissiles = flying(active);
    game.missileClock_s = 0;
    return core.stepGame(game, shoot).events.filter((e) => e.type === 'missile_launch').length;
  };
  assert.equal(core.MISSILE.max_active_top, 8);
  assert.equal(launchesWith(7), 1, 'the missile swarm level allows an eighth missile');
  assert.equal(launchesWith(8), 0);
  game.weaponLevel = 3;
  assert.equal(launchesWith(5), 1, 'levels 2-4 allow a sixth missile');
  assert.equal(launchesWith(6), 0);
  game.weaponLevel = 0;
  assert.equal(launchesWith(core.MISSILE.max_active), 0, 'level 0 caps at the base count');
});

test('forecast marks moves that collect or approach a wanted pickup, and ignores unwanted ones', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core, { x: 480, y: 400 });
  // 160 px away: moving right brings the ship inside the 110 px magnet radius within the lease.
  game.pickups = [{ id: 'p', kind: 'bomb', x: 640, y: 400, vx: 0, vy: 0, age_s: 0, hue: 0 }];
  const byId = () => Object.fromEntries(core.observeGame(game, { expected_delay_ms: 0 }).forecast.candidates.map((c) => [c.id, c.medium]));
  let forecast = byId();
  assert.equal(forecast.right.pickup_collect, 'bomb');
  assert.equal(forecast.left.pickup_collect, null);
  assert.equal(forecast.left.pickup_toward, null);
  game.pickups[0].x = 800;
  forecast = byId();
  assert.equal(forecast.right.pickup_collect, null);
  assert.equal(forecast.right.pickup_toward, 'bomb');
  game.bomb.charges = core.BOMB.max_charges;
  forecast = byId();
  assert.equal(forecast.right.pickup_toward, null, 'full bombs make bomb pickups unwanted');
});

// A forecast fixture with no wave refills, pickup spawns, or boss, for the weapon and pickup rules.
function quietGame(core, overrides) {
  const game = cleanForecastGame(core, overrides);
  game.waveActive = false;
  game.pickups = [];
  game.pickupClock_s = { bomb: 99, weapon: 99, wingman: 99 };
  game.boss.clock_s = 999;
  return game;
}

test('weapon max level is 5 and each level adds its rule', () => {
  const { core } = loadModules();
  assert.equal(core.WEAPON.max_level, 5);
  for (const table of ['spread_by_level', 'missile_salvo_by_level', 'missile_cap_bonus_by_level']) {
    assert.equal(core.WEAPON[table].length, core.WEAPON.max_level + 1, table);
  }
  assert.equal(core.MISSILE.max_active_top, 8);
});

test('the pickup magnet pulls a nearby pickup in and collects it without the ship moving', () => {
  const { core } = loadModules();
  const game = quietGame(core, { x: 480, y: 540 });
  game.pickupClock_s = { bomb: 99, weapon: 99, wingman: 99 };
  // 90 px left of the ship, drifting away from it: inside the 110 px radius, not touching the ship.
  game.pickups = [{ id: 'mag', kind: 'weapon', x: 390, y: 540, vx: -55, vy: 0, age_s: 0, hue: 0 }];
  const before = Math.abs(game.pickups[0].x - game.player.x);
  core.stepGame(game, null);
  assert.ok(Math.abs(game.pickups[0].x - game.player.x) < before, 'the pickup moves toward the ship');
  let collected = null;
  for (let tick = 0; tick < 60 && !collected; tick += 1) {
    collected = core.stepGame(game, null).events.find((e) => e.type === 'pickup_collected');
  }
  assert.ok(collected, 'collected within a second');
  assert.equal(collected.kind, 'weapon');
  assert.equal(game.weaponLevel, 1);
  assert.equal(game.player.x, 480, 'the ship never moved');

  // Outside the radius a pickup keeps drifting.
  const far = quietGame(core, { x: 480, y: 540 });
  far.pickupClock_s = { bomb: 99, weapon: 99, wingman: 99 };
  far.pickups = [{ id: 'far', kind: 'bomb', x: 300, y: 400, vx: 0, vy: 0, age_s: 0, hue: 0 }];
  core.stepGame(far, null);
  assert.deepEqual([far.pickups[0].x, far.pickups[0].y], [300, 400]);
});

test('a pickup that reaches the magnet radius is collected even while the ship flies away', () => {
  const { core } = loadModules();
  const game = quietGame(core, { x: 480, y: 450 });
  game.pickupClock_s = { bomb: 99, weapon: 99, wingman: 99 };
  // Just inside the edge, drifting away at full drift speed, while the ship flees at full speed.
  game.pickups = [{ id: 'edge', kind: 'bomb', x: 375, y: 450, vx: -55, vy: 0, age_s: 0, hue: 0 }];
  const flee = { movement: 'right', fire: 'cease', decision_id: 'flee', sequence: 1, source: 'djev' };
  let collected = false;
  for (let tick = 0; tick < 120 && !collected; tick += 1) collected = core.stepGame(game, flee).events.some((e) => e.type === 'pickup_collected');
  assert.ok(collected);
});

test('forecast collect facts follow the magnet radius', () => {
  const { core } = loadModules();
  const game = quietGame(core, { x: 480, y: 400 });
  game.pickups = [{ id: 'near', kind: 'weapon', x: 580, y: 400, vx: 0, vy: 0, age_s: 0, hue: 0 }];
  const byId = () => Object.fromEntries(core.observeGame(game, { expected_delay_ms: 0 }).forecast.candidates.map((c) => [c.id, c.medium]));
  // Already within 110 px: every move collects it, because the magnet reaches it whatever the ship does.
  assert.ok(Object.values(byId()).every((m) => m.pickup_collect === 'weapon'));
  game.pickups[0].age_s = core.PICKUP.lifetime_s - .2;
  assert.ok(Object.values(byId()).every((m) => m.pickup_collect === null), 'a pickup about to expire is not promised');
});

test('escort shots lead the nearest enemy within range, and keep their facing with none in range', () => {
  const { core } = loadModules();
  const game = quietGame(core, { x: 480, y: 500 });
  game.wingmen = 1; // slot 0 faces left (180 deg)
  game.wingPhase = 0;
  const shoot = { movement: 'hold', fire: 'shoot', decision_id: 'esc', sequence: 1, source: 'djev' };
  const escortShot = () => game.playerBullets.find((s) => s.wingman === 0 && s.id === game.playerBullets.filter((b) => b.wingman === 0).at(-1).id);

  core.stepGame(game, shoot);
  let shot = escortShot();
  assert.ok(shot.vx < 0 && Math.abs(shot.vy) < 1e-6, 'no target: fires along its facing');

  // An enemy far off the jet's axis: up and to the right, within 380 px.
  game.playerBullets = [];
  game.wingClock_s = 0;
  game.player.cooldown_s = 0;
  game.enemies = [{ ...scoutEnemy({ id: 'off-axis', x: 600, y: 250 }), vx: 35, hp: 999, maxHp: 999 }];
  game.missileClock_s = 99; // keep homing missiles out of this check
  core.stepGame(game, shoot);
  shot = escortShot();
  assert.equal(shot.aim_id, 'off-axis');
  const wing = { x: shot.x - shot.vx / 60, y: shot.y - shot.vy / 60 };
  const enemy = game.enemies[0];
  const toEnemy = Math.atan2(enemy.y - wing.y, enemy.x - wing.x);
  const heading = Math.atan2(shot.vy, shot.vx);
  assert.ok(Math.abs(heading - toEnemy) < .35, 'heads toward the enemy');
  assert.ok(heading > toEnemy - 1e-9, 'leads the target in its direction of motion (clockwise, since it drifts right and down)');
  assert.ok(Math.abs(Math.hypot(shot.vx, shot.vy) - core.WINGMAN.shot_speed) < 1e-6);

  // Fly the shot forward: it meets the moving enemy.
  let hit = false;
  for (let tick = 0; tick < 60 && !hit; tick += 1) hit = core.stepGame(game, { ...shoot, fire: 'cease' }).events.some((e) => e.type === 'enemy_damaged' && e.shot_id === shot.id);
  assert.ok(hit, 'the led shot hits');

  // Escorts never fire on cease.
  game.playerBullets = [];
  game.wingClock_s = 0;
  core.stepGame(game, { ...shoot, fire: 'cease' });
  assert.equal(game.playerBullets.length, 0);
});

test('rapid fire halves the gun cooldown from level 3', () => {
  const { core } = loadModules();
  const game = quietGame(core, { x: 480, y: 500 });
  const shoot = { movement: 'hold', fire: 'shoot', decision_id: 'rapid', sequence: 1, source: 'djev' };
  const cooldownAt = (level) => { game.weaponLevel = level; game.player.cooldown_s = 0; core.stepGame(game, shoot); return game.player.cooldown_s; };
  const base = cooldownAt(2);
  assert.ok(Math.abs(base - 0.17) < 1e-9, 'base gun cooldown');
  assert.ok(Math.abs(cooldownAt(3) - base * .5) < 1e-9);
  assert.ok(Math.abs(cooldownAt(5) - base * .5) < 1e-9);
  const shotsIn = (level) => {
    game.weaponLevel = level; game.player.cooldown_s = 0; game.playerBullets = [];
    let n = 0;
    for (let tick = 0; tick < 60; tick += 1) n += core.stepGame(game, shoot).events.filter((e) => e.type === 'shot').length;
    return n / core.WEAPON.spread_by_level[level];
  };
  assert.ok(shotsIn(3) >= shotsIn(2) * 1.6, 'about twice the volleys per second (tick-quantised)');
});

test('explosive shells at level 4 splash nearby enemies and bullets; the boss only takes damage', () => {
  const { core } = loadModules();
  const shoot = { movement: 'hold', fire: 'shoot', decision_id: 'boom', sequence: 1, source: 'djev' };
  const volley = (level) => {
    const game = quietGame(core, { x: 480, y: 560 });
    game.weaponLevel = level;
    core.stepGame(game, shoot);
    return game.playerBullets.filter((b) => b.wingman === undefined);
  };
  assert.ok(volley(4).every((b) => b.explosive), 'level 4 gun shells are explosive');
  assert.ok(volley(3).every((b) => !b.explosive), 'level 3 shells are not');

  // One level 4 shell flying straight up into 'struck'; 'neighbour' sits 30 px beside the hit point.
  const shell = (game) => { game.playerBullets = [{ id: 'shell', x: 480, y: 520, vx: 0, vy: -580, radius: 3, level: 4, explosive: true, authorizedBy: null }]; };
  const game = quietGame(core, { x: 480, y: 580 });
  game.enemies = [
    { ...scoutEnemy({ id: 'struck', x: 480, y: 470 }), phase: 0 },
    { ...scoutEnemy({ id: 'neighbour', x: 510, y: 480 }), hp: 1, maxHp: 1, phase: 0 },
    { ...scoutEnemy({ id: 'far-away', x: 700, y: 470 }), hp: 1, maxHp: 1, phase: 0 },
  ];
  game.enemyBullets = [bullet({ id: 'near-bullet', x: 465, y: 490 }), bullet({ id: 'far-bullet', x: 200, y: 450 })];
  shell(game);
  let splash = null;
  for (let tick = 0; tick < 20 && !splash; tick += 1) splash = core.stepGame(game, null).events.find((e) => e.type === 'splash');
  assert.ok(splash, 'the shell burst on hit');
  assert.deepEqual([...splash.destroyed_enemy_ids], ['neighbour']);
  assert.ok(!game.enemies.some((e) => e.id === 'neighbour'));
  assert.equal(game.enemies.find((e) => e.id === 'struck').hp, 1, 'the struck enemy takes the normal hit only');
  assert.ok(game.enemies.some((e) => e.id === 'far-away'), 'outside the radius is untouched');
  assert.deepEqual([...splash.cleared_bullet_ids], ['near-bullet']);
  assert.ok(game.enemyBullets.some((b) => b.id === 'far-bullet'));
  assert.equal(game.counters.splashKills, 1);

  // The same shell without the explosive flag leaves the neighbour alone.
  const plain = quietGame(core, { x: 480, y: 580 });
  plain.enemies = [{ ...scoutEnemy({ id: 'struck', x: 480, y: 470 }), phase: 0 }, { ...scoutEnemy({ id: 'neighbour', x: 510, y: 480 }), hp: 1, maxHp: 1, phase: 0 }];
  shell(plain);
  delete plain.playerBullets[0].explosive;
  for (let tick = 0; tick < 20; tick += 1) assert.ok(!core.stepGame(plain, null).events.some((e) => e.type === 'splash'));
  assert.ok(plain.enemies.some((e) => e.id === 'neighbour'));

  // A boss inside the splash only loses hp.
  const bossGame = quietGame(core, { x: 480, y: 580 });
  bossGame.enemies = [{ ...scoutEnemy({ id: 'struck', x: 480, y: 470 }), phase: 0 }, { id: 'boss-x', type: 'boss', x: 480, y: 410, w: 96, h: 58, hp: 3, maxHp: 45, vx: 0, phase: 0 }];
  shell(bossGame);
  let bossSplash = null;
  for (let tick = 0; tick < 20 && !bossSplash; tick += 1) bossSplash = core.stepGame(bossGame, null).events.find((e) => e.type === 'splash');
  assert.ok(bossSplash);
  const boss = bossGame.enemies.find((e) => e.id === 'boss-x');
  assert.ok(boss && boss.hp === 3 - core.WEAPON.splash_damage, 'the boss takes splash damage, not instant death');
});

test('a level 5 salvo launches 8 fanned missiles in parallel, and escorts keep their slots within the cap', () => {
  const { core } = loadModules();
  const game = quietGame(core, { x: 480, y: 500 });
  game.weaponLevel = 5;
  game.enemies = [{ ...scoutEnemy({ id: 'sponge', x: 480, y: 60 }), hp: 999, maxHp: 999 }];
  const shoot = { movement: 'hold', fire: 'shoot', decision_id: 'swarm', sequence: 1, source: 'djev' };
  const launches = core.stepGame(game, shoot).events.filter((e) => e.type === 'missile_launch');
  assert.equal(launches.length, 8, 'eight missiles on the same tick');
  assert.equal(game.playerMissiles.length, 8);
  const headings = game.playerMissiles.map((m) => m.heading);
  assert.equal(new Set(headings.map((h) => h.toFixed(6))).size, 8, 'each on its own fanned heading');
  assert.ok(Math.max(...headings) - Math.min(...headings) <= 2 * core.WEAPON.swarm_fan_rad + 1e-9);

  const escorted = quietGame(core, { x: 480, y: 500 });
  escorted.weaponLevel = 5;
  escorted.wingmen = 2;
  escorted.enemies = [{ ...scoutEnemy({ id: 'sponge', x: 480, y: 60 }), hp: 999, maxHp: 999 }];
  const withEscorts = core.stepGame(escorted, shoot).events.filter((e) => e.type === 'missile_launch');
  assert.equal(withEscorts.length, 8, 'the total salvo stays at 8');
  assert.equal(withEscorts.filter((e) => e.wingman !== undefined).length, 2, 'both escort missile slots still fire');
});

test('controller carries a Djev bomb choice onto the active command and rejects unknown bomb values', () => {
  const { controller: api } = loadModules();
  const control = api.createController({ run_id: 'bomb-run', epoch: 1 });
  api.beginDecision(control, { tick: 0, wall_ms: 0, state: {}, forecast: {}, checkpoint: {} });
  const reply = { run_id: 'bomb-run', epoch: 1, sequence: 1, api_ok: true, valid_choice: true, decision_id: 'b1', movement: 'left', fire: 'shoot', lease: 'medium', intent: 'evade', bomb: 'detonate' };
  api.receiveDecision(control, reply, { tick: 10, wall_ms: 150 });
  api.finishDecision(control, { sequence: 1, wall_ms: 150 });
  assert.equal(api.commandForTick(control, { tick: 10, wall_ms: 150 }).command.bomb, 'detonate');

  api.beginDecision(control, { tick: 11, wall_ms: 160, state: {}, forecast: {}, checkpoint: {} });
  const rejected = api.receiveDecision(control, { ...reply, sequence: 2, decision_id: 'b2', bomb: 'maybe' }, { tick: 20, wall_ms: 300 });
  assert.equal(rejected.events[0].reason, 'unknown_bomb');
});

test('canonical policy ticks return the proposal separately from the effective command', () => {
  const { core } = loadModules();
  const game = cleanForecastGame(core, { x: 200, y: 300 });
  game.policy_mode = 'hybrid';
  game.enemyBullets = [bullet({ id: 'tick-risk', x: 200, y: 318 })];
  const proposal = { movement: 'down', fire: 'shoot', decision_id: 'proposal-tick', sequence: 12, source: 'djev' };
  const result = core.stepPolicyTick(game, proposal);

  assert.equal(result.model_proposal.movement, 'down');
  assert.notEqual(result.effective_command.movement, 'down');
  assert.equal(result.effective_command.decision_id, 'proposal-tick');
  assert.equal(result.effective_command.source, 'local_safety');
  assert.ok(result.events.some((event) => event.type === 'safety_override'));
});

test('missing, invalid, and stale atomic replies produce neutral command state', () => {
  const { core, controller: controllerApi } = loadModules();
  for (const reply of [
    { movement: 'left', fire: null, lease: 'short', valid_choice: false, api_ok: true },
    { movement: 'left', fire: 'shoot', lease: 'long', valid_choice: true, api_ok: true },
    { movement: 'left', fire: 'shoot', lease: 'short', valid_choice: true, api_ok: false },
  ]) {
    const controller = controllerApi.createController({ run_id: 'unit-run', epoch: 4 });
    const request = begin(controllerApi, controller, core, 4);
    controllerApi.receiveDecision(controller, validReply(request, {
      decision_id: 'bad-decision',
      ...reply,
    }), { tick: 4, wall_ms: 100 });
    finish(controllerApi, controller, request, 100);
    const result = controllerApi.commandForTick(controller, { tick: 4, wall_ms: 101 });
    assert.equal(result.command, null);
  }

  const stale = controllerApi.createController({ run_id: 'unit-run', epoch: 5 });
  const staleRequest = begin(controllerApi, stale, core, 0);
  controllerApi.receiveDecision(stale, validReply(staleRequest, {
    decision_id: 'stale-decision',
    movement: 'left',
    fire: 'shoot',
    lease: 'short',
  }), { tick: 0, wall_ms: 601 });
  finish(controllerApi, stale, staleRequest, 601);
  assert.equal(controllerApi.commandForTick(stale, { tick: 0, wall_ms: 602 }).command, null);
});

test('lease expiry and old callbacks cannot extend or cancel newer authority', () => {
  const { core, controller: controllerApi } = loadModules();
  const controller = controllerApi.createController({ run_id: 'unit-run', epoch: 8 });
  const oldRequest = begin(controllerApi, controller, core, 0);
  controllerApi.invalidateController(controller, 'manual-reset');
  const currentRequest = begin(controllerApi, controller, core, 1);
  controllerApi.receiveDecision(controller, validReply(oldRequest, {
    decision_id: 'old-decision',
    movement: 'left',
    fire: 'shoot',
    lease: 'medium',
  }), { tick: 1, wall_ms: 110 });
  controllerApi.receiveDecision(controller, validReply(currentRequest, {
    decision_id: 'current-decision',
    movement: 'right',
    fire: 'cease',
    lease: 'short',
  }), { tick: 1, wall_ms: 120 });
  finish(controllerApi, controller, currentRequest, 120);
  assert.equal(controllerApi.commandForTick(controller, { tick: 1, wall_ms: 121 }).command.movement, 'right');
  assert.equal(controllerApi.commandForTick(controller, { tick: 15, wall_ms: 360 }).command.movement, 'right');
  assert.equal(controllerApi.commandForTick(controller, { tick: 16, wall_ms: 377 }).command, null);
});

test('stale old replies cannot neutralize a newer active command', () => {
  const { core, controller: controllerApi } = loadModules();
  const controller = controllerApi.createController({ run_id: 'unit-run', epoch: 11 });
  const oldRequest = begin(controllerApi, controller, core, 0);
  controllerApi.invalidateController(controller, 'restart');
  const currentRequest = begin(controllerApi, controller, core, 1);
  controllerApi.receiveDecision(controller, validReply(currentRequest, {
    decision_id: 'current-authority',
    movement: 'up',
    fire: 'shoot',
    lease: 'medium',
  }), { tick: 1, wall_ms: 100 });
  finish(controllerApi, controller, currentRequest, 100);
  assert.equal(controllerApi.commandForTick(controller, { tick: 1, wall_ms: 101 }).command.decision_id, 'current-authority');

  controllerApi.receiveDecision(controller, validReply(oldRequest, {
    decision_id: 'obsolete-neutralizer',
    movement: 'left',
    fire: 'cease',
    lease: 'short',
    api_ok: false,
    valid_choice: false,
  }), { tick: 2, wall_ms: 120 });
  const afterOldReply = controllerApi.commandForTick(controller, { tick: 2, wall_ms: 121 });
  assert.equal(afterOldReply.command.decision_id, 'current-authority');
  assert.equal(afterOldReply.command.movement, 'up');
});

test('missing run_id or decision_id is rejected without inventing command authority', () => {
  const { core, controller: controllerApi } = loadModules();
  for (const replyPatch of [
    { run_id: undefined },
    { run_id: 'wrong-run' },
    { decision_id: undefined },
    { decision_id: '' },
  ]) {
    const controller = controllerApi.createController({ run_id: 'unit-run', epoch: 12 });
    const request = begin(controllerApi, controller, core, 0);
    controllerApi.receiveDecision(controller, validReply(request, replyPatch), { tick: 0, wall_ms: 100 });
    finish(controllerApi, controller, request, 100);
    const result = controllerApi.commandForTick(controller, { tick: 0, wall_ms: 101 });
    assert.equal(result.command, null);
    assert.equal(result.events.some((event) => event.type === 'command_applied'), false);
  }
});

test('neutral ticks do not autofire and shots link to an authorizing decision', () => {
  const { core } = loadModules();
  const game = core.createGame(makeManifest());
  for (let i = 0; i < 20; i += 1) {
    const { events } = core.stepGame(game, null);
    const unauthorizedShots = playerShotEvents(events);
    assert.equal(unauthorizedShots.length, 0);
  }

  const command = {
    decision_id: 'run-unit:1:1',
    sequence: 1,
    movement: 'hold',
    fire: 'shoot',
    lease: 'short',
    start_tick: 20,
    end_tick: 35,
    applied_wall_ms: 333.4,
    expires_wall_ms: 583.4,
  };
  let sawShot = false;
  for (let tick = 20; tick < 35; tick += 1) {
    const { events } = core.stepGame(game, command);
    for (const event of playerShotEvents(events)) {
      sawShot = true;
      assert.equal(shotDecisionId(event), 'run-unit:1:1');
    }
  }
  assert.equal(sawShot, true);
});

test('shoot cooldown continues through cease and never banks extra shots', () => {
  const { core } = loadModules();
  const game = core.createGame(makeManifest());
  const shoot = {
    decision_id: 'cooldown-shoot',
    sequence: 1,
    movement: 'hold',
    fire: 'shoot',
    lease: 'medium',
    start_tick: 0,
    end_tick: 30,
    applied_wall_ms: 0,
    expires_wall_ms: 500,
  };
  const firstShotTicks = [];
  for (let tick = 0; tick < 30; tick += 1) {
    const { events } = core.stepGame(game, shoot);
    for (const event of playerShotEvents(events)) firstShotTicks.push(event.tick);
  }
  assert.deepEqual(firstShotTicks, [0, 11, 22]);

  for (let tick = 30; tick < 60; tick += 1) {
    const { events } = core.stepGame(game, null);
    assert.equal(playerShotEvents(events).length, 0);
  }

  const resumed = { ...shoot, decision_id: 'cooldown-resume', sequence: 2, start_tick: 60, end_tick: 75, applied_wall_ms: 1000, expires_wall_ms: 1250 };
  const { events } = core.stepGame(game, resumed);
  const resumedShots = playerShotEvents(events);
  assert.equal(resumedShots.length, 1);
  assert.equal(shotDecisionId(resumedShots[0]), 'cooldown-resume');
});

test('serialize, restore, and hash form a deterministic replay boundary', () => {
  const { core } = loadModules();
  const game = core.createGame(makeManifest());
  const command = {
    decision_id: 'move-decision',
    sequence: 1,
    movement: 'up_left',
    fire: 'cease',
    lease: 'medium',
    start_tick: 0,
    end_tick: 30,
    applied_wall_ms: 0,
    expires_wall_ms: 500,
  };
  for (let i = 0; i < 12; i += 1) core.stepGame(game, command);
  const checkpoint = core.serializeGame(game);
  const restored = core.restoreGame(checkpoint);
  assert.equal(core.hashGame(restored), core.hashGame(game));
  assert.deepEqual(core.serializeGame(restored), checkpoint);
});
