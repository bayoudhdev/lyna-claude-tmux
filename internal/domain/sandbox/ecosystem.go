package sandbox

// Ecosystem is a package ecosystem detected from a manifest at the project root.
type Ecosystem string

// Ecosystems with a known package registry.
const (
	EcosystemNode   Ecosystem = "node"
	EcosystemGo     Ecosystem = "go"
	EcosystemRust   Ecosystem = "rust"
	EcosystemPython Ecosystem = "python"
	EcosystemRuby   Ecosystem = "ruby"
)

type ecosystemSpec struct {
	ecosystem Ecosystem
	markers   []string
	domains   []string
}

// ecosystems is ordered: detection results and allowlist entries follow it,
// so generated settings are stable across runs. Domains are exact hosts, not
// wildcards, because the sandbox proxy decides from the requested hostname
// and every extra host widens what a command can reach.
var ecosystems = []ecosystemSpec{
	{EcosystemNode, []string{"package.json"}, []string{"registry.npmjs.org", "registry.yarnpkg.com"}},
	{EcosystemGo, []string{"go.mod"}, []string{"proxy.golang.org", "sum.golang.org"}},
	{EcosystemRust, []string{"Cargo.toml"}, []string{"crates.io", "index.crates.io", "static.crates.io"}},
	{EcosystemPython, []string{"pyproject.toml", "requirements.txt"}, []string{"pypi.org", "files.pythonhosted.org"}},
	{EcosystemRuby, []string{"Gemfile"}, []string{"rubygems.org", "index.rubygems.org"}},
}

// gitHubDomains serve clones, API calls, release downloads and raw files.
var gitHubDomains = []string{
	"github.com",
	"api.github.com",
	"codeload.github.com",
	"objects.githubusercontent.com",
	"raw.githubusercontent.com",
}

// GitHubDomains returns the hosts the strict profile always allows for git
// and GitHub API traffic.
func GitHubDomains() []string { return append([]string(nil), gitHubDomains...) }

// DetectEcosystems reports the ecosystems whose manifest exists. exists
// receives paths relative to the project root, such as "go.mod".
func DetectEcosystems(exists func(rel string) bool) []Ecosystem {
	var found []Ecosystem
	for _, spec := range ecosystems {
		for _, m := range spec.markers {
			if exists(m) {
				found = append(found, spec.ecosystem)
				break
			}
		}
	}
	return found
}

// EcosystemDomains returns the registry hosts of an ecosystem, or nil when
// the ecosystem is unknown.
func EcosystemDomains(e Ecosystem) []string {
	for _, spec := range ecosystems {
		if spec.ecosystem == e {
			return append([]string(nil), spec.domains...)
		}
	}
	return nil
}
