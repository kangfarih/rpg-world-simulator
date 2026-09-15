# Go Plan — Server Recreation (Part I) + Rolling Updates (Part II)

Contract: JSON wire identical (`SPEC.md`: bulk `[[id,data]|[id,opcode,data]]` over `ws://host:port`). No `packages/*` edits. All game logic reimplemented in Go.

## Part I — Recreation (condensed from GO-SERVER-PLAN.md)

### 0. Stack

CURRENT (stub): Go 1.21 module `rpg-world-server`, `gorilla/websocket`, flat `main.go` + `packets.go`, in-memory only — no DB, no SQLite, no persistence.
TARGET: Go 1.22 + `gobwas/ws` (`nhooyr.io/websocket` fallback) + `modernc.org/sqlite` (pure Go, no cgo) `PRAGMA journal_mode=WAL; synchronous=NORMAL; busy_timeout=5000`. Single-writer goroutine + prepared statements. Env config (`HOST/PORT/HUB_ENABLED`). Data copied/embedded from `packages/server/data/`.

### 1. Inventory

Subsystems: bootstrap; conn + anti-spam (handshake/login/ready, IP throttle, chat bucket); regions/areas (48 tiles/region, sideLength 24, surroundingRegions); movement + anticheat (speed/collision/entity-grid verify, teleport-back); combat + projectiles (GCD, aggro, poison/burn/freeze/bleed); gathering (deplete→respawn); drops/loot (30–50s despawn); quests + achievements; stores/bank/trade/craft/enchant (stub: stores in-memory only); guilds/friends/hub; chat/commands ~1300 lines; abilities; minigames teamwar/coursing; NPC/pet/stats.
Data (verified 2026-09-15): `items.json` 525, `mobs.json` 148, `npcs/spawns/tables.json`, `trees/rocks/fishing/foraging.json`, `stores.json`, `crafting/` 7 recipes + index loader, `quests/` 21 + `quest_bases/` 28, `abilities.json`, `minigames.json`, `map/world.json` 1152×1008; `sprites.json` client-only, no port.
Packets: 61 (0–60 Connected…AdminSync; 59 Resource, 60 AdminSync) + opcodes (movement 0/1/2/3/4/5/7, equipment Batch0, store/guild/quest/ability/minigame sub-ops). Port all 53 `network/impl/*.ts` frames exactly.
State split — persistent TARGET TODO (no DB in stub): players, equipment/inventory/bank, quests/achievements, skills/XP, stats, abilities, guilds; `meta` + migrations TODO. In-memory: entities, grids/regions, combat, loot timers, resource timers, store cache 20s TTL, minigame lobby/queue.

### 2. Package layout — TARGET (TODO M2+; CURRENT = flat `main.go` + `packets.go`)

- `cmd/server` — env, load data, open SQLite, build world, start hub+WS, signal flush.
- `internal/net` — hub, bulk codec, 61 IDs + opcodes, 53 `impl/*` frames; region-scoped send; rate limits.
- `internal/world` — map, collision/entity grids, regions/areas, pathing.
- `internal/entities` — player/mob/npc/pet/projectile/item/chest/resource structs matching `EntityData/PlayerData`; never send type 6.
- `internal/combat` — formulas, GCD, aggro, DoT ticks, projectile flight+Hit.
- `internal/skills` — gathering/crafting rolls, XP curves (19 skills incl Chiseling/Smelting pseudo).
- `internal/quests` — handlers over `quests/` 21 + `quest_bases/` 28 + achievements.
- `internal/economy` — stores/bank/trade/craft/enchant + 20s store cache.
- `internal/social` — guilds/friends/hub routing + chat/commands.
- `internal/persist` — SQLite schema + single-writer store, dirty-flush.
- `internal/minigame` — teamwar/coursing lobby/queue/score.

### 3. SQLite schema — TARGET (stub: all in-memory; `meta`/migration TODO)

