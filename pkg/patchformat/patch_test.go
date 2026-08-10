package patchformat

import (
	"strings"
	"testing"
)

func TestParseAndPrepareCodexPatch(t *testing.T) {
	operations, err := Parse(`*** Begin Patch
*** Add File: added.txt
+new
*** Update File: changed.txt
@@
-old
+new
*** Delete File: removed.txt
*** End Patch`, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 3 || operations[0].Kind != Add || operations[1].Kind != Update ||
		operations[2].Kind != Delete {
		t.Fatalf("operations = %#v", operations)
	}
	added, err := Prepare(operations[0], nil, false)
	if err != nil || string(added.After) != "new\n" {
		t.Fatalf("added = %#v, err = %v", added, err)
	}
	updated, err := Prepare(operations[1], []byte("old\n"), true)
	if err != nil || string(updated.After) != "new\n" {
		t.Fatalf("updated = %#v, err = %v", updated, err)
	}
	deleted, err := Prepare(operations[2], []byte("gone\n"), true)
	if err != nil || deleted.Action != string(Delete) || string(deleted.Before) != "gone\n" {
		t.Fatalf("deleted = %#v, err = %v", deleted, err)
	}
}

func TestParseRejectsMoveDuplicateLimitAndAmbiguousHunk(t *testing.T) {
	move := "*** Begin Patch\n*** Update File: a\n*** Move to: b\n*** End Patch"
	if _, err := Parse(move, 1); err == nil {
		t.Fatal("move patch passed")
	}
	overLimit := "*** Begin Patch\n*** Add File: a\n+x\n*** Add File: b\n+y\n*** End Patch"
	if _, err := Parse(overLimit, 1); err == nil {
		t.Fatal("over-limit patch passed")
	}
	operation := Operation{Kind: Update, Path: "a", Lines: []string{"@@", "-same", "+changed"}}
	if _, err := Prepare(operation, []byte(strings.Repeat("same\n", 2)), true); err == nil {
		t.Fatal("ambiguous update hunk passed")
	}
}
