package ci

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/gosuda/rukie/internal/store"
)

type Problem struct{ Code string }

func (p *Problem) Error() string { return p.Code }
func problem(code string) error  { return &Problem{Code: code} }
func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	if p, ok := errors.AsType[*Problem](err); ok {
		return p.Code
	}
	return ""
}

type Config struct {
	Now     func() time.Time
	Mono    func() int64
	Deliver func(ctx context.Context, row Outbox, signature string) (string, error)
	SignKey ed25519.PrivateKey
	SealKey []byte
}
type Controller struct {
	bootMono  time.Time
	store     *store.Store
	now       func() time.Time
	mono      func() int64
	deliver   func(ctx context.Context, row Outbox, signature string) (string, error)
	signKey   ed25519.PrivateKey
	sealKey   []byte
	lastWall  int64
	lastMono  int64
	epoch     atomic.Int64
	ready     atomic.Bool
	baselined bool
}
type Run struct {
	CancelReason string   `json:"cancel_reason"`
	Repo         string   `json:"repo"`
	Owner        string   `json:"owner"`
	ID           string   `json:"id"`
	Principal    string   `json:"principal"`
	Subject      string   `json:"subject"`
	Tree         string   `json:"tree"`
	BaseCommit   string   `json:"base_commit"`
	TriggerKey   string   `json:"trigger_key"`
	SpecFrom     string   `json:"spec_from"`
	Trust        string   `json:"trust"`
	Group        string   `json:"group"`
	Class        string   `json:"class"`
	State        string   `json:"state"`
	Jobs         []string `json:"jobs"`
	Spec         Spec     `json:"spec"`
	RepoID       int64    `json:"repo_id"`
	CreatedAt    int64    `json:"created_at"`
	FinishedAt   int64    `json:"finished_at"`
}
type SpecError struct {
	Pointer string
	Message string
	Line    int
	Col     int
}

func (e *SpecError) Error() string {
	if e.Pointer != "" {
		return "ValidateSpec: " + e.Pointer + ": " + e.Message
	}
	if e.Line > 0 {
		return "DecodeSpec: " + e.Message
	}
	return "ValidateSpec: " + e.Message
}
func specError(pointer, message string) error { return &SpecError{Pointer: pointer, Message: message} }

type Job struct {
	ID             string
	RunID          string
	SpecDigest     string
	ActionDigest   string
	State          string
	WaitReason     string
	CurrentAttempt string
	Spec           JobSpec
	ReadyAt        int64
	QueuedSeq      int64
	AttemptsUsed   int
	Fence          int64
}
type Runner struct {
	ID             string
	Scope          string
	TokenHash      string
	Status         string
	SandboxProfile string
	Labels         []string
	PubKey         []byte
	Capacity       int
}
type StepState struct {
	Name        string
	State       string
	StartOffset int64
	EndOffset   int64
	ExitCode    int
}
type LogEvent struct {
	Kind     string
	Index    int
	Offset   int64
	ExitCode int
}

const (
	maxLogBytes = 64 << 20
	keepLogHead = 4 << 20
)

type Attempt struct {
	Chunks                map[int64]LogAppend
	ResultDigest          string
	JobID                 string
	RunnerID              string
	CancelReason          string
	State                 string
	TokenHash             string
	ID                    string
	Reply                 FinishReply
	Log                   []byte
	Elided                [][2]int64
	Steps                 []StepState
	StartDeadlineAt       int64
	LostAt                int64
	PreRestartHeartbeatAt int64
	RebasedAt             int64
	CompletedAt           int64
	StartedAt             int64
	GrantedAt             int64
	DeadlineAt            int64
	LogBase               int64
	LogSeq                int64
	Number                int
	LeaseExpiresAt        int64
	LastHeartbeatAt       int64
	Fence                 int64
}
type SealedSecret struct {
	Name       string
	Ephemeral  []byte
	Nonce      []byte
	Ciphertext []byte
	Version    int64
}
type secretValue struct {
	Nonce      []byte
	Ciphertext []byte
	Version    int64
}
type Cache struct {
	Producer  string
	Artifacts []Artifact
}
type Outbox struct {
	Statement Statement
	ID        string
	Chain     string
	Digest    string
	State     string
	LastError string
	Payload   []byte
	Seq       int64
	RepoID    int64
	Attempts  int
	NextAt    int64
}
type ciState struct {
	Fair       map[string]float64
	Secrets    map[string]secretValue
	Attempts   map[string]Attempt
	Runners    map[string]Runner
	Cache      map[string]Cache
	Chains     map[string]string
	ChainTrust map[string]string
	Runs       map[string]Run
	Jobs       map[string]Job
	Triggers   map[string]string
	Blobs      map[string][]byte
	Artifacts  map[string][]Artifact
	Outbox     []Outbox
	Sequence   int64
}
type CreateRunRequest struct {
	BaseCommit string
	Owner      string
	Repo       string
	Principal  string
	Subject    string
	Tree       string
	SpecFrom   string
	Trust      string
	TriggerKey string
	Group      string
	Class      string
	Spec       Spec
	RepoID     int64
}
type Lease struct {
	Token   string
	Secrets []SealedSecret
	Run     Run
	Job     Job
	Attempt Attempt
}
type HeartbeatReply struct {
	State                string
	Cancel               string
	HeartbeatPersistedAt int64
	LogAckOffset         int64
}
type Artifact struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}
type FinishRequest struct {
	Outcome      string     `json:"outcome"`
	Class        string     `json:"class"`
	LogDigest    string     `json:"log_digest"`
	ResultDigest string     `json:"result_digest,omitempty"`
	Artifacts    []Artifact `json:"artifacts"`
	Fence        int64      `json:"fence"`
	ExitCode     int        `json:"exit_code"`
	LogSeq       int64      `json:"log_seq"`
	LogEnd       int64      `json:"log_end"`
}
type FinishReply struct {
	Attempt  string `json:"attempt"`
	Job      string `json:"job"`
	Run      string `json:"run"`
	Replayed bool   `json:"replayed"`
}
type LogAppend struct {
	SHA256 string
	Data   []byte
	Events []LogEvent
	Fence  int64
	Seq    int64
	Offset int64
}

