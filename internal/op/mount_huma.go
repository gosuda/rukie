package op

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
)

var ErrUnauthenticated = errors.New("unauthenticated")

type Authenticator interface {
	Bearer(ctx context.Context, secret string) (Principal, error)
	Session(ctx context.Context, id string) (Principal, error)
}

type Outcome uint8

const (
	OutcomeNew Outcome = iota + 1
	OutcomeReplay
	OutcomeInFlight
	OutcomeReused
)

type Recorded struct {
	Location string
	ETag     string
	Link     string
	Body     []byte
	Status   int
}

type Claim struct {
	ID    string
	Key   string
	Owner int64
}

type IdempotencyStore interface {
	Lookup(ctx context.Context, owner int64, key string, digest [32]byte, now time.Time) (Outcome, *Recorded, error)
	Begin(ctx context.Context, owner int64, key string, digest [32]byte, now time.Time) (Claim, Outcome, error)
	Complete(ctx context.Context, c Claim, rec Recorded, a AuditRow) error
	Abort(ctx context.Context, c Claim) error
}

const IdempotencyTTL = 24 * time.Hour

func Digest(method, requestURI string, body []byte) [32]byte {
	compact := body
	v := jsontext.Value(bytes.Clone(body))
	if err := v.Compact(); err == nil {
		compact = []byte(v)
	}
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte("\n"))
	h.Write([]byte(requestURI))
	h.Write([]byte("\n"))
	h.Write(compact)
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

type idemEntry struct {
	expires  time.Time
	recorded *Recorded
	digest   [32]byte
	inflight bool
}

type MemoryIdempotency struct {
	entries map[string]*idemEntry
	now     func() time.Time
	mu      sync.Mutex
}

func NewMemoryIdempotency() *MemoryIdempotency {
	return &MemoryIdempotency{entries: map[string]*idemEntry{}, now: time.Now}
}

func idemMapKey(owner int64, key string) string {
	return strconv.FormatInt(owner, 10) + "\x00" + key
}

func (m *MemoryIdempotency) Lookup(ctx context.Context, owner int64, key string, digest [32]byte, now time.Time) (Outcome, *Recorded, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[idemMapKey(owner, key)]
	if !ok || !now.Before(e.expires) {
		return OutcomeNew, nil, nil
	}
	if e.inflight {
		if e.digest == digest {
			return OutcomeInFlight, nil, nil
		}
		return OutcomeReused, nil, nil
	}
	if e.digest == digest {
		return OutcomeReplay, e.recorded, nil
	}
	return OutcomeReused, nil, nil
}

func (m *MemoryIdempotency) Begin(ctx context.Context, owner int64, key string, digest [32]byte, now time.Time) (Claim, Outcome, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	mk := idemMapKey(owner, key)
	if e, ok := m.entries[mk]; ok && now.Before(e.expires) {
		if e.inflight && e.digest == digest {
			return Claim{}, OutcomeInFlight, nil
		}
		return Claim{}, OutcomeReused, nil
	}
	c := Claim{ID: newClaimID(), Owner: owner, Key: key}
	m.entries[mk] = &idemEntry{digest: digest, expires: now.Add(IdempotencyTTL), inflight: true}
	return c, OutcomeNew, nil
}

func (m *MemoryIdempotency) Complete(ctx context.Context, c Claim, rec Recorded, a AuditRow) error {
	_ = ctx
	_ = a
	m.mu.Lock()
	defer m.mu.Unlock()
	mk := idemMapKey(c.Owner, c.Key)
	digest := sha256.Sum256(rec.Body)
	if e, ok := m.entries[mk]; ok && e.inflight {
		digest = e.digest
	}
	m.entries[mk] = &idemEntry{recorded: &rec, digest: digest, expires: m.now().Add(IdempotencyTTL)}
	return nil
}

func (m *MemoryIdempotency) Abort(ctx context.Context, c Claim) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	mk := idemMapKey(c.Owner, c.Key)
	if e, ok := m.entries[mk]; ok && e.inflight {
		delete(m.entries, mk)
	}
	return nil
}

func (m *MemoryIdempotency) StartSweeper(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				m.mu.Lock()
				for k, e := range m.entries {
					if !now.Before(e.expires) {
						delete(m.entries, k)
					}
				}
				m.mu.Unlock()
			}
		}
	}()
}

func newClaimID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

type AuditRow struct {
	At             time.Time
	IfMatch        string
	Decision       string
	RukRequestID   string
	Problem        string
	EventID        string
	Op             string
	Method         string
	Path           string
	RequestID      string
	IdempotencyKey string
	Scope          string
	TokenID        int64
	Status         int
	PrincipalID    int64
	LatencyMicros  int64
	PrincipalKind  PrincipalKind
	Surface        Surface
}

type Auditor interface {
	Append(ctx context.Context, a AuditRow) error
}

type FuncAuditor func(ctx context.Context, a AuditRow) error

func (f FuncAuditor) Append(ctx context.Context, a AuditRow) error { return f(ctx, a) }

type RateKind uint8

const (
	RateToken RateKind = iota + 1
	RateIPAnon
	RateIPLogin
	RateIPGit
)

type RateKey struct {
	ID   string
	Kind RateKind
}

type RateDecision struct {
	RetryAfter time.Duration
	Allow      bool
}

type Limiter interface {
	Admit(ctx context.Context, key RateKey) RateDecision
}

type Surface uint8

const (
	SurfaceHTTP Surface = iota + 1
	SurfaceMCP
)

type Config struct {
	Git           http.Handler
	Authenticator Authenticator
	Idempotency   IdempotencyStore
	Audit         Auditor
	Limiter       Limiter
	Clock         func() time.Time
	Views         *Views
	PublicOrigin  string
	Title         string
	Version       string
	CSRFKey       []byte
	MaxBody       int64
	MaxRecorded   int
}

