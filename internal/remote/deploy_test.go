package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owenjacob-dev/auriga/internal/secret"
)

const (
	genSeed    = "SAAACAQDAQCQMBYIBEFAWDANBYHRAEISCMKBKFQXDAMRUGY4DUPB6NKI" // the shim's gen-seed output
	genAddress = "GAB2CB576PHBBPQ5ODORRZ2LYCMWPZGWGCN2KDK7DXOIMZASKUY3QZ6Q"
)

func render(marker string) []byte {
	s := "# rendered by auriga\nNETWORK_PASSPHRASE = \"Test SDF Network ; September 2015\"\n"
	if marker != "" {
		s += "NODE_SEED = \"<auriga-seed:" + marker + "> acme-a\"\n"
	}
	return []byte(s + "NODE_IS_VALIDATOR = true\nHTTP_PORT = 11626\n# a \\ backslash and 'quotes' survive\n")
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestStageSeedSources(t *testing.T) {
	for _, shell := range shells {
		for _, tc := range []struct {
			name, marker string
			src          SeedSource
			seed         string // stdin, for import
			gen          bool
			wantSeed     string
			wantPK       string
		}{
			{"keep", "keep:" + testAddress, SeedFromConfig, "", false, testSeed, testAddress},
			{"import", "import:" + genAddress, SeedFromStdin, genSeed, false, genSeed, genAddress},
			{"generate", "generate", SeedFromPending, "", true, genSeed, genAddress},
			{"none", "", SeedNone, "", false, "", ""},
		} {
			t.Run(shell+"/"+tc.name, func(t *testing.T) {
				h := newFakeHost(t, shell)
				cfg := writeConfig(t, h, sampleConfig, 0o640)
				r := newDirectRunner(map[string]Conn{"a": h}, nil)
				if tc.gen {
					pk, err := r.GenSeed(ctx(t), "a", cfg)
					if err != nil || pk != genAddress {
						t.Fatalf("gen-seed: %q %v", pk, err)
					}
					// Running it again reuses the pending seed.
					if pk, err := r.GenSeed(ctx(t), "a", cfg); err != nil || pk != genAddress {
						t.Fatalf("second gen-seed: %q %v", pk, err)
					}
					if st, _ := os.Stat(cfg + ".auriga-pending-seed"); st == nil || st.Mode().Perm() != 0o600 {
						t.Fatalf("pending seed file %v", st)
					}
				}
				var buf *secret.Buffer
				if tc.seed != "" {
					buf = secret.FromBytes([]byte(tc.seed))
				}
				st, err := r.Stage(ctx(t), "a", cfg, render(tc.marker), tc.src, buf)
				if err != nil {
					t.Fatal(err)
				}
				staged := readFile(t, cfg+StagedSuffix)
				want := string(render(tc.marker))
				if tc.marker != "" {
					want = strings.Replace(want, "<auriga-seed:"+tc.marker+">", tc.wantSeed, 1)
				}
				if staged != want {
					t.Errorf("staged:\n%s\nwant:\n%s", staged, want)
				}
				if st.PublicKey != tc.wantPK || st.SHA256 != sum(staged) {
					t.Errorf("result %+v", st)
				}
				if fi, _ := os.Stat(cfg + StagedSuffix); fi.Mode().Perm() != 0o640 {
					t.Errorf("staged mode %v", fi.Mode())
				}
				if log := h.argvLog(t); strings.Contains(log, testSeed) || strings.Contains(log, genSeed) ||
					!strings.Contains(log, "chown root:stellar "+cfg+StagedSuffix) {
					t.Errorf("argv log:\n%s", log)
				}
				// Verify staged: the redacted read matches the render with the key.
				rc, err := r.ReadConfig(ctx(t), "a", cfg+StagedSuffix)
				if err != nil {
					t.Fatal(err)
				}
				if tc.marker != "" && !strings.Contains(string(rc.Text), `NODE_SEED = "<redacted:`+tc.wantPK+`> acme-a"`) {
					t.Errorf("redacted staged:\n%s", rc.Text)
				}
			})
		}
	}
}

func TestStageRefusals(t *testing.T) {
	h := newFakeHost(t, "dash")
	r := newDirectRunner(map[string]Conn{"a": h}, nil)
	noSeed := writeConfig(t, h, "HTTP_PORT=11626\n", 0o640)
	var he *Error
	if _, err := r.Stage(ctx(t), "a", noSeed, render("keep:"+testAddress), SeedFromConfig, nil); !errors.As(err, &he) || he.Exit != 6 {
		t.Errorf("keep without a seed: %v", err)
	}
	if _, err := r.Stage(ctx(t), "a", noSeed, render("generate"), SeedFromPending, nil); !errors.As(err, &he) || he.Exit != 6 {
		t.Errorf("generate without gen-seed: %v", err)
	}
	cfg := writeConfig(t, h, sampleConfig, 0o640)
	if _, err := r.Stage(ctx(t), "a", cfg, render(""), SeedFromConfig, nil); !errors.As(err, &he) || he.Exit != 7 {
		t.Errorf("no marker: %v", err)
	}
	if _, err := os.Stat(cfg + StagedSuffix); !os.IsNotExist(err) {
		t.Error("a refused stage left a staged file")
	}
	if _, err := r.Stage(ctx(t), "a", cfg, render("keep"), SeedFromStdin, nil); err == nil {
		t.Error("import without a seed accepted")
	}
	if _, err := r.Stage(ctx(t), "a", cfg, make([]byte, MaxConfigArg), SeedNone, nil); err == nil {
		t.Error("oversized config accepted")
	}
	if _, err := r.Stage(ctx(t), "a", cfg, render("keep"), SeedSource("x; id"), nil); err == nil {
		t.Error("bad seed source accepted")
	}
}

func TestBackupInstallRollback(t *testing.T) {
	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			h := newFakeHost(t, shell)
			cfg := writeConfig(t, h, sampleConfig, 0o640)
			r := newDirectRunner(map[string]Conn{"a": h}, nil)
			// Eleven old backups: the backup keeps the ten newest.
			for i := range 11 {
				old := cfg + ".auriga-2026010" + string(rune('0'+i/10)) + "T0000" + string(rune('0'+i%10)) + "0Z.bak"
				if err := os.WriteFile(old, []byte("old"), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			b, err := r.Backup(ctx(t), "a", cfg)
			if err != nil || !strings.HasPrefix(b, cfg+".auriga-") || readFile(t, b) != sampleConfig {
				t.Fatalf("backup %q %v", b, err)
			}
			if fi, _ := os.Stat(b); fi.Mode().Perm() != 0o640 {
				t.Errorf("backup mode %v", fi.Mode())
			}
			if list, _ := filepath.Glob(cfg + ".auriga-*.bak"); len(list) != 10 {
				t.Errorf("%d backups kept", len(list))
			}

			if _, err := r.Stage(ctx(t), "a", cfg, render("keep:"+testAddress), SeedFromConfig, nil); err != nil {
				t.Fatal(err)
			}
			staged := readFile(t, cfg+StagedSuffix)
			if s, err := r.Install(ctx(t), "a", cfg); err != nil || s != sum(staged) || readFile(t, cfg) != staged {
				t.Fatalf("install %q %v", s, err)
			}
			if _, err := os.Stat(cfg + StagedSuffix); !os.IsNotExist(err) {
				t.Error("staged file left after install")
			}
			if a, err := r.Restart(ctx(t), "a", "stellar-core"); err != nil || a != "active" {
				t.Errorf("restart %q %v", a, err)
			}
			if a, err := r.Rollback(ctx(t), "a", cfg, "stellar-core", b); err != nil || a != "active" || readFile(t, cfg) != sampleConfig {
				t.Errorf("rollback %q %v", a, err)
			}
			if _, err := r.Rollback(ctx(t), "a", cfg, "stellar-core", "/etc/passwd"); err == nil {
				t.Error("rollback from a file that is not a backup")
			}
			if log := h.argvLog(t); strings.Count(log, "systemctl restart stellar-core") != 2 {
				t.Errorf("argv log:\n%s", log)
			}

			// Manual rollback: stage a backup.
			if s, err := r.StageBackup(ctx(t), "a", cfg, b); err != nil || s != sum(sampleConfig) {
				t.Errorf("stage-backup %q %v", s, err)
			}
			if _, err := r.StageBackup(ctx(t), "a", cfg, cfg); err == nil {
				t.Error("staged a file that is not a backup")
			}

			// A new node: no config, no backup; rollback moves the config aside.
			fresh := filepath.Join(h.dir, "new", "stellar-core.cfg")
			os.MkdirAll(filepath.Dir(fresh), 0o755)
			if b, err := r.Backup(ctx(t), "a", fresh); err != nil || b != "" {
				t.Errorf("backup of nothing: %q %v", b, err)
			}
			os.WriteFile(fresh, []byte("bad"), 0o640)
			if a, err := r.Rollback(ctx(t), "a", fresh, "stellar-core", ""); err != nil || a != "stopped" {
				t.Errorf("rollback of a new node %q %v", a, err)
			}
			if _, err := os.Stat(fresh); !os.IsNotExist(err) {
				t.Error("failed config of a new node left in place")
			}
		})
	}
}

