package ruk

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

const (
	testCanary = "CANARY-7f3a-s3cret"
	testOID    = "0123456789abcdef0123456789abcdef01234567"
	testOID2   = "abcdef0123456789abcdef0123456789abcdef01"
)

func mustMarshalJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := jsonv2.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func mustUnmarshal[T any](t *testing.T, doc string) T {
	t.Helper()
	var out T
	if err := jsonv2.Unmarshal([]byte(doc), &out, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("unmarshal %s: %v", doc, err)
	}
	return out
}

func TestTransportAuthMarshalGolden(t *testing.T) {
	auth := wire.TransportAuth{
		Kind:       wire.TransportAuthBasic,
		Credential: "alice:" + testCanary,
		Origin:     "https://host.x:443",
	}
	want := `{"kind":"basic","credential":"alice:` + testCanary + `","origin":"https://host.x:443"}`
	if got := mustMarshalJSON(t, auth); got != want {
		t.Fatalf("auth JSON = %s, want %s", got, want)
	}
}

func TestInvocationOpenAuthOmission(t *testing.T) {
	bare := mustMarshalJSON(t, wire.InvocationOpen{Repo: "r", Method: "push"})
	if want := `{"repo":"r","method":"push"}`; bare != want {
		t.Fatalf("bare open = %s, want %s", bare, want)
	}
	auth := wire.TransportAuth{
		Kind:       wire.TransportAuthBearer,
		Credential: "tok",
		Origin:     "https://host.x:443",
	}
	with := mustMarshalJSON(t, wire.InvocationOpen{Repo: "r", Method: "push", TransportAuth: &auth})
	want := `{"repo":"r","method":"push","transport_auth":{"kind":"bearer","credential":"tok","origin":"https://host.x:443"}}`
	if with != want {
		t.Fatalf("auth open = %s, want %s", with, want)
	}
}

func TestAuthPointerNilCases(t *testing.T) {
	if got := authPointer(wire.TransportAuth{}); got != nil {
		t.Fatalf("zero auth pointer = %+v, want nil", got)
	}
	if got := authPointer(wire.TransportAuth{Kind: wire.TransportAuthNone}); got != nil {
		t.Fatalf("none auth pointer = %+v, want nil", got)
	}
	full := wire.TransportAuth{Kind: wire.TransportAuthBasic, Credential: "u:p", Origin: "https://host.x:443"}
	got := authPointer(full)
	if got == nil {
		t.Fatal("full auth pointer = nil, want non-nil")
	}
	if *got != full {
		t.Fatalf("auth pointer = %+v, want %+v", *got, full)
	}
	if got := authPointer(wire.TransportAuth{Kind: wire.TransportAuthBasic}); got == nil {
		t.Fatal("explicit kind without credential must be sent through, got nil")
	}
}

