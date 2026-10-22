package fc

import (
	"encoding/binary"
	"fmt"
)

// CT（Common Transport）の IU ヘッダは 16 バイト。
const CTHeaderLen = 16

const (
	GSTypeDirectory = 0xFC
	GSSubNameServer = 0x02

	CTAccept = 0x8002
	CTReject = 0x8001
)

// ネームサーバのコマンド。
const (
	NSGaNxt  = 0x0100
	NSGpnID  = 0x0112
	NSGnnID  = 0x0113
	NSGftID  = 0x0117
	NSGspnID = 0x0118
	NSGffID  = 0x011F
	NSGidPN  = 0x0121
	NSGidFT  = 0x0171
	NSGpnFT  = 0x0172
	NSGnnFT  = 0x0173
	NSRpnID  = 0x0212
	NSRnnID  = 0x0213
	NSRcsID  = 0x0214
	NSRftID  = 0x0217
	NSRspnID = 0x0218
	NSRffID  = 0x021F
	NSRsnnNN = 0x0239
	NSDaID   = 0x0300
)

// CT の拒否の理由と説明。
const (
	CTRjtInvalidCmd   = 0x01
	CTRjtUnableToPerf = 0x09
	CTRjtNotSupported = 0x0B

	CTExpNone           = 0x00
	CTExpPortIDNotReg   = 0x01
	CTExpPortNameNotReg = 0x02
	CTExpFC4TypesNotReg = 0x07
)

// CTHeader は CT の IU ヘッダ。
type CTHeader struct {
	Revision    byte
	GSType      byte
	GSSubtype   byte
	Command     uint16
	MaxSize     uint16
	Reason      byte
	Explanation byte
}

func ParseCT(b []byte) (CTHeader, []byte, error) {
	if len(b) < CTHeaderLen {
		return CTHeader{}, nil, fmt.Errorf("CT が短い（%d バイト）", len(b))
	}
	return CTHeader{
		Revision:    b[0],
		GSType:      b[4],
		GSSubtype:   b[5],
		Command:     binary.BigEndian.Uint16(b[8:10]),
		MaxSize:     binary.BigEndian.Uint16(b[10:12]),
		Reason:      b[13],
		Explanation: b[14],
	}, b[CTHeaderLen:], nil
}

// BuildCTResp はネームサーバの応答（受理か拒否）を作る。
func BuildCTResp(req CTHeader, cmd uint16, reason, explanation byte, body []byte) []byte {
	b := make([]byte, CTHeaderLen+len(body))
	b[0] = 0x01
	b[4] = req.GSType
	b[5] = req.GSSubtype
	binary.BigEndian.PutUint16(b[8:10], cmd)
	b[13] = reason
	b[14] = explanation
	copy(b[CTHeaderLen:], body)
	return b
}
