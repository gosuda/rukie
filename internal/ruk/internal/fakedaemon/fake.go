package fakedaemon

import (
	"encoding/binary"
	"errors"
	"math"
	"net"
	"sync"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
)

const mathMaxUint32 = math.MaxUint32

var errFrameTooBig = errors.New("fakedaemon: frame too big")

const (
	frameBytes     = 13
	headerBytes    = 9
	maxDataPayload = 64 << 10
	initialWindow  = 256 << 10
)

type kind uint8

const (
	kindOpen         kind = 1
	kindAccepted     kind = 2
	kindStdout       kind = 5
	kindWindowUpdate kind = 7
	kindCancel       kind = 8
	kindExit         kind = 10
	kindError        kind = 11
	kindHookCall     kind = 12
)

type frame struct {
	payload []byte
	stream  uint64
	kind    kind
}

func writeFrame(w net.Conn, mu *sync.Mutex, k kind, stream uint64, payload []byte) error {
	var hdr [frameBytes]byte
	total := int64(headerBytes) + int64(len(payload))
	if total < 0 || total > mathMaxUint32 {
		return errFrameTooBig
	}
	binary.BigEndian.PutUint32(hdr[0:4], uint32(total))
	hdr[4] = byte(k)
	binary.BigEndian.PutUint64(hdr[5:13], stream)
	mu.Lock()
	defer mu.Unlock()
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

func readFrame(r net.Conn) (frame, error) {
	var hdr [frameBytes]byte
	if _, err := readFull(r, hdr[:]); err != nil {
		return frame{}, err
	}
	total := binary.BigEndian.Uint32(hdr[0:4])
	payload := make([]byte, total-headerBytes)
	if _, err := readFull(r, payload); err != nil {
		return frame{}, err
	}
	stream := binary.BigEndian.Uint64(hdr[5:13])
	return frame{kind: kind(hdr[4]), stream: stream, payload: payload}, nil
}

func readFull(c net.Conn, buf []byte) (int, error) {
	done := 0
	for done < len(buf) {
		n, err := c.Read(buf[done:])
		done += n
		if err != nil {
			return done, err
		}
	}
	return done, nil
}

// Config tunes one fake.
type Config struct {
	ValidRepo         func(repo string) (handle string, code string)
	Refuse            map[string]string
	RefuseAfterAccept map[string]string
	Directions        *[]string
	Doc               jsonv1.RawMessage
	StreamDelay       time.Duration
	Serial            bool
	HoldOpens         bool
	RequireAuth       bool
	RefuseOnce        bool
	InjectHook        bool
}

// Server speaks the daemon wire on one socket.
type Server struct {
	listener *net.UnixListener
	cfg      Config
	wg       sync.WaitGroup
	mu       sync.Mutex
}

// Listen starts one fake on a unix socket.
func Listen(socketPath string, cfg Config) (*Server, error) {
	if cfg.ValidRepo == nil {
		cfg.ValidRepo = func(repo string) (string, string) {
			return "handle-" + repo, ""
		}
	}
	if cfg.Doc == nil {
		cfg.Doc = jsonv1.RawMessage(`{"schema_version":{"major":1,"minor":1}}`)
	}
	addr, err := net.ResolveUnixAddr("unix", socketPath)
	if err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, listener: listener}
	s.wg.Go(s.serve)
	return s, nil
}

// Close stops the fake.
func (s *Server) Close() {
	_ = s.listener.Close()
	s.wg.Wait()
}

func (s *Server) serve() {
	for {
		nc, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		s.wg.Go(func() {
			s.serveConn(nc)
		})
	}
}

type invocationOpen struct {
	RequestID *string `json:"request_id"`
	ArgsJSON  *string `json:"args_json"`
	SessionID *string `json:"session_id"`
	Repo      string  `json:"repo"`
	Method    string  `json:"method"`
}

