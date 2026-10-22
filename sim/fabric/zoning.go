package fabric

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tjst-t/fc-switch-sim/sim/fc"
)

// ゾーニングの設定が無いときの扱い。
const (
	DefaultZoneAll  = "allaccess" // 全部が見える
	DefaultZoneNone = "noaccess"  // 何も見えない
)

// Zoning は有効なゾーニングの設定（ベンダーに依らない形）。
//
// Members には WWPN（"10:00:..."）か、Aliases の名前を書く。
type Zoning struct {
	DefaultZone string              `json:"defaultZone" yaml:"defaultZone"`
	Aliases     map[string][]string `json:"aliases,omitempty" yaml:"aliases,omitempty"`
	Zones       []Zone              `json:"zones" yaml:"zones"`
}

type Zone struct {
	Name    string   `json:"name" yaml:"name"`
	Members []string `json:"members" yaml:"members"`
}

// compiledZoning は WWPN からその WWPN を含むゾーンの番号への表。
type compiledZoning struct {
	defaultAll bool
	active     bool // ゾーンが 1 つ以上ある
	byWWPN     map[fc.WWN][]int
}

func compileZoning(z Zoning) (compiledZoning, error) {
	c := compiledZoning{byWWPN: map[fc.WWN][]int{}}
	switch z.DefaultZone {
	case "", DefaultZoneAll:
		c.defaultAll = true
	case DefaultZoneNone:
	default:
		return c, fmt.Errorf("defaultZone は %s か %s（いまは %q）", DefaultZoneAll, DefaultZoneNone, z.DefaultZone)
	}
	names := map[string]bool{}
	for i, zone := range z.Zones {
		if zone.Name == "" {
			return c, fmt.Errorf("%d 番目のゾーンに名前が無い", i+1)
		}
		if names[zone.Name] {
			return c, fmt.Errorf("ゾーン %q が 2 つある", zone.Name)
		}
		names[zone.Name] = true
		seen := map[fc.WWN]bool{}
		for _, m := range zone.Members {
			wwpns, err := resolveMember(z.Aliases, m)
			if err != nil {
				return c, fmt.Errorf("ゾーン %q：%w", zone.Name, err)
			}
			for _, w := range wwpns {
				if !seen[w] {
					seen[w] = true
					c.byWWPN[w] = append(c.byWWPN[w], i)
				}
			}
		}
	}
	c.active = len(z.Zones) > 0
	return c, nil
}

func resolveMember(aliases map[string][]string, m string) ([]fc.WWN, error) {
	if w, err := fc.ParseWWN(m); err == nil {
		return []fc.WWN{w}, nil
	}
	targets, ok := aliases[m]
	if !ok {
		return nil, fmt.Errorf("メンバー %q は WWPN でもエイリアスでもない", m)
	}
	var out []fc.WWN
	for _, t := range targets {
		w, err := fc.ParseWWN(t)
		if err != nil {
			return nil, fmt.Errorf("エイリアス %q：%w", m, err)
		}
		out = append(out, w)
	}
	return out, nil
}

// canSee は 2 つの WWPN がゾーニングで通じ合えるか。
func (c compiledZoning) canSee(a, b fc.WWN) bool {
	if a == b {
		return true
	}
	if !c.active {
		return c.defaultAll
	}
	za, zb := c.byWWPN[a], c.byWWPN[b]
	for _, x := range za {
		for _, y := range zb {
			if x == y {
				return true
			}
		}
	}
	return false
}

// normalize はゾーニングの設定の WWPN を小文字のコロン区切りにそろえる（表示用）。
func normalize(z Zoning) Zoning {
	out := Zoning{DefaultZone: z.DefaultZone}
	if out.DefaultZone == "" {
		out.DefaultZone = DefaultZoneAll
	}
	norm := func(s string) string {
		if w, err := fc.ParseWWN(s); err == nil {
			return w.String()
		}
		return s
	}
	if len(z.Aliases) > 0 {
		out.Aliases = map[string][]string{}
		keys := make([]string, 0, len(z.Aliases))
		for k := range z.Aliases {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, v := range z.Aliases[k] {
				out.Aliases[k] = append(out.Aliases[k], norm(v))
			}
		}
	}
	for _, zone := range z.Zones {
		nz := Zone{Name: zone.Name}
		for _, m := range zone.Members {
			nz.Members = append(nz.Members, strings.TrimSpace(norm(m)))
		}
		out.Zones = append(out.Zones, nz)
	}
	return out
}
