package stellarcfg

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// ParseError is a config that is not valid TOML, or has an owned key of the
// wrong type. Line and Column are 1-based (SPEC.md "Errors": file, line and
// column).
type ParseError struct {
	Line, Column int
	Key          string
	Msg          string
}

func (e *ParseError) Error() string {
	if e.Key != "" {
		return fmt.Sprintf("line %d, column %d: %s: %s", e.Line, e.Column, e.Key, e.Msg)
	}
	return fmt.Sprintf("line %d, column %d: %s", e.Line, e.Column, e.Msg)
}

// Result is a parsed config and what the model could not represent.
type Result struct {
	Config *Config
	// Unsupported lists features the model does not cover. A config with
	// any is read-only: monitored and armed, never rewritten.
	Unsupported []string
	// Comments is the number of comment lines a render would drop.
	Comments int
}

// Marker forms of NODE_SEED.
const (
	redactedPrefix = "<redacted:"
	markerPrefix   = "<auriga-seed:"
)

// Parse reads a config as returned by the read-config helper (NODE_SEED
// redacted on the host) or as rendered by Auriga (NODE_SEED as a seed
// marker). A config holding a plain secret seed is refused: seeds never
// leave the host.
func Parse(text []byte) (*Result, error) {
	// Validate the whole document first, for complete TOML errors with
	// positions (duplicate keys, bad tables, …).
	var probe map[string]any
	if err := toml.Unmarshal(text, &probe); err != nil {
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return nil, &ParseError{Line: row, Column: col, Msg: de.String()}
		}
		return nil, &ParseError{Line: 1, Column: 1, Msg: err.Error()}
	}

	p := &parser{text: text, res: &Result{Config: &Config{}}}
	p.ast.KeepComments = true
	p.ast.Reset(text)
	for p.ast.NextExpression() {
		if err := p.expression(p.ast.Expression()); err != nil {
			return nil, err
		}
	}
	if err := p.ast.Error(); err != nil {
		return nil, &ParseError{Line: 1, Column: 1, Msg: err.Error()}
	}
	p.flush()
	// Comments a render drops: all of them, except those inside entries
	// kept verbatim (e.g. in a multi-line array).
	p.res.Comments = countComments(text)
	for _, e := range p.res.Config.Unknown {
		p.res.Comments -= countComments([]byte(e.Raw))
	}
	return p.res, nil
}

// countComments counts '#' comments outside quoted strings. Multi-line
// strings are rare in stellar-core configs; a '#' inside one may be counted,
// which only affects the adoption note.
func countComments(text []byte) int {
	n := 0
	for _, line := range bytes.Split(text, []byte("\n")) {
		var quote byte
		for i := 0; i < len(line); i++ {
			c := line[i]
			switch {
			case quote != 0 && c == '\\' && quote == '"':
				i++
			case quote != 0 && c == quote:
				quote = 0
			case quote == 0 && (c == '"' || c == '\''):
				quote = c
			case quote == 0 && c == '#':
				n++
				i = len(line)
			}
		}
	}
	return n
}

// section is the table the parser is in.
type section struct {
	kind   unstable.Kind // Invalid = top level
	path   []string
	header string // original header text
	// For unknown tables, the raw lines collected so far.
	raw []string
	// Index into the matching Config slice for modelled tables.
	idx int
	// known is true when the table is one the model manages.
	known bool
}

type parser struct {
	text []byte
	ast  unstable.Parser
	res  *Result
	cur  section
}

func (p *parser) cfg() *Config { return p.res.Config }

func (p *parser) unsupported(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	for _, u := range p.res.Unsupported {
		if u == msg {
			return
		}
	}
	p.res.Unsupported = append(p.res.Unsupported, msg)
}

func (p *parser) raw(r unstable.Range) string { return string(p.ast.Raw(r)) }

func (p *parser) pos(r unstable.Range) (int, int) {
	before := p.text[:min(int(r.Offset), len(p.text))]
	line := bytes.Count(before, []byte("\n")) + 1
	col := int(r.Offset) - bytes.LastIndexByte(before, '\n')
	return line, col
}

func (p *parser) errAt(r unstable.Range, key, format string, args ...any) error {
	line, col := p.pos(r)
	return &ParseError{Line: line, Column: col, Key: key, Msg: fmt.Sprintf(format, args...)}
}

// flush stores an unknown table collected so far.
func (p *parser) flush() {
	if p.cur.kind != unstable.Invalid && !p.cur.known {
		p.cfg().Unknown = append(p.cfg().Unknown, Entry{
			Key:   p.cur.header,
			Raw:   strings.Join(append([]string{p.cur.header}, p.cur.raw...), "\n"),
			Table: true,
		})
	}
}

func keyParts(it unstable.Iterator) []string {
	var parts []string
	for it.Next() {
		parts = append(parts, string(it.Node().Data))
	}
	return parts
}

