# CLASS-DESIGN V2 — Level Cap 20 (simple, make it work)

Cap 20 cuts everything above: `firestaff` L25 and `cursestaff` L35 ("Ice Staff") excluded; Mage tops at `naturestaff` L17. All keys verified via `rg '"<key>":' packages/server/data/items.json` (no `level` = starter). Effects from `packages/common/network/modules.ts` (30 members incl None); bows all gateless = cosmetic; `ARCHER_ATTACK_RANGE: 8` (modules.ts:644) vs staff `attackRange: 9`. No daggers exist (zero `*dagger*` hits) — Rogue-types use swords.

## 21 classes (1 line each: role / weapon / armor / 3 unique skills Lv1-Lv10-Lv20 with Effect+proj+mana/cd)

1. **Warrior** — frontline cleave. W: `coppersword`(—)/`bronzesword`(5)/`ironsword`(10). A: bronze set. Skills: Slash(`Critical`/—/m2/cd8s); Iron Guard(`DefenseBuff`/—/m4/cd30s); Warbanner(`StrengthSuperBuff`/—/m6/cd60s).
2. **Knight** — tank/escort. W: `tinsword`(—)/`bronzespear`(5)/`ironspear`(10). A: `knighthelmet`(1)+bronze. Skills: Shield Oath(`DefenseBuff`/—/m2/cd15s); Smite(`Stun`/`boulder`/m4/cd25s); Aegis(`Invincible`/—/m7/cd60s).
3. **Berserker** — suicide DPS. W: `bronzebattleaxe`(1)/`ironbattleaxe`(10)/`cobaltbattleaxe`(15). A: iron set. Skills: Rage(`StrengthBuff`/—/m2/cd12s); Frenzy(`Critical`/—/m4/cd20s); Bloodlust(`StrengthSuperBuff`/`boulder`/m7/cd60s).
4. **Duelist** — single-target burst. W: `tinsword`(—)/`ironsword`(10)/`nisocsword`(15). A: leather set. Skills: Feint(`AccuracyBuff`/—/m2/cd10s); Riposte(`Critical`/—/m4/cd20s); Deathmark(`DualistsMark`/—/m6/cd45s).
5. **Lancer** — reach/stab. W: `bronzespear`(5)/`ironspear`(10)/`cobaltspear`(15). A: bronze set. Skills: Jab(`AccuracyBuff`/—/m2/cd8s); Impale(`Bleed`/`arrow`/m4/cd20s); Phalanx(`DefenseBuff`/—/m6/cd45s).
6. **Warden** — spear tank. W: `bronzespear`(5)/`cobaltspear`(15)/`goldspear`(20). A: gold set. Skills: Brace(`DefenseBuff`/—/m2/cd12s); Sentinel(`ThickSkin`/—/m5/cd30s); Unbreakable(`DefenseSuperBuff`/—/m7/cd60s).
7. **Reaper** — scythe AoE. W: `bronzescythe`(5)/`ironscythe`(10)/`cobaltscythe`(15). A: cobalt set. Skills: Reap(`Bleed`/—/m3/cd10s); Harvest(`Poisonball`/`poisonarrow`/m5/cd25s); Doom(`TerrorStatus`/`terror`/m7/cd60s).
8. **Executioner** — axe finisher. W: `bronzeaxe`(—)/`ironaxe`(10)/`goldaxe`(20). A: gold set. Skills: Chop(`StrengthBuff`/—/m2/cd10s); Sunder(`Critical`/`boulder`/m5/cd25s); Execute(`AccuracySuperBuff`/—/m7/cd50s).
9. **Woodsman** — gather/utility. W: `bronzebattleaxe`(1)/`ironaxe`(10)/`cobaltaxe`(15). A: leather set. Skills: Timber(`StrengthBuff`/—/m2/cd8s); Forage(`Healing`/—/m3/cd20s); Old Growth(`DefenseBuff`/—/m5/cd40s).
10. **Ranger** — bow DPS (cosmetic bows + real arrows). W: `woodenbow`/`bowbamboo`/`bowpine` (all gateless). A: leather set. Skills: Aimed Shot(`ArcheryBuff`/`arrow`/m2/cd8s); Volley(`Critical`/`arrow` x3/m4/cd20s); Bullseye(`ArcherySuperBuff`/`firearrow`/m6/cd45s).
11. **Hunter** — bow + wolf pet. W: `woodenbow`/`bowocean`/`bowsky`. A: leather set. Skills: Track(`Running`/—/m2/cd15s); Snare(`Stun`/`arrow`/m4/cd25s); Pack Hunt(`ArcherySuperBuff`/`lightningarrow`/m6/cd50s).
12. **Mage** — top cap-20 caster. W: `aquastaff`(—,m2,`purplebolt`)/`lightningstaff`(10,m4,`yellowlightning`)/`naturestaff`(17,m6,`greenbolt`). A: leather set. Skills: Spark(`Fireball`/`purplebolt`/m2/cd8s); Storm(`Fireball`/`yellowlightning`/m4/cd20s); Overgrowth(`Poisonball`/`greenbolt`/m6/cd45s).
13. **Stormcaller** — lightning spec. W: `aquastaff`(—)/`lightningstaff`(10). A: leather set. Skills: Zap(`Fireball`/`yellowlightning`/m2/cd8s); Static(`Stun`/`lightningarrow`/m4/cd20s); Tempest(`MagicSuperBuff`/`yellowlightning`/m7/cd60s).
14. **Tidemage** — water sustain. W: `aquastaff`(—)/`lightningstaff`(10). A: leather set. Skills: Splash(`Iceball`/`purplebolt`/m2/cd8s); Tide(`Healing`/—/m4/cd25s); Deluge(`MagicBuff`/`iceball`/m6/cd50s).
15. **Druid** — nature + bear. W: `aquastaff`(—)/`naturestaff`(17). A: leather set + `bearhelm`(5). Skills: Thorn(`Bleed`/`greenbolt`/m2/cd10s); Regrow(`Healing`/—/m4/cd25s); Maul (`StrengthBuff`/—/m6/cd40s, bear strikes with you).
16. **Necromancer** — capped debuff (no curse staff). W: `aquastaff`(—)/`naturestaff`(17). A: `skeletonhelm`(1)+leather. Skills: Hex(`Terror`/`purplebolt`/m3/cd12s); Rot(`Poisonball`/`poisonarrow`/m5/cd25s); Grave Pact(`TerrorStatus`/`terror`/m7/cd60s, FX-only, weak at 20).
17. **Venomancer** — poison DoT + spider. W: `aquastaff`(—)/`naturestaff`(17). A: leather set. Skills: Sting(`Poisonball`/`poisonarrow`/m2/cd8s); Web(`Stun`/`arrow`/m4/cd25s); Plague(`Poisonball`/`greenbolt`/m6/cd50s).
18. **Serpent Caller** — spear + snek. W: `bronzespear`(5)/`ironspear`(10). A: bronze set. Skills: Hiss(`AccuracyBuff`/—/m2/cd10s); Coil(`Stun`/—/m4/cd25s); Venom (`Bleed`/`poisonarrow`/m6/cd45s, snek bites).
19. **Shadowblade** — assassin + clone. W: `ironsword`(10)/`icesword`(17). A: leather set. Skills: Blink(`Running`/—/m3/cd15s); Backstab(`Critical`/—/m4/cd20s); Assassinate(`DualistsMark`/`iceball`/m7/cd60s, clone strikes from behind).
20. **Engineer** — turret pet. W: `bronzesword`(5)/`ironsword`(10). A: iron set. Skills: Deploy(`ThickSkin`/—/m3/cd20s); Overclock(`ArcheryBuff`/`arrow`/m4/cd25s); Barrage(`Boulder`/`boulder`/m7/cd60s, turret fires).
21. **Ember Cultist** — fire fantasy without `firestaff` (L25 cut). W: `woodenbow` + `firearrow` (burning). A: leather set. Skills: Cinder(`Burning`/`firearrow`/m2/cd10s); Immolate(`FirePotion`/`firearrow`/m4/cd25s); Ashfall(`Burning`/`fireball`-sprite FX/m6/cd50s, puddle burns under target).

