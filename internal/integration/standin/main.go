//go:build integration

// Command standin serves synthetic stand-ins for the services a disposable
// Sonarr or Radarr needs in order to add fictional content and grab a
// release, so the integration suite can produce real grab history without
// editing manager databases:
//
//   - an HTTPS metadata server for the managers' hard-coded metadata hosts,
//     with a certificate from a throwaway CA generated at start-up (the
//     harness redirects those hosts to loopback with a private /etc/hosts
//     inside its network namespace and points SSL_CERT_FILE at the CA);
//   - a plain-HTTP Torznab-style indexer on loopback that lists the
//     manifest's managed releases and serves their synthetic .torrent files.
//
// Every response comes from files in the repository or the harness work
// directory. Titles and IDs are fictional. Nothing contacts a real service.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// route maps a metadata request (host, path) to a response file. A path
// ending in "*" matches by prefix. The query string is ignored.
type route struct {
	Host   string `json:"host"`
	Path   string `json:"path"`
	File   string `json:"file"`
	Status int    `json:"status"`
}

// release is a manifest entry the indexer advertises.
type release struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Hash string `json:"hash"`
	Size int64  `json:"size"`
	// Magnet releases are offered only as a magnet link (no .torrent):
	// their metadata can never arrive.
	Magnet  bool `json:"magnet"`
	Managed *struct {
		Kind    string `json:"kind"` // "tv" or "movie"
		TvdbID  int    `json:"tvdbId"`
		TmdbID  int    `json:"tmdbId"`
		ImdbID  string `json:"imdbId"`
		Season  int    `json:"season"`
		Episode int    `json:"episode"`
	} `json:"managed"`
}

func main() {
	tlsAddr := flag.String("tls-addr", "127.0.0.1:443", "metadata HTTPS listen address")
	idxAddr := flag.String("indexer-addr", "", "Torznab indexer listen address (host:port)")
	hosts := flag.String("hosts", "", "comma-separated metadata host names for the certificate")
	caOut := flag.String("ca-out", "", "write the generated CA certificate (PEM) here")
	routesFile := flag.String("routes", "", "metadata routes JSON")
	manifest := flag.String("manifest", "", "managed-release manifest (managed.json)")
	torrents := flag.String("torrents", "", "directory holding <name>.torrent files")
	apiKey := flag.String("apikey", "", "indexer API key the managers must send")
	ready := flag.String("ready", "", "touch this file once both listeners are up")
	flag.Parse()

	cert, caPEM, err := certificates(strings.Split(*hosts, ","))
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*caOut, caPEM, 0o644); err != nil {
		log.Fatal(err)
	}
	routes, base := loadRoutes(*routesFile)
	rels := loadReleases(*manifest)

	meta := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveMeta(w, r, routes, base)
	}), TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second}
	ml, err := net.Listen("tcp", *tlsAddr)
	if err != nil {
		log.Fatal(err)
	}
	il, err := net.Listen("tcp", *idxAddr)
	if err != nil {
		log.Fatal(err)
	}
	idx := &http.Server{Handler: indexer(rels, *torrents, *apiKey, "http://"+il.Addr().String()), ReadHeaderTimeout: 10 * time.Second}
	go func() { log.Fatal(meta.ServeTLS(ml, "", "")) }()
	go func() { log.Fatal(idx.Serve(il)) }()
	if *ready != "" {
		if err := os.WriteFile(*ready, nil, 0o644); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("metadata on https://%s, indexer on http://%s (%d releases)", ml.Addr(), il.Addr(), len(rels))
	select {}
}

func certificates(hosts []string) (tls.Certificate, []byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Housekeeparr integration throwaway CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: hosts[0]}, DNSNames: hosts,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(47 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), nil
}

func loadRoutes(path string) ([]route, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	var rs []route
	if err := json.Unmarshal(b, &rs); err != nil {
		log.Fatalf("%s: %v", path, err)
	}
	return rs, filepath.Dir(path)
}

func loadReleases(path string) []release {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	var all []release
	if err := json.Unmarshal(b, &all); err != nil {
		log.Fatal(err)
	}
	var out []release
	for _, r := range all {
		if r.Managed != nil {
			out = append(out, r)
		}
	}
	return out
}

func serveMeta(w http.ResponseWriter, r *http.Request, routes []route, base string) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	for _, rt := range routes {
		if rt.Host != host {
			continue
		}
		if p, ok := strings.CutSuffix(rt.Path, "*"); ok && strings.HasPrefix(r.URL.Path, p) || rt.Path == r.URL.Path {
			status := rt.Status
			if status == 0 {
				status = http.StatusOK
			}
			var body []byte
			if rt.File != "" {
				var err error
				if body, err = os.ReadFile(filepath.Join(base, rt.File)); err != nil {
					log.Printf("meta %s %s: %v", host, r.URL, err)
					http.Error(w, "stand-in file missing", http.StatusInternalServerError)
					return
				}
			}
			log.Printf("meta %s %s %s -> %d %s", r.Method, host, r.URL, status, rt.File)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return
		}
	}
	log.Printf("meta %s %s %s -> 404 (no route)", r.Method, host, r.URL)
	http.NotFound(w, r)
}

type rss struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Atom    string   `xml:"xmlns:atom,attr"`
	Torznab string   `xml:"xmlns:torznab,attr"`
	Channel struct {
		Title string `xml:"title"`
		Items []item `xml:"item"`
	} `xml:"channel"`
}

