# go-server (rpg-world-server)

Minimal Kaetram-compatible WebSocket stub in Go. Starting point migrated
verbatim from the experiment (`main.go`, `packets.go`).

## Run

```sh
cd go-server
go mod tidy
go run .                 # TESTMAP showcase (default ON), ws://127.0.0.1:9001
TESTMAP=0 go run .       # pure 9 real regions, no overlays
CLEAN=1 go run .         # clean mode: pure terrain + one equipped adventurer
COMBAT=1 go run .        # combat party mode: 4 bots + BossDummy
```

Flags also work: `--clean` / `--noclean`, `--combat`, `--testmap=false` /
`--notestmap`. `CLEAN=0` / `COMBAT=` turn modes off (default OFF).

## Ports

- Server: `ws://127.0.0.1:9001` (client server PORT, HUB disabled).
- Client: stock Kaetram client at `http://127.0.0.1:9000`
  (`yarn workspace @kaetram/client dev --port 9000 --host 127.0.0.1`).

## Checks

```sh
go run . &                 # TESTMAP=1 default
go run ./e2e/testmap       # showcase check
COMBAT=1 go run . &        # restart server first
go run ./e2e/combat        # combat check
CLEAN=1 go run . &         # restart server first
go run ./e2e/clean         # clean check
```

Milestone harnesses (each against a TESTMAP=1 server — the default; use a
fresh DB per run, e.g. `DB_PATH=/tmp/e2e.db`, since logins restore persisted
positions). The harnesses dial a running server, they don't spawn one, so the
debug damage accelerators are server-side env: start the server with
`M9_MOBDMG=10` (m9 death leg) and/or `M11_HERODMG=10` (m11 skeleton kill
leg). m11's drop leg greps the server log, so pass `M11_SERVER_LOG=<server
log file>` to the m11 harness as well. Every harness (and the server)
honors `PORT` — set it on both when 9001 is taken, e.g. a TS dev server
running side-by-side:

```sh
go run ./e2e/m6                 # stores/bank/NPC/persistence
go run ./e2e/m7                 # chat + rank-gated commands
go run ./e2e/m8                 # minigames lobby/queue/score
go run ./e2e/m9                 # mob AI aggro/death/leash/kill
go run ./e2e/m10                # areas music/overlay/pvp/camera + chest flow
go run ./e2e/m11                # quests/achievements + gated drops/persistence
go run ./e2e/m12                # trade + crafting + enchanting
```

## Docs

See `docs/`: `SPEC.md`, `CLIENT-ASSETS.md`, `GO-SERVER-PLAN.md`,
`WORLD-RECREATION.md`, `CLASS-DESIGN.md`, `CLASS-DESIGN-V2.md`,
`COMBAT-SKILLS.md`, `NOTES.md`.
