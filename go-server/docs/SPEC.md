# Kaetram Minimal Server Protocol SPEC (custom server → stock web client)

Source of truth (read-only): `packages/common/network/{packets,opcodes,modules,packet}.ts`,
`impl/{spawn,movement,animation,equipment,despawn,teleport,sync,map,handshake,welcome,resource,chat,list}.ts`,
`common/types/entity.d.ts`, client `src/network/{socket,connection,messages}.ts`,
`src/controllers/entities.ts create()`, `src/entity/character/character.ts`.

## 1. Wire shape

- Transport: `WebSocket`, URL from client config — `ws://host:port` (plain) or `wss://host` (ssl, no port) —
  `client/src/network/socket.ts:56`. Hub skipped when `!config.hub`.
- Framing: JSON text starting with `[`, else ignored (`socket.ts:100`).
  Server→client is a bulk array of packets: `[[id,data], [id,opcode,data], ...]`
  (`messages.ts handleBulkData→handleData`). Client→server: `send(packet,data)` → `[packet,data]`.
- Serialize rule (`common/network/packet.ts:25-33`): `[id,data]` if no opcode, else `[id,opcode,data]`
  (+ trailing bufferSize appended only when present — in practice only Map uses it, so Map is
  always 3 elements: `[4, base64, bufSize]` per `common/network/impl/map.ts:9-15`).

### Packet IDs (`common/network/packets.ts` enum, 0-based)

