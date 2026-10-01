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
- it stays in the station's outer part without heading toward the centre.

A perfect reader of these labels: **8/8, 0 hits**. The labels carry the strategy.

## 4. Model lane: Julia reading v2 labels

| Step | Change | Best-tier picks | 2× lockstep |
| --- | --- | --- | --- |
| C1 | "survives 3/4 futures" | 91.1% | 3/4 (seeds 1–4) |
| C2 | offline replay of 800 logged states: "hit in 1 of 4 futures" | 90.8% → **98.6%** | — |
| C3 | hit-count wording | 94.3% | 3/8: momentum drift |
| C4 | + station core (position before momentum) | see results.tsv | see results.tsv |

What was learnt about Julia:
- Its outcome judgement reads the word *hit*, not a fraction.
- Dropping "keeps course" made it worse offline (74.9%).
- Best-tier picks alone aren't enough. In C3 every pick on seed 6 was best-tier, yet "keeps course" walked the
  ship along the station band, out of it, and against the wall. The fix was in the labels, not the model: moving
  outward inside the station is no longer GOOD.

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
