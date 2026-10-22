// Package fip は FCoE Initialization Protocol（FC-BB-5）のフレームを読み書きする。
package fip

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/tjst-t/fc-switch-sim/sim/fc"
)

// 届け先の決まった Ethernet のマルチキャスト。
var (
	AllENodeMACs = net.HardwareAddr{0x01, 0x10, 0x18, 0x01, 0x00, 0x01}
	AllFCFMACs   = net.HardwareAddr{0x01, 0x10, 0x18, 0x01, 0x00, 0x02}
)

// Protocol Code と Subcode。
const (
	OpDisc  = 0x0001
	OpLS    = 0x0002
	OpCtrl  = 0x0003
	OpVLAN  = 0x0004
	OpVN2VN = 0x0005

	SubDiscSol    = 0x01
	SubDiscAdv    = 0x02
	SubLSReq      = 0x01
	SubLSRep      = 0x02
	SubKeepAlive  = 0x01
	SubClearVLink = 0x02
	SubVLANReq    = 0x01
	SubVLANNote   = 0x02
)

// フラグ。
const (
	FlagFPMA  = 0x8000
	FlagSPMA  = 0x4000
	FlagAvail = 0x0004 // ログインを受け付ける
	FlagSol   = 0x0002 // 求めに応じた広告
	FlagFPort = 0x0001 // FCF（F_Port を出す）
)

// 記述子の種類。
const (
	DTPri      = 1
	DTMAC      = 2
	DTMapOUI   = 3
	DTName     = 4
	DTFab      = 5
	DTFCoESize = 6
	DTFLOGI    = 7
	DTFDISC    = 8
	DTLOGO     = 9
	DTELP      = 10
	DTVNID     = 11
	DTFKA      = 12
	DTVendor   = 13
	DTVLAN     = 14
)

// Descriptor は記述子 1 つ（種類と、長さの 2 バイトのあとの中身）。
type Descriptor struct {
	Type  byte
	Value []byte
}

// Packet は FIP のフレームの中身（Ethernet ヘッダを除く）。
type Packet struct {
	Op    uint16
	Sub   byte
	Flags uint16
	Descs []Descriptor
}

// Parse は Ethernet の中身（Ethertype のあと）を読む。
func Parse(b []byte) (Packet, error) {
	if len(b) < 10 {
		return Packet{}, fmt.Errorf("FIP が短い（%d バイト）", len(b))
	}
	if b[0]>>4 != 1 {
		return Packet{}, fmt.Errorf("FIP の版 %d は知らない", b[0]>>4)
	}
	p := Packet{
		Op:    binary.BigEndian.Uint16(b[2:4]),
		Sub:   b[5],
		Flags: binary.BigEndian.Uint16(b[8:10]),
	}
	dlen := int(binary.BigEndian.Uint16(b[6:8])) * 4
	d := b[10:]
	if dlen > len(d) {
		return Packet{}, fmt.Errorf("記述子の長さ %d が中身 %d を超える", dlen, len(d))
	}
	d = d[:dlen]
	for len(d) > 0 {
		if len(d) < 2 {
			return Packet{}, fmt.Errorf("記述子が途中で切れている")
		}
		n := int(d[1]) * 4
		if n < 4 || n > len(d) {
			return Packet{}, fmt.Errorf("記述子 %d の長さ %d が正しくない", d[0], n)
		}
		p.Descs = append(p.Descs, Descriptor{Type: d[0], Value: d[2:n]})
		d = d[n:]
	}
	return p, nil
}

// Find は最初の種類 t の記述子を返す。
func (p Packet) Find(t byte) ([]byte, bool) {
	for _, d := range p.Descs {
		if d.Type == t {
			return d.Value, true
		}
	}
	return nil, false
}

