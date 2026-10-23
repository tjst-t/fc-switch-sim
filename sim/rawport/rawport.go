//go:build linux

// Package rawport は Linux のネットワークインタフェースで Ethernet のフレームを直に送受信する（AF_PACKET）。
package rawport

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Port はインタフェース 1 つ。FCoE と FIP のフレームだけを受け取る。
type Port struct {
	ifi   *net.Interface
	fd    int
	drops uint64
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// Open はインタフェースを開き、プロミスキャスにする。インタフェースが現れるまで wait だけ待つ。
func Open(name string, wait time.Duration) (*Port, error) {
	deadline := time.Now().Add(wait)
	var ifi *net.Interface
	var err error
	for {
		ifi, err = net.InterfaceByName(name)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("インタフェース %s：%w", name, err)
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		return nil, fmt.Errorf("AF_PACKET のソケット：%w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_ALL), Ifindex: ifi.Index}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("%s に結びつけられない：%w", name, err)
	}
	mreq := unix.PacketMreq{Ifindex: int32(ifi.Index), Type: unix.PACKET_MR_PROMISC}
	if err := unix.SetsockoptPacketMreq(fd, unix.SOL_PACKET, unix.PACKET_ADD_MEMBERSHIP, &mreq); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("%s をプロミスキャスにできない：%w", name, err)
	}
	// FCoE と FIP 以外を受け取らないようにする（BPF：Ethertype が 0x8906 か 0x8914）。
	filter := []unix.SockFilter{
		{Code: 0x28, K: 12},            // ldh [12]
		{Code: 0x15, Jt: 2, K: 0x8906}, // jeq #0x8906, accept
		{Code: 0x15, Jt: 1, K: 0x8914}, // jeq #0x8914, accept
		{Code: 0x06, K: 0},             // ret #0
		{Code: 0x06, K: 0x0004_0000},   // ret #262144
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.SetsockoptSockFprog(fd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, &prog); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("%s にフィルタを付けられない：%w", name, err)
	}
	// FCoE の書き込みは 2KB のフレームが一度に数百届く。既定の受信バッファ（約 200KB）ではあふれて落ちる。
	// 上限（net.core.rmem_max）を超えて取るには特権が要る。取れなければ上限まで。
	for _, opt := range [][2]int{{unix.SO_RCVBUFFORCE, unix.SO_RCVBUF}, {unix.SO_SNDBUFFORCE, unix.SO_SNDBUF}} {
		if unix.SetsockoptInt(fd, unix.SOL_SOCKET, opt[0], sockBufBytes) != nil {
			if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, opt[1], sockBufBytes); err != nil {
				unix.Close(fd)
				return nil, fmt.Errorf("%s のバッファを大きくできない：%w", name, err)
			}
		}
	}
	return &Port{ifi: ifi, fd: fd}, nil
}

const sockBufBytes = 64 << 20

// KernelDrops はカーネルの中で受け取りきれずに捨てたフレームの数（読むたびに 0 に戻るので足し込む）。
func (p *Port) KernelDrops() uint64 {
	st, err := unix.GetsockoptTpacketStats(p.fd, unix.SOL_PACKET, unix.PACKET_STATISTICS)
	if err != nil {
		return p.drops
	}
	p.drops += uint64(st.Drops)
	return p.drops
}

func (p *Port) MAC() net.HardwareAddr { return p.ifi.HardwareAddr }

// RcvBuf はいまの受信バッファの大きさ（バイト）。
func (p *Port) RcvBuf() int {
	v, _ := unix.GetsockoptInt(p.fd, unix.SOL_SOCKET, unix.SO_RCVBUF)
	return v
}
func (p *Port) Name() string { return p.ifi.Name }

func (p *Port) Send(frame []byte) error {
	return unix.Sendto(p.fd, frame, 0, &unix.SockaddrLinklayer{Ifindex: p.ifi.Index, Halen: 6})
}

// Run はフレームを受け取るたびに fn を呼ぶ。自分が送ったフレームは除く。
func (p *Port) Run(fn func([]byte)) error {
	buf := make([]byte, 65536)
	for {
		n, from, err := unix.Recvfrom(p.fd, buf, 0)
		if err != nil {
			switch err {
			case unix.EINTR:
				continue
			case unix.ENETDOWN, unix.ENXIO, unix.EAGAIN:
				// リンクが落ちている（ケーブルを抜いた）：戻るまで待つ。リンクの状態は LinkUp で見る
				time.Sleep(200 * time.Millisecond)
				continue
			}
			return err
		}
		if ll, ok := from.(*unix.SockaddrLinklayer); ok && ll.Pkttype == unix.PACKET_OUTGOING {
			continue
		}
		fn(buf[:n])
	}
}

func (p *Port) Close() error { return unix.Close(p.fd) }

// LinkUp はリンクがつながっているか（/sys/class/net/<if>/operstate が up か unknown）。
// carrier は管理上 down のインタフェースでは読めないので使わない。読めなければ true とみなす。
func (p *Port) LinkUp() bool {
	b, err := os.ReadFile("/sys/class/net/" + p.ifi.Name + "/operstate")
	if err != nil {
		return true
	}
	switch strings.TrimSpace(string(b)) {
	case "up", "unknown":
		return true
	}
	return false
}
