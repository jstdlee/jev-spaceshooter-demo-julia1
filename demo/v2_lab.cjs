#!/usr/bin/env node
// v2 strategy lab: the lockstep loop of lockstep_sim.cjs with the decision made in-process by a pluggable
// policy, so strategies can be explored without the bridge or a model. Every hit gets an autopsy that says
// whether a safe move existed when the fatal decisions were made.
//
//   node demo/v2_lab.cjs --policy rollout --profile max2x --seeds 1-8 --seconds 60
//
// Policies (see POLICIES):
//   forecast  argmax over the forecast the bridge labels are built from (a simulation oracle without future knowledge)
//   rollout   clones the game and plays each candidate with the real engine (sees future spawns: a ceiling, not fair)
'use strict';

const path = require('node:path');
const fs = require('node:fs');
const {
  buildManifest, buildObservationContext, activeCommandForObservation, loadSpaceModulesFromHtml,
} = require('./benchmark.cjs');

const PROFILES = {
  max: { bulletDensity: 8, enemyDensity: 6, fastBulletRatio: 1, fastBulletSpeed: 4.8 },
  max2x: { bulletDensity: 16, enemyDensity: 12, fastBulletRatio: 1, fastBulletSpeed: 9.6 },
  'max1.5x': { bulletDensity: 12, enemyDensity: 9, fastBulletRatio: 1, fastBulletSpeed: 7.2 },
};

function parseArgs(argv) {
  const o = { policy: 'forecast', seeds: [1, 2, 3, 4, 5, 6, 7, 8], profile: 'max2x', latencyMs: 30, seconds: 60, out: null, opts: {} };
  for (let i = 0; i < argv.length; i += 1) {
    const value = () => argv[++i];
    const arg = argv[i];
    if (arg === '--policy') o.policy = value();
    else if (arg === '--seeds') {
      const text = value();
      const range = text.match(/^(\d+)-(\d+)$/);
      o.seeds = range ? Array.from({ length: Number(range[2]) - Number(range[1]) + 1 }, (_, k) => Number(range[1]) + k) : text.split(',').map(Number);
    } else if (arg === '--profile') o.profile = value();
    else if (arg === '--latency-ms') o.latencyMs = Number(value());
    else if (arg === '--seconds') o.seconds = Number(value());
    else if (arg === '--out') o.out = value();
    else if (arg === '--opt') { const [k, v] = value().split('='); o.opts[k] = Number.isNaN(Number(v)) ? v : Number(v); }
    else throw new Error(`unknown argument ${arg}`);
  }
  if (!PROFILES[o.profile]) throw new Error(`unknown profile ${o.profile}`);
  return o;
}

// ---- policies -------------------------------------------------------------------------------------------
// A policy gets { game, observed, core, opts, control } and returns { movement, fire, bomb, lease, note }.

const SAFE = (c) => c.contact_ms === null;
// Forecast candidates arrive as { id, medium: {...} }; flatten them to { movement, ...medium }.
const candidatesOf = (observed) => observed.forecast.candidates.map((c) => ({ movement: c.id, ...c.medium }));

function forecastScore(c) {
  if (!SAFE(c)) return -1e6 + (c.contact_ms || 0);              // later contact is less bad
  return (c.escape_options || 0) * 1000 + Math.min(c.escape_clearance_px ?? 0, 200) + Math.min(c.room_px ?? 0, 60) / 2;
}

