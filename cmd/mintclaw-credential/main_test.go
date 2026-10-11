package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTokenArgumentsAreRejectedWithoutEcho(t *testing.T) {
	const canary = "SYNTHETIC_ARG_TOKEN_CANARY"
	for _, args := range [][]string{nil, {canary}, {"provision", canary}, {"version", canary}, {"--token=" + canary}} {
		var output bytes.Buffer
		err := execute(context.Background(), args, &output)
		if !errors.Is(err, errUsage) || strings.Contains(err.Error(), canary) || output.Len() != 0 {
			t.Fatal("invalid arguments were accepted or echoed")
		}
	}
}

func TestVersionDoesNotProvision(t *testing.T) {
	var output bytes.Buffer
	if err := execute(context.Background(), []string{"version"}, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != version+"\n" {
		t.Fatal("unexpected version output")
	}
}
