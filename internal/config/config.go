// Package config loads and validates Housekeeparr's versioned TOML
// configuration. Credentials are only accepted as secret references.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
)

// CurrentVersion is the configuration schema version this build reads.
const CurrentVersion = 1

// Duration is a time.Duration written as a Go duration string ("10m").
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("invalid duration %q (use forms like \"90m\" or \"4h\")", string(b))
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// Mode is the global operating mode.
type Mode string

const (
	ModeObserve Mode = "observe"
	ModePaused  Mode = "paused"
	// ModeAutomatic enables policy evaluation of per-workflow automation
	// permissions; actual writes still require hard safety prerequisites.
	ModeAutomatic Mode = "automatic"
)

// Config is the validated configuration.
type Config struct {
	Version    int              `toml:"version"`
	Mode       Mode             `toml:"mode"`
	Automation AutomationConfig `toml:"automation"`
	State      StateConfig      `toml:"state"`
	Web        WebConfig        `toml:"web"`
	Polling    PollingConfig    `toml:"polling"`
	Retention  RetentionConfig  `toml:"retention"`
	Log        LogConfig        `toml:"log"`
	Policy     PolicyOverrides  `toml:"policy"`

	Rules      []RuleConfig      `toml:"rule"`
	Overrides  []OverrideConfig  `toml:"override"`
	Exclusions []ExclusionConfig `toml:"exclude"`

	Sonarr       []ManagerConfig `toml:"sonarr"`
	Radarr       []ManagerConfig `toml:"radarr"`
	QBittorrent  []ClientConfig  `toml:"qbittorrent"`
	Transmission []ClientConfig  `toml:"transmission"`
	Deluge       []ClientConfig  `toml:"deluge"`
	RTorrent     []ClientConfig  `toml:"rtorrent"`

	// Path is the file this configuration was loaded from.
	Path string `toml:"-"`
	// Revision is a digest of the effective (non-secret) configuration.
	Revision string `toml:"-"`
}

// AutomationConfig holds global automation controls and per-workflow enablement.
type AutomationConfig struct {
	EmergencyStop bool            `toml:"emergency_stop"`
	Workflows     map[string]bool `toml:"workflows"`
	// UnknownPrivacy opts workflows in to acting on transfers whose privacy
	// the client cannot report (see policy.UnknownPrivacyWorkflows).
	UnknownPrivacy map[string]bool `toml:"unknown_privacy"`
	// PrivateRecovery sets how no-progress recovery treats a transfer that
	// is private or of unknown privacy: policy.PrivateKeepRunning (default),
	// policy.PrivateStop, or policy.PrivateObserve.
	PrivateRecovery string       `toml:"private_recovery"`
	Limits          LimitsConfig `toml:"limits"`
}

// LimitsConfig bounds acquisition-starting actions. Unset fields use the
// proposed defaults in policy.DefaultLimits; none can be disabled.
type LimitsConfig struct {
	Acquisitions        *int      `toml:"acquisitions"`
	AcquisitionWindow   *Duration `toml:"acquisition_window"`
	ManagerAcquisitions *int      `toml:"manager_acquisitions"`
	ContentAttempts     *int      `toml:"content_attempts"`
	SuccessCooldown     *Duration `toml:"success_cooldown"`
	RetainedTransfers   *int      `toml:"retained_transfers"`
	RetainedGiB         *int64    `toml:"retained_gib"`
	ActionsPerCycle     *int      `toml:"actions_per_cycle"`
	CorrelatedStalls    *int      `toml:"correlated_stalls"`
	CorrelatedPercent   *int      `toml:"correlated_percent"`
}

// Limits returns the effective acquisition limits with provenance.
func (c *Config) Limits() policy.Limits {
	l := policy.DefaultLimits()
	c2 := c.Automation.Limits
	set := func(name, value string) {
		l.Sources = append(l.Sources, policy.Source{Setting: name, Value: value, From: "config [automation.limits] " + name})
	}
	if c2.Acquisitions != nil {
		l.Acquisitions = *c2.Acquisitions
		set("acquisitions", strconv.Itoa(l.Acquisitions))
	}
	if c2.AcquisitionWindow != nil {
		l.AcquisitionWindow = c2.AcquisitionWindow.Duration
		set("acquisition_window", l.AcquisitionWindow.String())
	}
	if c2.ManagerAcquisitions != nil {
		l.ManagerAcquisitions = *c2.ManagerAcquisitions
		set("manager_acquisitions", strconv.Itoa(l.ManagerAcquisitions))
	}
	if c2.ContentAttempts != nil {
		l.ContentAttempts = *c2.ContentAttempts
		set("content_attempts", strconv.Itoa(l.ContentAttempts))
	}
	if c2.SuccessCooldown != nil {
		l.SuccessCooldown = c2.SuccessCooldown.Duration
		set("success_cooldown", l.SuccessCooldown.String())
	}
	if c2.RetainedTransfers != nil {
		l.RetainedTransfers = *c2.RetainedTransfers
		set("retained_transfers", strconv.Itoa(l.RetainedTransfers))
	}
	if c2.ActionsPerCycle != nil {
		l.ActionsPerCycle = *c2.ActionsPerCycle
		set("actions_per_cycle", strconv.Itoa(l.ActionsPerCycle))
	}
	if c2.CorrelatedStalls != nil {
		l.CorrelatedStalls = *c2.CorrelatedStalls
		set("correlated_stalls", strconv.Itoa(l.CorrelatedStalls))
	}
	if c2.CorrelatedPercent != nil {
		l.CorrelatedPercent = *c2.CorrelatedPercent
		set("correlated_percent", strconv.Itoa(l.CorrelatedPercent))
	}
	if c2.RetainedGiB != nil {
		l.RetainedBytes = *c2.RetainedGiB << 30
		set("retained_gib", strconv.FormatInt(*c2.RetainedGiB, 10))
	}
	return l
}

func validateLimits(errf errFunc, cfg *Config) {
	l := cfg.Limits()
	intIn := func(field string, v, lo, hi int) {
		if v < lo || v > hi {
			errf("automation.limits."+field, "must be between %d and %d (limits cannot be disabled)", lo, hi)
		}
	}
	intIn("acquisitions", l.Acquisitions, 1, 1000)
	intIn("manager_acquisitions", l.ManagerAcquisitions, 1, 1000)
	intIn("content_attempts", l.ContentAttempts, 1, 20)
	if l.AcquisitionWindow < time.Hour || l.AcquisitionWindow > 7*24*time.Hour {
		errf("automation.limits.acquisition_window", "must be between 1h and 168h")
	} else if l.AcquisitionWindow > cfg.Retention.AuditEvents.Duration {
		errf("automation.limits.acquisition_window", "must not exceed retention.audit_events, or pacing history would be pruned while still counted")
	}
	intIn("retained_transfers", l.RetainedTransfers, 1, 10000)
	intIn("actions_per_cycle", l.ActionsPerCycle, 1, 10)
	intIn("correlated_stalls", l.CorrelatedStalls, 2, 10000)
	intIn("correlated_percent", l.CorrelatedPercent, 1, 100)
	if g := cfg.Automation.Limits.RetainedGiB; g != nil && (*g < 0 || *g > 1<<20) {
		errf("automation.limits.retained_gib", "must be between 0 (no byte ceiling) and 1048576")
	}
	if l.SuccessCooldown < 0 || l.SuccessCooldown > 30*24*time.Hour {
		errf("automation.limits.success_cooldown", "must be between 0s and 720h")
	}
}