const POLICIES = {
  forecast({ observed }) {
    const cands = candidatesOf(observed);
    let best = cands[0];
    for (const c of cands) if (forecastScore(c) > forecastScore(best)) best = c;
    return { movement: best.movement, fire: 'shoot', bomb: 'hold', lease: 'medium', note: { best_safe: cands.some(SAFE) } };
  },

  // Ceiling: real-engine rollouts from a clone. Each candidate = the active command for the latency, then the move for
  // `hold` ticks, then the best of the nine follow-ups for `follow` ticks. Score = ticks until the first hit.
  rollout({ game, core, opts, control, latencyTicks }) {
    const holdTicks = opts.hold ?? 15;
    const followTicks = opts.follow ?? 15;
    const moves = core.ACTION_IDS;
    const base = core.serializeGame(game);
    const prefix = control.active ? { movement: control.active.movement, fire: 'shoot', decision_id: 'p', sequence: 0 } : null;
    const run = (g, movement, ticks, id) => {
      const lives = g.player.lives;
      for (let i = 0; i < ticks; i += 1) {
        core.stepPolicyTick(g, { movement, fire: 'shoot', decision_id: id, sequence: 0 });
        if (g.player.lives < lives || g.terminal) return i;
      }
      return ticks;
    };
    const start = core.restoreGame(base);
    const pre = run(start, prefix ? prefix.movement : 'hold', latencyTicks, 'pre');
    if (pre < latencyTicks) return { movement: 'hold', fire: 'shoot', bomb: opts.bombs ? 'detonate' : 'hold', lease: 'medium', note: { doomed_in_prefix: true } };
    const afterPre = core.serializeGame(start);
    let best = null;
    for (const m of moves) {
      const g1 = core.restoreGame(afterPre);
      const t1 = run(g1, m, holdTicks, `a-${m}`);
      let score = t1;
      if (t1 === holdTicks && followTicks > 0) {
        const mid = core.serializeGame(g1);
        let bestFollow = -1;
        for (const n of moves) {
          const g2 = core.restoreGame(mid);
          const t2 = run(g2, n, followTicks, `b-${n}`);
          if (t2 > bestFollow) bestFollow = t2;
          if (bestFollow === followTicks) break;
        }
        score += bestFollow;
      }
      if (!best || score > best.score) best = { movement: m, score };
    }
    const full = holdTicks + followTicks;
    const bomb = opts.bombs && best.score < full ? 'detonate' : 'hold';
    return { movement: best.movement, fire: 'shoot', bomb, lease: 'medium', note: { best_safe: best.score >= full, score: best.score } };
  },

  // Fair Monte-Carlo forecaster: the same real-engine rollouts, but every clone gets a fresh RNG state, so enemy
  // fire is sampled rather than known. Score per move = mean survival over K sampled futures, then position.
  mc(ctx) { return monteCarlo(ctx).decision; },

  // The same scoring as mc, but reading the core's sampled-futures forecast (observeGame) instead of the lab's own
  // rollouts: this is what the bridge sees, so it checks the core implementation against the lab.
  futures({ game, observed, opts, control }) {
    const cands = candidatesOf(observed);
    const W = game.width, H = game.height;
    let best = null;
    for (const c of cands) {
      const e = c.futures.end || game.player;
      const side = Math.min(e.x, W - e.x);
      const wantY = opts.y_frac != null ? H * opts.y_frac : H - 90;
      const pos = (opts.w_side ?? 1) * Math.min(side, 200) / 200 - (opts.w_y ?? 1) * Math.abs(e.y - wantY) / H;
      const keep = control.active && control.active.movement === c.movement ? (opts.w_keep ?? 0.02) : 0;
      const score = c.futures.survival * 10 + pos * (opts.w_pos ?? 0.5) + keep;
      if (!best || score > best.score) best = { movement: c.movement, score };
    }
    return { movement: best.movement, fire: 'shoot', bomb: 'hold', lease: 'medium' };
  },
};

function sampledState(tick, k) {
  // Any fixed function of (tick, k) that is unrelated to the game's own RNG stream.
  let h = (Math.imul(tick + 1, 2654435761) ^ Math.imul(k + 1, 40503)) >>> 0;
  h = Math.imul(h ^ (h >>> 15), 2246822507) >>> 0;
  return (h ^ (h >>> 13)) >>> 0 || 1;
}

