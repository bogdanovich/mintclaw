package companion

import (
	"fmt"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
)

func validateCodingTaskProcessAuthority(profile codingtask.TaskMode) error {
	privileged, err := currentCodingProcessPrivileged()
	if err != nil {
		return fmt.Errorf("inspect coding process authority: %w", err)
	}
	return validateCodingTaskPrivilege(profile, privileged)
}

func validateCodingTaskPrivilege(profile codingtask.TaskMode, privileged bool) error {
	if profile == codingtask.TaskModeMachineYolo && privileged {
		return fmt.Errorf("machine-yolo requires a non-elevated companion process")
	}
	return nil
}
