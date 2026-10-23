package webhook

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

// MaxAttempts caps automatic delivery attempts before dead-lettering.
const MaxAttempts = 10

const (
	claimBatchSize     = 64
	defaultWorkerCount = 8
	defaultInflightCap = 256
	defaultPoll        = 500 * time.Millisecond
	defaultRetention   = 30 * 24 * time.Hour
	maxErrorLen        = 2048
)

// LeaseSweeper re-queues messages stranded by crashed workers.
type LeaseSweeper interface {
	SweepWebhookLeases(ctx context.Context, nowMillis int64) (int64, error)
}

// MessageClaimer atomically claims due messages with a lease.
// A non-positive perEndpointCap falls back to each endpoint's own
// concurrency.
type MessageClaimer interface {
	ClaimWebhookMessages(ctx context.Context, nowMillis int64, batch int, perEndpointCap int) ([]store.WebhookMessageRow, error)
}

// EndpointLoader loads one endpoint row by id.
type EndpointLoader interface {
	GetWebhookEndpoint(ctx context.Context, id string) (store.WebhookEndpointRow, error)
}

// AttemptRecorder appends one bounded history row per attempt.
type AttemptRecorder interface {
	RecordWebhookAttempt(ctx context.Context, attempt store.WebhookAttemptInsert) error
}

// DeliveryMarker records terminal delivery outcomes.
type DeliveryMarker interface {
	MarkWebhookDelivered(ctx context.Context, id string, nowMillis int64, statusCode int64) error
	MarkWebhookDead(ctx context.Context, id string, errText string, nowMillis int64, statusCode int64) error
}

// MessageRequeuer schedules another attempt or resets after dead-letter.
type MessageRequeuer interface {
	RequeueWebhookMessage(ctx context.Context, id string, errText string, statusCode int64, nextAttemptAt int64, nowMillis int64) error
	RedeliverWebhookMessage(ctx context.Context, id string, nowMillis int64) error
}

// EndpointReporter persists failure counters and suspension.
type EndpointReporter interface {
	NoteEndpointResult(ctx context.Context, endpointID string, consecutiveFailures int64, lastStatus string, disable bool, disableReason string, nowMillis int64) error
}

// HistoryPruner deletes delivered and dead history before a bound.
type HistoryPruner interface {
	PruneWebhookHistory(ctx context.Context, beforeMillis int64) (int64, error)
}

// Store is the persistence boundary the worker drives. Every method maps to
// one store transaction; the wiring slice binds *store.Store to it.
type Store interface {
	LeaseSweeper
	MessageClaimer
	EndpointLoader
	AttemptRecorder
	DeliveryMarker
	MessageRequeuer
	EndpointReporter
	HistoryPruner
}

// DeliverySecrets carries one attempt's endpoint secrets. Previous is set
// only inside the rotation overlap window while it stays unexpired.
type DeliverySecrets struct {
	Current  []byte
	Previous []byte
}

// SendFunc delivers one attempt. It returns the HTTP status with captured
// headers and body, or a transport error string starting with transport:,
// denied by egress policy:, or invalid webhook url:.
type SendFunc func(ctx context.Context, msg store.WebhookMessageRow, endpoint store.WebhookEndpointRow, secrets DeliverySecrets, tsUnix int64, body []byte) (statusCode int64, respHeaders []byte, respBody []byte, transportErr string)

// WorkerConfig carries everything a Worker needs. Store is required and is
// bound through StoreTx; Send defaults to HTTPS delivery. LeaseDuration
// documents the 5 minute lease the store claim owns; DriveOnce passes
// only now millis. Tests in this package may set backend directly.
type WorkerConfig struct {
	backend           Store
	Store             *store.Store
	Now               func() time.Time
	Send              SendFunc
	SealKey           []byte
	Workers           int
	GlobalMaxInflight int
	PollInterval      time.Duration
	LeaseDuration     time.Duration
}

// Worker delivers webhook outbox messages with leases and backoff.
type Worker struct {
	policy *Policy
	sem    chan struct{}
	cfg    WorkerConfig
}

