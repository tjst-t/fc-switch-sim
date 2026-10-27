package libmgr

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// 試験用の口 /_sim/v1：データプレーン（ロボットの SCSI の答え）に障害を入れる。
// 管理 API（/api/v1）・SNMP・トラップには出さない。状態は mhVTL の vtllibrary のメモリにだけあり、
// VM やデーモンを起こし直すと消える（入れ直すのは呼ぶ側の役目）。
//
//	GET    /_sim/v1/state                 いまの障害・扉・待っている UNIT ATTENTION
//	PUT    /_sim/v1/faults/move-medium    {"key":4,"asc":"44","ascq":0,"direction":"any|load|unload"}
//	                                      MOVE MEDIUM と INITIALIZE ELEMENT STATUS をこの sense で失敗させる
//	DELETE /_sim/v1/faults/move-medium    障害を消す
//	POST   /_sim/v1/door/open             {"notReady":false}：ロボットを動かす命令は NOT READY 04h/03h
//	                                      （notReady=true なら TEST UNIT READY・READ ELEMENT STATUS も）
//	POST   /_sim/v1/door/close            {"clearFaults":true,"becomingReadySeconds":0}
//	                                      次のチェンジャーへの命令に UNIT ATTENTION 28h/00h を1回返す

// SimState は /_sim/v1/state の答え。
type SimState struct {
	Faults SimFaults `json:"faults"`
	Door   SimDoor   `json:"door"`
	// UnitAttentionPending は、扉を閉じたあとの 28h/00h をまだどの経路にも返していない。
	UnitAttentionPending bool `json:"unitAttentionPending"`
	// BecomingReadySeconds は NOT READY 04h/01h を返す残りの秒数。
	BecomingReadySeconds int `json:"becomingReadySeconds"`
}

type SimFaults struct {
	MoveMedium *SimSense `json:"moveMedium"` // 無ければ null
}

type SimSense struct {
	Key       int    `json:"key"`
	ASC       int    `json:"asc"`
	ASCQ      int    `json:"ascq"`
	Direction string `json:"direction"`
	Sense     string `json:"sense"` // "04/44/00" の形（16 進）
}

type SimDoor struct {
	Open     bool `json:"open"`
	NotReady bool `json:"notReady"` // 開いている間、TEST UNIT READY と READ ELEMENT STATUS も NOT READY
}

// hexOrInt は JSON の数（10 進）か文字列（16 進、0x は付けても付けなくてもよい）。
type hexOrInt int

func (h *hexOrInt) UnmarshalJSON(b []byte) error {
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		*h = hexOrInt(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("数か 16 進の文字列にする")
	}
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x"), 16, 16)
	if err != nil {
		return fmt.Errorf("16 進として読めない：%q", s)
	}
	*h = hexOrInt(v)
	return nil
}

func (m *Manager) vtlcmdLib(args ...string) (string, error) {
	full := append([]string{strconv.Itoa(m.cfg.LibraryID)}, args...)
	out, err := m.run("vtlcmd", full...)
	out = strings.TrimSpace(out)
	if err != nil {
		return out, fmt.Errorf("vtlcmd %s：%v %s", strings.Join(full, " "), err, out)
	}
	return out, nil
}

func (m *Manager) vtlcmdLibOK(args ...string) error {
	out, err := m.vtlcmdLib(args...)
	if err != nil {
		return err
	}
	if !strings.Contains(out, "OK") {
		return fmt.Errorf("vtlcmd %s：%s", strings.Join(args, " "), out)
	}
	return nil
}

// SimSetMoveFault は MOVE MEDIUM（と INITIALIZE ELEMENT STATUS）の障害を入れる。
func (m *Manager) SimSetMoveFault(key, asc, ascq int, direction string) error {
	if direction == "" {
		direction = "any"
	}
	switch {
	case key < 0 || key > 0xf:
		return fmt.Errorf("key は 0〜f（%d）", key)
	case asc < 0 || asc > 0xff || ascq < 0 || ascq > 0xff:
		return fmt.Errorf("asc・ascq は 0〜ff")
	case direction != "any" && direction != "load" && direction != "unload":
		return fmt.Errorf("direction は any・load・unload（%q）", direction)
	}
	return m.vtlcmdLibOK("fault", "move", fmt.Sprintf("%x", key), fmt.Sprintf("%x", asc), fmt.Sprintf("%x", ascq), direction)
}

// SimClearFaults は障害を消す。
func (m *Manager) SimClearFaults() error { return m.vtlcmdLibOK("fault", "clear") }

// SimDoorOpen は扉を開ける。
func (m *Manager) SimDoorOpen(notReady bool) error {
	args := []string{"door", "open"}
	if notReady {
		args = append(args, "notready")
	}
	return m.vtlcmdLibOK(args...)
}

