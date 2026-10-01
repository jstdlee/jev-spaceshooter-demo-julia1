# v2: the doubled ceiling, and how the decision strategy was found

v2 doubles every difficulty slider ceiling: bullets 8× → 16×, enemies 6× → 12×, fast bullets stay at 100% and their
speed goes from 4.8× to 9.6×. Settings at or below the old ceilings behave exactly as before; the enemy cap and the
fire-interval floor stretch only above them. The demo auto-ramp still tops out at the old 4/4.

The strategy was found layer by layer with the method in `~/dev/clm-terriance/notes/` (decision-framework.md):
first the ceiling, then the forecast, then the labels, then the model. Every row is in [results.tsv](results.tsv).

## 1. Ceiling first

| Policy | 2× result (8 seeds × 60 s, 30 ms lockstep) |
| --- | --- |
| Perfect reader of the v13 labels (`--upstream oracle`) | 12.9 s mean, 0/8 |
| Argmax over the v13 straight-line forecast (lab) | 11.1 s mean, 0/8 |
| Real-engine rollouts **with future knowledge**, survival only | 47.3 s mean, 3/8 |

Even a perfect reader of the old labels died, so the problem was the forecast and the strategy, not the model. The
lab autopsy (`demo/v2_lab.cjs`) showed safe moves until about 0.3 s before each hit. The straight-line forecast
misses bullets that are fired after the observation, and at 9.6× they cross the arena in under 0.4 s. The ship also
drifted to the walls and the top. Even perfect foresight died when it had no position strategy.

## 2. Forecast: sampled futures

`sampledFuturesForecast` (core) plays each move on K clones of the game with the engine itself. Every clone gets
its own RNG state, so enemy fire is *sampled* and never read from the real stream. The forecast knows no more than
a player could. A move's survival is the mean fraction of 250 ms of the move plus the best 250 ms follow-up.

| Lab policy (fair) | 2× result |
| --- | --- |
| K=4 futures + low station preference | **60 s, 8/8, 1 hit** |
| K=4 futures, no position preference | 39.9 s, 0/4 |
| K=2 futures + station | 60 s, 4/4, 3 hits |

Position is the strategy. Staying low gives reaction time against shots from above, and staying off the side walls
keeps the escape directions open. That is the difference between 0/4 and 8/8.

## 3. Representation: v2 labels

When the game sends `futures`, the bridge sets the tier from sampled survival, and the "zone" becomes a low
**station** band (x 200–760, y ≥ 430).

| Tier | Rule |
| --- | --- |
| 6 DEADLY | hit in every sampled future |
| 5 DOOMED | survives less than 60% of the window on average |
| 4 TRAP | ends within 25 px of a wall |
| 1 GOOD → 3 RISKY | survival ≥ 0.999 / ≥ 0.9 / else |

A safe move drops one tier in these cases (never below RISKY):
- it ends near a wall;
- it leaves the station without heading back;
- it is one of the safest moves but ends in a clearly worse place (station score more than 0.05 below the best of them).

A perfect reader of these labels: **8/8, 0 hits**. The labels carry the strategy.

## 4. Model lane: Julia reading v2 labels

| Step | Change | Best-tier picks | 2× lockstep |
| --- | --- | --- | --- |
| C1 | "survives 3/4 futures" | 91.1% | 3/4 (seeds 1–4) |
| C2 | offline replay of 800 logged states: "hit in 1 of 4 futures" | 90.8% → **98.6%** | — |
| C3 | hit-count wording | 94.3% | 3/8: momentum drift |
| C4 | + station core rule (position before momentum, words only) | 93.9% | 3/8: drifts left and up instead |
| **C5** | **position in the tier: among the safest moves, a clearly worse end position drops a tier** | **94.8%** | **6/8, mean 53.6 s** |
| C6 | C5 with a linear pull to the centre | 87.5% | 4/8 (reverted) |

**Final: C5.** Julia survives 6 of 8 seeds at 2× (mean 53.6 s). A perfect reader of the same labels survives 8/8 with
0 hits, so the remaining gap is the model lane: on seed 7 Julia still slides along the station's flat middle by
"keeps course" until the way back is DOOMED.

What was learnt about Julia:
- Its outcome judgement reads the word *hit*, not a fraction.
- Dropping "keeps course" made it worse offline (74.9%).
- Best-tier picks alone aren't enough. In C3 every pick on seed 6 was best-tier, yet "keeps course" walked the
  ship along the station band, out of it, and against the wall.
