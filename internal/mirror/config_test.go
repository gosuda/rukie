package mirror

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validMirror() Mirror {
	return Mirror{Name: "m", TargetURL: "https://example.com/repo.git", HostKind: HostGeneric, Mode: ModeExact, RefSelection: RefSelectionAll}
}
func TestMirrorValidate(t *testing.T) {
	cases := []struct {
		name    string
		edit    func(*Mirror)
		wantErr bool
	}{
		{"valid", func(*Mirror) {}, false}, {"empty name", func(m *Mirror) { m.Name = "" }, true}, {"long name", func(m *Mirror) { m.Name = strings.Repeat("x", 101) }, true}, {"space", func(m *Mirror) { m.Name = "a b" }, true}, {"control", func(m *Mirror) { m.Name = "a\nb" }, true}, {"userinfo", func(m *Mirror) { m.TargetURL = "https://user:pass@example.com/x" }, true}, {"scheme", func(m *Mirror) { m.TargetURL = "ssh://example.com/x" }, true}, {"http public", func(m *Mirror) { m.TargetURL = "http://example.com/x" }, true}, {"http loopback", func(m *Mirror) { m.TargetURL = "http://127.0.0.1:123/x" }, false}, {"private ip", func(m *Mirror) { m.TargetURL = "https://192.168.1.2/x" }, true}, {"codeberg ack", func(m *Mirror) { m.HostKind = HostForgejo; m.TargetURL = "https://CodeBerg.org:443/x" }, true}, {"codeberg accepted", func(m *Mirror) { m.HostKind = HostForgejo; m.TargetURL = "https://codeberg.org/x"; m.PolicyAck = true }, false}, {"host kind", func(m *Mirror) { m.HostKind = "wat" }, true}, {"mode", func(m *Mirror) { m.Mode = "ff_only" }, true}, {"selection", func(m *Mirror) { m.RefSelection = "nope" }, true}, {"glob", func(m *Mirror) { m.RefSelection = RefSelectionGlobs; m.Globs = []string{"["} }, true}, {"debounce 9", func(m *Mirror) { m.SyncOnRefUpdate = true; m.DebounceSeconds = 9 }, true}, {"debounce 10", func(m *Mirror) { m.SyncOnRefUpdate = true; m.DebounceSeconds = 10 }, false}, {"debounce 11", func(m *Mirror) { m.SyncOnRefUpdate = true; m.DebounceSeconds = 11 }, false}, {"max less", func(m *Mirror) { m.DebounceSeconds = 11; m.MaxDelaySeconds = 10 }, true},
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
	b, err := json.Marshal(validMirror())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "token") || strings.Contains(string(b), "CANARY-") {
		t.Fatalf("unexpected secret field: %s", b)
	}
}
