// Command server is the canonical runner for the Kaetram stub server.
//
// It drives the frozen boot (internal/server Steps) through the canonical
// boot driver (internal/app Run): config from env/flags, then persist +
// registries + schedulers + tick loop + entity seed + showcase/combat
// brains, then serve. All packet shapes, tick cadences, boot order and
// TESTMAP/CLEAN/COMBAT behavior are owned by internal/server and unchanged.
//
// The module-root shim (`go run .` in go-server/) calls the same driver
// with the same steps.
package main

import (
	"log"
	"os"

	"rpg-world-server/internal/app"
	"rpg-world-server/internal/server"
)

func main() {
	cfg := app.FromEnv(os.Getenv, os.Args[1:])
	if err := app.Run(cfg, server.Steps()); err != nil {
		log.Fatalf("server: %v", err)
	}
}
