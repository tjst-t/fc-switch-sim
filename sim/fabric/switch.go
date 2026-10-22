package fabric

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/tjst-t/fc-switch-sim/sim/fc"
	"github.com/tjst-t/fc-switch-sim/sim/fip"
)

// Transport はスイッチの 1 ポートの Ethernet の出口。
type Transport interface {
	Send(frame []byte) error
	MAC() net.HardwareAddr
}

// Port はスイッチのポート。
type Port struct {
	Index   int
	Name    string
	tr      Transport
	Enabled bool

	// ポートの先の ENode（FIP の発見で分かる）。
	ENodeMAC     net.HardwareAddr
	ENodeWWNN    fc.WWN
	ENodeMaxSize uint16
	lastENodeKA  time.Time
	lastAdv      time.Time
	lastCVL      time.Time

	Counters PortCounters
}

// PortCounters はポートの数え。
type PortCounters struct {
	RxFIP        uint64 `json:"rxFIP"`
	RxFCoE       uint64 `json:"rxFCoE"`
	TxFIP        uint64 `json:"txFIP"`
	TxFCoE       uint64 `json:"txFCoE"`
	Forwarded    uint64 `json:"forwarded"`    // 他のポートへ転送した（受けた側で数える）
	ZoneDrops    uint64 `json:"zoneDrops"`    // ゾーニングで捨てた
	UnknownDrops uint64 `json:"unknownDrops"` // 宛先が無い・送り元が正しくない
	CRCErrors    uint64 `json:"crcErrors"`
	TxErrors     uint64 `json:"txErrors"`
	KernelDrops  uint64 `json:"kernelDrops"` // 受け取りきれずにカーネルで捨てた
}

// Login はファブリックにログインした VN_Port（N_Port）。
type Login struct {
	FCID      fc.FCID
	WWPN      fc.WWN
	WWNN      fc.WWN
	Port      int
	ENodeMAC  net.HardwareAddr
	VNMAC     net.HardwareAddr
	NPIV      bool // FDISC でログインした
	LoginTime time.Time
	MaxSize   uint16

	FC4Types    [32]byte
	FC4Features map[byte]byte
	SymPortName string
	SymNodeName string
	SCR         byte // 0 なら RSCN を受け取らない
}

// hasType は FC-4 の種類 t を登録しているか（FC-GS の 32 バイトの表：語ごとに大きいほうから）。
func (l *Login) hasType(t byte) bool {
	word := int(t) / 32
	bit := uint(t) % 32
	v := binary.BigEndian.Uint32(l.FC4Types[word*4 : word*4+4])
	return v&(1<<bit) != 0
}

// Event はスイッチで起きたこと（API で見せる）。
type Event struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Port    int       `json:"port"`
	Message string    `json:"message"`
}

const maxEvents = 1000

// Switch は FCF 1 台。
type Switch struct {
	mu     sync.Mutex
	cfg    Config
	ports  []*Port
	logins map[fc.FCID]*Login
	zoning Zoning
	zc     compiledZoning
	events []Event
	rxid   uint16
	oxid   uint16
	log    *slog.Logger
	now    func() time.Time
}

// New はスイッチを作る。transports は cfg.Ports と同じ順。
func New(cfg Config, transports []Transport, log *slog.Logger) (*Switch, error) {
	if err := cfg.setDefaults(); err != nil {
		return nil, err
	}
	if len(transports) != len(cfg.Ports) {
		return nil, fmt.Errorf("ポートの数（%d）と出口の数（%d）が合わない", len(cfg.Ports), len(transports))
	}
	zc, err := compileZoning(cfg.Zoning)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	s := &Switch{
		cfg:    cfg,
		logins: map[fc.FCID]*Login{},
		zoning: normalize(cfg.Zoning),
		zc:     zc,
		rxid:   0x1000,
		oxid:   0x2000,
		log:    log,
		now:    time.Now,
	}
	for i, pc := range cfg.Ports {
		s.ports = append(s.ports, &Port{Index: i, Name: pc.Interface, tr: transports[i], Enabled: !pc.Disabled})
	}
	return s, nil
}

