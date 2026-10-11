package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/provisioning"
)

var version = "dev"

var errUsage = errors.New("usage: mintclaw-credential <provision|version>; token arguments are not accepted")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mintclaw-credential:", err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string, output io.Writer) error {
	if len(args) != 1 {
		return errUsage
	}
	switch args[0] {
	case "version":
		if _, err := fmt.Fprintln(output, version); err != nil {
			return errors.New("credential command output is unavailable")
		}
	case "provision":
		if err := provisioning.Provision(ctx); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(
			output,
			"Encrypted service-account credential stored. Agent access remains disabled.",
		); err != nil {
			return errors.New("credential setup result could not be written")
		}
	default:
		return errUsage
	}
	return nil
}
