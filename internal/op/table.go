package op

import (
	"context"
	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
)

type Kind uint8

const (
	KindRead Kind = iota + 1
	KindMutation
)

func (k Kind) String() string {
	switch k {
	case KindRead:
		return "read"
	case KindMutation:
		return "mutation"
	default:
		return "unknown"
	}
}

type Concurrency uint8

const (
	ConcurrencyNone Concurrency = iota
	ConcurrencyIfMatchOptional
	ConcurrencyIfMatchRequired
)

type Params func(name string) string

type Guard func(ctx context.Context, p Principal, path Params) error

type Pagination[Out any] struct {
	Next    func(out Out) (cursor string, more bool)
	Default int
	Max     int
}

type CLI struct {
	Path       []string
	Positional []string
}

type NoBody struct{}

type Op[In, Out any] struct {
	ETag        func(out Out) string
	Handler     func(ctx context.Context, p Principal, in In) (Out, error)
	Pagination  *Pagination[Out]
	Location    func(out Out) string
	Summary     string
	Method      string
	Description string
	Sunset      string
	Name        string
	Path        string
	CLI         CLI
	Guards      []Guard
	Problems    []Code
	Status      int
	Scope       Scope
	Kind        Kind
	Concurrency Concurrency
	Destructive bool
	Idempotent  bool
	Hidden      bool
}

type Spec struct {
	Out         reflect.Type
	In          reflect.Type
	Name        string
	Summary     string
	Description string
	Method      string
	Path        string
	Sunset      string
	fields      fieldIndex
	CLI         CLI
	BodyKeys    []string
	Problems    []Code
	Status      int
	PageDefault int
	PageMax     int
	Kind        Kind
	Scope       Scope
	Concurrency Concurrency
	Destructive bool
	Idempotent  bool
	HasETag     bool
	HasLocation bool
	Hidden      bool
}

type fieldIndex struct {
	cursor         []int
	limit          []int
	ifMatch        []int
	idempotencyKey []int
}

type Table struct {
	specs   []Spec
	byName  map[string]int
	byRoute map[string]int
	binds   []func(m *mount) error
}

func NewTable() *Table {
	return &Table{
		byName:  map[string]int{},
		byRoute: map[string]int{},
	}
}

func Register[In, Out any](t *Table, o Op[In, Out]) error {
	spec, err := specOf(o)
	if err != nil {
		return err
	}
	if _, dup := t.byName[spec.Name]; dup {
		return fmt.Errorf("op %q: duplicate operation name", spec.Name)
	}
	key := routeKey(spec.Method, spec.Path)
	if _, dup := t.byRoute[key]; dup {
		return fmt.Errorf("op %q: duplicate route %q", spec.Name, key)
	}
	t.byName[spec.Name] = len(t.specs)
	t.byRoute[key] = len(t.specs)
	t.specs = append(t.specs, spec)
	t.binds = append(t.binds, func(m *mount) error { return mountOp(m, o, spec) })
	return nil
}

func (t *Table) Validate() error {
	seenName := map[string]bool{}
	seenRoute := map[string]bool{}
	for i := range t.specs {
		if err := checkSpec(&t.specs[i]); err != nil {
			return err
		}
		if seenName[t.specs[i].Name] {
			return fmt.Errorf("op %q: duplicate operation name", t.specs[i].Name)
		}
		seenName[t.specs[i].Name] = true
		key := routeKey(t.specs[i].Method, t.specs[i].Path)
		if seenRoute[key] {
			return fmt.Errorf("op %q: duplicate route %q", t.specs[i].Name, key)
		}
		seenRoute[key] = true
	}
	return nil
}

