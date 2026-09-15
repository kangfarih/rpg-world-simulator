# Go + SQLite Server Plan (stock web client, custom world)

Contract: `SPEC.md` (bulk `[[id,data]|[id,opcode,data]]`, `ws://host:port`). Reuse tables: `CLIENT-ASSETS.md`. No `packages/*` edits.

## Phase A — Stub (sprite on screen)

Go: `net/http` + gorilla/nhooyr WS, single hub, JSON bulk write. Boot order (`connection.ts:186-241`, `game.ts:202`): S `Connected[0,null]` → C `Handshake{gVer}` → S `Handshake{type:'client',instance,serverId,serverTime}` → C `Login{opcode:Guest}` → S `Welcome(PlayerData)` + `Map[4,base64,bufSize]` → C `Ready[9,{regionsLoaded,userAgent}]` (log only) → S `Spawn*` (after Welcome).

```json
[[3,{"instance":"p1","type":0,"key":"base","name":"hero","x":100,"y":100,"rank":0,"pvp":false,"orientation":1,"level":1,"hitPoints":100,"maxHitPoints":100,"mana":50,"maxMana":50,"movementSpeed":220,"attackRange":1,"equipments":[]}],[5,{"instance":"m1","type":3,"key":"crab","name":"Crab","x":103,"y":100,"movementSpeed":220,"hitPoints":30,"maxHitPoints":30,"attackRange":1,"level":1,"orientation":1}],[5,{"instance":"t1","type":10,"key":"oak","name":"Oak","x":105,"y":102,"state":0}]]
```

```go
// Field optionality mirrors common/types/entity.d.ts: instance/type/key/name/x/y required,
// everything else omitempty. Resource state lives on ResourceEntityData (resource.d.ts:27-28).
type EntityData struct {
  Instance string  `json:"instance"`
  Type     int     `json:"type"`
  Key      string  `json:"key"`
  Name     string  `json:"name"`
  X        int     `json:"x"`
  Y        int     `json:"y"`
  Colour   *string `json:"colour,omitempty"`
  Scale    *float64 `json:"scale,omitempty"`
  MovementSpeed *int `json:"movementSpeed,omitempty"`
  HitPoints     *int `json:"hitPoints,omitempty"`
  MaxHitPoints  *int `json:"maxHitPoints,omitempty"`
  AttackRange   *int `json:"attackRange,omitempty"`
  Level         *int `json:"level,omitempty"`
  HiddenName    *bool `json:"hiddenName,omitempty"`
  Orientation   *int `json:"orientation,omitempty"`
  Count         *int `json:"count,omitempty"`
  Enchantments  *Enchantments `json:"enchantments,omitempty"`
  OwnerInstance  *string `json:"ownerInstance,omitempty"` // projectile
  TargetInstance *string `json:"targetInstance,omitempty"` // projectile (required with Hit)
  Hit            *HitData `json:"hit,omitempty"` // projectile impact {type,damage}
  DisplayInfo    *EntityDisplayInfo `json:"displayInfo,omitempty"`
  State          *int `json:"state,omitempty"` // resources: 0 default / 1 depleted
}
type PetData struct {
  EntityData
  Owner string `json:"owner"` // required: owner player instance (pet.d.ts:3-5)
  // MovementSpeed required for pets — reuse EntityData.MovementSpeed, always set.
}
type PlayerData struct {
  EntityData
  Rank int `json:"rank"`
  Pvp  bool `json:"pvp"`
  Orientation int `json:"orientation"`
  Experience *int `json:"experience,omitempty"`
  Mana    *int `json:"mana,omitempty"`
  MaxMana *int `json:"maxMana,omitempty"`
  Equipments []EquipmentData `json:"equipments"`
}
```
// Never send EntityType.Object=6: no case in entities.ts:78-163 → log.error, nothing spawns.

Map stub: always 3 elements `[4,base64,bufSize]` (`impl/map.ts:9-15`, `packet.ts:25-33`): base64 = `Utils.compress(JSON.stringify({regions:{...}}))` (deflate→base64), bufSize = `Utils.getBufferSize(data)`; client `connection.ts:264-280 atob→inflate→loadRegions`. Entities render even if regions empty. Accept: client walks locally after `Move`.

Done when: hero+crab+oak visible at http://127.0.0.1:9000 with WS pointed at Go port.

## Phase B — Movement / Animation / Equipment

- Movement `[11,opcode,data]`: handle C→S Request; broadcast `Move{instance,x,y}` → client pathfinds+walks; `Stop`→idle; `Follow{instance,target}`; `Speed{movementSpeed}`. Validate bounds/collisions server-side, relay authoritative pos via `List.Positions` on mismatch.
- Animation `[16,{instance,action,resourceInstance?}]`: `Attack(1)` = atk row one-shot; with `resourceInstance` shakes tree + chop sfx.
- Equipment Batch `[8,0,[{type,key,...}]]` → `player/<slot>/<key>` paperdoll (`player.ts:239`: `WeaponSkin` type → `player/weapon/<key>`); `Sync[10,PlayerData]` for others. Resource `[59,{instance,state:1}]` → stump.
- Spawn requirements: projectile `type:5` needs `ownerInstance+targetInstance+hit` (`entities.ts:262-314`); pet `type:7` needs `owner+movementSpeed` (`pet.d.ts:3-5`); never send `type:6` (Object, un-spawnable). Ready C→S shape `[9,{regionsLoaded,userAgent}]` (`game.ts:202`).
- Tick: 20 Hz in-memory entity positions; broadcast only deltas.

## Phase C — SQLite persistence

```sql
CREATE TABLE players(instance TEXT PRIMARY KEY, name TEXT, x INT, y INT, level INT, hp INT, max_hp INT, data JSON);
CREATE TABLE entities(instance TEXT PRIMARY KEY, type INT, key TEXT, x INT, y INT, state INT, data JSON);
CREATE TABLE worlds(id INTEGER PRIMARY KEY, name TEXT, map_json TEXT);
PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;
```

Go structs mirror EntityData/PlayerData + `World{Name, MapJSON}`. Notes: WAL for concurrent readers; in-memory authoritative state, flush dirty players every 5–15 s + on disconnect; prepared statements; single writer goroutine to avoid lock contention.

## Phase D — Custom Tiled world pipeline

Author in Tiled (tileSize 16px, region MAP_DIVISION_SIZE 48 tiles per side per modules.ts:630, tilesheets 1–6 only) → `tools/map parser/exporter` → `packages/client/data/maps/map.json` (client) + `server world.json` (Go loads regions/collisions/spawns). New project: `yarn workspace @kaetram/tools map -- <tiled.tmx>`. Details: `WORLD-RECREATION.md`.

## Performance

In-memory maps `instance→*Entity`, region buckets for interest management; only send entities in client's regions (`List.Spawns/Positions`); batch packets per tick into one bulk array; compress Map once at startup; SQLite off hot path.
