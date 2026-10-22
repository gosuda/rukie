package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	jsonv2 "encoding/json/v2"
)

// Container is one running job container.
type Container interface {
	Wait() int
	Kill() error
	Name() string
}

// ContainerExec starts job containers.
type ContainerExec interface {
	Start(args []string, timeout time.Duration) (Container, error)
}

// PodmanExec runs real containers.
type PodmanExec struct {
	Podman string
}

type podmanContainer struct {
	done   chan int
	podman string
	name   string
}

func containerName(args []string) string {
	for i, arg := range args {
		if arg == "--name" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// Start launches podman run in the background and reports its exit code.
func (p PodmanExec) Start(args []string, timeout time.Duration) (Container, error) {
	name := containerName(args)
	if name == "" {
		return nil, fmt.Errorf("PodmanExec.Start: container has no name")
	}
	c := &podmanContainer{podman: p.Podman, name: name, done: make(chan int, 1)}
	go func() {
		_, stderr, err := RunPodman(args, timeout)
		if err == nil {
			c.done <- 0
			return
		}
		if exit, ok := err.(interface{ ExitCode() int }); ok {
			c.done <- exit.ExitCode()
			return
		}
		_ = stderr
		c.done <- 125
	}()
	return c, nil
}

// Wait blocks for the container exit code.
func (c *podmanContainer) Wait() int {
	return <-c.done
}

// Name returns the container name.
func (c *podmanContainer) Name() string {
	return c.name
}

// Kill stops the container with SIGTERM and SIGKILL fallback.
func (c *podmanContainer) Kill() error {
	stopOut, stopErr := runBounded(c.podman, nil, 30*time.Second, "stop", "-t", "15", c.name)
	if stopErr == nil {
		return nil
	}
	killOut, killErr := runBounded(c.podman, nil, 30*time.Second, "kill", c.name)
	if killErr != nil {
		return fmt.Errorf("podmanContainer.Kill: stop %s kill %s: %w", strings.TrimSpace(string(stopOut)), strings.TrimSpace(string(killOut)), killErr)
	}
	return nil
}

// Loop owns one runner process lifetime.
type Loop struct {
	Config Config
	Stdout *os.File
	Stderr *os.File
	Exec   ContainerExec
	Client *Client
	Now    func() time.Time
	Mono   func() int64
	Tick   time.Duration
	Once   bool
}

func (l *Loop) mono() int64 {
	if l.Mono != nil {
		return l.Mono()
	}
	return time.Now().UnixNano()
}

// AttemptWork is the prepared attempt directory.
type AttemptWork struct {
	Dir     string
	Work    string
	Log     string
	Grant   *Grant
	EnvFile string
}

// PrepareAttempt materializes the exact tree and writes attempt files.
func PrepareAttempt(cfg Config, grant *Grant) (AttemptWork, error) {
	work := AttemptWork{Grant: grant}
	work.Dir = filepath.Join(cfg.StateDir, "attempts", grant.Attempt.ID)
	work.Work = filepath.Join(work.Dir, "work")
	work.Log = filepath.Join(work.Dir, "log")
	work.EnvFile = filepath.Join(work.Dir, "attempt.env")
	for _, dir := range []string{work.Work, work.Log} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return AttemptWork{}, fmt.Errorf("PrepareAttempt: %w", err)
		}
	}
	key := repoKeyOf(grant)
	mirror := MirrorPath(cfg.StateDir, key)
	tokenEnv := append(os.Environ(), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=AUTHORIZATION: Bearer "+grant.Attempt.Token)
	if err := UpdateMirror(mirror, fetchURL(cfg, grant), grant.Fetch.Want, tokenEnv); err != nil {
		return AttemptWork{}, err
	}
	if err := VerifyTree(mirror, grant.Fetch.Want, grant.Fetch.ExpectTree); err != nil {
		return AttemptWork{}, err
	}
	if err := Materialize(mirror, grant.Fetch.ExpectTree, work.Work); err != nil {
		return AttemptWork{}, err
	}
	envLines := []string{}
	for name, value := range grant.Job.Env {
		envLines = append(envLines, name+"="+value)
	}
	envLines = append(envLines,
		"RUKIE_SPEC_DIGEST="+grant.Job.SpecDigest,
		"RUKIE_ATTEMPT="+grant.Attempt.ID,
		"RUKIE_FENCE="+strconv.FormatInt(grant.Attempt.Fence, 10),
	)
	if err := os.WriteFile(work.EnvFile, []byte(strings.Join(envLines, "\n")+"\n"), 0600); err != nil {
		return AttemptWork{}, fmt.Errorf("PrepareAttempt: %w", err)
	}
	steps := ExecSpec{Env: grant.Job.Env}
	for _, step := range grant.Job.Steps {
		steps.Steps = append(steps.Steps, ExecStep(step))
	}
	raw, err := jsonv2.Marshal(steps)
	if err != nil {
		return AttemptWork{}, fmt.Errorf("PrepareAttempt: %w", err)
	}
	stepsDir := filepath.Join(work.Dir, ".rukie")
	if err := os.MkdirAll(stepsDir, 0700); err != nil {
		return AttemptWork{}, fmt.Errorf("PrepareAttempt: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stepsDir, "steps.json"), raw, 0600); err != nil {
		return AttemptWork{}, fmt.Errorf("PrepareAttempt: %w", err)
	}
	if err := linkOrCopyBinary(stepsDir); err != nil {
		return AttemptWork{}, err
	}
	return work, nil
}

func slugPath(part string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, part)
}

func repoKeyOf(grant *Grant) string {
	trimmed := strings.TrimSuffix(grant.Fetch.URL, ".git")
	trimmed = strings.TrimPrefix(trimmed, "/")
	parts := strings.Split(trimmed, "/")
	for i, part := range parts {
		parts[i] = slugPath(part)
	}
	return strings.Join(parts, "-")
}

func fetchURL(cfg Config, grant *Grant) string {
	return strings.TrimSuffix(cfg.Server, "/") + grant.Fetch.URL
}

// MapVerdict converts an executor exit into a finish outcome.
func MapVerdict(exit int, cancelled bool) (string, string) {
	if cancelled {
		return "errored", "cancelled"
	}
	switch exit {
	case 0:
		return "passed", ""
	case 3:
		return "errored", "timeout"
	case 4:
		return "errored", "infra"
	default:
		return "failed", "script_failure"
	}
}

// LastAck tracks heartbeat acknowledgements on the monotonic clock.
type LastAck struct {
	At    int64
	Limit int64
}

// Stale reports a self-fence breach.
func (l LastAck) Stale(now int64) bool {
	return now-l.At > l.Limit
}

// SandboxArgs renders the job container command.
func SandboxArgs(cfg Config, podman string, grant *Grant, work AttemptWork) []string {
	caches := []CacheMount{}
	return BuildPodmanArgs(podman, SandboxRun{
		Name:      "rukie-" + grant.Attempt.ID,
		Image:     grant.Job.Image["ref"],
		UID:       os.Getuid(),
		GID:       os.Getgid(),
		CPUs:      cfg.CPUs,
		MemoryMB:  cfg.MemoryBytes >> 20,
		PidsLimit: 512,
		TmpMB:     512,
		Network:   grant.Job.Network,
		WorkHost:  work.Work,
		LogHost:   work.Log,
		EnvFile:   work.EnvFile,
		Caches:    caches,
		RukieDir:  filepath.Join(work.Dir, ".rukie"),
	})
}

// RunLoop pulls leases until the context ends.
func (l *Loop) RunLoop(ctx context.Context) int {
	abandoned := false
	for {
		select {
		case <-ctx.Done():
			return abandonedCode(abandoned)
		default:
		}
		grant, err := l.Client.Lease(LeaseWaitMaxS)
		if err != nil {
			select {
			case <-ctx.Done():
				return abandonedCode(abandoned)
			case <-time.After(backoffWithJitter(time.Second, 0)):
			}
			continue
		}
		if grant == nil {
			if l.Once {
				return abandonedCode(abandoned)
			}
			continue
		}
		if l.execute(ctx, grant) == ExecuteAbandoned {
			abandoned = true
		}
		if l.Once {
			return abandonedCode(abandoned)
		}
	}
}

// ExecuteOutcome is the attempt execution result.
type ExecuteOutcome int

const (
	ExecuteDone ExecuteOutcome = iota
	ExecuteAbandoned
)

func abandonedCode(abandoned bool) int {
	if abandoned {
		return ExitDrainAbandoned
	}
	return ExitOK
}

// Execute runs one granted attempt to a finish or an abandon.
func (l *Loop) execute(ctx context.Context, grant *Grant) ExecuteOutcome {
	work, err := PrepareAttempt(l.Config, grant)
	if err != nil {
		return ExecuteAbandoned
	}
	timeout := time.Duration(grant.Job.TimeoutS) * time.Second
	container, err := l.Exec.Start(SandboxArgs(l.Config, l.Config.PodmanPath, grant, work), timeout+2*time.Minute)
	if err != nil {
		return ExecuteAbandoned
	}
	ack := LastAck{At: l.mono(), Limit: SelfFenceS * int64(time.Second)}
	done := make(chan int, 1)
	go func() { done <- container.Wait() }()
	tick := l.Tick
	if tick <= 0 {
		tick = HeartbeatIntervalS * time.Second
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	stream := &logStreamer{client: l.Client, attempt: grant.Attempt, logDir: work.Log, seq: -1}
	cancelled := false
	for {
		select {
		case <-ctx.Done():
			_ = container.Kill()
			<-done
			return ExecuteAbandoned
		case exit := <-done:
			ticker.Stop()
			outcome, class := MapVerdict(exit, cancelled)
			l.finish(ctx, AttemptDone{Work: work, Grant: grant, Stream: stream, Outcome: outcome, Class: class, Exit: exit})
			return ExecuteDone
		case <-ticker.C:
			reply, herr := l.Client.Heartbeat(grant.Attempt)
			if herr != nil {
				if _, ok := errors.AsType[*LeaseLostError](herr); ok {
					_ = container.Kill()
					<-done
					return ExecuteAbandoned
				}
				if ack.Stale(l.mono()) {
					_ = container.Kill()
					<-done
					return ExecuteAbandoned
				}
				break
			}
			ack.At = l.mono()
			if reply.Cancel != "" {
				cancelled = true
				_ = container.Kill()
			}
			stream.ship()
		}
	}
}

// AttemptDone carries the completed attempt and its verdict.
type AttemptDone struct {
	Work    AttemptWork
	Grant   *Grant
	Stream  *logStreamer
	Outcome string
	Class   string
	Exit    int
}

func (l *Loop) finish(ctx context.Context, done AttemptDone) {
	done.Stream.ship()
	refs := []ArtifactRef{}
	for _, name := range done.Grant.Job.Artifacts {
		digest, raw, err := CollectArtifact(done.Work.Work, name)
		if err != nil {
			continue
		}
		if err := l.Client.PutArtifact(done.Grant.Attempt, raw); err != nil {
			continue
		}
		refs = append(refs, ArtifactRef{Name: name, Digest: digest, Size: int64(len(raw))})
	}
	in := FinishInput{
		Outcome: done.Outcome, Class: done.Class, ExitCode: done.Exit, Fence: done.Grant.Attempt.Fence,
		LogSHA: done.Stream.digest(), LogSeq: done.Stream.seq, LogEnd: done.Stream.end, Artifacts: refs,
	}
	raw, err := jsonv2.Marshal(in)
	if err == nil {
		_ = os.WriteFile(filepath.Join(done.Work.Dir, "result.json"), raw, 0600)
	}
	for attempt := 0; ; attempt++ {
		_, ferr := l.Client.Finish(done.Grant.Attempt, in)
		if ferr == nil {
			return
		}
		if _, ok := errors.AsType[*LeaseLostError](ferr); ok {
			return
		}
		if strings.Contains(ferr.Error(), "E_RESULT_CONFLICT") {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoffWithJitter(time.Second, attempt)):
		}
	}
}

type logStreamer struct {
	sum     []byte
	attempt AttemptRef
	logDir  string
	client  *Client
	offsets map[string]int64
	seq     int64
	end     int64
}

func (s *logStreamer) ship() {
	if s.offsets == nil {
		s.offsets = map[string]int64{}
	}
	for _, stream := range []string{"job.stdout", "job.stderr"} {
		raw, err := OpenLogChunk(filepath.Join(s.logDir, stream))
		if err != nil {
			continue
		}
		at := s.offsets[stream]
		if int64(len(raw)) <= at {
			continue
		}
		chunk := raw[at:]
		for len(chunk) > 0 {
			take := min(len(chunk), ChunkMax)
			_, _ = s.client.AppendLog(s.attempt, s.seq+1, s.end, chunk[:take], nil)
			s.seq++
			s.end += int64(take)
			s.sum = append(s.sum, chunk[:take]...)
			chunk = chunk[take:]
		}
		s.offsets[stream] = int64(len(raw))
	}
}

func (s *logStreamer) digest() string {
	return Digest(s.sum)
}
