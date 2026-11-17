// Package upgrades models network upgrade votes (SPEC.md "Network
// upgrades"): the parameters of upgrades?mode=set, the parser for the
// arming strings SDF publishes, the query builder, and the checks run
// before anything is sent. It is pure: no I/O, no UI.
package upgrades

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TimeLayout is the only upgradetime format stellar-core takes.
const TimeLayout = "2006-01-02T15:04:05Z"

// Field is one numeric setting.
type Field struct {
	Param string // query parameter, e.g. "basefee"
	Get   string // key in upgrades?mode=get, e.g. "fee"
	Label string // form label
}

// Fields are the numeric settings, in the order SPEC.md lists them.
var Fields = []Field{
	{"protocolversion", "version", "Protocol version"},
	{"basefee", "fee", "Base fee (stroops)"},
	{"basereserve", "reserve", "Base reserve (stroops)"},
	{"maxtxsetsize", "maxtxsize", "Max tx set size (operations)"},
	{"maxsorobantxsetsize", "maxsorobantxsetsize", "Max Soroban tx set size"},
	{"nominationtimeoutlimit", "nominationtimeoutlimit", "Nomination timeout limit"},
	{"expirationminutes", "expirationminutes", "Expiration (minutes)"},
}

// Params are the values of one arming request. Unset values are nil (or
// zero for Time, "" for the key) and left out of the query.
type Params struct {
	Time                time.Time
	Values              map[string]uint32 // by Field.Param
	ConfigUpgradeSetKey string
}

// Get returns a numeric setting.
func (p Params) Get(param string) (uint32, bool) {
	v, ok := p.Values[param]
	return v, ok
}

// Settings counts the settings besides upgradetime and expirationminutes,
// which on their own upgrade nothing.
func (p Params) Settings() int {
	n := 0
	for k := range p.Values {
		if k != "expirationminutes" {
			n++
		}
	}
	if p.ConfigUpgradeSetKey != "" {
		n++
	}
	return n
}

// Query returns the upgrades?mode=set parameters.
func (p Params) Query() url.Values {
	q := url.Values{"mode": {"set"}}
	if !p.Time.IsZero() {
		q.Set("upgradetime", p.Time.UTC().Format(TimeLayout))
	}
	for _, f := range Fields {
		if v, ok := p.Values[f.Param]; ok {
			q.Set(f.Param, strconv.FormatUint(uint64(v), 10))
		}
	}
	if p.ConfigUpgradeSetKey != "" {
		q.Set("configupgradesetkey", p.ConfigUpgradeSetKey)
	}
	return q
}

// String is the request as an operator reads it, in SPEC.md's order, e.g.
// "upgrades?mode=set&upgradetime=2026-11-23T17:00:00Z&protocolversion=30".
func (p Params) String() string {
	parts := []string{"mode=set"}
	if !p.Time.IsZero() {
		parts = append(parts, "upgradetime="+p.Time.UTC().Format(TimeLayout))
	}
	for _, f := range Fields {
		if v, ok := p.Values[f.Param]; ok {
			parts = append(parts, fmt.Sprintf("%s=%d", f.Param, v))
		}
	}
	if p.ConfigUpgradeSetKey != "" {
		parts = append(parts, "configupgradesetkey="+p.ConfigUpgradeSetKey)
	}
	return "upgrades?" + strings.Join(parts, "&")
}

// ParseTime reads an upgradetime.
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(TimeLayout, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, fmt.Errorf("upgradetime %q is not a UTC time like 2026-11-23T17:00:00Z", s)
	}
	return t, nil
}

// ParseUint32 reads a numeric setting.
func ParseUint32(param, s string) (uint32, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a whole number", param, s)
	}
	if n > 1<<32-1 {
		return 0, fmt.Errorf("%s %d is outside uint32 (0 to 4294967295)", param, n)
	}
	return uint32(n), nil
}

// CheckKey checks that a ConfigUpgradeSetKey is base64 XDR of the right
// size: a 32-byte contract ID and a 32-byte content hash.
func CheckKey(key string) error {
	b, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return fmt.Errorf("configupgradesetkey is not valid base64")
	}
	if len(b) != 64 {
		return fmt.Errorf("configupgradesetkey decodes to %d bytes; a ConfigUpgradeSetKey has 64", len(b))
	}
	return nil
}

// Parse reads an arming string as SDF publishes it, for example
// "upgrades?mode=set&upgradetime=2026-09-16T17:00:00Z&protocolversion=28".
// Surrounding text (a curl command, a URL, quotes) is ignored.
func Parse(s string) (Params, error) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "upgrades?"); i >= 0 {
		s = s[i+len("upgrades?"):]
	}
	s = strings.TrimLeft(s, "?")
	if i := strings.IndexAny(s, " \t\r\n'\""); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return Params{}, errors.New("nothing to parse; paste a string like upgrades?mode=set&upgradetime=…&protocolversion=…")
	}
	p := Params{Values: map[string]uint32{}}
	known := map[string]bool{}
	for _, f := range Fields {
		known[f.Param] = true
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(s, "&") {
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return Params{}, fmt.Errorf("%q has no value", part)
		}
		if uv, err := url.QueryUnescape(v); err == nil {
			v = uv
		}
		k = strings.ToLower(k)
		if seen[k] {
			return Params{}, fmt.Errorf("%s appears twice", k)
		}
		seen[k] = true
		switch {
		case k == "mode":
			if v != "set" {
				return Params{}, fmt.Errorf("mode=%s: only mode=set strings can be pasted", v)
			}
		case k == "upgradetime":
			t, err := ParseTime(v)
			if err != nil {
				return Params{}, err
			}
			p.Time = t
		case k == "configupgradesetkey":
			p.ConfigUpgradeSetKey = v
		case known[k]:
			n, err := ParseUint32(k, v)
			if err != nil {
				return Params{}, err
			}
			p.Values[k] = n
		default:
			return Params{}, fmt.Errorf("unknown setting %q", k)
		}
	}
	return p, nil
}

// FromScheduled reads what upgrades?mode=get reports back into Params, to
// confirm a host is armed as asked. The key is reported as present only.
func FromScheduled(s map[string]string) Params {
	p := Params{Values: map[string]uint32{}}
	for _, f := range Fields {
		if v, ok := s[f.Get]; ok {
			if n, err := strconv.ParseUint(strings.Trim(v, `"`), 10, 32); err == nil {
				p.Values[f.Param] = uint32(n)
			}
		}
	}
	if v, ok := s["time"]; ok {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil && sec > 0 {
			p.Time = time.Unix(sec, 0).UTC()
		}
	}
	if v, ok := s["configupgradesetkey"]; ok && v != "" {
		p.ConfigUpgradeSetKey = v
	}
	return p
}

// Matches reports whether scheduled (from FromScheduled) holds every value
// of p. The key only needs to be present.
func (p Params) Matches(scheduled Params) bool {
	if !p.Time.Equal(scheduled.Time) {
		return false
	}
	for k, v := range p.Values {
		if got, ok := scheduled.Values[k]; !ok || got != v {
			return false
		}
	}
	return p.ConfigUpgradeSetKey == "" || scheduled.ConfigUpgradeSetKey != ""
}
