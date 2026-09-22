// Command server is the E9b thin launcher for the Kaetram stub server.
//
// The game boot lives in the root package main (`go run .` in the
// module root), which this shim cannot import, so it stays thin: resolve
// the boot Config via internal/app, log it, then hand over to the
// canonical root via app.ExecCanonical (syscall.Exec, so the PID and
// signal behavior are identical to `go run .`). All packet shapes, tick
// cadences, boot order and TESTMAP/CLEAN/COMBAT behavior are owned by the
// root and unchanged.
package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"

	"rpg-world-server/internal/app"
)

func moduleRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		log.Fatal("server: cannot locate module root")
	}
	// file = <root>/cmd/server/main.go -> root is three dirs up.
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func main() {
	cfg := app.FromEnv(os.Getenv, os.Args[1:])
	app.LogConfig(cfg)
	root := moduleRoot()
	if err := app.ExecCanonical(root, os.Args[1:], os.Environ()); err != nil {
		log.Fatalf("server: %v", err)
	}
}
