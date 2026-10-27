package libmgr

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config は libmgr の設定。
type Config struct {
	LibraryID  int    // mhVTL のライブラリの番号（既定 10）
	DeviceConf string // 既定 /etc/mhvtl/device.conf
	HomeDir    string // テープを置く場所（既定 /opt/mhvtl）
	MediaMB    int    // 投入するテープの容量（既定 500）
}

// Notifier は出来事を外へ知らせる（SNMP トラップ）。
type Notifier interface {
	Notify(ev Event, st State)
}

// Manager はライブラリの様子を見張り、操作を受ける。
type Manager struct {
	mu       sync.Mutex
	cfg      Config
	run      Runner
	log      *slog.Logger
	base     DeviceConf
	st       State
	ready    bool // 最初の読み取りが済んだ
	alerts   map[int][]int
	loads    map[int]uint64
	events   []Event
	notifier []Notifier
	now      func() time.Time
}

const maxEvents = 500

// NewManager は device.conf を読んで Manager を作る。
func NewManager(cfg Config, run Runner, log *slog.Logger) (*Manager, error) {
	if cfg.LibraryID == 0 {
		cfg.LibraryID = 10
	}
	if cfg.DeviceConf == "" {
		cfg.DeviceConf = "/etc/mhvtl/device.conf"
	}
	if cfg.HomeDir == "" {
		cfg.HomeDir = "/opt/mhvtl"
	}
	if cfg.MediaMB == 0 {
		cfg.MediaMB = 500
	}
	if run == nil {
		run = ExecRunner
	}
	if log == nil {
		log = slog.Default()
	}
	raw, err := os.ReadFile(cfg.DeviceConf)
	if err != nil {
		return nil, err
	}
	dc, err := ParseDeviceConf(string(raw), cfg.LibraryID)
	if err != nil {
		return nil, err
	}
	m := &Manager{cfg: cfg, run: run, log: log, base: dc, alerts: map[int][]int{}, loads: map[int]uint64{}, now: time.Now}
	m.st = m.compose(nil, StatusUnknown, "")
	return m, nil
}

// AddNotifier は出来事の知らせ先を足す。
func (m *Manager) AddNotifier(n Notifier) { m.notifier = append(m.notifier, n) }

// compose は device.conf の中身と、読んだ要素から State を作る。el が nil なら前の要素を使う。
func (m *Manager) compose(el *Elements, status, robot string) State {
	st := State{Library: m.base.Library, UpdatedAt: m.now()}
	st.Library.Status = status
	st.Library.Robot = robot
	prev := map[int]Drive{}
	for _, d := range m.st.Drives {
		prev[d.Index] = d
	}
	for _, d := range m.base.Drives {
		d.State = DriveEmpty
		if el != nil {
			if e, ok := el.Drives[d.Element]; ok && e.Full {
				d.State, d.Volume, d.SourceSlot = DriveLoaded, e.Volume, e.SourceSlot
			}
		} else if p, ok := prev[d.Index]; ok {
			d.State, d.Volume, d.SourceSlot = p.State, p.Volume, p.SourceSlot
		}
		d.Alerts = append([]int{}, m.alerts[d.Index]...)
		d.Loads = m.loads[d.Index]
		if len(d.Alerts) > 0 {
			st.Library.AlertDrives++
		}
		st.Drives = append(st.Drives, d)
	}
	if el != nil {
		st.Slots = append([]Slot{}, el.Slots...)
	} else {
		st.Slots = append([]Slot{}, m.st.Slots...)
	}
	st.Library.DriveCount = len(st.Drives)
	for _, s := range st.Slots {
		if s.Type == SlotImportExport {
			st.Library.IESlotCount++
		} else {
			st.Library.SlotCount++
		}
	}
	return st
}

