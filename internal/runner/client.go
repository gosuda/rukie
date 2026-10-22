package runner

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	jsonv2 "encoding/json/v2"
)

func encodeBase64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// Grant is a leased and started attempt with its job spec.
type Grant struct {
	Attempt AttemptRef       `json:"attempt"`
	Job     JobPayload       `json:"job"`
	Run     RunPayload       `json:"run"`
	Fetch   FetchPayload     `json:"fetch"`
	Secrets []map[string]any `json:"secrets"`
	Timers  map[string]int64 `json:"timers"`
}

// AttemptRef identifies a live attempt.
type AttemptRef struct {
	ID     string `json:"id"`
	Token  string `json:"token"`
	Number int    `json:"number"`
	Fence  int64  `json:"fence"`
}

// JobPayload is the job the runner must execute.
type JobPayload struct {
	Name         string            `json:"name"`
	SpecDigest   string            `json:"spec_digest"`
	ActionDigest string            `json:"action_digest"`
	Effects      string            `json:"effects"`
	Network      string            `json:"network"`
	Image        map[string]string `json:"image"`
	Steps        []StepPayload     `json:"steps"`
	Env          map[string]string `json:"env"`
	Artifacts    []string          `json:"artifacts"`
	TimeoutS     int64             `json:"timeout_s"`
	Pure         bool              `json:"pure"`
}

// StepPayload is one shell step.
type StepPayload struct {
	Name string `json:"name"`
	Run  string `json:"run"`
}

// RunPayload identifies the run.
type RunPayload struct {
	ID        string `json:"id"`
	Trust     string `json:"trust"`
	Subject   string `json:"subject"`
	Tree      string `json:"tree"`
	SpecClaim string `json:"spec_source"`
}

// FetchPayload tells the runner where the tree lives.
type FetchPayload struct {
	URL        string `json:"url"`
	Want       string `json:"want"`
	ExpectTree string `json:"expect_tree"`
}

// LeaseLostError marks a fenced-out attempt.
type LeaseLostError struct {
	Code string
}

func (e *LeaseLostError) Error() string {
	return e.Code + ": the lease is lost"
}

// Client speaks the /-/ci/v1/ runner protocol.
type Client struct {
	Base   string
	HTTP   *http.Client
	Runner string
	Token  string
}

// NewClient builds a protocol client for server with runner credentials.
func NewClient(server, runnerID, token string) *Client {
	return &Client{
		Base:   strings.TrimSuffix(server, "/"),
		HTTP:   &http.Client{Timeout: 40 * time.Second},
		Runner: runnerID,
		Token:  token,
	}
}

func (c *Client) post(path string, token string, body any) ([]byte, int, error) {
	raw, err := jsonv2.Marshal(body)
	if err != nil {
		return nil, 0, fmt.Errorf("post: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, c.Base+path, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, fmt.Errorf("post: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("post: %w", err)
	}
	return out, resp.StatusCode, nil
}

func problemCode(body []byte) string {
	var decoded struct {
		Code string `json:"code"`
	}
	if err := jsonv2.Unmarshal(body, &decoded); err != nil {
		return ""
	}
	return decoded.Code
}

func leaseLost(code string) bool {
	return code == "E_LEASE_LOST" || code == "E_NOT_STARTED"
}

// Lease requests one attempt, waiting up to waitS for work.
func (c *Client) Lease(waitS int) (*Grant, error) {
	out, status, err := c.post("/-/ci/v1/lease", c.Token, map[string]any{
		"runner_id": c.Runner,
		"wait_ms":   waitS * 1000,
	})
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("Client.Lease: %s", problemCode(out))
	}
	var grant Grant
	if err := jsonv2.Unmarshal(out, &grant); err != nil {
		return nil, fmt.Errorf("Client.Lease: %w", err)
	}
	return &grant, nil
}

// HeartbeatReply is the server heartbeat answer.
type HeartbeatReply struct {
	State   string `json:"state"`
	Cancel  string `json:"cancel"`
	LogAck  int64  `json:"log_ack_offset"`
	Persist int64  `json:"heartbeat_persisted_at_ms"`
}