func (t *Table) Specs() []Spec {
	out := slices.Clone(t.specs)
	slices.SortFunc(out, func(a, b Spec) int { return strings.Compare(a.Name, b.Name) })
	return out
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*(\.[a-z][a-z0-9]*(-[a-z0-9]+)*){1,2}$`)

var reservedTopWords = map[string]bool{
	"ops":          true,
	"problems":     true,
	"mcp":          true,
	"openapi.json": true,
	"static":       true,
	"session":      true,
	"account":      true,
	"explore":      true,
	"favicon.ico":  true,
	"robots.txt":   true,
	".well-known":  true,
}

func routeKey(method, path string) string {
	var b strings.Builder
	b.WriteString(strings.ToUpper(method))
	b.WriteByte(' ')
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		if i > 0 {
			b.WriteByte('/')
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			b.WriteString("{}")
		} else {
			b.WriteString(seg)
		}
	}
	return b.String()
}

func pathPlaceholders(path string) []string {
	var out []string
	for _, seg := range strings.Split(path, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			inner := seg[1 : len(seg)-1]
			inner = strings.TrimSuffix(inner, "...")
			out = append(out, inner)
		}
	}
	return out
}

func specOf[In, Out any](o Op[In, Out]) (Spec, error) {
	var s Spec
	s.Name = o.Name
	s.Summary = o.Summary
	s.Description = o.Description
	s.Kind = o.Kind
	s.Method = strings.ToUpper(o.Method)
	s.Path = o.Path
	s.Scope = o.Scope
	s.Problems = append([]Code(nil), o.Problems...)
	s.Destructive = o.Destructive
	s.Idempotent = o.Idempotent
	s.Concurrency = o.Concurrency
	s.HasETag = o.ETag != nil
	s.HasLocation = o.Location != nil
	if o.Pagination != nil {
		s.PageDefault = o.Pagination.Default
		s.PageMax = o.Pagination.Max
	}
	s.Status = o.Status
	s.Sunset = o.Sunset
	s.Hidden = o.Hidden
	s.CLI = o.CLI
	s.In = reflect.TypeFor[In]()
	s.Out = reflect.TypeFor[Out]()
	s.fields = indexFields(s.In)
	if err := checkSpec(&s); err != nil {
		return Spec{}, err
	}
	if o.Handler == nil {
		return Spec{}, fmt.Errorf("op %q: nil handler", o.Name)
	}
	for i, g := range o.Guards {
		if g == nil {
			return Spec{}, fmt.Errorf("op %q: nil guard at index %d", o.Name, i)
		}
	}
	if o.Pagination != nil && o.Pagination.Next == nil {
		return Spec{}, fmt.Errorf("op %q: nil pagination next function", o.Name)
	}
	return s, nil
}

func indexFields(t reflect.Type) fieldIndex {
	var f fieldIndex
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return f
	}
	collectFieldIndex(t, nil, &f)
	return f
}

func collectFieldIndex(rt reflect.Type, prefix []int, f *fieldIndex) {
	for i := range rt.NumField() {
		sf := rt.Field(i)
		if !sf.IsExported() {
			continue
		}
		idx := append(append([]int(nil), prefix...), i)
		switch {
		case sf.Tag.Get("query") == "cursor" && sf.Type.Kind() == reflect.String:
			f.cursor = idx
		case sf.Tag.Get("query") == "limit" && sf.Type.Kind() == reflect.Int:
			f.limit = idx
		case sf.Tag.Get("header") == "If-Match" && sf.Type.Kind() == reflect.String:
			f.ifMatch = idx
		case sf.Tag.Get("header") == "Idempotency-Key" && sf.Type.Kind() == reflect.String:
			f.idempotencyKey = idx
		}
	}
}

func pathTags(t reflect.Type) map[string]bool {
	tags := map[string]bool{}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return tags
	}
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		if name, ok := sf.Tag.Lookup("path"); ok {
			tags[name] = true
		}
	}
	return tags
}

func checkSpec(s *Spec) error {
	if !namePattern.MatchString(s.Name) || len(s.Name) > 128 {
		return fmt.Errorf("op %q: invalid name", s.Name)
	}
	if len(s.Summary) < 1 || len(s.Summary) > 120 {
		return fmt.Errorf("op %q: summary must be 1..120 characters", s.Name)
	}
	switch s.Kind {
	case KindRead:
		if s.Method != http.MethodGet {
			return fmt.Errorf("op %q: read ops must use GET", s.Name)
		}
	case KindMutation:
		switch s.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			return fmt.Errorf("op %q: mutation ops must use POST, PUT, PATCH, or DELETE", s.Name)
		}
	default:
		return fmt.Errorf("op %q: unknown kind", s.Name)
	}
	if s.Destructive && s.Kind != KindMutation {
		return fmt.Errorf("op %q: destructive requires a mutation", s.Name)
	}
	if !s.Scope.Valid() {
		return fmt.Errorf("op %q: invalid scope", s.Name)
	}
	if !strings.HasPrefix(s.Path, "/") {
		return fmt.Errorf("op %q: path must begin with /", s.Name)
	}
	for _, seg := range strings.Split(s.Path, "/") {
		if strings.HasSuffix(seg, ".git") {
			return fmt.Errorf("op %q: no path segment may end in .git", s.Name)
		}
	}
	trimmed := strings.TrimPrefix(s.Path, "/")
	if first, _, _ := strings.Cut(trimmed, "/"); reservedTopWords[first] {
		return fmt.Errorf("op %q: path prefix %q is reserved", s.Name, first)
	}
	if s.In.Kind() != reflect.Struct || s.Out.Kind() != reflect.Struct {
		return fmt.Errorf("op %q: In and Out must be struct types", s.Name)
	}
	placeholders := pathPlaceholders(s.Path)
	tags := pathTags(s.In)
	if len(placeholders) != len(tags) {
		return fmt.Errorf("op %q: path placeholders and path tags differ", s.Name)
	}
	for _, p := range placeholders {
		if !tags[p] {
			return fmt.Errorf("op %q: path placeholder {%s} has no path tag", s.Name, p)
		}
	}
	if s.PageDefault != 0 || s.PageMax != 0 {
		if s.fields.cursor == nil || s.fields.limit == nil {
			return fmt.Errorf("op %q: paginated ops require Cursor and Limit fields", s.Name)
		}
		if s.PageDefault < 1 || s.PageDefault > s.PageMax || s.PageMax > 1000 {
			return fmt.Errorf("op %q: pagination bounds must satisfy 1 <= default <= max <= 1000", s.Name)
		}
	}
	if s.Concurrency != ConcurrencyNone {
		if !s.HasETag {
			return fmt.Errorf("op %q: concurrency requires an ETag function", s.Name)
		}
		if s.fields.ifMatch == nil {
			return fmt.Errorf("op %q: concurrency requires an IfMatch field", s.Name)
		}
	}
	if s.Kind == KindMutation && s.fields.idempotencyKey == nil {
		return fmt.Errorf("op %q: mutations require an IdempotencyKey field", s.Name)
	}
	if s.Status != 0 {
		valid := false
		for _, st := range []int{200, 201, 202, 203, 204, 206} {
			if s.Status == st {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("op %q: invalid success status %d", s.Name, s.Status)
		}
	}
	declared := map[Code]bool{}
	for _, c := range s.Problems {
		if !c.Valid() {
			return fmt.Errorf("op %q: invalid problem code", s.Name)
		}
		declared[c] = true
	}
	for _, c := range frameworkProblems(s) {
		declared[c] = true
	}
	if len(declared) == 0 {
		return fmt.Errorf("op %q: no problems declared", s.Name)
	}
	return nil
}

func (s *Spec) successStatus() int {
	if s.Status != 0 {
		return s.Status
	}
	switch s.Method {
	case http.MethodPost:
		return http.StatusCreated
	case http.MethodDelete:
		return http.StatusNoContent
	default:
		return http.StatusOK
	}
}

func (s *Spec) statusesOf() []int {
	seen := map[int]bool{}
	var out []int
	for _, c := range s.Problems {
		if p, ok := LookupProblem(c); ok && !seen[p.Status] {
			seen[p.Status] = true
			out = append(out, p.Status)
		}
	}
	for _, c := range frameworkProblems(s) {
		if p, ok := LookupProblem(c); ok && !seen[p.Status] {
			seen[p.Status] = true
			out = append(out, p.Status)
		}
	}
	slices.Sort(out)
	return out
}

func frameworkProblems(s *Spec) []Code {
	base := []Code{
		CodeUnauthorized,
		CodeForbidden,
		CodeNotAcceptable,
		CodeUnsupportedMedia,
		CodeRateLimited,
		CodeInternal,
		CodeUnavailable,
		CodeMethodNotAllowed,
	}
	if hasInputs(s.In) || s.PageDefault != 0 || s.PageMax != 0 {
		base = append(base, CodeInvalid, CodeValidation)
	}
	if s.Kind == KindMutation {
		base = append(base, CodeTooLarge, CodeKeyReused, CodeInFlight)
	}
	if len(pathPlaceholders(s.Path)) > 0 {
		base = append(base, CodeNotFound)
	}
	if s.Concurrency != ConcurrencyNone {
		base = append(base, CodePreconditionReq, CodeStale)
	}
	return base
}

func hasInputs(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		if _, ok := sf.Tag.Lookup("path"); ok {
			continue
		}
		if sf.Tag.Get("query") == "cursor" || sf.Tag.Get("query") == "limit" {
			continue
		}
		if sf.Tag.Get("header") == "If-Match" || sf.Tag.Get("header") == "Idempotency-Key" {
			continue
		}
		return true
	}
	return false
}

type Scope struct{ id uint8 }

var (
	ScopePublicRead     = Scope{1}
	ScopeRepoRead       = Scope{2}
	ScopeRepoWrite      = Scope{3}
	ScopeRepoCreate     = Scope{4}
	ScopeIntentsRead    = Scope{5}
	ScopeIntentsWrite   = Scope{6}
	ScopeCIRead         = Scope{7}
	ScopeCITrigger      = Scope{8}
	ScopeWebhooksRead   = Scope{9}
	ScopeWebhooksWrite  = Scope{10}
	ScopeReleasesRead   = Scope{11}
	ScopeReleasesWrite  = Scope{12}
	ScopeMirrorsRead    = Scope{13}
	ScopeMirrorsTrigger = Scope{14}
	ScopeAdminRead      = Scope{15}
	ScopeAdminWrite     = Scope{16}
)

func (s Scope) Valid() bool { return s.id >= 1 && s.id <= 16 }

func (s Scope) TokenMintable() bool { return s.id >= 2 && s.id <= 16 }

func (s Scope) String() string {
	switch s {
	case ScopePublicRead:
		return "public:read"
	case ScopeRepoRead:
		return "repo:read"
	case ScopeRepoWrite:
		return "repo:write"
	case ScopeRepoCreate:
		return "repo:create"
	case ScopeIntentsRead:
		return "intents:read"
	case ScopeIntentsWrite:
		return "intents:write"
	case ScopeCIRead:
		return "ci:read"
	case ScopeCITrigger:
		return "ci:trigger"
	case ScopeWebhooksRead:
		return "webhooks:read"
	case ScopeWebhooksWrite:
		return "webhooks:write"
	case ScopeReleasesRead:
		return "releases:read"
	case ScopeReleasesWrite:
		return "releases:write"
	case ScopeMirrorsRead:
		return "mirrors:read"
	case ScopeMirrorsTrigger:
		return "mirrors:trigger"
	case ScopeAdminRead:
		return "admin:read"
	case ScopeAdminWrite:
		return "admin:write"
	default:
		return "invalid"
	}
}

func ParseScope(v string) (Scope, bool) {
	for _, s := range AllScopes() {
		if s.String() == v {
			return s, true
		}
	}
	return Scope{}, false
}

func AllScopes() []Scope {
	return []Scope{
		ScopePublicRead,
		ScopeRepoRead,
		ScopeRepoWrite,
		ScopeRepoCreate,
		ScopeIntentsRead,
		ScopeIntentsWrite,
		ScopeCIRead,
		ScopeCITrigger,
		ScopeWebhooksRead,
		ScopeWebhooksWrite,
		ScopeReleasesRead,
		ScopeReleasesWrite,
		ScopeMirrorsRead,
		ScopeMirrorsTrigger,
		ScopeAdminRead,
		ScopeAdminWrite,
	}
}

type Scopes uint64

func (s Scopes) Has(x Scope) bool {
	if !x.Valid() {
		return false
	}
	return uint64(s)&(uint64(1)<<x.id) != 0
}

func (s Scopes) Add(x Scope) Scopes {
	if !x.Valid() {
		return s
	}
	return Scopes(uint64(s) | (uint64(1) << x.id))
}

type PrincipalKind uint8

const (
	PrincipalAnonymous PrincipalKind = iota + 1
	PrincipalHuman
	PrincipalService
)

type Principal struct {
	UserID  int64
	Kind    PrincipalKind
	TokenID int64
	Grants  Scopes
}

func (p Principal) Can(s Scope) bool { return p.Grants.Has(s) }

func AnonymousPrincipal() Principal {
	return Principal{Kind: PrincipalAnonymous, Grants: Scopes(0).Add(ScopePublicRead)}
}

type Code struct{ slug string }

var (
	CodeInvalid           = Code{"invalid"}
	CodeValidation        = Code{"validation"}
	CodeUnauthorized      = Code{"unauthorized"}
	CodeForbidden         = Code{"forbidden"}
	CodeNotFound          = Code{"not-found"}
	CodeMethodNotAllowed  = Code{"method-not-allowed"}
	CodeNotAcceptable     = Code{"not-acceptable"}
	CodeUnsupportedMedia  = Code{"unsupported-media"}
	CodeTooLarge          = Code{"too-large"}
	CodeConflict          = Code{"conflict"}
	CodeLeaseConflict     = Code{"lease-conflict"}
	CodeInFlight          = Code{"idempotency-in-flight"}
	CodeStale             = Code{"stale"}
	CodeArchived          = Code{"archived"}
	CodePreconditionReq   = Code{"precondition-required"}
	CodeKeyReused         = Code{"idempotency-key-reused"}
	CodeUnsupported       = Code{"unsupported"}
	CodeLimitExceeded     = Code{"limit-exceeded"}
	CodeShallowHistory    = Code{"shallow-history"}
	CodeRateLimited       = Code{"rate-limited"}
	CodeInternal          = Code{"internal"}
	CodeCorrupt           = Code{"corrupt"}
	CodeUnavailable       = Code{"unavailable"}
	CodeRemoteUnknown     = Code{"remote-unknown"}
	CodeRemoteMoved       = Code{"remote-moved"}
	CodePushRejected      = Code{"push-rejected"}
	CodeConfigUnavailable = Code{"config-unavailable"}
)

func (c Code) Valid() bool {
	_, ok := LookupProblem(c)
	return ok
}

func (c Code) Slug() string { return c.slug }

type Problem struct {
	Code      Code
	Title     string
	RukCodes  []string
	Status    int
	Exit      int
	Retryable bool
}

func Problems() []Problem {
	return []Problem{
		{Code: CodeArchived, Title: "Archived", Status: 423, Exit: 3},
		{Code: CodeConfigUnavailable, Title: "Configuration unavailable", Status: 503, Exit: 78},
		{Code: CodeConflict, Title: "Conflict", RukCodes: []string{"E_CONFLICTED", "E_DIVERGENT", "E_DIRTY", "E_UNDO_CONFLICT", "E_WORKSPACE_APPLYING", "E_WORKSPACE_RECOVERY"}, Status: 409, Exit: 2},
		{Code: CodeCorrupt, Title: "Corrupt", RukCodes: []string{"E_CORRUPT"}, Status: 500, Exit: 5},
		{Code: CodeForbidden, Title: "Forbidden", RukCodes: []string{"E_FORBIDDEN", "E_POLICY"}, Status: 403, Exit: 3},
		{Code: CodeInFlight, Title: "Idempotency in flight", Status: 409, Exit: 3, Retryable: true},
		{Code: CodeInternal, Title: "Internal error", RukCodes: []string{"E_IO", "E_CANCELLED"}, Status: 500, Exit: 1, Retryable: true},
		{Code: CodeInvalid, Title: "Invalid", RukCodes: []string{"E_INVALID"}, Status: 400, Exit: 65},
		{Code: CodeKeyReused, Title: "Idempotency key reused", RukCodes: []string{"E_REQUEST_REUSED"}, Status: 422, Exit: 3},
		{Code: CodeLeaseConflict, Title: "Lease conflict", RukCodes: []string{"E_LEASE_LOST", "E_CLAIMED"}, Status: 409, Exit: 3},
		{Code: CodeLimitExceeded, Title: "Limit exceeded", RukCodes: []string{"E_LIMIT"}, Status: 422, Exit: 1},
		{Code: CodeMethodNotAllowed, Title: "Method not allowed", Status: 405, Exit: 1},
		{Code: CodeNotAcceptable, Title: "Not acceptable", Status: 406, Exit: 1},
		{Code: CodeNotFound, Title: "Not found", RukCodes: []string{"E_NOT_FOUND"}, Status: 404, Exit: 1},
		{Code: CodePreconditionReq, Title: "Precondition required", Status: 428, Exit: 3},
		{Code: CodePushRejected, Title: "Push rejected", RukCodes: []string{"E_PUSH_REJECTED"}, Status: 409, Exit: 4},
		{Code: CodeRateLimited, Title: "Rate limited", Status: 429, Exit: 4, Retryable: true},
		{Code: CodeRemoteMoved, Title: "Remote moved", RukCodes: []string{"E_REMOTE_MOVED"}, Status: 409, Exit: 4},
		{Code: CodeRemoteUnknown, Title: "Remote state unknown", RukCodes: []string{"E_PUSH_UNCERTAIN"}, Status: 502, Exit: 4, Retryable: true},
		{Code: CodeShallowHistory, Title: "Shallow history", RukCodes: []string{"E_SHALLOW"}, Status: 422, Exit: 4},
		{Code: CodeStale, Title: "Precondition failed", RukCodes: []string{"E_STALE"}, Status: 412, Exit: 3},
		{Code: CodeTooLarge, Title: "Too large", Status: 413, Exit: 65},
		{Code: CodeUnauthorized, Title: "Unauthorized", Status: 401, Exit: 3},
		{Code: CodeUnavailable, Title: "Unavailable", RukCodes: []string{"E_OFFLINE", "E_CAPACITY", "E_COORDINATOR_UNRESPONSIVE"}, Status: 503, Exit: 4, Retryable: true},
		{Code: CodeUnsupported, Title: "Unsupported", RukCodes: []string{"E_OBJECT_FORMAT", "E_UNSUPPORTED_LFS", "E_UNSUPPORTED_GIT_COMMAND", "E_VERSION", "E_HOSTED"}, Status: 422, Exit: 1},
		{Code: CodeUnsupportedMedia, Title: "Unsupported media type", Status: 415, Exit: 65},
		{Code: CodeValidation, Title: "Validation failed", Status: 422, Exit: 65},
	}
}

var problemIndex = func() map[string]Problem {
	m := map[string]Problem{}
	for _, p := range Problems() {
		m[p.Code.slug] = p
	}
	return m
}()

func LookupProblem(c Code) (Problem, bool) {
	p, ok := problemIndex[c.slug]
	return p, ok
}

func LookupProblemSlug(slug string) (Problem, bool) {
	p, ok := problemIndex[slug]
	return p, ok
}

var rukToCode = map[string]Code{
	"E_STALE":                    CodeStale,
	"E_LEASE_LOST":               CodeLeaseConflict,
	"E_CLAIMED":                  CodeLeaseConflict,
	"E_REQUEST_REUSED":           CodeKeyReused,
	"E_FORBIDDEN":                CodeForbidden,
	"E_POLICY":                   CodeForbidden,
	"E_CONFLICTED":               CodeConflict,
	"E_DIVERGENT":                CodeConflict,
	"E_DIRTY":                    CodeConflict,
	"E_UNDO_CONFLICT":            CodeConflict,
	"E_WORKSPACE_APPLYING":       CodeConflict,
	"E_WORKSPACE_RECOVERY":       CodeConflict,
	"E_REMOTE_MOVED":             CodeRemoteMoved,
	"E_PUSH_REJECTED":            CodePushRejected,
	"E_PUSH_UNCERTAIN":           CodeRemoteUnknown,
	"E_OFFLINE":                  CodeUnavailable,
	"E_SHALLOW":                  CodeShallowHistory,
	"E_CORRUPT":                  CodeCorrupt,
	"E_OBJECT_FORMAT":            CodeUnsupported,
	"E_UNSUPPORTED_LFS":          CodeUnsupported,
	"E_UNSUPPORTED_GIT_COMMAND":  CodeUnsupported,
	"E_VERSION":                  CodeUnsupported,
	"E_HOSTED":                   CodeUnsupported,
	"E_LIMIT":                    CodeLimitExceeded,
	"E_NOT_FOUND":                CodeNotFound,
	"E_IO":                       CodeInternal,
	"E_CANCELLED":                CodeInternal,
	"E_INVALID":                  CodeInvalid,
	"E_COORDINATOR_UNRESPONSIVE": CodeUnavailable,
	"E_CAPACITY":                 CodeUnavailable,
}

func FromRuk(ruk string) (Code, bool) {
	c, ok := rukToCode[ruk]
	return c, ok
}

func ExitFor(c Code) int {
	if p, ok := LookupProblem(c); ok {
		return p.Exit
	}
	return 1
}

type Recovery struct {
	Next        string `json:"next,omitempty"`
	CurrentETag string `json:"current_etag,omitempty"`
	Holder      string `json:"holder,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	MergeBase   string `json:"merge_base,omitempty"`
	Docs        string `json:"docs,omitempty"`
	BackoffMs   int64  `json:"backoff_ms,omitzero"`
}

