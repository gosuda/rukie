package ruk

import (
	"fmt"
	"testing"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
)

func TestPushReconcileOriginalRemoteIsExplicit(t *testing.T) {
	if got := mustMarshalJSON(t, PushReconcileArgs{Plan: "plan"}); got != `{"plan":"plan"}` {
		t.Fatalf("automatic reconcile supplied a historical destination: %s", got)
	}
	remote := "https://original.example/repository.git"
	args := PushReconcileArgs{Plan: "plan", OriginalRemote: &remote}
	if got := mustMarshalJSON(t, args); got != `{"original_remote":"https://original.example/repository.git","plan":"plan"}` {
		t.Fatalf("explicit recovery destination = %s", got)
	}
}

func TestPushReconcileRewrittenFlag(t *testing.T) {
	for _, test := range []struct {
		field string
		want  bool
	}{
		{field: ""},
		{field: `,"rewritten":false`},
		{field: `,"rewritten":true`, want: true},
		{field: ",\"rewritten\": \n true \t", want: true},
	} {
		t.Run(test.field, func(t *testing.T) {
			doc := `{"plan":"plan","state":"uncertain","observations":{"items":[]}` + test.field + `}`
			reply := PushReconcileReply{Rewritten: true}
			if err := jsonv2.Unmarshal([]byte(doc), &reply, jsonv1.DefaultOptionsV1()); err != nil {
				t.Fatal(err)
			}
			if reply.Plan != "plan" || reply.Rewritten != test.want {
				t.Fatalf("reconcile rewrite marker = %+v, want %v", reply, test.want)
			}
		})
	}
}

func TestPushReconcileRejectsMalformedRewrittenFlag(t *testing.T) {
	for _, value := range []string{`null`, `"true"`, `"false"`, `0`, `1`, `[]`, `{}`} {
		t.Run(value, func(t *testing.T) {
			doc := fmt.Sprintf(`{"plan":"plan","state":"uncertain","observations":{"items":[]},"rewritten":%s}`, value)
			reply := PushReconcileReply{Plan: "unchanged", Rewritten: true}
			if err := jsonv2.Unmarshal([]byte(doc), &reply, jsonv1.DefaultOptionsV1()); err == nil {
				t.Fatalf("accepted malformed rewritten flag %s", value)
			}
			if reply.Plan != "unchanged" || !reply.Rewritten {
				t.Fatalf("invalid reply partially changed the result: %+v", reply)
			}
		})
	}
}