// NewWorker builds a Worker over cfg. A nil policy means default deny.
func NewWorker(cfg WorkerConfig, policy *Policy) *Worker {
	if policy == nil {
		policy = &Policy{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.backend == nil && cfg.Store != nil {
		cfg.backend = &StoreTx{DB: cfg.Store}
	}
	inflight := cfg.GlobalMaxInflight
	if inflight <= 0 {
		inflight = defaultInflightCap
	}
	w := &Worker{cfg: cfg, policy: policy}
	w.sem = make(chan struct{}, inflight)
	if w.cfg.Send == nil {
		w.cfg.Send = w.sendHTTP
	}
	return w
}

// BackoffForAttempt returns the table delay after failed attempt n (1..9).
func BackoffForAttempt(n int64) time.Duration {
	schedule := backoffSchedule()
	switch {
	case n < 1:
		return schedule[0]
	case n > int64(len(schedule)):
		return schedule[len(schedule)-1]
	default:
		return schedule[n-1]
	}
}

// ApplyJitter spreads d uniformly by +-20 percent.
func ApplyJitter(d time.Duration) time.Duration {
	return applyJitterPct(d, 0.20)
}

// ParseRetryAfter parses delta-seconds or an HTTP date, capping at 24h.
func ParseRetryAfter(v string, now time.Time) (time.Time, bool) {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return time.Time{}, false
	}
	if secs, err := strconv.Atoi(trimmed); err == nil {
		if secs < 0 {
			return time.Time{}, false
		}
		if secs > int(maxRetryAfterDelay/time.Second) {
			secs = int(maxRetryAfterDelay / time.Second)
		}
		return now.Add(time.Duration(secs) * time.Second), true
	}
	at, err := http.ParseTime(trimmed)
	if err != nil {
		return time.Time{}, false
	}
	delay := at.Sub(now)
	if delay < 0 {
		delay = 0
	}
	if delay > maxRetryAfterDelay {
		delay = maxRetryAfterDelay
	}
	return now.Add(delay), true
}

// DriveOnce sweeps expired leases, claims due messages, and delivers each
// concurrently bounded by the global in-flight cap.
func (w *Worker) DriveOnce(ctx context.Context) error {
	if w.cfg.backend == nil {
		return fmt.Errorf("DriveOnce: no store")
	}
	nowMillis := w.cfg.Now().UnixMilli()
	if _, err := w.cfg.backend.SweepWebhookLeases(ctx, nowMillis); err != nil {
		return fmt.Errorf("DriveOnce: %w", err)
	}
	msgs, err := w.cfg.backend.ClaimWebhookMessages(ctx, nowMillis, claimBatchSize, 0)
	if err != nil {
		return fmt.Errorf("DriveOnce: %w", err)
	}
	var wg sync.WaitGroup
	errCh := make(chan error, len(msgs))
	for _, msg := range msgs {
		wg.Go(func() {
			select {
			case w.sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-w.sem }()
			if err := w.deliverOne(ctx, msg); err != nil {
				errCh <- err
			}
		})
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		return fmt.Errorf("DriveOnce: %w", err)
	}
	return nil
}

// SweepOnce re-queues messages stranded by crashed workers.
func (w *Worker) SweepOnce(ctx context.Context) (int64, error) {
	if w.cfg.backend == nil {
		return 0, fmt.Errorf("SweepOnce: no store")
	}
	n, err := w.cfg.backend.SweepWebhookLeases(ctx, w.cfg.Now().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("SweepOnce: %w", err)
	}
	return n, nil
}

// JanitorOnce deletes delivered and dead history older than retention,
// defaulting to 30 days when retention is not positive.
func (w *Worker) JanitorOnce(ctx context.Context, retention time.Duration) (int64, error) {
	if w.cfg.backend == nil {
		return 0, fmt.Errorf("JanitorOnce: no store")
	}
	if retention <= 0 {
		retention = defaultRetention
	}
	n, err := w.cfg.backend.PruneWebhookHistory(ctx, w.cfg.Now().Add(-retention).UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("JanitorOnce: %w", err)
	}
	return n, nil
}

// Run polls until ctx cancels, then returns nil after workers stop.
func (w *Worker) Run(ctx context.Context) error {
	count := w.cfg.Workers
	if count <= 0 {
		count = defaultWorkerCount
	}
	interval := w.cfg.PollInterval
	if interval <= 0 {
		interval = defaultPoll
	}
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			if ctx.Err() != nil {
				return
			}
			_ = w.DriveOnce(ctx)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					_ = w.DriveOnce(ctx)
				}
			}
		})
	}
	wg.Wait()
	return nil
}

