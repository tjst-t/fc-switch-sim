package libmgr

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const deviceConf = `VERSION: 5
Library: 10 CHANNEL: 00 TARGET: 00 LUN: 00
 Vendor identification: STK
 Product identification: L700
 Unit serial number: LIB1_A
Drive: 11 CHANNEL: 00 TARGET: 01 LUN: 00
 Library ID: 10 Slot: 01
 Vendor identification: IBM
 Product identification: ULT3580-TD8
 Unit serial number: LIB1_A1
Drive: 12 CHANNEL: 00 TARGET: 02 LUN: 00
 Library ID: 10 Slot: 02
 Vendor identification: IBM
 Product identification: ULT3580-TD6
 Unit serial number: LIB1_A2
Library: 30 CHANNEL: 00 TARGET: 08 LUN: 00
 Vendor identification: STK
 Product identification: L80
 Unit serial number: XYZZY_B
Drive: 31 CHANNEL: 00 TARGET: 09 LUN: 00
 Library ID: 30 Slot: 01
 Unit serial number: XYZZY_B1
`

const mtxEmpty = `  Storage Changer /dev/sg2:2 Drives, 4 Slots ( 1 Import/Export )
Data Transfer Element 0:Empty
Data Transfer Element 1:Empty
      Storage Element 1:Full :VolumeTag=E11001L8
      Storage Element 2:Full :VolumeTag=E11002L8
      Storage Element 3:Empty
      Storage Element 4 IMPORT/EXPORT:Empty
`

const mtxLoaded = `  Storage Changer /dev/sg2:2 Drives, 4 Slots ( 1 Import/Export )
Data Transfer Element 0:Full (Storage Element 1 Loaded):VolumeTag = E11001L8
Data Transfer Element 1:Empty
      Storage Element 1:Empty
      Storage Element 2:Full :VolumeTag=E11002L8
      Storage Element 3:Empty
      Storage Element 4 IMPORT/EXPORT:Full :VolumeTag=IMP001L8
`

func TestParse(t *testing.T) {
	dc, err := ParseDeviceConf(deviceConf, 10)
	if err != nil {
		t.Fatal(err)
	}
	if dc.Library.Serial != "LIB1_A" || len(dc.Drives) != 2 || dc.Drives[1].Serial != "LIB1_A2" || dc.Drives[1].Element != 1 || dc.Drives[1].ID != 12 {
		t.Fatalf("device.conf の読み取りがおかしい：%+v", dc)
	}
	e, err := ParseMtxStatus(mtxLoaded)
	if err != nil {
		t.Fatal(err)
	}
	if d := e.Drives[0]; !d.Full || d.Volume != "E11001L8" || d.SourceSlot != 1 {
		t.Fatalf("ドライブ 0 がおかしい：%+v", d)
	}
	if len(e.Slots) != 4 || e.Slots[3].Type != SlotImportExport || e.Slots[3].Volume != "IMP001L8" {
		t.Fatalf("スロットがおかしい：%+v", e.Slots)
	}
}

// fakeLib は mhVTL の代わり。mtx は見本の mtx status（READ ELEMENT STATUS の返り値はここから作る）。
type fakeLib struct {
	mtx       string
	online    bool
	calls     []string
	notices   []Event
	simStatus string
}

func (f *fakeLib) run(name string, args ...string) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	switch name {
	case "lsscsi":
		return "[2:0:0:0]    mediumx STK      L700             0108  /dev/sch0  /dev/sg2\n", nil
	case "sg_vpd":
		return "VPD INQUIRY: Unit serial number page\n  Unit serial number: LIB1_A\n", nil
	case "sg_raw":
		if !f.online {
			return "", errFake
		}
		for i, a := range args {
			if a == "-o" {
				e, err := ParseMtxStatus(f.mtx)
				if err != nil {
					return "", err
				}
				return "", os.WriteFile(args[i+1], buildRES(e), 0o600)
			}
		}
		return "", nil
	case "mtx":
		return f.mtx, nil
	case "vtlcmd":
		if len(args) >= 3 && args[1] == "fault" && args[2] == "status" {
			return f.simStatus, nil
		}
		return "OK", nil
	}
	return "", nil
}

func (f *fakeLib) Notify(ev Event, st State) { f.notices = append(f.notices, ev) }

type fakeErr struct{}

func (fakeErr) Error() string { return "失敗" }

var errFake = fakeErr{}

