package mirror

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
)

type HostKind string

const (
	HostGitHub  HostKind = "github"
	HostGitLab  HostKind = "gitlab"
	HostForgejo HostKind = "forgejo"
	HostGeneric HostKind = "generic"
)

type Mode string

const (
	ModeExact       Mode = "exact"
	ModeFastForward Mode = "fast-forward-only"
)

type RefSelection string

const (
	RefSelectionAll               RefSelection = "all"
	RefSelectionDefaultBranchTags RefSelection = "default_branch_tags"
	RefSelectionGlobs             RefSelection = "globs"
)

type Mirror struct {
	Name                     string       `json:"name"`
	TargetURL                string       `json:"target_url"`
	HostKind                 HostKind     `json:"host_kind"`
	Username                 string       `json:"username"`
	Mode                     Mode         `json:"mode"`
	RefSelection             RefSelection `json:"ref_selection"`
	Globs                    []string     `json:"globs"`
	DebounceSeconds          uint64       `json:"debounce_seconds"`
	MaxDelaySeconds          uint64       `json:"max_delay_seconds"`
	ReconcileIntervalSeconds uint64       `json:"reconcile_interval_seconds"`
	IncludeNotes             bool         `json:"include_notes"`
	ProtectDefaultBranch     bool         `json:"protect_default_branch"`
	Enabled                  bool         `json:"enabled"`
	SyncOnRefUpdate          bool         `json:"sync_on_ref_update"`
	CreateIfMissing          bool         `json:"create_if_missing"`
	PolicyAck                bool         `json:"policy_ack"`
	AdoptExistingRefs        bool         `json:"adopt_existing_refs"`
}

func (m Mirror) Validate(_ time.Time) error {
	if m.Name == "" || len([]rune(m.Name)) > 100 {
		return fmt.Errorf("Mirror.Validate: invalid name")
	}
	for _, r := range m.Name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("Mirror.Validate: invalid name")
		}
	}
	u, err := url.Parse(m.TargetURL)
	if err != nil || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("Mirror.Validate: invalid target URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("Mirror.Validate: target URL must use HTTP or HTTPS")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	ip := net.ParseIP(host)
	loopbackAllowed := host == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme == "http" && !loopbackAllowed {
		return fmt.Errorf("Mirror.Validate: HTTP target must be loopback")
	}
	if ip != nil && !ip.IsLoopback() && (ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsLoopback()) {
		return fmt.Errorf("Mirror.Validate: private target IP")
	}
	switch m.HostKind {
	case HostGitHub, HostGitLab, HostForgejo, HostGeneric:
	default:
		return fmt.Errorf("Mirror.Validate: unknown host kind")
	}
	if m.HostKind == HostForgejo && host == "codeberg.org" && !m.PolicyAck {
		return fmt.Errorf("Mirror.Validate: codeberg policy acknowledgement required")
	}
	switch m.Mode {
	case ModeExact, ModeFastForward:
	default:
		return fmt.Errorf("Mirror.Validate: unknown mode")
	}
	switch m.RefSelection {
	case RefSelectionAll, RefSelectionDefaultBranchTags:
	case RefSelectionGlobs:
		for _, pattern := range m.Globs {
			if _, err := path.Match(pattern, ""); err != nil {
				return fmt.Errorf("Mirror.Validate: invalid glob: %w", err)
			}
		}
	default:
		return fmt.Errorf("Mirror.Validate: unknown ref selection")
	}
	if m.SyncOnRefUpdate && m.DebounceSeconds < 10 {
		return fmt.Errorf("Mirror.Validate: debounce must be at least 10 seconds")
	}
	if m.MaxDelaySeconds != 0 && m.DebounceSeconds != 0 && m.MaxDelaySeconds < m.DebounceSeconds {
		return fmt.Errorf("Mirror.Validate: max delay is less than debounce")
	}
	if m.ReconcileIntervalSeconds > 86400 {
		return fmt.Errorf("Mirror.Validate: reconcile interval exceeds 24 hours")
	}
	return nil
}