// Build は Ethernet フレームにする。pad が 0 より大きければ、Ethernet の中身をその長さまで詰める。
func (p Packet) Build(dst, src net.HardwareAddr, pad int) []byte {
	var desc []byte
	for _, d := range p.Descs {
		n := 2 + len(d.Value)
		if n%4 != 0 {
			panic(fmt.Sprintf("記述子 %d の長さ %d が 4 の倍数でない", d.Type, n))
		}
		desc = append(desc, d.Type, byte(n/4))
		desc = append(desc, d.Value...)
	}
	b := make([]byte, 14+10+len(desc))
	copy(b[0:6], dst)
	copy(b[6:12], src)
	binary.BigEndian.PutUint16(b[12:14], fc.EtherTypeFIP)
	h := b[14:]
	h[0] = 0x10
	binary.BigEndian.PutUint16(h[2:4], p.Op)
	h[5] = p.Sub
	binary.BigEndian.PutUint16(h[6:8], uint16(len(desc)/4))
	binary.BigEndian.PutUint16(h[8:10], p.Flags)
	copy(h[10:], desc)
	if pad > 0 && len(b)-14 < pad {
		b = append(b, make([]byte, pad-(len(b)-14))...)
	}
	if len(b) < 60 {
		b = append(b, make([]byte, 60-len(b))...)
	}
	return b
}

// 記述子を作る。

func Pri(pri byte) Descriptor { return Descriptor{DTPri, []byte{0, pri}} }

func MAC(m net.HardwareAddr) Descriptor {
	v := make([]byte, 6)
	copy(v, m)
	return Descriptor{DTMAC, v}
}

func Name(w fc.WWN) Descriptor {
	v := make([]byte, 10)
	binary.BigEndian.PutUint64(v[2:], uint64(w))
	return Descriptor{DTName, v}
}

func Fab(vfid uint16, fcmap uint32, fabric fc.WWN) Descriptor {
	v := make([]byte, 14)
	binary.BigEndian.PutUint16(v[0:2], vfid)
	v[3], v[4], v[5] = byte(fcmap>>16), byte(fcmap>>8), byte(fcmap)
	binary.BigEndian.PutUint64(v[6:], uint64(fabric))
	return Descriptor{DTFab, v}
}

func FKA(periodMs uint32) Descriptor {
	v := make([]byte, 6)
	binary.BigEndian.PutUint32(v[2:], periodMs)
	return Descriptor{DTFKA, v}
}

func VNID(mac net.HardwareAddr, id fc.FCID, wwpn fc.WWN) Descriptor {
	v := make([]byte, 18)
	copy(v[0:6], mac)
	fc.PutFCID(v[7:10], id)
	binary.BigEndian.PutUint64(v[10:], uint64(wwpn))
	return Descriptor{DTVNID, v}
}

// ELS は FLOGI・FDISC・LOGO の記述子（予約の 2 バイトのあとに FC フレーム）。
func ELS(t byte, frame []byte) Descriptor {
	v := make([]byte, 2+len(frame))
	copy(v[2:], frame)
	if pad := (2 + len(v)) % 4; pad != 0 {
		v = append(v, make([]byte, 4-pad)...)
	}
	return Descriptor{t, v}
}

// 記述子を読む。

func ReadMAC(v []byte) (net.HardwareAddr, bool) {
	if len(v) < 6 {
		return nil, false
	}
	return net.HardwareAddr(append([]byte(nil), v[:6]...)), true
}

func ReadName(v []byte) (fc.WWN, bool) {
	if len(v) < 10 {
		return 0, false
	}
	return fc.WWN(binary.BigEndian.Uint64(v[2:10])), true
}

func ReadSize(v []byte) (uint16, bool) {
	if len(v) < 2 {
		return 0, false
	}
	return binary.BigEndian.Uint16(v[0:2]), true
}

// VNIDValue は VN_Port の識別の記述子の中身。
type VNIDValue struct {
	MAC  net.HardwareAddr
	ID   fc.FCID
	WWPN fc.WWN
}

func ReadVNID(v []byte) (VNIDValue, bool) {
	if len(v) < 18 {
		return VNIDValue{}, false
	}
	return VNIDValue{
		MAC:  net.HardwareAddr(append([]byte(nil), v[0:6]...)),
		ID:   fc.GetFCID(v[7:10]),
		WWPN: fc.WWN(binary.BigEndian.Uint64(v[10:18])),
	}, true
}

// ReadELS は FLOGI・FDISC・LOGO の記述子から FC フレームを取り出す（末尾の詰め物は ELS の中身に残る）。
func ReadELS(v []byte) ([]byte, bool) {
	if len(v) < 2+fc.HeaderLen+4 {
		return nil, false
	}
	return v[2:], true
}