// RuleConfig defines a scoped policy and/or automation rule.
type RuleConfig struct {
	Name       string          `toml:"name"`
	Priority   *int            `toml:"priority"`
	Match      RuleMatch       `toml:"match"`
	Policy     PolicyOverrides `toml:"policy"`
	Automation map[string]bool `toml:"automation"`
	// UnknownPrivacy: see AutomationConfig.UnknownPrivacy.
	UnknownPrivacy map[string]bool `toml:"unknown_privacy"`
	// PrivateRecovery: see AutomationConfig.PrivateRecovery.
	PrivateRecovery string `toml:"private_recovery"`
}

// RuleMatch selects which subjects a rule applies to. All non-empty fields
// must match.
type RuleMatch struct {
	Client   string `toml:"client"`
	Category string `toml:"category"`
	Manager  string `toml:"manager"`
	Privacy  string `toml:"privacy"`
}

// OverrideConfig defines an item-level policy and/or automation override.
type OverrideConfig struct {
	Transfer   string          `toml:"transfer"`
	Content    string          `toml:"content"`
	Reason     string          `toml:"reason"`
	Policy     PolicyOverrides `toml:"policy"`
	Automation map[string]bool `toml:"automation"`
	// UnknownPrivacy: see AutomationConfig.UnknownPrivacy.
	UnknownPrivacy map[string]bool `toml:"unknown_privacy"`
	// PrivateRecovery: see AutomationConfig.PrivateRecovery.
	PrivateRecovery string `toml:"private_recovery"`
}

// ExclusionConfig excludes a scope from one or more automation workflows.
type ExclusionConfig struct {
	Client    string   `toml:"client"`
	Category  string   `toml:"category"`
	Transfer  string   `toml:"transfer"`
	Content   string   `toml:"content"`
	Manager   string   `toml:"manager"`
	Workflows []string `toml:"workflows"`
	Reason    string   `toml:"reason"`
}

// StateConfig locates persistent state.
type StateConfig struct {
	Dir string `toml:"dir"`
}

// WebConfig controls the optional interface.
type WebConfig struct {
	Enabled bool   `toml:"enabled"`
	Listen  string `toml:"listen"`
}

// PollingConfig bounds external traffic.
type PollingConfig struct {
	Interval       Duration `toml:"interval"`
	RequestTimeout Duration `toml:"request_timeout"`
	MaxConcurrency int      `toml:"max_concurrency"`
	// Jitter is the maximum random delay added to each cycle start.
	Jitter Duration `toml:"jitter"`
}

// RetentionConfig bounds state growth.
type RetentionConfig struct {
	Observations               Duration `toml:"observations"`
	MaxObservationsPerTransfer int      `toml:"max_observations_per_transfer"`
	ConnectionChecks           Duration `toml:"connection_checks"`
	ResolvedFindings           Duration `toml:"resolved_findings"`
	AuditEvents                Duration `toml:"audit_events"`
}

// LogConfig controls log output.
type LogConfig struct {
	Level  string `toml:"level"`  // debug, info, warn, error
	Format string `toml:"format"` // text, json
}

// PolicyOverrides are optional diagnosis settings. A nil field inherits.
type PolicyOverrides struct {
	NewDownloadGrace   *Duration `toml:"new_download_grace"`
	NoProgressWindow   *Duration `toml:"no_progress_window"`
	NearCompleteRatio  *float64  `toml:"near_complete_ratio"`
	NearCompleteWindow *Duration `toml:"near_complete_window"`
	MinObservations    *int      `toml:"min_observations"`
	MaxObservationGap  *Duration `toml:"max_observation_gap"`
	MetadataTimeout    *Duration `toml:"metadata_timeout"`
	ImportSettle       *Duration `toml:"import_settle"`
	MinProgressBytes   *int64    `toml:"min_progress_bytes"`
}

// ConnectionCommon holds transport settings shared by every connection.
type ConnectionCommon struct {
	ID   string `toml:"id"`
	Name string `toml:"name"`
	URL  string `toml:"url"`
	// CAFile adds a PEM CA bundle; TLS verification is never disabled.
	CAFile string `toml:"ca_file"`
	// AllowInsecureHTTP acknowledges plain HTTP to a host that is not on a
	// recognisably local network (for example inside a trusted VPN tunnel).
	AllowInsecureHTTP bool `toml:"allow_insecure_http"`
	// Enabled defaults to true; false keeps the connection configured but idle.
	Enabled *bool `toml:"enabled"`
}

// IsEnabled reports whether the connection should be polled.
func (c ConnectionCommon) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// DisplayName returns the name or ID.
func (c ConnectionCommon) DisplayName() string {
	if c.Name != "" {
		return c.Name
	}
	return c.ID
}

// ManagerConfig configures a Sonarr or Radarr instance.
type ManagerConfig struct {
	ConnectionCommon
	APIKey SecretRef `toml:"api_key"`
	// DownloadClients maps the download client names configured inside the
	// manager to Housekeeparr client connection IDs. Without a mapping,
	// correlation falls back to infohash matching only, which is recorded as
	// weaker evidence.
	DownloadClients map[string]string `toml:"download_clients"`
	// QueuePageSize and MaxQueuePages bound queue pagination.
	QueuePageSize int `toml:"queue_page_size"`
	MaxQueuePages int `toml:"max_queue_pages"`
}

// ClientConfig configures a download client instance.
type ClientConfig struct {
	ConnectionCommon
	Username string    `toml:"username"`
	Password SecretRef `toml:"password"`
	// RPCPath is the RPC endpoint path for Transmission (default
	// /transmission/rpc) or rTorrent's XML-RPC gateway (default /RPC2).
	RPCPath string `toml:"rpc_path"`
	// Policy holds connection-scoped diagnosis overrides.
	Policy PolicyOverrides `toml:"policy"`
}

// Problem is a validation finding. Errors make the configuration unusable;
// warnings are shown but do not block startup.
type Problem struct {
	Severity string `json:"severity"` // "error" or "warning"
	Field    string `json:"field"`
	Message  string `json:"message"`
}

func (p Problem) String() string {
	if p.Field == "" {
		return p.Severity + ": " + p.Message
	}
	return fmt.Sprintf("%s: %s: %s", p.Severity, p.Field, p.Message)
}

// ValidationError aggregates problems.
type ValidationError struct{ Problems []Problem }

func (e *ValidationError) Error() string {
	var parts []string
	for _, p := range e.Problems {
		if p.Severity == "error" {
			parts = append(parts, p.String())
		}
	}
	return "invalid configuration:\n  " + strings.Join(parts, "\n  ")
}