func (s *Switch) event(kind string, port int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.log.Info(msg, "kind", kind, "port", port)
	s.events = append(s.events, Event{Time: s.now(), Kind: kind, Port: port, Message: msg})
	if len(s.events) > maxEvents {
		s.events = s.events[len(s.events)-maxEvents:]
	}
}

func (s *Switch) send(p *Port, frame []byte, fcoe bool) {
	if err := p.tr.Send(frame); err != nil {
		p.Counters.TxErrors++
		s.log.Debug("送れなかった", "port", p.Index, "err", err)
		return
	}
	if fcoe {
		p.Counters.TxFCoE++
	} else {
		p.Counters.TxFIP++
	}
}

func (s *Switch) nextRXID() uint16 {
	s.rxid++
	if s.rxid == 0xFFFF {
		s.rxid = 0x1000
	}
	return s.rxid
}

func (s *Switch) nextOXID() uint16 {
	s.oxid++
	if s.oxid == 0xFFFF {
		s.oxid = 0x2000
	}
	return s.oxid
}

// Receive はポート idx に届いた Ethernet フレームを処理する。
func (s *Switch) Receive(idx int, eth []byte) {
	if len(eth) < 14 || idx < 0 || idx >= len(s.ports) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.ports[idx]
	switch binary.BigEndian.Uint16(eth[12:14]) {
	case fc.EtherTypeFIP:
		p.Counters.RxFIP++
		if !p.Enabled {
			return
		}
		pkt, err := fip.Parse(eth[14:])
		if err != nil {
			s.log.Debug("FIP を読めない", "port", idx, "err", err)
			return
		}
		s.handleFIP(p, net.HardwareAddr(eth[6:12]), pkt)
	case fc.EtherTypeFCoE:
		p.Counters.RxFCoE++
		if !p.Enabled {
			return
		}
		s.handleFCoE(p, eth)
	}
}

// Tick は時間で動くこと（広告・キープアライブの見張り）をする。1 秒ごとくらいに呼ぶ。
func (s *Switch) Tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	period := s.cfg.fkaPeriod()
	for _, p := range s.ports {
		if !p.Enabled {
			continue
		}
		if now.Sub(p.lastAdv) >= period {
			s.sendAdv(p, nil, false)
		}
		if s.cfg.ENodeTimeoutFactor > 0 && p.ENodeMAC != nil && !p.lastENodeKA.IsZero() {
			limit := time.Duration(float64(period) * s.cfg.ENodeTimeoutFactor)
			if now.Sub(p.lastENodeKA) > limit && len(s.loginsOnPort(p.Index)) > 0 {
				s.event("enode-timeout", p.Index, "ENode %s のキープアライブが %v 途絶えたのでログアウトさせる", p.ENodeMAC, limit)
				s.clearPort(p, true, "キープアライブが途絶えた")
			}
		}
	}
}

// ---- FIP ----

func (s *Switch) sendAdv(p *Port, to net.HardwareAddr, solicited bool) {
	flags := uint16(fip.FlagFPMA | fip.FlagFPort | fip.FlagAvail)
	dst := fip.AllENodeMACs
	pad := 0
	if solicited {
		flags |= fip.FlagSol
		dst = to
		if !s.cfg.NoPadSolicitedAdv {
			pad = int(p.ENodeMaxSize)
		}
	}
	pkt := fip.Packet{
		Op: fip.OpDisc, Sub: fip.SubDiscAdv, Flags: flags,
		Descs: []fip.Descriptor{
			fip.Pri(s.cfg.Priority),
			fip.MAC(p.tr.MAC()),
			fip.Name(s.cfg.SwitchWWN),
			fip.Fab(0, s.cfg.FCMap, s.cfg.FabricName),
			fip.FKA(s.cfg.FKAPeriodMs),
		},
	}
	s.send(p, pkt.Build(dst, p.tr.MAC(), pad), false)
	if !solicited {
		p.lastAdv = s.now()
	}
}

