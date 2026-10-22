package libmgr

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// EnterpriseOID は MIB の根（FCSIM-TAPE-LIBRARY-MIB）。99999 は仮の企業番号で、IANA には登録していない。
const EnterpriseOID = ".1.3.6.1.4.1.99999.1"

// Var は SNMP の値 1 つ（pass_persist の形：型は string・integer・counter）。
type Var struct {
	OID   []int
	Type  string
	Value string
}

func (v Var) OIDString() string {
	var b strings.Builder
	for _, n := range v.OID {
		b.WriteString(".")
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

func parseOID(s string) ([]int, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), ".")
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("OID %q を読めない", s)
		}
		out[i] = n
	}
	return out, nil
}

func cmpOID(a, b []int) int {
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

func root() []int {
	r, _ := parseOID(EnterpriseOID)
	return r
}

func oid(parts ...int) []int { return append(root(), parts...) }

// 値の番号（MIB と同じ）。
const (
	statusOnline  = 1
	statusOffline = 2
	statusUnknown = 3
)

func statusNum(s string) int {
	switch s {
	case StatusOnline:
		return statusOnline
	case StatusOffline:
		return statusOffline
	}
	return statusUnknown
}

func intsString(fs []int) string {
	p := make([]string, len(fs))
	for i, f := range fs {
		p[i] = strconv.Itoa(f)
	}
	return strings.Join(p, ",")
}

// Vars は State を SNMP の表にする（OID の順）。
//
//	.1.1.x.0     ライブラリ：1 vendor・2 product・3 serial・4 status（1 online・2 offline・3 unknown）・
//	             5 driveCount・6 slotCount・7 ieSlotCount・8 alertDrives
//	.1.2.1.c.i   ドライブの表（i はドライブの番号）：1 index・2 serial・3 product・4 state（1 empty・2 loaded）・
//	             5 volume・6 alerts（"15,20"）・7 loads（Counter32）
//	.1.3.1.c.a   スロットの表（a は要素の番号）：1 address・2 type（1 storage・2 importExport）・3 state（1 empty・2 full）・4 volume
func Vars(st State) []Var {
	L := st.Library
	vs := []Var{
		{oid(1, 1, 0), "string", L.Vendor},
		{oid(1, 2, 0), "string", L.Product},
		{oid(1, 3, 0), "string", L.Serial},
		{oid(1, 4, 0), "integer", strconv.Itoa(statusNum(L.Status))},
		{oid(1, 5, 0), "integer", strconv.Itoa(L.DriveCount)},
		{oid(1, 6, 0), "integer", strconv.Itoa(L.SlotCount)},
		{oid(1, 7, 0), "integer", strconv.Itoa(L.IESlotCount)},
		{oid(1, 8, 0), "integer", strconv.Itoa(L.AlertDrives)},
	}
	for _, d := range st.Drives {
		state := 1
		if d.State == DriveLoaded {
			state = 2
		}
		i := d.Index
		vs = append(vs,
			Var{oid(2, 1, 1, i), "integer", strconv.Itoa(i)},
			Var{oid(2, 1, 2, i), "string", d.Serial},
			Var{oid(2, 1, 3, i), "string", d.Product},
			Var{oid(2, 1, 4, i), "integer", strconv.Itoa(state)},
			Var{oid(2, 1, 5, i), "string", d.Volume},
			Var{oid(2, 1, 6, i), "string", intsString(d.Alerts)},
			Var{oid(2, 1, 7, i), "counter", strconv.FormatUint(d.Loads%(1<<32), 10)},
		)
	}
	for _, s := range st.Slots {
		t, full := 1, 1
		if s.Type == SlotImportExport {
			t = 2
		}
		if s.Full {
			full = 2
		}
		a := s.Address
		vs = append(vs,
			Var{oid(3, 1, 1, a), "integer", strconv.Itoa(a)},
			Var{oid(3, 1, 2, a), "integer", strconv.Itoa(t)},
			Var{oid(3, 1, 3, a), "integer", strconv.Itoa(full)},
			Var{oid(3, 1, 4, a), "string", s.Volume},
		)
	}
	sort.Slice(vs, func(i, j int) bool { return cmpOID(vs[i].OID, vs[j].OID) < 0 })
	return vs
}

// Lookup は get（exact）か getnext（next）の答えを探す。
func Lookup(vs []Var, o []int, next bool) (Var, bool) {
	for _, v := range vs {
		c := cmpOID(v.OID, o)
		if (!next && c == 0) || (next && c > 0) {
			return v, true
		}
	}
	return Var{}, false
}

// PassPersist は net-snmp の pass_persist の決まりで答える（snmpd が起こす）。
// state は答えるたびに呼ばれる（呼ぶ側でキャッシュする）。
func PassPersist(in io.Reader, out io.Writer, state func() (State, error)) error {
	sc := bufio.NewScanner(in)
	w := bufio.NewWriter(out)
	reply := func(lines ...string) error {
		for _, l := range lines {
			w.WriteString(l)
			w.WriteString("\n")
		}
		return w.Flush()
	}
	for sc.Scan() {
		cmd := strings.TrimSpace(sc.Text())
		switch cmd {
		case "PING":
			if err := reply("PONG"); err != nil {
				return err
			}
		case "get", "getnext":
			if !sc.Scan() {
				return sc.Err()
			}
			o, err := parseOID(sc.Text())
			st, serr := state()
			if err != nil || serr != nil {
				reply("NONE")
				continue
			}
			v, ok := Lookup(Vars(st), o, cmd == "getnext")
			if !ok {
				reply("NONE")
				continue
			}
			if err := reply(v.OIDString(), v.Type, v.Value); err != nil {
				return err
			}
		case "set":
			sc.Scan()
			sc.Scan()
			reply("not-writable")
		case "":
		default:
			reply("NONE")
		}
	}
	return sc.Err()
}

// ---- トラップ ----

// 通知の番号（.1.0.n）。
var trapNum = map[string]int{
	EvStatusChange: 1,
	EvDriveLoad:    2,
	EvDriveUnload:  3,
	EvDriveAlert:   4,
	EvIEImport:     5,
	EvIERemove:     6,
}

// TrapVars はトラップの変数（sysUpTime と snmpTrapOID のあと）を作る。
func TrapVars(ev Event, st State) (string, []gosnmp.SnmpPDU) {
	trapOID := Var{OID: oid(0, trapNum[ev.Kind])}.OIDString()
	str := func(o []int, s string) gosnmp.SnmpPDU {
		return gosnmp.SnmpPDU{Name: Var{OID: o}.OIDString(), Type: gosnmp.OctetString, Value: s}
	}
	num := func(o []int, n int) gosnmp.SnmpPDU {
		return gosnmp.SnmpPDU{Name: Var{OID: o}.OIDString(), Type: gosnmp.Integer, Value: n}
	}
	vars := []gosnmp.SnmpPDU{str(oid(1, 3, 0), st.Library.Serial)}
	switch ev.Kind {
	case EvStatusChange:
		vars = append(vars, num(oid(1, 4, 0), statusNum(ev.Status)))
	case EvDriveLoad, EvDriveUnload:
		vars = append(vars, num(oid(2, 1, 1, ev.Drive), ev.Drive), str(oid(2, 1, 5, ev.Drive), ev.Volume))
	case EvDriveAlert:
		vars = append(vars, num(oid(2, 1, 1, ev.Drive), ev.Drive), str(oid(2, 1, 6, ev.Drive), intsString(ev.Alerts)))
	case EvIEImport, EvIERemove:
		vars = append(vars, num(oid(3, 1, 1, ev.Slot), ev.Slot), str(oid(3, 1, 4, ev.Slot), ev.Volume))
	}
	return trapOID, vars
}

// TrapSender は SNMPv2c のトラップを送る。
type TrapSender struct {
	Targets   []string // host:port
	Community string
	start     time.Time
	errf      func(target string, err error)
}

func NewTrapSender(targets []string, community string, errf func(string, error)) *TrapSender {
	return &TrapSender{Targets: targets, Community: community, start: time.Now(), errf: errf}
}

func (t *TrapSender) Notify(ev Event, st State) {
	trapOID, vars := TrapVars(ev, st)
	pdus := append([]gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(time.Since(t.start) / (10 * time.Millisecond))},
		{Name: ".1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: trapOID},
	}, vars...)
	for _, target := range t.Targets {
		host, port, err := net.SplitHostPort(target)
		if err != nil {
			host, port = target, "162"
		}
		p, _ := strconv.Atoi(port)
		g := &gosnmp.GoSNMP{Target: host, Port: uint16(p), Community: t.Community, Version: gosnmp.Version2c, Timeout: 2 * time.Second, Retries: 0}
		if err := g.Connect(); err != nil {
			t.errf(target, err)
			continue
		}
		_, err = g.SendTrap(gosnmp.SnmpTrap{Variables: pdus})
		g.Conn.Close()
		if err != nil {
			t.errf(target, err)
		}
	}
}
