package libmgr

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strings"
)

// 見回りの命令の CDB の CONTROL バイトに立てるベンダー固有のビット（bit 6）。
// mhVTL のパッチ 0003 は、このビットの立った命令に、扉を閉じたあとの UNIT ATTENTION 28h/00h・
// BECOMING READY・扉の NOT READY を返さない（28h/00h を見回りが先に受け取って消さないため）。
const pollVendorBit = 0x40

// SCSI の要素の種類（READ ELEMENT STATUS の要素の種類のコード）。
const (
	elemTransport    = 1
	elemStorage      = 2
	elemImportExport = 3
	elemDataTransfer = 4
)

// resAllocLen は READ ELEMENT STATUS で受け取る大きさの上限。
const resAllocLen = 0xffff

// readElementStatusCDB は、全部の要素をボリュームタグ付きで読む READ ELEMENT STATUS（0xB8）の CDB。
func readElementStatusCDB() []string {
	n := uint32(resAllocLen)
	cdb := []byte{0xb8, 0x10, 0x00, 0x00, 0xff, 0xff, 0x00,
		byte(n >> 16), byte(n >> 8), byte(n), 0x00, pollVendorBit}
	out := make([]string, len(cdb))
	for i, b := range cdb {
		out[i] = fmt.Sprintf("%02x", b)
	}
	return out
}

// readElementStatus はロボットの要素を sg_raw の READ ELEMENT STATUS で読む。
func readElementStatus(run Runner, sg string) (Elements, error) {
	f, err := os.CreateTemp("", "libmgr-res-*.bin")
	if err != nil {
		return Elements{}, err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	args := append([]string{"-r", fmt.Sprint(resAllocLen), "-o", path, sg}, readElementStatusCDB()...)
	if out, err := run("sg_raw", args...); err != nil {
		return Elements{}, fmt.Errorf("READ ELEMENT STATUS：%v %s", err, strings.TrimSpace(out))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Elements{}, err
	}
	return ParseElementStatus(data)
}

type rawElem struct {
	typ       int
	addr      int
	full      bool
	srcValid  bool
	src       int
	volumeTag string
}

// ParseElementStatus は READ ELEMENT STATUS の返り値（SMC-3 6.10）を読み、mtx status と同じ番号の振り方で
// Elements を作る：ドライブは住所の順に 0 から、保管スロットは住所の順に 1 から、メールスロットはその続きの番号。
// ドライブの「どこから入れたか」は、元の住所をこの番号に直す。
func ParseElementStatus(data []byte) (Elements, error) {
	e := Elements{Drives: map[int]ElemDrive{}}
	if len(data) < 8 {
		return e, fmt.Errorf("READ ELEMENT STATUS の返り値が短い（%d バイト）", len(data))
	}
	total := int(data[5])<<16 | int(data[6])<<8 | int(data[7])
	end := 8 + total
	if end > len(data) {
		end = len(data)
	}
	var elems []rawElem
	p := 8
	for p+8 <= end {
		typ := int(data[p] & 0x0f)
		pvol := data[p+1]&0x80 != 0
		dlen := int(binary.BigEndian.Uint16(data[p+2 : p+4]))
		bytes := int(data[p+5])<<16 | int(data[p+6])<<8 | int(data[p+7])
		p += 8
		pend := p + bytes
		if pend > end {
			pend = end
		}
		if dlen < 12 {
			return e, fmt.Errorf("要素の記述子の長さがおかしい（%d）", dlen)
		}
		for ; p+dlen <= pend; p += dlen {
			d := data[p : p+dlen]
			r := rawElem{typ: typ, addr: int(binary.BigEndian.Uint16(d[0:2])), full: d[2]&0x01 != 0,
				srcValid: d[9]&0x80 != 0, src: int(binary.BigEndian.Uint16(d[10:12]))}
			if pvol && dlen >= 12+36 {
				r.volumeTag = strings.TrimRight(string(d[12:44]), " \x00")
			}
			elems = append(elems, r)
		}
		p = pend
	}
	if len(elems) == 0 {
		return e, fmt.Errorf("READ ELEMENT STATUS に要素が無い")
	}
	byType := func(t int) []rawElem {
		var out []rawElem
		for _, r := range elems {
			if r.typ == t {
				out = append(out, r)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].addr < out[j].addr })
		return out
	}
	number := map[int]int{} // 保管スロット・メールスロットの住所 → mtx の番号
	n := 0
	for _, t := range []int{elemStorage, elemImportExport} {
		for _, r := range byType(t) {
			n++
			number[r.addr] = n
			s := Slot{Address: n, Type: SlotStorage, Full: r.full}
			if t == elemImportExport {
				s.Type = SlotImportExport
			}
			if r.full {
				s.Volume = firstField(r.volumeTag)
			}
			e.Slots = append(e.Slots, s)
		}
	}
	for i, r := range byType(elemDataTransfer) {
		d := ElemDrive{Full: r.full}
		if r.full {
			d.Volume = firstField(r.volumeTag)
			if r.srcValid {
				d.SourceSlot = number[r.src]
			}
		}
		e.Drives[i] = d
	}
	return e, nil
}

func firstField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}
