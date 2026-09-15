# CLASS-DESIGN — Level Cap 30 (MAX_LEVEL 120 → 30)

Melee pools use only grepped keys (spears bronze L5/iron L10/cobalt L15/gold L20; swords copper/tin starter → bronze L5 → iron L10 → nisoc L15 → gold L20 + icesword L17; rapiers L25 only — NO daggers exist); archer 17 bows (ALL level-less, cosmetic) + 9 arrows; magic 4 staves (aqua starter → lightning L10 → nature L17 → fire L25; curse L35 EXCLUDED); FX: Effects enum 30 incl None (29 usable) + 32 projectile sprites + 8 abilities + 19 Skills incl 2 pseudo + 12 AttackStyles + 6 DamageStyles (None/Crush/Slash/Stab/Magic/Archery); mobs 24/148 ≤30; resources 26/20/5/11; crafting 94/7; stores 7; quests 49 (21 active).

Every key below verified via `rg '"<key>":' packages/server/data/items.json` (missing `level` = no gate = starter). Enums from `packages/common/network/modules.ts`; abilities from `packages/server/data/abilities.json`.

## 1. Eight classes

1. **Warrior** — Pool: `coppersword` starter, `bronzesword` L5, `ironsword` L10, `cobaltaxe` L15. Look: leather → bronze/iron → cobalt/gold plate. Core: Slash + NEW-PROPOSAL cleave kit; secondary: Mining. Sig: `Effects.Burning` + `fireball` (Flame Cleave, NEW PROPOSAL). Role: frontline cleave/tank.
2. **Knight-Paladin** — Pool: `tinsword` starter, `bronzespear` L5, `ironspear` L10, `goldspear` L20 (alt `goldsword` L20). Look: knight/paladin/samurai plate + shield. Core: real `thickskin` + NEW-PROPOSAL smite; secondary: Smithing. Sig: `Effects.Healing` (no holy projectile exists; Judgment FX NEW PROPOSAL). Role: tank/support, quest-escort hook.
3. **Rogue-Assassin** — NO `*dagger*` keys exist (grep = zero hits), no `ironrapier`/amethyst dagger — Pool: `coppersword`, `bronzesword` L5, `ironsword` L10, `icesword` L17, `adeptsrapier`/`royalrapier` L25 (weaponType sword). Look: leather + hoods. Core: NEW-PROPOSAL Backstab/Poison (`Effects.Poisonball` + `Bleed`); secondary: Foraging. Sig: `Poisonball` + `poisonarrow`. Role: burst single-target, flanking.
4. **Archer-Ranger** — Pool: `woodenbow` (= `boweggplant`), `bowbamboo`, `bowpine` (+14 more; ALL 17 bows level-less → cosmetic, NO gates) + 9 arrows (`arrow`, `firearrow` burning, `poisonarrow` poisonous, `lightningarrow`, `icearrow`/`nisocarrow`/`cinnabararrow`/`pythararrow`/`iboarrow` freezing; archeryyk 7→28). Look: leather + cloaks. Core: NEW-PROPOSAL Aimed/Volley; secondary: Woodcutting. Sig: `Effects.Boulder` + `arrow`. Role: ranged DPS; arrows consumed per shot (real decrement).
5. **Mage-Elementalist** — Pool: `aquastaff` starter (manaCost 2, range 9, `purplebolt`), `lightningstaff` L10 (4, `yellowlightning`), `naturestaff` L17 (6, `greenbolt`), `firestaff` L25 (7, `fireball`). Look: wizard hats/robes (no defense). Core: Fireball/Frost Nova (NEW PROPOSAL kit, see §3); secondary: Foraging/Alchemy. Sig: `Effects.Fireball`/`Iceball` + `fireball`/`iceball`. Role: AoE burst, range 9.
6. **Necro-Curse (capped)** — Pool: `aquastaff`, `naturestaff` L17; `cursestaff` L35 ("Ice Staff", `terror`) EXCLUDED. (Real key `magichamberge`, L20, no projectileName, is off-ladder and excluded per spec.) Look: dark robes + skull. Core: NEW-PROPOSAL Terror/Drain on `Effects.Terror`/`TerrorStatus` + `terror` (FX-only at 30); secondary: Fishing. Role: capped debuff/dot; full unlock only if cap rises.
7. **Woodsman-Gatherer** — Pool: `bronzebattleaxe` L1, `ironaxe` L10, `cobaltaxe` L15, `cobaltspear` L15 (pickaxes bronze/iron/gold/cobalt all level-less → utility, no gates). Look: leather + lumberjack. Core: NEW-PROPOSAL Chop/Mine; secondary: Bow-craft. Sig: `Effects.Boulder` + `boulder`. Role: trees/rocks/fishing/foraging engine; feeds Crafter.
8. **Crafter-Smith** — Pool: `bronzepickaxe`/`ironpickaxe`/`goldpickaxe` (level-less), `goldsword` L20, `smithshammer` L25 (no "Bronze Hammer" key). Look: apron over plate. Core: real-ability frame + NEW-PROPOSAL Smelt/Forge; secondary: Shopkeep. Sig: `AccuracyBuff`-family (see §3). Role: 94 recipes/7 tables; store/quest hook.

