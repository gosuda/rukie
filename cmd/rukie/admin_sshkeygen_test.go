package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestAdminSSHKeygenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "ssh_host_key")
	if code := runSSHKeygen([]string{"--out", out}); code != 0 {
		t.Fatalf("ssh-keygen = %d", code)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if !strings.Contains(string(raw), "OPENSSH PRIVATE KEY") {
		t.Fatalf("key file is not an OpenSSH private key")
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	if typ := signer.PublicKey().Type(); typ != "ssh-ed25519" {
		t.Fatalf("host key type = %q, want ssh-ed25519", typ)
	}
	first := ssh.FingerprintSHA256(signer.PublicKey())
	if code := runSSHKeygen([]string{"--out", out}); code == 0 {
		t.Fatal("second ssh-keygen without --force succeeded")
	}
	if cerr := os.Chmod(out, 0o644); cerr != nil {
		t.Fatalf("chmod: %v", cerr)
	}
	if code := runSSHKeygen([]string{"--out", out, "--force"}); code != 0 {
		t.Fatalf("ssh-keygen --force = %d", code)
	}
	info, err = os.Stat(out)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode after force = %o, want 600", info.Mode().Perm())
	}
	raw, err = os.ReadFile(out)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	signer, err = ssh.ParsePrivateKey(raw)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	if fp := ssh.FingerprintSHA256(signer.PublicKey()); fp == first {
		t.Fatal("forced keygen kept the same key")
	}
}

func TestAdminSSHKeygenRequiresOut(t *testing.T) {
	if code := runSSHKeygen([]string{}); code != 2 {
		t.Fatalf("ssh-keygen without --out = %d, want 2", code)
	}
}
