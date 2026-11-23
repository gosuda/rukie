package review

import (
	"math"
	"testing"
)

func TestIngressTarget(t *testing.T) {
	branch, topic, ok := IngressTarget("refs/for/main")
	if !ok || branch != "main" || topic != "" {
		t.Fatalf("target = (%q, %q, %v)", branch, topic, ok)
	}
	branch, topic, ok = IngressTarget("refs/for/main/docs")
	if !ok || branch != "main" || topic != "docs" {
		t.Fatalf("topic target = (%q, %q, %v)", branch, topic, ok)
	}
	if _, _, ok := IngressTarget("refs/for"); ok {
		t.Fatal("bare refs/for names a target")
	}
	if _, _, ok := IngressTarget("refs/heads/main"); ok {
		t.Fatal("plain branch is a review target")
	}
	if _, _, ok := IngressTarget("refs/for/../escape"); ok {
		t.Fatal("traversal branch is a target")
	}
}

func TestSpansFromPatch(t *testing.T) {
	patch := `--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,4 @@
 line1
+added
 line2
 line3
@@ -10,2 +11,1 @@
-old10
-old11
+new10
`
	spans, err := SpansFromPatch(map[string]string{"a.txt": patch})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := spans["a.txt"]
	if !ok {
		t.Fatal("no spans for a.txt")
	}
	if len(got) != 4 {
		t.Fatalf("spans = %+v, want context runs, the inter-hunk gap, and the final tail", got)
	}
	if got[0] != (Span{OldStart: 1, NewStart: 1, Lines: 1}) {
		t.Fatalf("span 0 = %+v, want old 1 to new 1", got[0])
	}
	if got[1] != (Span{OldStart: 2, NewStart: 3, Lines: 2}) {
		t.Fatalf("span 1 = %+v, want old 2..3 to new 3..4", got[1])
	}
	if got[2] != (Span{OldStart: 4, NewStart: 5, Lines: 6}) {
		t.Fatalf("span 2 = %+v, want old 4..9 to new 5..10", got[2])
	}
	if _, ok := RemapLine(5, got); !ok {
		t.Fatal("line 5 does not remap")
	}
	if mapped, ok := RemapLine(2, got); !ok || mapped != 3 {
		t.Fatalf("context line 2 maps to (%d, %v), want (3, true)", mapped, ok)
	}
	if _, ok := RemapLine(10, got); ok {
		t.Fatal("removed line 10 remaps")
	}
	if _, ok := RemapLine(11, got); ok {
		t.Fatal("second removed line remaps")
	}
}

func TestSpansFromPureInsertion(t *testing.T) {
	patch := `--- a/a.txt
+++ b/a.txt
@@ -2,0 +3,2 @@
+new3
+new4
`
	spans, err := SpansFromPatch(map[string]string{"a.txt": patch})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := spans["a.txt"]
	if !ok {
		t.Fatal("no spans for a.txt")
	}
	if len(got) != 2 {
		t.Fatalf("spans = %+v, want the leading run and shifted tail", got)
	}
	if got[0] != (Span{OldStart: 1, NewStart: 1, Lines: 2}) {
		t.Fatalf("span 0 = %+v, want old 1..2 to new 1..2", got[0])
	}
	if mapped, ok := RemapLine(2, got); !ok || mapped != 2 {
		t.Fatalf("line before insertion maps to (%d, %v)", mapped, ok)
	}
	if mapped, ok := RemapLine(3, got); !ok || mapped != 5 {
		t.Fatalf("line after insertion maps to (%d, %v)", mapped, ok)
	}
	if mapped, ok := RemapLine(1, got); !ok || mapped != 1 {
		t.Fatalf("old line 1 maps to (%d, %v), want (1, true)", mapped, ok)
	}
}

func TestSpansFromPatchEmpty(t *testing.T) {
	if spans, err := SpansFromPatch(nil); err != nil || len(spans) != 0 {
		t.Fatalf("nil patches = (%v, %v)", spans, err)
	}
	for _, patch := range []string{"", "--- a/a.txt\n+++ b/a.txt\n"} {
		spans, err := SpansFromPatch(map[string]string{"a.txt": patch})
		if err != nil {
			t.Fatal(err)
		}
		if mapped, ok := RemapLine(math.MaxUint32, spans["a.txt"]); !ok || mapped != math.MaxUint32 {
			t.Fatalf("unchanged file maps last line to (%d, %v)", mapped, ok)
		}
	}
	if _, err := SpansFromPatch(map[string]string{"a.txt": "not a patch\n"}); err == nil {
		t.Fatal("malformed patch accepted")
	}
}

