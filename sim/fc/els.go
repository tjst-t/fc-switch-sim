package fc

import "encoding/binary"

// ELS のコマンド。
const (
	ELSLsRjt = 0x01
	ELSLsAcc = 0x02
	ELSPlogi = 0x03
	ELSFlogi = 0x04
	ELSLogo  = 0x05
	ELSRls   = 0x0F
	ELSEcho  = 0x10
	ELSRrq   = 0x12
	ELSPrli  = 0x20
	ELSPrlo  = 0x21
	ELSFdisc = 0x51
	ELSAdisc = 0x52
	ELSRscn  = 0x61
	ELSScr   = 0x62
	ELSRnid  = 0x78
)

// LS_RJT の理由。
const (
	RjtInvalidCmd    = 0x01
	RjtLogicalError  = 0x03
	RjtUnableToPerf  = 0x09
	RjtNotSupported  = 0x0B
	RjtExpNone       = 0x00
	RjtExpInvalidNPN = 0x0D // N_Port の名前が正しくない
)

// サービスパラメータの機能ビット（共通の部分）。
const (
	SPFeatNPIVAcc = 0x2000 // FLOGI の LS_ACC：NPIV を受け付ける
	SPFeatFPort   = 0x1000 // F_Port から返した
)

// ServiceParamsLen は PLOGI・FLOGI の中身の長さ（コマンドの 4 バイトを含む）。
const ServiceParamsLen = 116

// LoginParams は PLOGI・FLOGI・FDISC の中身から読むもの。
type LoginParams struct {
	WWPN    WWN
	WWNN    WWN
	BBData  uint16 // 受け取れる最大のデータの長さ
	Feature uint16
}

func ParseLoginParams(p []byte) (LoginParams, bool) {
	if len(p) < 36 {
		return LoginParams{}, false
	}
	return LoginParams{
		Feature: binary.BigEndian.Uint16(p[8:10]),
		BBData:  binary.BigEndian.Uint16(p[10:12]) & 0x0FFF,
		WWPN:    WWN(binary.BigEndian.Uint64(p[20:28])),
		WWNN:    WWN(binary.BigEndian.Uint64(p[28:36])),
	}, true
}

// BuildLoginAcc はファブリック側のサービス（F_Port・ネームサーバなど）が返す LS_ACC のサービスパラメータ。
// fabric が true なら FLOGI・FDISC への応答（R_A_TOV を入れ、F_Port の印を立てる）。
func BuildLoginAcc(wwpn, wwnn WWN, maxPayload uint16, fabric bool) []byte {
	p := make([]byte, ServiceParamsLen)
	p[0] = ELSLsAcc
	csp := p[4:20]
	csp[0], csp[1] = 0x20, 0x20                      // FC-PH の版
	binary.BigEndian.PutUint16(csp[2:4], 0x0010)     // BB_Credit
	binary.BigEndian.PutUint16(csp[6:8], maxPayload) // 受け取れる最大の長さ
	if fabric {
		binary.BigEndian.PutUint16(csp[4:6], SPFeatFPort|SPFeatNPIVAcc)
		binary.BigEndian.PutUint32(csp[8:12], 10000) // R_A_TOV（ミリ秒）
	} else {
		binary.BigEndian.PutUint16(csp[8:10], 0x00FF) // 同時に開ける順序の数
		binary.BigEndian.PutUint16(csp[10:12], 0x001F)
	}
	binary.BigEndian.PutUint32(csp[12:16], 2000) // E_D_TOV（ミリ秒）
	binary.BigEndian.PutUint64(p[20:28], uint64(wwpn))
	binary.BigEndian.PutUint64(p[28:36], uint64(wwnn))
	// クラス 3 のサービスパラメータ（4 つのうち 3 番目）。
	c3 := p[36+32 : 36+48]
	binary.BigEndian.PutUint16(c3[0:2], 0x8000|0x0800) // 有効・順序どおりに届ける
	binary.BigEndian.PutUint16(c3[6:8], maxPayload)
	binary.BigEndian.PutUint16(c3[8:10], 0x00FF)
	c3[13] = 0x01
	return p
}

// LsRjt は LS_RJT の中身。
func LsRjt(reason, explanation byte) []byte {
	return []byte{ELSLsRjt, 0, 0, 0, 0, reason, explanation, 0}
}

// LsAcc は中身のない LS_ACC。
func LsAcc() []byte { return []byte{ELSLsAcc, 0, 0, 0} }

// RSCN のページのアドレスの形。
const (
	RSCNPort   = 0
	RSCNArea   = 1
	RSCNDomain = 2
	RSCNFabric = 3
)

// RSCNPage は RSCN の 1 ページ。
type RSCNPage struct {
	Format byte
	ID     FCID
}

// BuildRSCN は RSCN の中身を作る。
func BuildRSCN(pages []RSCNPage) []byte {
	p := make([]byte, 4+4*len(pages))
	p[0] = ELSRscn
	p[1] = 4
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	for i, pg := range pages {
		o := 4 + 4*i
		p[o] = pg.Format & 0x03
		PutFCID(p[o+1:o+4], pg.ID)
	}
	return p
}

// BAAcc は ABTS への BA_ACC の中身。
func BAAcc(oxid, rxid uint16) []byte {
	p := make([]byte, 12)
	binary.BigEndian.PutUint16(p[4:6], oxid)
	binary.BigEndian.PutUint16(p[6:8], rxid)
	binary.BigEndian.PutUint16(p[10:12], 0xFFFF)
	return p
}
