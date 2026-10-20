package ruk

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

func TestHookDeadlineTable(t *testing.T) {
	ceiling := 10 * time.Second
	cases := []struct {
		name       string
		deadlineMs uint64
		want       time.Duration
	}{
		{"zero takes the ceiling", 0, ceiling},
		{"tiny deadline", 1, time.Millisecond},
		{"below the ceiling", 9999, 9999 * time.Millisecond},
		{"equal keeps the ceiling", 10000, ceiling},
		{"above keeps the ceiling", 10001, ceiling},
		{"overflow keeps the ceiling", ^uint64(0), ceiling},
	}
	for _, tc := range cases {
		if got := hookDeadline(ceiling, tc.deadlineMs); got != tc.want {
			t.Fatalf("%s: hookDeadline = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAnswerHookTimeoutRejectsAll(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	pc := &conn{nc: server, writeEvery: 30 * time.Second}
	release := make(chan struct{})
	var returned atomic.Bool
	handle := func(_ context.Context, _ wire.HookCall) wire.HookReply {
		<-release
		returned.Store(true)
		return wire.HookReply{}
	}
	d := &commandDriver{pc: pc, id: 3, handle: handle, service: "receive-pack", hookBudget: 50 * time.Millisecond}
	call := wire.HookCall{
		Phase:      wire.HookPreReceive,
		DeadlineMs: 50,
		Commands: []wire.HookCommand{
			{Name: "refs/heads/main", Old: strings.Repeat("0", 40), New: strings.Repeat("1", 40), Authorized: true},
		},
	}
	raw, err := jsonv2.Marshal(call)
	if err != nil {
		t.Fatalf("encode call: %v", err)
	}
	type frameOut struct {
		err   error
		frame Frame
	}
	box := make(chan frameOut, 1)
	go func() {
		frame, err := readFrame(client)
		box <- frameOut{frame: frame, err: err}
	}()
	if err := d.answerHook(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("answerHook err = %v, want timeout", err)
	}
	var frame Frame
	select {
	case out := <-box:
		if out.err != nil {
			t.Fatalf("read reply: %v", out.err)
		}
		frame = out.frame
	case <-time.After(5 * time.Second):
		t.Fatal("reply never arrived")
	}
	if frame.Kind != KindHookReply || frame.Stream != 3 {
		t.Fatalf("reply = kind %d stream %d, want hook-reply on 3", uint8(frame.Kind), frame.Stream)
	}
	var reply wire.HookReply
	if err := jsonv2.Unmarshal(frame.Payload, &reply, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if !reply.RejectAll {
		t.Fatalf("reply = %+v, want reject_all", reply)
	}
	if returned.Load() {
		t.Fatal("slow handler answered before its release")
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for !returned.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !returned.Load() {
		t.Fatal("released handler never returned")
	}
	_ = server.Close()
}