func TestTransportAuthStringRedacts(t *testing.T) {
	auth := wire.TransportAuth{
		Kind:       wire.TransportAuthBasic,
		Credential: "alice:" + testCanary,
		Origin:     "https://host.x:443",
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		got := fmt.Sprintf(format, auth)
		if strings.Contains(got, testCanary) {
			t.Fatalf("String %s leaks credential: %s", format, got)
		}
		if !strings.Contains(got, "basic") || !strings.Contains(got, "https://host.x:443") {
			t.Fatalf("String %s hides kind/origin: %s", format, got)
		}
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("call", "auth", auth)
	if out := buf.String(); strings.Contains(out, testCanary) {
		t.Fatalf("slog output leaks credential: %s", out)
	} else if !strings.Contains(out, "basic") || !strings.Contains(out, "https://host.x:443") {
		t.Fatalf("slog output hides kind/origin: %s", out)
	}
}

func TestTransportAuthValidate(t *testing.T) {
	rows := []struct {
		name  string
		auth  wire.TransportAuth
		valid bool
	}{
		{"zero defaults to none", wire.TransportAuth{}, true},
		{"none", wire.TransportAuth{Kind: wire.TransportAuthNone}, true},
		{"none ignores halves", wire.TransportAuth{Kind: wire.TransportAuthNone, Credential: "x", Origin: "y"}, true},
		{"basic full", wire.TransportAuth{Kind: wire.TransportAuthBasic, Credential: "u:p", Origin: "https://host.x:443"}, true},
		{"bearer full", wire.TransportAuth{Kind: wire.TransportAuthBearer, Credential: "tok", Origin: "http://host.x:80"}, true},
		{"explicit kind needs credential", wire.TransportAuth{Kind: wire.TransportAuthBasic, Origin: "https://host.x:443"}, false},
		{"unknown kind rejected", wire.TransportAuth{Kind: "weird", Credential: "u:p", Origin: "https://host.x:443"}, false},
		{"explicit kind needs origin", wire.TransportAuth{Kind: wire.TransportAuthBasic, Credential: "u:p"}, false},
		{"origin needs scheme and port", wire.TransportAuth{Kind: wire.TransportAuthBasic, Credential: "u:p", Origin: "https://host.x"}, false},
		{"origin rejects bad scheme", wire.TransportAuth{Kind: wire.TransportAuthBasic, Credential: "u:p", Origin: "ftp://host.x:21"}, false},
		{"origin rejects unnormalized", wire.TransportAuth{Kind: wire.TransportAuthBasic, Credential: "u:p", Origin: "HTTPS://HOST.X:443"}, false},
	}
	for _, row := range rows {
		err := row.auth.Validate()
		if row.valid && err != nil {
			t.Fatalf("%s: Validate = %v, want nil", row.name, err)
		}
		if !row.valid && err == nil {
			t.Fatalf("%s: Validate = nil, want error", row.name)
		}
		if err != nil && strings.Contains(err.Error(), testCanary) {
			t.Fatalf("%s: error leaks credential", row.name)
		}
	}
	bad := wire.TransportAuth{Kind: wire.TransportAuthBasic, Credential: "u:" + testCanary}
	if err := bad.Validate(); err == nil {
		t.Fatal("half-specified auth validated")
	} else if strings.Contains(err.Error(), testCanary) {
		t.Fatalf("Validate error leaks credential: %v", err)
	}
}

func TestOriginOf(t *testing.T) {
	rows := []struct {
		url     string
		want    string
		wantErr bool
	}{
		{"https://host.x/a.git", "https://host.x:443", false},
		{"http://host.x/a", "http://host.x:80", false},
		{"https://host.x:8443/a", "https://host.x:8443", false},
		{"http://host.x:8080/a", "http://host.x:8080", false},
		{"https://host.x:443/a", "https://host.x:443", false},
		{"http://host.x:0/a", "http://host.x:80", false},
		{"https://[::1]/a", "https://::1:443", false},
		{"https://[2001:db8::1]:9443/x", "https://2001:db8::1:9443", false},
		{"HTTPS://HOST.X/A", "https://host.x:443", false},
		{"https://user:pass@host.x/a", "https://host.x:443", false},
		{"https://a:b@c:d@host.x:1234/a", "https://host.x:1234", false},
		{"https://host.x:/x", "https://host.x::443", false},
		{"ftp://host.x/a", "", true},
		{"not a url", "", true},
		{"", "", true},
		{"https://", "", true},
		{"https:///a", "", true},
		{"https://:8080/x", "", true},
		{"https://[::1/x", "", true},
		{"https://[::1]junk/x", "", true},
		{"https://[]/x", "", true},
		{"https://host.x:99999/x", "", true},
		{"https://[::1]:99999/x", "", true},
	}
	for _, row := range rows {
		got, err := wire.OriginOf(row.url)
		if row.wantErr && err == nil {
			t.Fatalf("OriginOf(%q) = %q, want error", row.url, got)
		}
		if !row.wantErr {
			if err != nil {
				t.Fatalf("OriginOf(%q) error = %v", row.url, err)
			}
			if got != row.want {
				t.Fatalf("OriginOf(%q) = %q, want %q", row.url, got, row.want)
			}
		}
	}
}

func TestAuthForMirror(t *testing.T) {
	auth, err := wire.AuthForMirror("ada", "tok", "https://Host.X/r.git")
	if err != nil {
		t.Fatalf("AuthForMirror error = %v", err)
	}
	if auth.Kind != wire.TransportAuthBasic || auth.Credential != "ada:tok" || auth.Origin != "https://host.x:443" {
		t.Fatalf("AuthForMirror = %+v, want basic ada:tok scoped to https://host.x:443", auth)
	}
	for _, row := range []struct {
		name             string
		user, token, url string
	}{
		{"empty user", "", "tok", "https://host.x/r.git"},
		{"empty token", "ada", "", "https://host.x/r.git"},
		{"userinfo refused", "ada", "tok", "https://ada:" + testCanary + "@host.x/r.git"},
		{"non-http refused", "ada", "tok", "ftp://host.x/r.git"},
		{"scp-like refused", "ada", "tok", "git@host.x:r.git"},
	} {
		_, err := wire.AuthForMirror(row.user, row.token, row.url)
		if err == nil {
			t.Fatalf("%s: AuthForMirror accepted %q", row.name, row.url)
		}
		if strings.Contains(err.Error(), testCanary) {
			t.Fatalf("%s: error leaks credential: %v", row.name, err)
		}
	}
}

func TestPushSourceRoundTrip(t *testing.T) {
	if got := mustMarshalJSON(t, DeleteSource()); got != `"delete"` {
		t.Fatalf("delete source = %s", got)
	}
	if got := mustUnmarshal[PushSource](t, `"delete"`); !got.IsDelete() {
		t.Fatalf("delete source decoded as commit: %+v", got)
	}
	commit := CommitSource(testOID)
	if got := mustMarshalJSON(t, commit); got != `{"commit":{"oid":"`+testOID+`"}}` {
		t.Fatalf("commit source = %s", got)
	}
	if got := mustUnmarshal[PushSource](t, `{"commit":{"oid":"`+testOID+`"}}`); got.Commit == nil || *got.Commit != testOID {
		t.Fatalf("commit source decoded as %+v", got)
	}
	for _, doc := range []string{`"bogus"`, `{"commit":{}}`, `{"change":{"change":"x"}}`, `123`, `null`, `{"commit":{"oid":123}}`} {
		var out PushSource
		if err := jsonv2.Unmarshal([]byte(doc), &out, jsonv1.DefaultOptionsV1()); err == nil {
			t.Fatalf("PushSource accepted %s", doc)
		}
	}
}

func TestRemoteExpectRoundTrip(t *testing.T) {
	if got := mustMarshalJSON(t, AbsentExpect()); got != `"absent"` {
		t.Fatalf("absent = %s", got)
	}
	present := PresentExpect(testOID)
	if got := mustMarshalJSON(t, present); got != `{"present":{"oid":"`+testOID+`"}}` {
		t.Fatalf("present = %s", got)
	}
	if got := mustUnmarshal[RemoteExpect](t, `"absent"`); !got.IsAbsent() {
		t.Fatalf("absent decoded as present: %+v", got)
	}
	for _, doc := range []string{`"bogus"`, `{"present":{}}`, `{"absent":{}}`, `null`, `42`} {
		var out RemoteExpect
		if err := jsonv2.Unmarshal([]byte(doc), &out, jsonv1.DefaultOptionsV1()); err == nil {
			t.Fatalf("RemoteExpect accepted %s", doc)
		}
	}
}

func TestPushRefStatusRoundTrip(t *testing.T) {
	if got := mustMarshalJSON(t, UncertainStatus()); got != `"uncertain"` {
		t.Fatalf("uncertain = %s", got)
	}
	if got := mustUnmarshal[PushRefStatus](t, `"uncertain"`); got.Kind != "uncertain" {
		t.Fatalf("uncertain decoded as %+v", got)
	}
	confirmed := ConfirmedStatus(&[]string{testOID}[0])
	if got := mustMarshalJSON(t, confirmed); got != `{"confirmed":{"new":"`+testOID+`"}}` {
		t.Fatalf("confirmed = %s", got)
	}
	if got := mustMarshalJSON(t, ConfirmedStatus(nil)); got != `{"confirmed":{"new":null}}` {
		t.Fatalf("confirmed delete = %s", got)
	}
	if got := mustUnmarshal[PushRefStatus](t, `{"confirmed":{}}`); got.Kind != "confirmed" || got.New != nil {
		t.Fatalf("empty confirmed decoded as %+v", got)
	}
	rejected := RejectedStatus("non-fast-forward")
	if got := mustMarshalJSON(t, rejected); got != `{"rejected":{"reason":"non-fast-forward"}}` {
		t.Fatalf("rejected = %s", got)
	}
	if got := mustUnmarshal[PushRefStatus](t, `{"rejected":{"reason":"stale"}}`); got.Kind != "rejected" || got.Reason != "stale" {
		t.Fatalf("rejected decoded as %+v", got)
	}
	for _, doc := range []string{`"bogus"`, `{"confirmed":{"new":123}}`, `{"rejected":{}}`, `{"bogus":1}`, `42`, `null`} {
		var out PushRefStatus
		if err := jsonv2.Unmarshal([]byte(doc), &out, jsonv1.DefaultOptionsV1()); err == nil {
			t.Fatalf("PushRefStatus accepted %s", doc)
		}
	}
}

func TestPushArgsMarshalGolden(t *testing.T) {
	present := testOID2
	args := PushArgs{
		RemoteURL: "https://host.x/r.git",
		Updates: []PushUpdate{
			{RemoteRef: "refs/heads/main", Source: CommitSource(testOID), ExpectedOld: &RemoteExpect{Present: &present}},
			{RemoteRef: "refs/heads/old", Source: DeleteSource(), Force: true},
		},
	}
	want := `{"remote":{"url":{"url":"https://host.x/r.git"}},"updates":[` +
		`{"remote_ref":"refs/heads/main","source":{"commit":{"oid":"` + testOID + `"}},"expected_old":{"present":{"oid":"` + testOID2 + `"}},"force":false},` +
		`{"remote_ref":"refs/heads/old","source":"delete","expected_old":null,"force":true}],"atomicity":"non_atomic"}`
	if got := mustMarshalJSON(t, args); got != want {
		t.Fatalf("push args = %s, want %s", got, want)
	}
	explicit := PushArgs{RemoteURL: "https://host.x/r.git", Atomicity: "non_atomic"}
	if got, want := mustMarshalJSON(t, explicit), `{"remote":{"url":{"url":"https://host.x/r.git"}},"updates":[],"atomicity":"non_atomic"}`; got != want {
		t.Fatalf("empty push args = %s, want %s", got, want)
	}
}

func TestPushReplyDecodeEnvelope(t *testing.T) {
	doc := `{"outcome":{},"plan":"11111111-2222-3333-4444-555555555555","state":"uncertain",` +
		`"refs":{"total":2,"truncated":false,"items":[` +
		`{"remote_ref":"refs/heads/main","status":"uncertain"},` +
		`{"remote_ref":"refs/heads/old","status":{"confirmed":{"new":null}}}]}}`
	reply := mustUnmarshal[PushReply](t, doc)
	if reply.Plan != "11111111-2222-3333-4444-555555555555" || reply.State != "uncertain" {
		t.Fatalf("reply header = %+v", reply)
	}
	if len(reply.Refs) != 2 {
		t.Fatalf("refs = %+v", reply.Refs)
	}
	if reply.Refs[0].Status.Kind != "uncertain" {
		t.Fatalf("first status = %+v", reply.Refs[0].Status)
	}
	if second := reply.Refs[1].Status; second.Kind != "confirmed" || second.New != nil {
		t.Fatalf("second status = %+v", second)
	}
	for _, doc := range []string{
		`{"plan":"p","state":"s","refs":"nope"}`,
		`{"plan":"p","state":"s","refs":[1,2]}`,
		`{"plan":"p","state":"s"}`,
		`not json`,
	} {
		var out PushReply
		if err := jsonv2.Unmarshal([]byte(doc), &out, jsonv1.DefaultOptionsV1()); err == nil {
			t.Fatalf("PushReply accepted %s", doc)
		}
	}
}

func TestPushReconcileReplyDecodeEnvelope(t *testing.T) {
	doc := `{"outcome":{},"plan":"11111111-2222-3333-4444-555555555555","state":"confirmed",` +
		`"observations":{"total":2,"truncated":false,"items":[` +
		`{"remote_ref":"refs/heads/main","verdict":"equal_new","observed":"` + testOID + `"},` +
		`{"remote_ref":"refs/heads/gone","verdict":"moved","observed":null}]}}`
	reply := mustUnmarshal[PushReconcileReply](t, doc)
	if reply.Plan != "11111111-2222-3333-4444-555555555555" || reply.State != "confirmed" {
		t.Fatalf("reconcile header = %+v", reply)
	}
	if len(reply.Observations) != 2 {
		t.Fatalf("observations = %+v", reply.Observations)
	}
	first := reply.Observations[0]
	if first.Verdict != "equal_new" || first.Observed == nil || *first.Observed != testOID {
		t.Fatalf("first observation = %+v", first)
	}
	if second := reply.Observations[1]; second.Verdict != "moved" || second.Observed != nil {
		t.Fatalf("second observation = %+v", second)
	}
	var bad PushReconcileReply
	if err := jsonv2.Unmarshal([]byte(`{"plan":"p","state":"s","observations":"nope"}`), &bad, jsonv1.DefaultOptionsV1()); err == nil {
		t.Fatal("PushReconcileReply accepted string observations")
	}
}

func TestRemoteRefsReplyDecode(t *testing.T) {
	doc := `{"view":{"store":"11111111-2222-3333-4444-555555555555","view":7},` +
		`"refs":[{"name":"refs/heads/main","target":{"direct":"` + testOID + `"}}]}`
	reply := mustUnmarshal[RemoteRefsReply](t, doc)
	if reply.View.View != 7 || reply.View.Store != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("view = %+v", reply.View)
	}
	if len(reply.Refs) != 1 || reply.Refs[0].Name != "refs/heads/main" {
		t.Fatalf("refs = %+v", reply.Refs)
	}
	if got := reply.Refs[0].Target.Hex(); got != testOID {
		t.Fatalf("target = %q", got)
	}
	if got := mustMarshalJSON(t, RemoteRefsArgs{URL: "https://host.x/r.git"}); got != `{"url":"https://host.x/r.git"}` {
		t.Fatalf("remote.refs args = %s", got)
	}
}

