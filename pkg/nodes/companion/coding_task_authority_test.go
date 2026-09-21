package companion

import (
	"strings"
	"testing"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
)

func TestMachineYoloRejectsPrivilegedCompanionProcess(t *testing.T) {
	if err := validateCodingTaskPrivilege(codingtask.TaskModeMachineYolo, true); err == nil ||
		!strings.Contains(err.Error(), "non-privileged companion process") {
		t.Fatalf("privileged machine-yolo error = %v", err)
	}
	for _, test := range []struct {
		profile    codingtask.TaskMode
		privileged bool
	}{
		{profile: codingtask.TaskModeMachineYolo, privileged: false},
		{profile: codingtask.TaskModeProjectYolo, privileged: true},
		{profile: codingtask.TaskModeInvestigate, privileged: true},
	} {
		if err := validateCodingTaskPrivilege(test.profile, test.privileged); err != nil {
			t.Fatalf("validateCodingTaskPrivilege(%q, %v) error = %v", test.profile, test.privileged, err)
		}
	}
}