type Server struct {
	api     huma.API
	mux     *http.ServeMux
	byRoute map[string]int
	allow   map[string][]string
	doc     *Document
	views   *Views
	csrf    *http.CrossOriginProtection
	nonOp   map[string]http.Handler
	now     func() time.Time
	specs   []Spec
	cfg     Config
}

type mount struct {
	server *Server
}

type Response struct {
	Header http.Header
	Body   []byte
	Status int
}

type reqState struct {
	out            any
	srv            *Server
	dispatchHeader http.Header
	query          url.Values
	raw            url.Values
	spec           *Spec
	header         http.Header
	failure        *Error
	claim          *Claim
	replay         *Recorded
	requestID      string
	link           string
	key            string
	path           string
	csrf           string
	location       string
	etag           string
	cookies        []*http.Cookie
	body           []byte
	dispatchBody   []byte
	principal      Principal
	retryAfter     time.Duration
	dispatchStatus int
	digest         [32]byte
	media          Media
	surface        Surface
}

type stateKey struct{}

func stateFrom(ctx context.Context) *reqState {
	st, _ := ctx.Value(stateKey{}).(*reqState)
	return st
}

type trustedKey struct{}

type trustedPrincipal struct {
	principal Principal
	surface   Surface
}

func WithTrusted(ctx context.Context, p Principal, surf Surface) context.Context {
	return context.WithValue(ctx, trustedKey{}, trustedPrincipal{principal: p, surface: surf})
}

func trustedFrom(ctx context.Context) (trustedPrincipal, bool) {
	tp, ok := ctx.Value(trustedKey{}).(trustedPrincipal)
	return tp, ok
}

func SetCookie(ctx context.Context, c *http.Cookie) {
	if st := stateFrom(ctx); st != nil {
		st.cookies = append(st.cookies, c)
	}
}

func SetHeader(ctx context.Context, key, value string) {
	if st := stateFrom(ctx); st != nil {
		st.header.Set(key, value)
	}
}

type clientIPKey struct{}

// ClientIP reports the request's remote address for abuse accounting.
// Handlers that rate-limit or block by IP read it here; empty when unset.
func ClientIP(ctx context.Context) string {
	if ip, ok := ctx.Value(clientIPKey{}).(string); ok {
		return ip
	}
	return ""
}

func CursorKey(ctx context.Context) []byte {
	if st := stateFrom(ctx); st != nil && st.srv != nil {
		return cursorSubkey(st.srv.cfg.CSRFKey)
	}
	return nil
}

type problemBody struct {
	Recovery  *Recovery    `json:"recovery,omitempty"`
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Detail    string       `json:"detail,omitempty"`
	Instance  string       `json:"instance,omitempty"`
	Code      string       `json:"code"`
	RequestID string       `json:"request_id,omitempty"`
	TraceID   string       `json:"trace_id,omitempty"`
	RukCode   string       `json:"ruk_code,omitempty"`
	Errors    []FieldError `json:"errors,omitempty"`
	Status    int          `json:"status"`
	Retryable bool         `json:"retryable"`
}

func (p problemBody) GetStatus() int { return p.Status }

func (p problemBody) Error() string { return p.Code + ": " + p.Detail }

var _ huma.StatusError = problemBody{}

var humaErrorOnce sync.Once

func installHumaErrors() {
	humaErrorOnce.Do(func() {
		huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
			return problemFromHuma(status, msg, errs)
		}
	})
}

func problemFromHuma(status int, msg string, errs []error) problemBody {
	code := CodeInternal
	switch status {
	case http.StatusBadRequest:
		code = CodeInvalid
	case http.StatusUnprocessableEntity:
		code = CodeValidation
	}
	p, ok := LookupProblem(code)
	if !ok {
		p, _ = LookupProblem(CodeInternal)
	}
	var fields []FieldError
	for _, err := range errs {
		if detail, ok := errors.AsType[*huma.ErrorDetail](err); ok {
			fields = append(fields, FieldError{Location: detail.Location, Message: detail.Message, Value: detail.Value})
		} else if err != nil {
			fields = append(fields, FieldError{Location: "", Message: err.Error()})
		}
	}
	return problemBody{
		Type:      "/-/problems/" + p.Code.Slug(),
		Title:     p.Title,
		Status:    status,
		Detail:    msg,
		Code:      p.Code.Slug(),
		Retryable: p.Retryable,
		Errors:    fields,
	}
}

func defaultRecovery(code Code) *Recovery {
	r := &Recovery{Docs: "/-/problems/" + code.Slug()}
	switch code {
	case CodeUnauthorized:
		r.Next = "rukie auth login"
	case CodeStale:
		r.Next = "refetch the representation, then retry with a fresh If-Match"
	case CodePreconditionReq:
		r.Next = "retry with If-Match set to the current ETag"
	case CodeLeaseConflict:
		r.Next = "inspect the holder and expiry, then retry after release"
	case CodeRateLimited, CodeInFlight:
		r.BackoffMs = 1000
		r.Next = "retry after Retry-After seconds"
	case CodeUnavailable:
		r.Next = "retry with backoff"
	case CodeKeyReused:
		r.Next = "mint a fresh idempotency key for the corrected request"
	}
	return r
}

func problemFor(st *reqState, s *Spec, err error) problemBody {
	target, ok := errors.AsType[*Error](err)
	if !ok {
		target = &Error{Code: CodeInternal, Detail: "internal error"}
	}
	declared := s == nil
	if !declared {
		for _, c := range s.Problems {
			if c == target.Code {
				declared = true
				break
			}
		}
	}
	if !declared {
		for _, c := range frameworkProblems(s) {
			if c == target.Code {
				declared = true
				break
			}
		}
	}
	code := target.Code
	if !declared {
		code = CodeInternal
	}
	p, ok := LookupProblem(code)
	if !ok {
		p, _ = LookupProblem(CodeInternal)
	}
	rec := target.Recovery
	if rec == nil {
		rec = defaultRecovery(code)
	}
	if target.RetryAfter > 0 {
		ms := int64(target.RetryAfter / time.Millisecond)
		rec.BackoffMs = ms
	}
	body := problemBody{
		Type:      "/-/problems/" + p.Code.Slug(),
		Title:     p.Title,
		Status:    p.Status,
		Detail:    target.Detail,
		Code:      p.Code.Slug(),
		Retryable: p.Retryable,
		RukCode:   target.RukCode,
		Recovery:  rec,
		Errors:    target.Fields,
	}
	if st != nil {
		body.Instance = st.path
		body.RequestID = st.requestID
		body.TraceID = st.requestID
	}
	return body
}

