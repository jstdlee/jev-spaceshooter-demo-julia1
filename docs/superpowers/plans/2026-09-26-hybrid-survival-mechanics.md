# Hybrid Survival Mechanics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add deterministic local movement safety, auto-aim missiles, and a limited emergency bomb while keeping Djev's proposed command and hybrid performance clearly attributable.

**Architecture:** Keep the simulation in the existing inline `SpaceDecisionCore`. Add one canonical tick API that accepts the raw Djev proposal, runs local coordination for hybrid runs, advances physics and local weapons, and returns both the effective command and ordered events. Browser play, CLI simulation, and current trace replay use this API; archived engine snapshots continue to replay older traces.

**Tech Stack:** Inline JavaScript in `demo/space-shooter.html`; Node.js `node:test` suites and VM-loaded core in `demo/test_space_shooter_logic.cjs` and `demo/test_benchmark.cjs`; Python standard-library trace bridge in `demo/space_shooter_server.py`.

**Spec:** `docs/superpowers/specs/2026-09-26-hybrid-survival-mechanics-design.md`

## Global Constraints

- The game simulation remains deterministic at 60 ticks/s.
- Djev remains responsible for strategic movement and normal-gun authorization; do not add model calls for bullet or bomb choices.
- Check all nine legal routes over a 250 ms emergency horizon; use the spec's invulnerability, clearance, wall-room, stable-action, and latest-contact ordering.
- Launch missiles at 420 px/s, turning up to 6 rad/s, with one damage, a two-second expiry, a 1.5-second launch interval, and at most three active missiles.
- Start with one bomb charge per run, never replenish it, and use it only when every route predicts projectile contact within 250 ms.
- Record the policy mode, mechanics parameters, proposal, effective action, and local intervention events so replay and reporting preserve provenance.
- Keep `djev-only` and `hybrid` benchmark results as separate policy modes.

## Review Focus

- No fresh Djev command: retain hold and cease fire if safe, or choose only an emergency route if hold is unsafe; test both cases and ensure an invalid route assessment preserves authorized movement and records failure.
- Current invulnerability covers an otherwise intersecting path: do not treat that contact as damaging; test expiry within the horizon separately.
- Every route has a projected contact: choose latest contact with stable tie breaking and record that no safe escape existed; test deterministic repeated calls.
- Missile target disappears or there is no live enemy: reacquire by the documented target rule or do not launch; test both before and after the three-missile cap.
- Bomb trigger is ambiguous because a route has only an enemy-body contact: conserve the charge; test that a body-only threat never triggers the bomb.

---

### Task 1: Local movement safety coordinator

**Files:**
- Modify: `demo/space-shooter.html` — core coordinator and canonical tick API.
- Modify: `demo/benchmark.cjs` — require and use the canonical tick API in live CLI simulation.
- Test: `demo/test_space_shooter_logic.cjs`.
- Test: `demo/test_benchmark.cjs`.

**Interfaces:**
- Produces `SpaceDecisionCore.coordinateAction(game, proposal) -> { effective_command, use_bomb, events }`. `proposal` is a valid Djev command or `null`; the initial implementation always returns `use_bomb: false`.
- Produces `SpaceDecisionCore.stepPolicyTick(game, proposal) -> { effective_command, events }`; it applies the coordinator result, then advances the existing physics once. `game.policy_mode === "djev-only"` bypasses local movement arbitration.
- `createGame(manifest)` defaults `policy_mode` to `"hybrid"` until Task 4 makes the selected run mode explicit in every manifest.
- `effective_command` preserves Djev decision metadata when Djev's route is retained. A locally selected route carries `source: "local_safety"`, the superseded `decision_id` when present, and `fire: "cease"` when there is no valid Djev command.
- Internal counters follow the existing camelCase convention: `safetyOverrides` in Task 1; `missileLaunches`, `missileHits`, and `missileKills` in Task 2; `bombsUsed` in Task 3. Trace reports expose corresponding readable metrics.
- Consumers: browser `processTick` and CLI tick loop pass the unmodified command from `SpaceDjevController.commandForTick` to `stepPolicyTick`; trace replay is switched to this API in Task 4.

