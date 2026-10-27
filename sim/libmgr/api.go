package libmgr

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Handler はテープライブラリの管理の REST API。
//
//	GET  /api/v1/library                    全体（ライブラリ・ドライブ・スロット）
//	GET  /api/v1/drives                     ドライブ
//	GET  /api/v1/slots                      スロット（保管とメールスロット）
//	GET  /api/v1/events?n=100               起きたこと（新しい順）
//	POST /api/v1/library/online             オンラインにする
//	POST /api/v1/library/offline            オフラインにする（サーバからのロボットの命令は Not Ready になる）
//	PUT  /api/v1/drives/{n}/tapealert       {"flags":[15]}：TapeAlert のフラグを立てる（[] で消す）
//	POST /api/v1/import                     {"barcode":"IMP001L8"}：新しいテープをメールスロットに入れる
//
// 試験用の口 /_sim/v1（データプレーンへの障害注入）は sim.go。
func Handler(m *Manager) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/library", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, m.State()) })
	mux.HandleFunc("GET /api/v1/drives", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, m.State().Drives) })
	mux.HandleFunc("GET /api/v1/slots", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, m.State().Slots) })
	mux.HandleFunc("GET /api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n == 0 {
			n = 100
		}
		writeJSON(w, 200, m.Events(n))
	})
	online := func(on bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if err := m.SetOnline(on); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			writeJSON(w, 200, m.State().Library)
		}
	}
	mux.HandleFunc("POST /api/v1/library/online", online(true))
	mux.HandleFunc("POST /api/v1/library/offline", online(false))
	mux.HandleFunc("PUT /api/v1/drives/{n}/tapealert", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(r.PathValue("n"))
		if err != nil {
			writeErr(w, 400, "ドライブの番号が数でない")
			return
		}
		var body struct {
			Flags []int `json:"flags"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "JSON を読めない："+err.Error())
			return
		}
		if err := m.SetAlerts(n, body.Flags); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		for _, d := range m.State().Drives {
			if d.Index == n {
				writeJSON(w, 200, d)
			}
		}
	})
	mux.HandleFunc("POST /api/v1/import", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Barcode string `json:"barcode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "JSON を読めない："+err.Error())
			return
		}
		if err := m.Import(body.Barcode); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, m.State().Slots)
	})
	simHandlers(mux, m)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
