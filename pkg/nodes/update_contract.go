package nodes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var updateVersionPattern = regexp.MustCompile(
	`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)` +
		`(?:-[0-9A-Za-z][0-9A-Za-z-]*(?:\.[0-9A-Za-z][0-9A-Za-z-]*)*)?$`,
)

const (
	MaxUpdateProfiles        = 16
	MaxUpdateReleases        = 16
	MaxUpdateVersionBytes    = 64
	MaxUpdateDescriptionSize = 256
)

type UpdateReleaseDescriptor struct {
	Alias          string `json:"alias"`
	Version        string `json:"version"`
	Description    string `json:"description,omitempty"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
	ArtifactSHA256 string `json:"artifact_sha256,omitempty"`
	ArtifactSize   int64  `json:"artifact_size,omitempty"`
	AuthorityHash  string `json:"authority_hash,omitempty"`
}

type UpdateProfileDescriptor struct {
	Alias          string                    `json:"alias"`
	Revision       string                    `json:"revision"`
	Channel        string                    `json:"channel"`
	Approval       string                    `json:"approval"`
	CurrentVersion string                    `json:"current_version,omitempty"`
	Platform       string                    `json:"platform,omitempty"`
	Architecture   string                    `json:"architecture,omitempty"`
	Releases       []UpdateReleaseDescriptor `json:"releases"`
	Downgrade      bool                      `json:"downgrade,omitempty"`
}

// NodeUpdatePlanAuthority is the bounded, non-secret update identity retained
// with an execution plan and invocation record. It lets a successor companion
// query or cancel the exact coordinator transaction without replaying it.
type NodeUpdatePlanAuthority struct {
	ExecutionID     string `json:"execution_id"`
	Profile         string `json:"profile"`
	ProfileRevision string `json:"profile_revision"`
	ReleaseAlias    string `json:"release_alias"`
	ReleaseVersion  string `json:"release_version"`
	CurrentVersion  string `json:"current_version"`
	Platform        string `json:"platform"`
	Architecture    string `json:"architecture"`
	ManifestSHA256  string `json:"manifest_sha256"`
	ArtifactSHA256  string `json:"artifact_sha256"`
	ArtifactSize    int64  `json:"artifact_size"`
	AuthorityHash   string `json:"authority_hash"`
}

func (authority NodeUpdatePlanAuthority) Validate() error {
	if !validInvocationIdentifier(authority.ExecutionID) ||
		(Alias(authority.Profile)).Validate() != nil ||
		!validInvocationIdentifier(authority.ProfileRevision) ||
		(Alias(authority.ReleaseAlias)).Validate() != nil ||
		!validUpdateVersion(authority.ReleaseVersion) ||
		!validUpdateVersion(authority.CurrentVersion) ||
		(authority.Platform != "linux" && authority.Platform != "darwin") ||
		(authority.Architecture != "amd64" && authority.Architecture != "arm64") ||
		!validUpdateDigest(authority.ManifestSHA256) ||
		!validUpdateDigest(authority.ArtifactSHA256) ||
		authority.ArtifactSize <= 0 || authority.ArtifactSize > 128*1024*1024 ||
		!validUpdateDigest(authority.AuthorityHash) {
		return fmt.Errorf("%w: malformed node update authority", ErrInvalidInvocation)
	}
	return nil
}

func (authority NodeUpdatePlanAuthority) matchesDescriptor(
	profile UpdateProfileDescriptor,
	release UpdateReleaseDescriptor,
) bool {
	return authority.Profile == profile.Alias &&
		authority.ProfileRevision == profile.Revision &&
		authority.ReleaseAlias == release.Alias &&
		authority.ReleaseVersion == release.Version &&
		authority.CurrentVersion == profile.CurrentVersion &&
		authority.Platform == profile.Platform &&
		authority.Architecture == profile.Architecture &&
		authority.ManifestSHA256 == release.ManifestSHA256 &&
		authority.ArtifactSHA256 == release.ArtifactSHA256 &&
		authority.ArtifactSize == release.ArtifactSize &&
		authority.AuthorityHash == release.AuthorityHash
}

func NewNodeUpdatePlanAuthority(
	executionID string,
	profile UpdateProfileDescriptor,
	releaseAlias string,
) (*NodeUpdatePlanAuthority, error) {
	for _, release := range profile.Releases {
		if release.Alias != releaseAlias {
			continue
		}
		authority := &NodeUpdatePlanAuthority{
			ExecutionID: executionID, Profile: profile.Alias, ProfileRevision: profile.Revision,
			ReleaseAlias: release.Alias, ReleaseVersion: release.Version,
			CurrentVersion: profile.CurrentVersion, Platform: profile.Platform,
			Architecture: profile.Architecture, ManifestSHA256: release.ManifestSHA256,
			ArtifactSHA256: release.ArtifactSHA256, ArtifactSize: release.ArtifactSize,
			AuthorityHash: release.AuthorityHash,
		}
		if err := authority.Validate(); err != nil {
			return nil, err
		}
		return authority, nil
	}
	return nil, fmt.Errorf("%w: update release is unavailable", ErrInvalidInvocation)
}

func ProjectUpdateDescriptorForProfile(
	descriptor CommandDescriptor,
	profileAlias string,
) (CommandDescriptor, bool) {
	if len(descriptor.UpdateProfiles) == 0 {
		return descriptor, true
	}
	if descriptor.Name != "node.update.v1" || profileAlias == "" {
		return CommandDescriptor{}, false
	}
	for _, profile := range descriptor.UpdateProfiles {
		if profile.Alias != profileAlias {
			continue
		}
		descriptor.UpdateProfiles = CloneUpdateProfileDescriptors([]UpdateProfileDescriptor{profile})
		descriptor.InputSchema = NodeUpdateInputSchema(descriptor.UpdateProfiles)
		return descriptor, true
	}
	return CommandDescriptor{}, false
}

func (profile UpdateProfileDescriptor) Validate() error {
	if err := (Alias(profile.Alias)).Validate(); err != nil ||
		!validInvocationIdentifier(profile.Revision) ||
		(profile.Channel != "stable" && profile.Channel != "nightly") ||
		profile.Approval != "required" ||
		len(profile.Releases) == 0 || len(profile.Releases) > MaxUpdateReleases {
		return fmt.Errorf("%w: malformed update profile descriptor", ErrInvalidCapability)
	}
	priorAlias := ""
	versions := make(map[string]struct{}, len(profile.Releases))
	for _, release := range profile.Releases {
		if err := release.Validate(profile.Channel); err != nil {
			return err
		}
		if priorAlias != "" && release.Alias <= priorAlias {
			return fmt.Errorf("%w: update releases are not sorted", ErrInvalidCapability)
		}
		if _, duplicate := versions[release.Version]; duplicate {
			return fmt.Errorf("%w: duplicate update version", ErrInvalidCapability)
		}
		versions[release.Version] = struct{}{}
		priorAlias = release.Alias
	}
	return nil
}

func (release UpdateReleaseDescriptor) Validate(channel string) error {
	if err := (Alias(release.Alias)).Validate(); err != nil ||
		len(release.Version) == 0 || len(release.Version) > MaxUpdateVersionBytes ||
		!validUpdateVersion(release.Version) ||
		len(release.Description) > MaxUpdateDescriptionSize ||
		release.Description != strings.TrimSpace(release.Description) ||
		containsModelControl(release.Description) {
		return fmt.Errorf("%w: malformed update release descriptor", ErrInvalidCapability)
	}
	prerelease := strings.Contains(release.Version, "-")
	if (channel == "stable" && prerelease) || (channel == "nightly" && !prerelease) {
		return fmt.Errorf("%w: update release does not match channel", ErrInvalidCapability)
	}
	return nil
}

func (profile UpdateProfileDescriptor) validateRuntimeAuthority() error {
	if !validUpdateVersion(profile.CurrentVersion) ||
		(profile.Platform != "linux" && profile.Platform != "darwin") ||
		(profile.Architecture != "amd64" && profile.Architecture != "arm64") {
		return fmt.Errorf("%w: update profile lacks managed runtime facts", ErrInvalidCapability)
	}
	for _, release := range profile.Releases {
		if !validUpdateDigest(release.ManifestSHA256) ||
			!validUpdateDigest(release.ArtifactSHA256) ||
			release.ArtifactSize <= 0 || release.ArtifactSize > 128*1024*1024 ||
			!validUpdateDigest(release.AuthorityHash) {
			return fmt.Errorf("%w: update release lacks authenticated authority", ErrInvalidCapability)
		}
	}
	return nil
}

func validUpdateDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func NodeUpdateInputSchema(profiles []UpdateProfileDescriptor) json.RawMessage {
	releases := make(map[string]struct{})
	for _, profile := range profiles {
		for _, release := range profile.Releases {
			releases[release.Alias] = struct{}{}
		}
	}
	aliases := make([]string, 0, len(releases))
	for alias := range releases {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	schema, _ := json.Marshal(map[string]any{
		"type":     "object",
		"required": []string{"release"},
		"properties": map[string]any{
			"release": map[string]any{"type": "string", "enum": aliases},
		},
		"additionalProperties": false,
	})
	return schema
}

func validUpdateVersion(value string) bool {
	return updateVersionPattern.MatchString(value)
}

func CloneUpdateProfileDescriptors(profiles []UpdateProfileDescriptor) []UpdateProfileDescriptor {
	cloned := make([]UpdateProfileDescriptor, len(profiles))
	for index, profile := range profiles {
		cloned[index] = profile
		cloned[index].Releases = append([]UpdateReleaseDescriptor(nil), profile.Releases...)
	}
	return cloned
}