type connection struct {
	nc     *net.UnixConn
	closed chan struct{}
	credit map[uint64]uint64
	signal chan struct{}
	wmu    sync.Mutex
	mu     sync.Mutex
}

func (s *Server) serveConn(nc *net.UnixConn) {
	defer func() {
		_ = nc.Close()
	}()
	hello, err := readFrame(nc)
	if err != nil {
		return
	}
	if hello.kind != kindOpen || hello.stream != 0 {
		return
	}
	accept, _ := jsonv2.Marshal(map[string]any{
		"protocol_major": 1,
		"protocol_minor": 1,
		"profile":        "fake",
		"object_formats": []string{"sha1"},
		"features":       []string{"credits", "hooks.v1", "events.v1", "archive.v1"},
	})
	var wmu sync.Mutex
	if writeFrame(nc, &wmu, kindAccepted, 0, accept) != nil {
		return
	}
	c := &connection{nc: nc, closed: make(chan struct{}), credit: make(map[uint64]uint64), signal: make(chan struct{}, 1)}
	defer close(c.closed)
	if !s.cfg.Serial {
		for {
			fr, err := readFrame(nc)
			if err != nil {
				return
			}
			switch fr.kind {
			case kindOpen:
				if !s.cfg.HoldOpens {
					go s.runStream(c, fr)
				}
			case kindWindowUpdate:
				s.grant(c, fr)
			case kindCancel, kindAccepted, kindStdout, kindExit, kindError, kindHookCall:
			}
		}
	}
	var pending []frame
	for {
		fr, err := readFrame(nc)
		if err != nil {
			return
		}
		switch fr.kind {
		case kindOpen:
			if s.cfg.HoldOpens {
				pending = append(pending, fr)
			} else {
				s.runStream(c, fr)
				for _, queued := range pending {
					s.runStream(c, queued)
				}
				pending = nil
			}
		case kindCancel:
			pending = dropStream(pending, fr.stream)
		case kindWindowUpdate:
			s.grant(c, fr)
		case kindAccepted, kindStdout, kindExit, kindError, kindHookCall:
		}
	}
}

func dropStream(pending []frame, stream uint64) []frame {
	kept := make([]frame, 0, len(pending))
	for _, fr := range pending {
		if fr.stream != stream {
			kept = append(kept, fr)
		}
	}
	return kept
}

func (s *Server) grant(c *connection, fr frame) {
	var credit map[string]jsonv1.RawMessage
	if err := jsonv2.Unmarshal(fr.payload, &credit, jsonv1.DefaultOptionsV1()); err != nil {
		return
	}
	var dir string
	if raw, ok := credit["direction"]; ok {
		_ = jsonv2.Unmarshal(raw, &dir, jsonv1.DefaultOptionsV1())
	}
	var CreditBytes uint64
	if raw, ok := credit["credit_bytes"]; ok {
		_ = jsonv2.Unmarshal(raw, &CreditBytes, jsonv1.DefaultOptionsV1())
	}
	if s.cfg.Directions != nil {
		s.mu.Lock()
		*s.cfg.Directions = append(*s.cfg.Directions, dir)
		s.mu.Unlock()
	}
	c.mu.Lock()
	c.credit[fr.stream] += CreditBytes
	c.mu.Unlock()
	select {
	case c.signal <- struct{}{}:
	default:
	}
}

// SnapshotDirections copies the recorded grant directions.
func (s *Server) SnapshotDirections() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Directions == nil {
		return nil
	}
	out := make([]string, len(*s.cfg.Directions))
	copy(out, *s.cfg.Directions)
	return out
}

