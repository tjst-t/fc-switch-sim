// Package swsnmp は FC スイッチ（fcsw）の SNMP：FCSIM-FC-SWITCH-MIB の値の表と、トラップ。
//
// 根は .1.3.6.1.4.1.99999.2（99999 は仮の企業番号。IANA には登録していない）。
//
//	.2.1.x.0     スイッチ：1 name・2 switchWWN・3 domainID・4 portCount・5 loginCount・6 zoneCount・7 zoningChanges（Counter32）
//	.2.2.1.c.i   ポートの表（i はポート番号＋1）：1 portNumber（0 から）・2 interface・3 portWWN・4 state（1 online・2 noLogin・3 disabled）・
//	             5 loginCount・6 fcids（"01.00.00,01.00.01"）・7 zoneDrops・8 rxFCoE・9 txFCoE（7〜9 は Counter32）
//	.2.3.1.c.i   ログインの表（i は FC_ID を 24 ビットの数にしたもの）：1 fcid・2 wwpn・3 portNumber・4 npiv（1 yes・2 no）
//	.2.0.n       通知：1 portStateChange・2 login・3 logout・4 zoningChange
package swsnmp

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/tjst-t/fc-switch-sim/sim/fabric"
	"github.com/tjst-t/fc-switch-sim/sim/snmpagent"
)

const Root = ".1.3.6.1.4.1.99999.2"

var root = snmpagent.OID(Root)

func o(parts ...int) []int { return snmpagent.Join(root, parts...) }

func StateNum(s string) int {
	switch s {
	case "Online":
		return 1
	case "NoLogin":
		return 2
	}
	return 3
}

func fcidNum(s string) int {
	n, _ := strconv.ParseInt(strings.ReplaceAll(s, ".", ""), 16, 32)
	return int(n)
}

// Vars はスイッチのいまの様子を値の表にする（OID の順）。
func Vars(sw *fabric.Switch, changes uint, start time.Time) []snmpagent.Var {
	info := sw.Info()
	ports := sw.Ports()
	ns := sw.NameServer()
	z := sw.Zoning()
	str := func(oid []int, s string) snmpagent.Var {
		return snmpagent.Var{OID: oid, Type: gosnmp.OctetString, Value: s}
	}
	num := func(oid []int, n int) snmpagent.Var { return snmpagent.Var{OID: oid, Type: gosnmp.Integer, Value: n} }
	cnt := func(oid []int, n uint64) snmpagent.Var {
		return snmpagent.Var{OID: oid, Type: gosnmp.Counter32, Value: uint(n % (1 << 32))}
	}
	vs := snmpagent.SysVars("fc-switch-sim fcsw (FCoE FCF simulator)", Root, info.Name, start)
	vs = append(vs,
		str(o(1, 1, 0), info.Name),
		str(o(1, 2, 0), info.SwitchWWN.String()),
		num(o(1, 3, 0), int(info.DomainID)),
		num(o(1, 4, 0), info.Ports),
		num(o(1, 5, 0), info.Logins),
		num(o(1, 6, 0), len(z.Zones)),
		cnt(o(1, 7, 0), uint64(changes)),
	)
	for _, p := range ports {
		i := p.Index + 1
		vs = append(vs,
			num(o(2, 1, 1, i), p.Index),
			str(o(2, 1, 2, i), p.Interface),
			str(o(2, 1, 3, i), p.PortWWN.String()),
			num(o(2, 1, 4, i), StateNum(p.State)),
			num(o(2, 1, 5, i), len(p.FCIDs)),
			str(o(2, 1, 6, i), strings.Join(p.FCIDs, ",")),
			cnt(o(2, 1, 7, i), p.Counters.ZoneDrops),
			cnt(o(2, 1, 8, i), p.Counters.RxFCoE),
			cnt(o(2, 1, 9, i), p.Counters.TxFCoE),
		)
	}
	for _, l := range ns {
		i := fcidNum(l.FCID)
		npiv := 2
		if l.NPIV {
			npiv = 1
		}
		vs = append(vs,
			str(o(3, 1, 1, i), l.FCID),
			str(o(3, 1, 2, i), l.WWPN.String()),
			num(o(3, 1, 3, i), l.Port),
			num(o(3, 1, 4, i), npiv),
		)
	}
	snmpagent.Sort(vs)
	return vs
}

