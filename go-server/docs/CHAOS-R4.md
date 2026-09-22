# Chaos Drill R4 (GO-PLAN §12 R4)

Verification exercise on the all-in-one / shard / router build
(`2a1f4be`). No new game systems; no packet-shape or balance changes;
no code changes at all (one temporary tick-timing log was built to
`/tmp`, then reverted — the tree is untouched except this file).

Pass bars (§12): **no item/XP/quest loss** (DB diff vs event log),
**p95 tick < 50ms**, **hub reconnect < 10s**.

## Setup common to all legs

- Server binaries built from this commit to `/tmp/chaos/` (`server-plain`,
  plus `server-tick` with a temporary `tickdur_us` log line in
  `startTickLoop`, reverted afterwards — `git status` clean).
- Every leg used fresh scratch DBs (`DB_PATH=/tmp/chaos/*.db`). The repo
  `go-server/data.db` was never touched (mtime unchanged, pre-drill).
- Servers were run with cwd `go-server/` so `quests/`, `crafting/` and
  other data dirs resolve (an early soak run from `/tmp` logged
  `quests disabled` / `crafting disabled`; it was discarded and re-run —
  final numbers below are all with data live).
- All harnesses live in `/tmp/chaos/{soak,term,wal,relogin,skew}/`
  (NOT in `go-server/e2e/`). All servers killed afterwards; all drill
  ports verified free (9001-range + 9021/9022/9101).

## Leg 1 — 100+ bots soak (60s movement + chat + trade + combat)

Harness opens 100 concurrent WS conns; admitted bots login (`Handshake`
→ `Login` → `Ready`), then every 1.5s send a movement Step + a combat
swing, chat on a 7s subset tick, hold one open trade pair, and fight one
spawned skeleton. Fresh DB, `TESTMAP=1` default.

Results:

| metric | value |
|---|---|
| dial attempts | 100 |
| admitted | 16 |
| rejected (HTTP 429) | 84 (`ops: reject ip=... over per-IP cap (16)`) |
| login failures | 0 |
| soak duration | 60s, readers alive whole run (`read_errors=0`) |
| combat swings processed | ~640/run (`combat instance=...` in log) |
| mob kill + loot | skeleton killed, `loot spawned (gold ...)`; post-kill swings correctly ignored |
| chat | 512 chat frames rx'd by bots; region bubbles relayed (`withBubble:true`) |
| trade | server `opening trade between ...`; Trade Open frames on both parties; Close at teardown |
| server state at end | `RUNNING`, load drained to 0, no crash/panic/race |
| server tick p95 | **0.074ms** (n=3864 `tickdur_us` samples; p50 0.028ms, max 8.7ms) |
| reconnect dial-to-Welcome | 0.07–0.10s |

Note: 16/84 is the designed per-IP cap (`MAX_CONNECTIONS=16`,
GO-PLAN §5 documents the 16-admit soak behavior), not a failure — the
drill records admission, and all 16 admitted bots soaked the full 60s.
Client-observed bulk gaps (p50 ~1s, p95 ~5s) reflect event-driven 20Hz
flushes (the server only writes when there is something to send; the 5s
showcase anim dominates idle cadence), not tick overruns — the bar
metric is the server-side tick p95 above.

**Leg 1: PASS** — server alive, no crashes, p95 tick 0.07ms < 50ms.

## Leg 2 — kill -TERM mid-fight + mid-trade

Fresh DB, `M9_MOBDMG=10`. `termA`+`termB` login adjacent. termA crafts a
flask (skill XP + item), sets `tutorial=2`, spawns `termmob` next to
itself and swings; termA+termB open a mutual trade and termA puts a logs
offer on the table (session left OPEN, no accept). At TERM time the log
shows a live fight (mob hits termA 62→59 HP, hero swings back 11 dmg,
XP flowing) and `opening trade between termB and termA` + offer Add.

Results:

