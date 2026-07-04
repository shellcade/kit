package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/shellcade/kit/v2/preview"
)

// preview.go is the author-facing preview tooling:
// `shellcade-kit preview pack <dir>` validates an authoring directory
// (preview.yaml + frame files) and writes the single preview.scp bundle the
// arcade's games screen plays — the bundle a game ships as a release asset.

func runPreview(args []string) {
	if len(args) == 0 || args[0] != "pack" {
		fmt.Fprintln(os.Stderr, "usage: shellcade-kit preview pack <dir> [-o out.scp]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("preview pack", flag.ExitOnError)
	out := fs.String("o", "preview.scp", "output bundle path")
	fs.Parse(args[1:])
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: shellcade-kit preview pack <dir> [-o out.scp]")
		os.Exit(2)
	}
	dir := fs.Arg(0)
	fs.Parse(fs.Args()[1:]) // accept flags after the positional dir too
	if fs.NArg() != 0 {
		// A stray positional would silently swallow any flags after it
		// (flag stops at the first non-flag) — reject it loudly instead.
		fmt.Fprintf(os.Stderr, "preview pack: unexpected argument %q\nusage: shellcade-kit preview pack <dir> [-o out.scp]\n", fs.Arg(0))
		os.Exit(2)
	}

	data, err := preview.Pack(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "preview pack: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "preview pack: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("packed %s (%d bytes)\n", *out, len(data))
}