Skills: 19-member enum incl pseudo `Chiseling`, `Smelting` ("Not a skill" in source); AttackStyles 12; DamageStyles 6 (above).

## 2. Skill trees + 30-level progression

Triples (2 combat + 1 gather/craft) are NEW PROPOSAL, not implemented — mapped onto the 8 real abilities (`intimidate`/`run`/`dualistsmark`/`hotshot`/`thickskin` actives: mana 15–21, cd 60000ms; `awareness`/`precognition`/`secretcalling` passives): Warrior Slash/Whirlwind+Mining; Knight `thickskin`/Smite+Smithing; Rogue Backstab/Poison+Foraging; Archer Aimed/Volley+Woodcutting; Mage Fire/Frost+Alchemy; Necro Terror/Drain+Fishing; Woodsman Chop/Mine+Tracking; Crafter Smelt/Forge+Haggle.

| Tier | Weapon (real keys) | Armor | Spell/Ability (of 8 real) | Recipe hook (of 94/7 tables) |
|---|---|---|---|---|
| 1 | copper/tin swords, `aquastaff`, any bow (gateless), `bronzebattleaxe` | leather, wizard cloth | basic atk, 1 opener | plank/copper ingot, starter quest (of 21) |
| 5 | `bronzesword`, `bronzespear` | bronze L5 | AoE/doT #1 (NEW PROPOSAL) | bronze gear, arrow bundle |
| 10 | `ironsword`, `ironspear`, `lightningstaff` | iron L10 | signature FX, real `run`/`hotshot` | iron gear |
| 15 | `nisocsword`, `cobaltspear`, `cobaltaxe` | cobalt L15 | elite passive (NEW PROPOSAL), 2nd ability | cobalt gear, mid quest chain |
| 17 | `icesword`, `naturestaff` | — (off-tier spike) | Frost/Nature unlock | mid-game consumables |
| 20 | `goldsword`, `goldspear`, `cinnabarsword` | gold L20 | ult #1 (NEW PROPOSAL) | gold gear, store unlock (of 7) |
| 25 | `firestaff`, `adeptsrapier`/`royalrapier`, `smithshammer` | amethyst L25 | ult #2 (NEW PROPOSAL) | amethyst gear, endgame consumables |
| 30 | BiS ≤30 (no curse) | amethyst+ L25-30 | mastery buff (NEW PROPOSAL) | BiS craft, capstone quest |

Mage/Necro follow staff ladder aqua(starter)/10/17/25; melee follow sword ladder starter/5/10/15(`nisocsword`)/20 + `icesword` L17; spears 5/10/15/20. Bows: no ladder (cosmetic). Pickaxes: no ladder (utility).

## 3. Spells (magic classes) — real Effects enum + real projectileNames

