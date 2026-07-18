// raven — Go binary for the Raven drive-time voice pipeline. Ports the bash/
// Python orchestration to a single dependency-free static binary. Synthesis
// (synthd, Kokoro/mlx-audio) stays Python; everything else migrates here.
//
// Usage:
//
//	raven hook       # Claude Code Stop/UserPromptSubmit/SessionEnd handler (stdin)
//
// (serve, write, diagnose subcommands to follow.)
package main

import (
	"fmt"
	"os"

	"raven-go/internal/hook"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: raven <hook|serve|write|diagnose>")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "hook":
		hook.Run(os.Stdin)
	default:
		fmt.Fprintf(os.Stderr, "raven: unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}
