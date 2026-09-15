# Go Server — Full Recreation Plan (stock web client, custom world)

Contract: keep JSON wire identical (`SPEC.md`: bulk `[[id,data]|[id,opcode,data]]` over `ws://host:port`). No `packages/*` edits, no client change. All game logic reimplemented in Go.

## 0. Stack

CURRENT (stub as built): Go 1.21 module `rpg-world-server`, `github.com/gorilla/websocket` for WS, flat `main.go` + `packets.go`, in-memory state only — no DB, no SQLite, no persistence yet.
TARGET: Go 1.22 + `gobwas/ws` + `modernc.org/sqlite` WAL + single binary `kaetram-stub`. Choice: `gobwas/ws` for raw low-alloc WS + manual JSON bulk write per tick; `nhooyr.io/websocket` is the fallback if HTTP-mux integration or WASM-adjacent API matters more than alloc control. `modernc.org/sqlite` (pure Go, no cgo) with `PRAGMA journal_mode=WAL; synchronous=NORMAL; busy_timeout=5000`. Single writer goroutine + prepared statements. Config via env (`HOST/PORT/HUB_ENABLED`). Data: copy JSON from `packages/server/data/` at build or embed via `go:embed`.

## 1. Inventory (what to port)

Subsystems: bootstrap (config/env/loader/world init); connection + anti-spam (handshake/login/ready, per-IP throttle, chat-spam bucket); regions/areas (MAP_DIVISION_SIZE 48 tiles/region, sideLength 24, surroundingRegions, area triggers/music/pvp); movement + anticheat (request/step/stop/follow/speed, collision/entity-grid verify, teleport-back); combat + projectiles (melee/range/magic, poison/burn/freeze/bleed, GCD, aggro/target); resources/gathering (trees/rocks/fishing/foraging, shake→deplete→respawn); drops/loot (mob tables, chest/lootbag, 30–50s despawn); quests + achievements (talk/kill/gather/craft stages, rewards — impl handlers + data defs, see per-file counts below); stores/bank/trade/craft/enchant (buy/sell/select, bank stash, trade sessions, recipes/tables, enchant reroll — CURRENT stub: stores in-memory only); guilds/friends/hub (create/invite/rank, friend list, hub routing); chat/commands ~1300 lines (say/global/guild + admin commands); abilities (mana/cd/effects/projectiles); minigames teamwar/coursing (lobby/queue/team/score); NPC/pet/stats (shop/dialog nodes, pet follow/attack, level/XP/stat allocation).
Data files (per-file, verified 2026-09-15): `items.json` 525, `mobs.json` 148, `npcs.json`, `spawns.json`, `tables.json` (drop tables), `trees/rocks/fishing/foraging.json`, `stores.json`, `crafting/` 7 recipe JSON files (`alchemy/chiseling/cooking/crafting/fletching/smelting/smithing.json` + `index.ts` loader), `quests/` 21 JSON files + `quest_bases/` 28 JSON files, `abilities.json`, `minigames.json`, plus `map/world.json` (1152×1008, collisions/objects/cursors), `sprites.json`.
Packets: 61 (`packets.ts` 0–60 Connected…AdminSync — 59 Resource, 60 AdminSync) + opcodes (`movement`: Request0/Started1/Step2/Stop3/Move4/Follow5/Speed7; `equipment` Batch0; `store` Buy/Sell/Select; `trade`, `guild`, `quest`, `ability`, `minigame` sub-ops). Port all 53 `network/impl/*.ts` send/recv shapes exactly (Handshake1/Welcome3/Map4/Spawn5/Equipment8/Sync10/Movement11/Teleport12/Despawn13/Animation16/Chat19/Combat/Heal/Effect/Resource59 etc).
State split — persistent (TARGET, TODO — no DB in stub yet): players, equipment/inventory/bank, quests/achievements, skills/XP, stats, abilities, guilds. `meta` table + schema-version/migration story is TODO with the SQLite work. In-memory only (CURRENT + carried forward): world/entities, grids/regions, combat instances, loot/chests (30–50s), resources (deplete/respawn timers), store cache (20s TTL), minigame lobby/queue.

## 2. Package layout — TARGET STRUCTURE (TODO M2+; CURRENT = flat `main.go` + `packets.go` in `go-server/` root)

