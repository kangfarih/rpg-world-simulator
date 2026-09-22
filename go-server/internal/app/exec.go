// ExecCanonical re-execs the canonical root server (package main, `go run
// .` in rootDir) with the given args/env, replacing the shim process so
// signals and the PID behave exactly like a direct `go run .`.
//
// The shim cannot import the root package (package main is not importable),
// and the game boot lives in behavior-frozen root files, so the shim stays
// a thin launcher: resolve Config via FromEnv, then hand over. The
// E9B_SHIM guard refuses nested execs (exec loop = fatal).
package app

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// ExecCanonical replaces the current process with `go run . <args>` in
// rootDir. It returns only when the exec itself fails.
func ExecCanonical(rootDir string, args, env []string) error {
	if os.Getenv("E9B_SHIM") != "" {
		return fmt.Errorf("app: refusing nested shim exec (E9B_SHIM set)")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("app: go toolchain not found in PATH: %w", err)
	}
	argv := append([]string{"go", "run", "."}, args...)
	fullEnv := append(append([]string{}, env...), "E9B_SHIM=1")
	if err := syscall.Chdir(rootDir); err != nil {
		return fmt.Errorf("app: chdir %s: %w", rootDir, err)
	}
	return syscall.Exec(goBin, argv, fullEnv)
}