func NewController(st *store.Store, cfg Config) *Controller {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	mono := cfg.Mono
	var bootMono time.Time
	if mono == nil {
		bootMono = time.Now()
		mono = func() int64 { return time.Since(bootMono).Nanoseconds() }
	}
	return &Controller{store: st, now: now, mono: mono, signKey: cfg.SignKey, sealKey: cfg.SealKey, deliver: cfg.Deliver, bootMono: bootMono}
}
func newState() ciState {
	return ciState{
		Runs: map[string]Run{}, Jobs: map[string]Job{}, Attempts: map[string]Attempt{}, Runners: map[string]Runner{},
		Cache: map[string]Cache{}, Fair: map[string]float64{}, Chains: map[string]string{}, ChainTrust: map[string]string{},
		Triggers: map[string]string{}, Blobs: map[string][]byte{}, Artifacts: map[string][]Artifact{}, Secrets: map[string]secretValue{},
	}
}
func decodeState(data []byte) (ciState, error) {
	state := newState()
	err := json.Unmarshal(data, &state)
	if err != nil {
		return ciState{}, fmt.Errorf("decodeState: %w", err)
	}
	return state, nil
}
func randomID(prefix string) (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("randomID: %w", err)
	}
	return prefix + hex.EncodeToString(data[:]), nil
}
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (c *Controller) Recover(ctx context.Context) error {
	c.ready.Store(false)
	err := c.store.Write(ctx, func(tx *store.WriteTx) error {
		epoch, fence, data, err := tx.CIState()
		if err != nil {
			return err
		}
		state, err := decodeState(data)
		if err != nil {
			return err
		}
		now := c.now().UnixMilli()
		active := map[string]bool{}
		fences := map[int64]bool{}
		for id, a := range state.Attempts {
			if a.Fence <= 0 || a.Fence > fence || fences[a.Fence] {
				return problem("E_CORRUPT")
			}
			fences[a.Fence] = true
			if !attemptActive(a.State) {
				continue
			}
			j, ok := state.Jobs[a.JobID]
			if !ok || j.Fence != a.Fence || active[j.ID] {
				return problem("E_CORRUPT")
			}
			active[j.ID] = true
			rebaseAttempt(&a, j, now)
			state.Attempts[id] = a
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if err = tx.SetCIState(epoch, epoch+1, fence, encoded); err != nil {
			return err
		}
		c.epoch.Store(epoch + 1)
		return nil
	})
	if err != nil {
		return fmt.Errorf("Recover: %w", err)
	}
	c.lastWall = c.now().UnixMilli()
	c.lastMono = c.mono()
	c.baselined = true
	c.ready.Store(true)
	return nil
}
func rebaseAttempt(a *Attempt, j Job, now int64) {
	if a.State == "granted" {
		a.StartDeadlineAt = now + 60000
		return
	}
	a.PreRestartHeartbeatAt = a.LastHeartbeatAt
	a.LastHeartbeatAt = now
	a.LeaseExpiresAt = now + 30000
	a.LostAt = now + 150000
	a.RebasedAt = now
	a.State = "started"
	timeout, _ := time.ParseDuration(j.Spec.Timeout)
	a.DeadlineAt = now + timeout.Milliseconds()
}
func attemptActive(state string) bool {
	return state == "granted" || state == "started" || state == "suspect"
}
func jobTerminal(state string) bool {
	return slices.Contains([]string{"passed", "failed", "errored", "timed_out", "cancelled", "skipped", "cached", "carried"}, state)
}
func jobSuccess(state string) bool {
	return state == "passed" || state == "cached" || state == "carried"
}
func (c *Controller) mutate(ctx context.Context, fn func(*ciState, *int64, int64) error) error {
	if !c.ready.Load() {
		return problem("E_RECOVERING")
	}
	err := c.store.Write(ctx, func(tx *store.WriteTx) error {
		epoch, fence, data, err := tx.CIState()
		if err != nil {
			return err
		}
		if epoch != c.epoch.Load() {
			return problem("E_LEASE_LOST")
		}
		state, err := decodeState(data)
		if err != nil {
			return err
		}
		if err = fn(&state, &fence, c.now().UnixMilli()); err != nil {
			return err
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			return err
		}
		return tx.SetCIState(epoch, epoch, fence, encoded)
	})
	if err != nil {
		return fmt.Errorf("mutate: %w", err)
	}
	return nil
}
func (c *Controller) snapshot(ctx context.Context) (ciState, error) {
	if !c.ready.Load() {
		return ciState{}, problem("E_RECOVERING")
	}
	var state ciState
	err := c.store.Read(ctx, func(tx *store.ReadTx) error {
		epoch, _, data, err := tx.CIState()
		if err != nil {
			return err
		}
		if epoch != c.epoch.Load() {
			return problem("E_LEASE_LOST")
		}
		state, err = decodeState(data)
		return err
	})
	if err != nil {
		return ciState{}, fmt.Errorf("snapshot: %w", err)
	}
	return state, nil
}

func (c *Controller) CreateRun(ctx context.Context, req CreateRunRequest) (Run, error) {
	if err := ValidateSpec(req.Spec); err != nil {
		return Run{}, err
	}
	jobs, err := ExpandMatrix(req.Spec)
	if err != nil {
		return Run{}, err
	}
	if req.Trust != "trusted" && req.Trust != "untrusted" {
		return Run{}, problem("E_INVALID")
	}
	if req.Trust == "untrusted" && req.SpecFrom != "base" {
		return Run{}, problem("E_FORBIDDEN")
	}
	if req.Class == "" {
		req.Class = "interactive"
	}
	if !slices.Contains([]string{"land", "interactive", "push", "bulk"}, req.Class) {
		return Run{}, problem("E_INVALID")
	}
	id, err := randomID("run_")
	if err != nil {
		return Run{}, err
	}
	var run Run
	err = c.mutate(ctx, func(state *ciState, _ *int64, now int64) error {
		key := fmt.Sprintf("%d:%s", req.RepoID, req.TriggerKey)
		if prior, exists := state.Triggers[key]; exists {
			run = state.Runs[prior]
			return nil
		}
		run = Run{ID: id, RepoID: req.RepoID, Owner: req.Owner, Repo: req.Repo, Principal: req.Principal, Subject: req.Subject, Tree: req.Tree, BaseCommit: req.BaseCommit, SpecFrom: req.SpecFrom, Trust: req.Trust, TriggerKey: req.TriggerKey, Group: req.Group, Class: req.Class, State: "pending", CreatedAt: now, Spec: req.Spec}
		for _, spec := range jobs {
			digest, e := JobSpecDigest(spec)
			if e != nil {
				return e
			}
			jobID, e := randomID("job_")
			if e != nil {
				return e
			}
			state.Sequence++
			job := Job{ID: jobID, RunID: id, Spec: spec, SpecDigest: digest, State: "waiting", QueuedSeq: state.Sequence, ReadyAt: now}
			if len(spec.Needs) == 0 {
				job.State = "queued"
			}
			if req.Trust == "untrusted" && (spec.Network == "egress" || len(spec.Secrets) > 0) {
				job.State = "errored"
			}
			state.Jobs[jobID] = job
			run.Jobs = append(run.Jobs, jobID)
		}
		state.Runs[id] = run
		state.Triggers[key] = id
		if req.Group != "" {
			for priorID, prior := range state.Runs {
				if priorID != id && prior.RepoID == run.RepoID && prior.Group == run.Group && !runTerminal(prior.State) {
					cancelRun(state, priorID, "superseded", now)
				}
			}
		}
		if aerr := advanceJobs(state, id, now); aerr != nil {
			return aerr
		}
		run = state.Runs[id]
		return nil
	})
	return run, err
}
func runTerminal(state string) bool {
	return slices.Contains([]string{"succeeded", "failed", "errored", "cancelled"}, state)
}

func (c *Controller) CreateRunner(ctx context.Context, labels []string, scope string, capacity int, profile string, pubkey []byte) (Runner, string, error) {
	if len(labels) == 0 || capacity < 1 || capacity > 256 || (scope != "trusted" && scope != "untrusted") {
		return Runner{}, "", problem("E_INVALID")
	}
	if len(pubkey) != 0 && len(pubkey) != 32 {
		return Runner{}, "", problem("E_INVALID")
	}
	id, err := randomID("rnr_")
	if err != nil {
		return Runner{}, "", err
	}
	token, err := randomID("")
	if err != nil {
		return Runner{}, "", err
	}
	runner := Runner{ID: id, Labels: labels, Scope: scope, Capacity: capacity, TokenHash: tokenHash(token), Status: "active", SandboxProfile: profile, PubKey: slices.Clone(pubkey)}
	err = c.mutate(ctx, func(state *ciState, _ *int64, _ int64) error { state.Runners[id] = runner; return nil })
	return runner, token, err
}
func (c *Controller) RevokeRunner(ctx context.Context, id string) error {
	return c.mutate(ctx, func(state *ciState, fence *int64, now int64) error {
		runner, exists := state.Runners[id]
		if !exists {
			return problem("E_NOT_FOUND")
		}
		runner.Status = "revoked"
		state.Runners[id] = runner
		affected := map[string]bool{}
		for aid, a := range state.Attempts {
			if a.RunnerID != id || !attemptActive(a.State) {
				continue
			}
			*fence++
			a.State = "lost"
			a.LostAt = now
			a.Fence = *fence
			state.Attempts[aid] = a
			if j, ok := state.Jobs[a.JobID]; ok {
				j.Fence = 0
				retryJob(&j, "lost", now)
				state.Jobs[a.JobID] = j
				affected[j.RunID] = true
			}
		}
		for runID := range affected {
			if err := advanceJobs(state, runID, now); err != nil {
				return err
			}
		}
		return nil
	})
}
func eligible(state *ciState, j Job, runner Runner, now int64) bool {
	if j.State != "queued" || j.ReadyAt > now {
		return false
	}
	run := state.Runs[j.RunID]
	if runTerminal(run.State) || run.State == "cancelling" {
		return false
	}
	if run.Trust == "untrusted" && runner.Scope != "untrusted" {
		return false
	}
	if len(j.Spec.Secrets) > 0 && (run.Trust != "trusted" || runner.Scope != "trusted") {
		return false
	}
	for _, label := range j.Spec.RunsOn {
		if !slices.Contains(runner.Labels, label) {
			return false
		}
	}
	repoCount, principalCount, runnerCount := 0, 0, 0
	for _, a := range state.Attempts {
		if !attemptActive(a.State) {
			continue
		}
		other := state.Runs[state.Jobs[a.JobID].RunID]
		if other.RepoID == run.RepoID {
			repoCount++
		}
		if other.Principal == run.Principal {
			principalCount++
		}
		if a.RunnerID == runner.ID {
			runnerCount++
		}
	}
	return repoCount < 8 && principalCount < 16 && runnerCount < runner.Capacity
}
func fairKeys(run Run) []string {
	return []string{"class:" + run.Class, fmt.Sprintf("repo:%s:%d", run.Class, run.RepoID), "principal:" + run.Principal}
}
func fairLess(state *ciState, a, b Job) bool {
	ak := fairKeys(state.Runs[a.RunID])
	bk := fairKeys(state.Runs[b.RunID])
	for i := range ak {
		av, bv := state.Fair[ak[i]], state.Fair[bk[i]]
		if av != bv {
			return av < bv
		}
	}
	return a.QueuedSeq < b.QueuedSeq
}
func (c *Controller) Lease(ctx context.Context, runnerID, token string) (*Lease, error) {
	var lease *Lease
	err := c.mutate(ctx, func(state *ciState, fence *int64, now int64) error {
		c.clockGuard(state, now)
		runner, exists := state.Runners[runnerID]
		if !exists || runner.Status != "active" || runner.TokenHash != tokenHash(token) {
			return problem("E_UNAUTHENTICATED")
		}
		if err := reap(state, now); err != nil {
			return err
		}
		candidates := make([]Job, 0)
		for _, j := range state.Jobs {
			if eligible(state, j, runner, now) {
				candidates = append(candidates, j)
			}
		}
		slices.SortFunc(candidates, func(a, b Job) int {
			if fairLess(state, a, b) {
				return -1
			}
			if fairLess(state, b, a) {
				return 1
			}
			return strings.Compare(a.ID, b.ID)
		})
		for _, j := range candidates {
			if j.Spec.Pure {
				if hit, ok := state.Cache[j.ActionDigest]; ok {
					state.Artifacts[j.ID] = slices.Clone(hit.Artifacts)
					j.State = "cached"
					j.WaitReason = ""
					state.Jobs[j.ID] = j
					if err := addVerdict(state, j, now); err != nil {
						return err
					}
					if err := advanceJobs(state, j.RunID, now); err != nil {
						return err
					}
					continue
				}
				if singleFlightHolder(state, j) {
					j.State = "queued"
					j.WaitReason = "single_flight"
					state.Jobs[j.ID] = j
					continue
				}
			}
			j.WaitReason = ""
			id, e := randomID("att_")
			if e != nil {
				return e
			}
			secret, e := randomID("")
			if e != nil {
				return e
			}
			*fence++
			a := Attempt{ID: id, JobID: j.ID, RunnerID: runnerID, Fence: *fence, Number: j.AttemptsUsed + 1, State: "granted", TokenHash: tokenHash(secret), GrantedAt: now, StartDeadlineAt: now + 60000, LogSeq: -1, Chunks: map[int64]LogAppend{}}
			j.State = "leased"
			j.CurrentAttempt = id
			j.Fence = *fence
			state.Jobs[j.ID] = j
			state.Attempts[id] = a
			run := state.Runs[j.RunID]
			keys := fairKeys(run)
			weight := map[string]float64{"land": 8, "interactive": 4, "push": 2, "bulk": 1}[run.Class]
			state.Fair[keys[0]] += 300 / weight
			state.Fair[keys[1]] += 300
			state.Fair[keys[2]] += 300
			sealed, sealErr := c.sealSecrets(state, run, j, runner)
			if sealErr != nil {
				return sealErr
			}
			lease = &Lease{Attempt: a, Token: secret, Run: run, Job: j, Secrets: sealed}
			return nil
		}
		return nil
	})
	return lease, err
}
func getAttempt(state *ciState, id, token string, fence int64) (Attempt, Job, error) {
	a, ok := state.Attempts[id]
	if !ok {
		return Attempt{}, Job{}, problem("E_NOT_FOUND")
	}
	j := state.Jobs[a.JobID]
	if a.TokenHash != tokenHash(token) {
		return Attempt{}, Job{}, problem("E_UNAUTHENTICATED")
	}
	if runner, ok := state.Runners[a.RunnerID]; !ok || runner.Status != "active" {
		return Attempt{}, Job{}, problem("E_UNAUTHENTICATED")
	}
	if a.Fence != fence || j.Fence != fence || !attemptActive(a.State) {
		return Attempt{}, Job{}, problem("E_LEASE_LOST")
	}
	return a, j, nil
}
func (c *Controller) Start(ctx context.Context, id, token string, fence int64) (HeartbeatReply, error) {
	var reply HeartbeatReply
	err := c.mutate(ctx, func(state *ciState, _ *int64, now int64) error {
		a, j, err := getAttempt(state, id, token, fence)
		if err != nil {
			return err
		}
		if a.State != "granted" {
			if a.State == "started" {
				reply = heartbeatReply(a)
				return nil
			}
			return problem("E_LEASE_LOST")
		}
		if now > a.StartDeadlineAt {
			return problem("E_LEASE_LOST")
		}
		a.State = "started"
		a.StartedAt = now
		a.LastHeartbeatAt = now
		a.LeaseExpiresAt = now + 30000
		a.LostAt = now + 150000
		timeout, _ := time.ParseDuration(j.Spec.Timeout)
		a.DeadlineAt = now + timeout.Milliseconds()
		j.State = "running"
		j.AttemptsUsed++
		state.Jobs[j.ID] = j
		state.Attempts[id] = a
		run := state.Runs[j.RunID]
		run.State = "running"
		state.Runs[run.ID] = run
		reply = heartbeatReply(a)
		return nil
	})
	return reply, err
}
func heartbeatReply(a Attempt) HeartbeatReply {
	return HeartbeatReply{State: a.State, Cancel: a.CancelReason, HeartbeatPersistedAt: a.LastHeartbeatAt, LogAckOffset: a.LogBase + int64(len(a.Log))}
}
func (c *Controller) Heartbeat(ctx context.Context, id, token string, fence int64) (HeartbeatReply, error) {
	var reply HeartbeatReply
	err := c.mutate(ctx, func(state *ciState, _ *int64, now int64) error {
		a, _, err := getAttempt(state, id, token, fence)
		if err != nil {
			return err
		}
		if a.State == "granted" {
			return problem("E_NOT_STARTED")
		}
		if now > a.LostAt {
			return problem("E_LEASE_LOST")
		}
		a.State = "started"
		a.LastHeartbeatAt = max(now, a.LastHeartbeatAt)
		a.LeaseExpiresAt = a.LastHeartbeatAt + 30000
		a.LostAt = a.LastHeartbeatAt + 150000
		state.Attempts[id] = a
		reply = heartbeatReply(a)
		return nil
	})
	return reply, err
}
func (c *Controller) CreateSecret(ctx context.Context, repoID int64, name string, value []byte) error {
	if len(c.sealKey) == 0 {
		return problem("E_FORBIDDEN")
	}
	if len(name) == 0 || len(name) > 64 || len(value) == 0 || len(value) > 65536 {
		return problem("E_INVALID")
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("CreateSecret: %w", err)
	}
	block, err := aes.NewCipher(c.sealKey)
	if err != nil {
		return fmt.Errorf("CreateSecret: %w", err)
	}
	sealed, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("CreateSecret: %w", err)
	}
	key := secretKey(repoID, name)
	return c.mutate(ctx, func(state *ciState, _ *int64, _ int64) error {
		previous, exists := state.Secrets[key]
		version := int64(1)
		if exists {
			version = previous.Version + 1
		}
		state.Secrets[key] = secretValue{Version: version, Nonce: nonce, Ciphertext: sealed.Seal(nil, nonce, value, []byte(key))}
		return nil
	})
}
func secretKey(repoID int64, name string) string { return fmt.Sprintf("%d:%s", repoID, name) }
func (c *Controller) sealSecrets(state *ciState, run Run, job Job, runner Runner) ([]SealedSecret, error) {
	if len(job.Spec.Secrets) == 0 {
		return nil, nil
	}
	if run.Trust != "trusted" || runner.Scope != "trusted" {
		return nil, problem("E_FORBIDDEN")
	}
	if len(runner.PubKey) != 32 {
		return nil, problem("E_INVALID")
	}
	peer, err := ecdh.X25519().NewPublicKey(runner.PubKey)
	if err != nil {
		return nil, fmt.Errorf("sealSecrets: %w", err)
	}
	out := make([]SealedSecret, 0, len(job.Spec.Secrets))
	for _, name := range job.Spec.Secrets {
		stored, exists := state.Secrets[secretKey(run.RepoID, name)]
		if !exists {
			return nil, problem("E_NOT_FOUND")
		}
		ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("sealSecrets: %w", err)
		}
		shared, err := ephemeral.ECDH(peer)
		if err != nil {
			return nil, fmt.Errorf("sealSecrets: %w", err)
		}
		key := make([]byte, 32)
		if _, herr := hkdf.New(sha256.New, shared, ephemeral.PublicKey().Bytes(), []byte(attestNamespace)).Read(key); herr != nil {
			return nil, fmt.Errorf("sealSecrets: %w", herr)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("sealSecrets: %w", err)
		}
		sealed, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("sealSecrets: %w", err)
		}
		nonce := make([]byte, 12)
		if _, err = rand.Read(nonce); err != nil {
			return nil, fmt.Errorf("sealSecrets: %w", err)
		}
		block2, err := aes.NewCipher(c.sealKey)
		if err != nil {
			return nil, fmt.Errorf("sealSecrets: %w", err)
		}
		opened, err := cipher.NewGCM(block2)
		if err != nil {
			return nil, fmt.Errorf("sealSecrets: %w", err)
		}
		plain, err := opened.Open(nil, stored.Nonce, stored.Ciphertext, []byte(secretKey(run.RepoID, name)))
		if err != nil {
			return nil, fmt.Errorf("sealSecrets: %w", err)
		}
		out = append(out, SealedSecret{Name: name, Version: stored.Version, Ephemeral: ephemeral.PublicKey().Bytes(), Nonce: nonce, Ciphertext: sealed.Seal(nil, nonce, plain, []byte(name))})
	}
	return out, nil
}
func (c *Controller) AppendLogs(ctx context.Context, id, token string, chunk LogAppend) (int64, error) {
	var ack int64
	err := c.mutate(ctx, func(state *ciState, _ *int64, now int64) error {
		a, _, err := getAttempt(state, id, token, chunk.Fence)
		if err != nil {
			return err
		}
		if a.State == "granted" {
			return problem("E_NOT_STARTED")
		}
		if now > a.LostAt {
			return problem("E_LEASE_LOST")
		}
		if len(chunk.Data) > 65536 {
			return problem("E_LIMIT")
		}
		if Digest(chunk.Data) != "sha256:"+strings.TrimPrefix(chunk.SHA256, "sha256:") {
			return problem("E_DIGEST_MISMATCH")
		}
		if prior, ok := a.Chunks[chunk.Seq]; ok {
			if prior.Offset != chunk.Offset || prior.SHA256 != chunk.SHA256 {
				return problem("E_SEQ_CONFLICT")
			}
			ack = prior.Offset + int64(len(prior.Data))
			return nil
		}
		if chunk.Seq != a.LogSeq+1 || chunk.Offset != a.LogBase+int64(len(a.Log)) {
			return problem("E_OFFSET_GAP")
		}
		if err := applyStepEvents(&a, state.Jobs[a.JobID], chunk); err != nil {
			return err
		}
		elideLog(&a, int64(len(chunk.Data)))
		a.Log = append(a.Log, chunk.Data...)
		a.LogSeq = chunk.Seq
		a.Chunks[chunk.Seq] = chunk
		state.Attempts[id] = a
		ack = a.LogBase + int64(len(a.Log))
		return nil
	})
	return ack, err
}
func elideLog(a *Attempt, incoming int64) {
	if int64(len(a.Log))+incoming <= maxLogBytes {
		return
	}
	tailKeep := maxLogBytes - keepLogHead - incoming
	if tailKeep < 0 {
		tailKeep = 0
	}
	tail := slices.Clone(a.Log[int64(len(a.Log))-tailKeep:])
	from := a.LogBase + int64(keepLogHead)
	to := a.LogBase + int64(len(a.Log)-len(tail))
	if to > from {
		a.Elided = append(a.Elided, [2]int64{from, to})
	}
	a.Log = append(slices.Clone(a.Log[:keepLogHead]), tail...)
}
func applyStepEvents(a *Attempt, job Job, chunk LogAppend) error {
	steps := job.Spec.Steps
	for _, event := range chunk.Events {
		if event.Index < 0 || event.Index >= len(steps) {
			return problem("E_INVALID")
		}
		if event.Offset < chunk.Offset || event.Offset > chunk.Offset+int64(len(chunk.Data)) {
			return problem("E_INVALID")
		}
		for len(a.Steps) <= event.Index {
			a.Steps = append(a.Steps, StepState{Name: steps[len(a.Steps)].Name, State: "pending"})
		}
		step := &a.Steps[event.Index]
		switch event.Kind {
		case "step_started":
			if event.Index > 0 && a.Steps[event.Index-1].State != "passed" {
				return problem("E_INVALID")
			}
			if step.State != "pending" {
				return problem("E_INVALID")
			}
			step.State = "running"
			step.StartOffset = event.Offset
		case "step_finished":
			if step.State != "running" {
				return problem("E_INVALID")
			}
			if event.ExitCode == 0 {
				step.State = "passed"
			} else {
				step.State = "failed"
			}
			step.EndOffset = event.Offset
			step.ExitCode = event.ExitCode
			for i := event.Index + 1; i < len(a.Steps); i++ {
				a.Steps[i].State = "skipped"
			}
		default:
			return problem("E_INVALID")
		}
	}
	return nil
}
func (c *Controller) PutBlob(ctx context.Context, id, token string, fence int64, digest string, data []byte) error {
	if len(data) > 1<<30 {
		return problem("E_LIMIT")
	}
	if Digest(data) != digest {
		return problem("E_DIGEST_MISMATCH")
	}
	return c.mutate(ctx, func(state *ciState, _ *int64, now int64) error {
		a, _, err := getAttempt(state, id, token, fence)
		if err != nil {
			return err
		}
		if now > a.LostAt {
			return problem("E_LEASE_LOST")
		}
		state.Blobs[digest] = slices.Clone(data)
		return nil
	})
}

