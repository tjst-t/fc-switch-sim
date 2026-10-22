package fabric

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"

	"github.com/tjst-t/fc-switch-sim/sim/fc"
	"github.com/tjst-t/fc-switch-sim/sim/fip"
)

// fakeTr は送ったフレームを貯める出口。
type fakeTr struct {
	mac  net.HardwareAddr
	sent [][]byte
}

func (f *fakeTr) Send(b []byte) error   { f.sent = append(f.sent, append([]byte(nil), b...)); return nil }
func (f *fakeTr) MAC() net.HardwareAddr { return f.mac }
func (f *fakeTr) take() [][]byte        { s := f.sent; f.sent = nil; return s }

// enode は試験用の ENode（Linux の fcoe がする順にフレームを作る）。
type enode struct {
	t     *testing.T
	sw    *Switch
	port  int
	tr    *fakeTr
	mac   net.HardwareAddr
	wwpn  fc.WWN
	fcid  fc.FCID
	vnmac net.HardwareAddr
	oxid  uint16
	rscns int // 受け取った RSCN の数
}

// split は届いたフレームから RSCN を数えて除く。
func (e *enode) split(out [][]byte) [][]byte {
	var rest [][]byte
	for _, b := range out {
		if f, err := fc.ParseFCoE(b); err == nil {
			if h, _ := fc.ParseHeader(f.FC); h.SID == fc.FIDFabricCtl && h.RCtl == fc.RCtlELSReq && f.FC[fc.HeaderLen] == fc.ELSRscn {
				e.rscns++
				continue
			}
		}
		rest = append(rest, b)
	}
	return rest
}

func (e *enode) takeRSCN() int {
	e.split(e.tr.take())
	n := e.rscns
	e.rscns = 0
	return n
}

