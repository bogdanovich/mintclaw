//go:build darwin

package main

import (
	"errors"
	"os/user"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
)

func TestConfigurePrivilegedOwnerShellLifecycleBindsExactCompanion(t *testing.T) {
	request := lifecycleRequest{
		System: true, ServiceUser: "node_runner",
		ExecutablePath: "/opt/mintclaw/mintclaw-node",
		ConfigPath:     "/etc/mintclaw/node.json",
	}
	config := companion.Config{OwnerShell: &companion.OwnerShellConfig{
		Enabled:          true,
		PrivilegedHelper: &companion.PrivilegedShellHelperConfig{},
	}}
	err := configurePrivilegedOwnerShellLifecycleRequestWith(
		&request,
		config,
		"/usr/local/libexec/mintclaw-node-broker",
		"/etc/mintclaw/node-authority-broker.json",
		func(string) (companion.AuthorityBrokerConfig, error) {
			return companion.AuthorityBrokerConfig{Companion: &companion.AuthorityBrokerCompanionConfig{
				ExecutablePath: request.ExecutablePath,
				ConfigPath:     request.ConfigPath,
				UID:            501,
				GID:            20,
			}}, nil
		},
		func(string) (*user.User, error) {
			return &user.User{Username: "node_runner", Uid: "501", Gid: "20"}, nil
		},
		func(*user.User) ([]string, error) { return []string{"20"}, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if request.AuthorityBrokerPath != "/usr/local/libexec/mintclaw-node-broker" ||
		request.AuthorityConfigPath != "/etc/mintclaw/node-authority-broker.json" {
		t.Fatalf("authority lifecycle request = %#v", request)
	}
}

func TestConfigurePrivilegedOwnerShellLifecycleFailsClosed(t *testing.T) {
	configured := companion.Config{OwnerShell: &companion.OwnerShellConfig{
		Enabled:          true,
		PrivilegedHelper: &companion.PrivilegedShellHelperConfig{},
	}}
	validRequest := lifecycleRequest{
		System: true, ServiceUser: "node_runner",
		ExecutablePath: "/opt/mintclaw/mintclaw-node",
		ConfigPath:     "/etc/mintclaw/node.json",
	}
	load := func(string) (companion.AuthorityBrokerConfig, error) {
		return companion.AuthorityBrokerConfig{Companion: &companion.AuthorityBrokerCompanionConfig{
			ExecutablePath: validRequest.ExecutablePath,
			ConfigPath:     validRequest.ConfigPath,
			UID:            501,
			GID:            20,
		}}, nil
	}
	lookup := func(string) (*user.User, error) {
		return &user.User{Username: "node_runner", Uid: "501", Gid: "20"}, nil
	}
	groups := func(*user.User) ([]string, error) { return []string{"20"}, nil }
	tests := []struct {
		name         string
		request      lifecycleRequest
		config       companion.Config
		broker       string
		brokerConfig string
		load         func(string) (companion.AuthorityBrokerConfig, error)
		lookup       func(string) (*user.User, error)
		groups       func(*user.User) ([]string, error)
	}{
		{name: "user scope", request: lifecycleRequest{}, config: configured},
		{name: "managed update", request: func() lifecycleRequest {
			request := validRequest
			request.ManagedUpdate = true
			return request
		}(), config: configured},
		{name: "missing paths", request: validRequest, config: configured},
		{
			name: "flags without helper", request: validRequest, config: companion.Config{},
			broker: "/broker", brokerConfig: "/broker.json",
		},
		{
			name: "changed companion", request: validRequest, config: configured,
			broker: "/broker", brokerConfig: "/broker.json",
			load: func(string) (companion.AuthorityBrokerConfig, error) {
				return companion.AuthorityBrokerConfig{Companion: &companion.AuthorityBrokerCompanionConfig{
					ExecutablePath: "/other/node", ConfigPath: validRequest.ConfigPath,
					UID: 501, GID: 20,
				}}, nil
			},
		},
		{
			name: "changed identity", request: validRequest, config: configured,
			broker: "/broker", brokerConfig: "/broker.json", load: load,
			lookup: func(string) (*user.User, error) {
				return &user.User{Username: "node_runner", Uid: "502", Gid: "20"}, nil
			},
		},
		{
			name: "load failure", request: validRequest, config: configured,
			broker: "/broker", brokerConfig: "/broker.json",
			load: func(string) (companion.AuthorityBrokerConfig, error) {
				return companion.AuthorityBrokerConfig{}, errors.New("denied")
			},
		},
		{
			name: "unowned supplemental group", request: validRequest, config: configured,
			broker: "/broker", brokerConfig: "/broker.json",
			load: func(string) (companion.AuthorityBrokerConfig, error) {
				return companion.AuthorityBrokerConfig{Companion: &companion.AuthorityBrokerCompanionConfig{
					ExecutablePath: validRequest.ExecutablePath, ConfigPath: validRequest.ConfigPath,
					UID: 501, GID: 20, SupplementaryGroups: []uint32{80},
				}}, nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loadConfig := test.load
			if loadConfig == nil {
				loadConfig = load
			}
			lookupUser := test.lookup
			if lookupUser == nil {
				lookupUser = lookup
			}
			groupLookup := test.groups
			if groupLookup == nil {
				groupLookup = groups
			}
			err := configurePrivilegedOwnerShellLifecycleRequestWith(
				&test.request,
				test.config,
				test.broker,
				test.brokerConfig,
				loadConfig,
				lookupUser,
				groupLookup,
			)
			if err == nil {
				t.Fatal("unsafe privileged owner-shell lifecycle was accepted")
			}
		})
	}
}
