package fc

import (
	"encoding/binary"
	"fmt"
	"net"
)

// Ethernet の種類。
const (
	EtherTypeFCoE = 0x8906
	EtherTypeFIP  = 0x8914
)

// SOF と EOF（FCoE での符号）。
const (
	SOFf  = 0x28
	SOFi2 = 0x2D
	SOFi3 = 0x2E
	SOFn2 = 0x35
	SOFn3 = 0x36

	EOFn = 0x41
	EOFt = 0x42
)

const (
	EthHeaderLen   = 14
	FCoEHeaderLen  = 14
	FCoETrailerLen = 8
)

// DefaultFCMap は FPMA の MAC アドレスの上位 24 ビット（FC-MAP の既定値）。
const DefaultFCMap = 0x0EFC00

// FPMA は FC-MAP と FC_ID から VN_Port の MAC アドレスを作る。
func FPMA(fcmap uint32, id FCID) net.HardwareAddr {
	return net.HardwareAddr{byte(fcmap >> 16), byte(fcmap >> 8), byte(fcmap), byte(id >> 16), byte(id >> 8), byte(id)}
}

// FCoEFrame は Ethernet に包んだ FC フレームを開いたもの。
type FCoEFrame struct {
	Dst, Src net.HardwareAddr
	SOF, EOF byte
	FC       []byte // FC ヘッダと中身（CRC は含まない）
	CRCOK    bool
}

// ParseFCoE は Ethernet フレーム（宛先 MAC から）を開く。VLAN タグは付いていない前提。
func ParseFCoE(eth []byte) (FCoEFrame, error) {
	if len(eth) < EthHeaderLen+FCoEHeaderLen+HeaderLen+FCoETrailerLen {
		return FCoEFrame{}, fmt.Errorf("FCoE フレームが短い（%d バイト）", len(eth))
	}
	if binary.BigEndian.Uint16(eth[12:14]) != EtherTypeFCoE {
		return FCoEFrame{}, fmt.Errorf("FCoE ではない")
	}
	f := FCoEFrame{Dst: net.HardwareAddr(eth[0:6]), Src: net.HardwareAddr(eth[6:12])}
	h := eth[EthHeaderLen:]
	if h[0]>>4 != 0 {
		return FCoEFrame{}, fmt.Errorf("FCoE の版 %d は知らない", h[0]>>4)
	}
	f.SOF = h[13]
	body := h[FCoEHeaderLen:]
	// 末尾は CRC(4)・EOF(1)・予約(3)。Ethernet の詰め物は FCoE では付かない（最小長を超える）。
	n := len(body) - FCoETrailerLen
	f.FC = body[:n]
	crc := binary.LittleEndian.Uint32(body[n : n+4])
	f.EOF = body[n+4]
	f.CRCOK = crc == CRC(f.FC)
	return f, nil
}

// BuildFCoE は FC フレームを Ethernet に包む。
func BuildFCoE(dst, src net.HardwareAddr, sof, eof byte, fcframe []byte) []byte {
	b := make([]byte, EthHeaderLen+FCoEHeaderLen+len(fcframe)+FCoETrailerLen)
	copy(b[0:6], dst)
	copy(b[6:12], src)
	binary.BigEndian.PutUint16(b[12:14], EtherTypeFCoE)
	b[EthHeaderLen+13] = sof
	off := EthHeaderLen + FCoEHeaderLen
	copy(b[off:], fcframe)
	off += len(fcframe)
	binary.LittleEndian.PutUint32(b[off:], CRC(fcframe))
	b[off+4] = eof
	return b
}