// Heartbeat sends one fenced heartbeat.
func (c *Client) Heartbeat(attempt AttemptRef) (HeartbeatReply, error) {
	var reply HeartbeatReply
	out, status, err := c.post("/-/ci/v1/attempts/"+attempt.ID+"/heartbeat", attempt.Token, map[string]any{
		"fence": attempt.Fence,
	})
	if err != nil {
		return reply, err
	}
	if status != http.StatusOK {
		code := problemCode(out)
		if leaseLost(code) {
			return reply, &LeaseLostError{Code: code}
		}
		return reply, fmt.Errorf("Client.Heartbeat: %s", code)
	}
	if err := jsonv2.Unmarshal(out, &reply); err != nil {
		return reply, fmt.Errorf("Client.Heartbeat: %w", err)
	}
	return reply, nil
}

// LogEvent marks a step transition inside a chunk.
type LogEvent struct {
	Kind     string `json:"kind"`
	Index    int    `json:"index"`
	Offset   int64  `json:"offset"`
	ExitCode int    `json:"exit_code"`
}

// AppendLog ships one digest-checked chunk.
func (c *Client) AppendLog(attempt AttemptRef, seq, offset int64, data []byte, events []LogEvent) (int64, error) {
	out, status, err := c.post("/-/ci/v1/attempts/"+attempt.ID+"/log.append", attempt.Token, map[string]any{
		"fence": attempt.Fence, "seq": seq, "offset": offset,
		"sha256": Digest(data), "data_b64": encodeBase64(data), "events": events,
	})
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		code := problemCode(out)
		if leaseLost(code) {
			return 0, &LeaseLostError{Code: code}
		}
		return 0, fmt.Errorf("Client.AppendLog: %s", code)
	}
	var decoded struct {
		Ack int64 `json:"ack_offset"`
	}
	if err := jsonv2.Unmarshal(out, &decoded); err != nil {
		return 0, fmt.Errorf("Client.AppendLog: %w", err)
	}
	return decoded.Ack, nil
}

// PutArtifact uploads one content-addressed blob.
func (c *Client) PutArtifact(attempt AttemptRef, data []byte) error {
	digest := Digest(data)
	req, err := http.NewRequest(http.MethodPut, c.Base+"/-/ci/v1/attempts/"+attempt.ID+"/artifact.put/"+digest, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("Client.PutArtifact: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+attempt.Token)
	req.Header.Set("Rukie-Attempt", attempt.ID)
	req.Header.Set("Rukie-Fence", fmt.Sprintf("%d", attempt.Fence))
	req.ContentLength = int64(len(data))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("Client.PutArtifact: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("Client.PutArtifact: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		code := problemCode(out)
		if leaseLost(code) {
			return &LeaseLostError{Code: code}
		}
		return fmt.Errorf("Client.PutArtifact: %s", code)
	}
	return nil
}

// FinishInput is the terminal verdict for an attempt.
type FinishInput struct {
	Artifacts []ArtifactRef
	Outcome   string
	Class     string
	LogSHA    string
	ExitCode  int
	Fence     int64
	LogSeq    int64
	LogEnd    int64
}

// ArtifactRef links one uploaded blob.
type ArtifactRef struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// FinishReply is the stored verdict answer.
type FinishReply struct {
	Attempt  string `json:"attempt"`
	Job      string `json:"job"`
	Run      string `json:"run"`
	Replayed bool   `json:"replayed"`
}

// Finish reports the terminal verdict with fencing.
func (c *Client) Finish(attempt AttemptRef, in FinishInput) (FinishReply, error) {
	var reply FinishReply
	out, status, err := c.post("/-/ci/v1/attempts/"+attempt.ID+"/finish", attempt.Token, map[string]any{
		"outcome": in.Outcome, "class": in.Class, "exit_code": in.ExitCode, "fence": in.Fence,
		"log":       map[string]any{"sha256": in.LogSHA, "last_seq": in.LogSeq, "end_offset": in.LogEnd},
		"artifacts": in.Artifacts,
	})
	if err != nil {
		return reply, err
	}
	if status != http.StatusOK {
		code := problemCode(out)
		if leaseLost(code) {
			return reply, &LeaseLostError{Code: code}
		}
		return reply, fmt.Errorf("Client.Finish: %s", code)
	}
	if err := jsonv2.Unmarshal(out, &reply); err != nil {
		return reply, fmt.Errorf("Client.Finish: %w", err)
	}
	return reply, nil
}