func writeProblem(ctx huma.Context, p problemBody) {
	raw, err := jsonv2.Marshal(p, jsonv1.DefaultOptionsV1())
	if err != nil {
		ctx.SetStatus(http.StatusInternalServerError)
		return
	}
	ctx.SetHeader("Content-Type", "application/problem+json")
	ctx.SetStatus(p.Status)
	_, _ = ctx.BodyWriter().Write(raw)
}

func authorize(s *Spec, guards []Guard) func(ctx huma.Context, next func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		st := stateFrom(ctx.Context())
		if st == nil {
			next(ctx)
			return
		}
		if !st.principal.Can(s.Scope) {
			writeProblem(ctx, problemFor(st, s, Errorf(CodeForbidden, "missing scope %q", s.Scope.String())))
			return
		}
		for _, g := range guards {
			if err := g(ctx.Context(), st.principal, ctx.Param); err != nil {
				writeProblem(ctx, problemFor(st, s, err))
				return
			}
		}
		if s.Concurrency == ConcurrencyIfMatchRequired && s.Method != http.MethodPost && ctx.Header("If-Match") == "" {
			writeProblem(ctx, problemFor(st, s, Errorf(CodePreconditionReq, "If-Match is required")))
			return
		}
		next(ctx)
	}
}

type wire[Out any] struct {
	Body Out
}

func mountOp[In, Out any](m *mount, o Op[In, Out], s Spec) error {
	huma.Register(m.server.api, huma.Operation{
		OperationID:   s.Name,
		Method:        s.Method,
		Path:          s.Path,
		Summary:       s.Summary,
		Description:   s.Description,
		Errors:        s.statusesOf(),
		DefaultStatus: s.successStatus(),
		MaxBodyBytes:  m.server.cfg.MaxBody,
		Security: []map[string][]string{
			{"bearer": {s.Scope.String()}},
			{"cookie": {}},
		},
		Extensions: map[string]any{
			"x-rukie-scope":    s.Scope.String(),
			"x-rukie-kind":     s.Kind.String(),
			"x-rukie-problems": problemSlugs(s.Problems),
		},
		Middlewares: huma.Middlewares{authorize(&s, o.Guards)},
	}, func(ctx context.Context, in *In) (*wire[Out], error) {
		return invoke(ctx, o, &s, in)
	})
	return nil
}

func problemSlugs(codes []Code) []string {
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		out = append(out, c.Slug())
	}
	return out
}

func invoke[In, Out any](ctx context.Context, o Op[In, Out], s *Spec, in *In) (result *wire[Out], err error) {
	st := stateFrom(ctx)
	if st == nil {
		return nil, problemBody{Type: "/-/problems/internal", Title: "Internal error", Status: 500, Code: "internal"}
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			if st.claim != nil && st.srv != nil {
				_ = st.srv.cfg.Idempotency.Abort(ctx, *st.claim)
				st.claim = nil
			}
			result = nil
			err = problemFor(st, s, Errorf(CodeInternal, "internal error"))
		}
	}()
	if nerr := normalizePage(st, in, s); nerr != nil {
		return nil, problemFor(st, s, nerr)
	}
	if s.Kind == KindMutation {
		if berr := beginIdempotent(ctx, st); berr != nil {
			return nil, problemFor(st, s, berr)
		}
	}
	out, herr := o.Handler(ctx, st.principal, *in)
	if herr != nil {
		return nil, problemFor(st, s, herr)
	}
	st.out = out
	recordHeadersTyped(st, o, out)
	return &wire[Out]{Body: out}, nil
}

func normalizePage(st *reqState, in any, s *Spec) error {
	v := reflect.ValueOf(in)
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if s.fields.cursor != nil && st != nil && st.srv != nil {
		f := v.FieldByIndex(s.fields.cursor)
		if f.CanSet() && f.Kind() == reflect.String && f.String() != "" {
			payload, err := DecodeCursor(cursorSubkey(st.srv.cfg.CSRFKey), f.String())
			if err != nil {
				return &Error{Code: CodeInvalid, Detail: "invalid cursor", Fields: []FieldError{{Location: "query.cursor", Message: "invalid or forged cursor"}}}
			}
			f.SetString(payload.Sort)
		}
	}
	if s.fields.limit != nil {
		f := v.FieldByIndex(s.fields.limit)
		if f.CanSet() && f.Kind() == reflect.Int {
			switch {
			case f.Int() == 0:
				f.SetInt(int64(s.PageDefault))
			case f.Int() > int64(s.PageMax):
				return &Error{Code: CodeInvalid, Detail: "limit exceeds maximum", Fields: []FieldError{{Location: "query.limit", Message: "exceeds maximum"}}}
			case f.Int() < 1:
				return &Error{Code: CodeInvalid, Detail: "limit must be positive", Fields: []FieldError{{Location: "query.limit", Message: "must be positive"}}}
			}
		}
	}
	return nil
}

func recordHeadersTyped[In, Out any](st *reqState, o Op[In, Out], out Out) {
	if o.ETag != nil {
		st.etag = o.ETag(out)
	}
	if o.Location != nil {
		st.location = o.Location(out)
	}
	if o.Pagination != nil && st.srv != nil {
		cursor, more := o.Pagination.Next(out)
		if more {
			encoded, err := EncodeCursor(cursorSubkey(st.srv.cfg.CSRFKey), CursorPayload{Sort: cursor})
			if err == nil {
				q := st.query.Clone()
				q.Set("cursor", encoded)
				st.link = "<" + st.path + "?" + q.Encode() + ">; rel=\"next\""
			}
		}
	}
}

