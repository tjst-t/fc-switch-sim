package snmpagent

import (
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

func TestAgentWalk(t *testing.T) {
	vs := SysVars("d", ".1.3.6.1.4.1.99999.2", "sw", time.Now())
	vs = append(vs, Var{OID(".1.3.6.1.4.1.99999.2.1.1.0"), gosnmp.Integer, 7}, Var{OID(".1.3.6.1.4.1.99999.2.2.1.7.1"), gosnmp.Counter32, uint(3)})
	Sort(vs)
	a := &Agent{Community: "public", Vars: func() []Var { return vs }}
	pc := listen(t, a)
	g := &gosnmp.GoSNMP{Target: "127.0.0.1", Port: pc, Community: "public", Version: gosnmp.Version2c, Timeout: time.Second}
	if err := g.Connect(); err != nil {
		t.Fatal(err)
	}
	defer g.Conn.Close()
	r, err := g.Get([]string{".1.3.6.1.2.1.1.5.0", ".1.3.6.1.4.1.99999.2.1.9.0"})
	if err != nil {
		t.Fatal(err)
	}
	if string(r.Variables[0].Value.([]byte)) != "sw" || r.Variables[1].Type != gosnmp.NoSuchObject {
		t.Fatalf("get の答えがおかしい：%+v", r.Variables)
	}
	var walked []string
	if err := g.BulkWalk(".1.3.6.1.4.1.99999.2", func(p gosnmp.SnmpPDU) error { walked = append(walked, p.Name); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(walked) != 2 {
		t.Fatalf("bulkwalk で %d 個（2 のはず）：%v", len(walked), walked)
	}
	walked = nil
	if err := g.Walk(".1.3.6.1", func(p gosnmp.SnmpPDU) error { walked = append(walked, p.Name); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(walked) != len(vs) {
		t.Fatalf("walk で %d 個（%d のはず）", len(walked), len(vs))
	}
}

func listen(t *testing.T, a *Agent) uint16 {
	// 空いているポートを探してから Serve に渡す
	for p := 20161; p < 20261; p++ {
		ch := make(chan error, 1)
		addr := "127.0.0.1:" + itoa(p)
		go func() { ch <- a.Serve(addr) }()
		select {
		case <-ch:
			continue
		case <-time.After(100 * time.Millisecond):
			return uint16(p)
		}
	}
	t.Fatal("ポートが無い")
	return 0
}

func itoa(n int) string { return OIDString([]int{n})[1:] }
