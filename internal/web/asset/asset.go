package asset

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gosuda/rukie/internal/web/secheaders"
)

type Asset struct {
	Name        string
	ContentType string
	Bytes       []byte
}

func (a Asset) sum() [32]byte {
	return sha256.Sum256(a.Bytes)
}

func (a Asset) ETag() string {
	sum := a.sum()
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (a Asset) ShortHash() string {
	sum := a.sum()
	return hex.EncodeToString(sum[:])[:16]
}

func (a Asset) URLPath() string {
	ext := ".bin"
	if i := strings.LastIndex(a.Name, "."); i >= 0 {
		ext = a.Name[i:]
	}
	base := strings.TrimSuffix(a.Name, ext)
	return "/static/" + base + "." + a.ShortHash() + ext
}

func (a Asset) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", a.ContentType)
	h.Set("ETag", a.ETag())
	secheaders.ApplyStatic(h)
	if r.Header.Get("If-None-Match") == a.ETag() {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(a.Bytes)
}
