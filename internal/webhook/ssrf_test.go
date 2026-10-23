package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("bad test addr %q: %v", s, err)
	}
	return addr
}

func TestWebhookSSRFDenyList(t *testing.T) {
	deny := []string{
		"127.0.0.1", "127.0.0.2", "::1",
		"10.0.0.1", "10.255.255.255",
		"172.16.0.1", "172.31.255.255",
		"192.168.0.1", "192.168.255.255",
		"169.254.169.254", "169.254.0.1",
		"100.64.0.1", "100.127.255.255",
		"192.0.0.1", "192.0.0.254",
		"198.18.0.1", "198.19.255.255",
		"240.0.0.1", "255.255.255.254",
		"64:ff9b::1", "64:ff9b::808:808",
		"::ffff:10.0.0.1",
		"fc00::1", "fd00::1",
		"::", "0.0.0.0",
		"224.0.0.1", "ff02::1", "ff00::1",
		"192.0.2.1", "198.51.100.1", "203.0.113.1",
		"2001:db8::1", "fe80::1",
	}
	policy := &Policy{}
	for _, ip := range deny {
		raw := ip
		if strings.Contains(raw, ":") {
			raw = "[" + raw + "]"
		}
		host, err := policy.CheckURL("https://" + raw + "/hook")
		if err != nil {
			t.Fatalf("CheckURL(https://%s/hook) = %v, want pass to resolve stage", ip, err)
		}
		if _, err := policy.ResolveAndCheck(context.Background(), host); err == nil {
			t.Fatalf("ResolveAndCheck(%s) allowed a denied address", ip)
		} else if !strings.HasPrefix(err.Error(), "denied by egress policy: ") {
			t.Fatalf("ResolveAndCheck(%s) error = %q, want egress prefix", ip, err)
		}
	}
	allow := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700:4700::1111"}
	for _, ip := range allow {
		if _, err := policy.ResolveAndCheck(context.Background(), ip); err != nil {
			t.Fatalf("ResolveAndCheck(%s) = %v, want allow", ip, err)
		}
	}
	mixed := &Policy{Resolve: func(_ context.Context, _ string) ([]netip.Addr, error) {
		return []netip.Addr{mustAddr(t, "93.184.216.34"), mustAddr(t, "10.0.0.1")}, nil
	}}
	if _, err := mixed.ResolveAndCheck(context.Background(), "mixed.example"); err == nil {
		t.Fatal("ResolveAndCheck allowed a mixed good+evil answer")
	}
	good := &Policy{Resolve: func(_ context.Context, _ string) ([]netip.Addr, error) {
		return []netip.Addr{mustAddr(t, "93.184.216.34")}, nil
	}}
	if _, err := good.ResolveAndCheck(context.Background(), "good.example"); err != nil {
		t.Fatalf("ResolveAndCheck(good.example) = %v, want allow", err)
	}
	for _, tc := range []struct {
		name string
		ip   string
	}{
		{"ietf-protocol-assignments", "192.0.0.1"},
		{"benchmarking", "198.18.0.1"},
		{"reserved", "240.0.0.1"},
		{"nat64", "64:ff9b::808:808"},
	} {
		resolved := &Policy{Resolve: func(_ context.Context, _ string) ([]netip.Addr, error) {
			return []netip.Addr{mustAddr(t, tc.ip)}, nil
		}}
		if _, err := resolved.ResolveAndCheck(context.Background(), tc.name+".example"); err == nil {
			t.Fatalf("ResolveAndCheck(%s) allowed a denied resolved address %s", tc.name, tc.ip)
		} else if !strings.HasPrefix(err.Error(), "denied by egress policy: ") {
			t.Fatalf("ResolveAndCheck(%s) error = %q, want egress prefix", tc.name, err)
		}
	}
	malformed := map[string]string{
		"userinfo":  "https://user@example.com/hook",
		"scheme":    "ftp://example.com/hook",
		"noScheme":  "example.com/hook",
		"emptyHost": "https:///hook",
		"overlong":  "https://example.com/" + strings.Repeat("a", 2048),
		"portZero":  "https://example.com:0/hook",
		"portHuge":  "https://example.com:99999/hook",
		"http":      "http://example.com/hook",
		"controls":  "https://example.com/ho\x01ok",
	}
	for name, raw := range malformed {
		if _, err := policy.CheckURL(raw); err == nil {
			t.Fatalf("CheckURL(%s) allowed %q", name, raw)
		} else if !strings.Contains(err.Error(), "invalid webhook url: ") {
			t.Fatalf("CheckURL(%s) error = %q, want invalid prefix", name, err)
		}
	}
	insecure := &Policy{AllowInsecureHTTP: true}
	if _, err := insecure.CheckURL("http://example.com/hook"); err != nil {
		t.Fatalf("CheckURL(http) with flag = %v, want allow", err)
	}
	bypass := &Policy{AllowPrivateTargets: true}
	for _, ip := range []string{"127.0.0.1", "10.1.2.3"} {
		if _, err := bypass.ResolveAndCheck(context.Background(), ip); err != nil {
			t.Fatalf("bypass ResolveAndCheck(%s) = %v, want allow", ip, err)
		}
	}
	if _, err := bypass.CheckURL("ftp://example.com/hook"); err == nil {
		t.Fatal("bypass CheckURL allowed a bad scheme")
	}
	extra := &Policy{ExtraDenyCIDRs: []string{"93.184.216.0/24"}}
	if _, err := extra.ResolveAndCheck(context.Background(), "93.184.216.34"); err == nil {
		t.Fatal("ResolveAndCheck ignored ExtraDenyCIDRs")
	}
	badExtra := &Policy{ExtraDenyCIDRs: []string{"nope"}}
	if _, err := badExtra.ResolveAndCheck(context.Background(), "93.184.216.34"); err == nil {
		t.Fatal("ResolveAndCheck accepted a bad ExtraDenyCIDRs entry")
	}
	if _, err := policy.DialValidated(context.Background(), "tcp", mustAddr(t, "169.254.169.254"), "443", "example.com"); err == nil {
		t.Fatal("DialValidated dialed a denied address")
	} else if !strings.HasPrefix(err.Error(), "denied by egress policy: ") {
		t.Fatalf("DialValidated error = %q, want egress prefix", err)
	}
}