```sql
PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;
CREATE TABLE players(instance TEXT PRIMARY KEY, name TEXT, x INT, y INT, level INT, hp INT, max_hp INT, mana INT, max_mana INT, orientation INT, data JSON);
CREATE TABLE inventory(player TEXT, slot INT, item TEXT, count INT, enchant JSON, PRIMARY KEY(player,slot));
CREATE TABLE equipment(player TEXT, slot TEXT, item TEXT, enchant JSON, PRIMARY KEY(player,slot));
CREATE TABLE bank(player TEXT, slot INT, item TEXT, count INT, PRIMARY KEY(player,slot));
CREATE TABLE skills(player TEXT, skill TEXT, level INT, xp BIGINT, PRIMARY KEY(player,skill));
CREATE TABLE stats(player TEXT PRIMARY KEY, points INT, data JSON);
CREATE TABLE abilities(player TEXT, ability TEXT, unlocked INT, PRIMARY KEY(player,ability));
CREATE TABLE quests(player TEXT, quest TEXT, stage INT, done INT, PRIMARY KEY(player,quest));
CREATE TABLE achievements(player TEXT, ach TEXT, progress INT, done INT, PRIMARY KEY(player,ach));
CREATE TABLE guilds(id TEXT PRIMARY KEY, name TEXT, owner TEXT, data JSON);
CREATE TABLE guild_members(guild TEXT, player TEXT, rank INT, PRIMARY KEY(guild,player));
CREATE TABLE friends(player TEXT, friend TEXT, PRIMARY KEY(player,friend));
CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT); -- world.json hash, schema version
```

In-memory only: live entities, region buckets, combat/aggro/projectiles, loot/chests (30–50s), resource timers, store cache (20s TTL), minigame lobby/queue, sessions + spam buckets, map frame cache.

### 4. Milestones M1–M8

- M1 boot+map+spawn — DONE in stub: `Connected→Handshake→Login→Welcome+Map→Ready→Spawn*`, `[4,base64gzip,bufSize]` 9 regions, entity-grid block + Stop/Teleport-back. Modes: TESTMAP (default, 9 real regions + overlays: pond, 5-oak row, 232-entity grid) / CLEAN=1 (pure terrain, 2 players) / COMBAT=1 (boss dummy + 4-bot party). TODO: full-world streaming + embed all data.
- M2 movement+anticheat — PARTIAL (single-resource block): TODO full verify per Request/Step, speed check, Follow/Speed, `List.Positions`, region handoff.
- M3 combat+projectiles+GCD — TODO: formulas, `Combat/Heal/Effect`, projectile flight, aggro/leash/respawn, DoT ticks, death→despawn.
- M4 resources+gathering — PARTIAL (shake + stump): TODO all tables, tool tiers, deplete→respawn, `Animation` + `Resource` sync.
- M5 drops/loot+XP/skills — TODO: drop tables → lootbag/chest (30–50s), pickup, XP curves, 19 skills.
- M6 quests/achievements+stores/bank/trade/craft — TODO: quest engine + achievements, store buy/sell/select + cache, bank, trade, 7-file recipes, enchant.
- M7 social+hub+chat/commands — TODO: guilds/friends persist + hub routing, ~1300-line commands port, anti-spam + per-IP limits.
- M8 minigames+hardening — TODO: teamwar/coursing lobby/queue/score; region-scope sends, per-tick bulks, rate limits, load/soak, `go vet/build/gofmt` green.

### 5. Perf notes — TARGET (stub: per-send writes, no tick/batching/region-scope)

20 Hz tick, deltas only; one bulk `[...]` write per conn per tick; region interest (surrounding regions); Map gzipped once at boot (cached frame); single-writer DB goroutine, flush dirty players 5–15s + on disconnect; persist off hot path; prepared statements + WAL; per-IP `MAX_CONNECTIONS`, per-conn msg/s + chat buckets.

### 6. Gap closures (audit 2026-09-15, all TARGET unless noted)

- G1 commands (`internal/social/commands.go`, rank-gated player<mod<admin): player/mod/admin/boss/teleport groups (~120 cases); unknown → Chat error; log admin use.
- G2 minigames: teamwar vs coursing sharing a lobby/queue/instance interface; `minigames.json` drives rules; Join/Leave/Queue/Start/Score/End.
- G3 areas (`internal/world/areas.go`): camera/music/pvp direct; chest≈loot, dynamic≈trigger, minigame≈instance, overlay≈safe; checked every Step + teleport.
- G4 globals: static light table on region enter; sign text on interact; read-only, no persistence.
- G5 mob AI: spawn-queue → roam → chase → leash → return → boss phase hooks.
- G6 pets: follow (teleport if >12 tiles), attack, tame rolls, hunger/expire timers; never block movement.
- G7 loot: blink (10s `Sync` warning) → destroy; chest refill from `tables.json`.
- G8 stores: Buy/Sell/Select; 20s snapshot TTL; `stores.json` prices authoritative.
- G9 social: friends + guilds (persist `guilds`/`guild_members`) + hub relay; Discord = local-broadcast hook only.
- G10 REST + hub auth: read-only `/players/:name /guilds /status` (env `API_PORT`, default off); hub bearer `HUB_TOKEN`.
- G11 Sentry opt-in (`SENTRY_DSN` empty = off, sample 0.1); admin web panel out-of-scope.
- G12 E2E (M8): Cypress parity + headless Go harness (`go-server/e2e/`); M8 gates on both green.
- G13 map-build: `tools map` export is a required pre-build step; `meta.world_hash` mismatch → refuse boot.
- G14 hub (`internal/social/hub.go`): server-list + player-location cache (TTL) + offline mailer; single goroutine, token-guarded.
- G15 filter + i18n: profanity wordlist (mask `***`, mod-bypass, mute counter); server strings English-only.
- G16 port checklist: controllers→`net`; combat/effects/projectiles/formulas→`combat`; abilities/XP→`skills`; equipment/containers→`entities`/`persist`; quests→`quests`; 53 frames→`net`; config→`cmd/server`.