- `cmd/server` — boot: env, load data, open SQLite, build world, start hub+WS, signal flush.
- `internal/net` — WS hub, bulk codec, 61 packet IDs + opcode consts, port of 53 `impl/*` frames; region-scoped `send/filter/broadcast`; rate limits (`MAX_CONNECTIONS` per IP, msg/s + chat buckets).
- `internal/world` — map load, grids (collision/entity), regions/areas, pathing helpers, surroundingRegions.
- `internal/entities` — player/mob/npc/pet/projectile/item/chest/lootbag/resource structs + (de)serialize matching `EntityData/PlayerData` (one json tag, omitempty optionals; never send EntityType 6).
- `internal/combat` — damage formula, GCD, aggro, poison/burn ticks, projectile flight+Hit.
- `internal/skills` — gathering rolls + crafting rolls, XP curves (19 skills incl Chiseling/Smelting pseudo).
- `internal/quests` — quest handlers over `quests/` 21 + `quest_bases/` 28 data defs + achievements engine.
- `internal/economy` — stores/bank/trade/craft/enchant sessions + 20s store cache.
- `internal/social` — guilds/friends/hub routing + chat/commands (~1300 lines port).
- `internal/persist` — SQLite schema + single-writer store, dirty-flush.
- `internal/minigame` — teamwar/coursing lobby/queue/score/instancing.

## 3. SQLite schema + in-memory list — TARGET (no DB in stub yet; CURRENT = all in-memory; `meta`/migration TODO with this work)

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

In-memory only (no tables): live `instance→*Entity` maps, region buckets + entity grids, combat/aggro/projectile state, lootbag/chest objects with 30–50s expiry timers, resource states/respawn timers, store snapshot cache (20s TTL), minigame lobby/queue/scores, connection sessions + spam buckets, map frame cache (`sync.Once` gzip+bufSize).

## 4. Milestones

- M1 boot+map+spawn — DONE in stub: `Connected→Handshake→Login→Welcome+Map→Ready→Spawn*`, `[4,base64gzip,bufSize]` 9 regions around spawn, entity-grid block + Stop/Teleport-back. Actual stub modes: TESTMAP (default ON — clone of 9 real regions + overlays: center pond, 5-oak row, 232-entity showcase grid stamped over base) / CLEAN (`CLEAN=1` — pure original terrain, zero overlays, exactly 2 players: Welcome hero + one fully-equipped adventurer Spawn + Equipment Batch) / COMBAT (`COMBAT=1` — pure terrain + combat scene: boss dummy + 4-bot party: warrior/archer/mage/support). TODO: full-world region streaming + embed all data files.
- M2 movement+anticheat — PARTIAL (stub single-resource block only): TODO full collision/entity-grid verify on every Request/Step, speed cheat check, Follow/Speed ops, `List.Positions` correction, region handoff `List.Spawns`.
- M3 combat+projectiles+GCD — TODO: melee/range/magic formulas, `Combat/Heal/Effect` packets, projectile `type:5` (owner+target+hit) flight, aggro/leash/respawn, poison/burn/freeze ticks, death→despawn.
- M4 resources+gathering — PARTIAL (Animation shake + Resource stump in stub path): TODO all trees/rocks/fishing/foraging tables, tool tiers, deplete→respawn timers, `Animation{resourceInstance}` + `Resource{state}` sync.
- M5 drops/loot+XPs/skills — TODO: drop tables → lootbag/chest Spawn (30–50s despawn), pickup, XP curves + level-up `Sync`, 19 skills.
- M6 quests/achievements+stores/bank/trade/craft — TODO: quest engine over `quests/` 21 + `quest_bases/` 28 defs + achievements, store buy/sell/select + 20s cache, bank stash, trade sessions, `crafting/` 7-file recipes, enchant.
- M7 social+hub+chat/commands — TODO: guilds/friends persist + hub routing, chat channels + ~1300-line commands port, anti-spam + `MAX_CONNECTIONS` per-IP + msg/s limits.
- M8 minigames+hardening — TODO: teamwar/coursing lobby/queue/instance/score; hardening: region-scope all sends, batch bulks per tick, rate limits, store-cache TTL, load/soak test, `go vet/build/gofmt` green.

## 5. Perf notes — TARGET (not yet in stub; CURRENT = per-send writes, no tick loop, no batching, no region-scoped broadcast yet)

20 Hz world tick, broadcast only deltas; batch each tick into one bulk `[...]` write per conn; region interest — only entities in client's surrounding regions (`List.Spawns/Positions`); compress Map once at startup (gzip + encodeURI bufSize, cached frame); single-writer DB goroutine, in-memory authoritative, flush dirty players every 5–15s + on disconnect; persist off hot path (never on tick); prepared statements + WAL readers; per-IP `MAX_CONNECTIONS`, per-conn msg/s + chat token buckets.
