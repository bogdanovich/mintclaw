//go:build linux || darwin

package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/internal/jsonstrict"
	nodeupdate "github.com/bogdanovich/mintclaw/pkg/nodes/update"
)

const maxCompanionConfigBytes = 1024 * 1024

type ConfigAuthorityResolver struct {
	configPath string
	releases   map[string]map[string]ResolvedRelease
}

type authoritySourceConfig struct {
	BaseURL                  string   `json:"base_url"`
	RedirectHosts            []string `json:"redirect_hosts,omitempty"`
	PublicKey                string   `json:"public_key"`
	Revoked                  bool     `json:"revoked,omitempty"`
	RequirePlatformSignature bool     `json:"require_platform_signature,omitempty"`
}

type authorityReleaseConfig struct {
	Tag         string `json:"tag"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

type authorityProfileConfig struct {
	Enabled        bool                              `json:"enabled"`
	Revision       string                            `json:"revision"`
	Source         string                            `json:"source"`
	Channel        nodeupdate.Channel                `json:"channel"`
	AllowDowngrade bool                              `json:"allow_downgrade,omitempty"`
	Approval       string                            `json:"approval,omitempty"`
	Releases       map[string]authorityReleaseConfig `json:"releases,omitempty"`
}

type normalizedAuthoritySource struct {
	release ResolvedRelease
	revoked bool
}

func LoadConfigAuthorityResolver(configPath string) (*ConfigAuthorityResolver, error) {
	resolver, err := loadConfigAuthorityResolver(configPath)
	if err != nil {
		return nil, err
	}
	resolver.configPath = configPath
	return resolver, nil
}

func loadConfigAuthorityResolver(configPath string) (*ConfigAuthorityResolver, error) {
	data, err := readPinnedCompanionConfig(configPath)
	if err != nil {
		return nil, err
	}
	if _, err = jsonstrict.Decode(data); err != nil {
		return nil, errors.New("companion config is not strict JSON")
	}
	var root map[string]json.RawMessage
	if err = json.Unmarshal(data, &root); err != nil {
		return nil, errors.New("decode companion update authority")
	}
	var sources map[string]authoritySourceConfig
	if raw := root["node_update_sources"]; len(raw) > 0 {
		if err = decodeAuthoritySection(raw, &sources); err != nil {
			return nil, errors.New("decode companion update sources")
		}
	}
	var profiles map[string]authorityProfileConfig
	if raw := root["node_update_policies"]; len(raw) > 0 {
		if err = decodeAuthoritySection(raw, &profiles); err != nil {
			return nil, errors.New("decode companion update policies")
		}
	}
	return normalizeCoordinatorAuthorities(sources, profiles)
}

func (resolver *ConfigAuthorityResolver) ResolveUpdateRelease(
	_ context.Context,
	profile string,
	release string,
) (ResolvedRelease, error) {
	if resolver == nil {
		return ResolvedRelease{}, ErrUpdateDenied
	}
	current := resolver
	if resolver.configPath != "" {
		refreshed, err := loadConfigAuthorityResolver(resolver.configPath)
		if err != nil {
			return ResolvedRelease{}, ErrUpdateDenied
		}
		current = refreshed
	}
	byRelease := current.releases[profile]
	resolved, ok := byRelease[release]
	if !ok {
		return ResolvedRelease{}, ErrUpdateDenied
	}
	resolved.RedirectHosts = append([]string(nil), resolved.RedirectHosts...)
	resolved.TrustedKey.PublicKey = append([]byte(nil), resolved.TrustedKey.PublicKey...)
	return resolved, nil
}

func normalizeCoordinatorAuthorities(
	sources map[string]authoritySourceConfig,
	profiles map[string]authorityProfileConfig,
) (*ConfigAuthorityResolver, error) {
	if len(sources) > 8 || len(profiles) > nodes.MaxUpdateProfiles {
		return nil, errors.New("coordinator update authority is unbounded")
	}
	normalizedSources := make(map[string]normalizedAuthoritySource, len(sources))
	for alias, source := range sources {
		if nodes.Alias(alias).Validate() != nil {
			return nil, errors.New("coordinator update source alias is invalid")
		}
		parsed, err := url.Parse(source.BaseURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
			parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
			return nil, errors.New("coordinator update source URL is invalid")
		}
		parsed.Path = strings.TrimSuffix(parsed.Path, "/")
		if parsed.Path == "" || parsed.Path == "/" || path.Clean(parsed.Path) != parsed.Path {
			return nil, errors.New("coordinator update source URL is invalid")
		}
		trusted, err := nodeupdate.ParsePublicKey(source.PublicKey)
		if err != nil {
			return nil, errors.New("coordinator update source trust is invalid")
		}
		redirects := append([]string(nil), source.RedirectHosts...)
		sort.Strings(redirects)
		if len(redirects) > 8 {
			return nil, errors.New("coordinator update redirect authority is unbounded")
		}
		for index, host := range redirects {
			if !validRedirectHost(host) || (index > 0 && host == redirects[index-1]) {
				return nil, errors.New("coordinator update redirect authority is invalid")
			}
		}
		normalizedSources[alias] = normalizedAuthoritySource{
			release: ResolvedRelease{
				BaseURL: parsed.String(), RedirectHosts: redirects, TrustedKey: trusted,
				RequirePlatformSignature: source.RequirePlatformSignature,
			},
			revoked: source.Revoked,
		}
	}
	resolver := &ConfigAuthorityResolver{releases: make(map[string]map[string]ResolvedRelease)}
	for alias, profile := range profiles {
		if nodes.Alias(alias).Validate() != nil || !boundedNamePattern.MatchString(profile.Revision) ||
			(profile.Approval != "" && profile.Approval != "required") ||
			(profile.Channel != nodeupdate.ChannelStable && profile.Channel != nodeupdate.ChannelNightly) {
			return nil, errors.New("coordinator update profile is invalid")
		}
		source, ok := normalizedSources[profile.Source]
		if !ok {
			return nil, errors.New("coordinator update profile source is unavailable")
		}
		if !profile.Enabled {
			if len(profile.Releases) != 0 || profile.AllowDowngrade {
				return nil, errors.New("disabled coordinator update profile retains authority")
			}
			continue
		}
		if source.revoked || len(profile.Releases) == 0 || len(profile.Releases) > nodes.MaxUpdateReleases {
			return nil, errors.New("enabled coordinator update profile has unavailable authority")
		}
		resolver.releases[alias] = make(map[string]ResolvedRelease, len(profile.Releases))
		for releaseAlias, release := range profile.Releases {
			if nodes.Alias(releaseAlias).Validate() != nil || release.Tag != release.Version ||
				!nodeupdate.ValidReleaseVersion(release.Version) {
				return nil, errors.New("coordinator update release is invalid")
			}
			prerelease := strings.Contains(release.Version, "-")
			if (profile.Channel == nodeupdate.ChannelStable && prerelease) ||
				(profile.Channel == nodeupdate.ChannelNightly && !prerelease) {
				return nil, errors.New("coordinator update release channel is invalid")
			}
			resolved := source.release
			resolved.Profile = alias
			resolved.ProfileRevision = profile.Revision
			resolved.ReleaseAlias = releaseAlias
			resolved.Tag = release.Tag
			resolved.Version = release.Version
			resolved.Channel = profile.Channel
			resolved.AllowDowngrade = profile.AllowDowngrade
			authorityHash, err := nodeupdate.HashReleaseAuthority(nodeupdate.ReleaseAuthority{
				Profile: resolved.Profile, ProfileRevision: resolved.ProfileRevision,
				ReleaseAlias: resolved.ReleaseAlias, Tag: resolved.Tag, Version: resolved.Version,
				BaseURL: resolved.BaseURL, RedirectHosts: resolved.RedirectHosts, Channel: resolved.Channel,
				AllowDowngrade: resolved.AllowDowngrade, KeyID: resolved.TrustedKey.KeyID,
				RequirePlatformSignature: resolved.RequirePlatformSignature,
			})
			if err != nil {
				return nil, err
			}
			resolved.AuthorityHash = authorityHash
			resolver.releases[alias][releaseAlias] = resolved
		}
	}
	return resolver, nil
}

func readPinnedCompanionConfig(configPath string) ([]byte, error) {
	if !pathIsCleanAbsolute(configPath) {
		return nil, errors.New("companion config path is invalid")
	}
	before, err := os.Lstat(configPath)
	if err != nil {
		return nil, errors.New("inspect companion config")
	}
	links, owner, ok := unixFileIdentity(before)
	if !ok || !before.Mode().IsRegular() || before.Mode().Perm()&0o022 != 0 || links != 1 ||
		(owner != 0 && owner != uint64(os.Geteuid())) || before.Size() <= 0 || before.Size() > maxCompanionConfigBytes {
		return nil, errors.New("companion config identity is unsafe")
	}
	fd, err := unix.Open(configPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("open companion config")
	}
	file := os.NewFile(uintptr(fd), configPath)
	defer func() { _ = file.Close() }()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New("companion config identity changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxCompanionConfigBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxCompanionConfigBytes {
		return nil, errors.New("read bounded companion config")
	}
	return data, nil
}

func decodeAuthoritySection(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("authority section contains trailing data")
	}
	return nil
}

func pathIsCleanAbsolute(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value
}
