package cli

import (
	"bytes"
	"cmp"
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
)

type denyAuthenticator struct{}

func (denyAuthenticator) Bearer(_ context.Context, _ string) (op.Principal, error) {
	return op.Principal{}, op.ErrUnauthenticated
}

func (denyAuthenticator) Session(_ context.Context, _ string) (op.Principal, error) {
	return op.AnonymousPrincipal(), nil
}

func assemble(deps api.Deps, authn op.Authenticator) (*op.Server, error) {
	table := op.NewTable()
	docs := &api.DocStore{}
	if err := api.RegisterAll(table, docs); err != nil {
		return nil, err
	}
	if err := api.RegisterAuth(table, deps); err != nil {
		return nil, err
	}
	if err := api.RegisterUser(table, deps); err != nil {
		return nil, err
	}
	if err := api.RegisterAdmin(table, deps); err != nil {
		return nil, err
	}
	if err := api.RegisterAdminF14(table, deps); err != nil {
		return nil, err
	}
	if err := api.RegisterF12(table, deps); err != nil {
		return nil, err
	}
	if err := api.RegisterRepos(table, deps); err != nil {
		return nil, err
	}
	if err := api.RegisterSearch(table, deps); err != nil {
		return nil, err
	}
	if err := api.RegisterCode(table, deps); err != nil {
		return nil, err
	}
	if err := api.RegisterRefs(table, deps); err != nil {
		return nil, err
	}
	if err := api.RegisterOrgs(table, deps); err != nil {
		return nil, err
	}
	fillLLMS, err := api.RegisterLLMS(table, "")
	if err != nil {
		return nil, err
	}
	views := op.NewViews()
	api.RegisterViews(views)
	api.RegisterAuthViews(views)
	api.RegisterUserViews(views)
	api.RegisterAdminViews(views)
	api.RegisterAdminF14Views(views)
	api.RegisterF12Views(views)
	api.RegisterReposViews(views)
	api.RegisterSearchViews(views)
	api.RegisterCodeViews(views)
	api.RegisterRefsViews(views)
	api.RegisterOrgsViews(views)
	api.RegisterLLMSViews(views)
	mux := http.NewServeMux()
	server, err := op.Mount(mux, table, op.Config{
		Authenticator: authn,
		IsSiteSegment: api.SiteSegment,
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(context.Context, op.AuditRow) error { return nil }),
		Limiter:       deps.Limiter,
		CSRFKey:       make([]byte, 32),
		Views:         views,
		Title:         "rukie",
		Version:       "0.0.0",
	})
	if err != nil {
		return nil, err
	}
	fillLLMS(server.Document())
	return server, nil
}

func buildRemote() (*op.Server, error) {
	deps := api.Deps{Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache()}
	return assemble(deps, denyAuthenticator{})
}

type RemoteInvoker struct {
	Base    *url.URL
	Client  *http.Client
	Jar     http.CookieJar
	Token   string
	Session string
	Retries int
}

func (r *RemoteInvoker) Invoke(ctx context.Context, req *http.Request) (*op.Response, error) {
	rawBody, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	attempts := r.Retries
	if attempts < 1 {
		attempts = 3
	}
	var last *op.Response
	var transportErr error
	for attempt := range attempts {
		out, retry, err := r.once(ctx, req, rawBody)
		switch {
		case err != nil:
			transportErr = err
		case !retry:
			return out, nil
		default:
			last = out
			if attempt+1 < attempts {
				sleepForRetry(out)
			}
		}
	}
	if last != nil {
		return last, nil
	}
	if transportErr == nil {
		transportErr = fmt.Errorf("request failed")
	}
	return nil, transportErr
}

func (r *RemoteInvoker) once(ctx context.Context, req *http.Request, rawBody []byte) (*op.Response, bool, error) {
	cloned := req.Clone(ctx)
	if rawBody != nil {
		cloned.Body = io.NopCloser(bytes.NewReader(rawBody))
		cloned.ContentLength = int64(len(rawBody))
	}
	if r.Token != "" {
		cloned.Header.Set("Authorization", "Bearer "+r.Token)
	} else if r.Session != "" {
		cloned.Header.Set("Cookie", r.Session)
	}
	if r.Jar != nil {
		cloned.Header.Del("Cookie")
		for _, cookie := range r.Jar.Cookies(cloned.URL) {
			cloned.AddCookie(cookie)
		}
	}
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(cloned)
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}
	out := &op.Response{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: body}
	if r.Jar != nil {
		r.Jar.SetCookies(cloned.URL, resp.Cookies())
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		if resp.Header.Get("Retry-After") != "" {
			return out, true, nil
		}
	}
	return out, false, nil
}

func sleepForRetry(resp *op.Response) {
	delay := parseRetryAfter(resp.Header.Get("Retry-After"))
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	if delay > 0 {
		timer := time.NewTimer(delay + retryJitter(delay))
		<-timer.C
	}
}

func retryJitter(delay time.Duration) time.Duration {
	span := delay/4 + 1
	n, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(span)))
	if err != nil {
		return 0
	}
	return time.Duration(n.Int64())
}

