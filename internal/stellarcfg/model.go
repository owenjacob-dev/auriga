// Package stellarcfg is Auriga's model of a stellar-core config: parse,
// render, validate, diff and classify (SPEC.md "Config keys the tool owns",
// "Rendering", "Classification").
//
// The package is pure: it never touches a host or the network. Configs it
// sees have NODE_SEED redacted on the host ("<redacted:G…>"), and configs it
// renders carry a seed marker that only the host helper replaces.
package stellarcfg

// Quality is a [[HOME_DOMAINS]] or [[VALIDATORS]] QUALITY.
type Quality string

// Qualities, strongest first (SPEC.md "Quorum sets").
const (
	Critical Quality = "CRITICAL"
	High     Quality = "HIGH"
	Medium   Quality = "MEDIUM"
	Low      Quality = "LOW"
)

// rank orders qualities strongest first; unknown values sort last.
func (q Quality) rank() int {
	switch q {
	case Critical:
		return 0
	case High:
		return 1
	case Medium:
		return 2
	case Low:
		return 3
	}
	return 4
}

// Valid reports whether q is one of the four qualities.
func (q Quality) Valid() bool { return q.rank() < 4 }

// HomeDomain is one [[HOME_DOMAINS]] entry.
type HomeDomain struct {
	HomeDomain string
	Quality    Quality
}

// Validator is one [[VALIDATORS]] entry.
type Validator struct {
	Name       string
	HomeDomain string
	PublicKey  string
	Address    string  // optional "host:port"
	History    string  // optional get command template
	Quality    Quality // optional; falls back to the home domain's
}

// HistoryArchive is one [HISTORY.<name>] table.
type HistoryArchive struct {
	Name  string
	Get   string
	Put   string // empty for read-only archives
	Mkdir string // empty when the backend needs none
}

// SeedMode says where the host helper gets NODE_SEED from at deploy time.
type SeedMode string

const (
	// SeedKeep copies the seed already in the host's config.
	SeedKeep SeedMode = "keep"
	// SeedGenerate runs stellar-core gen-seed on the host.
	SeedGenerate SeedMode = "generate"
	// SeedImport uses a seed the operator pasted, sent over stdin.
	SeedImport SeedMode = "import"
)

// Seed describes NODE_SEED without ever holding the secret.
type Seed struct {
	Mode SeedMode
	// PublicKey is the key derived on the host ("" if unknown, e.g. before
	// a seed is generated).
	PublicKey string
	// Alias is the optional local name after the seed ("acme-a").
	Alias string
}

// Entry is a part of a config the model does not manage, kept verbatim.
type Entry struct {
	// Key is the top-level key ("COMMANDS") or table header path
	// ("QUORUM_SET", "[[SOMETHING]]" for array tables).
	Key string
	// Raw is the original text: `KEY = value` for a key, or the header line
	// followed by its key/value lines for a table.
	Raw string
	// Table is true for [table] and [[array table]] entries.
	Table bool
}

// Config is a stellar-core config. Optional scalars are pointers or empty
// strings; nil slices mean "key absent".
type Config struct {
	NetworkPassphrase string
	NodeSeed          *Seed
	NodeIsValidator   *bool
	NodeHomeDomain    string

	HTTPPort       *int
	PublicHTTPPort *bool
	PeerPort       *int
	Database       string
	BucketDirPath  string
	// LogFilePath is a pointer because "" is meaningful: it turns file
	// logging off, while an absent key means stellar-core's relative default.
	LogFilePath *string

	KnownPeers        []string
	PreferredPeers    []string
	PreferredPeerKeys []string

	// UnsafeQuorum lets stellar-core start with a quorum set that tolerates
	// no failures. Auriga writes it only after the operator consents
	// (docs/decisions.md, M4).
	UnsafeQuorum *bool

	HomeDomains []HomeDomain
	Validators  []Validator
	History     []HistoryArchive

	// Unknown holds every key and table the model does not manage, in source
	// order, written back unchanged.
	Unknown []Entry
}

// Ptr returns a pointer to v, for optional fields.
func Ptr[T any](v T) *T { return &v }