func TestGenerateThenInstallConsumesPendingSeed(t *testing.T) {
	h := newFakeHost(t, "dash")
	cfg := writeConfig(t, h, sampleConfig, 0o640)
	r := newDirectRunner(map[string]Conn{"a": h}, nil)
	if _, err := r.GenSeed(ctx(t), "a", cfg); err != nil {
		t.Fatal(err)
	}
	p, _ := r.Preflight(ctx(t), "a", cfg, "stellar-core")
	if !p.PendingSeed || p.StagedLeftover || !p.ConfigPresent || p.UnitLoad != "loaded" || p.ConfigFSAvailKB <= 0 || p.WorkDirAvailKB <= 0 {
		t.Errorf("preflight %+v", p)
	}
	if _, err := r.Stage(ctx(t), "a", cfg, render("generate"), SeedFromPending, nil); err != nil {
		t.Fatal(err)
	}
	if p, _ := r.Preflight(ctx(t), "a", cfg, "stellar-core"); !p.StagedLeftover {
		t.Error("staged file not reported")
	}
	if _, err := r.Install(ctx(t), "a", cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg + ".auriga-pending-seed"); !os.IsNotExist(err) {
		t.Error("pending seed left after install")
	}
	h2 := newFakeHost(t, "dash", "SHIM_GEN_FAIL=1")
	if _, err := newDirectRunner(map[string]Conn{"a": h2}, nil).GenSeed(ctx(t), "a", writeConfig(t, h2, sampleConfig, 0o640)); err == nil {
		t.Error("failed gen-seed accepted")
	}
}

