package wire

import (
	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"strconv"
	"strings"
)

// Method names one daemon method.
type Method string

// Code names one closed daemon failure.
type Code string

// HandshakeHello proposes one connection.
type HandshakeHello struct {
	Profile       string   `json:"profile"`
	ObjectFormats []string `json:"object_formats"`
	Features      []string `json:"features"`
	ProtocolMajor uint32   `json:"protocol_major"`
	ProtocolMinor uint32   `json:"protocol_minor"`
}

// HandshakeAccept answers one proposal.
type HandshakeAccept struct {
	Profile       string   `json:"profile"`
	ObjectFormats []string `json:"object_formats"`
	Features      []string `json:"features"`
	ProtocolMajor uint32   `json:"protocol_major"`
	ProtocolMinor uint32   `json:"protocol_minor"`
}

// InvocationOpen starts one typed call.
type InvocationOpen struct {
	RequestID  *string `json:"request_id,omitempty"`
	ArgsJSON   *string `json:"args_json,omitempty"`
	SessionID  *string `json:"session_id,omitempty"`
	OnBehalfOf *string `json:"on_behalf_of,omitempty"`
	Repo       string  `json:"repo"`
	Method     string  `json:"method"`

	TransportAuth *TransportAuth `json:"transport_auth,omitempty"`
}

type ServiceOpen struct {
	SessionID    *string   `json:"session_id,omitempty"`
	RequestID    *string   `json:"request_id,omitempty"`
	OnBehalfOf   *string   `json:"on_behalf_of,omitempty"`
	Service      string    `json:"service"`
	Repository   string    `json:"repository"`
	GitProtocol  string    `json:"git_protocol"`
	StdoutCredit uint64    `json:"stdout_credit"`
	Advertise    bool      `json:"advertise"`
	Limits       GitLimits `json:"limits"`
}

type GitLimits struct {
	MaxPackBytes   uint64 `json:"max_pack_bytes"`
	MaxObjects     uint64 `json:"max_objects"`
	MaxObjectBytes uint64 `json:"max_object_bytes"`
	MaxDeltaDepth  uint64 `json:"max_delta_depth"`
	MaxStdinBytes  uint64 `json:"max_stdin_bytes"`
	MaxPushOptions uint64 `json:"max_push_options"`
}

func DefaultGitLimits() GitLimits {
	return GitLimits{
		MaxPackBytes:   1 << 31,
		MaxObjects:     5000000,
		MaxObjectBytes: 512 << 20,
		MaxDeltaDepth:  128,
		MaxStdinBytes:  256 << 20,
		MaxPushOptions: 256,
	}
}

type CommandAccepted struct {
	InvocationID string `json:"invocation_id"`
	StreamID     uint64 `json:"stream_id"`
	StdinCredit  uint64 `json:"stdin_credit"`
}

type CommandExitReport struct {
	Signal       string   `json:"signal"`
	Outcome      string   `json:"outcome"`
	InvocationID string   `json:"invocation_id"`
	OperationIDs []string `json:"operation_ids"`
	ExitCode     int32    `json:"exit_code"`
	Cancelled    bool     `json:"cancelled"`
}

type HookPhase string

const (
	HookPreReceive  HookPhase = "pre_receive"
	HookPostReceive HookPhase = "post_receive"
)

type HookCommand struct {
	Name       string `json:"name"`
	Old        string `json:"old"`
	New        string `json:"new"`
	Authorized bool   `json:"authorized"`
}

type HookCall struct {
	Phase       HookPhase     `json:"phase"`
	Commands    []HookCommand `json:"commands"`
	PushOptions []string      `json:"push_options"`
	NewCommits  []string      `json:"new_commits"`
	PackBytes   uint64        `json:"pack_bytes"`
	DeadlineMs  uint64        `json:"deadline_ms"`
}

type HookVerdictKind string

const (
	HookAccept HookVerdictKind = "accept"
	HookReject HookVerdictKind = "reject"
	HookDivert HookVerdictKind = "divert"
)

type HookVerdict struct {
	Kind      HookVerdictKind
	Reason    string
	ReviewRef string
}

func AcceptVerdict() HookVerdict {
	return HookVerdict{Kind: HookAccept}
}

func RejectVerdict(reason string) HookVerdict {
	return HookVerdict{Kind: HookReject, Reason: reason}
}

