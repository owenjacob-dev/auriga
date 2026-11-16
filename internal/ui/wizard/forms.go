package wizard

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/huh/v2"

	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
)

// formState holds the values a step's form edits. Huh binds to pointers,
// so per-host answers live here while the form is open and are copied
// into the draft when the step is completed.
type formState struct {
	keys     map[string]*KeyInput
	confirm  map[string]*string // typed alias for replacing a running key
	archives map[string]*archiveFields
	advanced map[string]*advancedFields
	pubnet   string // typed "pubnet" for consent on pubnet
}

// archiveFields mirrors ArchiveSpec with string fields Huh can bind.
type archiveFields struct {
	name, backend, url, dir, bucket, prefix, endpoint, get, put, mkdir string
}

type advancedFields struct {
	database, bucketDir, logFile, httpPort, peerPort, extraPeers string
	preferQuorum, quorumOverride                                 bool
	quorumEntries                                                string
}

func notEmpty(what string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New(what + " is required")
		}
		return nil
	}
}

func portOK(s string) error {
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("enter a port from 1 to 65535")
	}
	return nil
}

// buildForm returns the Huh form for the current step.
func (m *Model) buildForm() *huh.Form {
	d := m.draft
	m.fs = formState{keys: map[string]*KeyInput{}, confirm: map[string]*string{}, archives: map[string]*archiveFields{}, advanced: map[string]*advancedFields{}}
	var groups []*huh.Group
	switch m.step {
	case StepScope:
		var opts []huh.Option[string]
		for _, h := range m.fleet.Hosts {
			label := h.Alias
			if h.Class == nil {
				continue // not probed yet
			}
			if h.Class.Class.String() == "read-only" {
				continue
			}
			if h.Org != "" {
				label += "  (" + h.Org + ")"
			}
			opts = append(opts, huh.NewOption(label, h.Alias).Selected(slices.Contains(d.Hosts, h.Alias)))
		}
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("Organisation name").Description("A short name for this org in Auriga, e.g. acme.").Value(&d.OrgName).Validate(notEmpty("a name")),
			huh.NewInput().Title("Home domain (NODE_HOME_DOMAIN)").Description("The domain that publishes your stellar.toml, e.g. acme.org.").Value(&d.HomeDomain).Validate(notEmpty("a home domain")),
			huh.NewMultiSelect[string]().Title("Hosts in this org").Description("space selects; read-only and unprobed hosts are not listed").
				Options(opts...).Value(&d.Hosts).Height(min(len(opts), 8)+4),
		))

	case StepNetwork:
		nets := []huh.Option[string]{
			huh.NewOption("Testnet (reset periodically by SDF)", "testnet"),
			huh.NewOption("Pubnet (mainnet)", "pubnet"),
		}
		if _, ok := Passphrases[NetworkPrivate]; ok {
			nets = append(nets, huh.NewOption("Private test network (test flag; no preset)", NetworkPrivate))
		}
		groups = append(groups, huh.NewGroup(
			huh.NewSelect[string]().Title("Network").Options(nets...).Value(&d.Network),
		))
		if changing := m.hostsChangingNetwork(); len(changing) > 0 {
			var opts []huh.Option[string]
			for _, h := range changing {
				opts = append(opts, huh.NewOption(h, h).Selected(slices.Contains(d.NewDBAccepted, h)))
			}
			groups = append(groups, huh.NewGroup(
				huh.NewMultiSelect[string]().Title("These hosts are on another network").
					Description("Switching runs new-db, which wipes the node's local ledger state. Select each host to accept that.").
					Options(opts...).Value(&d.NewDBAccepted).Height(min(len(opts), 8)+4),
			))
		}

	case StepType:
		groups = append(groups, huh.NewGroup(
			huh.NewSelect[string]().Title("Node type (all hosts)").Options(
				huh.NewOption("Basic Validator: votes, no public archive", TypeBasic),
				huh.NewOption("Full Validator: votes and publishes an archive", TypeFull),
				huh.NewOption("Archiver: publishes an archive, does not vote", TypeArchiver),
				huh.NewOption("Watcher: observes only", TypeWatcher),
			).Value(&d.NodeType),
		))

	case StepKeys:
		for _, h := range d.Hosts {
			k := d.Keys[h]
			f := m.facts()[h]
			if k.Mode == "" {
				k.Mode = KeyGenerate
				if f.SeedPublicKey != "" {
					k.Mode = KeyKeep
				}
			}
			if k.Alias == "" {
				// Keep an adopted node's name: it is its NODE_SEED alias and
				// the NAME its siblings already use for it.
				k.Alias = h
				if f.Existing != nil && f.Existing.NodeSeed != nil && f.Existing.NodeSeed.Alias != "" {
					k.Alias = f.Existing.NodeSeed.Alias
				}
			}
			kp := &k
			m.fs.keys[h] = kp
			opts := []huh.Option[string]{}
			if f.SeedPublicKey != "" {
				opts = append(opts, huh.NewOption("Keep the existing seed ("+shortKey(f.SeedPublicKey)+")", KeyKeep))
			}
			opts = append(opts, huh.NewOption("Generate a new seed on the host", KeyGenerate), huh.NewOption("Import a seed I paste", KeyImport))
			confirm := ""
			m.fs.confirm[h] = &confirm
			host := h
			needsConfirm := func() bool {
				return kp.Mode != KeyKeep && f.SeedPublicKey != "" && f.Running && Validates(d.NodeType) && !kp.ReplaceConfirmed
			}
			groups = append(groups, huh.NewGroup(
				huh.NewSelect[string]().Title("Keys for "+h).Options(opts...).Value(&kp.Mode),
				huh.NewInput().Title("Node alias").Description("Appended to NODE_SEED and used as NAME by the other hosts.").Value(&kp.Alias),
				huh.NewInput().Title("Public address").Description("How other nodes reach this one, e.g. core-a.acme.org:11625.").Value(&kp.Address),
				huh.NewInput().TitleFunc(func() string { return "Type " + host + " to replace its running validator key" }, kp).
					DescriptionFunc(func() string {
						if !needsConfirm() {
							return "Not needed: the key is kept, or the node is not a running validator."
						}
						return "Every org that trusts this node must update its config."
					}, kp).Value(&confirm).Validate(func(s string) error {
					if needsConfirm() && s != host {
						return fmt.Errorf("type %s exactly", host)
					}
					return nil
				}),
			).Title(h))
		}

	case StepQuorum:
		qualities := []huh.Option[string]{
			huh.NewOption("MEDIUM (new validators)", string(stellarcfg.Medium)),
			huh.NewOption("HIGH (Tier 1: needs 3 validators, each with an archive)", string(stellarcfg.High)),
		}
		fields := []huh.Field{
			huh.NewSelect[string]().Title("Quality of your own org").Options(qualities...).Value(&d.Quorum.OwnQuality),
		}
		if m.preset != nil {
			fields = append(fields, huh.NewConfirm().Title("Trust the "+string(m.preset.Network)+" preset?").
				Description(m.preset.Summary()+". ctrl+r fetches the current list.").Value(&d.Quorum.UsePreset))
			var opts []huh.Option[string]
			for _, hd := range m.preset.HomeDomains {
				opts = append(opts, huh.NewOption(fmt.Sprintf("%s (%s)", hd.HomeDomain, hd.Quality), hd.HomeDomain).
					Selected(len(d.Quorum.PresetDomains) == 0 || slices.Contains(d.Quorum.PresetDomains, hd.HomeDomain)))
			}
			if len(opts) > 1 {
				fields = append(fields, huh.NewMultiSelect[string]().Title("Preset orgs to trust").Options(opts...).
					Value(&d.Quorum.PresetDomains).Height(min(len(opts), 8)+4))
			}
		}
		fields = append(fields, huh.NewText().Title("Other orgs and validators (optional)").
			Description("Paste [[HOME_DOMAINS]] and [[VALIDATORS]] entries.").Value(&d.Quorum.Manual).Lines(4))
		groups = append(groups, huh.NewGroup(fields...))
		if m.needsConsent() {
			consent := []huh.Field{huh.NewConfirm().Title("Accept a quorum set that tolerates no failures?").
				Description("stellar-core starts only with UNSAFE_QUORUM=true. If any one entity fails, this node stops closing ledgers.").
				Value(&d.Quorum.UnsafeConsent)}
			if d.Network == "pubnet" {
				consent = append(consent, huh.NewInput().Title("Type pubnet to confirm").Value(&m.fs.pubnet).Validate(func(s string) error {
					if d.Quorum.UnsafeConsent && s != "pubnet" {
						return errors.New("type pubnet to accept on pubnet")
					}
					return nil
				}))
			}
			groups = append(groups, huh.NewGroup(consent...).Title("Zero fault tolerance"))
		}

	case StepHistory:
		for i, h := range d.Hosts {
			a := d.Archives[h]
			af := &archiveFields{name: a.Name, backend: string(a.Backend), url: a.URL, dir: a.Dir, bucket: a.Bucket,
				prefix: a.Prefix, endpoint: a.Endpoint, get: a.Get, put: a.Put, mkdir: a.Mkdir}
			if af.name == "" {
				af.name = h
			}
			if af.backend == "" {
				af.backend = string(stellarcfg.BackendS3)
			}
			if af.prefix == "" && af.backend == string(stellarcfg.BackendS3) {
				af.prefix = h
			}
			m.fs.archives[h] = af
			is := func(b stellarcfg.Backend) func() bool { return func() bool { return af.backend != string(b) } }
			_ = i
			groups = append(groups,
				huh.NewGroup(
					huh.NewInput().Title("Archive name for "+h).Description("The [HISTORY.<name>] table and the new-hist argument.").Value(&af.name),
					huh.NewSelect[string]().Title("Backend").Options(
						huh.NewOption("S3-compatible (S3, R2, B2)", string(stellarcfg.BackendS3)),
						huh.NewOption("Local directory served by nginx", string(stellarcfg.BackendNginx)),
						huh.NewOption("Custom commands", string(stellarcfg.BackendCustom)),
					).Value(&af.backend),
				).Title(h),
				huh.NewGroup(
					huh.NewInput().Title("Public URL").Description("Where anyone reads the archive, e.g. https://history-a.acme.org.").Value(&af.url),
					huh.NewInput().Title("Bucket").Value(&af.bucket),
					huh.NewInput().Title("Prefix").Value(&af.prefix),
					huh.NewInput().Title("Endpoint URL (R2 or B2 only)").Value(&af.endpoint),
				).Title(h+": S3-compatible").WithHideFunc(is(stellarcfg.BackendS3)),
				huh.NewGroup(
					huh.NewInput().Title("Public URL").Value(&af.url),
					huh.NewInput().Title("Directory nginx serves").Description("Absolute path on the host, e.g. /var/www/history.").Value(&af.dir),
				).Title(h+": local directory").WithHideFunc(is(stellarcfg.BackendNginx)),
				huh.NewGroup(
					huh.NewInput().Title("get").Description("Must contain {0} and {1}.").Value(&af.get),
					huh.NewInput().Title("put").Value(&af.put),
					huh.NewInput().Title("mkdir").Value(&af.mkdir),
				).Title(h+": custom").WithHideFunc(is(stellarcfg.BackendCustom)),
			)
		}

	case StepAdvanced:
		for _, h := range d.Hosts {
			a := d.Advanced[h]
			af := &advancedFields{database: a.Database, bucketDir: a.BucketDir, logFile: a.LogFile, extraPeers: strings.Join(a.ExtraPeers, ", "),
				preferQuorum: a.PreferQuorum, quorumOverride: a.QuorumOverride, quorumEntries: a.QuorumEntries}
			if af.quorumEntries == "" && m.plan != nil {
				af.quorumEntries = OrgEntries(m.plan) // start from the org-wide set
			}
			if a.HTTPPort != 0 {
				af.httpPort = strconv.Itoa(a.HTTPPort)
			}
			if a.PeerPort != 0 {
				af.peerPort = strconv.Itoa(a.PeerPort)
			}
			m.fs.advanced[h] = af
			f := m.facts()[h]
			dbDesc := "Required for a new node: sqlite3:///var/lib/stellar/stellar.db or postgresql://dbname=stellar user=stellar."
			if f.Existing != nil && f.Existing.Database != "" {
				dbDesc = "Leave empty to keep " + f.Existing.Database + "."
			}
			fields := []huh.Field{
				huh.NewInput().Title("DATABASE").Description(dbDesc).Value(&af.database),
				huh.NewInput().Title("BUCKET_DIR_PATH").Placeholder(DefaultBucketDir).Value(&af.bucketDir),
				huh.NewInput().Title("LOG_FILE_PATH").Placeholder(DefaultLogFile).Value(&af.logFile),
				huh.NewInput().Title("HTTP_PORT").Placeholder(strconv.Itoa(DefaultHTTPPort)).Value(&af.httpPort).Validate(portOK),
				huh.NewInput().Title("PEER_PORT").Placeholder(strconv.Itoa(DefaultPeerPort)).Value(&af.peerPort).Validate(portOK),
				huh.NewInput().Title("Other known peers").Description("host:port, comma separated; sibling addresses are added for you.").Value(&af.extraPeers),
				huh.NewConfirm().Title("Prefer quorum validators as peers (PREFERRED_PEER_KEYS)?").Value(&af.preferQuorum),
			}
			if f.Existing != nil && len(f.Existing.Unknown) > 0 {
				var keys []string
				for _, e := range f.Existing.Unknown {
					keys = append(keys, e.Key)
				}
				fields = append(fields, huh.NewNote().Title("Kept unchanged").Description(strings.Join(keys, ", ")))
			}
			fields = append(fields, huh.NewConfirm().Title("Use a different quorum set for this host?").
				Description("By default every host trusts the same orgs. This host's own org and siblings are always kept.").
				Value(&af.quorumOverride))
			groups = append(groups,
				huh.NewGroup(fields...).Title(h),
				huh.NewGroup(
					huh.NewText().Title("Trusted orgs and validators for "+h).
						Description("[[HOME_DOMAINS]] and [[VALIDATORS]] entries, starting from the org-wide set. Entries for "+d.HomeDomain+" are ignored.").
						Value(&af.quorumEntries).Lines(max(6, m.rows-10)),
				).Title(h+": own quorum set").WithHideFunc(func() bool { return !af.quorumOverride }),
			)
		}
	}
	form := huh.NewForm(groups...).WithShowHelp(true).WithShowErrors(true).WithTheme(huh.ThemeFunc(huh.ThemeBase))
	if m.cols > 0 {
		form = form.WithWidth(m.cols)
	}
	m.formHeight = 0 // View sizes the form to the room left
	return form
}