type FieldError struct {
	Value    any    `json:"value,omitempty"`
	Location string `json:"location"`
	Message  string `json:"message"`
}

type Error struct {
	cause      error
	Recovery   *Recovery
	Code       Code
	Detail     string
	RukCode    string
	Fields     []FieldError
	RetryAfter time.Duration
}

func Errorf(c Code, format string, args ...any) *Error {
	return &Error{Code: c, Detail: fmt.Sprintf(format, args...)}
}

func Wrap(c Code, cause error, format string, args ...any) *Error {
	return &Error{Code: c, Detail: fmt.Sprintf(format, args...), cause: cause}
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return e.Code.slug + ": " + e.Detail
	}
	return e.Code.slug
}

func (e *Error) Unwrap() error { return e.cause }

var _ error = (*Error)(nil)

type Schema map[string]any

type Param struct {
	Schema   Schema
	In       string
	Name     string
	Required bool
}

type OperationSchema struct {
	Body     Schema
	Response Schema
	Params   []Param
}

type Document struct {
	Operations map[string]OperationSchema
	Components map[string]Schema
}

func DocumentFromOpenAPI(raw []byte) (*Document, error) {
	var doc struct {
		Paths      map[string]map[string]any `json:"paths"`
		Components map[string]any            `json:"components"`
	}
	if err := jsonv2.Unmarshal(raw, &doc, jsonv1.DefaultOptionsV1()); err != nil {
		return nil, err
	}
	d := &Document{
		Operations: map[string]OperationSchema{},
		Components: map[string]Schema{},
	}
	if schemas, ok := doc.Components["schemas"].(map[string]any); ok {
		for name, s := range schemas {
			if m, ok := s.(map[string]any); ok {
				d.Components[name] = m
			}
		}
	}
	for path, item := range doc.Paths {
		for method, rawOp := range item {
			opMap, ok := rawOp.(map[string]any)
			if !ok {
				continue
			}
			id, _ := opMap["operationId"].(string)
			if id == "" {
				continue
			}
			os := OperationSchema{}
			if params, ok := opMap["parameters"].([]any); ok {
				for _, rp := range params {
					pm, ok := rp.(map[string]any)
					if !ok {
						continue
					}
					name, _ := pm["name"].(string)
					in, _ := pm["in"].(string)
					req, _ := pm["required"].(bool)
					var sch Schema
					if sm, ok := pm["schema"].(map[string]any); ok {
						sch = sm
					}
					os.Params = append(os.Params, Param{In: in, Name: name, Required: req, Schema: sch})
				}
			}
			if rb, ok := opMap["requestBody"].(map[string]any); ok {
				if content, ok := rb["content"].(map[string]any); ok {
					if mt, ok := content["application/json"].(map[string]any); ok {
						if sm, ok := mt["schema"].(map[string]any); ok {
							os.Body = sm
						}
					}
				}
			}
			if resps, ok := opMap["responses"].(map[string]any); ok {
				for _, want := range []string{"200", "201", "204"} {
					if r, ok := resps[want].(map[string]any); ok {
						if content, ok := r["content"].(map[string]any); ok {
							if mt, ok := content["application/json"].(map[string]any); ok {
								if sm, ok := mt["schema"].(map[string]any); ok {
									os.Response = sm
								}
							}
						}
						break
					}
				}
			}
			_ = method
			_ = path
			d.Operations[id] = os
		}
	}
	return d, nil
}