func (w *Worker) deliverOne(ctx context.Context, msg store.WebhookMessageRow) error {
	now := w.cfg.Now()
	endpoint, err := w.cfg.backend.GetWebhookEndpoint(ctx, msg.EndpointID)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		if rerr := w.recordUnsentAttempt(ctx, msg, msg.AttemptCount+1, "undeliverable-endpoint-removed", now); rerr != nil {
			return rerr
		}
		if derr := w.cfg.backend.MarkWebhookDead(ctx, msg.ID, "undeliverable-endpoint-removed", now.UnixMilli(), 0); derr != nil {
			return fmt.Errorf("deliverOne: %w", derr)
		}
		return nil
	default:
		return fmt.Errorf("deliverOne: %w", err)
	}
	attemptNo := msg.AttemptCount + 1
	if !endpoint.Active {
		if rerr := w.recordUnsentAttempt(ctx, msg, attemptNo, "endpoint inactive", now); rerr != nil {
			return rerr
		}
		return w.failAttempt(ctx, msg, endpoint, attemptFailure{attemptNo: attemptNo, errText: "endpoint inactive", delay: tableDelay(attemptNo)}, now)
	}
	secret, err := OpenSecret(string(w.cfg.SealKey), base64.StdEncoding.EncodeToString(endpoint.SecretEnc), secretAAD(endpoint.ID))
	if err != nil {
		if rerr := w.recordUnsentAttempt(ctx, msg, attemptNo, "webhook secret unavailable", now); rerr != nil {
			return rerr
		}
		return w.failAttempt(ctx, msg, endpoint, attemptFailure{attemptNo: attemptNo, errText: "webhook secret unavailable", delay: tableDelay(attemptNo)}, now)
	}
	secrets := DeliverySecrets{Current: secret}
	if len(endpoint.PrevSecretEnc) > 0 && endpoint.PrevSecretExpiresAt > now.UnixMilli() {
		if prev, err := OpenSecret(string(w.cfg.SealKey), base64.StdEncoding.EncodeToString(endpoint.PrevSecretEnc), secretAAD(endpoint.ID)); err == nil {
			secrets.Previous = prev
		}
	}
	tsUnix := now.Unix()
	started := w.cfg.Now()
	status, respHeaders, respBody, transportErr := w.cfg.Send(ctx, msg, endpoint, secrets, tsUnix, msg.Payload)
	finished := w.cfg.Now()
	sig := "v1,<omitted>"
	if len(secrets.Previous) > 0 {
		sig += " v1,<omitted>"
	}
	attempt := store.WebhookAttemptInsert{
		MessageID:        msg.ID,
		AttemptNo:        attemptNo,
		StartedAt:        started.UnixMilli(),
		FinishedAt:       finished.UnixMilli(),
		AttemptTimestamp: tsUnix,
		HTTPStatus:       status,
		Error:            attemptError(status, transportErr),
		ReqHeaders:       signedHeaders(msg, tsUnix, sig),
		RespHeaders:      respHeaders,
		RespBody:         respBody,
		DurationMs:       finished.Sub(started).Milliseconds(),
	}
	if rerr := w.cfg.backend.RecordWebhookAttempt(ctx, attempt); rerr != nil {
		return fmt.Errorf("deliverOne: %w", rerr)
	}
	switch {
	case transportErr != "":
		if strings.HasPrefix(transportErr, "invalid webhook url") {
			if derr := w.cfg.backend.MarkWebhookDead(ctx, msg.ID, truncate2K(transportErr), now.UnixMilli(), 0); derr != nil {
				return fmt.Errorf("deliverOne: %w", derr)
			}
			return nil
		}
		return w.failAttempt(ctx, msg, endpoint, attemptFailure{attemptNo: attemptNo, errText: transportErr, delay: tableDelay(attemptNo)}, now)
	case status >= 200 && status <= 299:
		if derr := w.cfg.backend.MarkWebhookDelivered(ctx, msg.ID, now.UnixMilli(), status); derr != nil {
			return fmt.Errorf("deliverOne: %w", derr)
		}
		if nerr := w.cfg.backend.NoteEndpointResult(ctx, endpoint.ID, 0, "ok", false, "", now.UnixMilli()); nerr != nil {
			return fmt.Errorf("deliverOne: %w", nerr)
		}
		return nil
	case status == http.StatusGone:
		if derr := w.cfg.backend.MarkWebhookDead(ctx, msg.ID, "endpoint gone (410)", now.UnixMilli(), status); derr != nil {
			return fmt.Errorf("deliverOne: %w", derr)
		}
		if nerr := w.cfg.backend.NoteEndpointResult(ctx, endpoint.ID, endpoint.ConsecutiveFailures+1, "disabled", true, "410-gone", now.UnixMilli()); nerr != nil {
			return fmt.Errorf("deliverOne: %w", nerr)
		}
		return nil
	default:
		delay := tableDelay(attemptNo)
		if retry, ok := retryAfterDelay(respHeaders, status, now); ok {
			delay = applyJitterPct(retry, 0.10)
		}
		cause := attemptFailure{attemptNo: attemptNo, errText: "unexpected status " + strconv.FormatInt(status, 10), status: status, delay: delay}
		return w.failAttempt(ctx, msg, endpoint, cause, now)
	}
}