## Avatar/pet tricks (1 line each, all sprites exist in packages/client/data/sprites.json)

- Serpent: reskin pet to `mobs/snek` (exists, L16 mob).
- Bear: reskin pet to `mobs/whitebear` (exists).
- Wolf: reskin pet to `mobs/wolf` (exists; `mobs/snowwolf`/`darkwolf` alts).
- Spider: reskin pet to `mobs/spider` (exists; `babyspider`/`poisonspider` alts).
- Skeleton: player skin `player/skin/skeletonskin` + `skeletonhelm` (both exist).
- Turret: stationary pet entity, speed 0, ranged `arrow` attacks (pet system + `pet.d.ts` owner field, real).
- Clone: spawn pet copy of player sprite + server Teleport-back/speed burst (`Running` effect, real).
- Puddle: `lava` effectEntity (real, `effectentities.json`, duration 5000) placed at target tile as DoT zone.

## Shared level beats 1/5/10/15/20

| Lv | Weapon tier (real) | Armor tier (real) | Spell | Recipe hook |
|---|---|---|---|---|
| 1 | copper/tin, `aquastaff`, any bow (gateless), `bronzebattleaxe` | leather, `knighthelmet`/`skeletonhelm` | Lv1 core | plank/copper ingot, starter quest |
| 5 | `bronzesword`/`bronzespear`/`bronzescythe` | bronze L5, `bearhelm` | — | bronze gear, arrow bundle |
| 10 | `ironsword`/`ironspear`/`ironscythe`/`ironaxe`, `lightningstaff` | iron L10 | Lv10 spec | iron gear |
| 15 | `nisocsword`/`cobaltspear`/`cobaltscythe`/`cobaltaxe` | cobalt L15 | — | cobalt gear, mid quest chain |
| 17 | `icesword`, `naturestaff` (off-tier spike, mage/assassin only) | — | — | mid consumables |
| 20 | `goldsword`/`cinnabarsword`/`goldspear`/`goldscythe`/`goldaxe` | gold L20 | Lv20 ult | gold gear, store unlock |

