package stellarcfg

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// RenderOptions are inputs to Render besides the model. The time is an
// input (not the clock) so the same model and options always give the same
// bytes (SPEC.md "Rendering"; docs/decisions.md Q1).
type RenderOptions struct {
	Version string    // Auriga's version, for the header
	Time    time.Time // shown in the header, in UTC
}

// ErrPublicHTTPPort is returned for PUBLIC_HTTP_PORT=true, which Auriga
// never renders (CLAUDE.md hard rule 4).
var ErrPublicHTTPPort = errors.New("PUBLIC_HTTP_PORT=true would expose the admin endpoint; Auriga never writes it")

// SeedMarker is how NODE_SEED appears in a render: a marker the host helper
// replaces with the real seed, never the seed itself.
func (s Seed) SeedMarker() string {
	m := markerPrefix + string(s.Mode)
	if s.PublicKey != "" {
		m += ":" + s.PublicKey
	}
	m += ">"
	if s.Alias != "" {
		m += " " + s.Alias
	}
	return m
}

// Redacted is how NODE_SEED reads back from the host once a seed with this
// public key is in place, for comparing a staged file with a render.
func (s Seed) Redacted() string {
	pk := s.PublicKey
	if pk == "" {
		pk = "unknown"
	}
	v := redactedPrefix + pk + ">"
	if s.Alias != "" {
		v += " " + s.Alias
	}
	return v
}

// Render writes the config in Auriga's fixed layout: header, network, node
// identity, ports and storage, peers, other top-level keys kept from the
// original, quorum safety, [[HOME_DOMAINS]] by quality then domain, [[VALIDATORS]] by home
// domain then name, [HISTORY.*] by name, then other tables kept from the
// original. TOML requires every plain key before the first table, so kept
// keys come before the tables and kept tables after them.
func Render(c *Config, opt RenderOptions) ([]byte, error) {
	if c.PublicHTTPPort != nil && *c.PublicHTTPPort {
		return nil, ErrPublicHTTPPort
	}
	var w writer
	version := opt.Version
	if version == "" {
		version = "dev"
	}
	w.line("# stellar-core configuration, managed by auriga %s.", version)
	w.line("# Rendered %s. Change it with auriga: hand edits show up as a diff before the next deploy.", opt.Time.UTC().Format(time.RFC3339))

	w.section("Network")
	w.str("NETWORK_PASSPHRASE", c.NetworkPassphrase)

	w.section("Node identity")
	if c.NodeSeed != nil {
		w.kv("NODE_SEED", quote(c.NodeSeed.SeedMarker()))
	}
	w.boolean("NODE_IS_VALIDATOR", c.NodeIsValidator)
	w.str("NODE_HOME_DOMAIN", c.NodeHomeDomain)

	w.section("Ports and storage")
	w.integer("HTTP_PORT", c.HTTPPort)
	if c.PublicHTTPPort != nil {
		w.kv("PUBLIC_HTTP_PORT", "false")
	}
	w.integer("PEER_PORT", c.PeerPort)
	w.str("DATABASE", c.Database)
	w.str("BUCKET_DIR_PATH", c.BucketDirPath)
	if c.LogFilePath != nil {
		w.kv("LOG_FILE_PATH", quote(*c.LogFilePath))
	}

	w.section("Peers")
	w.list("KNOWN_PEERS", c.KnownPeers)
	w.list("PREFERRED_PEERS", c.PreferredPeers)
	w.list("PREFERRED_PEER_KEYS", c.PreferredPeerKeys)

	w.section("Quorum safety")
	w.boolean("UNSAFE_QUORUM", c.UnsafeQuorum)

	w.section("Other settings, kept as they were")
	for _, e := range c.Unknown {
		if !e.Table {
			w.raw(e.Raw)
		}
	}

	homes := append([]HomeDomain(nil), c.HomeDomains...)
	sort.SliceStable(homes, func(i, j int) bool {
		if homes[i].Quality.rank() != homes[j].Quality.rank() {
			return homes[i].Quality.rank() < homes[j].Quality.rank()
		}
		return homes[i].HomeDomain < homes[j].HomeDomain
	})
	for _, h := range homes {
		w.table("[[HOME_DOMAINS]]")
		w.str("HOME_DOMAIN", h.HomeDomain)
		w.str("QUALITY", string(h.Quality))
	}

	vals := append([]Validator(nil), c.Validators...)
	sort.SliceStable(vals, func(i, j int) bool {
		if vals[i].HomeDomain != vals[j].HomeDomain {
			return vals[i].HomeDomain < vals[j].HomeDomain
		}
		return vals[i].Name < vals[j].Name
	})
	for _, v := range vals {
		w.table("[[VALIDATORS]]")
		w.str("NAME", v.Name)
		w.str("HOME_DOMAIN", v.HomeDomain)
		w.str("PUBLIC_KEY", v.PublicKey)
		w.str("ADDRESS", v.Address)
		w.str("HISTORY", v.History)
		w.str("QUALITY", string(v.Quality))
	}

	hist := append([]HistoryArchive(nil), c.History...)
	sort.SliceStable(hist, func(i, j int) bool { return hist[i].Name < hist[j].Name })
	for _, h := range hist {
		w.table("[HISTORY." + key(h.Name) + "]")
		w.str("get", h.Get)
		w.str("put", h.Put)
		w.str("mkdir", h.Mkdir)
	}

	first := true
	for _, e := range c.Unknown {
		if e.Table {
			if first {
				w.blank()
				w.line("# Other tables, kept as they were")
				first = false
			} else {
				w.blank()
			}
			w.raw(e.Raw)
		}
	}
	return []byte(w.String()), nil
}

type writer struct {
	b       strings.Builder
	pending string // section comment waiting for its first line
}

func (w *writer) String() string { return w.b.String() }

func (w *writer) line(format string, args ...any) {
	fmt.Fprintf(&w.b, format+"\n", args...)
}

func (w *writer) blank() { w.b.WriteString("\n") }

// section starts a commented group; the comment appears only if something
// is written in it.
func (w *writer) section(name string) { w.pending = name }

func (w *writer) flushSection() {
	if w.pending != "" {
		w.blank()
		w.line("# %s", w.pending)
		w.pending = ""
	}
}

func (w *writer) table(header string) {
	w.pending = ""
	w.blank()
	w.line("%s", header)
}

func (w *writer) kv(k, v string) {
	w.flushSection()
	w.line("%s = %s", key(k), v)
}

func (w *writer) raw(s string) {
	w.flushSection()
	w.line("%s", s)
}

func (w *writer) str(k, v string) {
	if v != "" {
		w.kv(k, quote(v))
	}
}

func (w *writer) boolean(k string, v *bool) {
	if v != nil {
		w.kv(k, strconv.FormatBool(*v))
	}
}

func (w *writer) integer(k string, v *int) {
	if v != nil {
		w.kv(k, strconv.Itoa(*v))
	}
}

// list writes a string array on one line, or one item per line when long.
func (w *writer) list(k string, v []string) {
	if v == nil {
		return
	}
	items := make([]string, len(v))
	total := 0
	for i, s := range v {
		items[i] = quote(s)
		total += len(items[i]) + 2
	}
	if total+len(k) <= 76 {
		w.kv(k, "["+strings.Join(items, ", ")+"]")
		return
	}
	w.kv(k, "[\n  "+strings.Join(items, ",\n  ")+",\n]")
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// key returns k as a TOML key, quoted when it is not a bare key.
func key(k string) string {
	if bareKey.MatchString(k) {
		return k
	}
	return quote(k)
}

// quote returns s as a TOML basic string.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f || r == utf8.RuneError {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
