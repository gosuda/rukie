package api

import (
	"net/http"
	"testing"
)

func TestArtifactInflightBound(t *testing.T) {
	artifactInflight.Store(0)
	t.Cleanup(func() { artifactInflight.Store(0) })
	if !reserveArtifactBytes(8) {
		t.Fatal("empty inflight budget refused 8 bytes")
	}
	if reserveArtifactBytes(maxArtifactInflightBytes) {
		t.Fatal("inflight budget admitted past the cap")
	}
	releaseArtifactBytes(8)
	if !reserveArtifactBytes(maxArtifactInflightBytes) {
		t.Fatal("released budget did not admit a full reservation")
	}
	releaseArtifactBytes(maxArtifactInflightBytes)
	if ciRunnerStatus("E_BUSY") != http.StatusTooManyRequests {
		t.Fatal("E_BUSY does not map to 429")
	}
}