func (s *Server) runStream(c *connection, open frame) {
	var invocation invocationOpen
	if err := jsonv2.Unmarshal(open.payload, &invocation, jsonv1.DefaultOptionsV1()); err != nil {
		return
	}
	if code, ok := s.cfg.Refuse[invocation.Method]; ok {
		s.refuse(c, open.stream, code, "method refused")
		return
	}
	switch invocation.Method {
	case "schema", "session.open", "session.resume":
	default:
		if s.cfg.RequireAuth && invocation.SessionID == nil {
			s.refuse(c, open.stream, "E_INVALID", "method needs a session")
			return
		}
	}
	handle, code := s.cfg.ValidRepo(invocation.Repo)
	if code != "" {
		s.refuse(c, open.stream, code, "repository refused")
		return
	}
	accepted, _ := jsonv2.Marshal(map[string]any{
		"stream_id":   open.stream,
		"method":      invocation.Method,
		"repo_handle": handle,
	})
	if writeFrame(c.nc, &c.wmu, kindAccepted, open.stream, accepted) != nil {
		return
	}
	if s.cfg.StreamDelay > 0 {
		time.Sleep(s.cfg.StreamDelay)
	}
	if code, ok := s.cfg.RefuseAfterAccept[invocation.Method]; ok {
		if s.cfg.RefuseOnce {
			delete(s.cfg.RefuseAfterAccept, invocation.Method)
		}
		s.refuse(c, open.stream, code, "refused after accept")
		return
	}
	if s.cfg.InjectHook {
		hook, _ := jsonv2.Marshal(map[string]any{"hook": "pre-receive"})
		_ = writeFrame(c.nc, &c.wmu, kindHookCall, open.stream, hook)
	}
	body := s.answer(invocation)
	window := uint64(initialWindow)
	for offset := 0; offset < len(body); {
		end := offset + maxDataPayload
		if end > len(body) {
			end = len(body)
		}
		chunk := body[offset:end]
		width := uint64(len(chunk))
		for window < width {
			c.mu.Lock()
			got := c.credit[open.stream]
			c.credit[open.stream] = 0
			c.mu.Unlock()
			window += got
			if window >= width {
				break
			}
			select {
			case <-c.signal:
			case <-c.closed:
				return
			}
		}
		window -= width
		if writeFrame(c.nc, &c.wmu, kindStdout, open.stream, chunk) != nil {
			return
		}
		offset = end
	}
	exit, _ := jsonv2.Marshal(map[string]any{
		"status":       0,
		"code":         "OK",
		"outcome":      "Completed",
		"completed_at": 1,
	})
	_ = writeFrame(c.nc, &c.wmu, kindExit, open.stream, exit)
}

func (s *Server) answer(invocation invocationOpen) []byte {
	switch invocation.Method {
	case "schema":
		return []byte(s.cfg.Doc)
	case "session.open":
		body, _ := jsonv2.Marshal(map[string]any{
			"session":          "session-test",
			"reconnect_secret": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"principal":        "test",
		})
		return body
	default:
		reply := map[string]any{"echo": true}
		if invocation.Method == "session.resume" {
			reply = map[string]any{
				"session":      "session-test",
				"authority":    map[string]any{"active": map[string]any{"idle_expires_at": "2030-01-01T00:00:00Z"}},
				"capabilities": map[string]any{},
			}
		}
		envelope := map[string]any{
			"schema_version": map[string]any{"major": 1, "minor": 1},
			"body":           map[string]any{"success": map[string]any{invocation.Method: reply}},
		}
		if invocation.RequestID != nil {
			envelope["request_id"] = *invocation.RequestID
		}
		body, _ := jsonv2.Marshal(envelope)
		return body
	}
}

func (s *Server) refuse(c *connection, stream uint64, code, message string) {
	reply, _ := jsonv2.Marshal(map[string]any{
		"code":    code,
		"message": message,
		"hints":   []string{},
		"fixes":   []string{},
	})
	_ = writeFrame(c.nc, &c.wmu, kindError, stream, reply)
	exit, _ := jsonv2.Marshal(map[string]any{
		"status":       1,
		"code":         code,
		"outcome":      "Failed",
		"completed_at": 1,
	})
	_ = writeFrame(c.nc, &c.wmu, kindExit, stream, exit)
}
