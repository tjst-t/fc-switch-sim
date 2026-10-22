package fabric

import (
	"encoding/binary"

	"github.com/tjst-t/fc-switch-sim/sim/fc"
)

// handleNameServer はディレクトリ（0xFFFFFC）への CT の要求に答える。
// 問い合わせの答えは、問い合わせたポートからゾーニングで見えるものだけにする。
func (s *Switch) handleNameServer(src *Login, h fc.Header, payload []byte) {
	ct, body, err := fc.ParseCT(payload)
	if err != nil {
		return
	}
	hdr := fc.ReplyHeader(h, fc.RCtlSolCtl, s.nextRXID())
	accept := func(b []byte) { s.sendFC(src, hdr, fc.BuildCTResp(ct, fc.CTAccept, 0, 0, b)) }
	reject := func(reason, exp byte) { s.sendFC(src, hdr, fc.BuildCTResp(ct, fc.CTReject, reason, exp, nil)) }

	if ct.GSType != fc.GSTypeDirectory || ct.GSSubtype != fc.GSSubNameServer {
		reject(fc.CTRjtNotSupported, fc.CTExpNone)
		return
	}
	// 登録は自分の FC_ID のものだけ受ける。
	target := func() *Login {
		if len(body) < 4 {
			return nil
		}
		id := fc.GetFCID(body[1:4])
		if id != src.FCID {
			return nil
		}
		return src
	}
	// 問い合わせは見えるものだけ。
	visible := func(id fc.FCID) *Login {
		l := s.logins[id]
		if l == nil || !s.zc.canSee(src.WWPN, l.WWPN) {
			return nil
		}
		return l
	}

	switch ct.Command {
	// ---- 登録 ----
	case fc.NSRftID:
		l := target()
		if l == nil || len(body) < 36 {
			reject(fc.CTRjtUnableToPerf, fc.CTExpPortIDNotReg)
			return
		}
		copy(l.FC4Types[:], body[4:36])
		accept(nil)
	case fc.NSRffID:
		l := target()
		if l == nil || len(body) < 8 {
			reject(fc.CTRjtUnableToPerf, fc.CTExpPortIDNotReg)
			return
		}
		l.FC4Features[body[7]] = body[6]
		accept(nil)
	case fc.NSRnnID:
		l := target()
		if l == nil || len(body) < 12 {
			reject(fc.CTRjtUnableToPerf, fc.CTExpPortIDNotReg)
			return
		}
		l.WWNN = fc.WWN(binary.BigEndian.Uint64(body[4:12]))
		accept(nil)
	case fc.NSRspnID:
		l := target()
		if l == nil || len(body) < 5 {
			reject(fc.CTRjtUnableToPerf, fc.CTExpPortIDNotReg)
			return
		}
		l.SymPortName = readSym(body[4:])
		accept(nil)
	case fc.NSRsnnNN:
		if len(body) < 9 {
			reject(fc.CTRjtUnableToPerf, fc.CTExpNone)
			return
		}
		wwnn := fc.WWN(binary.BigEndian.Uint64(body[0:8]))
		name := readSym(body[8:])
		for _, l := range s.logins {
			if l.WWNN == wwnn {
				l.SymNodeName = name
			}
		}
		accept(nil)
	case fc.NSRpnID, fc.NSRcsID:
		if target() == nil {
			reject(fc.CTRjtUnableToPerf, fc.CTExpPortIDNotReg)
			return
		}
		accept(nil)
	case fc.NSDaID:
		accept(nil)

	// ---- 問い合わせ（ID から） ----
	case fc.NSGpnID, fc.NSGnnID, fc.NSGftID, fc.NSGffID, fc.NSGspnID:
		if len(body) < 4 {
			reject(fc.CTRjtUnableToPerf, fc.CTExpPortIDNotReg)
			return
		}
		l := visible(fc.GetFCID(body[1:4]))
		if l == nil {
			reject(fc.CTRjtUnableToPerf, fc.CTExpPortIDNotReg)
			return
		}
		switch ct.Command {
		case fc.NSGpnID:
			accept(be64(uint64(l.WWPN)))
		case fc.NSGnnID:
			accept(be64(uint64(l.WWNN)))
		case fc.NSGftID:
			accept(append([]byte(nil), l.FC4Types[:]...))
		case fc.NSGffID:
			// 128 バイト：FC-4 の種類ごとに 4 ビット（8 種類で 1 語）。
			b := make([]byte, 128)
			for t, feat := range l.FC4Features {
				word := int(t) / 8
				shift := uint(t%8) * 4
				v := binary.BigEndian.Uint32(b[word*4:]) | uint32(feat&0x0F)<<shift
				binary.BigEndian.PutUint32(b[word*4:], v)
			}
			accept(b)
		case fc.NSGspnID:
			accept(symBytes(l.SymPortName))
		}
	case fc.NSGidPN:
		if len(body) < 8 {
			reject(fc.CTRjtUnableToPerf, fc.CTExpPortNameNotReg)
			return
		}
		w := fc.WWN(binary.BigEndian.Uint64(body[0:8]))
		for _, l := range s.sortedLogins() {
			if l.WWPN == w && s.zc.canSee(src.WWPN, l.WWPN) {
				b := make([]byte, 4)
				fc.PutFCID(b[1:4], l.FCID)
				accept(b)
				return
			}
		}
		reject(fc.CTRjtUnableToPerf, fc.CTExpPortNameNotReg)

	// ---- 問い合わせ（FC-4 の種類から） ----
	case fc.NSGidFT, fc.NSGpnFT, fc.NSGnnFT:
		if len(body) < 4 {
			reject(fc.CTRjtUnableToPerf, fc.CTExpFC4TypesNotReg)
			return
		}
		ftype := body[3]
		var hits []*Login
		for _, l := range s.sortedLogins() {
			if l.hasType(ftype) && s.zc.canSee(src.WWPN, l.WWPN) {
				hits = append(hits, l)
			}
		}
		if len(hits) == 0 {
			reject(fc.CTRjtUnableToPerf, fc.CTExpFC4TypesNotReg)
			return
		}
		var out []byte
		for i, l := range hits {
			e := []byte{0, 0, 0, 0}
			if i == len(hits)-1 {
				e[0] = 0x80 // 最後の印
			}
			fc.PutFCID(e[1:4], l.FCID)
			switch ct.Command {
			case fc.NSGpnFT:
				e = append(e, 0, 0, 0, 0)
				e = append(e, be64(uint64(l.WWPN))...)
			case fc.NSGnnFT:
				e = append(e, 0, 0, 0, 0)
				e = append(e, be64(uint64(l.WWNN))...)
			}
			out = append(out, e...)
		}
		accept(out)
	default:
		reject(fc.CTRjtNotSupported, fc.CTExpNone)
	}
}

func be64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func readSym(b []byte) string {
	n := int(b[0])
	if n > len(b)-1 {
		n = len(b) - 1
	}
	return string(b[1 : 1+n])
}

func symBytes(s string) []byte {
	if len(s) > 255 {
		s = s[:255]
	}
	b := append([]byte{byte(len(s))}, s...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}
