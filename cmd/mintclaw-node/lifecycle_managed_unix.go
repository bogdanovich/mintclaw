//go:build linux || darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
	nodeupdate "github.com/bogdanovich/mintclaw/pkg/nodes/update"
	"github.com/bogdanovich/mintclaw/pkg/nodes/update/coordinator"
)

func configureManagedUpdateRequest(
	request *lifecycleRequest,
	cfg companion.Config,
	coordinatorValue string,
) error {
	if !nodeupdate.ValidReleaseVersion(clientVersion()) {
		return errors.New("managed update installation requires a release-versioned mintclaw-node")
	}
	coordinatorPath, err := resolveLifecyclePath(coordinatorValue)
	if err != nil {
		return fmt.Errorf("resolve stable update coordinator: %w", err)
	}
	if coordinatorPath == request.ExecutablePath {
		return errors.New("stable coordinator must be a separate executable")
	}
	digest, _, err := coordinator.InspectExecutable(coordinatorPath, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return fmt.Errorf("validate stable update coordinator: %w", err)
	}
	var coordinatorStat unix.Stat_t
	if err = unix.Lstat(coordinatorPath, &coordinatorStat); err != nil ||
		coordinatorStat.Mode&unix.S_IFMT != unix.S_IFREG || coordinatorStat.Nlink != 1 {
		return errors.New("stable update coordinator identity is invalid")
	}
	ownerUID, ownerGID := os.Geteuid(), os.Getegid()
	if request.System {
		account, lookupErr := user.Lookup(request.ServiceUser)
		if lookupErr != nil {
			return fmt.Errorf("resolve managed service account: %w", lookupErr)
		}
		ownerUID, err = strconv.Atoi(account.Uid)
		if err != nil {
			return errors.New("managed service account uid is invalid")
		}
		ownerGID, err = strconv.Atoi(account.Gid)
		if err != nil {
			return errors.New("managed service account gid is invalid")
		}
	}
	if ownerUID <= 0 || ownerGID <= 0 {
		return errors.New("managed update coordinator requires an unprivileged service account")
	}
	expectedCoordinatorOwner := uint32(ownerUID)
	if request.System {
		expectedCoordinatorOwner = 0
	}
	if coordinatorStat.Uid != expectedCoordinatorOwner || coordinatorStat.Mode&0o022 != 0 {
		return errors.New("stable update coordinator ownership is unsafe")
	}
	identity, err := loadManagedIdentity(cfg.StateDir, request.System, ownerUID, ownerGID)
	if err != nil {
		return err
	}
	request.ManagedUpdate = true
	request.CoordinatorPath = coordinatorPath
	request.CoordinatorSHA256 = digest
	request.StateDirectory = cfg.StateDir
	request.NodeID = identity.ID
	request.ActiveRelease = clientVersion()
	request.OwnerUID = ownerUID
	request.OwnerGID = ownerGID
	return nil
}

func loadManagedIdentity(
	stateDirectory string,
	system bool,
	ownerUID int,
	ownerGID int,
) (companion.Identity, error) {
	if !system {
		identity, err := companion.LoadOrCreateIdentity(stateDirectory)
		if err != nil {
			return companion.Identity{}, fmt.Errorf("load managed node identity: %w", err)
		}
		return identity, nil
	}
	for index, path := range []string{stateDirectory, filepath.Join(stateDirectory, "identity.json")} {
		var stat unix.Stat_t
		if err := unix.Lstat(path, &stat); err != nil || stat.Uid != uint32(ownerUID) ||
			stat.Gid != uint32(ownerGID) || stat.Mode&0o077 != 0 ||
			(index == 0 && stat.Mode&unix.S_IFMT != unix.S_IFDIR) ||
			(index == 1 && (stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1)) {
			return companion.Identity{}, errors.New(
				"system managed node identity must be pre-provisioned for the service account",
			)
		}
	}
	identity, err := companion.LoadIdentity(stateDirectory)
	if err != nil {
		return companion.Identity{}, fmt.Errorf("load pre-provisioned managed node identity: %w", err)
	}
	return identity, nil
}

func beginManagedUpdateAdoption(
	request lifecycleRequest,
	manager string,
	service string,
	transactionID string,
) (*coordinator.Adoption, error) {
	if !request.ManagedUpdate {
		return nil, nil
	}
	scope := "user"
	if request.System {
		scope = "system"
	}
	return coordinator.BeginAdoption(
		request.StateDirectory,
		coordinator.Installation{
			Instance: request.Instance, Manager: manager, Scope: scope, Service: service,
			InstallTransactionID: transactionID, ConfigPath: request.ConfigPath,
			CoordinatorPath: request.CoordinatorPath, CoordinatorSHA256: request.CoordinatorSHA256,
			ServiceUID: uint32(request.OwnerUID), ServiceGID: uint32(request.OwnerGID),
			NodeID: request.NodeID, Platform: runtime.GOOS, Architecture: runtime.GOARCH,
		},
		request.ExecutablePath,
		request.ActiveRelease,
		request.ActiveRelease,
		request.OwnerUID,
		request.OwnerGID,
	)
}