// Defaults returns the configuration used for unset values.
func Defaults() Config {
	d := policy.DefaultSettings()
	return Config{
		Version: CurrentVersion,
		Mode:    ModeObserve,
		State:   StateConfig{Dir: "state"},
		Web:     WebConfig{Enabled: false, Listen: "127.0.0.1:8484"},
		Polling: PollingConfig{
			Interval:       Duration{d.PollInterval},
			RequestTimeout: Duration{30 * time.Second},
			MaxConcurrency: 4,
			Jitter:         Duration{30 * time.Second},
		},
		Retention: RetentionConfig{
			Observations:               Duration{7 * 24 * time.Hour},
			MaxObservationsPerTransfer: 1000,
			ConnectionChecks:           Duration{7 * 24 * time.Hour},
			ResolvedFindings:           Duration{90 * 24 * time.Hour},
			AuditEvents:                Duration{180 * 24 * time.Hour},
		},
		Log: LogConfig{Level: "info", Format: "text"},
	}
}

// Load reads, decodes, and validates a configuration file. Warnings are
// returned even on success.
func Load(path string) (*Config, []Problem, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read configuration: %w", err)
	}
	abs, _ := filepath.Abs(path)
	return Parse(raw, abs)
}

// Parse decodes and validates configuration bytes; path is used to resolve
// relative state directories.
func Parse(raw []byte, path string) (*Config, []Problem, error) {
	cfg := Defaults()
	md, err := toml.Decode(string(raw), &cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("parse configuration: %w", err)
	}
	cfg.Path = path
	var probs []Problem
	add := func(sev, field, msg string, args ...any) {
		probs = append(probs, Problem{Severity: sev, Field: field, Message: fmt.Sprintf(msg, args...)})
	}
	for _, k := range md.Undecoded() {
		add("error", k.String(), "unknown configuration key")
	}
	probs = append(probs, validate(&cfg, md)...)
	if cfg.State.Dir != "" && !filepath.IsAbs(cfg.State.Dir) && path != "" {
		cfg.State.Dir = filepath.Join(filepath.Dir(path), cfg.State.Dir)
	}
	cfg.Revision = cfg.computeRevision()
	sort.SliceStable(probs, func(i, j int) bool { return probs[i].Severity < probs[j].Severity })
	for _, p := range probs {
		if p.Severity == "error" {
			return nil, probs, &ValidationError{Problems: probs}
		}
	}
	return &cfg, probs, nil
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

func validate(cfg *Config, md toml.MetaData) []Problem {
	var probs []Problem
	errf := func(field, msg string, args ...any) {
		probs = append(probs, Problem{"error", field, fmt.Sprintf(msg, args...)})
	}
	warnf := func(field, msg string, args ...any) {
		probs = append(probs, Problem{"warning", field, fmt.Sprintf(msg, args...)})
	}
	if !md.IsDefined("version") {
		errf("version", "required; set version = %d", CurrentVersion)
	} else if cfg.Version != CurrentVersion {
		errf("version", "unsupported configuration version %d (this build reads version %d)", cfg.Version, CurrentVersion)
	}
	switch cfg.Mode {
	case ModeObserve, ModePaused, ModeAutomatic:
	default:
		errf("mode", "must be \"observe\", \"paused\", or \"automatic\"")
	}
	validateWorkflowMap(errf, "automation.workflows", cfg.Automation.Workflows)
	validateUnknownPrivacyMap(errf, "automation.unknown_privacy", cfg.Automation.UnknownPrivacy)
	validatePrivateRecovery(errf, "automation.private_recovery", cfg.Automation.PrivateRecovery)
	validateLimits(errf, cfg)
	if strings.TrimSpace(cfg.State.Dir) == "" {
		errf("state.dir", "required")
	}

	// Web interface: loopback only until authentication exists.
	if cfg.Web.Enabled || md.IsDefined("web", "listen") {
		host, port, err := net.SplitHostPort(cfg.Web.Listen)
		if err != nil {
			errf("web.listen", "must be host:port, e.g. 127.0.0.1:8484")
		} else {
			if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
				errf("web.listen", "invalid port %q", port)
			}
			if !isLoopbackHost(host) {
				errf("web.listen", "%q is not a loopback address; remote exposure requires authentication and TLS or a trusted reverse proxy, which this milestone does not implement. Bind to 127.0.0.1 and use an SSH tunnel or local reverse proxy", host)
			}
		}
	}

	p := cfg.Polling
	if p.Interval.Duration < time.Minute || p.Interval.Duration > time.Hour {
		errf("polling.interval", "must be between 1m and 1h")
	}
	if p.RequestTimeout.Duration < time.Second || p.RequestTimeout.Duration > 2*time.Minute {
		errf("polling.request_timeout", "must be between 1s and 2m")
	}
	if p.MaxConcurrency < 1 || p.MaxConcurrency > 8 {
		errf("polling.max_concurrency", "must be between 1 and 8")
	}
	if p.Jitter.Duration < 0 || p.Jitter.Duration > p.Interval.Duration/4 {
		errf("polling.jitter", "must be between 0 and a quarter of the polling interval")
	}

	r := cfg.Retention
	if r.Observations.Duration < 24*time.Hour || r.Observations.Duration > 90*24*time.Hour {
		errf("retention.observations", "must be between 24h and 2160h (90 days)")
	}
	if r.MaxObservationsPerTransfer < 100 || r.MaxObservationsPerTransfer > 10000 {
		errf("retention.max_observations_per_transfer", "must be between 100 and 10000")
	}
	if r.ConnectionChecks.Duration < 24*time.Hour || r.ConnectionChecks.Duration > 90*24*time.Hour {
		errf("retention.connection_checks", "must be between 24h and 2160h")
	}
	if r.ResolvedFindings.Duration < 24*time.Hour || r.ResolvedFindings.Duration > 3*365*24*time.Hour {
		errf("retention.resolved_findings", "must be between 24h and 26280h (3 years)")
	}
	if r.AuditEvents.Duration < 7*24*time.Hour || r.AuditEvents.Duration > 3*365*24*time.Hour {
		errf("retention.audit_events", "must be between 168h and 26280h")
	}

	switch cfg.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		errf("log.level", "must be debug, info, warn, or error")
	}
	switch cfg.Log.Format {
	case "text", "json":
	default:
		errf("log.format", "must be text or json")
	}

	ids := map[string]string{}
	checkCommon := func(section string, i int, c *ConnectionCommon) {
		field := fmt.Sprintf("%s[%d]", section, i)
		if !idPattern.MatchString(c.ID) {
			errf(field+".id", "required; lowercase letters, digits, '-' or '_' (max 32 characters)")
		} else if prev, dup := ids[c.ID]; dup {
			errf(field+".id", "duplicate connection id %q (also used by %s)", c.ID, prev)
		} else {
			ids[c.ID] = field
			field = fmt.Sprintf("%s.%s", section, c.ID)
		}
		u, err := url.Parse(c.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			errf(field+".url", "must be an http:// or https:// URL")
			return
		}
		if u.User != nil {
			errf(field+".url", "must not contain credentials; use secret references")
		}
		if u.RawQuery != "" {
			errf(field+".url", "must not contain a query string")
		}
		if u.Scheme == "http" && !isLocalNetworkHost(u.Hostname()) {
			if c.AllowInsecureHTTP {
				warnf(field+".url", "plain HTTP to %s is allowed by allow_insecure_http; credentials are only protected by your tunnel or network", u.Hostname())
			} else {
				errf(field+".url", "plain HTTP to non-local host %q would expose credentials; use https, or set allow_insecure_http = true if traffic runs inside a trusted tunnel", u.Hostname())
			}
		}
		if c.CAFile != "" && !filepath.IsAbs(c.CAFile) {
			errf(field+".ca_file", "must be an absolute path")
		}
	}

	checkManager := func(section string, list []ManagerConfig) {
		for i := range list {
			m := &list[i]
			checkCommon(section, i, &m.ConnectionCommon)
			field := section + "." + m.ID
			if m.APIKey == "" {
				errf(field+".api_key", "required (e.g. \"env:%s_API_KEY\" or \"file:/run/secrets/%s_api_key\")", strings.ToUpper(section), section)
			} else if err := m.APIKey.validateSyntax(); err != nil {
				errf(field+".api_key", "%v", err)
			}
			if m.QueuePageSize == 0 {
				m.QueuePageSize = 200
			}
			if m.MaxQueuePages == 0 {
				m.MaxQueuePages = 25
			}
			if m.QueuePageSize < 10 || m.QueuePageSize > 1000 {
				errf(field+".queue_page_size", "must be between 10 and 1000")
			}
			if m.MaxQueuePages < 1 || m.MaxQueuePages > 200 {
				errf(field+".max_queue_pages", "must be between 1 and 200")
			}
		}
	}
	checkManager("sonarr", cfg.Sonarr)
	checkManager("radarr", cfg.Radarr)
	managerIDs := map[string]bool{}
	for _, m := range cfg.Sonarr {
		if m.ID != "" {
			managerIDs[m.ID] = true
		}
	}
	for _, m := range cfg.Radarr {
		if m.ID != "" {
			managerIDs[m.ID] = true
		}
	}
	clientIDs := map[string]bool{}
	sectionIndex := map[string]int{}
	for _, ce := range cfg.Clients() {
		c := ce.Config
		i := sectionIndex[ce.Section]
		sectionIndex[ce.Section]++
		checkCommon(ce.Section, i, &c.ConnectionCommon)
		if c.ID != "" {
			clientIDs[c.ID] = true
		}
		field := ce.Section + "." + c.ID
		if err := c.Password.validateSyntax(); err != nil {
			errf(field+".password", "%v", err)
		}
		switch ce.Section {
		case "qbittorrent":
			if c.Username != "" && c.Password == "" {
				errf(field+".password", "required when username is set")
			}
			if c.Username == "" && c.Password != "" {
				errf(field+".username", "required when password is set")
			}
			if c.Username == "" {
				warnf(field, "no credentials configured; this only works if qBittorrent bypasses authentication for Housekeeparr's address")
			}
			if c.RPCPath != "" {
				errf(field+".rpc_path", "only applies to Transmission and rTorrent")
			}
		case "deluge":
			if c.Username != "" {
				errf(field+".username", "deluge-web authenticates with a password only")
			}
			if c.Password == "" {
				errf(field+".password", "required (the deluge-web password)")
			}
			if c.RPCPath != "" {
				errf(field+".rpc_path", "only applies to Transmission and rTorrent")
			}
		case "transmission", "rtorrent":
			if c.RPCPath != "" && !strings.HasPrefix(c.RPCPath, "/") {
				errf(field+".rpc_path", "must start with /")
			}
			if c.Password != "" && c.Username == "" {
				warnf(field, "a password without a username is sent with an empty user name")
			}
		}
		if _, err := cfg.EffectivePolicy(c.ID); err != nil {
			errf(field+".policy", "%v", err)
		}
	}
	for section, list := range map[string][]ManagerConfig{"sonarr": cfg.Sonarr, "radarr": cfg.Radarr} {
		for _, m := range list {
			for name, target := range m.DownloadClients {
				if !clientIDs[target] {
					errf(section+"."+m.ID+".download_clients", "%q maps to unknown client connection %q", name, target)
				}
			}
		}
	}
	if _, err := cfg.EffectivePolicy(""); err != nil {
		errf("policy", "%v", err)
	}
	validateRules(errf, cfg, clientIDs, managerIDs)
	validateOverrides(errf, cfg, clientIDs, managerIDs)
	validateExclusions(errf, cfg, clientIDs, managerIDs)
	if len(cfg.Sonarr) == 0 && len(cfg.Radarr) == 0 && len(cfg.Clients()) == 0 {
		warnf("", "no connections are configured; Housekeeparr will have nothing to observe")
	}
	return probs
}

