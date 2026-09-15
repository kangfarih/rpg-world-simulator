# REWRITE-V2 — Full MMO Go Server with GW2-Style Zero-Downtime Live Updates

Locked: full MMO scope (GO-SERVER-PLAN §6 parity). JSON wire unchanged — client untouched. SQLite via modernc.org/sqlite, WAL mode. Single-binary ops. No `packages/*` edits.

## 0. CURRENT vs TARGET

CURRENT (go-server stub today): single flat binary (`main.go` + `packets.go`, no `cmd/server` + `internal/*` layout), gorilla/websocket transport, in-memory world only (no DB, no SQLite), fixed demo IDs (`p1` hero / `p2` guest), client `Handshake{gVer}` is received but gVer is unread/ignored (no gate), HUB none (hub/login server-list not built; client points directly at stub).

TARGET (this plan): one `go-server` binary run as `router`, `shard`, or `all-in-one` via flags; gobwas/ws transport (gorilla fallback); SQLite WAL per shard + shared account DB; real buildID+gVer stamping with enforced gVer gate; standalone router + hub server-list + login flow; RUNNING→DRAINING→SHUTDOWN drain lifecycle; expand-only migrations. Every CURRENT→TARGET gap is a TODO below — nothing below assumes stub behavior that does not exist yet.

Model: Guild Wars 2 live-update pattern (after the public GW2 pattern described in ArenaNet dev talks/blogs): microservices + self-registration (RouteSrv pattern), blue-green at instance level, restartability (no local-only state). GW2 runs frequent updates with no player-visible downtime; map instances otherwise run for weeks.

Existing Kaetram hooks to reuse (all TODO — none wired in stub today): `Handshake{gVer}` version gate (client already sends gVer; stub ignores it today — gate is V2-M1 work), hub server-list + login flow (requires building the hub first — no hub exists today), client `Teleport` packet (same-socket map moves only — NOT a cross-build vehicle), regions already sharded (`MAP_DIVISION_SIZE` 48, sideLength 24).

## 1. Goals / non-goals

Goals: (a) full MMO parity per GO-SERVER-PLAN §6 — all 61 packets, combat/skills/quests/economy/social/minigames, 525 items / 148 mobs / 94 recipes / 49 quests; (b) zero-downtime deploys — old build drains while new build takes new sessions; (c) single-binary ops — one `go-server` binary runs as router, shard, or both via flags; SQLite per shard + shared account DB, no external DB dependency.

Non-goals: no client changes (JSON wire frozen, client only sees new address via login/hub reconnect — Teleport never crosses builds); no background-asset streaming (client full-reloads on reconnect — accepted); no cross-region single-tick atomicity (trades/guild ops via shard-to-shard RPC, eventually consistent).

## 2. Architecture

One binary, three roles by flag: `router` (gateway/login + routing table), `shard` (world simulation for a region group), `all-in-one` (later convenience: router+shard in one process with drain support — NOT the V2-M1 path; V2-M1 builds the router first as a separate tiny process, see §6).

Router (RouteSrv-equivalent): in-memory routing table `buildID → {addr, gVer, state, load, regions}`. Shards self-register on boot (`Register{buildID,gVer,addr,regions,load}` + 5s heartbeat/health check); no hardcoded addresses. Router serves login/hub server-list and directs NEW sessions to newest healthy build. Health failures evict entries after 3 missed beats.

World shards: each shard owns a region group (e.g. 3×3 region tiles); phase 1 is a single shard owning all regions. Shard holds authoritative sim (positions, combat, AI, timers) at 20 Hz, region-scoped interest broadcast, bulk packets. Inter-shard: cross-boundary handoff RPC + player state transfer; phase 1 in-proc call, later an interface-first bus (NATS is one pluggable transport option for subject `shard.handoff` — NOT required; in-proc default, NATS only if adopted).

Persistence: SQLite WAL per shard (world state: players/inventory/bank/quests/skills/guilds belonging to its regions) + one shared account DB (credentials, characters, entitlements). Ownership rule: exactly one single-writer owner process per DB file (Go `sql.DB` max-open-conns=1 + write mutex inside the owner). During drain overlap the old and new builds NEVER open the same shard file as dual writers — cross-build state moves by owner-to-owner RPC only (flush + transfer + ack), or by copy-on-migrate for a cold move. Pick: owner+RPC (copy-on-migrate documented as fallback only). Shard never blocks tick on writes — queue + flush off hot path.

## 3. Live-update protocol

Build versions: every binary stamped `buildID` (git SHA) + `gVer` (wire version). TODO gVer gate (V2-M1): client already sends `Handshake{gVer}` but the stub ignores it today — router must reject mismatched `gVer` with a hub server-list pointing at the correct build (requires the hub to exist first; client already handles plain reconnect).

Lifecycle per instance: `RUNNING → DRAINING → SHUTDOWN`. Deploy: (1) start new build alongside old (same host, new port; registers with router as RUNNING); (2) router cutover — all NEW logins/sessions route to new build only; (3) old build receives SIGTERM → enters DRAINING: stops accepting new sessions/connections, keeps simulating existing players; (4) players leave naturally (logout, or disconnect+reconnect via login/hub to a new-build shard — Teleport CANNOT do this, it is same-socket only); (5) after drain timeout (default 30 min, flag-tunable) or zero players, old build flushes, closes cleanly, exits; orchestrator stops it.