func FinishDigest(req FinishRequest) (string, error) {
	req.ResultDigest = ""
	data, err := CanonicalJSON(req)
	if err != nil {
		return "", err
	}
	return Digest(data), nil
}
func (c *Controller) Complete(ctx context.Context, id, token string, req FinishRequest) (FinishReply, error) {
	var reply FinishReply
	err := c.mutate(ctx, func(state *ciState, _ *int64, now int64) error {
		a, exists := state.Attempts[id]
		if !exists {
			return problem("E_NOT_FOUND")
		}
		if a.TokenHash != tokenHash(token) {
			return problem("E_UNAUTHENTICATED")
		}
		if runner, ok := state.Runners[a.RunnerID]; !ok || runner.Status != "active" {
			return problem("E_UNAUTHENTICATED")
		}
		j := state.Jobs[a.JobID]
		if a.Fence != req.Fence || j.Fence != req.Fence {
			return problem("E_LEASE_LOST")
		}
		if a.State == "completed" {
			if a.ResultDigest != req.ResultDigest {
				return problem("E_RESULT_CONFLICT")
			}
			reply = a.Reply
			reply.Replayed = true
			return nil
		}
		if !attemptActive(a.State) {
			return problem("E_LEASE_LOST")
		}
		if a.State == "granted" {
			return problem("E_NOT_STARTED")
		}
		if now > a.LostAt {
			return problem("E_LEASE_LOST")
		}
		if req.Outcome != "passed" && req.Outcome != "failed" && req.Outcome != "errored" {
			return problem("E_INVALID")
		}
		if req.Outcome == "failed" && req.Class != "script_failure" {
			return problem("E_INVALID")
		}
		if req.Outcome == "errored" && !slices.Contains([]string{"infra", "lost", "timeout", "cancelled"}, req.Class) {
			return problem("E_INVALID")
		}
		digest, e := FinishDigest(req)
		if e != nil {
			return e
		}
		if digest != req.ResultDigest {
			return problem("E_DIGEST_MISMATCH")
		}
		if req.LogSeq != a.LogSeq || req.LogEnd != int64(len(a.Log)) {
			return problem("E_LOG_INCOMPLETE")
		}
		if Digest(a.Log) != "sha256:"+strings.TrimPrefix(req.LogDigest, "sha256:") {
			return problem("E_DIGEST_MISMATCH")
		}
		for _, artifact := range req.Artifacts {
			blob, ok := state.Blobs[artifact.Digest]
			if !ok || int64(len(blob)) != artifact.Size {
				return problem("E_BLOB_MISSING")
			}
		}
		j.State = req.Outcome
		for _, name := range j.Spec.Artifacts {
			if !slices.ContainsFunc(req.Artifacts, func(a Artifact) bool { return a.Name == name }) && j.State == "passed" {
				j.State = "failed"
				req.Class = "script_failure"
			}
		}
		if req.Outcome == "errored" {
			retryJob(&j, req.Class, now)
		} else if req.Outcome == "failed" && slices.Contains(j.Spec.Retry.On, req.Class) {
			retryJob(&j, req.Class, now)
		}
		a.State = "completed"
		a.CompletedAt = now
		a.ResultDigest = req.ResultDigest
		state.Jobs[j.ID] = j
		state.Attempts[id] = a
		state.Artifacts[j.ID] = slices.Clone(req.Artifacts)
		if j.State == "passed" && j.Spec.Pure && state.Runs[j.RunID].Trust == "trusted" {
			if _, exists := state.Cache[j.ActionDigest]; !exists {
				state.Cache[j.ActionDigest] = Cache{Producer: id, Artifacts: req.Artifacts}
			}
		}
		if j.State == "passed" || j.State == "failed" {
			if err := addVerdict(state, j, now); err != nil {
				return err
			}
		}
		if err := advanceJobs(state, j.RunID, now); err != nil {
			return err
		}
		reply = FinishReply{Attempt: "completed", Job: j.State, Run: state.Runs[j.RunID].State}
		a.Reply = reply
		state.Attempts[id] = a
		return nil
	})
	return reply, err
}
func retryJob(j *Job, class string, now int64) {
	if j.Spec.Effects != "exclusive" && j.AttemptsUsed <= j.Spec.Retry.Max && slices.Contains(j.Spec.Retry.On, class) {
		j.State = "queued"
		j.ReadyAt = now + min(int64(600000), int64(30000)<<max(0, j.AttemptsUsed-1))
		return
	}
	switch class {
	case "timeout":
		j.State = "timed_out"
	case "cancelled":
		j.State = "cancelled"
	case "script_failure":
	default:
		j.State = "errored"
	}
}
func (c *Controller) Release(ctx context.Context, id, token string, fence int64, reason string) error {
	if !slices.Contains([]string{"not_started", "runner_unsuitable", "shutdown"}, reason) {
		return problem("E_INVALID")
	}
	return c.mutate(ctx, func(state *ciState, _ *int64, now int64) error {
		a, j, err := getAttempt(state, id, token, fence)
		if err != nil {
			return err
		}
		if a.State != "granted" {
			return problem("E_NOT_STARTED")
		}
		a.State = "released"
		j.State = "queued"
		j.ReadyAt = now
		state.Attempts[id] = a
		state.Jobs[j.ID] = j
		return nil
	})
}
func (c *Controller) clockGuard(state *ciState, now int64) {
	mono := c.mono()
	if !c.baselined {
		c.lastWall = now
		c.lastMono = mono
		c.baselined = true
		return
	}
	wallElapsed := now - c.lastWall
	monoElapsed := (mono - c.lastMono) / int64(time.Millisecond)
	drift := wallElapsed - monoElapsed
	if drift < 0 {
		drift = -drift
	}
	c.lastWall = now
	c.lastMono = mono
	if drift <= 5000 {
		return
	}
	for id, a := range state.Attempts {
		if !attemptActive(a.State) {
			continue
		}
		j := state.Jobs[a.JobID]
		rebaseAttempt(&a, j, now)
		state.Attempts[id] = a
		state.Jobs[j.ID] = j
	}
}
func reap(state *ciState, now int64) error {
	for id, a := range state.Attempts {
		if !attemptActive(a.State) {
			continue
		}
		j := state.Jobs[a.JobID]
		switch {
		case a.State == "granted" && now > a.StartDeadlineAt:
			a.State = "expired_unstarted"
			j.State = "queued"
			j.ReadyAt = now
		case a.State != "granted" && now > a.LostAt:
			a.State = "lost"
			j.Fence = 0
			retryJob(&j, "lost", now)
		case a.State != "granted" && now > a.DeadlineAt+30000:
			a.State = "timed_out"
			j.Fence = 0
			retryJob(&j, "timeout", now)
		case a.State == "started" && now > a.LeaseExpiresAt:
			a.State = "suspect"
		}
		state.Attempts[id] = a
		state.Jobs[j.ID] = j
	}
	for id := range state.Runs {
		if err := advanceJobs(state, id, now); err != nil {
			return err
		}
	}
	return nil
}
func (c *Controller) Reap(ctx context.Context) error {
	return c.mutate(ctx, func(state *ciState, _ *int64, now int64) error { c.clockGuard(state, now); return reap(state, now) })
}
func singleFlightHolder(state *ciState, j Job) bool {
	if !j.Spec.Pure || j.ActionDigest == "" {
		return false
	}
	for _, a := range state.Attempts {
		if !attemptActive(a.State) || a.JobID == j.ID {
			continue
		}
		other := state.Jobs[a.JobID]
		if other.RunID == j.RunID {
			continue
		}
		if state.Runs[other.RunID].Trust != "trusted" {
			continue
		}
		if other.ActionDigest == j.ActionDigest {
			return true
		}
	}
	return false
}
func advanceJobs(state *ciState, runID string, now int64) error {
	run := state.Runs[runID]
	for changed := true; changed; {
		changed = false
		for _, id := range run.Jobs {
			j := state.Jobs[id]
			if j.State != "waiting" && j.State != "queued" {
				continue
			}
			ready := true
			for _, name := range j.Spec.Needs {
				for _, needID := range run.Jobs {
					need := state.Jobs[needID]
					if need.Spec.Name != name {
						continue
					}
					if !jobTerminal(need.State) {
						ready = false
					}
					if jobTerminal(need.State) && !jobSuccess(need.State) {
						j.State = "skipped"
						if need.State == "cancelled" {
							j.State = "cancelled"
						}
					}
				}
			}
			if j.State == "waiting" && ready {
				j.State = "queued"
				j.ReadyAt = now
				changed = true
			}
			if j.State == "queued" && j.Spec.Pure && j.ActionDigest == "" {
				key := struct {
					Schema string
					Tree   string
					Spec   string
					Image  string
					ABI    string
					Repo   int64
				}{Schema: "rukie.ci.action/v1", Tree: run.Tree, Spec: j.SpecDigest, Image: j.Spec.Image, ABI: "podman-rootless/1", Repo: run.RepoID}
				data, err := CanonicalJSON(key)
				if err != nil {
					return err
				}
				j.ActionDigest = Digest(data)
			}
			state.Jobs[id] = j
		}
	}
	terminal := true
	result := "succeeded"
	for _, id := range run.Jobs {
		j := state.Jobs[id]
		terminal = terminal && jobTerminal(j.State)
		if j.State == "failed" && result == "succeeded" {
			result = "failed"
		}
		if j.State == "errored" || j.State == "timed_out" {
			result = "errored"
		}
	}
	if terminal {
		if run.CancelReason != "" {
			result = "cancelled"
		}
		run.State = result
		run.FinishedAt = now
	}
	state.Runs[runID] = run
	return nil
}
func addVerdict(state *ciState, j Job, now int64) error {
	run := state.Runs[j.RunID]
	kind := "ci/" + j.Spec.Name + "/" + strings.TrimPrefix(j.SpecDigest, "sha256:")
	chain := run.Subject + "|" + kind
	if run.Trust == "untrusted" && state.ChainTrust[chain] == "trusted" {
		return nil
	}
	result := "pass"
	if j.State == "failed" {
		result = "fail"
	}
	evidenceBytes, err := CanonicalJSON(struct {
		Artifacts []Artifact
		Run       Run
		Job       Job
	}{Run: run, Job: j, Artifacts: state.Artifacts[j.ID]})
	if err != nil {
		return err
	}
	evidence := Digest(evidenceBytes)
	state.Blobs[evidence] = evidenceBytes
	bareEvidence := strings.TrimPrefix(evidence, "sha256:")
	statement := Statement{Subject: run.Subject, Kind: kind, Result: result, SignedAt: time.UnixMilli(now).UTC().Truncate(time.Second).Format(time.RFC3339), Evidence: &bareEvidence}
	if prior := state.Chains[chain]; prior != "" {
		statement.Supersedes = &prior
	}
	payload, err := CanonicalPayload(statement)
	if err != nil {
		return err
	}
	digest := strings.TrimPrefix(Digest(payload), "sha256:")
	id, err := randomID("")
	if err != nil {
		return err
	}
	id = id[:8] + "-" + id[8:12] + "-4" + id[13:16] + "-a" + id[17:20] + "-" + id[20:]
	state.Sequence++
	state.Outbox = append(state.Outbox, Outbox{ID: id, Seq: state.Sequence, RepoID: run.RepoID, Chain: chain, Statement: statement, Payload: payload, Digest: digest, State: "pending"})
	state.Chains[chain] = digest
	state.ChainTrust[chain] = run.Trust
	return nil
}
func cancelRun(state *ciState, id, reason string, now int64) {
	run := state.Runs[id]
	if runTerminal(run.State) {
		return
	}
	run.CancelReason = reason
	run.State = "cancelling"
	state.Runs[id] = run
	for _, jobID := range run.Jobs {
		j := state.Jobs[jobID]
		if j.State == "queued" || j.State == "waiting" {
			j.State = "cancelled"
		}
		if j.CurrentAttempt != "" {
			a := state.Attempts[j.CurrentAttempt]
			if a.State == "granted" {
				a.State = "released"
				j.State = "cancelled"
			} else if attemptActive(a.State) && j.Spec.Interruptible {
				a.CancelReason = reason
				j.State = "cancelling"
			}
			state.Attempts[a.ID] = a
		}
		state.Jobs[jobID] = j
	}
	_ = advanceJobs(state, id, now)
}
func (c *Controller) Cancel(ctx context.Context, id, reason string) error {
	return c.mutate(ctx, func(state *ciState, _ *int64, now int64) error {
		if _, ok := state.Runs[id]; !ok {
			return problem("E_NOT_FOUND")
		}
		cancelRun(state, id, reason, now)
		return nil
	})
}
func (c *Controller) Runs(ctx context.Context, repoID int64) ([]Run, error) {
	state, err := c.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	runs := make([]Run, 0)
	for _, run := range state.Runs {
		if repoID == 0 || run.RepoID == repoID {
			runs = append(runs, run)
		}
	}
	slices.SortFunc(runs, func(a, b Run) int { return strings.Compare(a.ID, b.ID) })
	return runs, nil
}
func (c *Controller) Run(ctx context.Context, id string) (Run, []Job, error) {
	state, err := c.snapshot(ctx)
	if err != nil {
		return Run{}, nil, err
	}
	run, ok := state.Runs[id]
	if !ok {
		return Run{}, nil, problem("E_NOT_FOUND")
	}
	jobs := make([]Job, 0, len(run.Jobs))
	for _, id := range run.Jobs {
		jobs = append(jobs, state.Jobs[id])
	}
	return run, jobs, nil
}
func (c *Controller) Runners(ctx context.Context) ([]Runner, error) {
	state, err := c.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Runner, 0, len(state.Runners))
	for _, runner := range state.Runners {
		runner.TokenHash = ""
		out = append(out, runner)
	}
	return out, nil
}
func (c *Controller) PendingOutbox(ctx context.Context) ([]Outbox, error) {
	state, err := c.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Outbox, 0)
	blocked := map[string]bool{}
	for _, row := range state.Outbox {
		if row.State == "delivered" {
			continue
		}
		if !blocked[row.Chain] && row.State == "pending" && row.NextAt <= c.now().UnixMilli() {
			out = append(out, row)
		}
		blocked[row.Chain] = true
	}
	return out, nil
}
func (c *Controller) MarkOutboxDelivered(ctx context.Context, id, digest string) error {
	return c.mutate(ctx, func(state *ciState, _ *int64, _ int64) error {
		for i := range state.Outbox {
			row := &state.Outbox[i]
			if row.ID != id {
				continue
			}
			if row.Digest != digest {
				return problem("E_CORRUPT")
			}
			row.State = "delivered"
			return nil
		}
		return problem("E_NOT_FOUND")
	})
}
func (c *Controller) MarkOutboxFailed(ctx context.Context, id string, cause error) error {
	return c.mutate(ctx, func(state *ciState, _ *int64, now int64) error {
		for i := range state.Outbox {
			row := &state.Outbox[i]
			if row.ID != id {
				continue
			}
			row.Attempts++
			row.LastError = cause.Error()
			row.NextAt = now + min(int64(300000), int64(1000)<<min(9, row.Attempts-1))
			if p, ok := errors.AsType[*Problem](cause); ok && slices.Contains([]string{"E_FORBIDDEN", "E_POLICY", "E_OBJECT_FORMAT"}, p.Code) {
				row.State = "dead"
			}
			if p, ok := errors.AsType[*Problem](cause); ok && p.Code == "E_INVALID" && row.Attempts >= 16 {
				row.State = "dead"
			}
			return nil
		}
		return problem("E_NOT_FOUND")
	})
}
func ProofKind(jobName, specDigest string) string {
	return "ci/" + jobName + "/" + strings.TrimPrefix(specDigest, "sha256:")
}
func RequiredProofs(spec Spec) ([]string, error) {
	if err := ValidateSpec(spec); err != nil {
		return nil, err
	}
	required := map[string]bool{}
	for _, name := range spec.Land.Required {
		required[name] = true
	}
	out := make([]string, 0, len(spec.Land.Required))
	for _, job := range spec.Jobs {
		if !required[job.Name] {
			continue
		}
		digest, err := JobSpecDigest(job)
		if err != nil {
			return nil, err
		}
		out = append(out, ProofKind(job.Name, digest))
	}
	slices.Sort(out)
	return out, nil
}
func (c *Controller) RelayOnce(ctx context.Context) (int, error) {
	if c.deliver == nil {
		return 0, problem("E_FORBIDDEN")
	}
	if len(c.signKey) != ed25519.PrivateKeySize {
		return 0, problem("E_FORBIDDEN")
	}
	rows, err := c.PendingOutbox(ctx)
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, row := range rows {
		signature, err := SignSSHSIG(row.Payload, c.signKey)
		if err != nil {
			return delivered, err
		}
		if _, err := c.deliver(ctx, row, signature); err != nil {
			if ferr := c.MarkOutboxFailed(ctx, row.ID, err); ferr != nil {
				return delivered, ferr
			}
			continue
		}
		if err := c.MarkOutboxDelivered(ctx, row.ID, row.Digest); err != nil {
			return delivered, err
		}
		delivered++
	}
	return delivered, nil
}
func (c *Controller) Artifacts(ctx context.Context, jobID string) ([]Artifact, error) {
	state, err := c.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return slices.Clone(state.Artifacts[jobID]), nil
}