| check | value |
|---|---|
| SIGTERM behavior | `drain: shutdown signal -> DRAINING (no new conns, sim continues for 2 players)` |
| `/healthz` after TERM | `{"state":"DRAINING","load":2}` HTTP 503 |
| sessions survive | yes — swings + chat processed after TERM (`combat instance=...` post-signal) |
| new WS conns during drain | HTTP 503 `server updating` |
| clients disconnect → | `drain: SHUTDOWN (flush barrier complete)`, process exit in ~2s, port free |
| DB after shutdown | termA L3 at (100,96); `flask×1`, `logs×3+×1` (offered items retained, no exchange happened); skills XP 3 rows (`3:L1:73`, `6:L3:213`, `17:L1:15`); `tutorial=2/0` |
| re-login on same DB | `flask=1`, `logs=4`, `m11:tutorial=2/0`, Skill Batch with 3 entries; dial-to-Welcome 2.5s |

**Leg 2: PASS** — DRAINING keeps sessions + sim, SHUTDOWN flushes and
exits, zero item/XP/quest loss (DB rows match the pre-TERM event log).

## Leg 3 — kill -9 WAL recovery

Fresh DB, `M11_HERODMG=10`. `walC` seeds `bronzesword×1` + `logs×5`,
crafts `flask×1` (skill 17 XP), sets `tutorial=3`, bumps achievement
`ratinfestation=1`, spawns and kills `walmob` (combat XP), then idles
14s so the 10s dirty flush lands (510KB uncheckpointed WAL present at
kill time). Snapshot dumped via `sqlite3`, then `kill -9`, then reboot
on the same DB file.

Pre-kill snapshot: `bronzesword×1`, `logs×5`, `flask×1`;
skills `3:L2:90`, `6:L3:270`, `17:L1:15`; `tutorial=3/0`;
`ratinfestation=1`; player row L4 at (100,96).

Results:

| check | value |
|---|---|
| reboot | `RUNNING`, serves traffic |
| log corruption errors | none (`corrupt/malform/unable/panic`: zero hits) |
| `PRAGMA integrity_check` | `ok` |
| DB diff pre-kill vs post-reboot | **identical** (all inventory/skill/quest/achievement/player rows) |
| re-login on recovered DB | `flask=1`, `logs=5`, `m11:tutorial=3/0`, Skill Batch 3 entries; dial-to-Welcome 2.5s |

**Leg 3: PASS** — WAL recovery with no loss and no corruption.

## Leg 4 — version-skew soak (~2min, VERSION=old + VERSION=new)

Router (`ROLE=router`, `HUB_LISTEN=127.0.0.1:9101`) + two shards on
separate DBs/ports: `shard-old` (`VERSION=old`, 9021, started first) and
`shard-new` (`VERSION=new`, 9022). Router table confirmed:
`preferred=127.0.0.1:9022`, `previous=127.0.0.1:9021`,
`warmUntil=<ts>`. 3 clients pinned to old (pre-swap logins) + 3 on
preferred, 120s of movement + tagged chat; then a router-routed
reconnect probe.

Results:

| check | value |
|---|---|
| `GET /servers?login=<each of 6>` | all 6 → `127.0.0.1:9022` (preferred) |
| refresh banner (`Chat-19`, no wire change) | old clients: 1 each; new clients: 0 each |
| chat cross-talk | 0 foreign messages (34/33 chats seen per client, all same-version) |
| reconnect via preferred | dial-to-instance 2.9s |
| post-soak health | both shards `RUNNING`, load 0; clean SIGTERM exits |

**Leg 4: PASS** — preferred routing + banner-on-old-only + no
cross-talk, reconnect 2.9s < 10s.

## Bars verdict

| bar | result |
|---|---|
| no item/XP/quest loss (DB diff vs event log) | PASS (legs 2, 3 row-level diffs; leg 1 persist-on-disconnect saves) |
| p95 tick < 50ms | PASS (0.074ms under 16-conn soak) |
| hub reconnect < 10s | PASS (0.07s / 2.5s / 2.5s / 2.9s across legs) |

No leg exposed a data-loss or crash bug, so no fix-forward was needed
(the only bugs found were in the throwaway `/tmp` harnesses themselves:
a stale gorilla read deadline, a missing `/openalchemy` before craft,
and a hardcoded quest stage in the re-login checker).

## Artifacts (all in /tmp, none committed)

- `/tmp/chaos/server-plain`, `/tmp/chaos/server-tick` (binaries)
- `/tmp/chaos/soak/`, `term/`, `wal/`, `relogin/`, `skew/` (harness sources)
- `/tmp/chaos/*.db*`, `/tmp/chaos/*.log` (scratch DBs, server logs, dumps)

Committed by this drill: `docs/CHAOS-R4.md` only.