func (d *Document) Localize(s Schema) (Schema, error) {
	defs := map[string]any{}
	out := localizeValue(map[string]any(s), defs, d.Components)
	m, ok := out.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema root is not an object")
	}
	if len(defs) > 0 {
		m["$defs"] = defs
	}
	return m, nil
}

func localizeValue(v any, defs map[string]any, components map[string]Schema) any {
	switch t := v.(type) {
	case map[string]any:
		if ref, ok := t["$ref"].(string); ok {
			const prefix = "#/components/schemas/"
			if strings.HasPrefix(ref, prefix) {
				name := strings.TrimPrefix(ref, prefix)
				if _, done := defs[name]; !done {
					defs[name] = map[string]any{}
					if src, ok := components[name]; ok {
						defs[name] = localizeValue(map[string]any(src), defs, components)
					}
				}
				cp := map[string]any{}
				for k, val := range t {
					if k != "$ref" {
						cp[k] = localizeValue(val, defs, components)
					}
				}
				cp["$ref"] = "#/$defs/" + name
				return cp
			}
			return t
		}
		cp := map[string]any{}
		for k, val := range t {
			if k == "$schema" {
				continue
			}
			cp[k] = localizeValue(val, defs, components)
		}
		return cp
	case []any:
		cp := make([]any, len(t))
		for i, val := range t {
			cp[i] = localizeValue(val, defs, components)
		}
		return cp
	default:
		return v
	}
}
