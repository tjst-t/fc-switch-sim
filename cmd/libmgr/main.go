// libmgr はテープライブラリの管理 API の模擬（ライブラリの VM の中で動かす）。
//
//	libmgr serve [-listen :8080] [-lib 10] [-traps 172.30.31.30:162,...] [-community public]
//	libmgr snmp-pass [-api http://127.0.0.1:8080]   net-snmp の pass_persist（snmpd.conf から起こす）
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tjst-t/fc-switch-sim/sim/libmgr"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "使い方：libmgr serve|snmp-pass [引数]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "snmp-pass":
		snmpPass(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "知らない命令 %s\n", os.Args[1])
		os.Exit(2)
	}
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", ":8080", "REST の待ち受け")
	lib := fs.Int("lib", 10, "mhVTL のライブラリの番号")
	traps := fs.String("traps", "", "トラップの送り先（host:port をカンマ区切り）")
	community := fs.String("community", "public", "トラップのコミュニティ")
	interval := fs.Duration("interval", 2*time.Second, "ライブラリを読み直す間隔")
	debug := fs.Bool("debug", false, "細かいログも出す")
	fs.Parse(args)
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	m, err := libmgr.NewManager(libmgr.Config{LibraryID: *lib}, nil, log)
	if err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
	if *traps != "" {
		m.AddNotifier(libmgr.NewTrapSender(strings.Split(*traps, ","), *community, func(t string, err error) {
			log.Warn("トラップを送れない", "target", t, "err", err)
		}))
	}
	m.Poll()
	go func() {
		for range time.Tick(*interval) {
			m.Poll()
		}
	}()
	st := m.State()
	log.Info("起動した", "library", st.Library.Serial, "status", st.Library.Status, "drives", st.Library.DriveCount, "listen", *listen, "traps", *traps)
	if err := http.ListenAndServe(*listen, libmgr.Handler(m)); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func snmpPass(args []string) {
	fs := flag.NewFlagSet("snmp-pass", flag.ExitOnError)
	api := fs.String("api", "http://127.0.0.1:8080", "libmgr の REST")
	fs.Parse(args)
	client := &http.Client{Timeout: 3 * time.Second}
	var mu sync.Mutex
	var cached libmgr.State
	var at time.Time
	state := func() (libmgr.State, error) {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(at) < time.Second {
			return cached, nil
		}
		resp, err := client.Get(*api + "/api/v1/library")
		if err != nil {
			return cached, err
		}
		defer resp.Body.Close()
		var st libmgr.State
		if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
			return cached, err
		}
		cached, at = st, time.Now()
		return st, nil
	}
	if err := libmgr.PassPersist(os.Stdin, os.Stdout, state); err != nil {
		os.Exit(1)
	}
}
