package update

import "testing"

func TestReleaseAuthorityHashIsCanonicalAndBindsAuthority(t *testing.T) {
	authority := ReleaseAuthority{
		Profile: "stable", ProfileRevision: "stable-v1", ReleaseAlias: "current",
		Tag: "v1.2.3", Version: "v1.2.3", BaseURL: "https://releases.example/node",
		RedirectHosts: []string{"objects-b.example", "objects-a.example"},
		Channel:       ChannelStable, KeyID: "key", RequirePlatformSignature: true,
	}
	first, err := HashReleaseAuthority(authority)
	if err != nil {
		t.Fatal(err)
	}
	authority.RedirectHosts[0], authority.RedirectHosts[1] = authority.RedirectHosts[1], authority.RedirectHosts[0]
	second, err := HashReleaseAuthority(authority)
	if err != nil || second != first {
		t.Fatalf("reordered authority hash = %q, %v; want %q", second, err, first)
	}
	authority.ProfileRevision = "stable-v2"
	changed, err := HashReleaseAuthority(authority)
	if err != nil || changed == first {
		t.Fatalf("changed authority hash = %q, %v; original %q", changed, err, first)
	}
}
