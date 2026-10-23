// fcsw は FCoE の FC スイッチ（FCF）のシミュレーター。
//
//	fcsw -config switch.yaml [-listen :8080]
//
// 設定のファイルに snmp: があれば、SNMP（FCSIM-FC-SWITCH-MIB）で答え、トラップを送る：
//
//	snmp:
//	  listen: ":161"
//	  community: public
//	  traps: ["172.30.31.30:162", "172.30.31.43:1162"]
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/tjst-t/fc-switch-sim/sim/api"
	"github.com/tjst-t/fc-switch-sim/sim/fabric"
	"github.com/tjst-t/fc-switch-sim/sim/rawport"
	"github.com/tjst-t/fc-switch-sim/sim/snmpagent"
	"github.com/tjst-t/fc-switch-sim/sim/swsnmp"
)

// SNMPConfig は設定のファイルの snmp:。
type SNMPConfig struct {
	Listen    string   `yaml:"listen"`
	Community string   `yaml:"community"`
	Traps     []string `yaml:"traps"`
}

func main() {
	cfgPath := flag.String("config", "/etc/fcsw/switch.yaml", "設定のファイル")
	listen := flag.String("listen", ":8080", "API の待ち受け")
	wait := flag.Duration("wait-interfaces", 60*time.Second, "インタフェースが現れるまで待つ時間")
	debug := flag.Bool("debug", false, "細かいログも出す")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if err := run(*cfgPath, *listen, *wait, log); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func run(cfgPath, listen string, wait time.Duration, log *slog.Logger) error {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	var cfg fabric.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("%s を読めない：%w", cfgPath, err)
	}
	var extra struct {
		SNMP *SNMPConfig `yaml:"snmp"`
	}
	if err := yaml.Unmarshal(raw, &extra); err != nil {
		return fmt.Errorf("%s の snmp を読めない：%w", cfgPath, err)
	}
	var ports []*rawport.Port
	var trs []fabric.Transport
	for _, pc := range cfg.Ports {
		p, err := rawport.Open(pc.Interface, wait)
		if err != nil {
			return err
		}
		log.Info("ポートを開いた", "interface", pc.Interface, "mac", p.MAC(), "rcvbuf", p.RcvBuf())
		ports = append(ports, p)
		trs = append(trs, p)
	}
	sw, err := fabric.New(cfg, trs, log)
	if err != nil {
		return err
	}
	for i, p := range ports {
		go func() {
			if err := p.Run(func(b []byte) { sw.Receive(i, b) }); err != nil {
				log.Error("受け取れなくなった", "port", i, "err", err)
				os.Exit(1)
			}
		}()
	}
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for range t.C {
			for i, p := range ports {
				sw.SetLink(i, p.LinkUp())
			}
			sw.Tick()
		}
	}()
	if sc := extra.SNMP; sc != nil {
		start := time.Now()
		if sc.Listen == "" {
			sc.Listen = ":161"
		}
		if sc.Community == "" {
			sc.Community = "public"
		}
		var sender *snmpagent.TrapSender
		if len(sc.Traps) > 0 {
			sender = &snmpagent.TrapSender{Targets: sc.Traps, Community: sc.Community, Start: start, Log: log}
		}
		traps := swsnmp.NewTraps(sw, sender)
		agent := &snmpagent.Agent{Community: sc.Community, Log: log,
			Vars: func() []snmpagent.Var { return swsnmp.Vars(sw, traps.ZoningChanges(), start) }}
		go func() {
			if err := agent.Serve(sc.Listen); err != nil {
				log.Error("SNMP で待ち受けられない", "err", err)
				os.Exit(1)
			}
		}()
		log.Info("SNMP", "listen", sc.Listen, "traps", sc.Traps)
	}
	info := sw.Info()
	log.Info("起動した", "name", info.Name, "switchWWN", info.SwitchWWN, "domain", info.DomainID, "ports", info.Ports, "api", listen)
	return http.ListenAndServe(listen, api.Handler(sw))
}
