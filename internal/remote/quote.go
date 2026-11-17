// Package remote runs Auriga's embedded helper scripts on hosts and parses
// their output (SPEC.md "Running commands on a host").
//
// Every remote action is one of the scripts in scripts/, piped to
// `sh -s -- <args>` over stdin. Arguments are allowlist-validated, then
// shell-quoted. Secrets travel only on stdin, never in argv (CLAUDE.md hard
// rule 2).
package remote

import (
	"fmt"
	"regexp"
	"strings"
)

// ArgKind selects the allowlist an argument must match.
type ArgKind int

const (
	// ArgAlias is a host alias or node alias: letters, digits, '.', '_', '-'.
	ArgAlias ArgKind = iota
	// ArgName is an archive or service name.
	ArgName
	// ArgPath is an absolute path without whitespace, quotes or shell
	// metacharacters, and without ".." segments.
	ArgPath
	// ArgPort is a TCP port number.
	ArgPort
	// ArgWord is a fixed keyword chosen by Auriga (e.g. "keep").
	ArgWord
	// ArgURL is a plain http(s) URL without shell metacharacters.
	ArgURL
	// ArgBase64 is standard base64 (a rendered config, which holds no
	// secret: its NODE_SEED is a marker).
	ArgBase64
	// ArgAdmin is an admin command with a URL-encoded query, e.g.
	// "quorum?compact=false". Only the six allowed commands match.
	ArgAdmin
	// ArgAdminRaw is the same command with its query decoded, for
	// `stellar-core http-command`, which does not decode it.
	ArgAdminRaw
)

var allow = map[ArgKind]*regexp.Regexp{
	ArgAlias:    regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`),
	ArgName:     regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,62}$`),
	ArgPath:     regexp.MustCompile(`^/[A-Za-z0-9._/+-]*$`),
	ArgPort:     regexp.MustCompile(`^([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`),
	ArgWord:     regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`),
	ArgURL:      regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~/-]*)?$`),
	ArgBase64:   regexp.MustCompile(`^[A-Za-z0-9+/]*={0,2}$`),
	ArgAdmin:    regexp.MustCompile(`^(info|quorum|peers|upgrades|sorobaninfo|dumpproposedsettings)(\?[A-Za-z0-9_.%=&-]{0,1000})?$`),
	ArgAdminRaw: regexp.MustCompile(`^(info|quorum|peers|upgrades|sorobaninfo|dumpproposedsettings)(\?[A-Za-z0-9_.=&:+/-]{0,1000})?$`),
}

// MaxConfigArg bounds a base64 argument below Linux's 128 KiB limit for one
// argv string.
const MaxConfigArg = 120_000

// Arg is one validated helper argument.
type Arg struct {
	Kind  ArgKind
	Value string
}

// Validate checks the argument against its allowlist.
func (a Arg) Validate() error {
	re, ok := allow[a.Kind]
	if !ok || !re.MatchString(a.Value) {
		return fmt.Errorf("argument %q is not allowed", a.Value)
	}
	if a.Kind == ArgBase64 && len(a.Value) > MaxConfigArg {
		return fmt.Errorf("argument is %d bytes; the limit is %d", len(a.Value), MaxConfigArg)
	}
	if a.Kind == ArgPath {
		if len(a.Value) > 1023 {
			return fmt.Errorf("path %q is too long", a.Value)
		}
		for _, seg := range strings.Split(a.Value, "/") {
			if seg == ".." || seg == "." {
				return fmt.Errorf("path %q must not contain . or .. segments", a.Value)
			}
		}
	}
	return nil
}

// Quote returns s as one POSIX shell word, single-quoted.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