// attemptFailure groups one failed attempt with its retry decision.
type attemptFailure struct {
	errText   string
	attemptNo int64
	status    int64
	delay     time.Duration
}

func signedHeaders(msg store.WebhookMessageRow, tsUnix int64, sig string) []byte {
	return headerJSON(map[string]string{
		"content-type":      webhookContentType,
		"user-agent":        webhookUserAgent,
		"webhook-id":        msg.ID,
		"webhook-signature": sig,
		"webhook-timestamp": strconv.FormatInt(tsUnix, 10),
	})
}

func unsignedHeaderRecord(msgID string) []byte {
	return headerJSON(map[string]string{"webhook-id": msgID})
}

func headerJSON(record map[string]string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(record); err != nil {
		return []byte("{}")
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func (w *Worker) recordUnsentAttempt(ctx context.Context, msg store.WebhookMessageRow, attemptNo int64, errText string, now time.Time) error {
	unsent := store.WebhookAttemptInsert{
		MessageID:        msg.ID,
		AttemptNo:        attemptNo,
		StartedAt:        now.UnixMilli(),
		FinishedAt:       now.UnixMilli(),
		AttemptTimestamp: now.Unix(),
		Error:            truncate2K(errText),
		ReqHeaders:       unsignedHeaderRecord(msg.ID),
	}
	if rerr := w.cfg.backend.RecordWebhookAttempt(ctx, unsent); rerr != nil {
		return fmt.Errorf("deliverOne: %w", rerr)
	}
	return nil
}

func (w *Worker) failAttempt(ctx context.Context, msg store.WebhookMessageRow, endpoint store.WebhookEndpointRow, cause attemptFailure, now time.Time) error {
	failures := endpoint.ConsecutiveFailures + 1
	lastStatus, disable := applyFailure(failures)
	if lastStatus == "" {
		lastStatus = endpoint.LastStatus
	}
	reason := ""
	if disable {
		reason = "undeliverable"
	}
	nowMillis := now.UnixMilli()
	if cause.attemptNo >= MaxAttempts {
		if derr := w.cfg.backend.MarkWebhookDead(ctx, msg.ID, truncate2K(cause.errText), nowMillis, cause.status); derr != nil {
			return fmt.Errorf("failAttempt: %w", derr)
		}
		if nerr := w.cfg.backend.NoteEndpointResult(ctx, endpoint.ID, failures, lastStatus, disable, reason, nowMillis); nerr != nil {
			return fmt.Errorf("failAttempt: %w", nerr)
		}
		return nil
	}
	if nerr := w.cfg.backend.NoteEndpointResult(ctx, endpoint.ID, failures, lastStatus, disable, reason, nowMillis); nerr != nil {
		return fmt.Errorf("failAttempt: %w", nerr)
	}
	if rerr := w.cfg.backend.RequeueWebhookMessage(ctx, msg.ID, truncate2K(cause.errText), cause.status, now.Add(cause.delay).UnixMilli(), nowMillis); rerr != nil {
		return fmt.Errorf("failAttempt: %w", rerr)
	}
	return nil
}

func (w *Worker) sendHTTP(ctx context.Context, msg store.WebhookMessageRow, endpoint store.WebhookEndpointRow, secrets DeliverySecrets, tsUnix int64, body []byte) (int64, []byte, []byte, string) {
	if _, err := w.policy.CheckURL(endpoint.URL); err != nil {
		return 0, nil, nil, invalidURLText(err)
	}
	client := w.policy.NewClient(clampTimeout(endpoint.TimeoutSeconds))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, "transport: " + err.Error()
	}
	req.Header.Set("webhook-id", msg.ID)
	req.Header.Set("webhook-timestamp", strconv.FormatInt(tsUnix, 10))
	req.Header.Set("webhook-signature", SignHeader(secrets.Current, secrets.Previous, msg.ID, tsUnix, body))
	req.Header.Set("Content-Type", webhookContentType)
	req.Header.Set("User-Agent", webhookUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		if denied, ok := errors.AsType[*policyError](err); ok {
			return 0, nil, nil, denied.Error()
		}
		return 0, nil, nil, "transport: " + err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, bodyCaptureLimit+1))
	if err != nil {
		return 0, nil, nil, "transport: " + err.Error()
	}
	if len(raw) > bodyCaptureLimit {
		raw = append(raw[:bodyCaptureLimit], truncationMarker...)
	}
	return int64(resp.StatusCode), serializeHeaders(resp.Header), raw, ""
}

