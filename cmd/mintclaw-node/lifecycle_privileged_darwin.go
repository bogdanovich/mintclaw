//go:build darwin

package main

import (
	"errors"
	"fmt"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
)

func configurePrivilegedOwnerShellLifecycleRequest(
	request *lifecycleRequest,
	config companion.Config,
	brokerValue string,
	brokerConfigValue string,
) error {
	return configurePrivilegedOwnerShellLifecycleRequestWith(
		request,
		config,
		brokerValue,
		brokerConfigValue,
		companion.LoadAuthorityBrokerConfig,
		user.Lookup,
		func(account *user.User) ([]string, error) { return account.GroupIds() },
	)
}

func configurePrivilegedOwnerShellLifecycleRequestWith(
	request *lifecycleRequest,
	config companion.Config,
	brokerValue string,
	brokerConfigValue string,
	loadConfig func(string) (companion.AuthorityBrokerConfig, error),
	lookupUser func(string) (*user.User, error),
	groupIDs func(*user.User) ([]string, error),
) error {
	configured := config.OwnerShell != nil && config.OwnerShell.Enabled &&
		config.OwnerShell.PrivilegedHelper != nil
	hasBroker := strings.TrimSpace(brokerValue) != ""
	hasBrokerConfig := strings.TrimSpace(brokerConfigValue) != ""
	if !configured {
		if hasBroker || hasBrokerConfig {
			return errors.New("authority broker flags require owner_shell.privileged_helper")
		}
		return nil
	}
	if request == nil || !request.System {
		return errors.New("macOS privileged owner shell requires a system LaunchDaemon")
	}
	if request.ManagedUpdate {
		return errors.New("macOS privileged owner shell cannot be combined with managed update")
	}
	if !hasBroker || !hasBrokerConfig {
		return errors.New("macOS privileged owner shell requires --authority-broker and --authority-config")
	}
	brokerPath, err := resolveLifecyclePath(brokerValue)
	if err != nil || !filepath.IsAbs(strings.TrimSpace(brokerValue)) {
		return errors.New("macOS authority broker path must be absolute")
	}
	brokerConfigPath, err := resolveLifecyclePath(brokerConfigValue)
	if err != nil || !filepath.IsAbs(strings.TrimSpace(brokerConfigValue)) {
		return errors.New("macOS authority broker config path must be absolute")
	}
	brokerConfig, err := loadConfig(brokerConfigPath)
	if err != nil {
		return fmt.Errorf("validate macOS authority broker config: %w", err)
	}
	if brokerConfig.Companion == nil ||
		filepath.Clean(brokerConfig.Companion.ExecutablePath) != filepath.Clean(request.ExecutablePath) ||
		filepath.Clean(brokerConfig.Companion.ConfigPath) != filepath.Clean(request.ConfigPath) {
		return errors.New("macOS authority broker config does not bind the installed companion")
	}
	account, err := lookupUser(request.ServiceUser)
	if err != nil || account == nil {
		return errors.New("resolve macOS companion service account")
	}
	uid, uidErr := strconv.ParseUint(account.Uid, 10, 32)
	gid, gidErr := strconv.ParseUint(account.Gid, 10, 32)
	if uidErr != nil || gidErr != nil || uint32(uid) != brokerConfig.Companion.UID ||
		uint32(gid) != brokerConfig.Companion.GID {
		return errors.New("macOS authority broker companion identity does not match --service-user")
	}
	groups, err := groupIDs(account)
	if err != nil {
		return errors.New("resolve macOS companion service account groups")
	}
	allowedGroups := make(map[uint32]struct{}, len(groups))
	for _, group := range groups {
		value, parseErr := strconv.ParseUint(group, 10, 32)
		if parseErr != nil || value == 0 {
			return errors.New("macOS companion service account groups are invalid")
		}
		allowedGroups[uint32(value)] = struct{}{}
	}
	for _, group := range brokerConfig.Companion.SupplementaryGroups {
		if _, ok := allowedGroups[group]; !ok {
			return errors.New("macOS authority broker grants an unowned companion group")
		}
	}
	request.AuthorityBrokerPath = brokerPath
	request.AuthorityConfigPath = brokerConfigPath
	return nil
}
