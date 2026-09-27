# Jev Space Shooter — local djev decision demo

A browser space shooter for exploring a practical question: **how do you turn a changing, continuous environment into a small decision problem that a local model can solve quickly enough?**

The left panel is the game; the right panel shows observations, the model's selected action, the action actually executing, latency, request rate, and token throughput. Difficulty controls let you increase enemy count, bullet density, and the proportion and speed of fast bullets.

This project uses a **self-hosted [djev-spark](https://github.com/mmastrac/djev-spark) model endpoint**, not the official hosted Jev service. In the current controller, djev chooses movement and firing. The client computes physical observations and executes the returned command; it does not secretly replace a bad tactical choice with a better one.

This is a decision-modeling experiment, not a solved bullet-hell agent. It still makes mistakes, sometimes drifts toward boundaries, and remains sensitive to inference latency. The earlier 120-second survival target was withdrawn; no stable hardest-profile success is claimed.

## Demo video

**Historical prototype footage — not the current API-authoritative controller.** The supplied recording shows the earlier hybrid controller, including failed API requests and locally executed fallback actions. It illustrates the arena and dashboard, but must not be used as evidence that djev produced that play.

[![Eight-second excerpt of the historical prototype, including its local-action and API-status panel](docs/media/historical-prototype-preview.gif)](https://raw.githubusercontent.com/jstdlee/jev-spaceshooter-demo/main/docs/media/space-shooter-demo.webm)

**[Download the full recording — WebM, 14.5 MiB](https://raw.githubusercontent.com/jstdlee/jev-spaceshooter-demo/main/docs/media/space-shooter-demo.webm)** · [Still frame](docs/media/historical-prototype-poster.png)

The inline animation is an 8-second, real-time excerpt; the original video is approximately 84.6 seconds. GitHub's file viewer does not preview the original at this size, so the full-recording link goes directly to the WebM for download and playback. Both assets are stored in this repository. See [media provenance](docs/media/README.md) for the source, conversion details, and limitations. Run the demo below for the current implementation; its measured results are listed separately under [Results](#results-and-their-limits).

## Run locally

### Dependencies

- A modern browser for the HTML/Canvas game. No Gradio, React, npm install, or frontend build is needed.
- Go **1.22+** for the HTTP bridge, using only the standard library.
- A running **djev-spark structured API** at `POST /v1/systemone`.
- Node.js **22** for the tested offline suites and optional CLI benchmarks; not needed just to open the game through the bridge.

The development endpoint used DiffusionGemma 26B-A4B NVFP4 through djev-spark and its patched vLLM runtime. Model weights, GPU resources, containers, and their dependencies are **not bundled here**. Follow the upstream [djev-spark setup](https://github.com/mmastrac/djev-spark) for the model-serving environment and its licenses. A generic `/v1/chat/completions` endpoint is not a drop-in replacement.

### Start the bridge

```bash
git clone https://github.com/jstdlee/jev-spaceshooter-demo.git
cd jev-spaceshooter-demo
cp .env.example .env
```

Edit `.env` to match your structured model server:

```dotenv
DJEV_URL=http://127.0.0.1:8011
DJEV_API_KEY=
DJEV_MODEL=jev-latest
```

`jev-latest` is the bridge's default compatibility identifier, **not a claim that an official Jev model is running**. Configure the identifier accepted by your own server. Set a bearer key only if the endpoint requires one. Process environment variables override `.env`; `.env` is ignored by Git.

```bash
go run ./demo/bridge --host 127.0.0.1 --port 7865
```

Open **[http://127.0.0.1:7865/](http://127.0.0.1:7865/)**. Watch both *valid djev commands* and *applied djev commands* increase. Opening the HTML with `file://` does not provide the API bridge. Run the command from the repository root (or pass `--demo-dir`). A green `/health` response checks the bridge only, not successful model inference.

Keep the bridge on loopback for this local demo. The browser never needs the upstream API key. GitHub hosts the source and media, not a running model or bridge backend.

## Who decides what?

```text
HTML game: positions, velocities, all nine physical path forecasts
    │ POST /api/decision
    ▼
Go bridge: validate and compact the observations
    │ POST /v1/systemone — one request, three choices
    ▼
Self-hosted djev-spark → local model
    │ intent + movement path + shoot/cease
    ▼
Bridge normalization → client validity/freshness checks → game physics
    └────────────────────── next observation ──────────────────────┘
```

| Component | Responsibility | Does not do |
| --- | --- | --- |
| HTML client | Render and simulate the game; observe positions/velocities; forecast each candidate path; execute accepted commands | Rank or filter tactical actions, veto a valid but dangerous direction, or supply fallback steering |
| Go bridge | Validate schemas, compact factual inputs, call djev, normalize the three answers, keep credentials server-side | Choose a winner, synthesize a movement answer, or replace an unsafe answer |
| Local djev model | Choose intent, one of nine paths, and shooting state | Run the game physics or inspect future random spawns |
| Protocol controller | Check run/epoch/sequence, response age, and command expiry | Decide which direction is tactically safer |

**Local trajectory calculation is still substantial preprocessing.** The model is not discovering collision geometry from pixels: it is selecting an action from engineered physical forecasts. What this demonstrates is structured model decision-making over those observations—not end-to-end visual intelligence or model-only trajectory prediction.

All nine paths are offered, even when some forecast a collision. There is no safety mask, local ranking, bomb, local rescue controller, or independent autofire in model mode. A valid but tactically wrong model choice is executed. Invalid, missing, expired, or stale authorization instead produces neutral **hold + cease**; that is a protocol rule, not an evasive maneuver.

### One atomic decision

Each upstream request uses `samples: 1` and `steps: 1`, with three choice questions in the same call:

| Choice | Values | Meaning |
| --- | --- | --- |
| `intent` | `evade`, `recover`, `position` | The model's description of its current intent |
| `path` | `hold__medium`, four cardinal and four diagonal directions | Which movement to execute |
| `fire` | `shoot`, `cease` | Whether the normal weapon cooldown may emit shots |

For example, the answer fields may be:

```json
{
  "answers": {
    "intent": {"choice": "recover"},
    "path": {"choice": "up_right__medium"},
    "fire": {"choice": "shoot"}
  }
}
```

The bridge maps `up_right__medium` to `movement: "up_right"` and `lease: "medium"`. All three choices must validate together. Intent is descriptive: a `recover` label does not activate a hidden client-side return-to-center routine. It can disagree with the quality of the selected movement.

Physics runs at 60 ticks/s. Each medium command authorizes at most **30 ticks / 500 ms**, and a newer valid answer can preempt it on the next tick. One request is pending per game; the next request starts after the previous one completes. Thus 500 ms is an authorization ceiling, not a mandatory wait between decisions. The current response-age limit is 600 ms. The request is deliberately lean: `state` is only `{"enemy_count": n}` and the intent question is gone, because every choice carries its own facts in its labels. Djev's latency grows sharply with numeric state: the former eight-field state plus intent question cost about 228 ms per call versus about 176 ms now in live play (108 ms idle), with fewer below-best picks. Earlier, Djev answered a distinct game state in about 185 ms (repeated identical payloads return in ~120 ms from its cache, which is why naive micro-benchmarks look faster); the bridge adds about 1 ms and the client about 25 ms, mostly waiting for the next 60 Hz tick. Keeping one request in flight is deliberate: in a deterministic lockstep simulation, overlapping requests (sending again before the previous answer lands) cut survival from ~90 s to ~25 s because each forecast assumes a command that is about to be replaced, and voting across three parallel `samples: 1` calls did not improve choices. Survival tracks latency instead: live hardest-profile runs at ~180 ms reached 88–121 s, while runs at ~300 ms died at 21–50 s.

## Current decision model

### Observations, not instructions disguised as scores

The compact state contains the player, whether it is inside the center region, whether holding forecasts a collision, the hold-path gap, estimated API delay, waiting-prefix collision time, and enemy count. It does not send the complete game world or duplicate a large path table.

Instead, each of the nine choice descriptions is a short label that leads with a **tier number** and then lists physical facts in words:

```text
Tier 1 GOOD: 7/9 escapes, open gap, open space, continues, toward center.
Tier 2 OK: 6/9 escapes, tight gap, near enemy, busy space, turns.
Tier 6 DEADLY: a threat hits the ship in 125 ms.
```

| Tier | Meaning (computed from the forecast, never from a preferred answer) |
| --- | --- |
| 6 DEADLY | The move's own lease (after the expected API wait) contacts a current threat |
| 5 DOOMED | The move is clear, but none of the nine follow-up moves over the next 500 ms is clear |
| 4 TRAP | Ends within 25 px of a wall |
| 3 RISKY | Fewer than 2 clear follow-ups, or the best continuation grazes a bullet (<15 px) |
| 2 OK | At least 2 clear follow-ups and at least a tight (≥15 px) gap |
| 1 GOOD | At least 4 clear follow-ups, an open (≥40 px) gap, and ≥110 px from every enemy ship |

A move that holds still, reverses the executing command, or ends within 70 px of a wall drops one tier (GOOD→OK, OK→RISKY). Djev follows the leading tier number about 97–99% of the time but largely ignored the same preferences when they were only words within a tier: about 20% of its picks held still or reversed, and it slid along walls.

The remaining words are facts for choosing within a tier: the number of clear follow-up moves (`escapes`), the best continuation's bullet gap, whether the path passes within 110 px of an enemy ship (enemies fire point-blank as they drift down), how many threats will be within 110 px of the endpoint (`open space` / `busy space` / `crowded`), how the move relates to the command already executing (`stationary`, `continues`, `turns`, `reverses`; wave-1 enemies aim at the ship's current position), and `toward center` when the endpoint is at least 15 px closer to center.

Why this encoding: measured on recorded states, Djev picked a path ending against a wall in 33% of decisions when given nine rows of raw numbers, even though it almost never picked `collision=true`; 9 of 11 hits were at a wall. Prose tiers cut below-best picks to about 8%; the leading tier number cut them to about 1%.

The forecast accounts for the active command during expected API wait, then the proposed movement for the 500 ms lease, then each follow-up move for another 500 ms. Existing bullets use observed velocity; enemies use a current-linear approximation. Future shots/spawns and hidden random state are excluded. A missing clearance measurement is not a promise of safety, especially when invulnerability covers the window.

The current fire question asks the model to shoot when enemies exist; it is not a sophisticated target-pursuit planner.

### Missiles, bombs, and pickups

- **Pickups** float and bounce through the lower play area (y 260–560, away from the enemy formation) and fade out over the last 2 s of a 14 s life. Bomb pickups (orange orbs, first at 6 s, then every 11 s) add a bomb; weapon blocks (color-cycling squares, first at 9 s, then every 17 s) raise the weapon level up to 3: the gun fires 1, 3, 5, 5 spread shots and the missile cap is 4, 5, 6, 6. Spawn positions come from the seeded game RNG, so replays match.
- **Collecting is a Djev decision.** Each path label says `collects bomb`/`collects weapon` when the move's path touches a wanted pickup, or `toward …` when it closes in by 20 px. A collecting move rises one tier; while a wanted pickup is reachable, safe moves that ignore it drop one. With only the rise, Djev gathered 24 of 96 pickups and survived 3/6 lockstep runs; with both, 41 of 101 and 5/6.
- **Escort jets** (green "F" triangles; first at 14 s, then every 16 s) add up to 8 little jets that ring the ship, each facing its own direction (left, right, the four diagonals, up, down) and firing outward with every volley. Their shots destroy enemies and also enemy bullets and missiles; the first two jets add a missile to every salvo. A hit on the ship costs one jet. Jet and weapon pickups are the proactive goal: "toward" points at them before any bomb pickup, and collecting one lifts a safe move two tiers.
- **Random boss**: 40–60 s into a run, then 35–55 s after each boss dies, a boss (45 HP) parks near the top and fires rotating 14-bullet rings every 2.4 s, 7-bullet aimed fans every 1.8 s, and pairs of slow homing missiles every 4.5 s. Missiles and any player shot can destroy its homing missiles; a bomb deals it 20 damage instead of destroying it outright. The forecast treats its homing missiles as straight-moving threats, like bullets.
- **Missile salvos**: weapon level fires 1, 2, 3, 3 fanned missiles per launch, each at a different target.
- **Wave changes clear nothing.** Bullets and missiles already in flight finish their paths.

- **Homing missiles** launch automatically every 0.5 s (4 in flight at weapon level 0, up to 6) while the active Djev command authorizes `shoot`. Each steers toward its target's predicted intercept point at up to 6 rad/s and 420 px/s, deals 1 damage, and expires after 2.5 s; if its target dies it re-targets the enemy with the shortest intercept time. Guidance is weapon physics, like a bullet's flight; Djev still decides whether to fire at all.
- **Bombs**: start with 3 charges, up to 6 by collecting bomb pickups. A detonation destroys every enemy bullet and enemy ship within 200 px of the ship. Djev decides with a fourth choice question (`hold` / `detonate`). Like paths, each choice leads with a rank: `Rank 1 USE NOW` for detonate only when every move is tier 5 or 6 and the blast would destroy something, otherwise `Rank 1 SAVE` for hold; both state what a blast would destroy and how many charges remain. With the reasons only in prose, Djev detonated in 1 of 14 such emergencies. A decision detonates at most once, even though its command lasts up to 500 ms.

Results (hardest = 4× bullets, 3× enemies, 85% fast bullets at 2.4×):

| Controller | Before missiles/bombs | After |
| --- | --- | --- |
| Perfect label reader, lockstep sim, hardest | 93 s mean, 1/8 full | 16/16 full 120 s runs |
| Djev, lockstep sim, browser 3.5×/2.75× settings | died at ~40 s live | 5/6 full, shortest 97.9 s |
| Djev, real-time CLI benchmark, hardest (seeds 20260920, 7, 11) | 29 s on seed 20260920 | 106 s, 121 s, 121 s |

### Tactical priorities sent to djev

1. Always pick from the lowest tier number present.
2. Within that tier: more escapes, then not stationary or reversing, then not near an enemy ship, then open space, then toward center. Center is a way to gain space, not a place to hold.
3. If every move is DEADLY, pick the latest predicted contact. This only buys time.

These are **model instructions**, not conditional steering code in the game. The model can fail to follow them. The shared framing is in [strategy.md](demo/strategy.md); factual option construction and question-specific instructions are in [decision.go](demo/bridge/decision.go), especially `pathTable`, `pathLabel`, `packModelContext`, and `buildUpstreamPayload`.

## Difficulty: why “just dodge” is not enough

| Setting | Browser default | `dense-mid-speed` | `hardest` |
| --- | ---: | ---: | ---: |
| Bullet density | 1× | 4× | 4× |
| Enemy density | 1× | 3× | 3× |
| Fast-bullet proportion | 0% | 85% | 85% |
| Fast-bullet speed multiplier | 1.6×, inactive at 0% | 1.7× | 2.4× |

The firing baseline is **0.95 seconds**, twice the firing frequency of the earlier 1.9-second baseline. At 4× density, the interval is 0.2375 seconds; projectile patterns can emit more than one bullet. Enemy density changes enemy count, while fast-bullet proportion controls which new bullets receive the speed multiplier. Sliders change the environment, not model intelligence.

Several objectives compete:

- **Immediate safety vs. future room.** A large gap at the edge can be a trap one decision later. Always maximizing clearance can push the ship into a corner.
- **Recovery vs. crossing danger.** Always moving toward center can cross a stream that a temporarily outward step would avoid.
- **Prediction vs. delay.** A correct snapshot decision may arrive too late; newly fired close-range bullets were not present in the snapshot.
- **Movement size vs. frequency.** Long sweeps cross unobserved danger. Short authorizations help only when fresh decisions arrive often enough.
- **Shooting vs. positioning.** Survival, firing opportunity, and wave completion are different objectives; surviving with enemies still alive is not clearing the game.

The current player remains 20 × 18 pixels, starts with three lives, moves at 112 px/s under model control, and uses a 0.17-second weapon cooldown. The latest prompt experiments did not improve scores by changing these physics, difficulty, hitboxes, or invulnerability.

## How the strategy was discovered

The useful loop was **form a hypothesis → change one representation or instruction → test fixed situations → try new situations → run real games → keep or reject the change**.

We compared larger contexts, compact path tables, inline factual choice descriptions, different recovery instructions, and single-call versus staged questions. Putting facts beside each option reduced the model's need to join a state table to a separate action list. Removing duplicated information retained useful observations without increasing round-trip time. Separate/staged questions cost roughly 460–480 ms in development probes and did not justify that delay for this controller.

In one 22-case development comparison, the path-table formulation selected a colliding route despite clear alternatives eight times; the adopted inline formulation did so zero times. **Those cases were used for tuning, not held-out proof.** Extra fields and stronger center-return wording sometimes improved selected fixtures while producing worse continuous play. A later “enough clearance, then return inward” candidate was not retained because live runs did not establish a benefit, with concurrent load also confounding the comparison.

For a useful experiment:

1. Keep physics, difficulty, seed, model configuration, and time limit explicit.
2. Include corners, clear center, incoming bullets, blocked centerward routes, and unavoidable-collision situations—not just successful examples.
3. Test a new seed or situation after tuning; once used to tune, it is no longer a holdout.
4. Measure actual survival, remaining lives, score/wave, invalid answers, latency, and applied-command rate. A correct intent label alone is insufficient.
5. Run one game against the model when comparing prompts. Browser preview plus CLI testing shares inference capacity.
6. Preserve a better-performing version when a more elaborate decision tree fails to generalize.

## Results and their limits

Development observations from **2026-09-20**, using the adopted inline-choice formulation and `dense-mid-speed`, with a 45-second test limit:

| Seed / attempt | Outcome | Lives left | API calls | Median API latency |
| --- | --- | ---: | ---: | ---: |
| 20260921 | Reached 45-second stop | 3 | 205 | 201 ms |
| 20260920 | Died at 42.9 s | 0 | 184 | 203 ms |
| 20260922, first | Died at 20.5 s | 0 | 52 | 377 ms |
| 20260922, repeat | Reached 45-second stop | 1 | 112 | 378 ms |

All four returned valid protocol choices, but two runs still died. The latter two overlapped browser inference; the second run also overlapped near its end. They are not a controlled, statistically significant comparison. Reaching the test stop means survival was observed **up to that stop**, not that the ship subsequently survived indefinitely. Results are development-log summaries; raw local run directories are not shipped in this repository.

An earlier formulation reached 64.15 seconds in a different development run. That result is not attributed to this prompt. There is **no demonstrated stable 120-second survival or hardest-profile pass**. The older target is no longer a completion gate; legacy qualification fields in the runner may still refer to it.

Typically, an otherwise idle endpoint completed a decision in about 200–230 ms: roughly 4–5 decisions/s, not 60 decisions/s. Concurrent games increased observed latency to about 380–400 ms. Game physics continuing at 60 Hz does not make a 200 ms model decision a 60 Hz reflex.

## Lockstep simulation

`demo/lockstep_sim.cjs` replays the production path (core, controller, and bridge labels) in game time: it pauses while a decision is in flight and applies each answer `--latency-ms` after its observation. Pair it with a bridge started with `--upstream oracle` to measure whether the labels carry enough information (no model call), or with a normal bridge to measure Djev itself:

```bash
go run ./demo/bridge --port 7870 --upstream oracle --runs-dir /tmp/sim-runs
node demo/lockstep_sim.cjs --url http://127.0.0.1:7870 --seeds 1-8 --profile hardest
```

## Tests and optional benchmarks

Offline tests require no running model:

```bash
node demo/test_space_shooter_logic.cjs
node demo/test_benchmark.cjs
node demo/test_strategy_regression.cjs
go test ./demo/bridge/
```

The publication check covers 49 core/controller/UI cases, 47 benchmark cases, 10 scenario-harness cases, and 25 Go bridge cases: **131 tests**. The scenario harness runs the bridge's event validator through `go run`, so Go must be on `PATH` (or set `GO`). These validate software behavior, not tactical quality.

With the bridge and model running, pause other model-controlled games before a bounded live test:

```bash
node demo/benchmark.cjs --seed 20260921 --profile dense-mid-speed --target-seconds 45 --url http://127.0.0.1:7865
```

Use `--profile hardest` only when deliberately testing the 2.4× fast-bullet setting, and label it accordingly. The physical simulation is seeded; full live games are not necessarily repeatable because response timing and model output can differ.

```bash
# Fixed diagnostic situations; not a survival benchmark.
node demo/strategy_regression.cjs --url http://127.0.0.1:7865

# Optional replay of your own local run.
node demo/benchmark.cjs --replay demo/runs/<run_id>/events.jsonl
```

The scenario harness retains strict recovery/position expectations from the design process. A live model can fail them even when offline harness tests pass. Do not weaken an expectation merely to advertise a better pass rate. Pausing, manual input, restarting, changing difficulty, or excessive scheduling lag prevents treating that browser run as a controlled benchmark.

### Reading the dashboard

- **Selected vs. executing:** receiving a response is not the same as applying a fresh command.
- **API token throughput:** reported input plus output tokens divided by request duration; **not generation/decode TPS**.
- **Requests/s:** completed calls over the recent window, not the animation frame rate.
- **Intent probability `p`:** confidence in the returned label, not calibrated survival probability.
- **Run integrity:** recording completeness, not a survival or strategy success badge.

Existing local JSONL logging and replay remain available; they are optional development tools, not the focus of this demo. `.env`, credentials, model weights, raw API-state dumps, and generated run directories must not be committed. Review diagnostic files before sharing because they may contain endpoint identifiers or local paths.

## What we learned

**Designing the decision problem is much of the work.** Choosing useful observations, time horizons, action granularity, competing objectives, and acceptance tests took repeated modeling and regression experiments. It resembles a training/evaluation loop in effort and discipline, but **no model weights were trained**: the work tuned prompts, context, and action representation around a fixed local model.

**A good-looking demo can prove the wrong thing.** The earlier hybrid controller used fast local collision screening, ranking, and overrides; the model only helped choose within a locally constrained problem. That architecture can work well as a fast safety controller plus a slower policy, but its survival cannot be credited to the model alone. Establishing the model's incremental benefit would require an otherwise matched local-only ablation. The supplied video makes this distinction especially important: it shows local movement even while API calls fail.

**Validity is not tactical competence.** A perfectly formatted answer, a plausible `recover` label, or high confidence can still steer into danger. The current implementation deliberately exposes those mistakes rather than silently rescuing them.

**More context and more rules are not automatically better.** Extra facts increase reading/comparison work; extra API stages cost reaction time. “Maximize clearance,” “always return to center,” and “move more frequently” each fail in some situations. Priorities must be tested together under the actual latency budget.

**Regression testing is part of strategy discovery.** Preserve difficult situations, compare multiple seeds, separate tuned cases from holdouts, keep difficulty fixed, and report unsuccessful runs alongside successes. A static improvement may not survive a moving environment. Prefer measured, bounded claims over a polished animation or one lucky long run.
