package main

import (
	"encoding/json"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestManagedNodeHealthUsesCompanionProtocolCatalog(t *testing.T) {
	catalog := nodes.CapabilityCatalog{Commands: []nodes.CommandDescriptor{{
		Name:         "test.info.v1",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","maximum":60}}}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
		Risk:         nodes.RiskRead,
	}}}
	health, err := managedNodeHealth("node_test", "v2.0.0", catalog)
	if err != nil {
		t.Fatal(err)
	}
	want, err := catalog.HashForProtocol(nodes.ProtocolV2)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := catalog.HashForProtocol(nodes.ProtocolV1)
	if err != nil {
		t.Fatal(err)
	}
	if health.CatalogHash != want {
		t.Fatalf("managed health catalog hash = %q; want v2 %q", health.CatalogHash, want)
	}
	if health.CatalogHash == legacy {
		t.Fatal("managed health retained the legacy v1 catalog identity")
	}
}