func TestSpansFromPatchPreservesTail(t *testing.T) {
	patch := "@@ -1,2 +1,3 @@\n first\n+inserted\n second\n"
	spans, err := SpansFromPatch(map[string]string{"a.txt": patch})
	if err != nil {
		t.Fatal(err)
	}
	for old, want := range map[uint32]uint32{2: 3, 3: 4, 100: 101, math.MaxUint32 - 1: math.MaxUint32} {
		if got, ok := RemapLine(old, spans["a.txt"]); !ok || got != want {
			t.Fatalf("tail line %d = (%d, %v), want %d", old, got, ok, want)
		}
	}
	if got, ok := RemapLine(math.MaxUint32, spans["a.txt"]); ok || got != 0 {
		t.Fatalf("overflowing tail mapped to (%d, %v)", got, ok)
	}
}

func TestSpansFromPatchTreatsHeaderLikeContentAsLines(t *testing.T) {
	patch := "--- a/a.txt\n+++ b/a.txt\n@@ -1,2 +1,2 @@\n--- removed\n+++ added\n kept\n"
	spans, err := SpansFromPatch(map[string]string{"a.txt": patch})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := RemapLine(2, spans["a.txt"]); !ok || got != 2 {
		t.Fatalf("line after header-like content = (%d, %v)", got, ok)
	}
	if _, ok := RemapLine(1, spans["a.txt"]); ok {
		t.Fatal("replaced header-like content remapped")
	}
}

func TestSpansFromPatchRejectsTruncationAndOverflow(t *testing.T) {
	for _, patch := range []string{
		"@@ -1,2 +1,2 @@\n one\n",
		"@@ -1 +1 @@\n one\n extra\n",
		"@@ -4294967295,2 +4294967295,2 @@\n one\n two\n",
		"@@ -9223372036854775807,1 +1,1 @@\n one\n",
		"@@ -1 +2 @@\n one\n",
		"--- \n+++ \n",
		"--- a/a.txt\n",
		"--- a/a.txt\n+++ b/other.txt\n",
		"--- /dev/null\n+++ /dev/null\n",
		"--- a/a.txt\n@@ -1 +1 @@\n one\n",
	} {
		if _, err := SpansFromPatch(map[string]string{"a.txt": patch}); err == nil {
			t.Fatalf("invalid patch accepted: %q", patch)
		}
	}
}

func TestSpansFromPatchDoesNotInventDeletedFileTail(t *testing.T) {
	patch := "--- a/a.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-gone\n"
	spans, err := SpansFromPatch(map[string]string{"a.txt": patch})
	if err != nil {
		t.Fatal(err)
	}
	if len(spans["a.txt"]) != 0 {
		t.Fatalf("deleted file has spans: %v", spans)
	}
}

func TestSpansFromPatchPreservesRawPathLabels(t *testing.T) {
	for _, path := range []string{"odd\nname.txt", "odd\xffname.txt", "odd\n@@ -99 +99 @@\n+++ marker.txt"} {
		header := "--- a/" + path + "\n+++ b/" + path + "\n"
		spans, err := SpansFromPatch(map[string]string{path: header + "@@ -1 +1,2 @@\n+inserted\n kept\n"})
		if err != nil {
			t.Fatalf("path %q: %v", path, err)
		}
		if got, ok := RemapLine(1, spans[path]); !ok || got != 2 {
			t.Fatalf("path %q line = (%d, %v), want (2, true)", path, got, ok)
		}
		spans, err = SpansFromPatch(map[string]string{path: header})
		if err != nil {
			t.Fatalf("unchanged path %q: %v", path, err)
		}
		if got, ok := RemapLine(math.MaxUint32, spans[path]); !ok || got != math.MaxUint32 {
			t.Fatalf("unchanged path %q last line = (%d, %v)", path, got, ok)
		}
	}
}

func TestSpansFromPatchHandlesRawAddedAndDeletedPaths(t *testing.T) {
	path := "odd\n\xffname.txt"
	for _, patch := range []string{
		"--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1 @@\n+new\n",
		"--- a/" + path + "\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n",
	} {
		spans, err := SpansFromPatch(map[string]string{path: patch})
		if err != nil {
			t.Fatal(err)
		}
		if len(spans[path]) != 0 {
			t.Fatalf("added or deleted file has spans: %v", spans)
		}
	}
}

func TestSpansFromPatchRejectsMismatchedRawPathLabels(t *testing.T) {
	path := "odd\n\xffname.txt"
	for _, patch := range []string{
		"--- a/" + path + "\n+++ b/other.txt\n",
		"--- a/" + path + "\n+++ b/" + path,
		"--- a/odd\n\ufffdname.txt\n+++ b/odd\n\ufffdname.txt\n",
	} {
		if _, err := SpansFromPatch(map[string]string{path: patch}); err == nil {
			t.Fatalf("mismatched file headers accepted: %q", patch)
		}
	}
}
