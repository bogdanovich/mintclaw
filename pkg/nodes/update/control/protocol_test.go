package control

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

func TestCodecRoundTripsStrictBoundedRequestResponseAndHealth(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	identity := testIdentity()
	request := Request{
		SchemaVersion: SchemaVersion, Kind: KindUpdate, RequestID: "request_1",
		Update: &UpdateRequest{
			Identity: identity, Profile: "stable", ReleaseAlias: "current",
			ExpectedManifestSHA256: strings.Repeat("d", 64),
			ExpectedArtifactSHA256: strings.Repeat("e", 64), ExpiresAt: now.Add(time.Minute).Unix(),
		},
	}
	var wire bytes.Buffer
	writer, err := NewCodec(bytes.NewReader(nil), &wire)
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.WriteRequest(request, now); err != nil {
		t.Fatal(err)
	}
	reader, err := NewCodec(&wire, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	incoming, err := reader.ReadIncoming(now)
	if err != nil || incoming.Request == nil || incoming.Request.Update.Identity != identity {
		t.Fatalf("ReadIncoming() = %#v, %v", incoming, err)
	}

	wire.Reset()
	response := Response{
		SchemaVersion: SchemaVersion, RequestID: request.RequestID,
		Observation: Observation{Phase: "staged", RequestedRelease: "v1.2.3", InstalledVersion: "v1.0.0"},
	}
	if err = writer.WriteResponse(response); err != nil {
		t.Fatal(err)
	}
	gotResponse, err := reader.ReadResponse()
	if err != nil || gotResponse != response {
		t.Fatalf("ReadResponse() = %#v, %v", gotResponse, err)
	}

	wire.Reset()
	health := Health{
		SchemaVersion: SchemaVersion, Kind: KindHealth, NodeID: "node_" + strings.Repeat("a", 52),
		Version: "v1.2.3", Platform: "linux", Architecture: "arm64", CatalogHash: strings.Repeat("f", 64),
	}
	if err = writer.WriteHealth(health); err != nil {
		t.Fatal(err)
	}
	incoming, err = reader.ReadIncoming(now)
	if err != nil || incoming.Health == nil || *incoming.Health != health {
		t.Fatalf("health ReadIncoming() = %#v, %v", incoming, err)
	}
}

func TestCodecRejectsOversizedDuplicateAndWrongVariantFrames(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	frames := map[string][]byte{
		"duplicate": []byte(`{"schema_version":1,"kind":"status","kind":"cancel","request_id":"request_1",` +
			`"identity":{"invocation_id":"inv","execution_id":"exec","plan_hash":"` + strings.Repeat("a", 64) +
			`","catalog_hash":"` + strings.Repeat("b", 64) + `","authority_hash":"` + strings.Repeat("c", 64) + `"}}`),
		"wrong variant": []byte(`{"schema_version":1,"kind":"status","request_id":"request_1",` +
			`"update":{"identity":{"invocation_id":"inv","execution_id":"exec","plan_hash":"` +
			strings.Repeat("a", 64) + `","catalog_hash":"` + strings.Repeat("b", 64) +
			`","authority_hash":"` + strings.Repeat("c", 64) + `"},"profile":"stable",` +
			`"release_alias":"current","expected_manifest_sha256":"` + strings.Repeat("d", 64) +
			`","expected_artifact_sha256":"` + strings.Repeat("e", 64) + `","expires_at":1786183260}}`),
	}
	for name, frame := range frames {
		t.Run(name, func(t *testing.T) {
			var wire bytes.Buffer
			var header [4]byte
			binary.BigEndian.PutUint32(header[:], uint32(len(frame)))
			wire.Write(header[:])
			wire.Write(frame)
			codec, err := NewCodec(&wire, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = codec.ReadIncoming(now); err == nil {
				t.Fatal("unsafe frame was accepted")
			}
		})
	}
	var oversized bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], MaxFrameBytes+1)
	oversized.Write(header[:])
	codec, err := NewCodec(&oversized, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = codec.ReadIncoming(now); err == nil {
		t.Fatal("oversized frame was accepted")
	}
}

func testIdentity() ExecutionIdentity {
	return ExecutionIdentity{
		InvocationID: "invocation_1", ExecutionID: "execution_1",
		PlanHash: strings.Repeat("a", 64), CatalogHash: strings.Repeat("b", 64),
		AuthorityHash: strings.Repeat("c", 64),
	}
}
