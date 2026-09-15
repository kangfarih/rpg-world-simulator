# Client Assets Catalog — Reuse Plan (read-only survey, no source edits)

Source: `packages/client/{src,public,data}`, `packages/common/{network,types}`. Counts verified 2026-09-15.

## 1. Class table (~40 core classes)

| Area | File | Class | Reuse as-is |
|---|---|---|---|
| Entities | `src/entity/entity.ts` | Entity (pos, sprites, animation, movement) | yes — base for all |
| | `src/entity/sprite.ts` | Sprite (image, load, offset) | yes |
| | `src/entity/animation.ts` | Animation (rows, lengths, speeds, flip) | yes — frame math `frame.x=index*width, frame.y=row*height` |
| | `src/entity/character/character.ts` | Character (go/move/follow/stop, performAction, atk 50ms, walk 120ms) | yes |
| | `src/entity/character/player/player.ts` | Player (+equip/sync/load, paperdoll `player/<slot>/<key>`, base `player/base`) | yes |
| | `src/entity/character/mob/mob.ts` | Mob | yes |
| | `src/entity/character/pet/pet.ts` | Pet | yes |
| | `src/entity/npc/npc.ts` | NPC | yes |
| | `src/entity/objects/item.ts` | Item (+LootBag shares `items/` prefix) | yes |
| | `src/entity/objects/chest.ts` | Chest (`objects/` prefix) | yes |
| | `src/entity/objects/projectile.ts` | Projectile | yes |
| | `src/entity/objects/effect.ts` | EffectEntity (`effectentity/`) | yes |
| | `src/entity/objects/resource/resource.ts` | Resource (shake/exhausted, `setExhausted`) | yes |
| | `.../resource/impl/tree.ts|rock.ts|foraging.ts|fishspot.ts` | Tree/Rock/Foraging/FishSpot | yes |
| Render | `src/renderer/renderer.ts` | Renderer (tilemap+entity layers, `drawImage`+flip) | yes |
| | `src/renderer/canvas.ts` | Canvas (8 canvas layers + `#border` wrapper div: `background/entities/entities-fore/foreground/entities-mask/cursor/overlay/text-canvas` per `renderer.ts:56-75`, `game.astro:1-12`) | yes |
| | `src/renderer/camera.ts` | Camera (`#border`, follow) | yes |
| | `src/map/map.ts` + `grids.ts` | Map/Grids (regions, collisions, `loadRegions`) | yes |
| Control | `src/controllers/entities.ts` | Entities.create() (`type→prefix`, missing=`console.trace`) | yes — server must obey prefix rule, never send type 6 (Object, no case → log.error) |
| | `src/controllers/sprites.ts` | Sprites.get/load (all Sprite objects instantiated up front; PNG load lazy — see §4) | yes |
| | others | Input/Pointer/HUD/Chat/Menu/Bubble/Audio/Zoning/Info/Joystick | partial — HUD/Menu/Chat are Astro-UI coupled |
| App/Net | `src/game.ts` | Game (boot, teleport, loops) | yes core, drop UI wiring |
| | `src/app.ts` + `components/game.astro` | App/Astro shell (canvas IDs) | split — keep IDs, replace shell |
| | `src/network/socket.ts|connection.ts|messages.ts` | Socket/Connection/Messages (WS `ws://host:port`, bulk `[[id,data]]`, `handleBulkData`) | yes — protocol contract |

## 2. Type table

| Type | File | Shape |
|---|---|---|
| EntityData | `common/types/entity.d.ts:25-57` | `{instance,type,key,name,x,y,movementSpeed?,hitPoints?,maxHitPoints?,attackRange?,level?,orientation?,state?,count?,...}` |
| PlayerData | `common/network/impl/player.ts` | EntityData + `{rank,pvp,mana,maxMana,equipments[]}` |
| EquipmentData | same / `types/slot.d.ts,item.d.ts` | `{type:Equipment enum,key,name,count,enchantments,...}` |
| Modules | `common/network/modules.ts` | EntityType/Equipment/Orientation/Actions enums |
| Packets | `common/network/packets.ts` | Connected0/Handshake1/Welcome3/Map4/Spawn5/List6/Equipment8/Sync10/Movement11/Teleport12/Despawn13/Animation16/Chat19/Resource59 |
| Opcodes | `common/network/opcodes.ts` | Movement{Request0..Speed7}, Equipment{Batch0,Equip1,...}, Login{...}, List{...} |
| SpriteData | `client/data/sprites.json` entries | `{id,width,height,offsetX,offsetY,idleSpeed?,animations:{name:{row,length}}}` |

## 3. Asset tables

Sprites: `public/img/sprites/` ≈1316 PNGs; `data/sprites.json` = 1308 entries (list of `{id,...}`). Prefixes: `player/|mobs/|npcs/|items/|objects/|trees/|rocks/|bushes/|fishspots/|pets/|projectiles/|effectentity/`.

3 verified examples (PIL+file, harness `viewer/img/`):

| id | PNG | Cells | Rows match |
|---|---|---|---|
| `player/base` | 128×384, 32×32, offX −8 | 4 cols × 12 rows (default player layout) | walk_down r3: `(0,96)→(96,96)` loop; Left=`*_right`+flip |
| `mobs/crab` | 256×320, 32×32, off −8,−8, idleSpeed 300 | 8 cols × 10 rows (override, not 9-row mob default) | walk_right r2 `(0,64)→(160,64)`; idle_down r9 len 7 |
| `trees/oak` | 128×288, 64×96, off −24,−64 | 2 cols × 3 rows | idle/shake/exhausted; `state:1`→stump |

Tilesets: `public/img/tilesets/tilesheet-1..6.png`; `data/maps/map.json` 1152×1008 tileSize 16, `tilesets[]` with firstGid/lastGid. Audio: `public/audio/music/` 44 mp3s (beach/desert/…). Interface: `public/img/interface/` (buttons, containers, chatbox, bars, achievements, …) + icons/overlays/flags.

## 4. Load model

Instantiate-all + lazy image load: `SpritesController.load()` (`controllers/sprites.ts:21-40`) instantiates a `Sprite` object for EVERY entry in `sprites.json` (1308) up front (animations parsed, no PNG fetched). PNG fetching is lazy via `Sprite.load()` (`entity/sprite.ts:83-97`, `new Image()` + `load` listener gated on `loaded` flag): only sprites with `preload:true` load at boot; all others load explicitly on demand (first use) plus derived `hurt` (`loadHurtSprite`, mobs/player only) and `silhouette` (`loadSilhouetteSprite`, mobs/player/npcs/trees/rocks/fishspots/bushes) variants once base is loaded. Missing key = trace + skip (`entities.ts:168-171`). Custom server only needs to send keys that exist.

## 5. Reusable vs Astro-UI split

Reuse verbatim: entity tree, renderer/canvas/camera/map/grids, controllers/entities+sprites, network socket/connection/messages, game core loop. Split/replace: `game.astro`/Astro menus/HUD/chat/bank/shop/crafting CSS — keep all 8 canvas IDs + wrapper (`#background #entities #entities-fore #foreground #entities-mask #cursor #overlay #text-canvas` inside `#canvas` inside `#border` per `game.astro:1-12`, `renderer.ts:56-75`), re-skin UI freely. No `packages/*` edits needed; point WS config at Go server.
