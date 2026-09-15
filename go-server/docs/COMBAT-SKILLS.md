# COMBAT-SKILLS — 21×3 COMBAT only (cap 20)

Simple, make it work, cap 20: Lv1/10/20, mana≤20, cd≤20s, real assets only. FX=`Modules.Effects`; proj=`projectiles/*`; asset=`mobs/*` pet / `lava` effectentity / buffs via `drawEffects` (`character.ts`). No professions.
Line: `Skill — Lv | mana/cd | FX + proj + asset | overlay / projectile / trick`.
All FX below exist in BOTH `character.ts` effects map AND `sprites.json` (visible via `drawEffects`): Critical, Stun, Burning, Freezing, Healing, Fireball/Iceball/Poisonball, Boulder, Strength/Defense/Magic/Archery Buffs (+Super), Terror/TerrorStatus. Movement-only skills carry no overlay.

## 1. Warrior (cleave)
- Slash — Lv1 | 2mp/8s | Critical + proj:— + asset:— | Critical flash on victim, no projectile, lunge.
- Iron Guard — Lv10 | 4mp/15s | DefenseBuff + proj:— + asset:— | DefenseBuff ring (drawEffects), brace.
- Warbanner — Lv20 | 6mp/20s | StrengthSuperBuff + proj:— + asset:— | SuperBuff glow (drawEffects), raise sword.

## 2. Knight (tank)
- Shield Oath — Lv1 | 2mp/10s | DefenseBuff + proj:— + asset:— | DefenseBuff overlay (drawEffects), kneel.
- Smite — Lv10 | 4mp/12s | Stun + boulder + asset:— | Stun stars on victim, boulder arcs down, slam.
- Aegis — Lv20 | 7mp/20s | DefenseSuperBuff + proj:— + asset:— | DefenseSuperBuff dome (drawEffects), hold ground. (was Invincible — no overlay; Super defense keeps tank-wall intent, visible)

## 3. Berserker (suicide DPS)
- Rage — Lv1 | 2mp/8s | StrengthBuff + proj:— + asset:— | StrengthBuff red overlay (drawEffects), shake.
- Frenzy — Lv10 | 4mp/12s | Critical + proj:— + asset:— | Critical flash on victim, double spin.
- Bloodlust — Lv20 | 7mp/20s | Burning + bloodball + asset:— | Burning overlay on victim (drawEffects), bloodball point-blank, dash. (was Bleed — no overlay; perpetual burn keeps DoT-drip intent, visible)

## 4. Duelist (burst)
- Feint — Lv1 | 2mp/8s | Critical + proj:— + asset:— | Critical flash on victim (drawEffects), sidestep. (was AccuracyBuff — sprite missing; crit keeps precision-strike intent, visible)
- Riposte — Lv10 | 4mp/12s | Critical + proj:— + asset:— | Critical flash on victim, thrust after parry.
- Deathmark — Lv20 | 6mp/20s | Critical+Terror + proj:— + asset:— | Critical flash + Terror face on victim (drawEffects), point blade. (was DualistsMark — no overlay; keeps mark-of-death intent, visible)

## 5. Lancer (reach)
- Jab — Lv1 | 2mp/8s | Critical + proj:— + asset:— | Critical flash on victim (drawEffects), spear poke. (was AccuracyBuff — sprite missing; crit keeps precision-poke intent, visible)
- Impale — Lv10 | 4mp/12s | Critical + arrow + asset:— | Critical flash on victim, arrow flies straight, charge. (was Bleed — no overlay; crit keeps pierce-burst intent, visible)
- Phalanx — Lv20 | 6mp/20s | DefenseBuff + proj:— + asset:— | DefenseBuff wall (drawEffects), plant spear.

## 6. Warden (spear tank)
- Brace — Lv1 | 2mp/8s | DefenseBuff + proj:— + asset:— | DefenseBuff overlay (drawEffects), crouch.
- Sentinel — Lv10 | 5mp/15s | DefenseBuff + proj:— + asset:— | DefenseBuff ring (drawEffects), stand firm. (was ThickSkin — no overlay; keeps wall intent, visible)
- Unbreakable — Lv20 | 7mp/20s | DefenseSuperBuff + proj:— + asset:— | SuperBuff dome (drawEffects), root.

## 7. Reaper (scythe AoE)
- Reap — Lv1 | 3mp/8s | Burning + proj:— + asset:— | Burning arc on victims (drawEffects), sweep scythe. (was Bleed — no overlay; burn keeps AoE-DoT intent, visible)
- Harvest — Lv10 | 5mp/15s | Poisonball + poisonarrow + asset:— | Poisonball cloud, poisonarrow flies, reap.
- Doom — Lv20 | 7mp/20s | TerrorStatus + terror + asset:— | TerrorStatus skull, terror wisp flies, raise arms.