func (s *Switch) handleFIP(p *Port, src net.HardwareAddr, pkt fip.Packet) {
	switch {
	case pkt.Op == fip.OpDisc && pkt.Sub == fip.SubDiscSol:
		mac := src
		if v, ok := pkt.Find(fip.DTMAC); ok {
			if m, ok := fip.ReadMAC(v); ok {
				mac = m
			}
		}
		if v, ok := pkt.Find(fip.DTName); ok {
			p.ENodeWWNN, _ = fip.ReadName(v)
		}
		if v, ok := pkt.Find(fip.DTFCoESize); ok {
			p.ENodeMaxSize, _ = fip.ReadSize(v)
		}
		if p.ENodeMAC == nil || !bytes.Equal(p.ENodeMAC, mac) {
			s.event("enode-discovered", p.Index, "ENode %s（WWNN %s、最大 %d バイト）を見つけた", mac, p.ENodeWWNN, p.ENodeMaxSize)
		}
		p.ENodeMAC = mac
		p.lastENodeKA = s.now()
		s.sendAdv(p, mac, true)
	case pkt.Op == fip.OpLS && pkt.Sub == fip.SubLSReq:
		s.handleLSReq(p, src, pkt)
	case pkt.Op == fip.OpCtrl && pkt.Sub == fip.SubKeepAlive:
		if p.ENodeMAC == nil {
			// スイッチが再起動したあと：キープアライブで ENode を知る。
			p.ENodeMAC = src
		}
		if bytes.Equal(src, p.ENodeMAC) {
			p.lastENodeKA = s.now()
		}
	case pkt.Op == fip.OpVLAN && pkt.Sub == fip.SubVLANReq:
		// VLAN は使わない（タグ無しの FCoE）。ENode 側は VLAN の自動発見を切っておく。
		s.log.Debug("VLAN の発見の要求は無視する", "port", p.Index, "src", src)
	default:
		s.log.Debug("扱わない FIP", "port", p.Index, "op", pkt.Op, "sub", pkt.Sub)
	}
}