type errFunc func(field, msg string, args ...any)

func validateWorkflowMap(errf errFunc, prefix string, m map[string]bool) {
	if len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !policy.ValidWorkflow(policy.Workflow(k)) {
			errf(prefix+"."+k, "unknown workflow %q", k)
		}
	}
}

func validatePrivateRecovery(errf errFunc, field, v string) {
	if v != "" && !policy.ValidPrivateMode(policy.PrivateMode(v)) {
		errf(field, "must be \"keep_running\", \"stop\", or \"observe\"")
	}
}

// validateUnknownPrivacyMap accepts only workflows for which acting on a
// transfer of unknown privacy is defined (policy.UnknownPrivacyWorkflows).
func validateUnknownPrivacyMap(errf errFunc, prefix string, m map[string]bool) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch wf := policy.Workflow(k); {
		case !policy.ValidWorkflow(wf):
			errf(prefix+"."+k, "unknown workflow %q", k)
		case !policy.UnknownPrivacyDefined(wf):
			errf(prefix+"."+k, "acting on transfers of unknown privacy is not defined for %s (only for metadata_recovery, "+
				"where no client can report privacy before metadata arrives and no payload has been downloaded)", k)
		}
	}
}

func validatePolicyOverrides(errf errFunc, field string, pollInterval time.Duration, o PolicyOverrides) {
	s := policy.DefaultSettings()
	if pollInterval >= time.Minute && pollInterval <= time.Hour {
		s.PollInterval = pollInterval
		s.MaxObservationGap = 2 * pollInterval
	}
	src := map[string]string{}
	apply(&s, src, o, field)
	if err := s.Validate(); err != nil {
		errf(field, "%v", err)
	}
}

func parseScopedRef(s string) (id, key string, ok bool) {
	id, key, ok = strings.Cut(s, "|")
	if !ok || strings.TrimSpace(id) == "" || strings.TrimSpace(key) == "" {
		return "", "", false
	}
	return id, key, true
}

// contentRefHelp explains the content reference format. Release-specific
// keys accepted by earlier builds are rejected rather than ignored: they
// would silently stop matching, and an exclusion that matches nothing is
// a safety regression.
const contentRefHelp = `must be "<manager-id>|episode:<id>" or "<manager-id>|movie:<id>" (the manager's episode or movie ID, ` +
	`shown on the finding page); release-specific keys containing a download ID are no longer accepted`