func newTestSwitch(t *testing.T, n int, z Zoning) (*Switch, []*fakeTr) {
	cfg := Config{Name: "fab-a", SwitchWWN: 0x100000051e000001, DomainID: 1, Zoning: z}
	var trs []Transport
	var fakes []*fakeTr
	for i := 0; i < n; i++ {
		f := &fakeTr{mac: net.HardwareAddr{0x02, 0xfc, 0, 0, 0, byte(i + 1)}}
		cfg.Ports = append(cfg.Ports, PortConfig{Interface: "eth" + string(rune('1'+i))})
		trs = append(trs, f)
		fakes = append(fakes, f)
	}
	sw, err := New(cfg, trs, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sw, fakes
}

func (e *enode) fip(p fip.Packet, dst net.HardwareAddr) [][]byte {
	e.sw.Receive(e.port, p.Build(dst, e.mac, 0))
	return e.tr.take()
}

func (e *enode) login(fcpTarget bool) {
	t := e.t
	// 発見
	out := e.fip(fip.Packet{Op: fip.OpDisc, Sub: fip.SubDiscSol, Flags: fip.FlagFPMA,
		Descs: []fip.Descriptor{fip.MAC(e.mac), fip.Name(e.wwpn - 1), {Type: fip.DTFCoESize, Value: []byte{0x09, 0x00}}}}, fip.AllFCFMACs)
	if len(out) != 1 {
		t.Fatalf("広告が 1 つ返らない：%d", len(out))
	}
	adv, err := fip.Parse(out[0][14:])
	if err != nil || adv.Op != fip.OpDisc || adv.Sub != fip.SubDiscAdv || adv.Flags&(fip.FlagSol|fip.FlagAvail) != fip.FlagSol|fip.FlagAvail {
		t.Fatalf("求めた広告がおかしい：%+v %v", adv, err)
	}
	if len(out[0]) != 14+0x900 {
		t.Fatalf("広告が最大の長さまで詰められていない：%d", len(out[0]))
	}
	for _, d := range []byte{fip.DTPri, fip.DTMAC, fip.DTName, fip.DTFab, fip.DTFKA} {
		if _, ok := adv.Find(d); !ok {
			t.Fatalf("広告に記述子 %d が無い", d)
		}
	}
	// FLOGI
	pl := make([]byte, fc.ServiceParamsLen)
	pl[0] = fc.ELSFlogi
	binary.BigEndian.PutUint16(pl[10:12], 2048)
	binary.BigEndian.PutUint64(pl[20:28], uint64(e.wwpn))
	binary.BigEndian.PutUint64(pl[28:36], uint64(e.wwpn-1))
	hdr := fc.Header{RCtl: fc.RCtlELSReq, DID: fc.FIDFlogi, Type: fc.TypeELS, FCtl: fc.FCtlReq, OXID: 0x77, RXID: 0xFFFF}
	out = e.fip(fip.Packet{Op: fip.OpLS, Sub: fip.SubLSReq, Flags: fip.FlagFPMA,
		Descs: []fip.Descriptor{fip.ELS(fip.DTFLOGI, fc.Frame{Header: hdr, Payload: pl}.Bytes()), fip.MAC(make(net.HardwareAddr, 6))}}, e.tr.mac)
	if len(out) != 1 {
		t.Fatalf("FLOGI の応答が 1 つ返らない：%d", len(out))
	}
	rep, _ := fip.Parse(out[0][14:])
	v, _ := rep.Find(fip.DTFLOGI)
	raw, _ := fip.ReadELS(v)
	rh, _ := fc.ParseHeader(raw)
	if raw[fc.HeaderLen] != fc.ELSLsAcc || rh.OXID != 0x77 || rh.SID != fc.FIDFlogi {
		t.Fatalf("FLOGI の LS_ACC でない：%+v", rh)
	}
	if binary.BigEndian.Uint16(raw[fc.HeaderLen+8:])&fc.SPFeatFPort == 0 {
		t.Fatalf("F_Port の印が無い")
	}
	mv, _ := rep.Find(fip.DTMAC)
	e.vnmac, _ = fip.ReadMAC(mv)
	e.fcid = rh.DID
	if !bytes.Equal(e.vnmac, fc.FPMA(fc.DefaultFCMap, e.fcid)) {
		t.Fatalf("FPMA が合わない：%s %s", e.vnmac, e.fcid)
	}
	// ネームサーバに PLOGI
	if r := e.els(fc.FIDDirServ, []byte{fc.ELSPlogi, 0, 0, 0}); r[0] != fc.ELSLsAcc {
		t.Fatalf("ネームサーバへの PLOGI が受理されない")
	}
	// RFT_ID：FCP
	body := make([]byte, 36)
	fc.PutFCID(body[1:4], e.fcid)
	binary.BigEndian.PutUint32(body[4+0:], 1<<fc.TypeFCP)
	if c, _ := e.ct(fc.NSRftID, body); c != fc.CTAccept {
		t.Fatalf("RFT_ID が受理されない")
	}
	feat := byte(0x02)
	if fcpTarget {
		feat = 0x01
	}
	body = make([]byte, 8)
	fc.PutFCID(body[1:4], e.fcid)
	body[6], body[7] = feat, fc.TypeFCP
	if c, _ := e.ct(fc.NSRffID, body); c != fc.CTAccept {
		t.Fatalf("RFF_ID が受理されない")
	}
	// SCR
	if r := e.els(fc.FIDFabricCtl, []byte{fc.ELSScr, 0, 0, 0, 0, 0, 0, 3}); r[0] != fc.ELSLsAcc {
		t.Fatalf("SCR が受理されない")
	}
}

func (e *enode) send(did fc.FCID, rctl, typ byte, payload []byte) [][]byte {
	e.oxid++
	hdr := fc.Header{RCtl: rctl, DID: did, SID: e.fcid, Type: typ, FCtl: fc.FCtlReq, OXID: e.oxid, RXID: 0xFFFF}
	e.sw.Receive(e.port, fc.BuildFCoE(e.tr.mac, e.vnmac, fc.SOFi3, fc.EOFt, fc.Frame{Header: hdr, Payload: payload}.Bytes()))
	return e.split(e.tr.take())
}

func (e *enode) reply(did fc.FCID, rctl, typ byte, payload []byte) []byte {
	out := e.send(did, rctl, typ, payload)
	if len(out) != 1 {
		e.t.Fatalf("%s への応答が 1 つでない：%d", did, len(out))
	}
	f, err := fc.ParseFCoE(out[0])
	if err != nil || !f.CRCOK || !bytes.Equal(f.Dst, e.vnmac) {
		e.t.Fatalf("応答のフレームがおかしい：%v", err)
	}
	return f.FC[fc.HeaderLen:]
}

func (e *enode) els(did fc.FCID, payload []byte) []byte {
	return e.reply(did, fc.RCtlELSReq, fc.TypeELS, payload)
}

func (e *enode) ct(cmd uint16, body []byte) (uint16, []byte) {
	req := make([]byte, fc.CTHeaderLen+len(body))
	req[0] = 1
	req[4], req[5] = fc.GSTypeDirectory, fc.GSSubNameServer
	binary.BigEndian.PutUint16(req[8:10], cmd)
	copy(req[fc.CTHeaderLen:], body)
	r := e.reply(fc.FIDDirServ, fc.RCtlUnsolCtl, fc.TypeCT, req)
	h, b, _ := fc.ParseCT(r)
	return h.Command, b
}

func (e *enode) gpnFT() []fc.WWN {
	code, b := e.ct(fc.NSGpnFT, []byte{0, 0, 0, fc.TypeFCP})
	if code != fc.CTAccept {
		return nil
	}
	var out []fc.WWN
	for i := 0; i+16 <= len(b); i += 16 {
		out = append(out, fc.WWN(binary.BigEndian.Uint64(b[i+8:])))
	}
	return out
}

func newENode(t *testing.T, sw *Switch, trs []*fakeTr, port int, wwpn fc.WWN) *enode {
	return &enode{t: t, sw: sw, port: port, tr: trs[port], mac: net.HardwareAddr{0x52, 0x54, 0, 0, 0, byte(port + 1)}, wwpn: wwpn}
}

const (
	hostWWPN  = fc.WWN(0x2000525400000001)
	tapeWWPN  = fc.WWN(0x2000525400000002)
	otherWWPN = fc.WWN(0x2000525400000003)
)

func TestLoginAndDiscovery(t *testing.T) {
	sw, trs := newTestSwitch(t, 2, Zoning{})
	host := newENode(t, sw, trs, 0, hostWWPN)
	tape := newENode(t, sw, trs, 1, tapeWWPN)
	host.login(false)
	tape.login(true)
	// FLOGI・RFT_ID・RFF_ID のそれぞれで RSCN（登録のあとにも知らせないと、相手が発見できない）
	if n := host.takeRSCN(); n != 3 {
		t.Fatalf("tape のログインと登録の RSCN が host に 3 つ届くはず：%d", n)
	}
	if host.fcid != 0x010000 || tape.fcid != 0x010100 {
		t.Fatalf("FC_ID がおかしい：%s %s", host.fcid, tape.fcid)
	}
	got := host.gpnFT()
	if len(got) != 2 {
		t.Fatalf("GPN_FT が 2 件でない：%v", got)
	}
	// host から tape へ PLOGI：tape のポートへ転送される。
	out := host.send(tape.fcid, fc.RCtlELSReq, fc.TypeELS, []byte{fc.ELSPlogi, 0, 0, 0})
	if len(out) != 0 {
		t.Fatalf("host のポートに何か返った")
	}
	fwd := tape.tr.take()
	if len(fwd) != 1 {
		t.Fatalf("tape へ転送されない")
	}
	f, _ := fc.ParseFCoE(fwd[0])
	if !bytes.Equal(f.Dst, tape.vnmac) || !bytes.Equal(f.Src, trs[1].mac) || !f.CRCOK {
		t.Fatalf("転送のフレームの MAC がおかしい：%s %s", f.Dst, f.Src)
	}
}

func TestZoning(t *testing.T) {
	sw, trs := newTestSwitch(t, 3, Zoning{
		DefaultZone: DefaultZoneNone,
		Aliases:     map[string][]string{"host1": {hostWWPN.String()}},
		Zones:       []Zone{{Name: "z_host1_tape1", Members: []string{"host1", tapeWWPN.String()}}},
	})
	host := newENode(t, sw, trs, 0, hostWWPN)
	tape := newENode(t, sw, trs, 1, tapeWWPN)
	other := newENode(t, sw, trs, 2, otherWWPN)
	host.login(false)
	tape.login(true)
	other.login(true)
	if got := host.gpnFT(); len(got) != 2 {
		t.Fatalf("host に見えるのは自分と tape のはず：%v", got)
	}
	if got := other.gpnFT(); len(got) != 1 || got[0] != otherWWPN {
		t.Fatalf("other には自分だけ見えるはず：%v", got)
	}
	// other から tape へはゾーニングで捨てる。
	other.send(tape.fcid, fc.RCtlELSReq, fc.TypeELS, []byte{fc.ELSPlogi, 0, 0, 0})
	if len(tape.tr.take()) != 0 {
		t.Fatalf("ゾーンの外から届いた")
	}
	// GPN_ID も見えない。
	body := make([]byte, 4)
	fc.PutFCID(body[1:4], tape.fcid)
	if c, _ := other.ct(fc.NSGpnID, body); c != fc.CTReject {
		t.Fatalf("ゾーンの外の GPN_ID が拒否されない")
	}
	// ゾーンを外すと、RSCN が SCR 済みの全部に届き、host からも tape が見えなくなる。
	for _, e := range []*enode{host, tape, other} {
		e.takeRSCN()
	}
	if err := sw.SetZoning(Zoning{DefaultZone: DefaultZoneNone, Zones: []Zone{{Name: "z_other", Members: []string{otherWWPN.String(), tapeWWPN.String()}}}}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []*enode{host, tape, other} {
		if n := e.takeRSCN(); n != 1 {
			t.Fatalf("ゾーンの変更の RSCN が 1 つ届くはず（%s）：%d", e.wwpn, n)
		}
	}
	host.send(tape.fcid, fc.RCtlELSReq, fc.TypeELS, []byte{fc.ELSPlogi, 0, 0, 0})
	if len(tape.tr.take()) != 0 {
		t.Fatalf("ゾーンを外したのに届いた")
	}
	if got := other.gpnFT(); len(got) != 2 {
		t.Fatalf("other から tape が見えるはず：%v", got)
	}
}

func TestPortDisableSendsCVL(t *testing.T) {
	sw, trs := newTestSwitch(t, 2, Zoning{})
	host := newENode(t, sw, trs, 0, hostWWPN)
	tape := newENode(t, sw, trs, 1, tapeWWPN)
	host.login(false)
	tape.login(true)
	host.takeRSCN()
	if err := sw.SetPortEnabled(1, false); err != nil {
		t.Fatal(err)
	}
	out := tape.tr.take()
	if len(out) != 1 {
		t.Fatalf("CVL が届かない")
	}
	p, _ := fip.Parse(out[0][14:])
	if p.Op != fip.OpCtrl || p.Sub != fip.SubClearVLink {
		t.Fatalf("CVL でない：%+v", p)
	}
	// host には tape のログアウトの RSCN。
	if host.takeRSCN() != 1 {
		t.Fatalf("host に RSCN が届かない")
	}
	if len(sw.NameServer()) != 1 {
		t.Fatalf("ネームサーバに tape が残っている")
	}
	// 無効のポートは発見に答えない。
	tape.fip(fip.Packet{Op: fip.OpDisc, Sub: fip.SubDiscSol, Descs: []fip.Descriptor{fip.MAC(tape.mac)}}, fip.AllFCFMACs)
	if len(trs[1].sent) != 0 {
		t.Fatalf("無効のポートが答えた")
	}
}

func TestVNPortKeepAliveDoesNotChangeENodeMAC(t *testing.T) {
	sw, trs := newTestSwitch(t, 1, Zoning{})
	host := newENode(t, sw, trs, 0, hostWWPN)
	host.login(false)
	// ポートを無効・有効にすると ENode の MAC を忘れる。
	_ = sw.SetPortEnabled(0, false)
	_ = sw.SetPortEnabled(0, true)
	trs[0].take()
	// VN_Port のキープアライブ（送り元は VN_Port の MAC、MAC の記述子は ENode の MAC）
	host.sw.Receive(0, fip.Packet{Op: fip.OpCtrl, Sub: fip.SubKeepAlive,
		Descs: []fip.Descriptor{fip.MAC(host.mac), fip.VNID(host.vnmac, host.fcid, host.wwpn)}}.Build(trs[0].mac, host.vnmac, 0))
	if got := sw.Ports()[0].ENodeMAC; got != host.mac.String() {
		t.Fatalf("ENode の MAC が %s になった（%s のはず）", got, host.mac)
	}
}

func TestReenabledPortResetsStaleENode(t *testing.T) {
	sw, trs := newTestSwitch(t, 1, Zoning{})
	host := newENode(t, sw, trs, 0, hostWWPN)
	host.login(false)
	_ = sw.SetPortEnabled(0, false)
	_ = sw.SetPortEnabled(0, true)
	trs[0].take()
	// CVL を取りこぼした ENode が、まだログインしているつもりで ENode のキープアライブを送る
	sw.Receive(0, fip.Packet{Op: fip.OpCtrl, Sub: fip.SubKeepAlive, Descs: []fip.Descriptor{fip.MAC(host.mac)}}.Build(trs[0].mac, host.mac, 0))
	out := trs[0].take()
	if len(out) != 1 {
		t.Fatalf("CVL が 1 つ返るはず：%d", len(out))
	}
	if p, _ := fip.Parse(out[0][14:]); p.Op != fip.OpCtrl || p.Sub != fip.SubClearVLink {
		t.Fatalf("CVL でない")
	}
	// VN_Port のキープアライブにも、その VN_Port の CVL
	sw.Receive(0, fip.Packet{Op: fip.OpCtrl, Sub: fip.SubKeepAlive, Descs: []fip.Descriptor{fip.MAC(host.mac), fip.VNID(host.vnmac, host.fcid, host.wwpn)}}.Build(trs[0].mac, host.vnmac, 0))
	out = trs[0].take()
	if len(out) != 1 {
		t.Fatalf("VN_Port の CVL が返らない")
	}
	p, _ := fip.Parse(out[0][14:])
	if _, ok := p.Find(fip.DTVNID); !ok {
		t.Fatalf("VN_Port の記述子が無い")
	}
}