// collect copies the form's per-host values into the draft.
func (m *Model) collect() {
	d := m.draft
	for h, k := range m.fs.keys {
		if c := m.fs.confirm[h]; c != nil && *c == h {
			k.ReplaceConfirmed = true
		}
		switch {
		case k.Mode != KeyImport:
			k.ImportedPublicKey = ""
			if b := m.seeds[h]; b != nil {
				b.Zero() // no longer importing: drop the pasted seed
				delete(m.seeds, h)
			}
		case k.ImportedPublicKey == "":
			// Pasted after the form was built: keep the key the paste set.
			k.ImportedPublicKey = d.Keys[h].ImportedPublicKey
		}
		d.Keys[h] = *k
	}
	for h, a := range m.fs.archives {
		d.Archives[h] = stellarcfg.ArchiveSpec{Name: strings.TrimSpace(a.name), Backend: stellarcfg.Backend(a.backend),
			URL: strings.TrimSpace(a.url), Dir: strings.TrimSpace(a.dir), Bucket: strings.TrimSpace(a.bucket),
			Prefix: strings.Trim(strings.TrimSpace(a.prefix), "/"), Endpoint: strings.TrimSpace(a.endpoint),
			Get: strings.TrimSpace(a.get), Put: strings.TrimSpace(a.put), Mkdir: strings.TrimSpace(a.mkdir)}
	}
	for h, a := range m.fs.advanced {
		in := AdvancedInput{Database: strings.TrimSpace(a.database), BucketDir: strings.TrimSpace(a.bucketDir),
			LogFile: strings.TrimSpace(a.logFile), PreferQuorum: a.preferQuorum, QuorumOverride: a.quorumOverride}
		if a.quorumOverride {
			in.QuorumEntries = a.quorumEntries
		}
		in.HTTPPort, _ = strconv.Atoi(a.httpPort)
		in.PeerPort, _ = strconv.Atoi(a.peerPort)
		for _, p := range strings.Split(a.extraPeers, ",") {
			if p = strings.TrimSpace(p); p != "" {
				in.ExtraPeers = append(in.ExtraPeers, p)
			}
		}
		d.Advanced[h] = in
	}
	if d.Network != "pubnet" || m.fs.pubnet == "pubnet" || !d.Quorum.UnsafeConsent {
		return
	}
	d.Quorum.UnsafeConsent = false // pubnet consent needs the typed confirmation
}

func shortKey(k string) string {
	if len(k) > 9 {
		return k[:4] + "…" + k[len(k)-4:]
	}
	return k
}
