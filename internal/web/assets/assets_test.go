package assets

import (
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/web/secheaders"
)

func appCSS(t *testing.T) string {
	t.Helper()
	for _, a := range Registry() {
		if a.Name == "app.css" {
			return string(a.Bytes)
		}
	}
	t.Fatal("app.css missing from registry")
	return ""
}

func TestCSSLayered(t *testing.T) {
	css := appCSS(t)
	first := ""
	for l := range strings.SplitSeq(css, "\n") {
		if strings.TrimSpace(l) != "" {
			first = strings.TrimSpace(l)
			break
		}
	}
	if first != "@layer reset, tokens, base, layout, components, code, diff;" {
		t.Fatalf("first statement = %q, want the @layer list", first)
	}
	if strings.Contains(css, "!important") {
		t.Fatal("app.css uses !important")
	}
	for _, name := range []string{"reset", "tokens", "base", "layout", "components", "code", "diff"} {
		if !strings.Contains(css, "@layer "+name+" {") {
			t.Fatalf("@layer %s block missing", name)
		}
	}
	depth := 0
	for l := range strings.SplitSeq(css, "\n") {
		trim := strings.TrimSpace(l)
		opens := strings.Count(l, "{")
		closes := strings.Count(l, "}")
		if depth == 0 && opens > 0 && !strings.HasPrefix(trim, "@layer") {
			t.Fatalf("unlayered rule at depth 0: %q", trim)
		}
		depth += opens - closes
	}
	if depth != 0 {
		t.Fatalf("unbalanced braces, depth = %d", depth)
	}
	reduced := strings.Index(css, "@media (prefers-reduced-motion: no-preference)")
	if reduced < 0 {
		t.Fatal("reduced-motion gate missing")
	}
	keys := strings.Index(css, "@keyframes")
	if keys < 0 {
		t.Fatal("CI spinner keyframes missing")
	}
	if keys < reduced {
		t.Fatal("@keyframes outside the reduced-motion gate")
	}
	allowed := map[string]bool{
		"background-color": true,
		"color":            true,
		"box-shadow":       true,
		"opacity":          true,
		"outline-color":    true,
		"transform":        true,
	}
	item := regexp.MustCompile(`([a-z-]+)\s+(\d+)ms\s+cubic-bezier\(0\.22,\s*1,\s*0\.36,\s*1\)`)
	decl := regexp.MustCompile(`transition:\s*([^;]+);`)
	if decl.FindString(css) == "" {
		t.Fatal("no transition declarations found")
	}
	for _, m := range decl.FindAllStringSubmatch(css, -1) {
		items := item.FindAllStringSubmatch(m[1], -1)
		if len(items) == 0 {
			t.Fatalf("transition without instrument easing: %q", m[1])
		}
		for _, it := range items {
			if !allowed[it[1]] {
				t.Fatalf("transition on forbidden property %q", it[1])
			}
			ms, err := strconv.Atoi(it[2])
			if err != nil || ms > 150 {
				t.Fatalf("transition duration %q exceeds 150ms", it[2])
			}
		}
	}
}

var oklchRe = regexp.MustCompile(`--([a-z0-9-]+):\s*oklch\(([0-9.]+)\s+([0-9.]+)\s+([0-9.]+)`)

func parseTokens(css string) map[string][3]float64 {
	out := map[string][3]float64{}
	for _, m := range oklchRe.FindAllStringSubmatch(css, -1) {
		l, _ := strconv.ParseFloat(m[2], 64)
		c, _ := strconv.ParseFloat(m[3], 64)
		h, _ := strconv.ParseFloat(m[4], 64)
		out[m[1]] = [3]float64{l, c, h}
	}
	return out
}

func luminance(v [3]float64) float64 {
	hr := v[2] * math.Pi / 180
	a := v[1] * math.Cos(hr)
	b := v[1] * math.Sin(hr)
	lp := v[0] + 0.3963377774*a + 0.2158037573*b
	mp := v[0] - 0.1055613458*a - 0.0638541728*b
	sp := v[0] - 0.0894841775*a - 1.2914855480*b
	l := lp * lp * lp
	m := mp * mp * mp
	s := sp * sp * sp
	r := 4.0767416621*l - 3.3077115913*m + 0.2309699292*s
	g := -1.2684380046*l + 2.6097574011*m - 0.3413193965*s
	bl := -0.0041960863*l - 0.7034186147*m + 1.7076147010*s
	return 0.2126*r + 0.7152*g + 0.0722*bl
}

func ratio(fg, bg [3]float64) float64 {
	y1 := luminance(fg)
	y2 := luminance(bg)
	if y1 < y2 {
		y1, y2 = y2, y1
	}
	return (y1 + 0.05) / (y2 + 0.05)
}