function monteCarlo({ game, core, opts, control, latencyTicks }) {
  const K = opts.k ?? 4;
  const holdTicks = opts.hold ?? 15;
  const followTicks = opts.follow ?? 15;
  const moves = core.ACTION_IDS;
  const full = holdTicks + followTicks;
  const prefixMove = control.active ? control.active.movement : 'hold';
  const run = (g, movement, ticks, id) => {
    const lives = g.player.lives;
    for (let i = 0; i < ticks; i += 1) {
      core.stepPolicyTick(g, { movement, fire: 'shoot', decision_id: id, sequence: 0 });
      if (g.player.lives < lives || g.terminal) return i;
    }
    return ticks;
  };
  const base = core.serializeGame(game);
  const totals = Object.fromEntries(moves.map((m) => [m, 0]));
  const ends = Object.fromEntries(moves.map((m) => [m, []]));
  for (let k = 0; k < K; k += 1) {
    const start = core.restoreGame(base);
    start.rng.state = sampledState(game.tick, k);
    if (run(start, prefixMove, latencyTicks, 'pre') < latencyTicks) continue;   // this future is lost whatever we pick
    const afterPre = core.serializeGame(start);
    for (const m of moves) {
      const g1 = core.restoreGame(afterPre);
      const t1 = run(g1, m, holdTicks, `a-${m}`);
      let score = t1;
      if (t1 === holdTicks && followTicks > 0) {
        const mid = core.serializeGame(g1);
        let bestFollow = -1;
        for (const n of moves) {
          const t2 = run(core.restoreGame(mid), n, followTicks, `b-${n}`);
          if (t2 > bestFollow) bestFollow = t2;
          if (bestFollow === followTicks) break;
        }
        score += bestFollow;
      }
      totals[m] += score;
      ends[m].push({ x: g1.player.x, y: g1.player.y });
    }
  }
  // Position preference among equally survivable moves: low (far from the shooters) and away from the side walls.
  const W = game.width, H = game.height;
  const posScore = (m) => {
    const e = ends[m][0] || game.player;
    const side = Math.min(e.x, W - e.x);
    const wantY = opts.y_frac != null ? H * opts.y_frac : H - 90;
    return (opts.w_side ?? 1) * Math.min(side, 200) / 200 - (opts.w_y ?? 1) * Math.abs(e.y - wantY) / H;
  };
  let best = null;
  for (const m of moves) {
    const survival = totals[m] / (K * full);       // 0..1
    const keep = control.active && control.active.movement === m ? (opts.w_keep ?? 0.02) : 0;
    const score = survival * 10 + posScore(m) * (opts.w_pos ?? 0.5) + keep;
    if (!best || score > best.score) best = { movement: m, score, survival };
  }
  const bombAt = opts.bomb_below ?? 0;     // detonate when even the best move survives less than this fraction
  const bomb = best.survival < bombAt ? 'detonate' : 'hold';
  return { decision: { movement: best.movement, fire: 'shoot', bomb, lease: 'medium' }, survival: Object.fromEntries(moves.map((m) => [m, totals[m] / (K * full)])) };
}

// ---- loop -----------------------------------------------------------------------------------------------