| Name | ID | Opcode? | Shape S→C |
|---|---|---|---|
| Connected | 0 | no | `[0,null]` (server hello; client replies Handshake) |
| Handshake | 1 | no | `[1,{type:'client',instance,serverId,serverTime}]` |
| Login | 2 | `Login{Login0,Register1,Guest2}` | C→S only |
| Welcome | 3 | no | `[3, PlayerData]` (self player, sets grid pos) |
| Map | 4 | no | `[4, base64(deflate(JSON regions)), bufSize]` — always 3 elements (`impl/map.ts:9-15`: `Utils.compress(JSON.stringify(data))` + `Utils.getBufferSize(data)`; client `connection.ts:264-280` `atob→inflate→loadRegions`) |
| Spawn | 5 | no | `[5, EntityData]` |
| List | 6 | `List{Spawns0,Positions1}` | S→C Spawns/Positions; C→S request `[6]` |
| Who | 7 | no | C→S `[7,[newIds]]` |
| Equipment | 8 | `Equipment{Batch0,Equip1,Unequip2,Style3}` | `[8,opcode,data]` |
| Ready | 9 | no | C→S `[9,{regionsLoaded,userAgent}]` (exact shape from `game.ts:202` inside `postLoad()`, sent after Welcome; `regionsLoaded` = `map.regionsLoaded` array, `userAgent` = client UA string) |
| Sync | 10 | no | `[10, PlayerData]` (other players' appearance/level) |
| Movement | 11 | `Movement{Request0,Started1,Step2,Stop3,Move4,Follow5,Entity6,Speed7}` | `[11,opcode,{instance,x?,y?,forced?,target?,movementSpeed?}]` |
| Teleport | 12 | no | `[12,{instance,x,y,withAnimation?}]` |
| Despawn | 13 | no | `[13,{instance,regions?}]` |
| Animation | 16 | no | `[16,{instance,action,resourceInstance?}]` |
| Chat | 19 | no | `[19,{instance?,message,withBubble?,colour?,source?}]` |
| Resource | 59 | no | `[59,{instance,state}]` (`ResourceState{Default0,Depleted1}`) |

Key modules: `EntityType{Player0,NPC1,Item2,Mob3,Chest4,Projectile5,Object6,Pet7,LootBag8,Effect9,Tree10,Rock11,Foraging12,FishSpot13}`;
`Orientation{Up0,Down1,Left2,Right3}`; `Actions{Idle0,Attack1,Walk2,Orientate3}`.
NOTE: `EntityType.Object=6` is un-spawnable — `entities.ts:78-163` has no `case` for it, so `entity`
stays undefined → `log.error('Failed to create entity …')` at `entities.ts:166` and nothing spawns. Never send `type:6` via Spawn.

## 2. Minimal boot → sprite on screen

Order (authoritative): `Connected(S→C)` → client sends `Handshake{gVer}` (`connection.ts:186-191`) →
`Handshake{type:'client',instance,serverId,serverTime}(S→C)` → client sends `Login{opcode:Guest|Login|Register}`
(`connection.ts:199-241`) → server sends `Welcome(PlayerData)` + `Map(compressed)` → client sends `Ready`
(`game.ts:202` inside `postLoad()`, called from `handleWelcome` at `connection.ts:249-254`) → server sends
`Spawn*` (after Welcome; Spawned via `connection.ts:134 onSpawn→entities.create`). Live handlers: `connection.ts:129-178,186-254`.

- `Welcome`/`Sync` payload = `PlayerData` (`impl/player.ts:15-28` extends `EntityData`):
  `{instance,type:0,key:'base',name,x,y,rank:0,pvp:false,orientation:1,hitPoints,maxHitPoints,
  mana,maxMana,movementSpeed,attackRange,level,equipments:[]}`. `Welcome.load()` sets self grid pos
  (`player.ts:106-126`); other players go through `entities.create()` → `sprites.get('player/'+key)`
  base key `player/base` (`player.ts:376-383`).
- `Map`: `[4, base64, bufSize]` where base64 = `Utils.compress(JSON.stringify(regions))` and bufSize =
  `Utils.getBufferSize(data)` (`impl/map.ts:9-15`, `packet.ts:25-33` appends trailing bufSize); client
  `atob→inflate→loadRegions` (`connection.ts:264-280`). For a stub, send a tiny-but-valid regions object —
  client renders tiles from it; entities render even with empty regions.
- `Spawn` payload = `EntityData` (`types/entity.d.ts:25-57`):
  `{instance,type,key,name,x,y[,movementSpeed,hitPoints,maxHitPoints,attackRange,level,hiddenName,
  orientation,count,enchantments,displayInfo,state]}`. `create()` maps `type→prefix` then
  `sprites.get(prefix+'/'+key)`; missing sprite = `console.trace` + no entity (`entities.ts:168-171`).

Sprite `key` prefix rules (`entities.ts:78-163`):

| type | prefix | example key → sprite path |
|---|---|---|
| Player 0 | `player/` | `base` → `player/base` (body); paperdoll `player/<slot>/<key>` |
| Mob 3 | `mobs/` | `crab` → `mobs/crab` |
| NPC 1 | `npcs/` | `shopkeeper` → `npcs/shopkeeper` |
| Item 2 / LootBag 8 | `items/` | `sword` → `items/sword` |
| Tree 10 | `trees/` | `oak` → `trees/oak` (+`state:0/1`) |
| Rock 11 | `rocks/` | `rock` → `rocks/rock` |
| Foraging 12 | `bushes/` | `bush` → `bushes/bush` |
| FishSpot 13 | `fishspots/` | `spot` → `fishspots/spot` |
| Chest 4 | `objects/` | `chest` → `objects/chest` |
| Pet 7 | `pets/` | `puppy` → `pets/puppy` |
| Projectile 5 | `projectiles/` | per projectile key — REQUIRES `ownerInstance`, `targetInstance`, `hit{type,damage}` (`entity.d.ts:51-54`, `entities.ts:262-314`; missing target → `createProjectile` returns undefined → `log.error`, no spawn) |
| Effect 9 | `effectentity/` | per effect key |

Spawn extras: Pet (`type:7`) REQUIRES `owner` (owner instance id) + `movementSpeed` (`types/pet.d.ts:3-5`, `entities.ts:344-362`; without a live owner it still spawns but never follows). `Object (type:6)` has no case → never spawns (see note above).

Mob spawn needs `hitPoints,maxHitPoints,attackRange,level,movementSpeed(≈220),orientation`
(`entities.ts:231-242`); tree/rock/etc need `state` (`ResourceEntityData`).

## 3. Per-frame triggers

- Movement (`connection.ts:409-470` → `character.go/move/follow/stop`): `Move{instance,x,y,forced?}` calls
  `entity.go()` → client pathfinds+walks (`performAction Walk` per step, `updateMovement`), arrival auto-`idle`.
  `Stop` → `entity.stop()` → idle. `Follow{instance,target}` → `entity.follow(target)`. `Speed{movementSpeed}`
  retunes ms/tile. Unknown instance → client sends `List` request (throttled 5 s). `List.Positions`
  mismatch → `game.teleport()`.
- Animation (`connection.ts:640-655`): `performAction(orientation, action)` (`character.ts:482-502`):
  `Attack(1)` → one-shot `atk_<dir>` (50 ms); `Walk(2)`/`Idle(0)`/`Orientate(3)` similarly; Left renders
  `*_right` flipped. Combat hits also force atk via Combat packet, not Animation. With
  `resourceInstance`, that resource `shake()`s + chop/mine sfx.
- Equipment Batch (`connection.ts:290-328` → `player.equip()`): each `{type:Modules.Equipment(Helmet0…
  Boots11), key, name, count, enchantments,...}` maps to `player/<slot>/<key>` (`player.ts:239-249`;
  `WeaponSkin` type maps to the `weapon/` folder, i.e. `player/weapon/<key>`, not `player/weaponskin/`);
  empty key = unequip; then `sync()` + light update. `Sync[10]` reloads another
  player's `equipments[]` + sprite (`connection.ts:388-399`).
- Resources: `Animation{action:Attack, resourceInstance}` = per-swing `shake()` (`resource.ts:16-20`,
  back to `idle()`); `Resource[59]{instance,state:1}` = `setExhausted(true)` → `exhausted` frame/stump,
  no silhouette, cursor skips it (`connection.ts:1583-1597`, `resource.ts:53-57`, `game.ts:384-385`).
  Spawn-time `state` presets it (`entities.ts:380-432`).
- Despawn/Teleport: `Despawn{instance}` → hurt→death anim→remove (item/chest/npc special-cased,
  `connection.ts:529-564`); `Teleport{instance,x,y,withAnimation?}` → freeze, optional death-anim,
  `game.teleport` (`connection.ts:478-522`).

## 4. Copy-paste packets (Node `ws.send(JSON.stringify(...))`)

```js
// A: boot self + mob + tree (Welcome id 3, Spawn id 5). Send as one bulk message.
ws.send(JSON.stringify([
  [3,{instance:'p1',type:0,key:'base',name:'hero',x:100,y:100,rank:0,pvp:false,orientation:1,
      level:1,hitPoints:100,maxHitPoints:100,mana:50,maxMana:50,movementSpeed:220,attackRange:1,equipments:[]}],
  [5,{instance:'m1',type:3,key:'crab',name:'Crab',x:103,y:100,movementSpeed:220,hitPoints:30,maxHitPoints:30,attackRange:1,level:1,orientation:1}],
  [5,{instance:'t1',type:10,key:'oak',name:'Oak',x:105,y:102,state:0}]
]));
// B: walk the mob two tiles right (Movement id 11, Move opcode 4)
ws.send(JSON.stringify([[11,4,{instance:'m1',x:105,y:100}]]));
// C: attack anim + tree shake, then exhaust it (Animation id 16, Resource id 59)
ws.send(JSON.stringify([[16,{instance:'m1',action:1,resourceInstance:'t1'}]]));
ws.send(JSON.stringify([[59,{instance:'t1',state:1}]]));
```
