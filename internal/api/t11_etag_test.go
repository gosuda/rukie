package api

import (
	"testing"
)

func TestBranchTagETagPrefixAccepted(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	if !etagMatches(`"`+sha+`"`, `"`+sha+`"`) {
		t.Fatalf("sanity: exact sha must match")
	}
	if !etagMatches("\"branch-"+sha+"\"", "\"branch-"+sha+"\"") {
		t.Fatalf("branch prefixed ETag must match itself")
	}
	if etagMatches("\"branch-deadbeef\"", "\""+sha+"\"") {
		t.Fatalf("wrong target must not match")
	}
	cases := []struct {
		header  string
		current string
		prefix  string
		want    bool
	}{
		{`"` + sha + `"`, sha, "branch-", true},
		{`"branch-` + sha + `"`, sha, "branch-", true},
		{`"tag-` + sha + `"`, sha, "tag-", true},
		{`"branch-wrong"`, sha, "branch-", false},
		{`*`, sha, "branch-", true},
	}
	for _, c := range cases {
		got := etagMatches(c.header, `"`+c.current+`"`) || etagMatches(c.header, `"`+c.prefix+c.current+`"`)
		if got != c.want {
			t.Fatalf("header %s current %s: got %v want %v", c.header, c.current, got, c.want)
		}
	}
}
