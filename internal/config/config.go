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
	// ModeAutomatic is reserved; it is rejected until automation exists.
	ModeAutomatic Mode = "automatic"
)

// Config is the validated configuration.
type Config struct {
	Version   int             `toml:"version"`
	Mode      Mode            `toml:"mode"`
	State     StateConfig     `toml:"state"`
	Web       WebConfig       `toml:"web"`
	Polling   PollingConfig   `toml:"polling"`
	Retention RetentionConfig `toml:"retention"`
	Log       LogConfig       `toml:"log"`
	Policy    PolicyOverrides `toml:"policy"`

	Sonarr       []ManagerConfig `toml:"sonarr"`
	Radarr       []ManagerConfig `toml:"radarr"`
	QBittorrent  []ClientConfig  `toml:"qbittorrent"`
	Transmission []ClientConfig  `toml:"transmission"`
	Deluge       []ClientConfig  `toml:"deluge"`

	// Planned integrations: recognised so that configuring them produces a
	// clear "not yet implemented" error instead of an unknown-key error or
	// silent ignorance.
	RTorrent []toml.Primitive `toml:"rtorrent"`

	// Path is the file this configuration was loaded from.
	Path string `toml:"-"`
	// Revision is a digest of the effective (non-secret) configuration.
	Revision string `toml:"-"`
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
	// RPCPath is the Transmission RPC endpoint path (default /transmission/rpc).
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
	if undec := md.Undecoded(); len(undec) > 0 {
		for _, k := range undec {
			key := k.String()
			// Planned sections are reported once below, not per key.
			if top := strings.SplitN(key, ".", 2)[0]; top == "rtorrent" {
				continue
			}
			add("error", key, "unknown configuration key")
		}
	}
	for name, n := range map[string]int{"rtorrent": len(cfg.RTorrent)} {
		if n > 0 {
			add("error", name, "%s is planned for v1 but not implemented in this build; remove the [[%s]] section for now", name, name)
		}
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
	case ModeObserve, ModePaused:
	case ModeAutomatic:
		errf("mode", "controlled automatic mode is not available in this milestone; use \"observe\" or \"paused\"")
	default:
		errf("mode", "must be \"observe\" or \"paused\"")
	}
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
	clientIDs := map[string]bool{}
	sectionIndex := map[string]int{}
	for _, ce := range cfg.Clients() {
		c := ce.Config
		i := sectionIndex[ce.Section]
		sectionIndex[ce.Section]++
		checkCommon(ce.Section, i, &c.ConnectionCommon)
		clientIDs[c.ID] = true
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
				errf(field+".rpc_path", "only applies to Transmission")
			}
		case "deluge":
			if c.Username != "" {
				errf(field+".username", "deluge-web authenticates with a password only")
			}
			if c.Password == "" {
				errf(field+".password", "required (the deluge-web password)")
			}
			if c.RPCPath != "" {
				errf(field+".rpc_path", "only applies to Transmission")
			}
		case "transmission":
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
	if len(cfg.Sonarr) == 0 && len(cfg.Radarr) == 0 && len(cfg.Clients()) == 0 {
		warnf("", "no connections are configured; Housekeeparr will have nothing to observe")
	}
	return probs
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
// then [policy], then the client's own [<section>.<id>.policy].
func (c *Config) EffectivePolicy(clientID string) (policy.Effective, error) {
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
	apply(&s, src, c.Policy, "config [policy]")
	id := "global"
	if clientID != "" {
		found := false
		for _, ce := range c.Clients() {
			if ce.Config.ID == clientID {
				apply(&s, src, ce.Config.Policy, fmt.Sprintf("config [%s.%s.policy]", ce.Section, ce.Config.ID))
				found = true
			}
		}
		if !found {
			return policy.Effective{}, fmt.Errorf("unknown client connection %q", clientID)
		}
		id = "client:" + clientID
	}
	if err := s.Validate(); err != nil {
		return policy.Effective{}, err
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
	eff := policy.Effective{ID: id, Settings: s, Revision: s.Revision()}
	for _, k := range append([]string{"poll_interval"}, settingNames...) {
		eff.Sources = append(eff.Sources, policy.Source{Setting: k, Value: vals[k], From: src[k]})
	}
	return eff, nil
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
	cp.RTorrent = nil
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
	return out
}
