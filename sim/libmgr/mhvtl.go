package libmgr

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Runner は命令を流して標準出力を返す（試験では差し替える）。
type Runner func(name string, args ...string) (string, error)

// ExecRunner は本物の命令を流す（10 秒で打ち切る）。
func ExecRunner(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// DeviceConf は mhVTL の device.conf から読んだ、1 つのライブラリとそのドライブ。
type DeviceConf struct {
	Library Library
	Drives  []Drive
}

// ParseDeviceConf は device.conf からライブラリ libID とそのドライブを読む。
func ParseDeviceConf(text string, libID int) (DeviceConf, error) {
	type block struct {
		kind string
		id   int
		kv   map[string]string
	}
	var blocks []*block
	var cur *block
	sc := bufio.NewScanner(strings.NewReader(text))
	head := regexp.MustCompile(`^(Library|Drive):\s*(\d+)`)
	libSlot := regexp.MustCompile(`Library ID:\s*(\d+)\s+Slot:\s*(\d+)`)
	for sc.Scan() {
		line := sc.Text()
		if m := head.FindStringSubmatch(line); m != nil {
			id, _ := strconv.Atoi(m[2])
			cur = &block{kind: m[1], id: id, kv: map[string]string{}}
			blocks = append(blocks, cur)
			continue
		}
		if cur == nil || !strings.HasPrefix(line, " ") {
			continue
		}
		if m := libSlot.FindStringSubmatch(line); m != nil {
			cur.kv["libid"], cur.kv["slot"] = m[1], m[2]
			continue
		}
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok {
			cur.kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	var dc DeviceConf
	found := false
	for _, b := range blocks {
		switch {
		case b.kind == "Library" && b.id == libID:
			found = true
			dc.Library = Library{ID: libID, Vendor: b.kv["Vendor identification"], Product: b.kv["Product identification"], Serial: b.kv["Unit serial number"]}
		case b.kind == "Drive" && b.kv["libid"] == strconv.Itoa(libID):
			slot, _ := strconv.Atoi(b.kv["slot"])
			dc.Drives = append(dc.Drives, Drive{
				Index: slot, ID: b.id, Element: slot - 1,
				Vendor: b.kv["Vendor identification"], Product: b.kv["Product identification"], Serial: b.kv["Unit serial number"],
			})
		}
	}
	if !found {
		return dc, fmt.Errorf("device.conf にライブラリ %d が無い", libID)
	}
	sort.Slice(dc.Drives, func(i, j int) bool { return dc.Drives[i].Index < dc.Drives[j].Index })
	return dc, nil
}

// Elements は mtx status から読んだ要素。
type Elements struct {
	Drives map[int]ElemDrive // Data Transfer Element の番号 → 中身
	Slots  []Slot
}

type ElemDrive struct {
	Full       bool
	Volume     string
	SourceSlot int
}

var (
	reDTE  = regexp.MustCompile(`^Data Transfer Element (\d+):(Full|Empty)(?: \(Storage Element (\d+) Loaded\))?(?::VolumeTag\s*=\s*(\S+))?`)
	reSlot = regexp.MustCompile(`^Storage Element (\d+)( IMPORT/EXPORT)?:(Full|Empty)(?:\s*:VolumeTag=(\S+))?`)
)

// ParseMtxStatus は `mtx -f <sg> status` の出力を読む。
func ParseMtxStatus(text string) (Elements, error) {
	e := Elements{Drives: map[int]ElemDrive{}}
	sc := bufio.NewScanner(strings.NewReader(text))
	seen := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if m := reDTE.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			src, _ := strconv.Atoi(m[3])
			e.Drives[n] = ElemDrive{Full: m[2] == "Full", Volume: m[4], SourceSlot: src}
			seen = true
			continue
		}
		if m := reSlot.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			t := SlotStorage
			if m[2] != "" {
				t = SlotImportExport
			}
			e.Slots = append(e.Slots, Slot{Address: n, Type: t, Full: m[3] == "Full", Volume: m[4]})
			seen = true
		}
	}
	if !seen {
		return e, fmt.Errorf("mtx status を読めない：%q", firstLine(text))
	}
	return e, nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

// findRobot は VM の中のロボットの /dev/sg を、シリアル番号で探す。
func findRobot(run Runner, serial string) (string, error) {
	out, err := run("lsscsi", "-g")
	if err != nil {
		return "", fmt.Errorf("lsscsi：%v", err)
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[1] != "mediumx" {
			continue
		}
		sg := f[len(f)-1]
		vpd, err := run("sg_vpd", "-p", "sn", sg)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(vpd, "\n") {
			if k, v, ok := strings.Cut(l, ":"); ok && strings.Contains(k, "serial number") && strings.TrimSpace(v) == serial {
				return sg, nil
			}
		}
	}
	return "", fmt.Errorf("シリアル番号 %s のロボットが見つからない", serial)
}

// testUnitReady はロボットが命令を受けられるか（vtlcmd offline だと通らない）。
func testUnitReady(run Runner, sg string) bool {
	_, err := run("sg_raw", sg, "00", "00", "00", "00", "00", "00")
	return err == nil
}