type item struct {
	Title     string `xml:"title"`
	GUID      string `xml:"guid"`
	Link      string `xml:"link"`
	Comments  string `xml:"comments"`
	PubDate   string `xml:"pubDate"`
	Size      int64  `xml:"size"`
	Enclosure struct {
		URL    string `xml:"url,attr"`
		Length int64  `xml:"length,attr"`
		Type   string `xml:"type,attr"`
	} `xml:"enclosure"`
	Attrs []attr `xml:"torznab:attr"`
}

type attr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

const capsXML = `<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <server title="Housekeeparr integration stand-in"/>
  <limits max="100" default="100"/>
  <searching>
    <search available="yes" supportedParams="q"/>
    <tv-search available="yes" supportedParams="q,season,ep,tvdbid"/>
    <movie-search available="yes" supportedParams="q,tmdbid,imdbid"/>
  </searching>
  <categories>
    <category id="2000" name="Movies"><subcat id="2040" name="Movies/HD"/></category>
    <category id="5000" name="TV"><subcat id="5040" name="TV/HD"/></category>
  </categories>
</caps>`

// indexer serves caps, searches, and .torrent downloads. Searches return
// every managed release of the requested kind that matches the supplied
// identifiers; the manager does its own parsing and decision-making.
func indexer(rels []release, dir, key, base string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		log.Printf("indexer %s", sanitize(r.URL.Query()))
		if q.Get("apikey") != key {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><error code="100" description="Incorrect user credentials"/>`)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		t := q.Get("t")
		if t == "caps" {
			fmt.Fprint(w, capsXML)
			return
		}
		var feed rss
		feed.Version, feed.Atom, feed.Torznab = "2.0", "http://www.w3.org/2005/Atom", "http://torznab.com/schemas/2015/feed"
		feed.Channel.Title = "Housekeeparr integration stand-in"
		for _, rel := range rels {
			if !matches(rel, t, q) {
				continue
			}
			feed.Channel.Items = append(feed.Channel.Items, toItem(rel, base, key))
		}
		b, _ := xml.MarshalIndent(feed, "", " ")
		fmt.Fprint(w, xml.Header)
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("indexer download %s", r.URL.Path)
		if r.URL.Query().Get("apikey") != key {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		k := strings.TrimPrefix(r.URL.Path, "/dl/")
		for _, rel := range rels {
			if rel.Key == k {
				w.Header().Set("Content-Type", "application/x-bittorrent")
				http.ServeFile(w, r, filepath.Join(dir, rel.Name+".torrent"))
				return
			}
		}
		http.NotFound(w, r)
	})
	return mux
}

func matches(rel release, t string, q map[string][]string) bool {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	m := rel.Managed
	switch t {
	case "tvsearch":
		if m.Kind != "tv" {
			return false
		}
		if v := get("tvdbid"); v != "" && v != strconv.Itoa(m.TvdbID) {
			return false
		}
		if v := get("season"); v != "" && v != strconv.Itoa(m.Season) {
			return false
		}
		if v := get("ep"); v != "" && v != strconv.Itoa(m.Episode) {
			return false
		}
		return true
	case "movie":
		if m.Kind != "movie" {
			return false
		}
		if v := get("tmdbid"); v != "" && v != strconv.Itoa(m.TmdbID) {
			return false
		}
		if v := strings.TrimPrefix(get("imdbid"), "tt"); v != "" && v != strings.TrimPrefix(m.ImdbID, "tt") {
			return false
		}
		return true
	case "search":
		return true // RSS sync and free-text: list everything
	}
	return false
}

func toItem(rel release, base, key string) item {
	var it item
	it.Title = rel.Name
	it.GUID = base + "/details/" + rel.Key
	it.Comments = it.GUID
	it.Link = base + "/dl/" + rel.Key + "?apikey=" + key
	it.PubDate = time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC1123Z)
	it.Size = rel.Size
	it.Enclosure.URL, it.Enclosure.Length, it.Enclosure.Type = it.Link, rel.Size, "application/x-bittorrent"
	if rel.Magnet {
		// The tracker is a closed loopback port (as in every fixture) and
		// DHT is off, so nothing can supply metadata. Released Sonarr and
		// Radarr refuse trackerless magnets when the client's DHT is off.
		it.Link = "magnet:?xt=urn:btih:" + rel.Hash + "&dn=" + rel.Name + "&tr=" + url.QueryEscape("http://127.0.0.1:9/announce")
		it.Enclosure.URL = it.Link
	}
	cat := "5040"
	if rel.Managed.Kind == "movie" {
		cat = "2040"
	}
	it.Attrs = []attr{{"category", cat}, {"size", strconv.FormatInt(rel.Size, 10)}, {"seeders", "1"}, {"peers", "1"},
		{"infohash", rel.Hash}, {"downloadvolumefactor", "1"}, {"uploadvolumefactor", "1"}}
	if rel.Magnet {
		it.Attrs = append(it.Attrs, attr{"magneturl", it.Link})
	}
	if m := rel.Managed; m.Kind == "tv" {
		it.Attrs = append(it.Attrs, attr{"tvdbid", strconv.Itoa(m.TvdbID)}, attr{"season", strconv.Itoa(m.Season)}, attr{"episode", strconv.Itoa(m.Episode)})
	} else {
		it.Attrs = append(it.Attrs, attr{"tmdbid", strconv.Itoa(m.TmdbID)})
		if m.ImdbID != "" {
			it.Attrs = append(it.Attrs, attr{"imdb", strings.TrimPrefix(m.ImdbID, "tt")})
		}
	}
	return it
}

func sanitize(q map[string][]string) string {
	parts := []string{}
	for k, v := range q {
		if k == "apikey" {
			v = []string{"REDACTED"}
		}
		parts = append(parts, k+"="+strings.Join(v, ","))
	}
	return strings.Join(parts, "&")
}