func newTestManager(t *testing.T) (*Manager, *fakeLib) {
	dir := t.TempDir()
	p := filepath.Join(dir, "device.conf")
	os.WriteFile(p, []byte(deviceConf), 0o644)
	f := &fakeLib{mtx: mtxEmpty, online: true}
	m, err := NewManager(Config{DeviceConf: p, HomeDir: dir}, f.run, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.AddNotifier(f)
	return m, f
}

func kinds(evs []Event) string {
	var k []string
	for _, e := range evs {
		k = append(k, e.Kind)
	}
	return strings.Join(k, ",")
}

func TestPollEvents(t *testing.T) {
	m, f := newTestManager(t)
	m.Poll()
	if len(f.notices) != 0 {
		t.Fatalf("最初の読み取りで出来事を出した：%v", kinds(f.notices))
	}
	st := m.State()
	if st.Library.Status != StatusOnline || st.Library.SlotCount != 3 || st.Library.IESlotCount != 1 || st.Library.DriveCount != 2 {
		t.Fatalf("状態がおかしい：%+v", st.Library)
	}
	f.mtx = mtxLoaded
	m.Poll()
	if got := kinds(f.notices); got != "driveLoad,ieImport" {
		t.Fatalf("出来事が %s（driveLoad,ieImport のはず）", got)
	}
	if d := m.State().Drives[0]; d.State != DriveLoaded || d.Volume != "E11001L8" || d.Loads != 1 {
		t.Fatalf("ドライブ 1 がおかしい：%+v", d)
	}
	f.notices = nil
	f.online = false
	m.Poll()
	if got := kinds(f.notices); got != "libraryStatusChange" || m.State().Library.Status != StatusOffline {
		t.Fatalf("オフラインの出来事が %s", got)
	}
	// オフラインの間は前の要素を覚えている
	if m.State().Drives[0].Volume != "E11001L8" {
		t.Fatalf("オフラインで要素を忘れた")
	}
	f.notices = nil
	f.online = true
	f.mtx = mtxEmpty
	m.Poll()
	if got := kinds(f.notices); got != "libraryStatusChange,driveUnload,ieRemove" {
		t.Fatalf("出来事が %s", got)
	}
}

func TestAlertsAndSNMP(t *testing.T) {
	m, f := newTestManager(t)
	m.Poll()
	if err := m.SetAlerts(2, []int{20, 15, 15}); err != nil {
		t.Fatal(err)
	}
	if last := f.calls[len(f.calls)-1]; last != "vtlcmd 12 TapeAlert 84000" {
		t.Fatalf("vtlcmd の引数が %q", last)
	}
	if err := m.SetAlerts(3, []int{1}); err == nil {
		t.Fatalf("無いドライブで失敗しない")
	}
	st := m.State()
	if st.Library.AlertDrives != 1 || len(st.Drives[1].Alerts) != 2 {
		t.Fatalf("TapeAlert の記録がおかしい：%+v", st.Drives[1])
	}
	vs := Vars(st)
	v, ok := Lookup(vs, oid(2, 1, 6, 2), false)
	if !ok || v.Value != "15,20" {
		t.Fatalf("ドライブ 2 の alerts が %+v", v)
	}
	// getnext で全部をたどれる（順に増える）
	var in bytes.Buffer
	in.WriteString("PING\ngetnext\n" + EnterpriseOID + "\n")
	cur := EnterpriseOID
	n := 0
	for {
		var out bytes.Buffer
		PassPersist(strings.NewReader("getnext\n"+cur+"\n"), &out, func() (State, error) { return st, nil })
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		if lines[0] == "NONE" {
			break
		}
		if len(lines) != 3 {
			t.Fatalf("pass_persist の答えが 3 行でない：%q", out.String())
		}
		a, _ := parseOID(cur)
		b, _ := parseOID(lines[0])
		if cmpOID(b, a) <= 0 {
			t.Fatalf("getnext が進まない：%s → %s", cur, lines[0])
		}
		cur = lines[0]
		n++
		if n > 1000 {
			t.Fatalf("終わらない")
		}
	}
	if n != len(vs) {
		t.Fatalf("getnext でたどった数 %d と値の数 %d が合わない", n, len(vs))
	}
	var out bytes.Buffer
	PassPersist(strings.NewReader("PING\nget\n"+EnterpriseOID+".1.3.0\n"), &out, func() (State, error) { return st, nil })
	if out.String() != "PONG\n"+EnterpriseOID+".1.3.0\nstring\nLIB1_A\n" {
		t.Fatalf("get の答えが %q", out.String())
	}
	trapOID, pdus := TrapVars(Event{Kind: EvDriveAlert, Drive: 2, Alerts: []int{15}}, st)
	if trapOID != EnterpriseOID+".0.4" || len(pdus) != 3 {
		t.Fatalf("トラップがおかしい：%s %+v", trapOID, pdus)
	}
	_ = time.Now
}
