// Minimal Kaetram-compatible WebSocket stub shim.
//
// Canonical runner: `go run ./cmd/server` (internal/app Run driving
// internal/server Steps). This root entry stays working (`go run .`) by
// calling the exact same driver with the exact same steps.
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
