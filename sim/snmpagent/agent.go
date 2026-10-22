// Package snmpagent は小さな SNMPv2c のエージェント（get・getnext・getbulk）とトラップの送り手。
// 値の表（OID の順に並んだ Var）は呼ぶ側が毎回作る。書き込み（set）は受けない。
package snmpagent

import (
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Var は値 1 つ。Type は gosnmp の型（Integer・OctetString・Counter32・Gauge32・TimeTicks・ObjectIdentifier）。
type Var struct {
	OID   []int
	Type  gosnmp.Asn1BER
	Value any
}

func OID(s string) []int {
	s = strings.TrimPrefix(strings.TrimSpace(s), ".")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}

func OIDString(o []int) string {
	var b strings.Builder
	for _, n := range o {
		b.WriteString(".")
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

// Join は根の OID に番号を足す。
func Join(root []int, parts ...int) []int {
	return append(append([]int{}, root...), parts...)
}

func Cmp(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return len(a) - len(b)
}

// Sort は OID の順に並べる。
func Sort(vs []Var) { sort.Slice(vs, func(i, j int) bool { return Cmp(vs[i].OID, vs[j].OID) < 0 }) }

// Agent は UDP で答えるエージェント。
type Agent struct {
	Community string
	Vars      func() []Var // OID の順
	Log       *slog.Logger
	start     time.Time
}

// SysVars は MIB-II の system の組（sysDescr・sysObjectID・sysUpTime・sysName）。
func SysVars(descr, objectID, name string, start time.Time) []Var {
	return []Var{
		{OID(".1.3.6.1.2.1.1.1.0"), gosnmp.OctetString, descr},
		{OID(".1.3.6.1.2.1.1.2.0"), gosnmp.ObjectIdentifier, objectID},
		{OID(".1.3.6.1.2.1.1.3.0"), gosnmp.TimeTicks, uint32(time.Since(start) / (10 * time.Millisecond))},
		{OID(".1.3.6.1.2.1.1.5.0"), gosnmp.OctetString, name},
	}
}

// Serve は addr（例 ":161"）で答え続ける。
func (a *Agent) Serve(addr string) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	defer pc.Close()
	buf := make([]byte, 65535)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return err
		}
		out, err := a.Handle(buf[:n])
		if err != nil {
			if a.Log != nil {
				a.Log.Debug("SNMP の要求を扱えない", "from", from, "err", err)
			}
			continue
		}
		pc.WriteTo(out, from)
	}
}

// Handle は要求の 1 つを読んで、応答のバイト列を返す。
func (a *Agent) Handle(req []byte) ([]byte, error) {
	g := &gosnmp.GoSNMP{}
	p, err := g.SnmpDecodePacket(req)
	if err != nil {
		return nil, err
	}
	if p.Version != gosnmp.Version2c && p.Version != gosnmp.Version1 {
		return nil, fmt.Errorf("版 %v は扱わない", p.Version)
	}
	if p.Community != a.Community {
		return nil, fmt.Errorf("コミュニティが違う")
	}
	vs := a.Vars()
	resp := &gosnmp.SnmpPacket{Version: p.Version, Community: p.Community, PDUType: gosnmp.GetResponse, RequestID: p.RequestID}
	switch p.PDUType {
	case gosnmp.GetRequest:
		for _, v := range p.Variables {
			resp.Variables = append(resp.Variables, a.get(vs, OID(v.Name)))
		}
	case gosnmp.GetNextRequest:
		for _, v := range p.Variables {
			resp.Variables = append(resp.Variables, a.next(vs, OID(v.Name)))
		}
	case gosnmp.GetBulkRequest:
		nonRep := int(p.NonRepeaters)
		if nonRep > len(p.Variables) {
			nonRep = len(p.Variables)
		}
		for _, v := range p.Variables[:nonRep] {
			resp.Variables = append(resp.Variables, a.next(vs, OID(v.Name)))
		}
		reps := int(p.MaxRepetitions)
		if reps > 50 {
			reps = 50
		}
		cur := make([][]int, 0)
		for _, v := range p.Variables[nonRep:] {
			cur = append(cur, OID(v.Name))
		}
		for r := 0; r < reps && len(cur) > 0; r++ {
			allEnd := true
			for i, o := range cur {
				pdu := a.next(vs, o)
				resp.Variables = append(resp.Variables, pdu)
				if pdu.Type != gosnmp.EndOfMibView {
					allEnd = false
					cur[i] = OID(pdu.Name)
				}
			}
			if allEnd {
				break
			}
		}
	case gosnmp.SetRequest:
		resp.Error = gosnmp.NotWritable
		resp.ErrorIndex = 1
		resp.Variables = p.Variables
	default:
		return nil, fmt.Errorf("PDU %v は扱わない", p.PDUType)
	}
	return resp.MarshalMsg()
}

func pdu(v Var) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: OIDString(v.OID), Type: v.Type, Value: v.Value}
}

func (a *Agent) get(vs []Var, o []int) gosnmp.SnmpPDU {
	for _, v := range vs {
		if Cmp(v.OID, o) == 0 {
			return pdu(v)
		}
	}
	return gosnmp.SnmpPDU{Name: OIDString(o), Type: gosnmp.NoSuchObject}
}

func (a *Agent) next(vs []Var, o []int) gosnmp.SnmpPDU {
	for _, v := range vs {
		if Cmp(v.OID, o) > 0 {
			return pdu(v)
		}
	}
	return gosnmp.SnmpPDU{Name: OIDString(o), Type: gosnmp.EndOfMibView}
}

// TrapSender は SNMPv2c のトラップを送る。
type TrapSender struct {
	Targets   []string // host:port（port を省くと 162）
	Community string
	Start     time.Time
	Log       *slog.Logger
}

// Send は trapOID のトラップを、vars を付けて送る（sysUpTime と snmpTrapOID は先頭に足す）。
func (t *TrapSender) Send(trapOID string, vars []Var) {
	pdus := []gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(time.Since(t.Start) / (10 * time.Millisecond))},
		{Name: ".1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: trapOID},
	}
	for _, v := range vars {
		pdus = append(pdus, pdu(v))
	}
	for _, target := range t.Targets {
		host, port, err := net.SplitHostPort(target)
		if err != nil {
			host, port = target, "162"
		}
		pn, _ := strconv.Atoi(port)
		g := &gosnmp.GoSNMP{Target: host, Port: uint16(pn), Community: t.Community, Version: gosnmp.Version2c, Timeout: 2 * time.Second}
		if err := g.Connect(); err != nil {
			t.warn(target, err)
			continue
		}
		if _, err := g.SendTrap(gosnmp.SnmpTrap{Variables: pdus}); err != nil {
			t.warn(target, err)
		}
		g.Conn.Close()
	}
}

func (t *TrapSender) warn(target string, err error) {
	if t.Log != nil {
		t.Log.Warn("トラップを送れない", "target", target, "err", err)
	}
}