// RepoBlob returns the blob only when the digest belongs to an artifact of
// a job in the given repo. Direct-digest fetches of other repos' blobs
// answer not-found, closing the cross-repo read and existence oracle.
func (c *Controller) RepoBlob(ctx context.Context, repoID int64, digest string) ([]byte, bool) {
	state, err := c.snapshot(ctx)
	if err != nil {
		return nil, false
	}
	for jobID, artifacts := range state.Artifacts {
		job, ok := state.Jobs[jobID]
		if !ok {
			continue
		}
		run, ok := state.Runs[job.RunID]
		if !ok || run.RepoID != repoID {
			continue
		}
		for _, artifact := range artifacts {
			if artifact.Digest != digest {
				continue
			}
			blob, ok := state.Blobs[digest]
			if !ok {
				return nil, false
			}
			return slices.Clone(blob), true
		}
	}
	return nil, false
}

func (c *Controller) RelayLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := c.RelayOnce(ctx); err != nil {
				continue
			}
		}
	}
}

func (c *Controller) GetAttempt(ctx context.Context, id string) (Attempt, Job, Run, error) {
	state, err := c.snapshot(ctx)
	if err != nil {
		return Attempt{}, Job{}, Run{}, err
	}
	a, exists := state.Attempts[id]
	if !exists {
		return Attempt{}, Job{}, Run{}, problem("E_NOT_FOUND")
	}
	j, exists := state.Jobs[a.JobID]
	if !exists {
		return Attempt{}, Job{}, Run{}, problem("E_NOT_FOUND")
	}
	run, exists := state.Runs[j.RunID]
	if !exists {
		return Attempt{}, Job{}, Run{}, problem("E_NOT_FOUND")
	}
	return a, j, run, nil
}
func (c *Controller) RetryRun(ctx context.Context, runID string, jobs []string, acknowledge bool) (Run, error) {
	id, err := randomID("run_")
	if err != nil {
		return Run{}, err
	}
	var out Run
	only := map[string]bool{}
	for _, name := range jobs {
		only[name] = true
	}
	err = c.mutate(ctx, func(state *ciState, _ *int64, now int64) error {
		prior, exists := state.Runs[runID]
		if !exists {
			return problem("E_NOT_FOUND")
		}
		if !runTerminal(prior.State) {
			return problem("E_CONFLICT")
		}
		out = Run{ID: id, RepoID: prior.RepoID, Owner: prior.Owner, Repo: prior.Repo, Principal: prior.Principal, Subject: prior.Subject, Tree: prior.Tree, BaseCommit: prior.BaseCommit, SpecFrom: prior.SpecFrom, Trust: prior.Trust, TriggerKey: prior.TriggerKey + ":retry:" + id, Class: prior.Class, State: "pending", CreatedAt: now, Spec: prior.Spec}
		for _, jobID := range prior.Jobs {
			job := state.Jobs[jobID]
			fresh, ferr := randomID("job_")
			if ferr != nil {
				return ferr
			}
			state.Sequence++
			next := Job{ID: fresh, RunID: id, Spec: job.Spec, SpecDigest: job.SpecDigest, ActionDigest: job.ActionDigest, State: "waiting", QueuedSeq: state.Sequence, ReadyAt: now}
			rerun := only[job.Spec.Name]
			if len(jobs) == 0 {
				rerun = !jobSuccess(job.State)
			}
			if job.State == "passed" || job.State == "cached" {
				if !rerun {
					next.State = "carried"
					state.Artifacts[fresh] = slices.Clone(state.Artifacts[jobID])
				}
			} else if job.Spec.Effects == "exclusive" && !acknowledge && rerun {
				return problem("E_FORBIDDEN")
			}
			state.Jobs[fresh] = next
			out.Jobs = append(out.Jobs, fresh)
		}
		state.Runs[id] = out
		state.Triggers[fmt.Sprintf("%d:%s", out.RepoID, out.TriggerKey)] = id
		return advanceJobs(state, id, now)
	})
	return out, err
}
