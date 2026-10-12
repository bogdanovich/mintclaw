package browser

import "testing"

func TestPlaywrightNativeObservationPreservesHierarchyAndExactSemantics(t *testing.T) {
	snapshot := `- main [ref=f3e1]:
  - region "Location map" [ref=f3e2]:
    - button "Full Screen" [ref=f3e3]
  - region "Product gallery" [ref=f3e4]:
    - button "Full Screen" [ref=f3e5]
    - img "Say \"hi\": \u005bref=e999]" [ref=f3e6]
  - iframe [ref=f3e7]:
    - button "Full Screen" [ref=f4e2]
- text: "DOM image evidence: decorative images are not product-photo counts."`
	text := "- Page URL: https://example.com/\n- Page Title: Fixture\n" +
		"- Snapshot encoding: json-names-v1\n### Snapshot\n```yaml\n" +
		snapshot + "\n```"
	observation, err := parsePlaywrightObservation(text, 8192, 32, testPlaywrightToolResultBytes)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Snapshot != snapshot || observation.Truncated || len(observation.Elements) != 8 {
		t.Fatalf("observation lost hierarchy or bindings: %+v", observation)
	}
	image := observation.Elements[5]
	if image.Target != "f3e6" || image.Role != "img" || image.Name != `Say "hi": [ref=e999]` {
		t.Fatalf("escaped native name was confused with reference authority: %+v", image)
	}
	if observation.Elements[2].Target == observation.Elements[4].Target ||
		observation.Elements[7].Target != "f4e2" {
		t.Fatalf("duplicate captions or frame refs were conflated: %+v", observation.Elements)
	}
	projected, err := parsePlaywrightObservation(text, 8192, 5, testPlaywrightToolResultBytes)
	if err != nil || !projected.Truncated || len(projected.Elements) != 5 ||
		projected.Elements[4].Target != "f3e5" {
		t.Fatalf("bounded native hierarchy projection = %+v, %v", projected, err)
	}
}

func TestPlaywrightObservationRetainsLegacyLiteralNames(t *testing.T) {
	for _, name := range []string{`C:\temp`, `C:\`, `C:\\`} {
		text := "- Page URL: https://example.com/\n- Page Title: Fixture\n### Snapshot\n```yaml\n" +
			`- button "` + name + `" [ref=e1]` + "\n```"
		observation, err := parsePlaywrightObservation(text, 1024, 2, testPlaywrightToolResultBytes)
		if err != nil || len(observation.Elements) != 1 || observation.Elements[0].Name != name {
			t.Fatalf("legacy driver name %q changed: %+v, %v", name, observation, err)
		}
	}
}
