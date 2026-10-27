package libmgr

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

// buildRES は Elements（mtx の番号）から、mhVTL と同じ形の READ ELEMENT STATUS の返り値を作る
// （ロボット 1・メールスロットは 10 から・ドライブは 500 から・保管スロットは 1000 から）。
func buildRES(e Elements) []byte {
	const dlen = 52
	var storage, ie []Slot
	for _, s := range e.Slots {
		if s.Type == SlotImportExport {
			ie = append(ie, s)
		} else {
			storage = append(storage, s)
		}
	}
	addrOf := func(n int) int { // mtx の番号 → 住所
		if n <= len(storage) {
			return 1000 + n - 1
		}
		return 10 + n - len(storage) - 1
	}
	desc := func(addr int, full bool, src int, tag string) []byte {
		d := make([]byte, dlen)
		binary.BigEndian.PutUint16(d[0:2], uint16(addr))
		if full {
			d[2] = 0x01
		}
		if src > 0 {
			d[9] = 0x80
			binary.BigEndian.PutUint16(d[10:12], uint16(src))
		}
		copy(d[12:48], []byte(tag + strings.Repeat(" ", 36))[:36])
		return d
	}
	page := func(typ int, descs [][]byte) []byte {
		h := []byte{byte(typ), 0x80, 0, dlen, 0, 0, 0, 0}
		n := len(descs) * dlen
		h[5], h[6], h[7] = byte(n>>16), byte(n>>8), byte(n)
		for _, d := range descs {
			h = append(h, d...)
		}
		return h
	}
	var body []byte
	body = append(body, page(elemTransport, [][]byte{desc(1, false, 0, "")})...)
	var ds [][]byte
	for i, s := range ie {
		ds = append(ds, desc(10+i, s.Full, 0, s.Volume))
	}
	body = append(body, page(elemImportExport, ds)...)
	ds = nil
	for i := 0; i < len(e.Drives); i++ {
		d := e.Drives[i]
		src := 0
		if d.SourceSlot > 0 {
			src = addrOf(d.SourceSlot)
		}
		ds = append(ds, desc(500+i, d.Full, src, d.Volume))
	}
	body = append(body, page(elemDataTransfer, ds)...)
	ds = nil
	for i, s := range storage {
		ds = append(ds, desc(1000+i, s.Full, 0, s.Volume))
	}
	body = append(body, page(elemStorage, ds)...)
	h := []byte{0, 1, 0, 0, 0, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	return append(h, body...)
}

// ラボの VM（lib1 の mhVTL）から採った READ ELEMENT STATUS と mtx status の見本が、同じ Elements になる。
func TestParseElementStatusMatchesMtx(t *testing.T) {
	for _, name := range []string{"l700-res.bin", "l700-res-loaded.bin"} {
		raw, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		txt, err := os.ReadFile("testdata/" + strings.Replace(strings.Replace(name, "res", "mtx", 1), ".bin", ".txt", 1))
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseElementStatus(raw)
		if err != nil {
			t.Fatal(err)
		}
		want, err := ParseMtxStatus(string(txt))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s：READ ELEMENT STATUS の読み %+v\nmtx status の読み %+v", name, got, want)
		}
	}
	raw, _ := os.ReadFile("testdata/l700-res-loaded.bin")
	e, _ := ParseElementStatus(raw)
	if d := e.Drives[2]; !d.Full || d.Volume != "F01030L6" || d.SourceSlot != 30 || len(e.Slots) != 43 || e.Slots[42].Type != SlotImportExport {
		t.Fatalf("見本の読みがおかしい：%+v %d", d, len(e.Slots))
	}
	// 自作の見本（buildRES）も往復する
	for _, txt := range []string{mtxEmpty, mtxLoaded} {
		want, _ := ParseMtxStatus(txt)
		got, err := ParseElementStatus(buildRES(want))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("往復しない：%v %+v %+v", err, got, want)
		}
	}
	if _, err := ParseElementStatus([]byte{0, 1}); err == nil {
		t.Fatalf("短い返り値で失敗しない")
	}
}