// Traps はスイッチの出来事とポートの状態の変化を見て、トラップを送る。
type Traps struct {
	sw      *fabric.Switch
	send    *snmpagent.TrapSender
	mu      sync.Mutex
	changes uint
	states  map[int]string
	queue   chan fabric.Event
}

// NewTraps は sw の出来事を受け取り始める。send が nil ならトラップは送らない（ゾーンの変更の回数だけ数える）。
func NewTraps(sw *fabric.Switch, send *snmpagent.TrapSender) *Traps {
	t := &Traps{sw: sw, send: send, states: map[int]string{}, queue: make(chan fabric.Event, 1000)}
	for _, p := range sw.Ports() {
		t.states[p.Index] = p.State
	}
	sw.Subscribe(func(ev fabric.Event) {
		select {
		case t.queue <- ev:
		default: // あふれたら捨てる（スイッチを止めない）
		}
	})
	go t.loop()
	return t
}

// ZoningChanges はゾーニングを変えた回数。
func (t *Traps) ZoningChanges() uint {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.changes
}

func (t *Traps) loop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case ev := <-t.queue:
			t.onEvent(ev)
			t.checkPorts()
		case <-tick.C:
			t.checkPorts()
		}
	}
}

func (t *Traps) name() snmpagent.Var {
	return snmpagent.Var{OID: o(1, 1, 0), Type: gosnmp.OctetString, Value: t.sw.Info().Name}
}

func (t *Traps) emit(n int, vars ...snmpagent.Var) {
	if t.send == nil {
		return
	}
	t.send.Send(snmpagent.OIDString(o(0, n)), append([]snmpagent.Var{t.name()}, vars...))
}

func (t *Traps) onEvent(ev fabric.Event) {
	switch ev.Kind {
	case "login", "logout":
		n := 2
		if ev.Kind == "logout" {
			n = 3
		}
		i := fcidNum(ev.FCID)
		t.emit(n,
			snmpagent.Var{OID: o(3, 1, 1, i), Type: gosnmp.OctetString, Value: ev.FCID},
			snmpagent.Var{OID: o(3, 1, 2, i), Type: gosnmp.OctetString, Value: ev.WWPN},
			snmpagent.Var{OID: o(3, 1, 3, i), Type: gosnmp.Integer, Value: ev.Port},
		)
	case "zoning":
		t.mu.Lock()
		t.changes++
		c := t.changes
		t.mu.Unlock()
		t.emit(4,
			snmpagent.Var{OID: o(1, 6, 0), Type: gosnmp.Integer, Value: ev.Zones},
			snmpagent.Var{OID: o(1, 7, 0), Type: gosnmp.Counter32, Value: c},
		)
	}
}

// checkPorts はポートの状態（Online・NoLogin・Disabled）が変わったら portStateChange を送る。
func (t *Traps) checkPorts() {
	for _, p := range t.sw.Ports() {
		if t.states[p.Index] == p.State {
			continue
		}
		t.states[p.Index] = p.State
		i := p.Index + 1
		t.emit(1,
			snmpagent.Var{OID: o(2, 1, 1, i), Type: gosnmp.Integer, Value: p.Index},
			snmpagent.Var{OID: o(2, 1, 2, i), Type: gosnmp.OctetString, Value: p.Interface},
			snmpagent.Var{OID: o(2, 1, 4, i), Type: gosnmp.Integer, Value: StateNum(p.State)},
		)
	}
}