func TestTokenContrast(t *testing.T) {
	css := appCSS(t)
	parts := strings.SplitN(css, "@media (prefers-color-scheme: dark)", 2)
	if len(parts) != 2 {
		t.Fatal("dark scheme block missing")
	}
	light := parseTokens(parts[0])
	dark := parseTokens(parts[1])
	for _, name := range []string{"paper", "s1", "s2", "s3", "ink", "ink2", "ink3", "accent", "accent-strong", "ok", "ok-tint", "warn", "warn-tint", "fail", "fail-tint", "info", "info-tint"} {
		if _, ok := light[name]; !ok {
			t.Fatalf("light token --%s missing", name)
		}
		if _, ok := dark[name]; !ok {
			t.Fatalf("dark token --%s missing", name)
		}
	}
	aa := []struct {
		name string
		fg   string
		bg   string
		dark bool
		min  float64
	}{
		{"light ink/paper", "ink", "paper", false, 7},
		{"light ink2/paper", "ink2", "paper", false, 4.5},
		{"light ink2/s2", "ink2", "s2", false, 4.5},
		{"light ink3/s1 icon tier", "ink3", "s1", false, 3},
		{"light accent/paper", "accent", "paper", false, 4.5},
		{"light ok chip", "ok", "ok-tint", false, 4.5},
		{"light warn chip", "warn", "warn-tint", false, 4.5},
		{"light fail chip", "fail", "fail-tint", false, 4.5},
		{"light info chip", "info", "info-tint", false, 4.5},
		{"light button label", "paper", "accent-strong", false, 4.5},
		{"dark ink/paper", "ink", "paper", true, 7},
		{"dark ink2/s1", "ink2", "s1", true, 4.5},
		{"dark ink3/s1", "ink3", "s1", true, 4.5},
		{"dark accent/paper", "accent", "paper", true, 4.5},
		{"dark ok chip", "ok", "ok-tint", true, 4.5},
		{"dark warn chip", "warn", "warn-tint", true, 4.5},
		{"dark fail chip", "fail", "fail-tint", true, 4.5},
		{"dark info chip", "info", "info-tint", true, 4.5},
		{"dark button label", "paper", "accent-strong", true, 4.5},
	}
	sets := map[bool]map[string][3]float64{false: light, true: dark}
	for _, tc := range aa {
		t.Run(tc.name, func(t *testing.T) {
			set := sets[tc.dark]
			if got := ratio(set[tc.fg], set[tc.bg]); got < tc.min {
				t.Fatalf("contrast %s/%s = %.2f, want >= %.1f", tc.fg, tc.bg, got, tc.min)
			}
		})
	}
}

func TestAssetHash(t *testing.T) {
	reg := Registry()
	if len(reg) != 3 {
		t.Fatalf("registry holds %d assets, want 3", len(reg))
	}
	seen := map[string]bool{}
	for _, a := range reg {
		if !strings.Contains(a.URLPath(), a.ShortHash()) {
			t.Fatalf("%s URL %q lacks its hash", a.Name, a.URLPath())
		}
		if len(a.ETag()) < 2 || !strings.HasPrefix(a.ETag(), `"`) {
			t.Fatalf("%s ETag %q is not strong-quoted", a.Name, a.ETag())
		}
		if seen[a.URLPath()] {
			t.Fatalf("duplicate URL %q", a.URLPath())
		}
		seen[a.URLPath()] = true
		if a.Name == "app.css" && len(a.Bytes) > 32*1024 {
			t.Fatalf("app.css = %d bytes, budget 32 KiB", len(a.Bytes))
		}
		if a.Name == "chroma.css" && len(a.Bytes) > 24*1024 {
			t.Fatalf("chroma.css = %d bytes, budget 24 KiB", len(a.Bytes))
		}
	}
	if reg[0].URLPath() == reg[1].URLPath() {
		t.Fatal("both assets share one URL")
	}
}

func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	t.Cleanup(func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close body: %v", err)
		}
	})
}

func TestStaticHeaders(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	var appPath string
	for _, a := range Registry() {
		if a.Name == "app.css" {
			appPath = a.URLPath()
		}
	}
	resp, err := http.Get(srv.URL + appPath)
	if err != nil {
		t.Fatalf("GET = %v", err)
	}
	closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	h := resp.Header
	if got := h.Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := h.Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("Cache-Control = %q, want immutable", got)
	}
	if got := h.Get("Content-Security-Policy"); got != secheaders.CSP {
		t.Fatalf("CSP = %q", got)
	}
	if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("XCTO = %q", got)
	}
	if got := h.Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Fatalf("CORP = %q", got)
	}
	etag := h.Get("ETag")
	if etag == "" {
		t.Fatal("ETag missing")
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+appPath, nil)
	req.Header.Set("If-None-Match", etag)
	cached, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("conditional GET = %v", err)
	}
	closeBody(t, cached)
	if cached.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional status = %d, want 304", cached.StatusCode)
	}
	notFound, err := http.Get(srv.URL + "/static/nope.css")
	if err != nil {
		t.Fatalf("GET missing = %v", err)
	}
	closeBody(t, notFound)
	if notFound.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", notFound.StatusCode)
	}
	post, err := http.Post(srv.URL+appPath, "text/css", nil)
	if err != nil {
		t.Fatalf("POST = %v", err)
	}
	closeBody(t, post)
	if post.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", post.StatusCode)
	}
}

func TestChromaCSSFresh(t *testing.T) {
	fresh, err := GenerateChromaCSS()
	if err != nil {
		t.Fatalf("GenerateChromaCSS = %v", err)
	}
	var committed []byte
	for _, a := range Registry() {
		if a.Name == "chroma.css" {
			committed = a.Bytes
		}
	}
	if fresh != string(committed) {
		t.Fatal("committed chroma.css differs from GenerateChromaCSS output; regenerate it")
	}
}
