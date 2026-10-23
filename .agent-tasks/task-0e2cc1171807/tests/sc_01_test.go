package tests

import "testing"

func TestSC01(t *testing.T) {
	t.Fatal("SC-01 unmet: git grep -niI gitea prints no line outside LICENSE, docs/plan, tasks, .agent-tasks")
}
