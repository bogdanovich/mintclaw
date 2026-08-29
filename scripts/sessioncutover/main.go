// Command sessioncutover prepares a copy-only current session corpus and an
// exact archive of non-current sessions for a coordinated stopped-state
// deployment. It is intentionally not linked into MintClaw startup.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

type configPathsFlag []string

func (paths *configPathsFlag) String() string {
	return fmt.Sprint([]string(*paths))
}

func (paths *configPathsFlag) Set(value string) error {
	*paths = append(*paths, value)
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "session cutover: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("sessioncutover", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var configs configPathsFlag
	var sourceRoot string
	var outputRoot string
	flags.Var(&configs, "config", "absolute current config.json to inventory (repeat for every active profile)")
	flags.StringVar(&sourceRoot, "source-root", "", "absolute ancestor of every config and workspace")
	flags.StringVar(&outputRoot, "output", "", "absolute new output directory; it must not already exist")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}

	manifest, err := convertSessions(convertOptions{
		SourceRoot:  sourceRoot,
		OutputRoot:  outputRoot,
		ConfigPaths: configs,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		stdout,
		"prepared %d current metadata documents and %d histories; archived %d metadata documents and %d histories; archived count mismatches: %d; manifest: %s\n",
		manifest.Totals.Retained.Metadata,
		manifest.Totals.Retained.Histories,
		manifest.Totals.Archived.Metadata,
		manifest.Totals.Archived.Histories,
		len(manifest.ArchivedHistoryCountMismatches),
		manifestPath(outputRoot),
	)
	return err
}
