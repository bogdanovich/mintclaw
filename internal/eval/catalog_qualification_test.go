package eval

import (
	"errors"
	"testing"
)

func TestQualifiedGeneralDefaultsKeepBudgetAndFrozenDefinitionGates(t *testing.T) {
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(corpus, Selection{DefaultOnly: true})
	if err != nil || len(selected) != 6 {
		t.Fatal("qualified default selection changed", err)
	}
	expected := map[string]string{
		"general.context.followup":       "4703ef12118060ecbb7c4edfb2ee19cf4950f91e13b52ac29e01851c8797b69d",
		"general.interaction.navigation": "d5e6ab4c8732f27ef726ea3407e181d595b05ca8ac035236a35c2dfd2f7332e4",
		"general.context.correction":     "2fd7760a9d5f54a530a48b3197f0440a02759c3ca42fc837d99af1823c82240b",
		"general.interaction.answer":     "a8eaee4e5515b36c0f320a30a96c7bfc2ea853d11f5670a54ec79ec50c1b20f4",
		"general.steering.boundary":      "a362d984dccc1001d5bbfad64879517943981263a09b9fb6b04447818f47c170",
		"general.steering.order":         "2718aa487cc2523a8e3c0b935e5d812c8814ef85c30cd399f7865be580cc7442",
		"general.session.isolation":      "e7e56af3cfbb07162e6f468adf2d164a3050e462c1f3252fac78d0f9f817cfb2",
		"general.tools.recovery":         "58906a951c87734b42ef35f3952d90209ee8c2aa3e2fdea2ea013e8ecfb7ae48",
	}
	recorded := 0
	for _, item := range corpus {
		if want, ok := expected[item.Case.ID]; ok {
			recorded++
			if item.Digest != want {
				t.Fatalf("recorded case %s changed: %s", item.Case.ID, item.Digest)
			}
		}
	}
	if recorded != len(expected) {
		t.Fatal("recorded core case was removed")
	}
	bounded, activeTool := 0, 0
	for _, item := range selected {
		if item.Case.Suite != "general" || item.Digest != expected[item.Case.ID] {
			t.Fatal("promotion admitted a domain or silently changed a frozen definition", item.Case.ID)
		}
		if requiresTargetBudget(item.Case) {
			bounded++
		}
		if requiresActiveTool(item.Case) {
			activeTool++
		}
	}
	if bounded != 4 || activeTool != 2 {
		t.Fatal("qualified C1/C2 requirements were removed or legacy contracts widened")
	}
	fixture := newAdmissionFixture(t)
	if _, err := Run(t.Context(), fixture.preset, selected, RunOptions{
		Home: fixture.home, OutputParent: fixture.output,
	}); !errors.Is(err, ErrUnsafeTarget) || fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
		t.Fatal("default promotion let an unbounded attach profile reach target work", err)
	}
}
