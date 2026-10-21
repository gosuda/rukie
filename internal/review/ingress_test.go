package review

import (
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
	spans := SpansFromPatch(map[string]string{"a.txt": patch})
	got, ok := spans["a.txt"]
	if !ok {
		t.Fatal("no spans for a.txt")
	}
	if len(got) != 3 {
		t.Fatalf("spans = %+v, want two context runs plus the inter-hunk gap", got)
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
		t.Fatal("line past the last hunk remaps")
	}
}

func TestSpansFromPatchEmpty(t *testing.T) {
	if spans := SpansFromPatch(nil); len(spans) != 0 {
		t.Fatalf("nil patches yield %d spans", len(spans))
	}
	spans := SpansFromPatch(map[string]string{"a.txt": "not a patch\n"})
	if len(spans) != 0 {
		t.Fatalf("hunkless patch yields %+v", spans)
	}
}
