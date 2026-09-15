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

## Docs

See `docs/`: `SPEC.md`, `CLIENT-ASSETS.md`, `GO-SERVER-PLAN.md`,
`WORLD-RECREATION.md`, `CLASS-DESIGN.md`, `CLASS-DESIGN-V2.md`,
`COMBAT-SKILLS.md`, `NOTES.md`.