func backoffSchedule() []time.Duration {
	return []time.Duration{
		5 * time.Second,
		5 * time.Minute,
		30 * time.Minute,
		2 * time.Hour,
		5 * time.Hour,
		10 * time.Hour,
		14 * time.Hour,
		20 * time.Hour,
		24 * time.Hour,
	}
}

func tableDelay(attemptNo int64) time.Duration {
	return ApplyJitter(BackoffForAttempt(attemptNo))
}

func applyJitterPct(d time.Duration, pct float64) time.Duration {
	if d <= 0 {
		return d
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return d
	}
	unit := float64(binary.BigEndian.Uint64(raw[:])) / (1 << 64)
	return time.Duration(float64(d) * (1 + (unit*2-1)*pct))
}

// applyFailure maps the post-increment failure count to the endpoint
// status and whether the endpoint suspends itself.
func applyFailure(consecutive int64) (string, bool) {
	switch {
	case consecutive >= 200:
		return "disabled", true
	case consecutive >= 50:
		return "failing", false
	default:
		return "", false
	}
}

func attemptError(status int64, transportErr string) string {
	switch {
	case transportErr != "":
		return truncate2K(transportErr)
	case status < 200 || status > 299:
		return truncate2K("unexpected status " + strconv.FormatInt(status, 10))
	default:
		return ""
	}
}

func retryAfterDelay(respHeaders []byte, status int64, now time.Time) (time.Duration, bool) {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
	default:
		return 0, false
	}
	var headers map[string]string
	if err := json.Unmarshal(respHeaders, &headers); err != nil {
		return 0, false
	}
	raw, ok := headers["retry-after"]
	if !ok {
		for k, v := range headers {
			if strings.EqualFold(k, "retry-after") {
				raw, ok = v, true
				break
			}
		}
	}
	if !ok {
		return 0, false
	}
	at, ok := ParseRetryAfter(raw, now)
	if !ok {
		return 0, false
	}
	return max(0, at.Sub(now)), true
}

func serializeHeaders(header http.Header) []byte {
	flat := make(map[string]string, len(header))
	for name, values := range header {
		flat[strings.ToLower(name)] = strings.Join(values, ",")
	}
	raw, err := json.Marshal(flat)
	if err != nil {
		return []byte("{}")
	}
	if len(raw) > headerCaptureLimit {
		raw = raw[:headerCaptureLimit]
	}
	return raw
}

func clampTimeout(seconds int64) time.Duration {
	if seconds <= 0 {
		seconds = 20
	}
	return time.Duration(min(max(seconds, 5), 60)) * time.Second
}

func secretAAD(endpointID string) string {
	return "webhook-endpoint-secret:" + endpointID
}

func invalidURLText(err error) string {
	const marker = "invalid webhook url: "
	s := err.Error()
	i := strings.Index(s, marker)
	if i < 0 {
		return marker + s
	}
	return s[i:]
}

func truncate2K(s string) string {
	if len(s) > maxErrorLen {
		return s[:maxErrorLen]
	}
	return s
}

// StoreTx binds *store.Store to the Store interface. Each method runs in
// exactly one store transaction: reads are snapshot-isolated and writes
// are linearizable through the single writer.
type StoreTx struct {
	DB *store.Store
}

