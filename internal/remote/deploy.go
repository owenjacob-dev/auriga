package remote

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/owenjacob-dev/auriga/internal/secret"
	"github.com/owenjacob-dev/auriga/internal/strkey"
)

// The deploy helpers (SPEC.md "Steps per host"). Each wrapper validates its
// arguments and parses the helper's key=value output.

// StagedSuffix is appended to the config path for the staged file.
const StagedSuffix = ".auriga-staged"

// Preflight is what preflight.sh found on a host.
type Preflight struct {
	UnitLoad        string // "loaded" when the unit exists
	Active          string
	ConfigPresent   bool
	ConfigFSAvailKB int64
	WorkDirAvailKB  int64
	StagedLeftover  bool // a staged file from an interrupted deploy
	PendingSeed     bool // a seed from gen-seed not yet installed
}

// Preflight runs preflight.sh.
func (r *Runner) Preflight(ctx context.Context, alias, configPath, service string) (Preflight, error) {
	res, err := r.Run(ctx, alias, HelperPreflight, []Arg{{ArgPath, configPath}, {ArgName, service}}, RunOptions{})
	if err != nil {
		return Preflight{}, err
	}
	kv, _ := parseKV(res.Stdout)
	num := func(k string) int64 { n, _ := strconv.ParseInt(kv[k], 10, 64); return n }
	return Preflight{
		UnitLoad:        kv["unit_load"],
		Active:          kv["active"],
		ConfigPresent:   kv["config_present"] == "yes",
		ConfigFSAvailKB: num("config_fs_avail_kb"),
		WorkDirAvailKB:  num("workdir_avail_kb"),
		StagedLeftover:  kv["staged_leftover"] == "yes",
		PendingSeed:     kv["pending_seed"] == "yes",
	}, nil
}

// GenSeed generates a seed on the host (or reuses the pending one) and
// returns only its public key. The seed stays on the host until Stage
// moves it into the staged config.
func (r *Runner) GenSeed(ctx context.Context, alias, configPath string) (string, error) {
	res, err := r.Run(ctx, alias, HelperGenSeed, []Arg{{ArgPath, configPath}}, RunOptions{})
	if err != nil {
		return "", err
	}
	kv, _ := parseKV(res.Stdout)
	pk := kv["pubkey"]
	if !strkey.ValidPublicKey(pk) {
		return "", fmt.Errorf("%s: gen-seed returned no valid public key", alias)
	}
	return pk, nil
}

// SeedSource says where Stage takes the seed from.
type SeedSource string

const (
	SeedFromConfig  SeedSource = "keep"     // the current config's NODE_SEED
	SeedFromPending SeedSource = "generate" // the seed GenSeed left on the host
	SeedFromStdin   SeedSource = "import"   // an operator paste
	SeedNone        SeedSource = "none"     // the render has no NODE_SEED
)

// Staged is the result of staging a config.
type Staged struct {
	SHA256    string // of the staged file, seed included
	PublicKey string // of the seed put in; empty for SeedNone
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Stage writes rendered to <config>.auriga-staged with the seed
// substituted on the host. For SeedFromStdin, seed is sent on stdin when
// the helper asks for it; it is not modified.
func (r *Runner) Stage(ctx context.Context, alias, configPath string, rendered []byte, src SeedSource, seed *secret.Buffer) (Staged, error) {
	if (src == SeedFromStdin) != (seed != nil) {
		return Staged{}, errors.New("a seed is sent only for an import")
	}
	opt := RunOptions{Secret: seed}
	res, err := r.Run(ctx, alias, HelperStage, []Arg{
		{ArgPath, configPath}, {ArgWord, string(src)}, {ArgBase64, base64.StdEncoding.EncodeToString(rendered)},
	}, opt)
	if err != nil {
		return Staged{}, err
	}
	return parseStaged(alias, res.Stdout, src != SeedNone)
}

func parseStaged(alias string, out []byte, wantKey bool) (Staged, error) {
	kv, _ := parseKV(out)
	s := Staged{SHA256: kv["sha256"], PublicKey: kv["pubkey"]}
	if !sha256Hex.MatchString(s.SHA256) {
		return Staged{}, fmt.Errorf("%s: stage returned no sha256", alias)
	}
	if wantKey && !strkey.ValidPublicKey(s.PublicKey) {
		return Staged{}, fmt.Errorf("%s: the staged seed has no valid public key", alias)
	}
	return s, nil
}

// StageBackup stages a backup for a manual rollback.
func (r *Runner) StageBackup(ctx context.Context, alias, configPath, backupPath string) (string, error) {
	res, err := r.Run(ctx, alias, HelperStageBackup, []Arg{{ArgPath, configPath}, {ArgPath, backupPath}}, RunOptions{})
	if err != nil {
		return "", err
	}
	kv, _ := parseKV(res.Stdout)
	if !sha256Hex.MatchString(kv["sha256"]) {
		return "", fmt.Errorf("%s: stage-backup returned no sha256", alias)
	}
	return kv["sha256"], nil
}

// Backup copies the current config to a timestamped backup and returns its
// path, or "" when there is no config yet.
func (r *Runner) Backup(ctx context.Context, alias, configPath string) (string, error) {
	res, err := r.Run(ctx, alias, HelperBackup, []Arg{{ArgPath, configPath}}, RunOptions{})
	if err != nil {
		return "", err
	}
	kv, _ := parseKV(res.Stdout)
	b, ok := kv["backup"]
	if !ok {
		return "", fmt.Errorf("%s: backup printed no path", alias)
	}
	return b, nil
}

// Install moves the staged config over the config and returns its sha256.
func (r *Runner) Install(ctx context.Context, alias, configPath string) (string, error) {
	res, err := r.Run(ctx, alias, HelperInstall, []Arg{{ArgPath, configPath}}, RunOptions{})
	if err != nil {
		return "", err
	}
	kv, _ := parseKV(res.Stdout)
	return kv["sha256"], nil
}

// InitKind is a one-off initialisation.
type InitKind string

const (
	InitNewDB   InitKind = "new-db"
	InitNewHist InitKind = "new-hist"
)

// Init stops the service and runs new-db, or new-hist for archive.
func (r *Runner) Init(ctx context.Context, alias, configPath, service string, kind InitKind, archive string) error {
	args := []Arg{{ArgPath, configPath}, {ArgName, service}, {ArgWord, string(kind)}}
	switch kind {
	case InitNewDB:
	case InitNewHist:
		args = append(args, Arg{ArgName, archive})
	default:
		return fmt.Errorf("unknown init %q", kind)
	}
	_, err := r.Run(ctx, alias, HelperInit, args, RunOptions{})
	return err
}

// Restart restarts (or starts) the service and returns its active state.
func (r *Runner) Restart(ctx context.Context, alias, service string) (string, error) {
	res, err := r.Run(ctx, alias, HelperRestart, []Arg{{ArgName, service}}, RunOptions{})
	if err != nil {
		return "", err
	}
	kv, _ := parseKV(res.Stdout)
	return kv["active"], nil
}

// Rollback restores backupPath and restarts the service. With no backup
// (a new node), the failed config is moved aside and the service stopped.
func (r *Runner) Rollback(ctx context.Context, alias, configPath, service, backupPath string) (string, error) {
	args := []Arg{{ArgPath, configPath}, {ArgName, service}}
	if backupPath != "" {
		args = append(args, Arg{ArgPath, backupPath})
	}
	res, err := r.Run(ctx, alias, HelperRollback, args, RunOptions{})
	if err != nil {
		return "", err
	}
	kv, _ := parseKV(res.Stdout)
	return kv["active"], nil
}