Reconnect flow: same-build, same-socket map moves use client `Teleport` packet (no reconnect, no loading screen). Cross-build moves (drain evacuation, login, rollback) are ALWAYS disconnect+reconnect via login/hub returning the new build address — client full-reloads (loading screen acceptable). Hub server-list + plain reconnect support requires building the hub first. Forced evacuation: shard sends a notice + disconnect reason (NOT a Teleport — Teleport cannot cross sockets/builds); client re-logs via hub to the new build.

Rollback: old build kept warm N minutes (default 15) after cutover before SHUTDOWN — if new build fails health checks, router flips new-session routing back; players on new build disconnect+reconnect to old via hub flow. Keep last two binaries + DB backups on disk.

## 4. State rules (restartability)

Principle (GW2 tournament-server rule): no local-only state — every layer recreates state from upstream (DB / authoritative service) on restart.

Ephemeral (recreated, never restored verbatim): live positions (respawn from DB save-point + spawn tables), combat instances/AI aggro/timers/cooldowns (rebuilt from mob spawn tables + player stats), region interest sets, routing table (rebuilt from self-registrations), bus subscriptions (in-proc default; NATS subscriptions only if the NATS transport is plugged in).

Persistent (SQLite): players (pos/checkpoint, HP/mana, XP/level), equipment/inventory/bank, skills/cooldown timestamps, quests flags/progress, guilds/parties/membership, stores Listings as configured. Flush policy: interval (every 30s dirty-set), on disconnect, on cross-shard handoff, and mandatory pre-shutdown barrier (DRAINING→SHUTDOWN blocks until queue empty + checkpoint). No in-memory-only progression — any XP/item/quest grant must be in the write queue before the packet announcing it goes out.

Crash recovery: on boot, shard loads latest checkpoint + replays write-ahead (WAL is the log); orphaned sessions (connected flag set but dead) cleared by heartbeat expiry.

## 5. DB migrations

Expand-only, backward-compatible: new columns always nullable or with defaults; new tables additive; never rename/drop in a deploy that overlaps a drain window. `schema_version` table; binary checks min-compatible version at boot and refuses to start on newer-than-known (fail fast, router keeps old build).

Skew window: during drain, old build writes old schema, new build writes new schema — both must succeed, but they NEVER share one file as dual writers (owner+RPC per §2). So migration runs BEFORE new-build rollout and must be readable+writable by old code (e.g. add column nullable → old ignores it; backfill in background after old build fully SHUTDOWN; drop/rename only two releases later). Rollback-safe: downgrade = router flip only, no down-migration.

## 6. Milestones

V2-M1 standalone router + gVer gate + drain lifecycle (router FIRST as a separate tiny process — NOT all-in-one; all-in-one comes later, or relax the done-criteria to router-only): `Register`/heartbeat table, login routes to newest RUNNING, `Handshake{gVer}` reject→hub redirect (hub must be built for the redirect to land anywhere), SIGTERM→DRAINING (no new conns, existing play on), drain timeout + pre-shutdown flush barrier, `/healthz` + state endpoint. Done when: old proc `kill -TERM` mid-session keeps player connected until logout, new logins land on new proc. Relaxation allowed: ship router-only (no shard sim) and gate the criteria on routing + drain, not world sim.

V2-M2 multi-instance regions + registration: shard flag takes region-group assignment; cross-shard handoff RPC (DB flush + state transfer + Spawn on target; Teleport packet used only for same-socket moves, cross-shard/cross-build handoff is disconnect+reconnect or socket handoff, never bare Teleport); router region→build lookup. Done when: two shards split the map, walking across boundary hands off without dup/loss.

V2-M3 blue-green deploy runbook + rollback: deploy script (backup DBs first, build, migrate expand-only, start new, health-gate, router cutover, TERM old, warm-hold N min, shutdown), backup/restore steps (pre-deploy snapshot of every shard DB + account DB with `sqlite3 .backup`, timestamped archive, restore procedure = stop new, restore files, router flip-back, restart old buildID, verify checkpoint), rollback path (router flip-back), runbook doc + dry-run on staging. Done when: full deploy with players online, zero rejects except gVer-mismatched clients redirected cleanly via hub.

V2-M4 load test + chaos: bots (100+ conns, movement/combat/trade), `kill -TERM` mid-fight + mid-trade, shard kill -9 (WAL recovery), schema-skew soak. Pass criteria: no item/XP/quest loss (DB diff vs event log), p95 tick <50 ms, reconnect via hub <10s.

## 7. Risks

SQLite single-writer owner per shard (+RPC during drain, §2): fine at our scale (one writer goroutine, WAL readers concurrent); risk is cross-shard transactions (trade/guild) — mitigate with handoff RPC + idempotency keys, accept eventual consistency. Schema skew window: mitigated by expand-only rule + two-release drop policy; residual risk is a forgotten NOT NULL — gate with migration lint in CI. Client reconnect UX: reconnect = full reload + loading screen (Kaetram client has no background-asset streaming) — accepted; keep hub redirect message clear (hub must exist first). GVer TODO gate: stub ignores gVer today; until V2-M1 lands, mismatched clients are NOT rejected — after the gate lands, mismatched clients cannot stay on old build past drain timeout (acceptable, matches GW2 forced-patch behavior). Split-brain routing (two routers): run single active router phase 1; later Raft or sticky-LB, shards accept last-writer-wins registration with buildID fencing.