// Poll はライブラリを読み直し、変わったところを出来事にする。
func (m *Manager) Poll() {
	robot, err := findRobot(m.run, m.base.Library.Serial)
	status := StatusUnknown
	var el *Elements
	if err != nil {
		m.log.Debug("ロボットが見つからない", "err", err)
	} else if !testUnitReady(m.run, robot) {
		status = StatusOffline
	} else {
		if e, err := readElementStatus(m.run, robot); err == nil {
			status, el = StatusOnline, &e
		} else {
			// TEST UNIT READY は通ったのに読めない：一時的なもの。前の要素のまま、状態は online とする
			m.log.Warn("READ ELEMENT STATUS が読めない", "err", err)
			status = StatusOnline
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.compose(el, status, robot)
	if m.ready {
		m.diff(m.st, &next)
	}
	m.st = next
	m.ready = true
}

// diff は前と後を比べて出来事を出す（ドライブの入れた回数もここで数える）。
func (m *Manager) diff(prev State, next *State) {
	if prev.Library.Status != next.Library.Status {
		m.emit(Event{Kind: EvStatusChange, Status: next.Library.Status,
			Message: fmt.Sprintf("ライブラリが %s から %s になった", prev.Library.Status, next.Library.Status)}, *next)
	}
	for i := range next.Drives {
		n := &next.Drives[i]
		var p Drive
		for _, d := range prev.Drives {
			if d.Index == n.Index {
				p = d
			}
		}
		if p.State == DriveLoaded && (n.State != DriveLoaded || p.Volume != n.Volume) {
			m.emit(Event{Kind: EvDriveUnload, Drive: n.Index, Volume: p.Volume,
				Message: fmt.Sprintf("ドライブ %d からテープ %s を出した", n.Index, p.Volume)}, *next)
		}
		if n.State == DriveLoaded && (p.State != DriveLoaded || p.Volume != n.Volume) {
			m.loads[n.Index]++
			n.Loads = m.loads[n.Index]
			m.emit(Event{Kind: EvDriveLoad, Drive: n.Index, Volume: n.Volume, Slot: n.SourceSlot,
				Message: fmt.Sprintf("ドライブ %d にテープ %s を入れた（スロット %d から）", n.Index, n.Volume, n.SourceSlot)}, *next)
		}
	}
	prevSlots := map[int]Slot{}
	for _, s := range prev.Slots {
		prevSlots[s.Address] = s
	}
	for _, s := range next.Slots {
		if s.Type != SlotImportExport {
			continue
		}
		p, ok := prevSlots[s.Address]
		if !ok {
			continue
		}
		switch {
		case !p.Full && s.Full:
			m.emit(Event{Kind: EvIEImport, Slot: s.Address, Volume: s.Volume,
				Message: fmt.Sprintf("メールスロット %d にテープ %s が入った", s.Address, s.Volume)}, *next)
		case p.Full && !s.Full:
			m.emit(Event{Kind: EvIERemove, Slot: s.Address, Volume: p.Volume,
				Message: fmt.Sprintf("メールスロット %d からテープ %s が無くなった", s.Address, p.Volume)}, *next)
		}
	}
}

// emit は出来事を記録して知らせる（m.mu を持ったまま呼ぶ）。
func (m *Manager) emit(ev Event, st State) {
	ev.Time = m.now()
	m.log.Info(ev.Message, "kind", ev.Kind)
	m.events = append(m.events, ev)
	if len(m.events) > maxEvents {
		m.events = m.events[len(m.events)-maxEvents:]
	}
	for _, n := range m.notifier {
		n.Notify(ev, st)
	}
}

// State はいまの様子を返す。
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st
}

// Events は出来事を新しい順に最大 n 件返す。
func (m *Manager) Events(n int) []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n <= 0 || n > len(m.events) {
		n = len(m.events)
	}
	out := make([]Event, 0, n)
	for i := len(m.events) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, m.events[i])
	}
	return out
}

