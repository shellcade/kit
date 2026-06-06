package smoke

import (
	"flag"
	"fmt"
	"os"

	"github.com/shellcade/kit/v2/internal/game"
)

// Wants reports whether the dev-runner invocation asks for smoke mode — a
// `-smoke` (or `--smoke`, `-smoke=…`) argument anywhere on the command line.
// kit.Main dispatches here before the interactive runner so `go run .
// -smoke smoke.yaml` needs no terminal.
func Wants(args []string) bool {
	for _, a := range args {
		if a == "-smoke" || a == "--smoke" {
			return true
		}
		if len(a) > 7 && (a[:7] == "-smoke=" || (len(a) > 8 && a[:8] == "--smoke=")) {
			return true
		}
	}
	return false
}

// MainCLI is the `go run . -smoke …` entrypoint: parse the script, run it,
// write the shots, report. Exits non-zero on any error.
func MainCLI(g game.Game, args []string) {
	fs := flag.NewFlagSet("smoke", flag.ExitOnError)
	file := fs.String("smoke", "smoke.yaml", "smoke script to run")
	out := fs.String("smoke-out", "smoke-out", "directory for shot files")
	_ = fs.Parse(args)

	b, err := os.ReadFile(*file)
	if err != nil {
		fatal(err)
	}
	script, err := Parse(b)
	if err != nil {
		fatal(err)
	}
	shots, err := Run(g, script)
	if err != nil {
		fatal(err)
	}
	names, err := WriteShots(*out, shots)
	if err != nil {
		fatal(err)
	}
	for _, n := range names {
		fmt.Printf("%s\n", n)
	}
	fmt.Printf("smoke: %d shots → %s\n", len(shots), *out)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "smoke:", err)
	os.Exit(1)
}
