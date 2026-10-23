package tests

import "testing"

func TestSC02(t *testing.T) {
	t.Fatal("SC-02 unmet: go list -deps lists no gitea/go-git/git2go path and no exec.Command(\"git\") in non-test Go files")
}