func (s *Switch) handleLSReq(p *Port, src net.HardwareAddr, pkt fip.Packet) {
	var dtype byte
	var raw []byte
	for _, t := range []byte{fip.DTFLOGI, fip.DTFDISC, fip.DTLOGO} {
		if v, ok := pkt.Find(t); ok {
			dtype = t
			raw, _ = fip.ReadELS(v)
			break
		}
	}
	if raw == nil {
		s.log.Debug("FIP の LS 要求に ELS が無い", "port", p.Index)
		return
	}
	h, err := fc.ParseHeader(raw)
	if err != nil {
		return
	}
	payload := raw[fc.HeaderLen:]
	if len(payload) == 0 {
		return
	}
	reply := func(fr fc.Frame, mac net.HardwareAddr) {
		descs := []fip.Descriptor{fip.ELS(dtype, fr.Bytes())}
		if mac != nil {
			descs = append(descs, fip.MAC(mac))
		}
		out := fip.Packet{Op: fip.OpLS, Sub: fip.SubLSRep, Flags: fip.FlagFPMA, Descs: descs}
		s.send(p, out.Build(src, p.tr.MAC(), 0), false)
	}
	reject := func(reason, exp byte) {
		hdr := fc.ReplyHeader(h, fc.RCtlELSRep, s.nextRXID())
		hdr.SID = fc.FIDFlogi
		reply(fc.Frame{Header: hdr, Payload: fc.LsRjt(reason, exp)}, nil)
	}
	p.ENodeMAC = src
	p.lastENodeKA = s.now()

	switch dtype {
	case fip.DTFLOGI, fip.DTFDISC:
		lp, ok := fc.ParseLoginParams(payload)
		if !ok {
			reject(fc.RjtLogicalError, fc.RjtExpNone)
			return
		}
		base := fc.FCID(uint32(s.cfg.DomainID)<<16 | uint32(p.Index)<<8)
		var id fc.FCID
		if dtype == fip.DTFLOGI {
			// FLOGI はそのポートの N_Port をやり直す：前のログインは全部消す。
			s.clearPort(p, false, "同じポートで FLOGI し直した")
			id = base
		} else {
			if s.logins[base] == nil {
				reject(fc.RjtLogicalError, fc.RjtExpNone)
				return
			}
			id = s.freeNPIV(p.Index)
			if id == 0 {
				reject(fc.RjtUnableToPerf, fc.RjtExpNone)
				return
			}
		}
		// 同じ WWPN が他で残っていれば消す（WWPN は 1 つのファブリックで重ならない）。
		for _, l := range s.sortedLogins() {
			if l.WWPN == lp.WWPN {
				s.removeLogin(l, "同じ WWPN で別にログインした")
			}
		}
		l := &Login{
			FCID: id, WWPN: lp.WWPN, WWNN: lp.WWNN, Port: p.Index,
			ENodeMAC: src, VNMAC: fc.FPMA(s.cfg.FCMap, id), NPIV: dtype == fip.DTFDISC,
			LoginTime: s.now(), MaxSize: lp.BBData, FC4Features: map[byte]byte{},
		}
		s.logins[id] = l
		hdr := fc.ReplyHeader(h, fc.RCtlELSRep, s.nextRXID())
		hdr.DID = id
		hdr.SID = fc.FIDFlogi
		acc := fc.BuildLoginAcc(s.cfg.PortWWN(p.Index), s.cfg.SwitchWWN, s.cfg.MaxPayload, true)
		reply(fc.Frame{Header: hdr, Payload: acc}, l.VNMAC)
		kind := "FLOGI"
		if l.NPIV {
			kind = "FDISC"
		}
		s.event("login", p.Index, "%s：WWPN %s に FC_ID %s を割り当てた", kind, l.WWPN, id)
		s.rscnFor(l)
	case fip.DTLOGO:
		l := s.logins[h.SID]
		hdr := fc.ReplyHeader(h, fc.RCtlELSRep, s.nextRXID())
		if l == nil || l.Port != p.Index {
			reply(fc.Frame{Header: hdr, Payload: fc.LsRjt(fc.RjtUnableToPerf, fc.RjtExpNone)}, nil)
			return
		}
		vn := l.VNMAC
		s.removeLogin(l, "LOGO（FIP）")
		reply(fc.Frame{Header: hdr, Payload: fc.LsAcc()}, vn)
	}
}

func (s *Switch) freeNPIV(port int) fc.FCID {
	for alpa := 1; alpa < 256; alpa++ {
		id := fc.FCID(uint32(s.cfg.DomainID)<<16 | uint32(port)<<8 | uint32(alpa))
		if s.logins[id] == nil {
			return id
		}
	}
	return 0
}

func (s *Switch) loginsOnPort(port int) []*Login {
	var out []*Login
	for _, l := range s.sortedLogins() {
		if l.Port == port {
			out = append(out, l)
		}
	}
	return out
}

// clearPort はポートのログインを全部消す。cvl なら ENode に Clear Virtual Link を送る。
func (s *Switch) clearPort(p *Port, cvl bool, why string) {
	ls := s.loginsOnPort(p.Index)
	if len(ls) == 0 {
		return
	}
	if cvl && p.ENodeMAC != nil {
		descs := []fip.Descriptor{fip.MAC(p.tr.MAC()), fip.Name(s.cfg.SwitchWWN)}
		for _, l := range ls {
			descs = append(descs, fip.VNID(l.VNMAC, l.FCID, l.WWPN))
		}
		pkt := fip.Packet{Op: fip.OpCtrl, Sub: fip.SubClearVLink, Descs: descs}
		s.send(p, pkt.Build(p.ENodeMAC, p.tr.MAC(), 0), false)
	}
	for _, l := range ls {
		s.removeLogin(l, why)
	}
}