func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	var seconds int
	if _, err := fmt.Sscanf(value, "%d", &seconds); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return time.Until(at)
	}
	return 0
}

func dispatch(ctx context.Context, std IO, server *op.Server, inv *op.Invocation, w wrapperOpts) int {
	serverURL := cmp.Or(inv.Global.Server, std.getenv("RUKIE_SERVER"))
	configPath := cmp.Or(w.config, std.getenv("RUKIE_CONFIG"))
	if serverURL != "" {
		return dispatchRemote(ctx, std, server, inv, serverURL, w)
	}
	if configPath != "" {
		return dispatchLocal(ctx, std, inv, configPath, w)
	}
	emit(std.Stderr, "rukie: --server or --config is required")
	return 64
}

func credentials(std IO, inv *op.Invocation, w wrapperOpts) (token, session string, code int) {
	if inv.Global.TokenFile != "" {
		raw, err := readSecretFile(inv.Global.TokenFile)
		if err != nil {
			emit(std.Stderr, "rukie:", err.Error())
			return "", "", 64
		}
		token = raw
	} else if path := std.getenv("RUKIE_TOKEN_FILE"); path != "" {
		raw, err := readSecretFile(path)
		if err != nil {
			emit(std.Stderr, "rukie:", err.Error())
			return "", "", 64
		}
		token = raw
	} else if value := std.getenv("RUKIE_TOKEN"); value != "" {
		token = strings.TrimSpace(value)
	}
	sessionFile := w.sessionFile
	if sessionFile == "" {
		sessionFile = DefaultSessionFile(std.Getenv)
	}
	if token == "" {
		if w.sessionFile != "" {
			raw, err := readSecretFile(w.sessionFile)
			if err != nil {
				emit(std.Stderr, "rukie:", err.Error())
				return "", "", 64
			}
			session = raw
		} else if _, err := os.Stat(sessionFile); err == nil {
			raw, err := readSecretFile(sessionFile)
			if err != nil {
				emit(std.Stderr, "rukie:", err.Error())
				return "", "", 64
			}
			session = raw
		}
	}
	return token, session, 0
}

func readSecretFile(path string) (string, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("read secret file: %w", err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "", fmt.Errorf("secret file %q is empty", path)
	}
	return trimmed, nil
}

func applyInput(std IO, inv *op.Invocation) int {
	if inv.Global.Input == "" {
		return 0
	}
	var raw []byte
	var err error
	if inv.Global.Input == "-" {
		raw, err = io.ReadAll(std.Stdin)
	} else {
		raw, err = os.ReadFile(inv.Global.Input)
	}
	if err != nil {
		emit(std.Stderr, "rukie:", err.Error())
		return 64
	}
	var obj map[string]any
	if err := jsonv2.Unmarshal(raw, &obj, jsonv1.DefaultOptionsV1()); err != nil {
		emit(std.Stderr, "rukie: --input is not a JSON object")
		return 64
	}
	merged := make(op.Args, len(obj)+len(inv.Args))
	maps.Copy(merged, obj)
	maps.Copy(merged, inv.Args)
	inv.Args = merged
	return 0
}

func dispatchRemote(ctx context.Context, std IO, server *op.Server, inv *op.Invocation, serverURL string, w wrapperOpts) int {
	base, err := url.Parse(strings.TrimSuffix(serverURL, "/"))
	if err != nil {
		emit(std.Stderr, "rukie: bad --server URL")
		return 64
	}
	token, session, code := credentials(std, inv, w)
	if code != 0 {
		return code
	}
	if code := applyInput(std, inv); code != 0 {
		return code
	}
	spec := inv.Leaf.Spec
	if w.dryRun {
		if !emitf(std.Stdout, "%s %s\n", spec.Method, spec.Path) {
			return 1
		}
		raw, _ := jsonv2.Marshal(inv.Args, jsonv1.DefaultOptionsV1())
		if !emit(std.Stdout, string(raw)) {
			return 1
		}
		return 0
	}
	if spec.Kind == op.KindMutation && inv.Args["idempotency_key"] == nil {
		inv.Args["idempotency_key"] = op.GenerateIdempotencyKey()
	}
	req, err := inv.Args.ToRequest(spec, server.Document(), base)
	if err != nil {
		emit(std.Stderr, "rukie:", err.Error())
		return 1
	}
	if inv.Global.JSON {
		req.Header.Set("Accept", "application/json")
	} else {
		req.Header.Set("Accept", "text/markdown")
	}
	timeout := inv.Global.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	jar, _ := cookiejar.New(nil)
	caller := &RemoteInvoker{
		Base: base, Token: token, Session: session, Jar: jar,
		Client: &http.Client{Timeout: timeout},
	}
	if session != "" {
		for _, part := range strings.Split(session, ";") {
			name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
			if ok {
				jar.SetCookies(base, []*http.Cookie{{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}})
			}
		}
	}
	if inv.Global.All {
		return runAll(ctx, std, server, inv, caller, req)
	}
	resp, err := caller.Invoke(ctx, req)
	if err != nil {
		emit(std.Stderr, "rukie:", err.Error())
		return 4
	}
	return render(std, inv, resp)
}

