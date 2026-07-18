// raven — Go binary for the Raven drive-time voice pipeline. Ports the bash/
// Python orchestration to a single dependency-free static binary. Synthesis
// (synthd, Kokoro/mlx-audio) stays Python; everything else migrates here.
//
// Usage:
//
//	raven hook       # Claude Code Stop/UserPromptSubmit/SessionEnd handler (stdin)
//	raven serve      # HLS file server and phone control API
//	raven write      # continuous raw PCM writer (stdout -> pcm.fifo)
//	raven diagnose [--since-min N] # read-only pipeline health report
package main

import (
	"fmt"
	"os"

	"raven-go/internal/diagnose"
	"raven-go/internal/hook"
	"raven-go/internal/serve"
	"raven-go/internal/write"
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
	case "write":
		if err := write.Run(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "raven write: %v\n", err)
			os.Exit(1)
		}
	case "diagnose":
		if err := diagnose.Run(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "raven diagnose: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "raven: unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}
