# Deploy Runbook + Rollback (GO-PLAN §12 R3)

One binary, three roles (`ROLE=all-in-one` default, `router`, `shard`).
A deploy runs the new build **alongside** the old (new port, own DB file —
old and new builds never dual-write, §11), the router moves NEW logins,
the old build drains, and rollback is a router swap-back with no data
migration and no redeploy.

Env contract (all new knobs default-off/0 — default boot is unchanged):

| Env              | Default      | Meaning                                              |
|------------------|--------------|------------------------------------------------------|
| `BACKUP_DIR`     | `./backups`  | Snapshot archive directory (created on demand).      |
| `BACKUP_ON_BOOT` | off          | `=1` snapshots the DB via the owner connection before `EnsureSchema` migrates. |
| `CANARY_PCT`     | `0`          | `0` = 100% of NEW logins to newest version (today's behavior). `>0` = that % to newest, rest to previous healthy version (sticky per login key). Router restart picks up changes (stateless, 5s grace). |
| `WARM_HOLD`      | `15m`        | How long the router keeps the old version entry WARM (routable) after a newer version registers. Bare numbers mean seconds. |
| `DRAIN_TIMEOUT`  | `30m`        | Bounds old-build DRAINING (empty-or-timeout, then flush barrier + exit). |
| `DB_PATH`        | `data.db`    | SQLite file for this instance (one single-writer owner per file). |
| `WORLD_HASH_STRICT` | off       | `=1` refuses boot on world.json drift (prod deploys set it). |
| `GVER_STRICT`    | on           | `=0` disables the handshake gVer gate (dev only).    |
| `HUB_LISTEN` / `HUB_ADDR` | `127.0.0.1:9101` | Router listen addr / shard dial URL (`ws://127.0.0.1:9101/`). |
| `HUB_TOKEN`      | —            | Bearer token authenticating shard registrations.     |
| `PORT`           | `9001`       | Game listen port (shard/all-in-one).                 |

## 0. Build (stamp the version)

```sh
cd go-server
SHA=$(git rev-parse --short HEAD)
go build -ldflags "-X rpg-world-server/internal/version.BuildID=$SHA" -o /tmp/server-new ./cmd/server
/tmp/server-new --help 2>&1 | head -2   # sanity: binary runs
```

The shard reports `buildID` + `gVer` + `VERSION` tag at registration; the
router tells builds apart by them (`GET /servers` shows `version`,
`buildId`, `state`, `load`).

## 1. Backup

```sh
# Manual snapshot any time (VACUUM INTO through database/sql, no sqlite3 CLI):
BACKUP_DIR=./backups BACKUP_ON_BOOT=1 /tmp/server-old &   # snapshots data.db -> ./backups/<UTC>-data.db, then boots
ls -l ./backups/
# Or snapshot on every boot of the NEW build (pre-migrate hook, default off):
BACKUP_DIR=./backups BACKUP_ON_BOOT=1 DB_PATH=data-v2.db PORT=9002 ROLE=shard \
  HUB_ADDR=ws://127.0.0.1:9101/ /tmp/server-new
```

Verify the archive opens (row counts match prod) before proceeding.

## 2. Migrate (expand-only)

Migrations are expand-only (nullable/default columns, additive tables;
drops/renames land ≥2 releases later — see `internal/persist/schema.go`).
`EnsureSchema` converges the schema at boot and stamps
`meta.schema_version`; a binary that meets a **newer** schema refuses boot:

```
m5: persist: schema_version 2 newer than binary supports (1): refusing boot; ...
```

The new build migrates its **own** DB file (`DB_PATH=data-v2.db`, seeded
from the backup copy — copy-on-migrate fallback). The old build keeps
running untouched on `data.db`.

## 3. Start new alongside old

```sh
# Router (if not already running):
ROLE=router HUB_LISTEN=127.0.0.1:9101 HUB_TOKEN=$HUB_TOKEN /tmp/server-new &

# Old build: all-in-one on 9001 (already running), or shard:
PORT=9001 ROLE=shard HUB_ADDR=ws://127.0.0.1:9101/ DB_PATH=data.db /tmp/server-old &

# New build: new port, own DB file, registers RUNNING:
PORT=9002 ROLE=shard HUB_ADDR=ws://127.0.0.1:9101/ DB_PATH=data-v2.db /tmp/server-new &
```

## 4. Health-gate the new build

```sh
curl -s http://127.0.0.1:9002/healthz   # want {"state":"RUNNING",...} (200)
curl -s http://127.0.0.1:9101/servers | python3 -m json.tool
# want: preferred=127.0.0.1:9002, previous=127.0.0.1:9001, warmUntil=<ts>
```

Do not proceed until the new shard is `RUNNING` in the table and its
`/healthz` is 200. Missed heartbeats (3×5s) evict the entry — a flapping
new build never becomes preferred.

## 5. Canary 5% → 100%

```sh
# 5% of NEW logins to the new build (sticky per login key: ?login=<id>
# always resolves to the same build). Restart the stateless router:
CANARY_PCT=5 ROLE=router HUB_LISTEN=127.0.0.1:9101 HUB_TOKEN=$HUB_TOKEN /tmp/server-new &
kill -TERM <old-router-pid>

# Spot-check the split (same key -> same build; ~5% preferred=new):
for u in alice bob carol dave erin frank; do
  curl -s "http://127.0.0.1:9101/servers?login=$u";
done
```

Watch the new build (errors, `/healthz`, player reports). Soak, then go
100% (all NEW logins to newest = today's routing):

```sh
CANARY_PCT=0 ROLE=router HUB_LISTEN=127.0.0.1:9101 HUB_TOKEN=$HUB_TOKEN /tmp/server-new &
kill -TERM <old-router-pid>
curl -s http://127.0.0.1:9101/servers   # preferred=9002, no canaryPct
```

## 6. TERM old → warm-hold → shutdown

```sh
kill -TERM <old-shard-pid>   # DRAINING: no new conns/sessions, sim continues
```

The old build evacuates on empty-or-`DRAIN_TIMEOUT` (stragglers get a
notice + disconnect — never a cross-build Teleport — then the
persist/quest/social flush barrier) and exits. Keep it **WARM** until
`warmUntil` (`WARM_HOLD`, default 15m after the new version registered):
existing sessions finish naturally and the canary remainder stays routable.
Only after the hold expires is the old process (and its `data.db`) safe to
reap/archive.

## Rollback (router swap-back)

Rollback needs **no data migration and no redeploy** — the old version was
kept warm. Exact operator action:

```sh
# 1. Retire the NEW version: SIGTERM its shard(s).
kill -TERM <new-shard-pid>
# 2. Watch preferred flip back (DRAINING shards take no new sessions;
#    evicted ones leave the table entirely):
watch -n 2 'curl -s http://127.0.0.1:9101/servers'
# 3. If a new-version process cannot be signalled, stop its unit / firewall
#    its heartbeats: 3 missed beats evict it with the same effect.
```

Once `preferred` points at the old addr again, new logins land on the old
version; existing sessions on the bad build drain or reconnect via the hub
(gVer-mismatched clients are redirected, never kicked mid-session).
Migrated `data-v2.db` stays out of the old build's way (it never
dual-writes); forward-fix or re-seed from `./backups/` on the next attempt.

## Recovery: world-hash mismatch

The boot gate compares the sha256 of the resolved `world.json` against
`meta.world_hash`:

- Warn (default): `worldhash: WARN MISMATCH ... (continuing; ...)` — dev
  iteration / e2e drift, boots through.
- `WORLD_HASH_STRICT=1` (prod): `log.Fatalf` refuses boot with the
  recovery line.

Recovery:

```sh
# a) The map regressed: restore the matching world.json, reboot.
# b) The DB is stale/copied from another checkout: reset to re-stamp
#    (LOSES persisted players — back up first):
cp data.db ./backups/data.db.pre-reset
rm data.db*   # next boot re-stamps world_hash (first boot)
```

## Chaos checklist pointer (R4)

Pre-release drill per GO-PLAN §12 R4: 100+ bots, `kill -TERM` mid-fight /
mid-trade, `kill -9` (WAL recovery), version skew soak. Pass criteria: no
item/XP/quest loss, p95 tick < 50ms, hub reconnect < 10s.
