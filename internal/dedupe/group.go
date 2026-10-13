package dedupe

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Match decides what counts as "the same site".
type Match string

const (
	// MatchHost treats audio.example.com and tools.example.com as different sites.
	MatchHost Match = "host"
	// MatchDomain treats both as example.com.
	MatchDomain Match = "domain"
)

// equivalentDomains share one account (similar to Bitwarden's equivalent domains).
var equivalentDomains = map[string][]string{
	"microsoft": {"microsoft.com", "live.com", "outlook.com", "hotmail.com", "msn.com",
		"office.com", "office365.com", "microsoftonline.com", "xbox.com",
		"skype.com", "onedrive.com", "bing.com", "azure.com", "windows.com"},
	"google": {"google.com", "google.de", "gmail.com", "youtube.com", "googlemail.com"},
	"apple":  {"apple.com", "icloud.com", "me.com"},
	"amazon": {"amazon.de", "amazon.com", "amazon.co.uk", "amazon.fr", "amazon.it",
		"amazon.es", "amazon.at", "audible.de", "audible.com"},
}

var domainFamily = func() map[string]string {
	m := map[string]string{}
	for family, domains := range equivalentDomains {
		for _, d := range domains {
			m[d] = family
		}
	}
	return m
}()

// neutralPrefixes usually mean the same login as without them (host matching).
var neutralPrefixes = map[string]bool{"www": true, "m": true, "login": true, "signin": true,
	"auth": true, "account": true, "accounts": true, "sso": true, "secure": true, "id": true}

var appURI = regexp.MustCompile(`(?i)^(androidapp|iosapp)://([\w.\-]+)`)

type parsedURI struct {
	host string // lower case, no trailing dot
	port string // empty for default ports
	reg  string // registrable domain, or host for IPs and single labels
	app  bool
}

func parseURI(raw string) (parsedURI, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return parsedURI{}, false
	}
	if m := appURI.FindStringSubmatch(raw); m != nil {
		labels := strings.Split(strings.ToLower(m[2]), ".")
		reg := labels[0]
		if len(labels) >= 2 {
			reg = labels[1] + "." + labels[0]
		}
		return parsedURI{host: reg, reg: reg, app: true}, true
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return parsedURI{}, false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return parsedURI{}, false
	}
	port := u.Port()
	if port == "80" || port == "443" {
		port = ""
	}
	p := parsedURI{host: host, port: port, reg: host}
	if net.ParseIP(host) == nil && strings.Contains(host, ".") {
		if reg, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
			p.reg = reg
		}
	}
	return p, true
}

// SiteKey identifies the site of a URI. IPs, localhost and dotless hosts
// include the port, because several services often share one address.
func SiteKey(raw string, match Match) string {
	p, ok := parseURI(raw)
	if !ok {
		return ""
	}
	if f, ok := domainFamily[p.reg]; ok {
		return f
	}
	if p.app {
		return p.reg
	}
	if net.ParseIP(p.host) != nil || !strings.Contains(p.host, ".") {
		if p.port != "" {
			return p.host + ":" + p.port
		}
		return p.host
	}
	if match == MatchDomain {
		return p.reg
	}
	labels := strings.Split(p.host, ".")
	regLabels := len(strings.Split(p.reg, "."))
	for len(labels) > regLabels && neutralPrefixes[labels[0]] {
		labels = labels[1:]
	}
	return strings.Join(labels, ".")
}

// Host returns the plain host of a URI for display.
func Host(raw string) string {
	if appURI.MatchString(strings.TrimSpace(raw)) {
		return strings.ToLower(strings.TrimSpace(raw))
	}
	if p, ok := parseURI(raw); ok {
		if p.port != "" && !p.app {
			return p.host + ":" + p.port
		}
		return p.host
	}
	return raw
}

func pwHash(pw string) string {
	if pw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(sum[:])
}

var spaces = regexp.MustCompile(`\s+`)

// Group is a set of items that look like duplicates of each other.
type Group struct {
	Items   []Item // newest first
	Reasons map[string]bool
}

// Reason codes.
const (
	ReasonSite   = "site"    // same site and username
	ReasonSitePW = "site_pw" // same site, no username, same password
	ReasonName   = "name"    // no URL, same name and username
)

