#!/usr/bin/env node
// Offline replay for the model lane: re-sends logged path questions to the model with a label rewrite applied and
// reports how often the model picks a best-tier move. No game runs, so a wording change is measured in seconds on
// thousands of real states (step 6.4 of the clm-terriance exploration notes).
//
//   node demo/v2_replay.cjs --runs /tmp/v2runs/julia --rewrite drop-keeps-course [--limit 800] [--url http://127.0.0.1:8011]
'use strict';

const fs = require('node:fs');
const path = require('node:path');

const REWRITES = {
  none: (label) => label,
  'drop-keeps-course': (label) => label.replace(', keeps course', ''),
  // State the outcome as hits, which is what the "What happens to the ship?" judgement scores.
  'outcome-words': (label) => outcomeWords(label),
  'drop-zone': (label) => dropZone(label),
  'outcome-drop-zone': (label) => dropZone(outcomeWords(label)),
};

function outcomeWords(label) {
  return label.replace(/survives (\d+)\/(\d+) futures/, (_, c, k) => Number(c) === Number(k) ? `safe in all ${k} futures` : `hit in ${Number(k) - Number(c)} of ${k} futures`);
}

function dropZone(label) {
  return label.replace(/, (outside zone|leaves zone|back to zone)/, '');
}

function parseArgs(argv) {
  const o = { runs: null, rewrite: 'none', limit: 800, url: 'http://127.0.0.1:8011' };
  for (let i = 0; i < argv.length; i += 1) {
    const v = () => argv[++i];
    if (argv[i] === '--runs') o.runs = v();
    else if (argv[i] === '--rewrite') o.rewrite = v();
    else if (argv[i] === '--limit') o.limit = Number(v());
    else if (argv[i] === '--url') o.url = v();
    else throw new Error(`unknown argument ${argv[i]}`);
  }
  if (!REWRITES[o.rewrite]) throw new Error(`unknown rewrite ${o.rewrite}`);
  return o;
}

const tierOf = (label) => Number((/^Tier (\d) /.exec(label) || [0, 9])[1]);

function loggedPayloads(dir) {
  const out = [];
  for (const run of fs.readdirSync(dir).filter((d) => d.startsWith('run-')).sort()) {
    for (const line of fs.readFileSync(path.join(dir, run, 'events.jsonl'), 'utf8').split('\n')) {
      if (!line.includes('"decision_response"')) continue;
      const e = JSON.parse(line);
      if (e.exactactualpayload && e.exactactualpayload.questions && e.exactactualpayload.questions.path) out.push(e.exactactualpayload);
    }
  }
  return out;
}

async function main() {
  const o = parseArgs(process.argv.slice(2));
  const all = loggedPayloads(o.runs);
  // Spread the sample across the whole log rather than taking the first run only.
  const step = Math.max(1, Math.floor(all.length / o.limit));
  const sample = all.filter((_, i) => i % step === 0).slice(0, o.limit);
  const rewrite = REWRITES[o.rewrite];
  let n = 0, best = 0, worse2 = 0, deadly = 0;
  for (const payload of sample) {
    const p = JSON.parse(JSON.stringify(payload));
    const criteria = p.questions.path.criteria;
    for (const k of Object.keys(criteria)) criteria[k] = rewrite(criteria[k]);
    p.questions = { path: p.questions.path };     // only the lane under test
    const r = await fetch(`${o.url}/v1/systemone`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(p) });
    const body = await r.json();
    const choice = body.answers && body.answers.path && body.answers.path.choice;
    if (!choice) continue;
    const tiers = Object.values(criteria).map(tierOf);
    const t = tierOf(criteria[choice]);
    const b = Math.min(...tiers);
    n += 1;
    if (t === b) best += 1;
    if (t >= b + 2) worse2 += 1;
    if (t === 6 && b < 6) deadly += 1;
  }
  console.log(`rewrite=${o.rewrite} states=${n} best-tier ${(100 * best / n).toFixed(1)}% worse-by-2 ${worse2} deadly-when-safe ${deadly}`);
}

main().catch((e) => { console.error(e); process.exit(1); });