## 8. Executioner (finisher)
- Chop — Lv1 | 2mp/8s | StrengthBuff + proj:— + asset:— | StrengthBuff overlay (drawEffects), overhead chop.
- Sunder — Lv10 | 5mp/15s | Critical + boulder + asset:— | Critical flash, boulder drops, smash.
- Execute — Lv20 | 7mp/20s | StrengthSuperBuff + proj:— + asset:— | SuperBuff glow (drawEffects), leap down. (was AccuracySuperBuff — sprite missing; Super strength keeps finisher-tier intent, visible)

## 9. Woodsman (brawler)
- Wild Swing — Lv1 | 2mp/8s | Critical + proj:— + asset:— | Critical flash on victim, wide axe swing.
- Barkskin — Lv10 | 4mp/15s | DefenseBuff + proj:— + asset:— | DefenseBuff overlay (drawEffects), beat chest. (was ThickSkin — no overlay; keeps tank-up intent, visible)
- Timberfall — Lv20 | 6mp/20s | Boulder + boulder + asset:— | Boulder impact, boulder flies, hurl.

## 10. Ranger (bow)
- Aimed Shot — Lv1 | 2mp/8s | ArcheryBuff + arrow + asset:— | ArcheryBuff (drawEffects), arrow flies true, steady.
- Volley — Lv10 | 4mp/15s | Critical + arrow×3 + asset:— | Critical flash, 3-arrow fan, jump back.
- Bullseye — Lv20 | 6mp/20s | ArcherySuperBuff + firearrow + asset:— | SuperBuff (drawEffects), firearrow streaks, kneel.

## 11. Hunter (bow + wolf)
- Track — Lv1 | 2mp/10s | movement-only + proj:— + pet:wolf | No overlay (was Running — no overlay); wolf sprints beside via Movement packets, dash trick.
- Snare — Lv10 | 4mp/15s | Stun + arrow + pet:wolf | Stun stars, arrow flies, wolf lunges.
- Pack Hunt — Lv20 | 6mp/20s | ArcherySuperBuff + lightningarrow + pet:wolf | SuperBuff (drawEffects), lightningarrow flies, wolf bites.

## 12. Mage (elemental)
- Spark — Lv1 | 2mp/8s | Fireball + purplebolt + asset:— | Fireball blast, purplebolt flies, staff thrust.
- Storm — Lv10 | 4mp/15s | Fireball + yellowlightning + asset:— | Fireball overlay, yellowlightning flies, staff spin.
- Overgrowth — Lv20 | 6mp/20s | Poisonball + greenbolt + asset:— | Poisonball cloud, greenbolt flies, staff slam.

## 13. Stormcaller (lightning)
- Zap — Lv1 | 2mp/8s | Fireball + yellowlightning + asset:— | Fireball spark, yellowlightning flies, jab.
- Static — Lv10 | 4mp/12s | Stun + lightningarrow + asset:— | Stun stars, lightningarrow flies, channel.
- Tempest — Lv20 | 7mp/20s | MagicSuperBuff + yellowlightning + zone:lava | SuperBuff (drawEffects), lightning falls, lava burns 5s, GO-NEW-LOGIC lava DoT (Go timer + Heal/Combat packets, no client change).

## 14. Tidemage (sustain)
- Splash — Lv1 | 2mp/8s | Iceball + purplebolt + asset:— | Iceball splash, purplebolt flies, wave staff.
- Tide — Lv10 | 4mp/15s | Healing + proj:— + asset:— | Healing cross (drawEffects), lift staff.
- Deluge — Lv20 | 6mp/20s | MagicBuff + iceball + asset:— | MagicBuff (drawEffects), iceball flies, flood.

## 15. Druid (nature + bear)
- Thorn — Lv1 | 2mp/8s | Poisonball + greenbolt + asset:— | Poisonball thorns (drawEffects), greenbolt flies, gesture. (was Bleed — no overlay; poison keeps nature-DoT intent, visible)
- Regrow — Lv10 | 4mp/15s | Healing + proj:— + pet:whitebear | Healing (drawEffects), whitebear rears beside.
- Maul — Lv20 | 6mp/20s | StrengthBuff + proj:— + pet:whitebear | StrengthBuff (drawEffects), whitebear mauls with you.