var _ Store = (*StoreTx)(nil)

// SweepWebhookLeases re-queues messages stranded by crashed workers.
func (s *StoreTx) SweepWebhookLeases(ctx context.Context, nowMillis int64) (int64, error) {
	var n int64
	err := s.DB.Write(ctx, func(tx *store.WriteTx) error {
		var err error
		n, err = tx.SweepWebhookLeases(nowMillis)
		return err
	})
	return n, err
}

// ClaimWebhookMessages atomically claims due messages with a lease.
func (s *StoreTx) ClaimWebhookMessages(ctx context.Context, nowMillis int64, batch int, perEndpointCap int) ([]store.WebhookMessageRow, error) {
	var out []store.WebhookMessageRow
	err := s.DB.Write(ctx, func(tx *store.WriteTx) error {
		var err error
		out, err = tx.ClaimWebhookMessages(nowMillis, batch, perEndpointCap)
		return err
	})
	return out, err
}

// GetWebhookEndpoint loads one endpoint row by id.
func (s *StoreTx) GetWebhookEndpoint(ctx context.Context, id string) (store.WebhookEndpointRow, error) {
	var ep store.WebhookEndpointRow
	err := s.DB.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		ep, err = tx.GetWebhookEndpoint(id)
		return err
	})
	return ep, err
}

// RecordWebhookAttempt appends one bounded history row per attempt.
func (s *StoreTx) RecordWebhookAttempt(ctx context.Context, attempt store.WebhookAttemptInsert) error {
	return s.DB.Write(ctx, func(tx *store.WriteTx) error {
		return tx.RecordWebhookAttempt(attempt)
	})
}

// MarkWebhookDelivered records a 2xx outcome.
func (s *StoreTx) MarkWebhookDelivered(ctx context.Context, id string, nowMillis, statusCode int64) error {
	return s.DB.Write(ctx, func(tx *store.WriteTx) error {
		return tx.MarkWebhookDelivered(id, nowMillis, statusCode)
	})
}

// MarkWebhookDead records a terminal failure.
func (s *StoreTx) MarkWebhookDead(ctx context.Context, id, errText string, nowMillis, statusCode int64) error {
	return s.DB.Write(ctx, func(tx *store.WriteTx) error {
		return tx.MarkWebhookDead(id, errText, nowMillis, statusCode)
	})
}

// RequeueWebhookMessage schedules another attempt with backoff.
func (s *StoreTx) RequeueWebhookMessage(ctx context.Context, id, errText string, statusCode, nextAttemptAt, nowMillis int64) error {
	return s.DB.Write(ctx, func(tx *store.WriteTx) error {
		_, err := tx.RequeueWebhookMessage(id, errText, statusCode, nextAttemptAt, nowMillis)
		return err
	})
}

// RedeliverWebhookMessage resets a dead or delivered message to pending.
func (s *StoreTx) RedeliverWebhookMessage(ctx context.Context, id string, nowMillis int64) error {
	return s.DB.Write(ctx, func(tx *store.WriteTx) error {
		_, err := tx.RedeliverWebhookMessage(id, nowMillis)
		return err
	})
}

// NoteEndpointResult persists failure counters and suspension.
func (s *StoreTx) NoteEndpointResult(ctx context.Context, endpointID string, consecutiveFailures int64, lastStatus string, disable bool, disableReason string, nowMillis int64) error {
	return s.DB.Write(ctx, func(tx *store.WriteTx) error {
		up := store.WebhookEndpointUpdate{ConsecutiveFailures: &consecutiveFailures, LastStatus: &lastStatus}
		if disable {
			active := false
			up.Active = &active
			up.DisableReason = &disableReason
			up.DisabledAt = &nowMillis
		}
		_, err := tx.UpdateWebhookEndpoint(endpointID, time.UnixMilli(nowMillis), up)
		return err
	})
}

// PruneWebhookHistory deletes delivered and dead history before a bound.
func (s *StoreTx) PruneWebhookHistory(ctx context.Context, beforeMillis int64) (int64, error) {
	var n int64
	err := s.DB.Write(ctx, func(tx *store.WriteTx) error {
		var err error
		n, err = tx.PruneWebhookHistory(beforeMillis)
		return err
	})
	return n, err
}
