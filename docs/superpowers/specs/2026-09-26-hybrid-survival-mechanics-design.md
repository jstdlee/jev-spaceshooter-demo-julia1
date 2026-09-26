# Hybrid Survival Mechanics Design

The numeric defaults below are proposals for review before implementation.

## Goal

Add autonomous homing missiles, a local movement safety coordinator, and a limited defensive bomb to the SpaceShooter demo. Djev remains the slower strategic position and normal-gun policy. The game must expose when local systems changed Djev's proposal and benchmark the combined controller as a hybrid.

## Current context

- `demo/space-shooter.html` contains the deterministic 60 Hz game, movement forecast, Djev command controller, Canvas renderer, and browser integration.
- `demo/space_shooter_server.py` accepts one structured Djev request with path, fire, and intent choices, normalizes its result, and records run events.
- `demo/benchmark.cjs` replays model commands through the archived game engine and verifies game checkpoints and command provenance.
- The current game has one ordinary player gun. It has no missile/bomb state, local safety veto, or local rescue movement. A valid but dangerous Djev move executes unchanged.

## Approved direction

Use a hybrid with three distinct responsibilities: Djev proposes strategic movement and ordinary-gun use; a deterministic local safety system protects against imminent projectile collisions; and deterministic local weapons provide auto-aim missiles plus an emergency bomb. Do not add extra model requests for bullet or bomb decisions. All local decisions use the same live deterministic game state and are visible in the trace and UI.

## Architecture

### Deterministic simulation and command arbitration

Keep the game core as the authority for positions, collisions, missiles, bomb charges, scoring, and replay state. Before each simulation tick, a deterministic coordinator considers the current Djev command and live game state. It may substitute only movement and trigger the bomb; it does not rewrite the upstream response or silently attribute local actions to Djev.

Every tick, the coordinator evaluates the selected movement and the other eight legal directions over a 250 ms emergency horizon against currently active bullets and enemies. Contacts during known invulnerability do not count as damaging contacts. It honors Djev's movement when that path has no projected damaging contact. If the proposed route is unsafe, it selects a route with no projected contact; among safe routes, it prefers the greatest minimum clearance, then the greatest wall room, then a stable action-ID order. If all routes are unsafe, it chooses the route with the latest projected contact and reports that escape was unavailable. Re-evaluate from the current state each tick so new projectiles enter the check immediately.

When there is no fresh Djev command, the same local movement check may choose an emergency path only if holding is unsafe; ordinary gun fire remains ceased without a valid Djev authorization. The positioning policy is lower priority than an imminent collision. Safety overrides carry their own source and the Djev decision ID they superseded, if any.

### Auto-aim missiles

Add a local missile weapon independent of the normal gun choice. When at least one enemy is alive and fewer than three player missiles are active, launch automatically every 1.5 seconds. A missile starts at the player's position, travels at 420 px/second, and turns toward a deterministic predicted intercept point at up to 6 radians/second. Estimate enemy velocity from the game engine's current motion rule. Select the target with the shortest estimated intercept time; break ties by stable enemy ID. If the target is destroyed, reacquire using the same rule. A missile deals one point of enemy damage on contact, then is removed; it expires after two seconds. It does not destroy hostile bullets. These values are rule constants recorded in the run manifest so they can be tuned without hiding the simulation configuration.

Ordinary shots remain model-authorized. Auto missiles are explicitly attributed to the local weapon system. Existing enemy-destruction score values apply regardless of whether a normal shot or missile delivered the final damage.

### Defensive bomb

Start each run with one bomb charge and do not replenish it between waves. The coordinator uses it only when all nine movement routes predict a damaging hostile-projectile contact within 250 ms and at least one such projectile is the threat. Detonation removes all currently active hostile bullets before movement and collision resolution, then recomputes movement risk without those bullets before choosing the executed path. It neither damages enemies nor grants invulnerability. If a route is safe, or the only projected collision is with an enemy body, keep the charge.

The bomb is a deterministic local action, not a Djev choice. Record its trigger evidence, charge before/after, and removed projectile IDs. If the bomb cannot clear a body collision or a projectile spawned after detonation, the coordinator records that residual risk rather than promising safety.

## Trace, replay, and benchmark behavior

- Record Djev's selected command separately from the effective movement and source used by the coordinator.
- Add explicit events for safety overrides, bomb use, missile launch/retarget/hit/expiry, missile damage, and missile-caused enemy destruction.
- Include policy mode and all missile, safety-horizon, and bomb parameters in the immutable run manifest and rules hash.
- Run the same coordinator and weapons in browser and CLI simulation. Replay starts from a recorded checkpoint and re-applies model commands through the same deterministic engine; its state hashes must include missile state, bomb charges, and new counters.
- Report `djev-only` and `hybrid` results as separate policy modes. Hybrid survival, kills, bomb use, override count, and lives remaining must not be presented as model-only performance.
- Retain old trace replay through each trace's archived engine snapshot. Bump engine/context/prompt versions as needed when the request context or trace contract changes.

## UI and documentation

Show Djev's proposed move separately from the movement actually executing when the safety layer intervenes. Display remaining bomb charges, active missile count, missile hits/kills, and safety override count. Explain that missiles, bombs, and emergency movement are local hybrid systems. Update the README's decision-boundary description, action rules, benchmark limits, and reproduction instructions.

## Failure handling

- Invalid, stale, or missing Djev replies do not authorize ordinary gun fire. The local safety layer can still dodge an imminent known threat; otherwise movement remains neutral.
- Safety computation must be bounded and deterministic. If it cannot produce a valid route assessment, preserve the current movement when it is authorized, do not spend a bomb on an unknown condition, and record a safety-calculation failure.
- No local action may be silently recorded as a Djev command. All interventions retain provenance through trace, dashboard, and replay.

## Verification and acceptance

1. A fixed seed produces identical missile trajectories, bomb effects, coordinator choices, counters, and state hashes on repeated simulation and trace replay.
2. Missiles auto-launch only with a target and below the active-missile cap; target choice, reacquisition, steering, damage, cooldown, and expiry follow the manifest rules.
3. The coordinator preserves a safe Djev move, overrides a route with an imminent projected hit when a safe alternative exists, and chooses a deterministic best-effort route when none exists.
4. The bomb is conserved when movement avoids the hit, is used only for the specified imminent projectile case, clears existing hostile bullets, and consumes exactly one charge.
5. Browser and CLI runs produce the same hybrid physics and replay hashes for equivalent commands.
6. Traces distinguish model choices from local actions. Reports expose policy mode and hybrid counters; pure-model and hybrid comparisons cannot be combined silently.
7. Add test-first coverage for each behavior and for the command/trace/replay contract. Run the repository's complete test suites before claiming completion.

## Scope boundaries

- No second or third LLM call; the bullet and bomb specialists are local deterministic policies.
- No visual-physics feedback, frontend framework, or replacement of the existing deterministic simulation.
- No bomb damage, bomb invulnerability, charge replenishment, or model-triggered bomb choice in this baseline.
- No changes to enemy firing, player hitboxes, movement speed, lives, or difficulty profiles as part of this feature.
