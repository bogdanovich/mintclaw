//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/bogdanovich/mintclaw/pkg/nodes/update/coordinator"
)

var version = "dev"

func main() {
	if err := execute(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mintclaw-node-coordinator:", err)
		os.Exit(1)
	}
}

func execute(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: mintclaw-node-coordinator <run|version>")
	}
	switch args[0] {
	case "run":
		return run(args[1:])
	case "version":
		fmt.Println(coordinatorVersion())
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	stateDirectory := flags.String("state-dir", "", "absolute managed companion state directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *stateDirectory == "" {
		return errors.New("run requires exactly one --state-dir")
	}
	store, err := coordinator.OpenStore(*stateDirectory)
	if err != nil {
		return err
	}
	state, err := store.Load()
	if err != nil {
		_ = store.Close()
		return err
	}
	if err = coordinator.ValidateRunningCoordinator(state.Installation); err != nil {
		_ = store.Close()
		return err
	}
	resolver, err := coordinator.LoadConfigAuthorityResolver(state.Installation.ConfigPath)
	if err != nil {
		_ = store.Close()
		return err
	}
	updateCoordinator, err := coordinator.New(store, resolver, coordinatorVersion())
	if err != nil {
		_ = store.Close()
		return err
	}
	defer func() { _ = updateCoordinator.Close() }()
	supervisor, err := coordinator.NewSupervisor(updateCoordinator, coordinator.DefaultHealthTimeout)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return supervisor.Run(ctx)
}

func coordinatorVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "v0.0.0-dev"
}