// FindGroups groups possible duplicates. Deleted items must be filtered out before.
func FindGroups(items []Item, match Match) []Group {
	parent := map[string]string{}
	var root func(string) string
	root = func(a string) string {
		if _, ok := parent[a]; !ok {
			parent[a] = a
		}
		for parent[a] != a {
			parent[a] = parent[parent[a]]
			a = parent[a]
		}
		return a
	}

	type key struct{ kind, a, b, c string }
	keys := map[key][]string{}
	var order []key
	add := func(k key, id string) {
		if _, ok := keys[k]; !ok {
			order = append(order, k)
		}
		keys[k] = append(keys[k], id)
	}
	for _, it := range items {
		user := strings.ToLower(strings.TrimSpace(it.Username()))
		hosts, domains := map[string]bool{}, map[string]bool{}
		for _, u := range it.URIs() {
			if s := SiteKey(u, MatchHost); s != "" {
				hosts[s] = true
			}
			if s := SiteKey(u, MatchDomain); s != "" {
				domains[s] = true
			}
		}
		pw := pwHash(it.Password())
		if it.Type() == TypeLogin && len(hosts) > 0 {
			// Same host and username is a duplicate. Across subdomains
			// (match by domain) the password must match too: the same
			// username on audio.example.com and tools.example.com with
			// different passwords are different accounts.
			for s := range hosts {
				switch {
				case user != "":
					add(key{ReasonSite, s, user, ""}, it.ID())
				case pw != "":
					add(key{ReasonSitePW, s, pw, ""}, it.ID())
				}
			}
			if match == MatchDomain && pw != "" {
				for s := range domains {
					if user != "" {
						add(key{ReasonSite, "domain:" + s, user, pw}, it.ID())
					} else {
						add(key{ReasonSitePW, "domain:" + s, pw, ""}, it.ID())
					}
				}
			}
		} else {
			name := spaces.ReplaceAllString(strings.ToLower(strings.TrimSpace(it.Name())), " ")
			if name != "" {
				add(key{ReasonName, string(rune('0' + it.Type())), name, user}, it.ID())
			}
		}
	}

	reasons := map[string]map[string]bool{}
	for _, k := range order {
		ids := keys[k]
		if len(ids) < 2 {
			continue
		}
		for _, id := range ids[1:] {
			parent[root(id)] = root(ids[0])
		}
		for _, id := range ids {
			if reasons[id] == nil {
				reasons[id] = map[string]bool{}
			}
			reasons[id][k.kind] = true
		}
	}

	byID := map[string]Item{}
	for _, it := range items {
		byID[it.ID()] = it
	}
	members := map[string][]Item{}
	var roots []string
	for _, it := range items {
		id := it.ID()
		if reasons[id] == nil {
			continue
		}
		r := root(id)
		if _, ok := members[r]; !ok {
			roots = append(roots, r)
		}
		members[r] = append(members[r], byID[id])
	}
	var groups []Group
	for _, r := range roots {
		m := members[r]
		if len(m) < 2 {
			continue
		}
		sort.SliceStable(m, func(i, j int) bool { return m[i].RevisionDate() > m[j].RevisionDate() })
		rs := map[string]bool{}
		for _, it := range m {
			for k := range reasons[it.ID()] {
				rs[k] = true
			}
		}
		groups = append(groups, Group{Items: m, Reasons: rs})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return strings.ToLower(Title(groups[i].Items, match)) < strings.ToLower(Title(groups[j].Items, match))
	})
	return groups
}

// Title describes a group by its sites and usernames.
func Title(items []Item, match Match) string {
	sites := map[string]bool{}
	users := map[string]bool{}
	for _, it := range items {
		for _, u := range it.URIs() {
			if s := SiteKey(u, match); s != "" {
				sites[s] = true
			}
		}
		if u := strings.TrimSpace(it.Username()); u != "" {
			users[u] = true
		}
	}
	t := strings.Join(sortedKeys(sites), ", ")
	if t == "" && len(items) > 0 {
		t = items[0].Name()
	}
	if len(users) > 0 {
		t += " — " + strings.Join(sortedKeys(users), ", ")
	}
	return t
}

// distinctHosts counts hosts without neutral prefixes; more than one means different subdomains.
func distinctHosts(items []Item) int {
	hosts := map[string]bool{}
	for _, it := range items {
		for _, u := range it.URIs() {
			if s := SiteKey(u, MatchHost); s != "" {
				hosts[s] = true
			}
		}
	}
	return len(hosts)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
