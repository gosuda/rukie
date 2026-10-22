package mirror

import (
	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
	"strings"
	"testing"
	"time"
)

func validMirror() Mirror {
	return Mirror{Name: "m", TargetURL: "https://example.com/repo.git", HostKind: HostGeneric, Mode: ModeExact, RefSelection: RefSelectionAll}
}
func TestMirrorValidate(t *testing.T) {
	cases := []struct {
		edit    func(*Mirror)
		name    string
		wantErr bool
	}{
		{edit: func(*Mirror) {}, name: "valid"}, {edit: func(m *Mirror) { m.Name = "" }, name: "empty name", wantErr: true}, {edit: func(m *Mirror) { m.Name = strings.Repeat("x", 101) }, name: "long name", wantErr: true}, {edit: func(m *Mirror) { m.Name = "a b" }, name: "space", wantErr: true}, {edit: func(m *Mirror) { m.Name = "a\nb" }, name: "control", wantErr: true}, {edit: func(m *Mirror) { m.TargetURL = "https://user:pass@example.com/x" }, name: "userinfo", wantErr: true}, {edit: func(m *Mirror) { m.TargetURL = "ssh://example.com/x" }, name: "scheme", wantErr: true}, {edit: func(m *Mirror) { m.TargetURL = "http://example.com/x" }, name: "http public", wantErr: true}, {edit: func(m *Mirror) { m.TargetURL = "http://127.0.0.1:123/x" }, name: "http loopback"}, {edit: func(m *Mirror) { m.TargetURL = "https://192.168.1.2/x" }, name: "private ip", wantErr: true}, {edit: func(m *Mirror) { m.HostKind = HostForgejo; m.TargetURL = "https://CodeBerg.org:443/x" }, name: "codeberg ack", wantErr: true}, {edit: func(m *Mirror) { m.HostKind = HostForgejo; m.TargetURL = "https://codeberg.org/x"; m.PolicyAck = true }, name: "codeberg accepted"}, {edit: func(m *Mirror) { m.HostKind = "wat" }, name: "host kind", wantErr: true}, {edit: func(m *Mirror) { m.Mode = "ff_only" }, name: "mode", wantErr: true}, {edit: func(m *Mirror) { m.RefSelection = "nope" }, name: "selection", wantErr: true}, {edit: func(m *Mirror) { m.RefSelection = RefSelectionGlobs; m.Globs = []string{"["} }, name: "glob", wantErr: true}, {edit: func(m *Mirror) { m.SyncOnRefUpdate = true; m.DebounceSeconds = 9 }, name: "debounce 9", wantErr: true}, {edit: func(m *Mirror) { m.SyncOnRefUpdate = true; m.DebounceSeconds = 10 }, name: "debounce 10"}, {edit: func(m *Mirror) { m.SyncOnRefUpdate = true; m.DebounceSeconds = 11 }, name: "debounce 11"}, {edit: func(m *Mirror) { m.DebounceSeconds = 11; m.MaxDelaySeconds = 10 }, name: "max less", wantErr: true}, {edit: func(m *Mirror) { m.ReconcileIntervalSeconds = 86401 }, name: "reconcile over 24h", wantErr: true}, {edit: func(m *Mirror) { m.ReconcileIntervalSeconds = 86400 }, name: "reconcile 24h"}, {edit: func(m *Mirror) { m.ReconcileIntervalSeconds = 0 }, name: "reconcile zero"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validMirror()
			tc.edit(&m)
			err := m.Validate(time.Now())
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
func TestMirrorJSONHasNoToken(t *testing.T) {
	b, err := jsonv2.Marshal(validMirror(), jsonv1.DefaultOptionsV1())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "token") || strings.Contains(string(b), "CANARY-") {
		t.Fatalf("unexpected secret field: %s", b)
	}
}