// resetUnknownENode は、ログインしていない VN_Port から届いたとき（スイッチの再起動のあとなど）、
// その ENode に VN_Port の記述子の無い Clear Virtual Link を送り、全部のログインをやり直させる（FC-BB-6）。
func (s *Switch) resetUnknownENode(p *Port) {
	if p.ENodeMAC == nil || len(s.loginsOnPort(p.Index)) > 0 || s.now().Sub(p.lastCVL) < 2*time.Second {
		return
	}
	p.lastCVL = s.now()
	pkt := fip.Packet{Op: fip.OpCtrl, Sub: fip.SubClearVLink, Descs: []fip.Descriptor{fip.MAC(p.tr.MAC()), fip.Name(s.cfg.SwitchWWN)}}
	s.send(p, pkt.Build(p.ENodeMAC, p.tr.MAC(), 0), false)
	s.event("clear-vlink", p.Index, "ログインしていない VN_Port から届いたので、ENode %s にやり直しを求めた", p.ENodeMAC)
}

func (s *Switch) removeLogin(l *Login, why string) {
	if s.logins[l.FCID] != l {
		return
	}
	delete(s.logins, l.FCID)
	s.event("logout", l.Port, "WWPN %s（FC_ID %s）がログアウトした：%s", l.WWPN, l.FCID, why)
	s.rscnFor(l)
}

// ---- FCoE（データの転送と、ファブリックのサービス） ----

func (s *Switch) handleFCoE(p *Port, eth []byte) {
	f, err := fc.ParseFCoE(eth)
	if err != nil {
		return
	}
	if !bytes.Equal(f.Dst, p.tr.MAC()) {
		return
	}
	if !f.CRCOK {
		p.Counters.CRCErrors++
		return
	}
	h, err := fc.ParseHeader(f.FC)
	if err != nil {
		return
	}
	src := s.logins[h.SID]
	if src == nil || src.Port != p.Index || !bytes.Equal(src.VNMAC, f.Src) {
		p.Counters.UnknownDrops++
		s.log.Debug("ログインしていない送り元", "port", p.Index, "sid", h.SID, "mac", f.Src)
		s.resetUnknownENode(p)
		return
	}
	if h.DID.IsWellKnown() {
		s.handleService(src, h, f.FC[fc.HeaderLen:])
		return
	}
	dst := s.logins[h.DID]
	if dst == nil {
		p.Counters.UnknownDrops++
		return
	}
	if !s.zc.canSee(src.WWPN, dst.WWPN) {
		p.Counters.ZoneDrops++
		return
	}
	dp := s.ports[dst.Port]
	if !dp.Enabled {
		p.Counters.UnknownDrops++
		return
	}
	out := make([]byte, len(eth))
	copy(out, eth)
	copy(out[0:6], dst.VNMAC)
	copy(out[6:12], dp.tr.MAC())
	p.Counters.Forwarded++
	s.send(dp, out, true)
}

// sendFC はファブリックのサービスからログインした VN_Port へ FC フレームを送る。
func (s *Switch) sendFC(l *Login, hdr fc.Header, payload []byte) {
	p := s.ports[l.Port]
	frame := fc.Frame{Header: hdr, Payload: payload}.Bytes()
	s.send(p, fc.BuildFCoE(l.VNMAC, p.tr.MAC(), fc.SOFi3, fc.EOFt, frame), true)
}

