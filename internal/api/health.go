package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/store"
)

func Healthz() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}

type Readiness struct {
	Store  *store.Store
	Daemon func() (bool, string)
	Now    func() time.Time
}

type readinessBody struct {
	Daemon daemonCheck    `json:"daemon"`
	Store  readinessCheck `json:"store"`
	Schema readinessCheck `json:"schema"`
	Ready  bool           `json:"ready"`
}

type readinessCheck struct {
	Ok bool `json:"ok"`
}

type daemonCheck struct {
	Reason string `json:"reason,omitempty"`
	Ready  bool   `json:"ready"`
}

func (rd Readiness) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readinessBody{}
		if rd.Store != nil {
			ctx, cancel := context.WithTimeoutCause(r.Context(), 5*time.Second, fmt.Errorf("readyz store ping timed out"))
			defer cancel()
			if err := rd.Store.Read(ctx, func(_ *store.ReadTx) error { return nil }); err == nil {
				body.Store.Ok = true
				body.Schema.Ok = true
			}
		}
		if rd.Daemon != nil {
			ready, reason := rd.Daemon()
			body.Daemon.Ready = ready
			body.Daemon.Reason = reason
		}
		body.Ready = body.Store.Ok && body.Schema.Ok && body.Daemon.Ready
		w.Header().Set("Content-Type", "application/json")
		if !body.Store.Ok || !body.Schema.Ok {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		raw, _ := jsonv2.Marshal(body, jsonv1.DefaultOptionsV1())
		_, _ = w.Write(raw)
	})
}

func NotReadyDaemon() (bool, string) {
	return false, "internal/ruk is absent; the daemon probe lands with the Ruk client"
}
