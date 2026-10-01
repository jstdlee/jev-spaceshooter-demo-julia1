#!/usr/bin/env node
// Death autopsy from bridge traces: for every hit, where the ship was, and what the model saw and chose in the second
// before it. Classifies each hit:
//   no_safe_move  - no move above DOOMED was offered (forecast or position problem: the ship was already cornered)
//   model_pick    - a better tier was offered but the model chose worse
//   forecast_miss - the chosen move was the best tier and still got hit (sampled futures missed it)
//
//   node demo/v2_autopsy.cjs <runs-dir> <lockstep-log>
// Lockstep runs do not send game events to the bridge, so hits come from the lockstep log (one JSON line per seed, in
// run order; sampled-futures runs are sequential, so run directories and seeds line up).
'use strict';

const fs = require('node:fs');
const path = require('node:path');

const tierOf = (label) => Number((/^Tier (\d) /.exec(label || '') || [0, 9])[1]);

function runDirs(target) {
  if (fs.existsSync(path.join(target, 'events.jsonl'))) return [target];
  return fs.readdirSync(target).filter((d) => d.startsWith('run-')).map((d) => path.join(target, d)).sort();
}

const totals = { hits: 0, no_safe_move: 0, model_pick: 0, forecast_miss: 0 };
const [runsDir, logPath] = process.argv.slice(2);
const seeds = fs.readFileSync(logPath, 'utf8').split('\n').filter((l) => l.startsWith('{')).map((l) => JSON.parse(l));
const dirs = runDirs(runsDir).slice(-seeds.length);
{
  for (const [index, dir] of dirs.entries()) {
    const decisions = [];
    const hits = [];
    for (const line of fs.readFileSync(path.join(dir, 'events.jsonl'), 'utf8').split('\n')) {
      if (!line) continue;
      const e = JSON.parse(line);
      if (e.record_type === 'decision_response' && e.normalized && e.normalized.labels) {
        const st = e.rawsnapshot.state;
        const path_ = e.normalized.labels.path;
        const chosen = Object.keys(path_).find((k) => k.startsWith(`${e.normalized.movement}__`));
        decisions.push({ tick: st.tick, x: st.player.x, y: st.player.y, best: Math.min(...Object.values(path_).map(tierOf)), got: chosen ? tierOf(path_[chosen]) : 9,
          position: e.normalized.position, near: st.surroundings && st.surroundings.bullets_near, center: st.surroundings && st.surroundings.center_distance_px });
      }
    }
    for (const h of (seeds[index].hits || '').split(' ').filter(Boolean)) {
      const m = /^([\d.]+)s@(-?\d+),(-?\d+)/.exec(h);
      if (m) hits.push({ tick: Math.round(Number(m[1]) * 60), kind: 'hit', x: Number(m[2]), y: Number(m[3]), source: 'seed ' + seeds[index].seed });
    }
    for (const hit of hits) {
      const window = decisions.filter((d) => d.tick <= hit.tick && d.tick > hit.tick - 60);
      const last = window.slice(-8);
      let cls = 'forecast_miss';
      if (last.some((d) => d.got > d.best)) cls = 'model_pick';
      if (last.length && last.every((d) => d.best >= 5)) cls = 'no_safe_move';
      totals.hits += 1;
      totals[cls] += 1;
      const pos = last.map((d) => d.position === 'center' ? 'C' : 's').join('');
      console.log(`${path.basename(dir).slice(-12)} t=${(hit.tick / 60).toFixed(1)}s ${hit.kind} by ${hit.source} at (${hit.x},${hit.y}) ${cls} | best/got ${last.map((d) => `${d.best}/${d.got}`).join(' ')} | pos ${pos} | near ${last.length ? last[last.length - 1].near : '-'} center ${last.length ? Math.round(last[last.length - 1].center) : '-'}`);
    }
  }
}
console.log('TOTAL', JSON.stringify(totals));