func TestWebhookPrivateBypassNarrowed(t *testing.T) {
	bypass := &Policy{AllowPrivateTargets: true}
	pass := []string{
		"127.0.0.1", "127.0.0.2", "::1", "::ffff:10.0.0.1",
		"10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.0.1",
	}
	for _, ip := range pass {
		if _, err := bypass.ResolveAndCheck(context.Background(), ip); err != nil {
			t.Fatalf("bypass ResolveAndCheck(%s) = %v, want allow", ip, err)
		}
	}
	stillDenied := []string{
		"169.254.169.254", "169.254.0.1",
		"224.0.0.1", "ff02::1",
		"192.0.2.1", "198.51.100.1", "203.0.113.1", "2001:db8::1",
		"100.64.0.1", "100.127.255.255",
		"fc00::1", "fd00::1", "fe80::1",
		"192.0.0.1", "198.18.0.1", "240.0.0.1", "255.255.255.255",
		"64:ff9b::1", "64:ff9b::808:808",
		"::", "0.0.0.0",
	}
	for _, ip := range stillDenied {
		raw := ip
		if strings.Contains(raw, ":") {
			raw = "[" + raw + "]"
		}
		host, err := bypass.CheckURL("https://" + raw + "/hook")
		if err != nil {
			t.Fatalf("CheckURL(https://%s/hook) = %v, want pass to resolve stage", ip, err)
		}
		if _, err := bypass.ResolveAndCheck(context.Background(), host); err == nil {
			t.Fatalf("bypass ResolveAndCheck(%s) allowed a non-bypassable address", ip)
		} else if !strings.HasPrefix(err.Error(), "denied by egress policy: ") {
			t.Fatalf("bypass ResolveAndCheck(%s) error = %q, want egress prefix", ip, err)
		}
	}
	if err := checkDenied(bypass, mustAddr(t, "192.0.2.1")); err == nil {
		t.Fatal("checkDenied allowed a documentation address under the flag")
	}
	if err := checkDenied(bypass, mustAddr(t, "10.9.9.9")); err != nil {
		t.Fatalf("checkDenied denied RFC 1918 under the flag: %v", err)
	}
	if _, err := bypass.DialValidated(context.Background(), "tcp", mustAddr(t, "169.254.169.254"), "443", "example.com"); err == nil {
		t.Fatal("DialValidated dialed link-local under the flag")
	} else if !strings.HasPrefix(err.Error(), "denied by egress policy: ") {
		t.Fatalf("DialValidated error = %q, want egress prefix", err)
	}
}

func TestWebhookNoRedirects(t *testing.T) {
	var secondHits int
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, second.URL, http.StatusFound)
	}))
	defer first.Close()
	client := (&Policy{AllowInsecureHTTP: true, AllowPrivateTargets: true}).NewClient(5 * time.Second)
	resp, err := client.Get(first.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302 without following", resp.StatusCode)
	}
	if secondHits != 0 {
		t.Fatalf("second server hit %d times, want 0", secondHits)
	}
}