func TestInit(t *testing.T) {
	h := newFakeHost(t, "dash")
	cfg := writeConfig(t, h, sampleConfig, 0o640)
	r := newDirectRunner(map[string]Conn{"a": h}, nil)
	if err := r.Init(ctx(t), "a", cfg, "stellar-core", InitNewDB, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.Init(ctx(t), "a", cfg, "stellar-core", InitNewHist, "acme-a"); err != nil {
		t.Fatal(err)
	}
	log := h.argvLog(t)
	for _, want := range []string{"systemctl stop stellar-core", "stellar-core --conf " + cfg + " new-db [cwd=" + h.dir + "]", "--conf " + cfg + " new-hist acme-a"} {
		if !strings.Contains(log, want) {
			t.Errorf("missing %q in:\n%s", want, log)
		}
	}
	if err := r.Init(ctx(t), "a", cfg, "stellar-core", InitKind("catchup"), ""); err == nil {
		t.Error("unknown init accepted")
	}
	h2 := newFakeHost(t, "dash", "SHIM_INIT_FAIL=1")
	if err := newDirectRunner(map[string]Conn{"a": h2}, nil).Init(ctx(t), "a", writeConfig(t, h2, sampleConfig, 0o640), "stellar-core", InitNewDB, ""); err == nil {
		t.Error("failed new-db accepted")
	}
}
