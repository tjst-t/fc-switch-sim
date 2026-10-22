// Package fc は Fibre Channel のフレーム（FC ヘッダ・ELS・CT）と FCoE のカプセル化を扱う。
//
// 値と並びは FC-FS / FC-LS / FC-GS / FC-BB-5 の規格に従う。
package fc

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"strings"
)

// FCID は 24 ビットの N_Port の識別子（Domain・Area・Port）。
type FCID uint32

// よく知られたアドレス（well-known address）。
const (
	FIDFlogi      FCID = 0xFFFFFE // F_Port のコントローラ（FLOGI の宛先）
	FIDFabricCtl  FCID = 0xFFFFFD // ファブリックのコントローラ（SCR・RSCN）
	FIDDirServ    FCID = 0xFFFFFC // ディレクトリ（ネームサーバ）
	FIDMgmtServ   FCID = 0xFFFFFA // 管理サーバ（FDMI など）
	FIDDomainBase FCID = 0xFFFC00 // ドメインのコントローラの先頭
)

func (f FCID) String() string {
	return fmt.Sprintf("%02x.%02x.%02x", byte(f>>16), byte(f>>8), byte(f))
}

// IsWellKnown はファブリック側のサービスのアドレスか。
func (f FCID) IsWellKnown() bool { return f >= 0xFFFC00 }

func GetFCID(b []byte) FCID {
	return FCID(uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2]))
}

func PutFCID(b []byte, f FCID) {
	b[0], b[1], b[2] = byte(f>>16), byte(f>>8), byte(f)
}

// WWN は 64 ビットの World Wide Name。
type WWN uint64

func (w WWN) String() string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(w))
	parts := make([]string, 8)
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02x", x)
	}
	return strings.Join(parts, ":")
}

// ParseWWN は "20:00:00:25:b5:00:00:01" や "2000002...":16 桁の16進を読む。
func ParseWWN(s string) (WWN, error) {
	h := strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x"), ":", ""), "-", "")
	if len(h) != 16 {
		return 0, fmt.Errorf("WWN %q は 16 桁の16進ではない", s)
	}
	var v uint64
	for _, c := range h {
		var d uint64
		switch {
		case c >= '0' && c <= '9':
			d = uint64(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint64(c-'a') + 10
		default:
			return 0, fmt.Errorf("WWN %q に16進でない文字がある", s)
		}
		v = v<<4 | d
	}
	return WWN(v), nil
}

func (w WWN) MarshalText() ([]byte, error) { return []byte(w.String()), nil }
func (w *WWN) UnmarshalText(b []byte) error {
	v, err := ParseWWN(string(b))
	if err != nil {
		return err
	}
	*w = v
	return nil
}

// R_CTL（ルーティングと情報の種類）。
const (
	RCtlDeviceDataUnsol = 0x00
	RCtlUnsolCtl        = 0x02 // FC-4 の制御（CT の要求）
	RCtlSolCtl          = 0x03 // FC-4 の制御（CT の応答）
	RCtlUnsolData       = 0x04
	RCtlDataDesc        = 0x05
	RCtlUnsolCmd        = 0x06 // FCP_CMND
	RCtlCmdStatus       = 0x07 // FCP_RSP
	RCtlELSReq          = 0x22
	RCtlELSRep          = 0x23
	RCtlBAAbts          = 0x81
	RCtlBAAcc           = 0x84
	RCtlBARjt           = 0x85
)

// TYPE。
const (
	TypeBLS = 0x00
	TypeELS = 0x01
	TypeFCP = 0x08
	TypeCT  = 0x20
)

// F_CTL のビット。
const (
	FCtlExCtx    = 0x800000 // 交換の応答側が送った
	FCtlSeqCtx   = 0x400000
	FCtlFirstSeq = 0x200000
	FCtlLastSeq  = 0x100000
	FCtlEndSeq   = 0x080000
	FCtlSeqInit  = 0x010000

	FCtlReq  = FCtlFirstSeq | FCtlEndSeq | FCtlSeqInit
	FCtlResp = FCtlExCtx | FCtlLastSeq | FCtlEndSeq | FCtlSeqInit
)

// HeaderLen は FC ヘッダの長さ。
const HeaderLen = 24

// Header は FC フレームのヘッダ。
type Header struct {
	RCtl   byte
	DID    FCID
	CSCtl  byte
	SID    FCID
	Type   byte
	FCtl   uint32
	SeqID  byte
	DFCtl  byte
	SeqCnt uint16
	OXID   uint16
	RXID   uint16
	Param  uint32
}

func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderLen {
		return Header{}, fmt.Errorf("FC ヘッダが短い（%d バイト）", len(b))
	}
	return Header{
		RCtl:   b[0],
		DID:    GetFCID(b[1:4]),
		CSCtl:  b[4],
		SID:    GetFCID(b[5:8]),
		Type:   b[8],
		FCtl:   uint32(b[9])<<16 | uint32(b[10])<<8 | uint32(b[11]),
		SeqID:  b[12],
		DFCtl:  b[13],
		SeqCnt: binary.BigEndian.Uint16(b[14:16]),
		OXID:   binary.BigEndian.Uint16(b[16:18]),
		RXID:   binary.BigEndian.Uint16(b[18:20]),
		Param:  binary.BigEndian.Uint32(b[20:24]),
	}, nil
}

func (h Header) Put(b []byte) {
	b[0] = h.RCtl
	PutFCID(b[1:4], h.DID)
	b[4] = h.CSCtl
	PutFCID(b[5:8], h.SID)
	b[8] = h.Type
	b[9], b[10], b[11] = byte(h.FCtl>>16), byte(h.FCtl>>8), byte(h.FCtl)
	b[12] = h.SeqID
	b[13] = h.DFCtl
	binary.BigEndian.PutUint16(b[14:16], h.SeqCnt)
	binary.BigEndian.PutUint16(b[16:18], h.OXID)
	binary.BigEndian.PutUint16(b[18:20], h.RXID)
	binary.BigEndian.PutUint32(b[20:24], h.Param)
}

// ReplyHeader は要求 req への応答のヘッダを作る（宛先と送り元を入れ替え、交換の応答側にする）。
func ReplyHeader(req Header, rctl byte, rxid uint16) Header {
	return Header{
		RCtl:  rctl,
		DID:   req.SID,
		SID:   req.DID,
		Type:  req.Type,
		FCtl:  FCtlResp,
		SeqID: req.SeqID + 1,
		OXID:  req.OXID,
		RXID:  rxid,
	}
}

// Frame は FC フレーム（ヘッダと中身）。
type Frame struct {
	Header  Header
	Payload []byte
}

func (f Frame) Bytes() []byte {
	b := make([]byte, HeaderLen+len(f.Payload))
	f.Header.Put(b)
	copy(b[HeaderLen:], f.Payload)
	return b
}

// CRC は FC の CRC（Ethernet と同じ多項式。FCoE ではリトルエンディアンで置く）。
func CRC(b []byte) uint32 { return crc32.ChecksumIEEE(b) }