func (p *parser) expression(e *unstable.Node) error {
	switch e.Kind {
	case unstable.Comment:
		return nil
	case unstable.Table, unstable.ArrayTable:
		p.flush()
		path := keyParts(e.Key())
		return p.openTable(e.Kind, path)
	case unstable.KeyValue:
		return p.keyValue(e)
	}
	return nil
}

func (p *parser) openTable(kind unstable.Kind, path []string) error {
	header := "[" + strings.Join(quotePath(path), ".") + "]"
	if kind == unstable.ArrayTable {
		header = "[" + header + "]"
	}
	p.cur = section{kind: kind, path: path, header: header}
	c := p.cfg()
	switch {
	case kind == unstable.ArrayTable && len(path) == 1 && path[0] == "HOME_DOMAINS":
		c.HomeDomains = append(c.HomeDomains, HomeDomain{})
		p.cur.known, p.cur.idx = true, len(c.HomeDomains)-1
	case kind == unstable.ArrayTable && len(path) == 1 && path[0] == "VALIDATORS":
		c.Validators = append(c.Validators, Validator{})
		p.cur.known, p.cur.idx = true, len(c.Validators)-1
	case kind == unstable.Table && len(path) == 2 && path[0] == "HISTORY":
		c.History = append(c.History, HistoryArchive{Name: path[1]})
		p.cur.known, p.cur.idx = true, len(c.History)-1
	case kind == unstable.Table && len(path) == 1 && path[0] == "HISTORY":
		// A bare [HISTORY] table with sub-keys is an unusual layout.
		p.unsupported("[HISTORY] written as one table instead of [HISTORY.<name>] tables")
	case len(path) > 0 && path[0] == "QUORUM_SET":
		p.unsupported("uses the legacy [QUORUM_SET] instead of [[HOME_DOMAINS]] and [[VALIDATORS]]")
	}
	return nil
}

func (p *parser) keyValue(e *unstable.Node) error {
	parts := keyParts(e.Key())
	rawKV := p.raw(e.Raw)
	v := e.Value()

	if p.cur.kind != unstable.Invalid {
		if !p.cur.known {
			p.cur.raw = append(p.cur.raw, rawKV)
			return nil
		}
		if len(parts) != 1 {
			p.unsupported("dotted key %q inside %s", strings.Join(parts, "."), p.cur.header)
			return nil
		}
		return p.tableKey(e, parts[0], v)
	}

	if len(parts) != 1 {
		if parts[0] == "HISTORY" || parts[0] == "VALIDATORS" || parts[0] == "HOME_DOMAINS" || parts[0] == "QUORUM_SET" {
			p.unsupported("%s written as a dotted key", strings.Join(parts, "."))
		}
		p.cfg().Unknown = append(p.cfg().Unknown, Entry{Key: strings.Join(parts, "."), Raw: rawKV})
		return nil
	}
	return p.topKey(e, parts[0], v, rawKV)
}

func (p *parser) topKey(e *unstable.Node, key string, v *unstable.Node, rawKV string) error {
	c := p.cfg()
	var err error
	switch key {
	case "NETWORK_PASSPHRASE":
		c.NetworkPassphrase, err = p.str(e, key, v)
	case "NODE_SEED":
		var s string
		if s, err = p.str(e, key, v); err == nil {
			c.NodeSeed, err = parseSeed(s)
			if err != nil {
				err = p.errAt(e.Raw, key, "%v", err)
			}
		}
	case "NODE_IS_VALIDATOR":
		var b bool
		b, err = p.boolean(e, key, v)
		c.NodeIsValidator = &b
	case "NODE_HOME_DOMAIN":
		c.NodeHomeDomain, err = p.str(e, key, v)
	case "HTTP_PORT":
		var n int
		n, err = p.integer(e, key, v)
		c.HTTPPort = &n
	case "PUBLIC_HTTP_PORT":
		var b bool
		b, err = p.boolean(e, key, v)
		c.PublicHTTPPort = &b
	case "PEER_PORT":
		var n int
		n, err = p.integer(e, key, v)
		c.PeerPort = &n
	case "DATABASE":
		c.Database, err = p.str(e, key, v)
	case "BUCKET_DIR_PATH":
		c.BucketDirPath, err = p.str(e, key, v)
	case "LOG_FILE_PATH":
		var s string
		s, err = p.str(e, key, v)
		c.LogFilePath = &s
	case "KNOWN_PEERS":
		c.KnownPeers, err = p.strings(e, key, v)
	case "PREFERRED_PEERS":
		c.PreferredPeers, err = p.strings(e, key, v)
	case "PREFERRED_PEER_KEYS":
		c.PreferredPeerKeys, err = p.strings(e, key, v)
	case "UNSAFE_QUORUM":
		var b bool
		b, err = p.boolean(e, key, v)
		c.UnsafeQuorum = &b
	case "HISTORY", "VALIDATORS", "HOME_DOMAINS", "QUORUM_SET":
		p.unsupported("%s written as an inline value", key)
		c.Unknown = append(c.Unknown, Entry{Key: key, Raw: rawKV})
	default:
		c.Unknown = append(c.Unknown, Entry{Key: key, Raw: rawKV})
	}
	return err
}

