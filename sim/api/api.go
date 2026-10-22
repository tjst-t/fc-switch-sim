// Package api はスイッチを操作・観察する HTTP の API（ベンダーに依らない形）。
//
//	GET  /api/v1/switch              スイッチの全体
//	GET  /api/v1/ports               ポートの一覧
//	POST /api/v1/ports/{n}/enable    ポートを有効にする
//	POST /api/v1/ports/{n}/disable   ポートを無効にする（ログインを切る）
//	GET  /api/v1/nameserver          ネームサーバ（ログインしている N_Port）
//	GET  /api/v1/zoning              いまのゾーニング
//	PUT  /api/v1/zoning              ゾーニングを入れ替える（RSCN で知らせる）
//	GET  /api/v1/events?n=100        起きたこと（新しい順）
package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/tjst-t/fc-switch-sim/sim/fabric"
)

// Handler は API の http.Handler を返す。
func Handler(sw *fabric.Switch) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/switch", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, sw.Info())
	})
	mux.HandleFunc("GET /api/v1/ports", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, sw.Ports())
	})
	portOp := func(enable bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			n, err := strconv.Atoi(r.PathValue("n"))
			if err != nil {
				writeErr(w, http.StatusBadRequest, "ポートの番号が数でない")
				return
			}
			if err := sw.SetPortEnabled(n, enable); err != nil {
				writeErr(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, sw.Ports()[n])
		}
	}
	mux.HandleFunc("POST /api/v1/ports/{n}/enable", portOp(true))
	mux.HandleFunc("POST /api/v1/ports/{n}/disable", portOp(false))
	mux.HandleFunc("GET /api/v1/nameserver", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, sw.NameServer())
	})
	mux.HandleFunc("GET /api/v1/zoning", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, sw.Zoning())
	})
	mux.HandleFunc("PUT /api/v1/zoning", func(w http.ResponseWriter, r *http.Request) {
		var z fabric.Zoning
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&z); err != nil {
			writeErr(w, http.StatusBadRequest, "JSON を読めない："+err.Error())
			return
		}
		if err := sw.SetZoning(z); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, sw.Zoning())
	})
	mux.HandleFunc("GET /api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n == 0 {
			n = 100
		}
		writeJSON(w, http.StatusOK, sw.Events(n))
	})
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