- [ ] **Step 1: Write failing coordinator tests** for `coordinateAction`: safe Djev path is retained; unsafe path is overridden by a safe route; clearance, wall-room, and action-ID ties resolve in order; when all routes are unsafe choose latest contact; no-command safe/unsafe behavior preserves cease-fire provenance; and an invalid route assessment preserves authorized movement and emits `safety_calculation_failed`.
- [ ] **Step 2: Run `node demo/test_space_shooter_logic.cjs` and confirm the new coordinator cases fail** because `coordinateAction` is not implemented.
- [ ] **Step 3: Implement `coordinateAction(game, proposal)` in the core.** Project each of the nine action paths against active hostile bullets and enemies for `250 ms`; ignore only contacts covered by current invulnerability. Preserve a safe proposal. For an unsafe proposal select the safe route with greatest minimum clearance, then greatest wall room, then stable action-ID order; if none is safe, choose the route with latest contact. With no proposal, use hold unless hold is unsafe, then use the same local route selection and force normal fire to cease. If route assessment fails, preserve an authorized proposal, do not trigger local actions, and emit `safety_calculation_failed`.
- [ ] **Step 4: Implement `stepPolicyTick(game, proposal)` and route browser and CLI ticks through it.** Return the unmodified proposal separately from the effective command; append `safety_override` only when movement changes, including candidate risks and whether a safe escape was available. Keep `stepGame` available for low-level legacy fixtures.
- [ ] **Step 5: Run `node demo/test_space_shooter_logic.cjs` and `node demo/test_benchmark.cjs`**; expect the coordinator cases and existing djev-only behavior to pass.
- [ ] **Step 6: Commit** as `feat: add local movement safety coordinator`.

### Task 2: Automatic homing missiles

**Files:**
- Modify: `demo/space-shooter.html` — missile state, targeting, update, collision, counters, rules manifest, and `stepPolicyTick`.
- Test: `demo/test_space_shooter_logic.cjs`.
- Test: `demo/test_benchmark.cjs`.

**Interfaces:**
- Extends `stepPolicyTick(game, proposal)` to advance missiles for `hybrid` runs only; `djev-only` runs retain current weapons behavior.
- Adds `playerMissiles` and missile launch scheduling to serialized game state. `RULES` pins speed `420`, turn rate `6`, damage `1`, lifetime `2`, launch interval `1.5`, and active cap `3`.
- Emits `missile_launch`, `missile_retarget`, `missile_hit`, `missile_expired`, `missile_damage`, and `missile_enemy_destroyed` events with missile, target, damage, and score provenance.

- [ ] **Step 1: Write failing tests** for: no launch without a living enemy; launch timing and active cap; shortest estimated intercept time with enemy-ID tie break; steering limited to 6 rad/s; reacquisition after target destruction; one damage and existing destruction score; and two-second expiry.
- [ ] **Step 2: Run `node demo/test_space_shooter_logic.cjs` and confirm the missile cases fail** because missile state and behavior are absent.
- [ ] **Step 3: Implement deterministic missile targeting and updates.** Estimate each enemy's current velocity with `enemyVelocity`; solve the constant-velocity intercept time for missile speed `420` and aim at the predicted point (fall back to the current target position when no positive intercept solution exists). Launch no more than one missile per `1.5` simulation seconds when fewer than three are active. Steer by at most `6 * DT_SECONDS` radians each tick; reacquire a destroyed target using the same ordering. A hit deals one damage, emits attribution events, and reuses existing enemy scoring.
- [ ] **Step 4: Add missile state and rule values to `createGame`, `serializeGame`, `restoreGame`, `hashGame`, and `gameStatus` counters.** Ensure waves do not silently refill the active cap or reset missile cadence.
- [ ] **Step 5: Run `node demo/test_space_shooter_logic.cjs` and `node demo/test_benchmark.cjs`**; expect missile rule, fixed-seed state, and current djev-only tests to pass.
- [ ] **Step 6: Commit** as `feat: add deterministic auto-aim missiles`.