func TestHTTPDetailOf(t *testing.T) {
	detailOf := func(doc string) *Error {
		return &Error{Detail: jsonv1.RawMessage(doc)}
	}
	full, ok := HTTPDetailOf(detailOf(`{"http":{"status":429,"retry_after_seconds":7,"location":null,"body_preview":"slow down"}}`))
	if !ok {
		t.Fatal("HTTPDetailOf missed an http envelope")
	}
	if full.Status != 429 || full.BodyPreview != "slow down" {
		t.Fatalf("detail = %+v", full)
	}
	if full.RetryAfterSeconds == nil || *full.RetryAfterSeconds != 7 {
		t.Fatalf("retry_after = %+v", full.RetryAfterSeconds)
	}
	if full.Location != nil {
		t.Fatalf("location = %+v, want nil", full.Location)
	}
	moved, ok := HTTPDetailOf(detailOf(`{"http":{"status":301,"body_preview":"","location":"https://new.example/r.git"}}`))
	if !ok || moved.Status != 301 || moved.Location == nil || *moved.Location != "https://new.example/r.git" {
		t.Fatalf("moved detail = %+v ok=%v", moved, ok)
	}
	wrapped := fmt.Errorf("push: %w", &refusalError{err: detailOf(`{"http":{"status":503,"body_preview":"down"}}`)})
	if got, ok := HTTPDetailOf(wrapped); !ok || got.Status != 503 {
		t.Fatalf("wrapped detail = %+v ok=%v", got, ok)
	}
	for _, row := range []struct {
		err  error
		name string
	}{
		{nil, "nil"},
		{errors.New("boom"), "plain"},
		{&Error{}, "no detail"},
		{detailOf(`{"push":{"plan":"p","state":"s","refs":[]}}`), "push detail"},
		{detailOf(`{"http":null}`), "null http"},
		{detailOf(`not json`), "garbage"},
	} {
		if _, ok := HTTPDetailOf(row.err); ok {
			t.Fatalf("%s: HTTPDetailOf = true, want false", row.name)
		}
	}
}
