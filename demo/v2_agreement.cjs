#!/usr/bin/env node
// Per-decision agreement between the model's path pick and the label oracle (the same ranking as bridge/oracle.go),
// read from bridge traces. This is the "model lane" metric of the v2 tuning ladder: survival alone hides how often
// the model got lucky.
//
//   node demo/v2_agreement.cjs <runs-dir-or-run-dir> [...]
'use strict';

const fs = require('node:fs');
const path = require('node:path');

const tierOf = (label) => { const m = /^Tier (\d) /.exec(label || ''); return m ? Number(m[1]) : 9; };

// Mirrors oracleScore in demo/bridge/oracle.go.
function oracleScore(label) {
  const tier = tierOf(label);
  const hit = /hits the ship in (\d+) ms/.exec(label);
  if (hit) return [tier, Number(hit[1])];
  let score = 0;
  const esc = /(\d)\/9 escapes/.exec(label); if (esc) score += Number(esc[1]) * 1000;
  const fut = /survives (\d+)\/(\d+) futures/.exec(label); if (fut) score += Number(fut[1]) * 1000;
  const safe = /safe in all (\d+) futures/.exec(label); if (safe) score += Number(safe[1]) * 1000;
  const hitk = /hit in (\d+) of (\d+) futures/.exec(label); if (hitk) score += (Number(hitk[2]) - Number(hitk[1])) * 1000;
  if (label.includes('back to zone')) score += 2;
  if (label.includes('keeps course')) score += 100;
  if (!label.includes('near enemy')) score += 10;
  if (label.includes('open space')) score += 2;
  if (label.includes('collects ')) score += 50;
  else if (/toward (bomb|weapon|missile)/.test(label)) score += 5;
  if (label.includes('toward center')) score += 1;
  return [tier, score];
}

function oracleChoice(criteria) {
  let best = null;
  for (const [key, label] of Object.entries(criteria)) {
    const [tier, score] = oracleScore(label);
    if (!best || tier < best.tier || (tier === best.tier && score > best.score)) best = { key, tier, score };
  }
  return best.key;
}

function runDirs(target) {
  if (fs.existsSync(path.join(target, 'events.jsonl'))) return [target];
  return fs.readdirSync(target).filter((d) => d.startsWith('run-')).map((d) => path.join(target, d)).sort();
}

function analyze(dir) {
  const s = { decisions: 0, best_tier: 0, oracle_agree: 0, worse_by_2: 0, deadly_when_safe: 0, label_ties: 0, tier_hist: {} };
  for (const line of fs.readFileSync(path.join(dir, 'events.jsonl'), 'utf8').split('\n')) {
    if (!line.includes('"decision_response"')) continue;
    const e = JSON.parse(line);
    const n = e.normalized;
    const criteria = n && n.labels && n.labels.path;
    if (!criteria || !n.valid_choice) continue;
    const chosen = Object.keys(criteria).find((k) => k.startsWith(`${n.movement}__`));
    if (!chosen) continue;
    s.decisions += 1;
    const tiers = Object.values(criteria).map(tierOf);
    const best = Math.min(...tiers);
    const got = tierOf(criteria[chosen]);
    s.tier_hist[got] = (s.tier_hist[got] || 0) + 1;
    if (got === best) s.best_tier += 1;
    if (got >= best + 2) s.worse_by_2 += 1;
    if (got === 6 && best < 6) s.deadly_when_safe += 1;
    if (chosen === oracleChoice(criteria)) s.oracle_agree += 1;
    const top = Object.values(criteria).filter((l) => l === criteria[chosen]).length;
    if (top > 1) s.label_ties += 1;
  }
  return s;
}

const totals = { decisions: 0, best_tier: 0, oracle_agree: 0, worse_by_2: 0, deadly_when_safe: 0, label_ties: 0 };
for (const target of process.argv.slice(2)) {
  for (const dir of runDirs(target)) {
    const s = analyze(dir);
    for (const k of Object.keys(totals)) totals[k] += s[k];
    const pct = (x) => s.decisions ? (100 * x / s.decisions).toFixed(1) + '%' : '-';
    console.log(`${path.basename(dir)}  decisions ${s.decisions}  best-tier ${pct(s.best_tier)}  oracle ${pct(s.oracle_agree)}  worse-by-2 ${s.worse_by_2}  deadly-when-safe ${s.deadly_when_safe}  tiers ${JSON.stringify(s.tier_hist)}`);
  }
}
const pct = (x) => totals.decisions ? (100 * x / totals.decisions).toFixed(1) + '%' : '-';
console.log(`TOTAL decisions ${totals.decisions}  best-tier ${pct(totals.best_tier)}  oracle-agree ${pct(totals.oracle_agree)}  worse-by-2 ${totals.worse_by_2}  deadly-when-safe ${totals.deadly_when_safe}  chosen-label-shared ${pct(totals.label_ties)}`);
