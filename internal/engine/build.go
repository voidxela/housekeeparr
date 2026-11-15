package engine

import (
	"fmt"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/qbittorrent"
	"github.com/voidxela/housekeeparr/internal/adapter/sonarr"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/redact"
)

// Connections is the set of live adapters built from configuration.
type Connections struct {
	Clients  []adapter.DownloadClient
	Managers []adapter.MediaManager
	// Meta describes every configured connection, including disabled ones.
	Meta map[string]ConnMeta
}

// ConnMeta carries configuration facts the engine needs for explanations.
type ConnMeta struct {
	ID, Name, BaseURL string
	Kind              adapter.Kind
	Enabled           bool
	SecretRef         string
	DownloadClients   map[string]string // managers only
}

// Build constructs adapters. secrets maps "section.id.field" to resolved values.
func Build(cfg *config.Config, secrets map[string]string) (*Connections, error) {
	out := &Connections{Meta: map[string]ConnMeta{}}
	timeout := cfg.Polling.RequestTimeout.Duration
	for _, m := range cfg.Sonarr {
		meta := ConnMeta{ID: m.ID, Name: m.DisplayName(), BaseURL: redact.URL(m.URL), Kind: adapter.KindSonarr,
			Enabled: m.IsEnabled(), SecretRef: m.APIKey.Display(), DownloadClients: m.DownloadClients}
		out.Meta[m.ID] = meta
		if !meta.Enabled {
			continue
		}
		key, ok := secrets["sonarr."+m.ID+".api_key"]
		if !ok {
			return nil, fmt.Errorf("sonarr.%s: api key secret could not be resolved", m.ID)
		}
		c, err := sonarr.New(sonarr.Options{ID: m.ID, BaseURL: m.URL, APIKey: key, Timeout: timeout, CAFile: m.CAFile,
			PageSize: m.QueuePageSize, MaxPages: m.MaxQueuePages})
		if err != nil {
			return nil, fmt.Errorf("sonarr.%s: %w", m.ID, err)
		}
		out.Managers = append(out.Managers, c)
	}
	for _, q := range cfg.QBittorrent {
		meta := ConnMeta{ID: q.ID, Name: q.DisplayName(), BaseURL: redact.URL(q.URL), Kind: adapter.KindQBittorrent,
			Enabled: q.IsEnabled(), SecretRef: q.Password.Display()}
		out.Meta[q.ID] = meta
		if !meta.Enabled {
			continue
		}
		pw := ""
		if q.Password != "" {
			v, ok := secrets["qbittorrent."+q.ID+".password"]
			if !ok {
				return nil, fmt.Errorf("qbittorrent.%s: password secret could not be resolved", q.ID)
			}
			pw = v
		}
		c, err := qbittorrent.New(qbittorrent.Options{ID: q.ID, BaseURL: q.URL, Username: q.Username, Password: pw, Timeout: timeout, CAFile: q.CAFile})
		if err != nil {
			return nil, fmt.Errorf("qbittorrent.%s: %w", q.ID, err)
		}
		out.Clients = append(out.Clients, c)
	}
	return out, nil
}