async function runSeed(modules, o, seed) {
  const { core, controller: api, engineHash } = modules;
  const DT = core.DT_MS;
  const manifest = buildManifest({ seed, profile: 'hardest', difficulty: PROFILES[o.profile], engineHash, engineVersion: core.ENGINE_VERSION, rules: core.RULES });
  const game = core.createGame(manifest);
  const runId = `lab-${seed}`;
  const control = api.createController({ run_id: runId, epoch: 1 });
  const latencyTicks = Math.max(1, Math.round(o.latencyMs / DT));
  const maxTicks = Math.round(o.seconds * 1000 / DT);
  const policy = POLICIES[o.policy];
  if (!policy) throw new Error(`unknown policy ${o.policy}`);
  const recent = [];      // last decisions, for the autopsy
  const autopsy = [];
  const stats = { decisions: 0, bombs: 0, hits: [], unsafe_decisions: 0, policy_ms: 0 };
  let pending = null;
  let seq = 0;

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
      if (event.type === 'bomb_detonated') stats.bombs += 1;
      if (event.type === 'hit') {
        const t = +(game.sim_ms / 1000).toFixed(2);
        stats.hits.push({ t, kind: event.source.kind, x: Math.round(event.player.x), y: Math.round(event.player.y) });
        const window = recent.filter((r) => r.t > t - 1.0);
        autopsy.push({ seed, t, kind: event.source.kind, fast: !!event.source.fast, x: Math.round(event.player.x), y: Math.round(event.player.y),
          decisions_last_1s: window.length,
          safe_existed_last_1s: window.filter((r) => r.safe_exists).length,
          chose_unsafe_last_1s: window.filter((r) => r.safe_exists && !r.chosen_safe).length,
          trail: window.slice(-6).map((r) => `${r.t}:${r.movement}${r.chosen_safe ? '' : '!'}${r.safe_exists ? '' : '#'}`).join(' ') });
      }
    }
    if (!pending && !control.pending && !game.terminal) {
      const requestMs = game.tick * DT;
      const active = activeCommandForObservation(control.active || null, game.tick, requestMs);
      const stats8 = { expected_delay_ms: o.latencyMs, latency_samples: 8, latency_spread_ms: 0 };
      const context = buildObservationContext({ control, stats: stats8, activeCommand: active, recentCommands: [], recentHits: [], currentSimMs: game.sim_ms });
      if (o.policy === 'futures') context.sampled_futures = { k: o.opts.k ?? 4 };
      const observed = core.observeGame(game, context);
      api.beginDecision(control, { tick: game.tick, wall_ms: requestMs, state: observed.state, forecast: observed.forecast, checkpoint: null });
      const t0 = process.hrtime.bigint();
      const d = policy({ game, observed, core, opts: o.opts, control, latencyTicks });
      stats.policy_ms += Number(process.hrtime.bigint() - t0) / 1e6;
      seq = control.pending.sequence;
      const cands = candidatesOf(observed);
      const chosen = cands.find((c) => c.movement === d.movement);
      const safeExists = cands.some(SAFE);
      if (safeExists && chosen && !SAFE(chosen)) stats.unsafe_decisions += 1;
      recent.push({ t: +(game.sim_ms / 1000).toFixed(2), movement: d.movement, safe_exists: safeExists, chosen_safe: chosen ? SAFE(chosen) : false });
      if (recent.length > 120) recent.shift();
      const reply = { run_id: runId, epoch: 1, sequence: seq, api_ok: true, valid_choice: true, decision_id: `${runId}-${seq}`,
        movement: d.movement, fire: d.fire, lease: d.lease || 'medium', bomb: d.bomb || 'hold', confidence: {} };
      stats.decisions += 1;
      pending = { due_tick: game.tick + latencyTicks, reply };
    }
  }
  return { seed, sim_s: +(game.sim_ms / 1000).toFixed(1), lives: game.player.lives, wave: game.wave, kills: game.counters.enemiesDestroyed,
    weapon: game.weaponLevel, jets: game.wingmen, bombs: stats.bombs, decisions: stats.decisions, unsafe_decisions: stats.unsafe_decisions,
    policy_ms_per_decision: +(stats.policy_ms / Math.max(1, stats.decisions)).toFixed(2),
    hits: stats.hits.map((h) => `${h.t}s@${h.x},${h.y}${h.kind === 'bullet' ? '' : ':' + h.kind}`).join(' '), autopsy };
}

async function main() {
  const o = parseArgs(process.argv.slice(2));
  const modules = loadSpaceModulesFromHtml(path.join(__dirname, 'space-shooter.html'));
  const results = [];
  for (const seed of o.seeds) {         // one seed at a time: this machine has crashed under parallel load
    const r = await runSeed(modules, o, seed);
    results.push(r);
    const { autopsy, ...row } = r;
    console.log(JSON.stringify(row));
  }
  const s = results.map((r) => r.sim_s);
  const full = s.filter((x) => x >= o.seconds).length;
  const autopsy = results.flatMap((r) => r.autopsy);
  const noSafe = autopsy.filter((a) => a.safe_existed_last_1s === 0).length;
  const summary = { policy: o.policy, opts: o.opts, profile: o.profile, latency_ms: o.latencyMs, seconds: o.seconds,
    mean_s: +(s.reduce((a, b) => a + b, 0) / s.length).toFixed(1), min_s: Math.min(...s), full: `${full}/${s.length}`,
    hits: autopsy.length, hits_with_no_safe_move_in_last_1s: noSafe,
    unsafe_picks: results.reduce((a, r) => a + r.unsafe_decisions, 0), bombs: results.reduce((a, r) => a + r.bombs, 0) };
  console.log('SUMMARY ' + JSON.stringify(summary));
  if (o.out) fs.writeFileSync(o.out, JSON.stringify({ summary, results }, null, 1));
}

main().catch((error) => { console.error(error); process.exit(1); });
