package wire

import (
	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
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
	RequestID *string `json:"request_id,omitempty"`
	ArgsJSON  *string `json:"args_json,omitempty"`
	SessionID *string `json:"session_id,omitempty"`
	Repo      string  `json:"repo"`
	Method    string  `json:"method"`
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