func beginIdempotent(ctx context.Context, st *reqState) error {
	if st.key == "" {
		return nil
	}
	srv := st.srv
	if srv == nil {
		return Errorf(CodeInternal, "internal error")
	}
	claim, outcome, err := srv.cfg.Idempotency.Begin(ctx, st.principal.UserID, st.key, st.digest, srv.now())
	if err != nil {
		return Errorf(CodeInternal, "internal error")
	}
	switch outcome {
	case OutcomeNew:
		st.claim = &claim
		return nil
	case OutcomeInFlight:
		return &Error{Code: CodeInFlight, Detail: "request in flight", RetryAfter: time.Second}
	case OutcomeReplay:
		return Errorf(CodeKeyReused, "concurrent request completed first; retry to replay")
	case OutcomeReused:
		return Errorf(CodeKeyReused, "idempotency key already used for a different request")
	}
	return Errorf(CodeKeyReused, "idempotency key already used for a different request")
}
func Mount(mux *http.ServeMux, t *Table, c Config) (*Server, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	if c.MaxBody <= 0 {
		c.MaxBody = 1 << 20
	}
	if c.MaxRecorded <= 0 {
		c.MaxRecorded = 64 << 10
	}
	if c.Clock == nil {
		c.Clock = time.Now
	}
	if c.Views == nil {
		c.Views = NewViews()
	}
	installHumaErrors()
	humaCfg := huma.DefaultConfig(c.Title, c.Version)
	humaCfg.OpenAPIPath = ""
	humaCfg.DocsPath = ""
	humaCfg.SchemasPath = ""
	humaCfg.DefaultFormat = "application/json"
	api := humago.New(mux, humaCfg)
	s := &Server{
		cfg:     c,
		mux:     mux,
		api:     api,
		specs:   t.Specs(),
		byRoute: map[string]int{},
		allow:   map[string][]string{},
		views:   c.Views,
		csrf:    http.NewCrossOriginProtection(),
		nonOp:   map[string]http.Handler{},
		now:     c.Clock,
	}
	if c.PublicOrigin != "" {
		if err := s.csrf.AddTrustedOrigin(c.PublicOrigin); err != nil {
			return nil, err
		}
	}
	if openAPI := api.OpenAPI(); openAPI != nil {
		if openAPI.Components == nil {
			openAPI.Components = &huma.Components{}
		}
		openAPI.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
			"bearer": {Type: "http", Scheme: "bearer"},
			"cookie": {Type: "apiKey", In: "cookie", Name: "__Host-rukie_session"},
		}
	}
	m := &mount{server: s}
	for i, spec := range s.specs {
		s.byRoute[routeKey(spec.Method, spec.Path)] = i
		s.allow[spec.Path] = append(s.allow[spec.Path], spec.Method)
	}
	for _, bind := range t.binds {
		if err := func() (err error) {
			defer func() {
				if recovered := recover(); recovered != nil {
					err = fmt.Errorf("mount: %v", recovered)
				}
			}()
			return bind(m)
		}(); err != nil {
			return nil, err
		}
	}
	raw, err := s.openapiJSON()
	if err != nil {
		return nil, err
	}
	doc, err := DocumentFromOpenAPI(raw)
	if err != nil {
		return nil, err
	}
	s.doc = doc
	fillBodyKeys(s.specs, doc)
	if err := s.views.check(s.specs); err != nil {
		return nil, err
	}
	if err := checkMCP(s.specs, doc); err != nil {
		return nil, err
	}
	if err := checkCLI(s.specs, doc); err != nil {
		return nil, err
	}
	if err := checkForms(s.specs, doc, s.views); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) openapiJSON() ([]byte, error) {
	return jsonv2.Marshal(s.api.OpenAPI(), jsonv1.DefaultOptionsV1())
}

func (s *Server) OpenAPI() []byte {
	raw, err := s.openapiJSON()
	if err != nil {
		return []byte(`{}`)
	}
	return raw
}

func (s *Server) Specs() []Spec { return slices.Clone(s.specs) }

func (s *Server) Document() *Document { return s.doc }

func (s *Server) Handle(pattern string, h http.Handler) {
	s.nonOp[pattern] = h
	s.mux.Handle(pattern, h)
}

func fillBodyKeys(specs []Spec, doc *Document) {
	for i := range specs {
		if specs[i].Kind != KindMutation {
			continue
		}
		os, ok := doc.Operations[specs[i].Name]
		if !ok || os.Body == nil {
			continue
		}
		body := resolveRef(os.Body, doc.Components)
		props, ok := body["properties"].(map[string]any)
		if !ok {
			continue
		}
		for name := range props {
			specs[i].BodyKeys = append(specs[i].BodyKeys, name)
		}
	}
}

func resolveRef(s Schema, components map[string]Schema) Schema {
	ref, ok := s["$ref"].(string)
	if !ok {
		return s
	}
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(ref, prefix) {
		return s
	}
	if resolved, ok := components[strings.TrimPrefix(ref, prefix)]; ok {
		return resolved
	}
	return s
}

func matchPath(tmpl, path string) bool {
	tsegs := strings.Split(tmpl, "/")
	psegs := strings.Split(path, "/")
	for i, ts := range tsegs {
		if strings.HasSuffix(ts, "...}") {
			return true
		}
		if i >= len(psegs) {
			return false
		}
		if strings.HasPrefix(ts, "{") && strings.HasSuffix(ts, "}") {
			continue
		}
		if ts != psegs[i] {
			return false
		}
	}
	return len(psegs) == len(tsegs)
}

func (s *Server) lookup(path string) (matches []int, methods []string) {
	for i, spec := range s.specs {
		if matchPath(spec.Path, path) {
			matches = append(matches, i)
			methods = append(methods, spec.Method)
		}
	}
	return matches, methods
}