## Part II — Rolling Update System (from REWRITE-V2.md + brainstorm)

### 7. CURRENT vs TARGET

CURRENT (stub): single binary, no DB, fixed IDs (`p1`/`p2`), `Handshake{gVer}` received but ignored (no gate), no hub/router — client points directly at the stub.
TARGET: one `go-server` binary as `router` / `shard` / `all-in-one` (flags); gobwas/ws; SQLite WAL per shard + shared account DB; stamped `buildID` (git SHA) + `gVer` with enforced gate; standalone router + hub server-list; RUNNING→DRAINING→SHUTDOWN lifecycle; expand-only migrations. Nothing below assumes stub behavior.

### 8. Worlds-as-versions

A deploy = new world rows in the hub server-list tagged with the new version (`buildID`, `gVer`, state). Router sends all NEW logins to the newest healthy (RUNNING) version; the old version drains: no new sessions, existing players finish tasks naturally, state persists, then shutdown on empty or drain timeout. Rollback = flip the router flag back to the old version (kept warm) — no data migration, no redeploy.

### 9. gVer lockstep + client banner

Client build is pinned to server `gVer`: router rejects mismatched `Handshake{gVer}` with a hub redirect at the correct build. Mismatched clients stay playable on the old version until they refresh — no forced kick. Banner (via existing Chat/notice frame, no wire change): "new version available — refresh when ready". Refresh loads the new bundle and opens a new socket atomically (old socket closes only after the new session is up). Cross-build moves are always disconnect+reconnect via hub; `Teleport` stays same-socket only.

### 10. Lifecycle, canary, health

Per instance: `RUNNING → DRAINING → SHUTDOWN`. Deploy: start new build alongside old (new port, self-registers RUNNING) → canary 5% of new logins, health-gate → 100% cutover → SIGTERM old → DRAINING (no new conns, sim continues) → flush + exit on empty or drain timeout (default 30 min, flag-tunable). Health endpoint (`/healthz` + state/load) with 5s heartbeats; 3 missed beats evicts the entry. Forced evacuation = notice + disconnect reason (never `Teleport`); client re-logs via hub.

### 11. Restartability, migrations, ownership

No RAM-only progression: every XP/item/quest grant enters the write queue before its packet goes out; flush every 30s dirty-set + on disconnect/handoff + mandatory pre-shutdown barrier. Ephemeral state (positions, aggro, timers, routing table) is rebuilt from DB/spawn tables on restart. Migrations are expand-only (nullable/default columns, additive tables; drops/renames two releases later); binary refuses boot on newer-than-known schema. One single-writer owner per DB file (`sql.DB` max-open-conns=1 + write mutex); old and new builds never dual-write — cross-build state moves by owner-to-owner RPC (copy-on-migrate is fallback only).

### 12. Milestones R1–R4

- R1 router + gVer gate: `Register`/heartbeat table, newest-RUNNING routing, `Handshake{gVer}` reject→hub redirect, SIGTERM→DRAINING, drain timeout + flush barrier, `/healthz`. Done: `kill -TERM` mid-session keeps players on; new logins land on new proc.
- R2 multi-world versions: version-tagged world rows, new-login cutover, old drains, cross-shard handoff RPC + router region→build lookup, refresh banner via Chat-19 (Notification-25 alt), no new opcode. Done: two versions live side by side, clients split by login time.
- R3 runbook + rollback: backup (`sqlite3 .backup` snapshot of every DB, timestamped archive) → migrate → start new → health-gate → canary 5% → 100% → TERM old → warm-hold 15 min → shutdown; rollback = router flip-back. Done: full deploy with players online, only gVer-mismatched redirects.
- R4 chaos drill: 100+ bots, `kill -TERM` mid-fight/mid-trade, `kill -9` (WAL recovery), skew soak. Pass: no item/XP/quest loss, p95 tick <50 ms, hub reconnect <10s.