// parseContentRef parses "<manager-id>|episode:<id>" or "<manager-id>|movie:<id>".
func parseContentRef(s string) (id, key string, ok bool) {
	id, key, ok = parseScopedRef(s)
	if !ok {
		return "", "", false
	}
	if _, _, valid := domain.ParseContentKey(key); !valid {
		return "", "", false
	}
	return id, key, true
}

func isValidPrivacy(p string) bool {
	return p == "public" || p == "private" || p == "unknown"
}

func validateRules(errf errFunc, cfg *Config, clientIDs, managerIDs map[string]bool) {
	names := map[string]string{}
	for i, r := range cfg.Rules {
		field := fmt.Sprintf("rule[%d]", i)
		if strings.TrimSpace(r.Name) == "" {
			errf(field+".name", "required")
		} else if prev, dup := names[r.Name]; dup {
			errf(field+".name", "duplicate rule name %q (also used by %s)", r.Name, prev)
		} else {
			names[r.Name] = field
		}
		if r.Priority == nil {
			errf(field+".priority", "required")
		}
		m := r.Match
		if m.Client == "" && m.Category == "" && m.Manager == "" && m.Privacy == "" {
			errf(field+".match", "at least one match field (client, category, manager, or privacy) is required")
		}
		if m.Category != "" && m.Client == "" {
			errf(field+".match.category", "category requires client to be set")
		}
		if m.Client != "" && !clientIDs[m.Client] {
			errf(field+".match.client", "unknown client connection %q", m.Client)
		}
		if m.Manager != "" && !managerIDs[m.Manager] {
			errf(field+".match.manager", "unknown manager connection %q", m.Manager)
		}
		if m.Privacy != "" && !isValidPrivacy(m.Privacy) {
			errf(field+".match.privacy", "must be \"public\", \"private\", or \"unknown\"")
		}
		validatePolicyOverrides(errf, field+".policy", cfg.Polling.Interval.Duration, r.Policy)
		validateWorkflowMap(errf, field+".automation", r.Automation)
		validateUnknownPrivacyMap(errf, field+".unknown_privacy", r.UnknownPrivacy)
		validatePrivateRecovery(errf, field+".private_recovery", r.PrivateRecovery)
	}
	for i := 0; i < len(cfg.Rules); i++ {
		a := cfg.Rules[i]
		if a.Priority == nil {
			continue
		}
		for j := i + 1; j < len(cfg.Rules); j++ {
			b := cfg.Rules[j]
			if b.Priority == nil || *a.Priority != *b.Priority {
				continue
			}
			if !matchersOverlap(a.Match, b.Match) {
				continue
			}
			if conflicts := ruleConflicts(a, b); len(conflicts) > 0 {
				errf(fmt.Sprintf("rule[%d]", j), "rule %s and rule %s have the same priority (%d) and overlapping matchers with conflicting %s",
					ruleLabel(i, a), ruleLabel(j, b), *a.Priority, strings.Join(conflicts, ", "))
			}
		}
	}
}

func ruleLabel(i int, r RuleConfig) string {
	if strings.TrimSpace(r.Name) != "" {
		return fmt.Sprintf("%q", r.Name)
	}
	return fmt.Sprintf("#%d", i+1)
}

func matchersOverlap(a, b RuleMatch) bool {
	if a.Client != "" && b.Client != "" && a.Client != b.Client {
		return false
	}
	if a.Category != "" && b.Category != "" && a.Category != b.Category {
		return false
	}
	if a.Manager != "" && b.Manager != "" && a.Manager != b.Manager {
		return false
	}
	if a.Privacy != "" && b.Privacy != "" && a.Privacy != b.Privacy {
		return false
	}
	return true
}

func ruleConflicts(a, b RuleConfig) []string {
	var conflicts []string
	ap, bp := a.Policy, b.Policy
	if ap.NewDownloadGrace != nil && bp.NewDownloadGrace != nil && ap.NewDownloadGrace.Duration != bp.NewDownloadGrace.Duration {
		conflicts = append(conflicts, "policy.new_download_grace")
	}
	if ap.NoProgressWindow != nil && bp.NoProgressWindow != nil && ap.NoProgressWindow.Duration != bp.NoProgressWindow.Duration {
		conflicts = append(conflicts, "policy.no_progress_window")
	}
	if ap.NearCompleteRatio != nil && bp.NearCompleteRatio != nil && *ap.NearCompleteRatio != *bp.NearCompleteRatio {
		conflicts = append(conflicts, "policy.near_complete_ratio")
	}
	if ap.NearCompleteWindow != nil && bp.NearCompleteWindow != nil && ap.NearCompleteWindow.Duration != bp.NearCompleteWindow.Duration {
		conflicts = append(conflicts, "policy.near_complete_window")
	}
	if ap.MinObservations != nil && bp.MinObservations != nil && *ap.MinObservations != *bp.MinObservations {
		conflicts = append(conflicts, "policy.min_observations")
	}
	if ap.MaxObservationGap != nil && bp.MaxObservationGap != nil && ap.MaxObservationGap.Duration != bp.MaxObservationGap.Duration {
		conflicts = append(conflicts, "policy.max_observation_gap")
	}
	if ap.MetadataTimeout != nil && bp.MetadataTimeout != nil && ap.MetadataTimeout.Duration != bp.MetadataTimeout.Duration {
		conflicts = append(conflicts, "policy.metadata_timeout")
	}
	if ap.ImportSettle != nil && bp.ImportSettle != nil && ap.ImportSettle.Duration != bp.ImportSettle.Duration {
		conflicts = append(conflicts, "policy.import_settle")
	}
	if ap.MinProgressBytes != nil && bp.MinProgressBytes != nil && *ap.MinProgressBytes != *bp.MinProgressBytes {
		conflicts = append(conflicts, "policy.min_progress_bytes")
	}
	for _, wf := range policy.AllWorkflows() {
		k := string(wf)
		av, aok := a.Automation[k]
		bv, bok := b.Automation[k]
		if aok && bok && av != bv {
			conflicts = append(conflicts, "automation."+k)
		}
		av, aok = a.UnknownPrivacy[k]
		bv, bok = b.UnknownPrivacy[k]
		if aok && bok && av != bv {
			conflicts = append(conflicts, "unknown_privacy."+k)
		}
	}
	if a.PrivateRecovery != "" && b.PrivateRecovery != "" && a.PrivateRecovery != b.PrivateRecovery {
		conflicts = append(conflicts, "private_recovery")
	}
	return conflicts
}