func newRequestID(r *http.Request) string {
	if v := r.Header.Get("X-Request-Id"); v != "" && len(v) <= 128 && isPrintable(v) {
		return v
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b)
}

func isPrintable(s string) bool {
	for i := range len(s) {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h, ok := s.nonOp[r.Method+" "+r.URL.Path]; ok {
		h.ServeHTTP(w, r)
		return
	}
	start := s.now()
	st := &reqState{
		srv:     s,
		header:  http.Header{},
		query:   r.URL.Query(),
		path:    r.URL.Path,
		media:   MediaJSON,
		surface: SurfaceHTTP,
	}
	st.requestID = newRequestID(r)
	ctx := context.WithValue(r.Context(), stateKey{}, st)
	ctx = context.WithValue(ctx, clientIPKey{}, clientIP(r))
	r = r.WithContext(ctx)
	decision := "allow"
	matches, methods := s.lookup(st.path)
	var spec *Spec
	for _, i := range matches {
		if s.specs[i].Method == r.Method {
			spec = &s.specs[i]
			break
		}
	}
	if spec == nil {
		if len(matches) > 0 {
			st.failure = &Error{Code: CodeMethodNotAllowed, Detail: "method not allowed"}
			st.header.Set("Allow", strings.Join(methods, ", "))
		} else {
			st.failure = &Error{Code: CodeNotFound, Detail: "not found"}
		}
		decision = "rejected:route"
		s.finish(w, r, st, start, decision)
		return
	}
	st.spec = spec
	if !s.negotiate(r, st) {
		decision = "rejected:negotiate"
		s.finish(w, r, st, start, decision)
		return
	}
	if tp, ok := trustedFrom(ctx); ok {
		st.principal = tp.principal
		st.surface = tp.surface
	} else {
		principal, failure := s.authenticate(r)
		st.principal = principal
		if failure != nil {
			st.failure = failure
			decision = "unauthenticated"
			s.finish(w, r, st, start, decision)
			return
		}
	}
	if !st.principal.Can(spec.Scope) {
		st.failure = Errorf(CodeForbidden, "missing scope %q", spec.Scope.String())
		decision = "forbid:scope"
		s.finish(w, r, st, start, decision)
		return
	}
	s.prepareRenderCSRF(r, st)
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
		body, ok := s.bufferBody(r, st)
		if !ok {
			decision = "rejected:body"
			s.finish(w, r, st, start, decision)
			return
		}
		st.body = body
		if ferr := s.checkOriginCSRF(r, st); ferr != nil {
			st.failure = ferr
			decision = "rejected:csrf"
			s.finish(w, r, st, start, decision)
			return
		}
	}
	if st.failure == nil {
		key := RateKey{ID: clientIP(r), Kind: RateIPAnon}
		if st.principal.Kind != PrincipalAnonymous {
			key = RateKey{Kind: RateToken, ID: strconv.FormatInt(st.principal.TokenID, 10) + ":" + strconv.FormatInt(st.principal.UserID, 10)}
		}
		if s.cfg.Limiter != nil {
			d := s.cfg.Limiter.Admit(r.Context(), key)
			if !d.Allow {
				st.failure = &Error{Code: CodeRateLimited, Detail: "rate limit exceeded", RetryAfter: d.RetryAfter}
				st.retryAfter = d.RetryAfter
				decision = "rejected:rate"
			}
		}
	}
	if st.failure == nil {
		if aerr := s.adapt(r, st); aerr != nil {
			st.failure = aerr
			decision = "rejected:adapt"
		}
	}
	if st.failure == nil && spec.Kind == KindMutation && st.key != "" {
		outcome, recorded, lerr := s.cfg.Idempotency.Lookup(r.Context(), st.principal.UserID, st.key, st.digest, s.now())
		if lerr != nil {
			st.failure = Errorf(CodeInternal, "internal error")
			decision = "rejected:idempotency"
		} else {
			switch outcome {
			case OutcomeReplay:
				st.replay = recorded
			case OutcomeInFlight:
				st.failure = &Error{Code: CodeInFlight, Detail: "request in flight", RetryAfter: time.Second}
				st.retryAfter = time.Second
			case OutcomeReused:
				st.failure = Errorf(CodeKeyReused, "idempotency key already used for a different request")
			case OutcomeNew:
			}
			if st.failure != nil {
				decision = "rejected:idempotency"
			}
		}
	}
	if st.failure == nil && st.replay == nil {
		s.dispatch(r, st)
	}
	s.finish(w, r, st, start, decision)
}

