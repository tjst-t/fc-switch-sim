package fabric

import (
	"fmt"
	"time"

	"github.com/tjst-t/fc-switch-sim/sim/fc"
)

// SwitchInfo はスイッチの全体の様子。
type SwitchInfo struct {
	Name        string `json:"name"`
	SwitchWWN   fc.WWN `json:"switchWWN"`
	FabricName  fc.WWN `json:"fabricName"`
	DomainID    byte   `json:"domainID"`
	FCMap       string `json:"fcMap"`
	FKAPeriodMs uint32 `json:"fkaPeriodMs"`
	Ports       int    `json:"ports"`
	Logins      int    `json:"logins"`
}

// PortInfo はポート 1 つの様子。
type PortInfo struct {
	Index     int          `json:"index"`
	Interface string       `json:"interface"`
	PortWWN   fc.WWN       `json:"portWWN"`
	MAC       string       `json:"mac"`
	Enabled   bool         `json:"enabled"`
	State     string       `json:"state"` // Online（ログインあり）・NoLogin・Disabled
	ENodeMAC  string       `json:"enodeMAC,omitempty"`
	ENodeWWNN *fc.WWN      `json:"enodeWWNN,omitempty"`
	Logins    []fc.FCID    `json:"-"`
	FCIDs     []string     `json:"fcids"`
	Counters  PortCounters `json:"counters"`
}

// LoginInfo はネームサーバの 1 件。
type LoginInfo struct {
	FCID        string    `json:"fcid"`
	WWPN        fc.WWN    `json:"wwpn"`
	WWNN        fc.WWN    `json:"wwnn"`
	Port        int       `json:"port"`
	NPIV        bool      `json:"npiv"`
	VNMAC       string    `json:"vnMAC"`
	ENodeMAC    string    `json:"enodeMAC"`
	LoginTime   time.Time `json:"loginTime"`
	FC4Types    []string  `json:"fc4Types"`
	Initiator   bool      `json:"initiator"`
	Target      bool      `json:"target"`
	SymPortName string    `json:"symPortName,omitempty"`
	SymNodeName string    `json:"symNodeName,omitempty"`
	RSCN        bool      `json:"rscnRegistered"`
}

// Info はスイッチの様子を返す。
func (s *Switch) Info() SwitchInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SwitchInfo{
		Name: s.cfg.Name, SwitchWWN: s.cfg.SwitchWWN, FabricName: s.cfg.FabricName,
		DomainID: s.cfg.DomainID, FCMap: fmt.Sprintf("%06x", s.cfg.FCMap),
		FKAPeriodMs: s.cfg.FKAPeriodMs, Ports: len(s.ports), Logins: len(s.logins),
	}
}

// Ports はポートの様子を返す。
func (s *Switch) Ports() []PortInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PortInfo
	for _, p := range s.ports {
		pi := PortInfo{
			Index: p.Index, Interface: p.Name, PortWWN: s.cfg.PortWWN(p.Index),
			MAC: p.tr.MAC().String(), Enabled: p.Enabled, Counters: p.Counters, FCIDs: []string{},
		}
		if kd, ok := p.tr.(interface{ KernelDrops() uint64 }); ok {
			pi.Counters.KernelDrops = kd.KernelDrops()
		}
		if p.ENodeMAC != nil {
			pi.ENodeMAC = p.ENodeMAC.String()
			w := p.ENodeWWNN
			pi.ENodeWWNN = &w
		}
		for _, l := range s.loginsOnPort(p.Index) {
			pi.FCIDs = append(pi.FCIDs, l.FCID.String())
		}
		switch {
		case !p.Enabled:
			pi.State = "Disabled"
		case len(pi.FCIDs) > 0:
			pi.State = "Online"
		default:
			pi.State = "NoLogin"
		}
		out = append(out, pi)
	}
	return out
}

// NameServer はネームサーバの中身を返す。
func (s *Switch) NameServer() []LoginInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []LoginInfo{}
	for _, l := range s.sortedLogins() {
		li := LoginInfo{
			FCID: l.FCID.String(), WWPN: l.WWPN, WWNN: l.WWNN, Port: l.Port, NPIV: l.NPIV,
			VNMAC: l.VNMAC.String(), ENodeMAC: l.ENodeMAC.String(), LoginTime: l.LoginTime,
			SymPortName: l.SymPortName, SymNodeName: l.SymNodeName, RSCN: l.SCR != 0,
			FC4Types: []string{},
		}
		if l.hasType(fc.TypeFCP) {
			li.FC4Types = append(li.FC4Types, "FCP")
		}
		if l.hasType(fc.TypeCT) {
			li.FC4Types = append(li.FC4Types, "CT")
		}
		// FC-4 の機能：ビット 0 がターゲット、ビット 1 がイニシエータ（FCP）。
		if f, ok := l.FC4Features[fc.TypeFCP]; ok {
			li.Target = f&0x01 != 0
			li.Initiator = f&0x02 != 0
		}
		out = append(out, li)
	}
	return out
}

// Zoning はいまのゾーニングの設定を返す。
func (s *Switch) Zoning() Zoning {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.zoning
}

// SetZoning はゾーニングを入れ替え、RSCN で全体に知らせる。
// 見えなくなった相手へのフレームは、この時点から捨てる（ハードゾーニング）。
func (s *Switch) SetZoning(z Zoning) error {
	zc, err := compileZoning(z)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.zc = zc
	s.zoning = normalize(z)
	s.event("zoning", -1, "ゾーニングを変えた（ゾーン %d 個、ゾーンが無いときは %s）", len(z.Zones), s.zoning.DefaultZone)
	s.rscnAll()
	return nil
}

// SetPortEnabled はポートを有効・無効にする。無効にするとログインを Clear Virtual Link で切る。
func (s *Switch) SetPortEnabled(idx int, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx < 0 || idx >= len(s.ports) {
		return fmt.Errorf("ポート %d は無い", idx)
	}
	p := s.ports[idx]
	if p.Enabled == enabled {
		return nil
	}
	if enabled {
		p.Enabled = true
		s.event("port-enable", idx, "ポート %d（%s）を有効にした", idx, p.Name)
		s.sendAdv(p, nil, false)
		return nil
	}
	if len(s.loginsOnPort(p.Index)) > 0 {
		p.needReset = true
	}
	s.clearPort(p, true, "ポートを無効にした")
	p.Enabled = false
	p.ENodeMAC = nil
	s.event("port-disable", idx, "ポート %d（%s）を無効にした", idx, p.Name)
	return nil
}

// Events は起きたことを新しいものから最大 n 件返す。
func (s *Switch) Events(n int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || n > len(s.events) {
		n = len(s.events)
	}
	out := make([]Event, 0, n)
	for i := len(s.events) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, s.events[i])
	}
	return out
}