## 16. Necromancer (debuff + summon)
- Hex — Lv1 | 3mp/10s | Terror + purplebolt + asset:— | Terror face, purplebolt flies, curse.
- Rot — Lv10 | 5mp/15s | Poisonball + poisonarrow + asset:— | Poisonball rot, poisonarrow flies, spread arms.
- Summon Dead — Lv20 | 7mp/20s | TerrorStatus + terror + pet:skeleton | Terror overlay + terror2 rise FX, skeleton pet 20s HP50 atk5 follows+attacks target. GO-NEW-LOGIC: Go server authoritative (not bound by Node pet.ts cosmetic-only) — Go makes skeleton follow+attack via Movement+Combat packets; no client change (pets already render+move).

## 17. Venomancer (poison + spider)
- Sting — Lv1 | 2mp/8s | Poisonball + poisonarrow + asset:— | Poisonball splash, poisonarrow flies, flick.
- Web — Lv10 | 4mp/15s | Stun + arrow + pet:spider | Stun web, arrow flies, spider skitters to victim.
- Plague — Lv20 | 6mp/20s | Poisonball + greenbolt + pet:spider | Poisonball cloud, greenbolt flies, spider swarms.

## 18. Serpent Caller (spear + snek)
- Hiss — Lv1 | 2mp/8s | Terror + proj:— + pet:snek | Terror face (drawEffects), snek coils beside. (was AccuracyBuff — sprite missing; terror keeps serpent-menace intent, visible)
- Coil — Lv10 | 4mp/15s | Stun + proj:— + pet:snek | Stun coil, snek wraps victim, jab.
- Venom — Lv20 | 6mp/20s | Poisonball + poisonarrow + pet:snek | Poisonball fangs (drawEffects), poisonarrow flies, snek bites. (was Bleed — no overlay; poison keeps venom intent, visible)

## 19. Shadowblade (assassin + clone)
- Blink — Lv1 | 3mp/10s | movement-only + proj:— + asset:— | No overlay (was Running — no overlay); vanish-dash (Teleport burst), speed is server state.
- Backstab — Lv10 | 4mp/15s | Critical + proj:— + asset:— | Critical flash, appear behind target.
- Assassinate — Lv20 | 7mp/20s | Critical+Terror + iceball + pet:ghost | Critical flash + Terror face on victim (drawEffects), iceball flies, ghost clone strikes behind. (was DualistsMark — no overlay; keeps mark-for-death intent, visible)

## 20. Engineer (turret)
- Deploy — Lv1 | 3mp/12s | DefenseBuff + proj:— + pet:vulture | DefenseBuff plate ring (drawEffects), vulture-reskin turret drops, speed 0. (was ThickSkin — no overlay) GO-NEW-LOGIC: Go spawns stationary pet via Spawn packet; no client change.
- Overclock — Lv10 | 4mp/15s | ArcheryBuff + arrow + pet:vulture | ArcheryBuff gears (drawEffects), turret fires arrow, crank. GO-NEW-LOGIC: Go drives turret shots via Combat packets; no client change.
- Barrage — Lv20 | 7mp/20s | Boulder + boulder + pet:vulture | Boulder impacts, turret volleys boulders, duck. GO-NEW-LOGIC: Go drives turret volleys via Combat packets; no client change.

## 21. Ember Cultist (fire w/o firestaff)
- Cinder — Lv1 | 2mp/8s | Burning + firearrow + asset:— | Burning embers, firearrow flies, fan flames.
- Immolate — Lv10 | 4mp/15s | Burning + firearrow + asset:— | Burning burst (drawEffects), firearrow flies, swing. (was FirePotion — no overlay; burn keeps immolate intent, visible)
- Ashfall — Lv20 | 6mp/20s | Burning + fireball + zone:lava | Burning cinders + fireball impact, lava puddle burns 5s, kneel. GO-NEW-LOGIC lava DoT (Go timer + Heal/Combat packets, no client change).

## Implementation notes (GO-NEW-LOGIC)
- Go server is authoritative (not bound by Node pet.ts cosmetic-only): skeleton pet follow+attack (Summon Dead), turret spawn/fire/volley (Deploy/Overclock/Barrage) all work via Spawn/Movement/Combat packets with zero client changes — pets already render+move.
- Lava DoT zones (Tempest, Ashfall): Go timer ticks damage via Heal/Combat packets while `lava` effectentity (effectentities.json, duration 5000) renders the puddle; no client change.
- Substitution map (invisible → visible, design intent kept): Invincible→DefenseSuperBuff (Aegis), Bleed→Burning (Bloodlust, Reap) / →Critical (Impale) / →Poisonball (Thorn, Venom), AccuracyBuff→Critical (Feint, Jab) / →Terror (Hiss), AccuracySuperBuff→StrengthSuperBuff (Execute), DualistsMark→Critical+Terror (Deathmark, Assassinate), ThickSkin→DefenseBuff (Sentinel, Barkskin, Deploy), Running→movement-only no overlay (Track, Blink), FirePotion→Burning (Immolate).
