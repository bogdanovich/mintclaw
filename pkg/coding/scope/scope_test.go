package scope

import "testing"

func TestProfileTraitsAndScopeMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		profile  Profile
		readOnly bool
		worktree bool
		direct   bool
		root     bool
		project  bool
		machine  bool
		v2       bool
		v3       bool
	}{
		{ProfileInvestigate, true, false, false, false, true, false, true, true},
		{ProfileMutate, false, true, false, false, true, false, true, true},
		{ProfileProjectYolo, false, true, false, false, true, false, false, true},
		{ProfileMachineYolo, false, false, true, false, false, true, false, false},
		{ProfileMachineYoloRoot, false, false, true, true, false, true, false, false},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.profile), func(t *testing.T) {
			t.Parallel()
			if !test.profile.Valid() || test.profile.ReadOnly() != test.readOnly ||
				test.profile.UsesIsolatedWorktree() != test.worktree ||
				test.profile.DirectWritable() != test.direct || test.profile.Privileged() != test.root ||
				test.profile.AllowedFor(KindGitProject) != test.project ||
				test.profile.AllowedFor(KindMachine) != test.machine ||
				test.profile.AdmittedInV2() != test.v2 || test.profile.AdmittedInV3() != test.v3 {
				t.Fatalf("unexpected traits for profile %q", test.profile)
			}
		})
	}
}

func TestInvalidScopeAndProfileAreDenied(t *testing.T) {
	t.Parallel()
	if Kind("workspace").Valid() || Profile("admin").Valid() ||
		ProfileInvestigate.AllowedFor(Kind("workspace")) ||
		Profile("admin").AllowedFor(KindGitProject) {
		t.Fatal("invalid scope or profile was accepted")
	}
}