func validateOverrides(errf errFunc, cfg *Config, clientIDs, managerIDs map[string]bool) {
	seenTransfers := map[string]string{}
	seenContents := map[string]string{}
	for i, o := range cfg.Overrides {
		field := fmt.Sprintf("override[%d]", i)
		if strings.TrimSpace(o.Reason) == "" {
			errf(field+".reason", "required")
		}
		switch {
		case o.Transfer == "" && o.Content == "":
			errf(field, "must set either transfer or content")
		case o.Transfer != "" && o.Content != "":
			errf(field, "cannot set both transfer and content")
		case o.Transfer != "":
			cid, nid, ok := parseScopedRef(o.Transfer)
			if !ok {
				errf(field+".transfer", "must be \"<client-id>|<native-id>\" with non-empty parts")
			} else {
				if !clientIDs[cid] {
					errf(field+".transfer", "unknown client connection %q", cid)
				}
				key := cid + "|" + nid
				if prev, dup := seenTransfers[key]; dup {
					errf(field+".transfer", "duplicate override for transfer %q (also defined at %s)", key, prev)
				} else {
					seenTransfers[key] = field
				}
			}
		case o.Content != "":
			mid, ckey, ok := parseContentRef(o.Content)
			if !ok {
				errf(field+".content", contentRefHelp)
			} else {
				if !managerIDs[mid] {
					errf(field+".content", "unknown manager connection %q", mid)
				}
				key := mid + "|" + ckey
				if prev, dup := seenContents[key]; dup {
					errf(field+".content", "duplicate override for content %q (also defined at %s)", key, prev)
				} else {
					seenContents[key] = field
				}
			}
		}
		validatePolicyOverrides(errf, field+".policy", cfg.Polling.Interval.Duration, o.Policy)
		validateWorkflowMap(errf, field+".automation", o.Automation)
		validateUnknownPrivacyMap(errf, field+".unknown_privacy", o.UnknownPrivacy)
		validatePrivateRecovery(errf, field+".private_recovery", o.PrivateRecovery)
	}
}

func validateExclusions(errf errFunc, cfg *Config, clientIDs, managerIDs map[string]bool) {
	for i, ex := range cfg.Exclusions {
		field := fmt.Sprintf("exclude[%d]", i)
		if strings.TrimSpace(ex.Reason) == "" {
			errf(field+".reason", "required")
		}
		if ex.Category != "" && ex.Client == "" {
			errf(field+".category", "category requires client to be set")
		}
		targets := 0
		if ex.Client != "" {
			targets++
		}
		if ex.Transfer != "" {
			targets++
		}
		if ex.Content != "" {
			targets++
		}
		if ex.Manager != "" {
			targets++
		}
		switch {
		case targets == 0 && ex.Category == "":
			errf(field, "must set exactly one of client, transfer, content, or manager")
		case targets > 1:
			errf(field, "must set only one of client (optionally with category), transfer, content, or manager")
		}
		if ex.Client != "" && !clientIDs[ex.Client] {
			errf(field+".client", "unknown client connection %q", ex.Client)
		}
		if ex.Manager != "" && !managerIDs[ex.Manager] {
			errf(field+".manager", "unknown manager connection %q", ex.Manager)
		}
		if ex.Transfer != "" {
			cid, _, ok := parseScopedRef(ex.Transfer)
			if !ok {
				errf(field+".transfer", "must be \"<client-id>|<native-id>\" with non-empty parts")
			} else if !clientIDs[cid] {
				errf(field+".transfer", "unknown client connection %q", cid)
			}
		}
		if ex.Content != "" {
			mid, _, ok := parseContentRef(ex.Content)
			if !ok {
				errf(field+".content", contentRefHelp)
			} else if !managerIDs[mid] {
				errf(field+".content", "unknown manager connection %q", mid)
			}
		}
		for _, wf := range ex.Workflows {
			if !policy.ValidWorkflow(policy.Workflow(wf)) {
				errf(field+".workflows", "unknown workflow %q", wf)
			}
		}
	}
}

