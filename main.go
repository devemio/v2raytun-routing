package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
)

type Route struct {
	Name           string `json:"name"`
	DomainStrategy string `json:"domainStrategy"`
	ID             string `json:"id"`
	DomainMatcher  string `json:"domainMatcher"`
	Rules          []Rule `json:"rules"`
	Balancers      []any  `json:"balancers"`
}

type Rule struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Domain      []string `json:"domain"`
	OutboundTag string   `json:"outboundTag"`
	Name        string   `json:"__name__"`
}

func main() {
	if len(os.Args) != 2 {
		fail("usage: go run . domains.txt")
	}

	domains, err := readDomains(os.Args[1])
	if err != nil {
		fail(err.Error())
	} else if len(domains) == 0 {
		fail("domain list is empty")
	}

	route := Route{
		Name:           "Default",
		DomainStrategy: "AsIs",
		ID:             uuid.NewString(),
		DomainMatcher:  "hybrid",
		Rules: []Rule{
			{
				ID:          uuid.NewString(),
				Type:        "field",
				Domain:      domains,
				OutboundTag: "direct",
				Name:        "Default",
			},
		},
		Balancers: []any{},
	}

	b, err := json.Marshal(route)
	if err != nil {
		fail(err.Error())
	}

	fmt.Printf("v2rayTun://import_route/%s", base64.URLEncoding.EncodeToString(b))
}

func readDomains(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	seen := make(map[string]struct{})
	out := make([]string, 0, 64)

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		if i := strings.Index(s, "#"); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}

		s = strings.ToLower(s)
		s = strings.TrimPrefix(s, "https://")
		s = strings.TrimPrefix(s, "http://")
		s = strings.TrimPrefix(s, "www.")
		s = strings.TrimSuffix(s, ".")

		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}

		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out, sc.Err()
}

func fail(msg string) {
	fmt.Fprint(os.Stderr, msg+"\n")
	os.Exit(1)
}