### Task 3: One-charge emergency bomb

**Files:**
- Modify: `demo/space-shooter.html` — bomb charge and pre-movement detonation in the coordinator/tick API.
- Test: `demo/test_space_shooter_logic.cjs`.
- Test: `demo/test_benchmark.cjs`.

**Interfaces:**
- Extends `coordinateAction(game, proposal)` to set `use_bomb: true` only when all nine routes forecast a damaging hostile-projectile contact within `250 ms` and at least one projectile is involved.
- Extends `stepPolicyTick` to remove current hostile bullets before movement and collision resolution, then recompute route risk before choosing the effective command.
- Adds `bomb_charges` to serialized game state and `bombs_used` to counters. Emits `bomb_used` with charge before/after, trigger evidence, removed projectile IDs, and residual risk after clearing.

- [ ] **Step 1: Write failing tests** proving: one charge at run start; no use when any route is safe; no use for body-only risk; use when every route has projectile contact; all current bullets are removed before movement; route safety is recalculated after detonation; and the charge is never restored on a wave transition.
- [ ] **Step 2: Run `node demo/test_space_shooter_logic.cjs` and confirm bomb cases fail** because the coordinator currently always returns `use_bomb: false`.
- [ ] **Step 3: Implement bomb trigger assessment and application.** Choose the bomb from pre-clear route forecasts only under the trigger predicate above, clear active hostile bullets before physics, then rerun the route coordinator without those bullets. Preserve and report any remaining enemy-body risk; do not grant damage or invulnerability.
- [ ] **Step 4: Add bomb charges and use counters to initialization, serialization, restoration, hashing, and status.** Record exactly one `bomb_used` event with the trigger evidence and removed bullet IDs.
- [ ] **Step 5: Run `node demo/test_space_shooter_logic.cjs` and `node demo/test_benchmark.cjs`**; expect bomb timing, conservation, and deterministic replay-state tests to pass.
- [ ] **Step 6: Commit** as `feat: add deterministic emergency bomb`.

### Task 4: Hybrid trace, policy-specific reporting, and old replay support

**Files:**
- Modify: `demo/space-shooter.html` — manifest fields and browser trace event wiring.
- Modify: `demo/benchmark.cjs` — policy-mode option, event provenance, counters, archived-engine selection, and replay tick API.
- Modify: `demo/space_shooter_server.py` — validate policy mode and archive the exact inline engine HTML by source hash on run start.
- Test: `demo/test_benchmark.cjs`.
- Test: `demo/test_space_shooter_logic.cjs`.
- Test: `demo/test_space_shooter.py`.

**Interfaces:**
- Run manifests contain `policy_mode: "djev-only" | "hybrid"` and all mechanic values from `SpaceDecisionCore.RULES`; browser starts `hybrid`, while CLI accepts `--policy-mode` and reports the chosen mode.
- Trace replay resolves `engines/<engine_hash>.html` alongside the trace's runs directory when no `--html` override is supplied; it verifies that snapshot's source hash before loading modules. Explicit `--html` remains supported.
- `summarizeTraceRecords`, `buildTraceReport`, and live benchmark summaries expose policy mode, safety overrides, bomb use, missile hits/kills, and lives. Hybrid counters never contribute to a `djev-only` report.

