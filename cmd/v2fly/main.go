package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	router "github.com/v2fly/v2ray-core/v5/app/router/routercommon"
	"google.golang.org/protobuf/proto"
)

type Match struct {
	Selector   string // geosite:<tag> or geosite:<tag>@<attr>
	Tag        string
	Attr       string // "" for base
	GroupSize  int    // number of domain rules in that selector
	Why        string // matched rule type: domain/full/plain/regex
	WhyRuleVal string // matched rule value
}

func main() {
	var geositePath string
	var domainsPath string
	var showWhy bool

	flag.StringVar(&geositePath, "geosite", "dlc.dat", "Path to geosite.dat (v2fly/domain-list-community build)")
	flag.StringVar(&domainsPath, "domains", "domains.txt", "Path to file with domains/urls (one per line)")
	flag.BoolVar(&showWhy, "why", true, "Show a brief reason (matched rule type/value)")
	flag.Parse()

	geo, err := loadGeoSiteList(geositePath)
	if err != nil {
		fatal(err)
	}

	domains, err := readDomains(domainsPath)
	if err != nil {
		fatal(err)
	}

	// Precompute group sizes:
	// - base size: total rules in tag
	// - attr size: count of rules within tag that have attr
	baseSize, attrSize := computeSizes(geo)

	// Regex cache across all matching
	regexCache := make(map[string]*regexp.Regexp)

	for _, raw := range domains {
		host, err := normalizeDomain(raw)
		if err != nil {
			fmt.Printf("%s\tERROR\t%v\n", raw, err)
			continue
		}

		matches := findMatchesForDomain(host, geo, baseSize, attrSize, regexCache)

		fmt.Printf("== %s ==\n", host)
		if len(matches) == 0 {
			fmt.Println("(no geosite match found)")
			fmt.Println()
			continue
		}

		// Sort: smallest group first, then selector for stability
		sort.Slice(matches, func(i, j int) bool {
			if matches[i].GroupSize != matches[j].GroupSize {
				return matches[i].GroupSize < matches[j].GroupSize
			}
			return matches[i].Selector < matches[j].Selector
		})

		for _, m := range matches {
			if showWhy {
				fmt.Printf("%-42s size=%-6d via=%s:%s\n", m.Selector, m.GroupSize, m.Why, m.WhyRuleVal)
			} else {
				fmt.Printf("%-42s size=%d\n", m.Selector, m.GroupSize)
			}
		}
		fmt.Println()
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

func loadGeoSiteList(path string) (*router.GeoSiteList, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	list := new(router.GeoSiteList)
	if err := proto.Unmarshal(b, list); err != nil {
		return nil, fmt.Errorf("proto unmarshal geosite.dat: %w", err)
	}
	return list, nil
}

func readDomains(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// normalizeDomain accepts:
// - pure host: sub.example.com
// - host:port
// - URL (http/https/etc)
// Returns lowercase host without trailing dot.
func normalizeDomain(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("empty")
	}

	// Try URL parse if it looks like one.
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err == nil && u.Host != "" {
			host := u.Host
			// strip port if present
			if h, _, err2 := net.SplitHostPort(host); err2 == nil {
				host = h
			}
			return cleanHost(host)
		}
	}

	// If contains path but no scheme, try adding scheme
	if strings.ContainsAny(s, "/?") && !strings.Contains(s, "://") {
		u, err := url.Parse("http://" + s)
		if err == nil && u.Host != "" {
			host := u.Host
			if h, _, err2 := net.SplitHostPort(host); err2 == nil {
				host = h
			}
			return cleanHost(host)
		}
	}

	// host:port ?
	if h, _, err := net.SplitHostPort(s); err == nil {
		return cleanHost(h)
	}

	// Otherwise assume it's already host
	return cleanHost(s)
}

func cleanHost(host string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return "", errors.New("empty host after normalization")
	}
	if strings.Contains(host, " ") {
		return "", fmt.Errorf("invalid host: %q", host)
	}
	return host, nil
}