func DivertVerdict(reviewRef string) HookVerdict {
	return HookVerdict{Kind: HookDivert, ReviewRef: reviewRef}
}

func (v HookVerdict) MarshalJSON() ([]byte, error) {
	switch v.Kind {
	case HookAccept:
		return jsonv2.Marshal("accept")
	case HookReject:
		return jsonv2.Marshal(map[string]map[string]string{"reject": {"reason": v.Reason}})
	case HookDivert:
		return jsonv2.Marshal(map[string]map[string]string{"divert": {"review_ref": v.ReviewRef}})
	default:
		return nil, fmt.Errorf("wire hook verdict: unknown kind %q", string(v.Kind))
	}
}

func (v *HookVerdict) UnmarshalJSON(data []byte) error {
	var word string
	if err := jsonv2.Unmarshal(data, &word, jsonv1.DefaultOptionsV1()); err == nil {
		if word != "accept" {
			return fmt.Errorf("wire hook verdict: unknown %q", word)
		}
		*v = AcceptVerdict()
		return nil
	}
	var outer map[string]jsonv1.RawMessage
	if err := jsonv2.Unmarshal(data, &outer, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("wire hook verdict: %w", err)
	}
	if raw, ok := outer["reject"]; ok {
		var inner struct {
			Reason string `json:"reason"`
		}
		if err := jsonv2.Unmarshal(raw, &inner, jsonv1.DefaultOptionsV1()); err != nil {
			return fmt.Errorf("wire hook verdict: %w", err)
		}
		*v = RejectVerdict(inner.Reason)
		return nil
	}
	if raw, ok := outer["divert"]; ok {
		var inner struct {
			ReviewRef string `json:"review_ref"`
		}
		if err := jsonv2.Unmarshal(raw, &inner, jsonv1.DefaultOptionsV1()); err != nil {
			return fmt.Errorf("wire hook verdict: %w", err)
		}
		*v = DivertVerdict(inner.ReviewRef)
		return nil
	}
	return fmt.Errorf("wire hook verdict: no known arm")
}

type HookRefVerdict struct {
	Name    string      `json:"name"`
	Verdict HookVerdict `json:"verdict"`
}

type HookReply struct {
	Reason    *string          `json:"reason"`
	Verdicts  []HookRefVerdict `json:"verdicts"`
	RejectAll bool             `json:"reject_all"`
}

func RejectAllHookReply(reason string) HookReply {
	return HookReply{RejectAll: true, Reason: &reason}
}

// InvocationAccepted admits one typed call.
type InvocationAccepted struct {
	Method     string `json:"method"`
	RepoHandle string `json:"repo_handle"`
	StreamID   uint64 `json:"stream_id"`
}

// ExitReport ends one typed call.
type ExitReport struct {
	Code        string `json:"code"`
	Outcome     string `json:"outcome"`
	CompletedAt uint64 `json:"completed_at"`
	Status      int32  `json:"status"`
}

// ErrorReply carries one typed failure.
type ErrorReply struct {
	Code    Code              `json:"code"`
	Message string            `json:"message"`
	Hints   []string          `json:"hints"`
	Fixes   []string          `json:"fixes"`
	Detail  jsonv1.RawMessage `json:"detail,omitempty"`
}

// WindowCredit grants outbound bytes to one stream.
type WindowCredit struct {
	Direction   string `json:"direction,omitempty"`
	CreditBytes uint64 `json:"credit_bytes"`
	Bytes       uint64 `json:"bytes,omitzero"`
}

func (c WindowCredit) Credit() uint64 {
	if c.Bytes != 0 {
		return c.Bytes
	}
	return c.CreditBytes
}

// SchemaArgs restricts one schema read.
type SchemaArgs struct {
	Method *string `json:"method,omitempty"`
}

// SessionOpenArgs opens one session.
type SessionOpenArgs struct {
	Capabilities *Capabilities     `json:"capabilities,omitempty"`
	Claims       map[string]string `json:"claims,omitempty"`
	Identity     *IdentityClaim    `json:"identity,omitempty"`
}

// Capabilities bounds one session.
type Capabilities struct {
	Attest       []string          `json:"attest,omitempty"`
	ClaimPaths   []string          `json:"claim_paths,omitempty"`
	Land         []string          `json:"land,omitempty"`
	PushRefs     []string          `json:"push_refs,omitempty"`
	ReadIntents  *ReadIntents      `json:"read_intents,omitempty"`
	Undo         jsonv1.RawMessage `json:"undo,omitempty"`
	WriteChanges jsonv1.RawMessage `json:"write_changes,omitempty"`
}