- Julia follows the tier, not within-tier words. The position strategy works only when it is part of the tier (C5).
  Expressing it in words (C4) or pulling too hard (C6) did not work.

## Tools
- `demo/v2_lab.cjs`: in-process lockstep with pluggable policies (`forecast`, `rollout`, `mc`, `futures`) and a death autopsy.
- `node demo/lockstep_sim.cjs --futures 4 …`: the production path with sampled futures.
- `demo/v2_replay.cjs`: re-scores logged model questions with a label rewrite, in seconds, without running the game.
- `demo/v2_agreement.cjs`: per-decision best-tier, oracle agreement and deadly picks from bridge traces.

## Open issues
- **Live cost.** The K=4 forecast costs about 75–90 ms of CPU per decision (more under load). Lockstep pauses the game
  for it, but the 60 Hz browser loop can't run it on its main thread. Options are a Web Worker, K=2, or forecasting
  every other decision.
- **Constant questions.** Fire is always `shoot`, and the bomb question is still asked when nothing can detonate.
  Both are asked every decision (the budget aspect).
- `test_strategy_regression.cjs` has one failing test inherited from `julia1`.

## Engine v15 (game features, after the strategy study)

Live at `/v2/` on the julia1 bridge (`bin/bridge --mount /v2=<v2 demo dir>`); julia1 stays at `/`.

- **Sampled futures in the browser:** each decision's K futures run in K Web Workers, so the 60 Hz loop never waits.
  `?futures=2` samples fewer, and `?futures=0` turns them off.
- **Shields:** a rare pickup (first at 28 s, then every 42 s). Each shield absorbs one hit, and they stack to 4, drawn
  as shimmering rings: cyan, green, magenta, gold.
- **Assistance strike:** one per 10-wave block. A sweep rises from the bottom of the arena to the top and destroys
  everything, the boss included. It is a new **triggered decision**: the bridge asks it only when ≥ 60 bullets are on
  screen. It ranks CALL NOW only when every move is tier 5+ and the bomb cannot help (no charge or jet left, or ≥ 90
  bullets). Otherwise the strike holds without a model call.
- **Boss:** denser rings (22 every 1.7 s) and fans (11 every 1.3 s), plus big bombs. A big bomb takes 3 hits to shoot
  down and bursts into a 16-bullet ring at its fuse or at the ship's height.
- **Stages:** every wave lasts 3 minutes. A cleared formation brings reinforcements; at the end, ships still on screen
  withdraw and the boss stays.
- **Missions:** one random mission per stage (destroy N, collect 3, lose no life, destroy the boss, intercept 60, no
  bombs). Completing one pays 1500 points and a bomb charge.
- **Position:** no rectangle. Only walls and corners cost a tier among the safest moves. This differs from the measured
  C5 station strategy and has not been re-evaluated.
- **Art:** Dart, Wasp and Crab variants of the enemy classes; Mantis and Hydra bosses next to the Dreadnought.
- **Ranking board:** after a death, enter a name. Runs rank by time survived, then score, stored per demo in
  `runs/leaderboard.json` (`GET/POST api/leaderboard`, one entry per finished run).

### Engine v15 rules on fairness and armor
- **No local dodge.** The `hybrid` local safety override is gone: the model's move is always the move executed. The
  engine refuses a manifest asking for it, and `benchmark.cjs --policy-mode` accepts only `djev-only`.
- **The forecast sees only what a player can see.** Each sampled future resamples the RNG and the hidden timers: the
  formation's next shot is drawn at random, a boss that has not appeared stays absent, and pickups are switched off.
  The lab's `rollout` policy, which knows the real future, exists only as a ceiling in `demo/v2_lab.cjs` and is never
  used by the game or the bridge.
- **What remains, all visible on screen:** the 1.1 s blink after a hit, the 0.6 s blink after a shield absorbs one,
  and shields themselves, which are pickups.
- **Armor:**
  - Enemy bullets take 2 hits (gun or escort shot, or splash).
  - Enemy missiles take 3 hits; a homing missile counts as 1.
  - Bombs and the assistance strike still clear everything at once.
  - Intact bullet shells show a grey outline, and missiles show armor pips.
- **Escorts:** at most 4, evenly spaced (left, right, above, below), circling the ship and firing outward.