func runAll(ctx context.Context, std IO, server *op.Server, inv *op.Invocation, caller *RemoteInvoker, req *http.Request) int {
	merged := map[string][]any{}
	var first map[string]any
	next := req
	for {
		resp, err := caller.Invoke(ctx, next)
		if err != nil {
			emit(std.Stderr, "rukie:", err.Error())
			return 4
		}
		if resp.Status < 200 || resp.Status >= 300 {
			return render(std, inv, resp)
		}
		var body map[string]any
		if uerr := jsonv2.Unmarshal(resp.Body, &body, jsonv1.DefaultOptionsV1()); uerr != nil {
			return render(std, inv, resp)
		}
		if first == nil {
			first = body
		}
		if !accumulate(body, merged) {
			return render(std, inv, resp)
		}
		link := resp.Header.Get("Link")
		name, cursor, ok := nextCursor(link, queryParams(server, inv.Leaf.Spec))
		if !ok {
			return render(std, inv, mergedResponse(resp, first, merged))
		}
		inv.Args[name] = cursor
		var rebuilt *http.Request
		rebuilt, err = inv.Args.ToRequest(inv.Leaf.Spec, server.Document(), caller.Base)
		if err != nil {
			emit(std.Stderr, "rukie:", err.Error())
			return 1
		}
		rebuilt.Header.Set("Accept", req.Header.Get("Accept"))
		if rebuilt.URL.String() == next.URL.String() {
			return render(std, inv, mergedResponse(resp, first, merged))
		}
		next = rebuilt
	}
}

func mergedResponse(resp *op.Response, first map[string]any, merged map[string][]any) *op.Response {
	for key, items := range merged {
		first[key] = items
	}
	delete(first, "next")
	delete(first, "next_cursor")
	raw, _ := jsonv2.Marshal(first, jsonv1.DefaultOptionsV1())
	return &op.Response{Status: resp.Status, Header: resp.Header, Body: raw}
}

func accumulate(body map[string]any, merged map[string][]any) bool {
	found := false
	for _, key := range slices.Sorted(maps.Keys(body)) {
		items, ok := body[key].([]any)
		if !ok {
			continue
		}
		found = true
		merged[key] = append(merged[key], items...)
	}
	return found || len(merged) > 0
}

func queryParams(server *op.Server, spec *op.Spec) map[string]bool {
	allowed := map[string]bool{}
	if server == nil || server.Document() == nil || spec == nil {
		return allowed
	}
	for _, p := range server.Document().Operations[spec.Name].Params {
		if p.In == "query" {
			allowed[p.Name] = true
		}
	}
	return allowed
}

func nextCursor(link string, allowed map[string]bool) (string, string, bool) {
	for part := range strings.SplitSeq(link, ",") {
		segments := strings.Split(strings.TrimSpace(part), ";")
		if len(segments) != 2 {
			continue
		}
		if strings.TrimSpace(segments[1]) != `rel="next"` {
			continue
		}
		target := strings.Trim(strings.TrimSpace(segments[0]), "<>")
		u, err := url.Parse(target)
		if err != nil {
			return "", "", false
		}
		query := u.Query()
		for _, name := range []string{"cursor", "after"} {
			if value := query.Get(name); value != "" && allowed[name] {
				return name, value, true
			}
		}
	}
	return "", "", false
}

func render(std IO, inv *op.Invocation, resp *op.Response) int {
	if resp.Status >= 200 && resp.Status < 300 {
		if len(resp.Body) > 0 && !emit(std.Stdout, string(resp.Body)) {
			return 1
		}
		if inv.Leaf.Spec.Kind == op.KindMutation {
			if key, ok := inv.Args["idempotency_key"].(string); ok && key != "" {
				emit(std.Stderr, "idempotency-key: "+key)
			}
		}
		return 0
	}
	if inv.Global.JSON {
		if !emit(std.Stdout, string(resp.Body)) {
			return 1
		}
	} else {
		emit(std.Stderr, problemDetail(resp.Body))
	}
	return exitForProblem(resp.Body)
}

func problemDetail(body []byte) string {
	var decoded map[string]any
	if err := jsonv2.Unmarshal(body, &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		return "request failed"
	}
	code, _ := decoded["code"].(string)
	detail, _ := decoded["detail"].(string)
	if detail != "" {
		return code + ": " + detail
	}
	if title, _ := decoded["title"].(string); title != "" {
		return code + ": " + title
	}
	return code
}

func exitForProblem(body []byte) int {
	var decoded map[string]any
	if err := jsonv2.Unmarshal(body, &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		return 1
	}
	slug, _ := decoded["code"].(string)
	if slug == "" {
		return 1
	}
	if p, ok := op.LookupProblemSlug(slug); ok {
		return p.Exit
	}
	return 1
}