// ReadIntents grants private read access.
type ReadIntents struct {
	Claims     bool `json:"claims"`
	Provenance bool `json:"provenance"`
}

// IdentityClaim names one author.
type IdentityClaim struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// SessionOpened answers session.open without an envelope.
type SessionOpened struct {
	Session         string          `json:"session"`
	ReconnectSecret ReconnectSecret `json:"reconnect_secret"`
	Principal       string          `json:"principal"`
}

// ReconnectSecret resumes one session and never prints.
type ReconnectSecret struct {
	value string
}

// NewReconnectSecret wraps one raw secret.
func NewReconnectSecret(raw string) ReconnectSecret {
	return ReconnectSecret{value: raw}
}

// String hides the secret.
func (s ReconnectSecret) String() string {
	return "<redacted>"
}

// Raw exposes the secret for the resume call.
func (s ReconnectSecret) Raw() string {
	return s.value
}

// MarshalJSON writes the secret as a plain string.
func (s ReconnectSecret) MarshalJSON() ([]byte, error) {
	return jsonv2.Marshal(s.value)
}

// UnmarshalJSON reads the secret from a plain string.
func (s *ReconnectSecret) UnmarshalJSON(data []byte) error {
	var raw string
	if err := jsonv2.Unmarshal(data, &raw, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("wire reconnect secret: %w", err)
	}
	s.value = raw
	return nil
}

// SessionResumeArgs resumes one session.
type SessionResumeArgs struct {
	Session         string          `json:"session"`
	ReconnectSecret ReconnectSecret `json:"reconnect_secret"`
}

// SessionResumeReply answers session.resume inside an envelope.
type SessionResumeReply struct {
	Session      string           `json:"session"`
	Authority    SessionAuthority `json:"authority"`
	Capabilities Capabilities     `json:"capabilities"`
}

// SessionAuthority bounds one resumed session.
type SessionAuthority struct {
	Active       *ActiveAuthority       `json:"active,omitempty"`
	ReceiptsOnly *ReceiptsOnlyAuthority `json:"receipts_only,omitempty"`
}

// IsActive reports full authority.
func (a SessionAuthority) IsActive() bool {
	return a.Active != nil
}

// ActiveAuthority carries full authority until its expiry.
type ActiveAuthority struct {
	IdleExpiresAt string `json:"idle_expires_at"`
}

// ReceiptsOnlyAuthority carries receipt lookup until its horizon.
type ReceiptsOnlyAuthority struct {
	ReceiptsUntil string `json:"receipts_until"`
}

// ReplyEnvelope wraps one typed answer.
type ReplyEnvelope struct {
	RequestID     *string           `json:"request_id,omitempty"`
	Body          jsonv1.RawMessage `json:"body"`
	SchemaVersion SchemaVersion     `json:"schema_version"`
}

// SchemaVersion pins one protocol version.
type SchemaVersion struct {
	Major uint32 `json:"major"`
	Minor uint32 `json:"minor"`
}

// TransportAuthKind names one injected transport credential kind.
type TransportAuthKind string

const (
	// TransportAuthNone sends no credential.
	TransportAuthNone TransportAuthKind = "none"
	// TransportAuthBasic sends a user:password credential.
	TransportAuthBasic TransportAuthKind = "basic"
	// TransportAuthBearer sends a bearer token credential.
	TransportAuthBearer TransportAuthKind = "bearer"
)

// TransportAuth carries one credential injected for remote authentication
// on a single call. It never enters digests, receipts, or logs: String
// redacts the credential.
type TransportAuth struct {
	Kind       TransportAuthKind `json:"kind,omitempty"`
	Credential string            `json:"credential,omitempty"`
	Origin     string            `json:"origin,omitempty"`
}

// String names the kind and origin while redacting the credential.
func (a TransportAuth) String() string {
	return "TransportAuth{kind:" + string(a.Kind) + " origin:" + a.Origin + " credential:[redacted]}"
}

// GoString redacts like String: %#v must never dump the credential.
func (a TransportAuth) GoString() string {
	return "wire.TransportAuth{Kind:" + strconv.Quote(string(a.Kind)) +
		" Origin:" + strconv.Quote(a.Origin) + " Credential:\"[redacted]\"}"
}

