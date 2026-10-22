// Package fabric は FCoE の FCF（FC スイッチ）を模擬する本体。
//
// 各ポートで FIP（発見・ログイン・キープアライブ）に応え、FC_ID を割り当て、
// ネームサーバとファブリックのコントローラを持ち、ログインした VN_Port どうしの
// FCoE のフレームを、ゾーニングで絞りながら転送する。
package fabric

import (
	"fmt"
	"time"

	"github.com/tjst-t/fc-switch-sim/sim/fc"
)

// Config はスイッチの設定。
type Config struct {
	Name       string `json:"name" yaml:"name"`
	SwitchWWN  fc.WWN `json:"switchWWN" yaml:"switchWWN"`
	FabricName fc.WWN `json:"fabricName,omitempty" yaml:"fabricName,omitempty"` // 省けば SwitchWWN
	DomainID   byte   `json:"domainID" yaml:"domainID"`
	FCMap      uint32 `json:"fcMap,omitempty" yaml:"fcMap,omitempty"` // 省けば 0x0EFC00
	// FKAPeriodMs は FIP のキープアライブの間隔（広告もこの間隔で出す）。
	FKAPeriodMs uint32 `json:"fkaPeriodMs,omitempty" yaml:"fkaPeriodMs,omitempty"`
	Priority    byte   `json:"priority,omitempty" yaml:"priority,omitempty"`
	// MaxPayload は受け取れる FC の中身の最大の長さ（既定 2112）。
	MaxPayload uint16 `json:"maxPayload,omitempty" yaml:"maxPayload,omitempty"`
	// NoPadSolicitedAdv を true にすると、求めに応じた広告を ENode の最大の長さまで詰めない。
	// 詰めるのが規格どおりで、経路の MTU が足りないとログインできない（本物と同じ）。
	NoPadSolicitedAdv bool `json:"noPadSolicitedAdv,omitempty" yaml:"noPadSolicitedAdv,omitempty"`
	// ENodeTimeoutFactor は ENode のキープアライブが途絶えたとみなすまでの、FKA の間隔の倍数（既定 2.5。0 未満なら見ない）。
	ENodeTimeoutFactor float64 `json:"enodeTimeoutFactor,omitempty" yaml:"enodeTimeoutFactor,omitempty"`

	Ports  []PortConfig `json:"ports" yaml:"ports"`
	Zoning Zoning       `json:"zoning" yaml:"zoning"`
}

// PortConfig はスイッチのポート 1 つ。番号は並びの順（0 から）。
type PortConfig struct {
	Interface string `json:"interface" yaml:"interface"`
	Disabled  bool   `json:"disabled,omitempty" yaml:"disabled,omitempty"`
}

func (c *Config) setDefaults() error {
	if c.SwitchWWN == 0 {
		return fmt.Errorf("switchWWN が要る")
	}
	if c.DomainID == 0 || c.DomainID > 239 {
		return fmt.Errorf("domainID は 1〜239（いまは %d）", c.DomainID)
	}
	if c.FabricName == 0 {
		c.FabricName = c.SwitchWWN
	}
	if c.FCMap == 0 {
		c.FCMap = fc.DefaultFCMap
	}
	if c.FKAPeriodMs == 0 {
		c.FKAPeriodMs = 8000
	}
	if c.Priority == 0 {
		c.Priority = 128
	}
	if c.MaxPayload == 0 {
		c.MaxPayload = 2112
	}
	if c.ENodeTimeoutFactor == 0 {
		c.ENodeTimeoutFactor = 2.5
	}
	if c.Zoning.DefaultZone == "" {
		c.Zoning.DefaultZone = DefaultZoneAll
	}
	if len(c.Ports) > 256 {
		return fmt.Errorf("ポートは 256 まで")
	}
	return nil
}

func (c *Config) fkaPeriod() time.Duration { return time.Duration(c.FKAPeriodMs) * time.Millisecond }

// PortWWN はスイッチのポートの WWN（スイッチの WWN の上位 4 ビットを 2 にし、ポート番号を入れる）。
func (c *Config) PortWWN(index int) fc.WWN {
	return fc.WWN(0x2000000000000000 | uint64(index&0xFFF)<<48 | uint64(c.SwitchWWN)&0x0000FFFFFFFFFFFF)
}