// SetOnline はライブラリをオンライン・オフラインにする（vtlcmd）。
func (m *Manager) SetOnline(online bool) error {
	cmd := "offline"
	if online {
		cmd = "online"
	}
	if out, err := m.run("vtlcmd", strconv.Itoa(m.cfg.LibraryID), cmd); err != nil {
		return fmt.Errorf("vtlcmd %s：%v %s", cmd, err, strings.TrimSpace(out))
	}
	m.Poll()
	return nil
}

func (m *Manager) drive(index int) (Drive, bool) {
	for _, d := range m.base.Drives {
		if d.Index == index {
			return d, true
		}
	}
	return Drive{}, false
}

// SetAlerts はドライブ index の TapeAlert のフラグ（1〜64）を立てる。空なら消す。
// サーバが TapeAlert のログを読むとドライブの中のフラグは消える（規格どおり）が、API の記録は API で消すまで残る。
func (m *Manager) SetAlerts(index int, flags []int) error {
	d, ok := m.drive(index)
	if !ok {
		return fmt.Errorf("ドライブ %d は無い", index)
	}
	var mask uint64
	uniq := map[int]bool{}
	for _, f := range flags {
		if f < 1 || f > 64 {
			return fmt.Errorf("TapeAlert のフラグは 1〜64（%d）", f)
		}
		mask |= 1 << uint(f-1)
		uniq[f] = true
	}
	clean := make([]int, 0, len(uniq))
	for f := range uniq {
		clean = append(clean, f)
	}
	sort.Ints(clean)
	if out, err := m.run("vtlcmd", strconv.Itoa(d.ID), "TapeAlert", strconv.FormatUint(mask, 16)); err != nil {
		return fmt.Errorf("vtlcmd TapeAlert：%v %s", err, strings.TrimSpace(out))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(clean) == 0 {
		delete(m.alerts, index)
	} else {
		m.alerts[index] = clean
	}
	m.st = m.compose(nil, m.st.Library.Status, m.st.Library.Robot)
	msg := fmt.Sprintf("ドライブ %d の TapeAlert を %v にした", index, clean)
	if len(clean) == 0 {
		msg = fmt.Sprintf("ドライブ %d の TapeAlert を消した", index)
	}
	m.emit(Event{Kind: EvDriveAlert, Drive: index, Alerts: clean, Message: msg}, m.st)
	return nil
}

var reBarcode = regexp.MustCompile(`^[A-Z0-9]{6}L([5-8])$`)

// Import は新しいテープを作り、メールスロットに入れる（オペレータがテープを差し込むのにあたる）。
func (m *Manager) Import(barcode string) error {
	mm := reBarcode.FindStringSubmatch(barcode)
	if mm == nil {
		return fmt.Errorf("バーコード %q は 6 文字＋L＋世代（例 IMP001L8）の形にする", barcode)
	}
	if _, err := os.Stat(filepath.Join(m.cfg.HomeDir, barcode)); err == nil {
		return fmt.Errorf("テープ %s はもうある", barcode)
	}
	lib := strconv.Itoa(m.cfg.LibraryID)
	if out, err := m.run("mktape", "-l", lib, "-m", barcode, "-s", strconv.Itoa(m.cfg.MediaMB), "-t", "data", "-d", "LTO"+mm[1]); err != nil {
		return fmt.Errorf("mktape：%v %s", err, strings.TrimSpace(out))
	}
	// mhVTL 1.8 の vtlcmd load map は、古い形式の data のファイルがあるかで有無を確かめる（新しい形式は data.0）
	if err := os.WriteFile(filepath.Join(m.cfg.HomeDir, barcode, "data"), nil, 0o640); err != nil {
		return err
	}
	for _, args := range [][]string{{lib, "open", "map"}, {lib, "load", "map", barcode}, {lib, "close", "map"}} {
		out, err := m.run("vtlcmd", args...)
		if err != nil || !strings.Contains(out, "OK") {
			return fmt.Errorf("vtlcmd %s：%v %s", strings.Join(args, " "), err, strings.TrimSpace(out))
		}
	}
	m.Poll()
	return nil
}