func (p *parser) tableKey(e *unstable.Node, key string, v *unstable.Node) error {
	c := p.cfg()
	var err error
	str := func(dst *string) { *dst, err = p.str(e, key, v) }
	switch p.cur.path[0] {
	case "HOME_DOMAINS":
		h := &c.HomeDomains[p.cur.idx]
		switch key {
		case "HOME_DOMAIN":
			str(&h.HomeDomain)
		case "QUALITY":
			var s string
			s, err = p.str(e, key, v)
			h.Quality = Quality(s)
		default:
			p.unsupported("[[HOME_DOMAINS]] has key %s", key)
		}
	case "VALIDATORS":
		val := &c.Validators[p.cur.idx]
		switch key {
		case "NAME":
			str(&val.Name)
		case "HOME_DOMAIN":
			str(&val.HomeDomain)
		case "PUBLIC_KEY":
			str(&val.PublicKey)
		case "ADDRESS":
			str(&val.Address)
		case "HISTORY":
			str(&val.History)
		case "QUALITY":
			var s string
			s, err = p.str(e, key, v)
			val.Quality = Quality(s)
		default:
			p.unsupported("[[VALIDATORS]] has key %s", key)
		}
	case "HISTORY":
		h := &c.History[p.cur.idx]
		switch key {
		case "get":
			str(&h.Get)
		case "put":
			str(&h.Put)
		case "mkdir":
			str(&h.Mkdir)
		default:
			p.unsupported("[HISTORY.%s] has key %s", h.Name, key)
		}
	}
	return err
}

func (p *parser) str(e *unstable.Node, key string, v *unstable.Node) (string, error) {
	if v.Kind != unstable.String {
		return "", p.errAt(e.Raw, key, "must be a string")
	}
	return string(v.Data), nil
}

func (p *parser) boolean(e *unstable.Node, key string, v *unstable.Node) (bool, error) {
	if v.Kind != unstable.Bool {
		return false, p.errAt(e.Raw, key, "must be true or false")
	}
	return string(v.Data) == "true", nil
}

func (p *parser) integer(e *unstable.Node, key string, v *unstable.Node) (int, error) {
	if v.Kind != unstable.Integer {
		return 0, p.errAt(e.Raw, key, "must be a whole number")
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(string(v.Data), "_", ""), 0, 64)
	if err != nil || n < 0 || n > 1<<31 {
		return 0, p.errAt(e.Raw, key, "number out of range")
	}
	return int(n), nil
}

func (p *parser) strings(e *unstable.Node, key string, v *unstable.Node) ([]string, error) {
	if v.Kind != unstable.Array {
		return nil, p.errAt(e.Raw, key, "must be a list of strings")
	}
	out := []string{}
	it := v.Children()
	for it.Next() {
		n := it.Node()
		if n.Kind != unstable.String {
			return nil, p.errAt(e.Raw, key, "must be a list of strings")
		}
		out = append(out, string(n.Data))
	}
	return out, nil
}

// parseSeed reads a redacted or marker NODE_SEED value. A plain seed is an
// error: configs reach this machine only with the seed redacted.
func parseSeed(v string) (*Seed, error) {
	val, alias, _ := strings.Cut(v, " ")
	alias = strings.TrimSpace(alias)
	switch {
	case strings.HasPrefix(val, redactedPrefix) && strings.HasSuffix(val, ">"):
		pk := strings.TrimSuffix(strings.TrimPrefix(val, redactedPrefix), ">")
		if pk == "unknown" {
			pk = ""
		}
		return &Seed{Mode: SeedKeep, PublicKey: pk, Alias: alias}, nil
	case strings.HasPrefix(val, markerPrefix) && strings.HasSuffix(val, ">"):
		body := strings.TrimSuffix(strings.TrimPrefix(val, markerPrefix), ">")
		mode, pk, _ := strings.Cut(body, ":")
		switch SeedMode(mode) {
		case SeedKeep, SeedGenerate, SeedImport:
			return &Seed{Mode: SeedMode(mode), PublicKey: pk, Alias: alias}, nil
		}
		return nil, fmt.Errorf("unknown seed marker %q", mode)
	case strings.HasPrefix(val, "S") && len(val) == 56:
		return nil, errors.New("contains a plain secret seed; configs must be read with the seed redacted on the host")
	}
	return nil, errors.New("value is neither a redacted seed nor a seed marker")
}

// quotePath writes table path parts, quoting those that are not bare keys.
func quotePath(parts []string) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = key(p)
	}
	return out
}