func (s *Server) authenticate(r *http.Request) (Principal, *Error) {
	if auth := r.Header.Get("Authorization"); auth != "" {
		scheme, secret, _ := strings.Cut(auth, " ")
		if !strings.EqualFold(scheme, "Bearer") || secret == "" {
			return Principal{}, Errorf(CodeUnauthorized, "invalid authorization")
		}
		if s.cfg.Authenticator == nil {
			return Principal{}, Errorf(CodeUnauthorized, "invalid token")
		}
		p, err := s.cfg.Authenticator.Bearer(r.Context(), secret)
		if err != nil {
			return Principal{}, Errorf(CodeUnauthorized, "invalid token")
		}
		return p, nil
	}
	cookie, err := r.Cookie("__Host-rukie_session")
	if err != nil {
		return AnonymousPrincipal(), nil
	}
	if s.cfg.Authenticator == nil {
		return AnonymousPrincipal(), nil
	}
	p, err := s.cfg.Authenticator.Session(r.Context(), cookie.Value)
	if err != nil {
		SetCookie(r.Context(), &http.Cookie{Name: "__Host-rukie_session", Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		return AnonymousPrincipal(), nil
	}
	return p, nil
}

func (s *Server) hasPageView(name string) bool {
	_, ok := s.views.pages[name]
	return ok
}

func (s *Server) hasFormView(name string) bool {
	_, ok := s.views.forms[name]
	return ok
}

func (s *Server) negotiate(r *http.Request, st *reqState) bool {
	spec := st.spec
	offered := Offered(s.hasPageView(spec.Name), s.hasFormView(spec.Name), spec.Kind)
	accept := r.Header.Get("Accept")
	contentType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	contentType = strings.TrimSpace(strings.ToLower(contentType))
	if contentType == "application/x-www-form-urlencoded" && (accept == "" || strings.TrimSpace(accept) == "*/*") {
		if s.hasFormView(spec.Name) {
			st.media = MediaHTML
			return true
		}
		st.failure = Errorf(CodeUnsupportedMedia, "form submissions require a form view")
		return false
	}
	media, ok := Negotiate(accept, offered)
	if !ok {
		st.failure = Errorf(CodeNotAcceptable, "no offered media type matches")
		return false
	}
	st.media = media
	r.Header.Set("Accept", "application/json")
	return true
}

func (s *Server) bufferBody(r *http.Request, st *reqState) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, s.cfg.MaxBody+1))
	if err != nil {
		st.failure = Errorf(CodeInternal, "internal error")
		return nil, false
	}
	if int64(len(body)) > s.cfg.MaxBody {
		st.failure = &Error{Code: CodeTooLarge, Detail: "request body too large"}
		return nil, false
	}
	return body, true
}

func csrfToken(key []byte, cookieValue string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("rukie-csrf-v1\x00"))
	mac.Write([]byte(cookieValue))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) checkOriginCSRF(r *http.Request, st *reqState) *Error {
	if r.Header.Get("Cookie") == "" {
		return nil
	}
	if err := s.csrf.Check(r); err != nil {
		return Errorf(CodeForbidden, "cross-origin request refused")
	}
	contentType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	if strings.TrimSpace(strings.ToLower(contentType)) != "application/x-www-form-urlencoded" {
		return nil
	}
	cookie, err := r.Cookie("__Host-rukie_csrf")
	if err != nil {
		return Errorf(CodeForbidden, "missing CSRF cookie")
	}
	values, err := url.ParseQuery(string(st.body))
	if err != nil {
		return Errorf(CodeValidation, "malformed form body")
	}
	st.raw = values
	if !hmac.Equal([]byte(values.Get("_csrf")), []byte(csrfToken(s.cfg.CSRFKey, cookie.Value))) {
		return Errorf(CodeForbidden, "invalid CSRF token")
	}
	return nil
}

func (s *Server) prepareRenderCSRF(r *http.Request, st *reqState) {
	if r.Method != http.MethodGet || st.media != MediaHTML {
		return
	}
	if cookie, err := r.Cookie("__Host-rukie_csrf"); err == nil {
		st.csrf = csrfToken(s.cfg.CSRFKey, cookie.Value)
		return
	}
	var b [32]byte
	_, _ = rand.Read(b[:])
	value := base64.RawURLEncoding.EncodeToString(b[:])
	st.csrf = csrfToken(s.cfg.CSRFKey, value)
	st.cookies = append(st.cookies, &http.Cookie{Name: "__Host-rukie_csrf", Value: value, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, Path: "/"})
}

func (s *Server) adapt(r *http.Request, st *reqState) *Error {
	spec := st.spec
	contentType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	contentType = strings.TrimSpace(strings.ToLower(contentType))
	if contentType == "application/x-www-form-urlencoded" {
		values, err := url.ParseQuery(string(st.body))
		if err != nil {
			return Errorf(CodeValidation, "malformed form body")
		}
		st.raw = values
		args, reserved, _, derr := DecodeForm(spec, s.doc, st.body)
		if derr != nil {
			return asOpError(derr)
		}
		raw, err := jsonv2.Marshal(args, jsonv1.DefaultOptionsV1())
		if err != nil {
			return Errorf(CodeInternal, "internal error")
		}
		st.body = raw
		if reserved.ETag != "" {
			r.Header.Set("If-Match", reserved.ETag)
		}
		if reserved.IdempotencyKey != "" {
			r.Header.Set("Idempotency-Key", reserved.IdempotencyKey)
		}
		r.Header.Set("Content-Type", "application/json")
	}
	if spec.Kind == KindMutation && (contentType == "application/json" || contentType == "application/x-www-form-urlencoded" || contentType == "") {
		if len(bytes.TrimSpace(st.body)) > 0 {
			if uerr := checkUnknownKeys(st.body, spec.BodyKeys); uerr != nil {
				return uerr
			}
		}
	}
	st.digest = Digest(r.Method, r.URL.RequestURI(), st.body)
	st.key = r.Header.Get("Idempotency-Key")
	if st.key != "" && len(st.key) > 255 {
		return Errorf(CodeValidation, "idempotency key too long")
	}
	return nil
}

func asOpError(err error) *Error {
	if target, ok := errors.AsType[*Error](err); ok {
		return target
	}
	return &Error{Code: CodeInternal, Detail: "internal error"}
}

func checkUnknownKeys(body []byte, keys []string) *Error {
	var decoded map[string]any
	if err := jsonv2.Unmarshal(body, &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		return nil
	}
	known := map[string]bool{}
	for _, k := range keys {
		known[k] = true
	}
	var fields []FieldError
	for k := range decoded {
		if !known[k] {
			fields = append(fields, FieldError{Location: "body." + k, Message: "unknown field"})
		}
	}
	if len(fields) > 0 {
		return &Error{Code: CodeValidation, Detail: "unknown fields in request body", Fields: fields}
	}
	return nil
}

type statusCapture struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (c *statusCapture) Header() http.Header { return c.header }

func (c *statusCapture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(b)
}

