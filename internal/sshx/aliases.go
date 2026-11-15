package sshx

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// maxIncludeDepth matches OpenSSH's limit on nested Include directives.
const maxIncludeDepth = 16

// ListAliases returns the concrete Host names in the ssh config, following
// Include directives. Patterns containing '*', '?' or '!' are skipped
// (SPEC.md "Reading ~/.ssh/config"). Aliases keep file order, without
// duplicates. A missing config file yields no aliases and no error.
func ListAliases(env Env) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	err := listFile(env, env.configPath(), 0, func(alias string) {
		if !seen[alias] {
			seen[alias] = true
			out = append(out, alias)
		}
	})
	if err != nil && os.IsNotExist(err) {
		return nil, nil
	}
	return out, err
}

func listFile(env Env, path string, depth int, add func(string)) error {
	if depth > maxIncludeDepth {
		return fmt.Errorf("%s: Include nested more than %d levels", path, maxIncludeDepth)
	}
	data, err := os.ReadFile(path) //nolint:gosec // reading the operator's own ssh config
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		keyword, args := splitDirective(sc.Text())
		switch keyword {
		case "host":
			for _, a := range args {
				if !strings.ContainsAny(a, "*?!") && validAlias(a) == nil {
					add(a)
				}
			}
		case "include":
			for _, pattern := range args {
				files, err := includeFiles(env, pattern)
				if err != nil {
					return fmt.Errorf("%s: Include %s: %w", path, pattern, err)
				}
				for _, f := range files {
					if err := listFile(env, f, depth+1, add); err != nil && !os.IsNotExist(err) {
						return err
					}
				}
			}
		}
	}
	return sc.Err()
}

// includeFiles resolves an Include argument: ~ expansion, relative paths
// against ~/.ssh, and glob patterns in lexical order, as OpenSSH does for
// user config files.
func includeFiles(env Env, pattern string) ([]string, error) {
	p := expandPath(pattern, env)
	if !filepath.IsAbs(p) {
		p = filepath.Join(env.Home, ".ssh", p)
	}
	matches, err := filepath.Glob(p)
	if err != nil {
		return nil, err
	}
	slices.Sort(matches)
	return matches, nil
}

// splitDirective returns the lower-cased keyword and the arguments of one
// config line, handling "Key=value", comments and double quotes.
func splitDirective(line string) (string, []string) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return "", nil
	}
	var keyword string
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return strings.ToLower(line), nil
	}
	keyword = strings.ToLower(line[:i])
	rest := strings.TrimLeft(line[i:], " \t")
	rest = strings.TrimPrefix(rest, "=")
	var args []string
	var cur strings.Builder
	inQuote, have := false, false
	for _, r := range rest {
		switch {
		case r == '"':
			inQuote = !inQuote
			have = true
		case !inQuote && (r == ' ' || r == '\t'):
			if have {
				args = append(args, cur.String())
				cur.Reset()
				have = false
			}
		case !inQuote && r == '#':
			if have {
				args = append(args, cur.String())
			}
			return keyword, args
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if have {
		args = append(args, cur.String())
	}
	return keyword, args
}
