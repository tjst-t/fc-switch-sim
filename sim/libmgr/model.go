// Package libmgr はテープライブラリの管理 API（REST・SNMP・SNMP トラップ）を模擬する。
//
// ライブラリの VM の中で動き、mhVTL の状態をロボットへの SCSI（READ ELEMENT STATUS・TEST UNIT READY）で読む。
// モニタリングツールが使う口を想定していて、ベンダーには依らない形にしている（MIB は docs/FCSIM-TAPE-LIBRARY-MIB.txt）。
package libmgr

import "time"

// State はライブラリのいまの様子。
type State struct {
	Library   Library   `json:"library"`
	Drives    []Drive   `json:"drives"`
	Slots     []Slot    `json:"slots"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ライブラリの状態。
const (
	StatusOnline  = "online"
	StatusOffline = "offline" // TEST UNIT READY が通らない（vtlcmd offline・ロボットの故障）
	StatusUnknown = "unknown" // ロボットが見つからない
)

type Library struct {
	ID          int    `json:"id"` // mhVTL のライブラリの番号
	Vendor      string `json:"vendor"`
	Product     string `json:"product"`
	Serial      string `json:"serial"`
	Status      string `json:"status"`
	Robot       string `json:"robot,omitempty"` // VM の中の /dev/sg
	DriveCount  int    `json:"driveCount"`
	SlotCount   int    `json:"slotCount"`   // 保管スロット
	IESlotCount int    `json:"ieSlotCount"` // メールスロット（Import/Export）
	AlertDrives int    `json:"alertDrives"` // TapeAlert が立っているドライブの数
}

// ドライブの状態。
const (
	DriveEmpty  = "empty"
	DriveLoaded = "loaded"
)

type Drive struct {
	Index      int    `json:"index"`   // 1 から（ライブラリの中のドライブの番号）
	ID         int    `json:"id"`      // mhVTL のドライブの番号（vtlcmd に渡す）
	Element    int    `json:"element"` // mtx の Data Transfer Element の番号（0 から）
	Vendor     string `json:"vendor"`
	Product    string `json:"product"`
	Serial     string `json:"serial"`
	State      string `json:"state"`
	Volume     string `json:"volume,omitempty"`
	SourceSlot int    `json:"sourceSlot,omitempty"`
	Alerts     []int  `json:"alerts"` // TapeAlert のフラグの番号（1〜64）。API で立てたもの
	Loads      uint64 `json:"loads"`  // テープを入れた回数（libmgr が起きてから）
}

// スロットの種類。
const (
	SlotStorage      = "storage"
	SlotImportExport = "importExport"
)

type Slot struct {
	Address int    `json:"address"` // mtx の Storage Element の番号
	Type    string `json:"type"`
	Full    bool   `json:"full"`
	Volume  string `json:"volume,omitempty"`
}

// Event はライブラリで起きたこと。SNMP トラップにもなる。
type Event struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	Drive   int       `json:"drive,omitempty"`
	Slot    int       `json:"slot,omitempty"`
	Volume  string    `json:"volume,omitempty"`
	Status  string    `json:"status,omitempty"`
	Alerts  []int     `json:"alerts,omitempty"`
}

// 出来事の種類（SNMP の通知と 1 対 1）。
const (
	EvStatusChange = "libraryStatusChange"
	EvDriveLoad    = "driveLoad"
	EvDriveUnload  = "driveUnload"
	EvDriveAlert   = "driveAlert" // フラグを立てた・消した（Alerts が空なら消した）
	EvIEImport     = "ieImport"   // メールスロットにテープが入った
	EvIERemove     = "ieRemove"   // メールスロットからテープが無くなった
)