func (s *Switch) handleService(src *Login, h fc.Header, payload []byte) {
	// ABTS には BA_ACC を返す。
	if h.RCtl == fc.RCtlBAAbts {
		hdr := fc.ReplyHeader(h, fc.RCtlBAAcc, h.RXID)
		hdr.Type = fc.TypeBLS
		s.sendFC(src, hdr, fc.BAAcc(h.OXID, h.RXID))
		return
	}
	switch h.Type {
	case fc.TypeELS:
		if h.RCtl != fc.RCtlELSReq || len(payload) == 0 {
			return // RSCN への LS_ACC など、応答は受け取るだけ
		}
		s.handleServiceELS(src, h, payload)
	case fc.TypeCT:
		if h.RCtl != fc.RCtlUnsolCtl {
			return
		}
		if h.DID != fc.FIDDirServ {
			if ct, _, err := fc.ParseCT(payload); err == nil {
				s.sendFC(src, fc.ReplyHeader(h, fc.RCtlSolCtl, s.nextRXID()), fc.BuildCTResp(ct, fc.CTReject, fc.CTRjtNotSupported, fc.CTExpNone, nil))
			}
			return
		}
		s.handleNameServer(src, h, payload)
	}
}

func (s *Switch) handleServiceELS(src *Login, h fc.Header, payload []byte) {
	hdr := fc.ReplyHeader(h, fc.RCtlELSRep, s.nextRXID())
	rjt := func(reason byte) { s.sendFC(src, hdr, fc.LsRjt(reason, fc.RjtExpNone)) }
	switch payload[0] {
	case fc.ELSPlogi:
		switch h.DID {
		case fc.FIDDirServ, fc.FIDFabricCtl:
			s.sendFC(src, hdr, fc.BuildLoginAcc(s.cfg.SwitchWWN, s.cfg.SwitchWWN, s.cfg.MaxPayload, false))
		default:
			rjt(fc.RjtNotSupported)
		}
	case fc.ELSLogo:
		s.sendFC(src, hdr, fc.LsAcc())
		if h.DID == fc.FIDFlogi {
			s.removeLogin(src, "LOGO")
		}
	case fc.ELSScr:
		if h.DID != fc.FIDFabricCtl || len(payload) < 8 {
			rjt(fc.RjtNotSupported)
			return
		}
		src.SCR = payload[7]
		s.sendFC(src, hdr, fc.LsAcc())
	case fc.ELSEcho:
		acc := append([]byte(nil), payload...)
		acc[0], acc[1], acc[2], acc[3] = fc.ELSLsAcc, 0, 0, 0
		s.sendFC(src, hdr, acc)
	default:
		rjt(fc.RjtNotSupported)
	}
}

// ---- RSCN ----

// rscnFor は l の変化を、RSCN を受け取りたいと言っていて l が見えるポートに知らせる。
func (s *Switch) rscnFor(l *Login) {
	for _, t := range s.sortedLogins() {
		if t == l || t.SCR == 0 || !s.zc.canSee(t.WWPN, l.WWPN) {
			continue
		}
		s.sendRSCN(t, []fc.RSCNPage{{Format: fc.RSCNPort, ID: l.FCID}})
	}
}

// rscnAll はファブリック全体の変化（ゾーニングの変更など）を全部に知らせる。
func (s *Switch) rscnAll() {
	for _, t := range s.sortedLogins() {
		if t.SCR != 0 {
			s.sendRSCN(t, []fc.RSCNPage{{Format: fc.RSCNFabric, ID: 0}})
		}
	}
}

func (s *Switch) sendRSCN(t *Login, pages []fc.RSCNPage) {
	hdr := fc.Header{
		RCtl: fc.RCtlELSReq, DID: t.FCID, SID: fc.FIDFabricCtl, Type: fc.TypeELS,
		FCtl: fc.FCtlReq, OXID: s.nextOXID(), RXID: 0xFFFF,
	}
	s.sendFC(t, hdr, fc.BuildRSCN(pages))
}

func (s *Switch) sortedLogins() []*Login {
	out := make([]*Login, 0, len(s.logins))
	for _, l := range s.logins {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FCID < out[j].FCID })
	return out
}