func (c *statusCapture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (s *Server) dispatch(r *http.Request, st *reqState) {
	r2 := r.Clone(r.Context())
	if st.body != nil {
		r2.Body = io.NopCloser(bytes.NewReader(st.body))
		r2.ContentLength = int64(len(st.body))
	}
	captured := &statusCapture{header: http.Header{}}
	s.mux.ServeHTTP(captured, r2)
	if captured.status == 0 {
		captured.status = http.StatusOK
	}
	st.dispatchStatus = captured.status
	st.dispatchHeader = captured.header
	st.dispatchBody = captured.body.Bytes()
}

func (s *Server) finish(w http.ResponseWriter, r *http.Request, st *reqState, start time.Time, decision string) {
	var status int
	header := http.Header{}
	var body []byte
	switch {
	case st.failure != nil:
		p := problemFor(st, st.spec, st.failure)
		status = p.Status
		header.Set("Content-Type", "application/problem+json")
		raw, _ := jsonv2.Marshal(p, jsonv1.DefaultOptionsV1())
		body = raw
	case st.replay != nil:
		status, header, body = s.renderReplay(st)
	default:
		status, header, body = st.dispatchStatus, st.dispatchHeader, st.dispatchBody
	}
	if st.failure == nil && status >= 400 {
		p := enrichProblem(st, body)
		status = p.Status
		header = http.Header{}
		header.Set("Content-Type", "application/problem+json")
		raw, _ := jsonv2.Marshal(p, jsonv1.DefaultOptionsV1())
		body = raw
	}
	if st.spec != nil && st.failure == nil && status < 400 {
		if st.spec.Method == http.MethodGet && status == http.StatusOK && st.etag != "" && matchETag(r.Header.Get("If-None-Match"), st.etag) {
			status = http.StatusNotModified
			body = nil
		}
	}
	if st.failure == nil && status < 400 && status != http.StatusNotModified {
		status, header, body = s.renderSuccess(st, status, header, body)
	} else if status != http.StatusNotModified {
		status, header, body = s.renderProblemMedia(st, status, header, body)
	}
	out := header
	switch st.media {
	case MediaHTML:
		out.Set("Content-Type", "text/html; charset=utf-8")
	case MediaMarkdown:
		out.Set("Content-Type", "text/markdown; charset=utf-8")
	case MediaJSON:
	default:
	}
	out.Set("Vary", "Accept, Cookie")
	out.Set("X-Content-Type-Options", "nosniff")
	if st.media == MediaHTML {
		out.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'; script-src 'none'")
		out.Set("Referrer-Policy", "same-origin")
	}
	if st.etag != "" {
		out.Set("ETag", `"`+st.etag+`"`)
	}
	if st.location != "" {
		out.Set("Location", st.location)
	}
	if st.link != "" {
		out.Set("Link", st.link)
	}
	if st.spec != nil && st.spec.Sunset != "" {
		out.Set("Sunset", st.spec.Sunset)
	}
	if st.retryAfter > 0 {
		out.Set("Retry-After", strconv.FormatInt(int64(st.retryAfter/time.Second)+1, 10))
	}
	for k, values := range st.header {
		for _, v := range values {
			out.Add(k, v)
		}
	}
	for _, c := range st.cookies {
		out.Add("Set-Cookie", c.String())
	}
	s.completeIdempotent(r, st, status, body)
	s.audit(r, st, status, decision, time.Since(start))
	for k, values := range out {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func matchETag(header, etag string) bool {
	quoted := `"` + etag + `"`
	for part := range strings.SplitSeq(header, ",") {
		if strings.TrimSpace(part) == quoted || strings.TrimSpace(part) == "*" {
			return true
		}
	}
	return false
}

func (s *Server) renderReplay(st *reqState) (int, http.Header, []byte) {
	rec := st.replay
	st.location = rec.Location
	st.etag = strings.Trim(rec.ETag, `"`)
	st.link = rec.Link
	if st.media == MediaHTML && st.spec != nil && st.spec.Kind == KindMutation {
		return http.StatusSeeOther, http.Header{}, nil
	}
	if st.media == MediaMarkdown {
		var decoded any
		if err := jsonv2.Unmarshal(rec.Body, &decoded, jsonv1.DefaultOptionsV1()); err == nil {
			var buf bytes.Buffer
			c := ViewCtx{Spec: st.spec, Principal: st.principal, RequestID: st.requestID, Path: st.path}
			if fn, ok := s.views.markdown[st.spec.Name]; ok {
				if err := fn(&buf, c, decoded); err == nil {
					return rec.Status, http.Header{}, buf.Bytes()
				}
			}
			buf.Reset()
			if err := RenderMarkdown(&buf, decoded); err == nil {
				return rec.Status, http.Header{}, buf.Bytes()
			}
		}
	}
	return rec.Status, http.Header{}, rec.Body
}

func (s *Server) renderSuccess(st *reqState, status int, header http.Header, body []byte) (int, http.Header, []byte) {
	switch st.media {
	case MediaJSON:
		return status, header, body
	case MediaHTML:
		if st.spec.Kind == KindMutation {
			loc := st.location
			if loc == "" {
				loc = st.path
			}
			header.Set("Location", loc)
			return http.StatusSeeOther, header, nil
		}
		entry, ok := s.views.pages[st.spec.Name]
		if !ok {
			return status, header, body
		}
		out := st.out
		if out == nil && len(body) > 0 {
			var decoded any
			if err := jsonv2.Unmarshal(body, &decoded, jsonv1.DefaultOptionsV1()); err == nil {
				out = decoded
			}
		}
		var buf bytes.Buffer
		c := ViewCtx{Spec: st.spec, Principal: st.principal, RequestID: st.requestID, Path: st.path, CSRF: st.csrf}
		if err := entry.fn(&buf, c, out); err != nil {
			return s.renderProblemMedia(st, status, header, mustProblem(st, err))
		}
		return status, header, buf.Bytes()
	case MediaMarkdown:
		out := st.out
		if out == nil && len(body) > 0 {
			var decoded any
			if err := jsonv2.Unmarshal(body, &decoded, jsonv1.DefaultOptionsV1()); err == nil {
				out = decoded
			}
		}
		var buf bytes.Buffer
		c := ViewCtx{Spec: st.spec, Principal: st.principal, RequestID: st.requestID, Path: st.path}
		if fn, ok := s.views.markdown[st.spec.Name]; ok {
			if err := fn(&buf, c, out); err == nil {
				return status, header, buf.Bytes()
			}
		}
		buf.Reset()
		if err := RenderMarkdown(&buf, out); err != nil {
			return s.renderProblemMedia(st, status, header, mustProblem(st, err))
		}
		return status, header, buf.Bytes()
	default:
		return status, header, body
	}
}

func (s *Server) renderFormRerender(st *reqState, status int, p problemBody) ([]byte, bool) {
	if status != http.StatusUnprocessableEntity || st.spec == nil || st.spec.Kind != KindMutation {
		return nil, false
	}
	fn, ok := s.views.forms[st.spec.Name]
	if !ok || fn == nil {
		return nil, false
	}
	var buf bytes.Buffer
	c := ViewCtx{Spec: st.spec, Principal: st.principal, RequestID: st.requestID, Path: st.path, CSRF: st.csrf, Input: st.raw, Errors: p.Errors}
	if err := fn(&buf, c); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

func mustProblem(st *reqState, err error) []byte {
	p := problemFor(st, st.spec, err)
	raw, _ := jsonv2.Marshal(p, jsonv1.DefaultOptionsV1())
	return raw
}

func (s *Server) renderProblemMedia(st *reqState, status int, header http.Header, body []byte) (int, http.Header, []byte) {
	var p problemBody
	if err := jsonv2.Unmarshal(body, &p, jsonv1.DefaultOptionsV1()); err != nil {
		return status, header, body
	}
	switch st.media {
	case MediaJSON:
		return status, header, body
	case MediaHTML:
		if rendered, ok := s.renderFormRerender(st, status, p); ok {
			return status, header, rendered
		}
		var buf bytes.Buffer
		if err := RenderErrorPage(&buf, ViewCtx{Spec: st.spec, RequestID: st.requestID, Path: st.path}, p); err != nil {
			return status, header, body
		}
		return status, header, buf.Bytes()
	case MediaMarkdown:
		var buf bytes.Buffer
		if err := RenderErrorMarkdown(&buf, ViewCtx{Spec: st.spec, RequestID: st.requestID, Path: st.path}, p); err != nil {
			return status, header, body
		}
		return status, header, buf.Bytes()
	default:
		return status, header, body
	}
}

func enrichProblem(st *reqState, body []byte) problemBody {
	var p problemBody
	if err := jsonv2.Unmarshal(body, &p, jsonv1.DefaultOptionsV1()); err != nil {
		return problemFor(st, st.spec, Errorf(CodeInternal, "internal error"))
	}
	if p.Type == "" {
		p.Type = "/-/problems/" + p.Code
	}
	if p.RequestID == "" {
		p.RequestID = st.requestID
	}
	if p.TraceID == "" {
		p.TraceID = st.requestID
	}
	if p.Instance == "" && st != nil {
		p.Instance = st.path
	}
	if p.Recovery == nil {
		if prob, ok := LookupProblemSlug(p.Code); ok {
			p.Recovery = defaultRecovery(prob.Code)
		}
	}
	if p.Title == "" {
		if prob, ok := LookupProblemSlug(p.Code); ok {
			p.Title = prob.Title
			if p.Status == 0 {
				p.Status = prob.Status
			}
		}
	}
	return p
}

func (s *Server) completeIdempotent(r *http.Request, st *reqState, status int, body []byte) {
	_ = r
	if st.claim == nil || st.srv == nil {
		return
	}
	if len(body) > s.cfg.MaxRecorded || status >= 500 {
		_ = st.srv.cfg.Idempotency.Abort(r.Context(), *st.claim)
		st.claim = nil
		return
	}
	if status >= 400 {
		var p problemBody
		if err := jsonv2.Unmarshal(body, &p, jsonv1.DefaultOptionsV1()); err == nil {
			if prob, ok := LookupProblemSlug(p.Code); ok && prob.Retryable {
				_ = st.srv.cfg.Idempotency.Abort(r.Context(), *st.claim)
				st.claim = nil
				return
			}
		}
	}
	rec := Recorded{Status: status, Location: st.location, Link: st.link, Body: body}
	if st.etag != "" {
		rec.ETag = `"` + st.etag + `"`
	}
	row := AuditRow{RequestID: st.requestID}
	_ = st.srv.cfg.Idempotency.Complete(r.Context(), *st.claim, rec, row)
	st.claim = nil
}

func (s *Server) audit(r *http.Request, st *reqState, status int, decision string, latency time.Duration) {
	_ = r
	if s.cfg.Audit == nil {
		return
	}
	row := AuditRow{
		EventID:        newClaimID(),
		RequestID:      st.requestID,
		At:             s.now(),
		PrincipalID:    st.principal.UserID,
		PrincipalKind:  st.principal.Kind,
		TokenID:        st.principal.TokenID,
		Method:         r.Method,
		Path:           st.path,
		Status:         status,
		Decision:       decision,
		IdempotencyKey: st.key,
		IfMatch:        r.Header.Get("If-Match"),
		LatencyMicros:  latency.Microseconds(),
		Surface:        st.surface,
	}
	if st.spec != nil {
		row.Op = st.spec.Name
		row.Scope = st.spec.Scope.String()
	}
	if st.claim != nil {
		row.RukRequestID = "rukie:" + st.claim.ID
	}
	_ = s.cfg.Audit.Append(r.Context(), row)
}

func (s *Server) Invoke(ctx context.Context, r *http.Request) (*Response, error) {
	rec := &statusCapture{header: http.Header{}}
	s.ServeHTTP(rec, r.WithContext(ctx))
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return &Response{Status: rec.status, Header: rec.header, Body: rec.body.Bytes()}, nil
}
