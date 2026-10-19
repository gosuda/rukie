package secheaders

import "net/http"

const CSP = "default-src 'none'; style-src 'self' https://fonts.bunny.net; " +
	"font-src https://fonts.bunny.net; img-src 'self' data:; " +
	"form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

const (
	contentTypeOptions = "nosniff"
	referrerPolicy     = "strict-origin-when-cross-origin"
	crossOriginPolicy  = "same-origin"
	immutableCache     = "public, max-age=31536000, immutable"
)

func apply(w http.Header, static bool) {
	w.Set("Content-Security-Policy", CSP)
	w.Set("X-Content-Type-Options", contentTypeOptions)
	w.Set("Referrer-Policy", referrerPolicy)
	if static {
		w.Set("Cross-Origin-Resource-Policy", crossOriginPolicy)
	}
}

func ApplyHTML(w http.Header) {
	apply(w, false)
}

func ApplyStatic(w http.Header) {
	apply(w, true)
	w.Set("Cache-Control", immutableCache)
}
