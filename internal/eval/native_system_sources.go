package eval

import (
	"encoding/json"
	"slices"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

func compareNativeSystemSources(result *Comparison, baseline, current RunReport) {
	a, aKnown := nativeSystemSourceGroups(baseline)
	b, bKnown := nativeSystemSourceGroups(current)
	if !aKnown {
		result.EnvironmentUnknowns = append(result.EnvironmentUnknowns, "baseline_native_system_sources")
	}
	if !bKnown {
		result.EnvironmentUnknowns = append(result.EnvironmentUnknowns, "current_native_system_sources")
	}
	if !aKnown || !bKnown {
		return
	}
	for index, name := range []string{"workspace_definition", "skill_catalog", "active_skills"} {
		if !slices.Equal(a[index], b[index]) {
			result.EnvironmentChanges = append(result.EnvironmentChanges, "native_"+name)
		}
	}
}

// Missing/withheld records are not an observed empty source group. Compare only
// complete routed request sets, retaining duplicates and case bindings.
func nativeSystemSourceGroups(report RunReport) ([3][]string, bool) {
	groups := [3][]string{}
	if report.NativeModels == nil || report.NativeModels.Code != "observed" ||
		report.NativeModels.Totals.Requests == 0 {
		return groups, false
	}
	cases := map[string]string{}
	for _, trial := range report.Trials {
		cases[trial.ID] = trial.CaseSHA256
	}
	for _, turn := range report.NativeModels.Turns {
		for _, call := range turn.Calls {
			value := call.SystemSources
			if value == nil || value.Code != "observed" || value.Validate() != nil {
				return [3][]string{}, false
			}
			for index, group := range []diagnostictrace.SystemSourceGroup{
				value.Workspace, value.SkillCatalog, value.ActiveSkills,
			} {
				data, _ := json.Marshal(struct {
					Case  string                            `json:"case"`
					Group diagnostictrace.SystemSourceGroup `json:"group"`
				}{cases[turn.TrialID], group})
				groups[index] = append(groups[index], string(data))
			}
		}
	}
	for index := range groups {
		slices.Sort(groups[index])
	}
	return groups, true
}