func computeSizes(geo *router.GeoSiteList) (map[string]int, map[string]map[string]int) {
	base := make(map[string]int)            // tag -> count
	attr := make(map[string]map[string]int) // tag -> attr -> count

	for _, site := range geo.GetEntry() {
		tag := site.GetCountryCode()
		domains := site.GetDomain()

		base[tag] = len(domains)
		if _, ok := attr[tag]; !ok {
			attr[tag] = make(map[string]int)
		}

		for _, d := range domains {
			for _, a := range d.GetAttribute() {
				k := a.GetKey()
				if k != "" {
					attr[tag][k]++
				}
			}
		}
	}

	return base, attr
}

func findMatchesForDomain(
	host string,
	geo *router.GeoSiteList,
	baseSize map[string]int,
	attrSize map[string]map[string]int,
	regexCache map[string]*regexp.Regexp,
) []Match {
	type why struct {
		ruleType string
		ruleVal  string
	}

	// selector -> best why (first hit)
	selectorWhy := make(map[string]why)

	for _, site := range geo.GetEntry() {
		tag := site.GetCountryCode()

		for _, rule := range site.GetDomain() {
			ok, whyType := matchRule(host, rule, regexCache)
			if !ok {
				continue
			}

			// Base selector always matches if rule matches
			baseSel := "geosite:" + tag
			if _, exists := selectorWhy[baseSel]; !exists {
				selectorWhy[baseSel] = why{ruleType: whyType, ruleVal: rule.GetValue()}
			}

			// Attribute selectors: geosite:<tag>@<attr>
			for _, a := range rule.GetAttribute() {
				k := a.GetKey()
				if k == "" {
					continue
				}
				sel := "geosite:" + tag + "@" + k
				if _, exists := selectorWhy[sel]; !exists {
					selectorWhy[sel] = why{ruleType: whyType, ruleVal: rule.GetValue()}
				}
			}
		}
	}

	// Build matches with sizes
	var out []Match
	for sel, w := range selectorWhy {
		tag, attr := parseSelector(sel)
		size := 0
		if attr == "" {
			size = baseSize[tag]
		} else {
			size = attrSize[tag][attr]
		}
		out = append(out, Match{
			Selector:   sel,
			Tag:        tag,
			Attr:       attr,
			GroupSize:  size,
			Why:        w.ruleType,
			WhyRuleVal: w.ruleVal,
		})
	}

	return out
}

func parseSelector(sel string) (tag string, attr string) {
	sel = strings.TrimPrefix(sel, "geosite:")
	parts := strings.SplitN(sel, "@", 2)
	tag = parts[0]
	if len(parts) == 2 {
		attr = parts[1]
	}
	return tag, attr
}

// IMPORTANT COMPAT FIX:
// Different v2fly/v2ray-core versions generate different enum constant names.
// To avoid "undefined: router.Domain_Domain", we match by the numeric enum values.
// According to the proto, the mapping is typically:
//
//	Plain=0, Regex=1, Domain=2, Full=3
//
// If your version differs, you can adjust the numbers below.
func matchRule(host string, d *router.Domain, cache map[string]*regexp.Regexp) (bool, string) {
	val := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d.GetValue()), "."))
	if val == "" {
		return false, ""
	}

	t := int32(d.GetType())

	switch t {
	case 0: // Plain
		if strings.Contains(host, val) {
			return true, "plain"
		}
		return false, "plain"

	case 2: // Domain (suffix)
		if host == val || strings.HasSuffix(host, "."+val) {
			return true, "domain"
		}
		return false, "domain"

	case 3: // Full
		if host == val {
			return true, "full"
		}
		return false, "full"

	case 1: // Regex
		re, ok := cache[val]
		if !ok {
			r, err := regexp.Compile(val)
			if err != nil {
				cache[val] = nil
				return false, "regex"
			}
			cache[val] = r
			re = r
		}
		if re != nil && re.MatchString(host) {
			return true, "regex"
		}
		return false, "regex"

	default:
		// Conservative fallback: exact match only
		if host == val {
			return true, "unknown"
		}
		return false, "unknown"
	}
}