// Validate fails closed on a half-specified credential: an explicit kind
// needs both the secret and a well-formed origin scope. None carries
// nothing and always passes.
func (a TransportAuth) Validate() error {
	if a.Kind == TransportAuthNone || a.Kind == "" {
		return nil
	}
	if a.Kind != TransportAuthBasic && a.Kind != TransportAuthBearer {
		return fmt.Errorf("TransportAuth: unknown kind %q", a.Kind)
	}
	if a.Credential == "" {
		return fmt.Errorf("TransportAuth: credential is required for kind %q", a.Kind)
	}
	scoped, err := OriginOf(a.Origin + "/")
	if err != nil || scoped != a.Origin {
		return fmt.Errorf("TransportAuth: origin must be scheme://host[:port]")
	}
	return nil
}

// OriginOf reports the origin scope of one http(s) URL: lowercase
// scheme://host:port with browser defaults (80/443) filled in. Userinfo
// never enters the scope. Only http and https have an origin.
func OriginOf(rawURL string) (string, error) {
	invalid := func() (string, error) {
		return "", fmt.Errorf("OriginOf: URL has no http(s) origin")
	}
	scheme, rest, ok := strings.Cut(rawURL, "://")
	if !ok {
		return invalid()
	}
	scheme = asciiLower(scheme)
	if scheme != "http" && scheme != "https" {
		return invalid()
	}
	authority, _, _ := strings.Cut(rest, "/")
	hostport := authority
	if _, after, found := strings.CutLast(authority, "@"); found {
		hostport = after
	}
	var host string
	var port uint16
	if bracketed, found := strings.CutPrefix(hostport, "["); found {
		end := strings.IndexByte(bracketed, ']')
		if end < 0 {
			return invalid()
		}
		host = asciiLower(bracketed[:end])
		if host == "" {
			return invalid()
		}
		tail := bracketed[end+1:]
		if tail != "" {
			portText, prefixed := strings.CutPrefix(tail, ":")
			if !prefixed {
				return invalid()
			}
			parsed, good := parsePortU16(portText)
			if !good {
				return invalid()
			}
			port = parsed
		}
	} else if head, tail, found := strings.CutLast(hostport, ":"); found && isAllDigits(tail) {
		if head == "" {
			return invalid()
		}
		parsed, good := parsePortU16(tail)
		if !good {
			return invalid()
		}
		host = asciiLower(head)
		port = parsed
	} else {
		if hostport == "" {
			return invalid()
		}
		host = asciiLower(hostport)
		port = 0
	}
	if port == 0 {
		if scheme == "https" {
			port = 443
		} else {
			port = 80
		}
	}
	return scheme + "://" + host + ":" + fmt.Sprint(port), nil
}

// AuthForMirror builds the Basic credential for mirroring to targetURL:
// username:token scoped to the URL's origin. It refuses an empty user or
// token, a URL embedding userinfo, and a non-http(s) URL.
func AuthForMirror(username, token, targetURL string) (TransportAuth, error) {
	var zero TransportAuth
	if username == "" || token == "" {
		return zero, fmt.Errorf("AuthForMirror: username and token are required")
	}
	if hasUserinfo(targetURL) {
		return zero, fmt.Errorf("AuthForMirror: remote URL embeds credentials")
	}
	origin, err := OriginOf(targetURL)
	if err != nil {
		return zero, fmt.Errorf("AuthForMirror: %w", err)
	}
	return TransportAuth{
		Kind:       TransportAuthBasic,
		Credential: username + ":" + token,
		Origin:     origin,
	}, nil
}

func hasUserinfo(rawURL string) bool {
	_, rest, ok := strings.Cut(rawURL, "://")
	if !ok {
		return false
	}
	authority := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority = rest[:i]
	}
	return strings.Contains(authority, "@")
}

func asciiLower(s string) string {
	raw := []byte(s)
	for i := range raw {
		if raw[i] >= 'A' && raw[i] <= 'Z' {
			raw[i] += 'a' - 'A'
		}
	}
	return string(raw)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range s {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func parsePortU16(s string) (uint16, bool) {
	if !isAllDigits(s) {
		return 0, false
	}
	var n uint32
	for i := range s {
		n = n*10 + uint32(s[i]-'0')
		if n > 0xffff {
			return 0, false
		}
	}
	return uint16(n), true
}