## Mob bands 1-20 (16 real mobs ≤20, from mobs.json)

- 1-5: `rat` 1, `crab` 1, `bat` 4. 6-10: `goblin` 7. 11-15: `wizard` 11, `pierrot` 12, `smalldevil` 13, `skeleton` 14, `hermitcrab` 15. 16-20: `snek`/`cactus`/`vulture`/`paladin` 16, `ogre` 18, `ghostrider` 19, `ironogre` 20.
- 21-30 content cut: 8 existing mobs (L21-30 band) excluded from V2; if a filler band is needed later, downscale (HP/DMG x0.35, XP x0.5, prefix "Lesser", no 21+ drops) reusing their art — NEW PROPOSAL, not implemented.

## Balance

Magic range 9 (real staff `attackRange`) beats bow range 8 (`ARCHER_ATTACK_RANGE`, modules.ts:644), but mages wear 0-def cloth (leather set = flavor, no scaling) while archers pay per shot — all 9 arrows (`arrow`…`iboarrow`) are consumable with real decrement, so Volley-style multi-shot costs 3 arrows vs a mage's flat mana (staff costs 2/4/6, class skills 2-7, actives cd 8-60s); melee pays nothing but must close distance, which keeps parity simple: range costs mana/arrows, melee costs positioning.

## Verify (2026-09-15, repo untouched)

- Swords: copper/tin starter, bronze5/iron10/nisoc15/gold20/cinnabar20/ice17 (`peddlepounder`20 ignored, joke item). Spears 5/10/15/20. Axes/scys: bronze(—/1)/iron10/cobalt15/gold20. Staves aqua—/lightning10/nature17/fire25-cut/curse35-cut. Bows 17 keys all level-less. Arrows 9 real. Armor leather—/bronze5/iron10/cobalt15/gold20 + knighthelmet1/skeletonhelm1/bearhelm5 (wolfhelmet29 cut). Effects 30 incl None. Pet/lava/snek/whitebear/wolf/spider/skeleton sprites + `ARCHER_ATTACK_RANGE: 8` grepped.