// 見回りの TUR と READ ELEMENT STATUS は CONTROL バイトにベンダーのビットを立て、mtx は使わない。
func TestPollUsesVendorBit(t *testing.T) {
	m, f := newTestManager(t)
	m.Poll()
	var tur, res bool
	for _, c := range f.calls {
		switch {
		case c == "sg_raw /dev/sg2 00 00 00 00 00 40":
			tur = true
		case strings.HasPrefix(c, "sg_raw -r 65535 -o ") && strings.HasSuffix(c, " /dev/sg2 b8 10 00 00 ff ff 00 00 ff ff 00 40"):
			res = true
		case strings.HasPrefix(c, "mtx "):
			t.Fatalf("mtx を呼んだ：%s", c)
		}
	}
	if !tur || !res {
		t.Fatalf("見回りの命令が違う：%v", f.calls)
	}
}

func TestSimAPI(t *testing.T) {
	m, f := newTestManager(t)
	m.Poll()
	f.simStatus = "fault=1 key=04 asc=44 ascq=00 direction=load door=open door_notready=1 ua_pending=1 becoming_ready=3"
	srv := httptest.NewServer(Handler(m))
	defer srv.Close()
	do := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v map[string]any
		json.NewDecoder(resp.Body).Decode(&v)
		return resp.StatusCode, v
	}
	last := func() string { return f.calls[len(f.calls)-2] } // 最後は fault status
	cases := []struct {
		method, path, body string
		code               int
		vtlcmd             string
	}{
		{"PUT", "/_sim/v1/faults/move-medium", `{"key":4,"asc":68,"ascq":0}`, 200, "vtlcmd 10 fault move 4 44 0 any"},
		{"PUT", "/_sim/v1/faults/move-medium", `{"key":"04","asc":"0x44","ascq":"01","direction":"unload"}`, 200, "vtlcmd 10 fault move 4 44 1 unload"},
		{"DELETE", "/_sim/v1/faults/move-medium", ``, 200, "vtlcmd 10 fault clear"},
		{"POST", "/_sim/v1/door/open", ``, 200, "vtlcmd 10 door open"},
		{"POST", "/_sim/v1/door/open", `{"notReady":true}`, 200, "vtlcmd 10 door open notready"},
		{"POST", "/_sim/v1/door/close", ``, 200, "vtlcmd 10 door close"},
		{"POST", "/_sim/v1/door/close", `{"clearFaults":false,"becomingReadySeconds":5}`, 200, "vtlcmd 10 door close noclear becoming 5"},
	}
	for _, c := range cases {
		code, v := do(c.method, c.path, c.body)
		if code != c.code || last() != c.vtlcmd {
			t.Fatalf("%s %s %s：%d %v（vtlcmd %q）", c.method, c.path, c.body, code, v, last())
		}
	}
	for _, bad := range []string{`{"key":4,"asc":68}`, `{"key":16,"asc":0,"ascq":0}`, `{"key":4,"asc":"zz","ascq":0}`, `{"key":4,"asc":1,"ascq":0,"direction":"up"}`} {
		n := len(f.calls)
		if code, _ := do("PUT", "/_sim/v1/faults/move-medium", bad); code != 400 || len(f.calls) != n {
			t.Fatalf("%s が 400 にならない（%d）", bad, code)
		}
	}
	code, v := do("GET", "/_sim/v1/state", "")
	b, _ := json.Marshal(v)
	want := `{"becomingReadySeconds":3,"door":{"notReady":true,"open":true},"faults":{"moveMedium":{"asc":68,"ascq":0,"direction":"load","key":4,"sense":"04/44/00"}},"unitAttentionPending":true}`
	if code != 200 || string(b) != want {
		t.Fatalf("state が %d %s", code, b)
	}
	f.simStatus = "fault=0 key=00 asc=00 ascq=00 direction=any door=closed door_notready=0 ua_pending=0 becoming_ready=0"
	_, v = do("GET", "/_sim/v1/state", "")
	b, _ = json.Marshal(v)
	if string(b) != `{"becomingReadySeconds":0,"door":{"notReady":false,"open":false},"faults":{"moveMedium":null},"unitAttentionPending":false}` {
		t.Fatalf("空の state が %s", b)
	}
	f.simStatus = "Contents: "
	if code, _ := do("GET", "/_sim/v1/state", ""); code != 500 {
		t.Fatalf("パッチの無い mhVTL で 500 にならない")
	}
	// /api/v1 の形は変わらない
	if code, v := do("GET", "/api/v1/library", ""); code != 200 || v["library"] == nil || v["drives"] == nil {
		t.Fatalf("/api/v1/library が %d %v", code, v)
	}
}
