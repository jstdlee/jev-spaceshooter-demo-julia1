#!/usr/bin/env node
// Deterministic lockstep simulation through the production path: real core, real Djev controller,
// real bridge labels. Game time pauses while a decision is in flight; the reply is applied
// `--latency-ms` of game time after its observation, matching the live round trip.
//
//   go run ./demo/bridge --port 7870 --upstream oracle --runs-dir /tmp/sim-runs &   # perfect label reader
//   go run ./demo/bridge --port 7871 --runs-dir /tmp/sim-runs &                     # real Djev
//   node demo/lockstep_sim.cjs --url http://127.0.0.1:7870 --seeds 1-8 --profile browser-hard
'use strict';

const path = require('node:path');
const {
  BENCHMARK_PROFILES, buildManifest, buildObservationContext, activeCommandForObservation, loadSpaceModulesFromHtml,
} = require('./benchmark.cjs');

const PROFILES = {
  ...BENCHMARK_PROFILES,
  // The settings from the browser runs that motivated this harness.
  'browser-hard': { bulletDensity: 3.5, enemyDensity: 2.75, fastBulletRatio: 0.7, fastBulletSpeed: 2.2 },
};

function parseArgs(argv) {
  const options = { url: 'http://127.0.0.1:7870', seeds: [1, 2, 3, 4, 5, 6, 7, 8], profile: 'browser-hard', latencyMs: 220, seconds: 120, verbose: false };
  for (let i = 0; i < argv.length; i += 1) {
    const value = () => argv[++i];
    const arg = argv[i];
    if (arg === '--url') options.url = value();
    else if (arg === '--seeds') {
      const text = value();
      const range = text.match(/^(\d+)-(\d+)$/);
      options.seeds = range ? Array.from({ length: Number(range[2]) - Number(range[1]) + 1 }, (_, k) => Number(range[1]) + k) : text.split(',').map(Number);
    } else if (arg === '--profile') options.profile = value();
    else if (arg === '--latency-ms') options.latencyMs = Number(value());
    else if (arg === '--seconds') options.seconds = Number(value());
    else if (arg === '--verbose') options.verbose = true;
    else throw new Error(`unknown argument ${arg}`);
  }
  if (!PROFILES[options.profile]) throw new Error(`unknown profile ${options.profile}`);
  return options;
}

async function post(url, route, body) {
  const response = await fetch(url + route, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  const reply = await response.json();
  if (!response.ok) throw new Error(`${route} ${response.status}: ${reply.error}`);
  return reply;
}

async function runSeed(modules, options, seed) {
  const { core, controller: api, engineHash } = modules;
  const DT = core.DT_MS;
  const manifest = buildManifest({ seed, profile: options.profile, difficulty: PROFILES[options.profile], engineHash, engineVersion: core.ENGINE_VERSION, rules: core.RULES });
  const { run_id: runId } = await post(options.url, '/api/run/start', { schema_version: 1, manifest });
  const game = core.createGame(manifest);
  const control = api.createController({ run_id: runId, epoch: 1 });
  const latencyTicks = Math.max(1, Math.round(options.latencyMs / DT));
  const maxTicks = Math.round(options.seconds * 1000 / DT);
  const stats = { decisions: 0, bombs: 0, bomb_pickups: 0, weapon_pickups: 0, pickups_spawned: 0, hits: [] };
  let pending = null; // { due_tick, reply }

  while (!game.terminal && game.tick < maxTicks) {
    const wallMs = game.tick * DT;
    if (pending && game.tick >= pending.due_tick) {
      api.receiveDecision(control, pending.reply, { tick: game.tick, wall_ms: wallMs });
      api.finishDecision(control, { sequence: pending.reply.sequence, wall_ms: wallMs });
      pending = null;
    }
    const { command } = api.commandForTick(control, { tick: game.tick, wall_ms: wallMs });
    const step = core.stepPolicyTick(game, command);
    for (const event of step.events) {
      if (event.type === 'hit') stats.hits.push({ t: +(game.sim_ms / 1000).toFixed(1), kind: event.source.kind, x: Math.round(event.player.x), y: Math.round(event.player.y) });
      if (event.type === 'bomb_detonated') stats.bombs += 1;
      if (event.type === 'pickup_spawned') stats.pickups_spawned += 1;
      if (event.type === 'pickup_collected') stats[`${event.kind}_pickups`] += 1;
    }
    // Like the live loop: request only after a tick has run with the newly applied command.
    if (!pending && !control.pending && !game.terminal) {
      const requestMs = game.tick * DT;
      const active = activeCommandForObservation(control.active || null, game.tick, requestMs);
      const stats8 = { expected_delay_ms: options.latencyMs, latency_samples: 8, latency_spread_ms: 0 };
      const observed = core.observeGame(game, buildObservationContext({ control, stats: stats8, activeCommand: active, recentCommands: [], recentHits: [], currentSimMs: game.sim_ms }));
      const body = api.beginDecision(control, { tick: game.tick, wall_ms: requestMs, state: observed.state, forecast: observed.forecast, checkpoint: core.serializeGame(game) });
      const reply = await post(options.url, '/api/decision', body);
      stats.decisions += 1;
      pending = { due_tick: game.tick + latencyTicks, reply };
    }
  }
  return { seed, sim_s: +(game.sim_ms / 1000).toFixed(1), lives: game.player.lives, wave: game.wave, kills: game.counters.enemiesDestroyed, weapon_level: game.weaponLevel, ...stats };
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  const modules = loadSpaceModulesFromHtml(path.join(__dirname, 'space-shooter.html'));
  const results = await Promise.all(options.seeds.map((seed) => runSeed(modules, options, seed)));
  for (const r of results) {
    console.log(JSON.stringify(options.verbose ? r : { seed: r.seed, sim_s: r.sim_s, lives: r.lives, wave: r.wave, kills: r.kills, bombs: r.bombs, picked: `${r.bomb_pickups}b+${r.weapon_pickups}w/${r.pickups_spawned}`, weapon: r.weapon_level, decisions: r.decisions, hits: r.hits.map((h) => `${h.t}s@${h.x},${h.y}`).join(' ') }));
  }
  const survived = results.map((r) => r.sim_s);
  const full = survived.filter((s) => s >= options.seconds).length;
  const sum = (key) => results.reduce((a, r) => a + r[key], 0);
  console.log(`${options.profile} latency=${options.latencyMs}ms: mean ${(survived.reduce((a, b) => a + b, 0) / survived.length).toFixed(1)}s, min ${Math.min(...survived)}s, full ${full}/${results.length}, pickups ${sum('bomb_pickups')}b+${sum('weapon_pickups')}w of ${sum('pickups_spawned')}, bombs used ${sum('bombs')}, hits ${results.reduce((a, r) => a + r.hits.length, 0)}`);
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
