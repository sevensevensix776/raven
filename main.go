// raven — Go binary for the Raven drive-time voice pipeline. Ports the bash/
// Python orchestration to a single dependency-free static binary. Synthesis
// (synthd, Kokoro/mlx-audio) stays Python; everything else migrates here.
//
// Usage:
//
//	raven hook       # Claude Code Stop/UserPromptSubmit/SessionEnd handler (stdin)
//	raven serve      # HLS file server and phone control API
//
// (write and diagnose subcommands to follow.)
package main

import (
	"fmt"
	"os"

	"raven-go/internal/hook"
	"raven-go/internal/serve"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: raven <hook|serve|write|diagnose>")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "hook":
		hook.Run(os.Stdin)
	case "serve":
		if err := serve.Run(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "raven serve: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "raven: unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}