// isLoopbackHost reports whether host is a loopback literal or "localhost".
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isLocalNetworkHost applies the documented trusted-host assumption: plain
// HTTP is accepted without acknowledgement only for loopback, private
// (RFC 1918 / ULA), link-local addresses, single-label names (such as
// container service names), and local-only suffixes.
func isLocalNetworkHost(host string) bool {
	if isLoopbackHost(host) {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.Contains(h, ".") {
		return true
	}
	for _, suf := range []string{".local", ".lan", ".home.arpa", ".internal", ".localhost"} {
		if strings.HasSuffix(h, suf) {
			return true
		}
	}
	return false
}

// EffectivePolicy resolves diagnosis settings for a client connection (or
// the global scope when clientID is empty): built-in proposed defaults,
// then [policy], then the client's own [<section>.<id>.policy], plus any
// matching global/client-scoped rules.
func (c *Config) EffectivePolicy(clientID string) (policy.Effective, error) {
	return c.Resolve(policy.Subject{ClientID: clientID})
}

// Resolve evaluates the effective policy, automation permissions, and matching
// exclusions for sub following the precedence order:
// built-in defaults -> [policy] / [automation.workflows] ->
// connection [<section>.<id>.policy] -> matching rules (ascending priority) ->
// item override.
func (c *Config) Resolve(sub policy.Subject) (policy.Effective, error) {
	if sub.Privacy != "" && !isValidPrivacy(sub.Privacy) {
		return policy.Effective{}, fmt.Errorf("invalid subject privacy %q", sub.Privacy)
	}
	if sub.ManagerID != "" && !c.hasManager(sub.ManagerID) {
		return policy.Effective{}, fmt.Errorf("unknown manager connection %q", sub.ManagerID)
	}

	s := policy.DefaultSettings()
	s.PollInterval = c.Polling.Interval.Duration
	// The maximum gap follows the polling interval unless explicitly set.
	s.MaxObservationGap = 2 * s.PollInterval
	const builtin = "built-in proposed default"
	src := map[string]string{}
	for _, k := range settingNames {
		src[k] = builtin
	}
	src["poll_interval"] = "config [polling].interval"
	src["max_observation_gap"] = builtin + " (2 × polling interval)"

	workflows := policy.AllWorkflows()
	autoEnabled := make(map[policy.Workflow]bool, len(workflows))
	autoFrom := make(map[policy.Workflow]string, len(workflows))
	privEnabled := make(map[policy.Workflow]bool, len(workflows))
	privFrom := make(map[policy.Workflow]string, len(workflows))
	for _, wf := range workflows {
		autoEnabled[wf], privEnabled[wf] = false, policy.UnknownPrivacyDefined(wf)
		autoFrom[wf], privFrom[wf] = "built-in default", "built-in default (no payload exists before metadata)"
	}

	apply(&s, src, c.Policy, "config [policy]")
	applyWorkflows(autoEnabled, autoFrom, c.Automation.Workflows, "config [automation.workflows]")
	applyWorkflows(privEnabled, privFrom, c.Automation.UnknownPrivacy, "config [automation.unknown_privacy]")
	privMode, privModeFrom := policy.PrivateKeepRunning, "built-in default"
	setPrivate := func(v, from string) {
		if v != "" {
			privMode, privModeFrom = policy.PrivateMode(v), from
		}
	}
	setPrivate(c.Automation.PrivateRecovery, "config [automation].private_recovery")

	if sub.ClientID != "" {
		found := false
		for _, ce := range c.Clients() {
			if ce.Config.ID == sub.ClientID {
				apply(&s, src, ce.Config.Policy, fmt.Sprintf("config [%s.%s.policy]", ce.Section, ce.Config.ID))
				found = true
			}
		}
		if !found {
			return policy.Effective{}, fmt.Errorf("unknown client connection %q", sub.ClientID)
		}
	}

	var matching []RuleConfig
	for _, r := range c.Rules {
		if r.Priority != nil && ruleMatches(r.Match, sub) {
			matching = append(matching, r)
		}
	}
	sort.SliceStable(matching, func(i, j int) bool {
		return *matching[i].Priority < *matching[j].Priority
	})
	for _, r := range matching {
		from := fmt.Sprintf("config rule %q (priority %d)", r.Name, *r.Priority)
		apply(&s, src, r.Policy, from)
		applyWorkflows(autoEnabled, autoFrom, r.Automation, from)
		applyWorkflows(privEnabled, privFrom, r.UnknownPrivacy, from)
		setPrivate(r.PrivateRecovery, from)
	}

	if sub.ManagerID != "" && sub.ContentKey != "" {
		target := sub.ManagerID + "|" + sub.ContentKey
		for _, o := range c.Overrides {
			if o.Content == target {
				from := fmt.Sprintf("config override content %s", o.Content)
				apply(&s, src, o.Policy, from)
				applyWorkflows(autoEnabled, autoFrom, o.Automation, from)
				applyWorkflows(privEnabled, privFrom, o.UnknownPrivacy, from)
				setPrivate(o.PrivateRecovery, from)
				break
			}
		}
	}
	if sub.ClientID != "" && sub.NativeID != "" {
		target := sub.ClientID + "|" + sub.NativeID
		for _, o := range c.Overrides {
			if o.Transfer == target {
				from := fmt.Sprintf("config override transfer %s", o.Transfer)
				apply(&s, src, o.Policy, from)
				applyWorkflows(autoEnabled, autoFrom, o.Automation, from)
				applyWorkflows(privEnabled, privFrom, o.UnknownPrivacy, from)
				setPrivate(o.PrivateRecovery, from)
				break
			}
		}
	}

	if err := s.Validate(); err != nil {
		return policy.Effective{}, err
	}

	var exclusions []policy.ExclusionMatch
	for i, ex := range c.Exclusions {
		if exclusionMatches(ex, sub) {
			exclusions = append(exclusions, policy.ExclusionMatch{
				Workflows: resolveExclusionWorkflows(ex.Workflows),
				Reason:    ex.Reason,
				From:      fmt.Sprintf("config exclude #%d", i+1),
			})
		}
	}

	vals := map[string]string{
		"poll_interval":        s.PollInterval.String(),
		"new_download_grace":   s.NewDownloadGrace.String(),
		"no_progress_window":   s.NoProgressWindow.String(),
		"near_complete_ratio":  strconv.FormatFloat(s.NearCompleteRatio, 'g', -1, 64),
		"near_complete_window": s.NearCompleteWindow.String(),
		"min_observations":     strconv.Itoa(s.MinObservations),
		"max_observation_gap":  s.MaxObservationGap.String(),
		"metadata_timeout":     s.MetadataTimeout.String(),
		"import_settle":        s.ImportSettle.String(),
		"min_progress_bytes":   strconv.FormatInt(s.MinProgressBytes, 10),
	}
	eff := policy.Effective{
		ID:            subjectID(sub),
		Mode:          string(c.Mode),
		EmergencyStop: c.Automation.EmergencyStop,
		Settings:      s,
		Exclusions:    exclusions,
	}
	for _, k := range append([]string{"poll_interval"}, settingNames...) {
		eff.Sources = append(eff.Sources, policy.Source{Setting: k, Value: vals[k], From: src[k]})
	}
	for _, wf := range workflows {
		eff.Automation = append(eff.Automation, policy.WorkflowPermission{
			Workflow: wf,
			Enabled:  autoEnabled[wf],
			From:     autoFrom[wf],
		})
		if policy.UnknownPrivacyDefined(wf) {
			eff.UnknownPrivacy = append(eff.UnknownPrivacy, policy.WorkflowPermission{Workflow: wf, Enabled: privEnabled[wf], From: privFrom[wf]})
			eff.Sources = append(eff.Sources, policy.Source{Setting: "unknown_privacy." + string(wf), Value: strconv.FormatBool(privEnabled[wf]), From: privFrom[wf]})
		}
	}
	eff.PrivateRecovery, eff.PrivateRecoveryFrom = privMode, privModeFrom
	eff.Sources = append(eff.Sources, policy.Source{Setting: "private_recovery", Value: string(privMode), From: privModeFrom})
	eff.Revision = eff.ComputeRevision()
	return eff, nil
}

// ResolveTransfer resolves the effective policy for a transfer subject
// (see policy.TransferSubject), dropping references to connections that are
// no longer configured: a removed manager resolves without its content
// scope, a removed client without any scope. It returns the subject that was
// resolved, for the action gate to re-resolve before every write.
func (c *Config) ResolveTransfer(sub policy.Subject) (policy.Effective, policy.Subject, error) {
	if sub.ManagerID != "" && !c.HasManager(sub.ManagerID) {
		sub.ManagerID, sub.ContentKey = "", ""
	}
	if sub.ClientID != "" && !c.HasClient(sub.ClientID) {
		sub = policy.Subject{}
	}
	eff, err := c.Resolve(sub)
	return eff, sub, err
}

// ScopedSettings reports whether any rule or item override changes
// diagnosis settings, so that diagnosis must resolve each transfer's own
// subject rather than its client's policy.
func (c *Config) ScopedSettings() bool {
	for _, r := range c.Rules {
		if r.Policy != (PolicyOverrides{}) {
			return true
		}
	}
	for _, o := range c.Overrides {
		if o.Policy != (PolicyOverrides{}) {
			return true
		}
	}
	return false
}

// HasManager reports whether a manager connection with id is configured.
func (c *Config) HasManager(id string) bool { return c.hasManager(id) }

// HasClient reports whether a client connection with id is configured.
func (c *Config) HasClient(id string) bool {
	for _, ce := range c.Clients() {
		if ce.Config.ID == id {
			return true
		}
	}
	return false
}

// ManagersFor returns the managers that could own a transfer in clientID:
// those whose download_clients mapping names it, and those with no
// mapping at all (their rows are correlated by hash alone).
func (c *Config) ManagersFor(clientID string) []string {
	var out []string
	for _, list := range [][]ManagerConfig{c.Sonarr, c.Radarr} {
		for _, m := range list {
			uses := len(m.DownloadClients) == 0
			for _, id := range m.DownloadClients {
				uses = uses || id == clientID
			}
			if uses {
				out = append(out, m.ID)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (c *Config) hasManager(id string) bool {
	for _, m := range c.Sonarr {
		if m.ID == id {
			return true
		}
	}
	for _, m := range c.Radarr {
		if m.ID == id {
			return true
		}
	}
	return false
}

func subjectID(sub policy.Subject) string {
	var parts []string
	if sub.ClientID != "" && sub.NativeID != "" {
		parts = append(parts, "transfer:"+sub.ClientID+"|"+sub.NativeID)
	} else if sub.ClientID != "" {
		parts = append(parts, "client:"+sub.ClientID)
	}
	if sub.Category != "" {
		parts = append(parts, "category:"+sub.Category)
	}
	if sub.ManagerID != "" && sub.ContentKey != "" {
		parts = append(parts, "content:"+sub.ManagerID+"|"+sub.ContentKey)
	} else if sub.ManagerID != "" {
		parts = append(parts, "manager:"+sub.ManagerID)
	}
	if sub.Privacy != "" {
		parts = append(parts, "privacy:"+sub.Privacy)
	}
	if len(parts) == 0 {
		return "global"
	}
	return strings.Join(parts, ",")
}

func ruleMatches(m RuleMatch, sub policy.Subject) bool {
	if m.Client != "" && sub.ClientID != m.Client {
		return false
	}
	if m.Category != "" && (sub.ClientID != m.Client || sub.Category != m.Category) {
		return false
	}
	if m.Manager != "" && sub.ManagerID != m.Manager {
		return false
	}
	if m.Privacy != "" && sub.Privacy != m.Privacy {
		return false
	}
	return true
}

func exclusionMatches(ex ExclusionConfig, sub policy.Subject) bool {
	switch {
	case ex.Transfer != "":
		return sub.ClientID != "" && sub.NativeID != "" && (sub.ClientID+"|"+sub.NativeID) == ex.Transfer
	case ex.Content != "":
		return sub.ManagerID != "" && sub.ContentKey != "" && (sub.ManagerID+"|"+sub.ContentKey) == ex.Content
	case ex.Manager != "":
		return sub.ManagerID == ex.Manager
	case ex.Client != "" && ex.Category != "":
		return sub.ClientID == ex.Client && sub.Category == ex.Category
	case ex.Client != "":
		return sub.ClientID == ex.Client
	default:
		return false
	}
}

func resolveExclusionWorkflows(raw []string) []policy.Workflow {
	if len(raw) == 0 {
		return policy.AllWorkflows()
	}
	seen := make(map[policy.Workflow]bool, len(raw))
	var out []policy.Workflow
	for _, s := range raw {
		wf := policy.Workflow(s)
		if !seen[wf] {
			seen[wf] = true
			out = append(out, wf)
		}
	}
	return out
}

func applyWorkflows(enabled map[policy.Workflow]bool, fromMap map[policy.Workflow]string, m map[string]bool, from string) {
	for _, wf := range policy.AllWorkflows() {
		if v, ok := m[string(wf)]; ok {
			enabled[wf] = v
			fromMap[wf] = from
		}
	}
}

var settingNames = []string{
	"new_download_grace", "no_progress_window", "near_complete_ratio", "near_complete_window",
	"min_observations", "max_observation_gap", "metadata_timeout", "import_settle", "min_progress_bytes",
}

func apply(s *policy.Settings, src map[string]string, o PolicyOverrides, from string) {
	if o.NewDownloadGrace != nil {
		s.NewDownloadGrace, src["new_download_grace"] = o.NewDownloadGrace.Duration, from
	}
	if o.NoProgressWindow != nil {
		s.NoProgressWindow, src["no_progress_window"] = o.NoProgressWindow.Duration, from
	}
	if o.NearCompleteRatio != nil {
		s.NearCompleteRatio, src["near_complete_ratio"] = *o.NearCompleteRatio, from
	}
	if o.NearCompleteWindow != nil {
		s.NearCompleteWindow, src["near_complete_window"] = o.NearCompleteWindow.Duration, from
	}
	if o.MinObservations != nil {
		s.MinObservations, src["min_observations"] = *o.MinObservations, from
	}
	if o.MaxObservationGap != nil {
		s.MaxObservationGap, src["max_observation_gap"] = o.MaxObservationGap.Duration, from
	}
	if o.MetadataTimeout != nil {
		s.MetadataTimeout, src["metadata_timeout"] = o.MetadataTimeout.Duration, from
	}
	if o.ImportSettle != nil {
		s.ImportSettle, src["import_settle"] = o.ImportSettle.Duration, from
	}
	if o.MinProgressBytes != nil {
		s.MinProgressBytes, src["min_progress_bytes"] = *o.MinProgressBytes, from
	}
}

// computeRevision digests the configuration. Secret references (not
// values) are included, so rotating a reference changes the revision.
func (c *Config) computeRevision() string {
	cp := *c
	cp.Path, cp.Revision = "", ""
	b, _ := json.Marshal(cp)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// ResolvedSecrets resolves every secret reference, returning per-field
// errors and warnings without ever including secret values.
func (c *Config) ResolvedSecrets(lookupEnv func(string) (string, bool)) (map[string]string, []Problem) {
	out := map[string]string{}
	var probs []Problem
	resolve := func(field string, r SecretRef) {
		if r == "" {
			return
		}
		v, warns, err := r.Resolve(lookupEnv)
		for _, w := range warns {
			probs = append(probs, Problem{"warning", field, string(w)})
		}
		if err != nil {
			probs = append(probs, Problem{"error", field, err.Error()})
			return
		}
		out[field] = v
	}
	for _, m := range c.Sonarr {
		resolve("sonarr."+m.ID+".api_key", m.APIKey)
	}
	for _, m := range c.Radarr {
		resolve("radarr."+m.ID+".api_key", m.APIKey)
	}
	for _, ce := range c.Clients() {
		resolve(ce.Section+"."+ce.Config.ID+".password", ce.Config.Password)
	}
	return out, probs
}

// ErrNoConfig is returned when no configuration file can be located.
var ErrNoConfig = errors.New("no configuration file found")

// Locate returns the configuration path from an explicit flag, the
// HOUSEKEEPARR_CONFIG environment variable, or ./housekeeparr.toml.
func Locate(flagPath string) (string, error) {
	if flagPath != "" {
		return flagPath, nil
	}
	if p := os.Getenv("HOUSEKEEPARR_CONFIG"); p != "" {
		return p, nil
	}
	if _, err := os.Stat("housekeeparr.toml"); err == nil {
		return "housekeeparr.toml", nil
	}
	return "", fmt.Errorf("%w: pass --config, set HOUSEKEEPARR_CONFIG, or create ./housekeeparr.toml (see housekeeparr.example.toml)", ErrNoConfig)
}

// ClientEntry is one configured download client with its section name.
type ClientEntry struct {
	Section string // "qbittorrent", "transmission"
	Config  *ClientConfig
}

// Clients lists every configured download client.
func (c *Config) Clients() []ClientEntry {
	var out []ClientEntry
	for i := range c.QBittorrent {
		out = append(out, ClientEntry{"qbittorrent", &c.QBittorrent[i]})
	}
	for i := range c.Transmission {
		out = append(out, ClientEntry{"transmission", &c.Transmission[i]})
	}
	for i := range c.Deluge {
		out = append(out, ClientEntry{"deluge", &c.Deluge[i]})
	}
	for i := range c.RTorrent {
		out = append(out, ClientEntry{"rtorrent", &c.RTorrent[i]})
	}
	return out
}
