package toolshared

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/taskresult"
)

func TestRetainedAttachmentModelNoteDoesNotExposeRecoveryOrPaths(t *testing.T) {
	result := (&ToolResult{ForLLM: "Complete business report."}).WithDeliverable(&taskresult.Deliverable{
		Artifacts: []taskresult.Artifact{{
			Ref: "media://retained", LocalPath: "/private/runtime/file.png", Kind: "image",
			Recovery: &taskresult.ArtifactRecovery{
				Kind: "browser_screenshot_claim", ActorID: "private_actor", ToolCallID: "private_call",
			},
		}},
	})
	content := result.ContentForLLM()
	for _, private := range []string{"private_actor", "private_call", "browser_screenshot_claim", "/private/runtime"} {
		if strings.Contains(content, private) {
			t.Fatalf("model content exposed %q: %s", private, content)
		}
	}
	if !strings.Contains(content, "runtime retained an attachment") ||
		!strings.Contains(content, "Complete business report.") ||
		result.Deliverable.Artifacts[0].Recovery == nil {
		t.Fatalf("runtime proof lost or missing safe delivery guidance: %s", content)
	}
}