Real `Effects` members: `Fireball`, `Iceball`, `Poisonball`, `Boulder`, `Terror`(+`TerrorStatus`), `Healing`, `Stun`, `Burning`, `Freezing`, `Bleed`, `Accuracy/Strength/Defense/Magic/ArcheryBuff`+Super. Real `projectileName`s: `fireball`, `iceball`, `terror`, `yellowlightning`, `greenbolt`, `purplebolt`, `arrow`, `fire/poison/lightning/ice/nisoc/cinnabar/pythar/iboarrow`. Staff manaCost 2/4/6/7; ability mana 15–21, cd 60s. Kit mapping NEW PROPOSAL (no spell system implements these):

- Fireball: `Fireball` + `fireball` (fired by `firestaff` L25, manaCost 7); main nuke.
- Frost Nova: `Iceball` + `Freezing` + `iceball`; slow (no staff fires `iceball` by default).
- Poison Cloud: `Poisonball` + `poisonarrow` (real `poisonous` flag); DoT.
- Boulder: `Boulder` + `boulder` (`projectiles/boulder`); knockback.
- Terror: `Terror`/`TerrorStatus` + `terror` (only `cursestaff` L35 fires it → FX-only at 30; real `Hits.Terror`).
- Drain (capped): `Bleed` + `purplebolt` via `aquastaff`; NEW PROPOSAL.
- Heal: `Healing`, no proj.
- Buffs: real Buff/SuperBuffs via `thickskin`/`dualistsmark`/`hotshot`/`run`; wiring NEW PROPOSAL.

## 4. Mob curve 1-30 + downscale reuse

Existing 24 (1-10:4, 11-20:12, 21-30:8; per-5 bands 2/2/6/6/4/4). Thin 26-30 → 6 downscaled elites reusing 31+ art — NEW PROPOSAL, not implemented (sketch HP/DMG ×0.35, XP ×0.5, no 31+ drops):

1. Fallen Knight (31+ knight art) → L27 elite; 2. Cave Horror (31+ horror) → L28; 3. Ember Imp (fire mob) → L26; 4. Thorn Golem (golem) → L29; 5. Plague Bat (flyer) → L26; 6. Drowned Captain (boss art) → L30 capstone. Keep sprites/projectiles, only numbers + name prefix "Lesser".

## 5. Balance

- Range: magic 9 (real staff `attackRange`) vs arrow-driven bows — mage outranges, cloth 0-def vs leather.
- Arrows: 9 types consumed per shot (real decrement); `firearrow` burning, ice/nisoc/cinnabar/pythar/ibo freezing, `poisonarrow` poisonous; archeryyk 7→28; Volley ×3 arrows (NEW PROPOSAL) balances vs manaCost.
- Mana: staves 2/4/6/7, actives 15–21 / 60s cd; melee free but positional (AttackStyles) keeps parity.
- Curse excluded: `cursestaff` L35 > 30 → Necro caps at `naturestaff` L17 + `aquastaff`, Terror/Drain weak (NEW PROPOSAL), tooltip "Sealed beyond 30". Full Necro returns if MAX_LEVEL 120 restored.
- Economy: 94 recipes/7 tables by tiers above; 7 stores at L1/10/20; 21 active quests (of 49), one fantasy each.

## Verify (evidence, 2026-09-15, repo untouched)

- All 31 pool keys hit `rg '"<key>":' items.json` (levels from entry `level`; absent = starter): spears bronze5/iron10/cobalt15/gold20; swords copper/tin starter/bronze5/iron10/nisoc15/gold20/cinnabar20/ice17; staves aqua starter/lightning10/nature17/fire25/curse35-excl; bows woodenbow/bowbamboo/bowpine/boweggplant (level-less); rapiers L25; axes battlebronze1/iron10/cobalt15/gold20; smithshammer25; pickaxes bronze/iron/gold (level-less).
- Zero hits: `*dagger*` keys; spellings `magichamberge`/`shortbow`/`bronzebow`/`ironbow`/`goldbow`/`ironrapier` (real near-miss `magichamberge` L20, no proj, off-ladder, excluded).
- Counts grepped: bows 17 / arrows 9; Skills 19 (pseudo Chiseling/Smelting); Effects 30 incl None (spec said 31 — grep says 30, doc uses 30); AttackStyles 12; DamageStyles 6; abilities 8.