// SimDoorClose は扉を閉じる（次の命令に 28h/00h を1回。clearFaults なら障害も消す）。
func (m *Manager) SimDoorClose(clearFaults bool, becomingReadySeconds int) error {
	if becomingReadySeconds < 0 || becomingReadySeconds > 3600 {
		return fmt.Errorf("becomingReadySeconds は 0〜3600")
	}
	args := []string{"door", "close"}
	if !clearFaults {
		args = append(args, "noclear")
	}
	if becomingReadySeconds > 0 {
		args = append(args, "becoming", strconv.Itoa(becomingReadySeconds))
	}
	return m.vtlcmdLibOK(args...)
}

// SimGetState は mhVTL の fault status を読む。
func (m *Manager) SimGetState() (SimState, error) {
	out, err := m.vtlcmdLib("fault", "status")
	if err != nil {
		return SimState{}, err
	}
	return ParseSimStatus(out)
}

// ParseSimStatus は vtlcmd <lib> fault status の1行を読む。
func ParseSimStatus(text string) (SimState, error) {
	kv := map[string]string{}
	for _, f := range strings.Fields(text) {
		if k, v, ok := strings.Cut(f, "="); ok {
			kv[k] = v
		}
	}
	if _, ok := kv["fault"]; !ok {
		return SimState{}, fmt.Errorf("fault status を読めない：%q（mhVTL に障害注入のパッチが当たっていない？）", firstLine(text))
	}
	hex := func(k string) int { v, _ := strconv.ParseInt(kv[k], 16, 32); return int(v) }
	var st SimState
	if kv["fault"] == "1" {
		st.Faults.MoveMedium = &SimSense{Key: hex("key"), ASC: hex("asc"), ASCQ: hex("ascq"), Direction: kv["direction"],
			Sense: fmt.Sprintf("%02x/%02x/%02x", hex("key"), hex("asc"), hex("ascq"))}
	}
	st.Door.Open = kv["door"] == "open"
	st.Door.NotReady = kv["door_notready"] == "1"
	st.UnitAttentionPending = kv["ua_pending"] == "1"
	st.BecomingReadySeconds, _ = strconv.Atoi(kv["becoming_ready"])
	return st, nil
}

// simHandlers は /_sim/v1 を mux に足す。
func simHandlers(mux *http.ServeMux, m *Manager) {
	reply := func(w http.ResponseWriter, err error) {
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		st, err := m.SimGetState()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, st)
	}
	decode := func(w http.ResponseWriter, r *http.Request, v any) bool {
		if r.ContentLength == 0 {
			return true
		}
		if err := json.NewDecoder(r.Body).Decode(v); err != nil && err != io.EOF {
			writeErr(w, 400, "JSON を読めない："+err.Error())
			return false
		}
		return true
	}
	mux.HandleFunc("GET /_sim/v1/state", func(w http.ResponseWriter, r *http.Request) { reply(w, nil) })
	mux.HandleFunc("PUT /_sim/v1/faults/move-medium", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Key       *hexOrInt `json:"key"`
			ASC       *hexOrInt `json:"asc"`
			ASCQ      *hexOrInt `json:"ascq"`
			Direction string    `json:"direction"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "JSON を読めない："+err.Error())
			return
		}
		if body.Key == nil || body.ASC == nil || body.ASCQ == nil {
			writeErr(w, 400, "key・asc・ascq が要る")
			return
		}
		if err := m.SimSetMoveFault(int(*body.Key), int(*body.ASC), int(*body.ASCQ), body.Direction); err != nil {
			if strings.HasPrefix(err.Error(), "vtlcmd") {
				writeErr(w, 500, err.Error())
			} else {
				writeErr(w, 400, err.Error())
			}
			return
		}
		reply(w, nil)
	})
	mux.HandleFunc("DELETE /_sim/v1/faults/move-medium", func(w http.ResponseWriter, r *http.Request) { reply(w, m.SimClearFaults()) })
	mux.HandleFunc("POST /_sim/v1/door/open", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			NotReady bool `json:"notReady"`
		}
		if !decode(w, r, &body) {
			return
		}
		reply(w, m.SimDoorOpen(body.NotReady))
	})
	mux.HandleFunc("POST /_sim/v1/door/close", func(w http.ResponseWriter, r *http.Request) {
		body := struct {
			ClearFaults          *bool `json:"clearFaults"`
			BecomingReadySeconds int   `json:"becomingReadySeconds"`
		}{}
		if !decode(w, r, &body) {
			return
		}
		clear := body.ClearFaults == nil || *body.ClearFaults
		if body.BecomingReadySeconds < 0 || body.BecomingReadySeconds > 3600 {
			writeErr(w, 400, "becomingReadySeconds は 0〜3600")
			return
		}
		reply(w, m.SimDoorClose(clear, body.BecomingReadySeconds))
	})
}
