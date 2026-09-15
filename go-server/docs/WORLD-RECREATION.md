# World Recreation Guide (custom maps for stock client + Go server)

## 1. Edit in Tiled

Open/create TMX in `packages/tools/map/data/` (template `map_template.json` exists; `map.json` is generated, currently `client/data/maps/map.json`). Keep `tileSize: 16` (px per tile) everywhere — client math, collisions, and `map.json width 1152 × height 1008` assume 16px tiles. Do not confuse with MAP_DIVISION_SIZE=48 (tiles per region side, §2). Paint only with the 6 stock tilesheets (`client/public/img/tilesets/tilesheet-1..6.png`); new GIDs break `firstGid/lastGid` mapping.

## 2. Regions / MAP_DIVISION_SIZE = 48

`common/network/modules.ts:630` → `Constants.MAP_DIVISION_SIZE = 48` (tiles per region side, not 16).
`client/src/map/map.ts:75` → `Utils.sideLength = width / MAP_DIVISION_SIZE`; `client/src/utils/util.ts:280-281`
→ `getRegion(x,y) = floor(x/48) + floor(y/48) * sideLength`. Entities are streamed per region
(`List.Spawns/Positions`, `Who[newIds]`). Keep maps multiples of 48 tiles per side; note spawn x/y in tile
coords (e.g. hero 100,100). Empty regions are legal — entities still render (stub trick).

## 3. Export pipeline

`packages/tools/map/{parser,exporter}` (run `yarn workspace @kaetram/tools map` via tsx, needs `.env`). Outputs: (a) `packages/client/data/maps/map.json` — compressed at runtime via `Utils.compress` + `Utils.getBufferSize` into always-3-element `[4,base64,bufSize]` (`impl/map.ts:9-15`, `packet.ts:25-33`); (b) `server world.json` — copy same regions+collisions+spawns for Go (load once, serve pre-compressed). Regenerate both on every Tiled change; never hand-edit generated JSON.

## 4. Tilesheet constraints

Do not reorder/add/remove tilesheets without updating `map.json tilesets[{firstGid,lastGid,path}]` and re-export. Client renders tiles by GID→tilesheet lookup; wrong ranges = wrong art or blanks. Stick to 6 files; custom art goes in as new sprites (below), not tiles.

## 5. Adding a new sprite

1. Add PNG under `client/public/img/sprites/<prefix>/<key>.png` (prefix by EntityType — never type 6/Object, un-spawnable with no case in entities.ts:78-163: `mobs|npcs|items|objects|trees|rocks|bushes|fishspots|pets|player|projectiles|effectentity`).
2. Append entry to `client/data/sprites.json` (list, 1308 entries): `{"id":"<prefix>/<key>","width":..,"height":..,"offsetX":..,"offsetY":..,"animations":{"idle_down":{"row":N,"length":M},...}}`. Row math: `frame.y=row*height`, `frame.x=index*width`; Left uses `*_right` row flipped.
3. Spawn with exact prefix rule (`entities.ts`): Mob `type:3,key:'crab'` → `mobs/crab`; Tree `type:10` → `trees/` + `state`; missing key = `console.trace`, invisible. Verify via harness `viewer/` (copy PNG, log frames) before wiring server spawns.