- [ ] **Step 1: Write failing tests** for required/valid policy mode in manifests; one hybrid run manifest and one djev-only manifest; trace report mode and hybrid counters; replay of a hybrid trace through `stepPolicyTick`; and replay of an older djev-only trace through a fixture archived engine snapshot.
- [ ] **Step 2: Run `node demo/test_benchmark.cjs` and `python3 -B -m unittest discover -s demo -p 'test_space_shooter.py' -v` and confirm the new contract cases fail** before implementation.
- [ ] **Step 3: Add manifest policy mode and mechanics parameters.** Browser runs explicitly select `hybrid`. Add a CLI `--policy-mode djev-only|hybrid` option; preserve the current raw Djev simulation for `djev-only`.
- [ ] **Step 4: Update benchmark stepping, event ingestion, replay, and report generation.** Feed recorded model proposals into `stepPolicyTick`; preserve safety and local weapon events; count hybrid metrics separately from model choices and normal-gun shots.
- [ ] **Step 5: Archive the exact HTML snapshot at run start in `runs/engines/<engine_hash>.html` and resolve it by manifest hash during replay.** Keep hash verification strict; if a matching archive is absent, return an explicit replay mismatch instead of silently using another engine.
- [ ] **Step 6: Update Python manifest validation and run summary to preserve the policy label and accept the new event types.** Leave upstream Djev request and normal-gun authorization contracts unchanged.
- [ ] **Step 7: Run `node demo/test_benchmark.cjs`, `node demo/test_space_shooter_logic.cjs`, and the Python bridge suite**; expect both new mode separation and old archived trace replay assertions to pass.
- [ ] **Step 8: Commit** as `feat: record and report hybrid policy runs`.

### Task 5: Hybrid HUD and user-facing documentation

**Files:**
- Modify: `demo/space-shooter.html` — labels, executed/proposed display, missile visuals, and counters.
- Modify: `README.md` — decision boundary, hybrid rules, benchmark commands, and interpretation limits.
- Test: `demo/test_space_shooter_logic.cjs`.

**Interfaces:**
- The dashboard keeps the Djev proposal separate from the effective movement and shows remaining bombs, active missiles, missile hits/kills, and safety override count.
- README commands show how to run `hybrid` and `djev-only` benchmarks; result interpretation labels hybrid-only behavior as local policy.

- [ ] **Step 1: Add a focused UI contract assertion** that the HTML exposes proposal/effective-move labels and elements for bomb, missile, and override counters.
- [ ] **Step 2: Run `node demo/test_space_shooter_logic.cjs` and confirm the new UI assertion fails** before the elements exist.
- [ ] **Step 3: Update the HUD and canvas.** Display Djev's proposed command apart from the effective command after arbitration; draw active missiles; display bomb charges, missile activity/hits/kills, and safety overrides; label the page as a hybrid controller.
- [ ] **Step 4: Rewrite the README's decision-boundary and benchmark sections** to document local movement safety, automatic missile use, the one-charge bomb, policy-mode separation, and the fact that hybrid survival is not Djev-only performance.
- [ ] **Step 5: Run `node demo/test_space_shooter_logic.cjs`**; expect UI contract and existing HTML checks to pass.
- [ ] **Step 6: Run the complete repository suites**: `node demo/test_space_shooter_logic.cjs`, `node demo/test_benchmark.cjs`, `node demo/test_strategy_regression.cjs`, and `python3 -B -m unittest discover -s demo -p 'test_space_shooter.py' -v`; expect all suites to pass and replay mismatches to be empty for fixture runs.
- [ ] **Step 7: Commit** as `docs: explain hybrid shooter control and metrics`.

## Spec Coverage Self-Review

- Deterministic coordinator, stale-command behavior, invulnerability, safe-path ranking, and failure path: Task 1.
- Missile cadence, cap, aim/retarget, steering, damage, expiry, serialization, and events: Task 2.
- Bomb trigger, ordering before movement/collision, charge conservation, residual risk, and trace evidence: Task 3.
- Proposal/effective provenance, policy and rules manifest, replay hashes, old engine snapshots, browser/CLI parity, and separated benchmark report: Task 4.
- Dashboard counters and hybrid/djev-only explanations: Task 5.
- Djev remains the sole source of normal-gun authorization; no additional model calls: Tasks 1 and 4.
