// Package uc 是「风铃分享库」后端 UC 网盘（drive.uc.cn）客户端的 Go 移植。
//
// 权威参考只有一份：~/fengling-server/uc.php 的 UcDrive 类（502 行）。
// 本包逐字对齐它的语义——扫码登录、手工 Cookie 兜底、分享链接解析（含一层
// 子目录展开）、分享内文件直链下载——因为 PHP 侧仍在跑，两套实现必须能读写
// 同一个凭据文件、对同一个分享链接给出同样的结果。
//
// # 符号对照
//
//	LoadAuth / SaveAuth / ClearAuth / Status   ← loadAuth / saveAuth / clearAuth / status
//	QRCreate / QRPoll                         ← qrCreate / qrPoll
//	LoginByCookie / FetchUserInfo             ← loginByCookie / fetchUserInfo
//	ShareKey / Resolve                        ← shareKey / resolve
//	DownloadURL / RefreshPuus / DownloadToTmp ← downloadUrl / refreshPuus / downloadToTmp
//	GuessNameVersion                          ← guessNameVersion
//
// # Result 的映射
//
// PHP 的各方法返回裸数组，键不统一（'code'/'msg'/'data'/'need_pwd'，user info 那边
// 是 'ok'/'nickname'/'account'/'raw'）。Go 侧统一成 Result：
//
//	Code     ↔ $r['code']（0 = 成功）；FetchUserInfo 用 0/401 表达 $r['ok']
//	Msg      ↔ $r['msg']（失败文案，逐字）
//	Data     ↔ $r['data']；无 data 时为 nil
//	NeedPwd  ↔ $r['need_pwd']（仅 Resolve 可能为 true）
//
// PHP 里失败分支偶尔回传的 'raw' 字段只做内部调试，api.php 从不读取，Go 侧
// 在 Data 里保留（键 "raw"）但不参与语义。
//
// # 包级配置
//
//	AuthPath  凭据文件路径，默认 "uc_auth.php"（对齐 __DIR__.'/uc_auth.php'）
//	APIBase   API 根，默认 https://pc-api.uc.cn/1/clouddrive
//	CASBase   CAS 根，默认 https://api.open.uc.cn/cas/ajax
//	DriveBase 网页根，默认 https://drive.uc.cn（仅供测试替换）
//	TmpDir    下载临时目录，默认 /tmp（对齐 sys_get_temp_dir()）
//
// 生产接线时只需设置 AuthPath（PHPDocRoot 下）与 TmpDir；其余保持默认。
package uc

import (
	"encoding/json"
)

// Result 对齐 PHP 方法返回的 ['code'=>int,'msg'=>string,'data'=>...,'need_pwd'=>bool]。
type Result struct {
	// Code 为 0 表示成功；非 0 由 api.php 映射成 HTTP 状态码。
	Code int
	// Msg 是失败文案，与 PHP 逐字一致。
	Msg string
	// Data 是成功时的数据体（对应 $r['data']）；没有 data 时为 nil。
	Data map[string]any
	// NeedPwd 仅 Resolve 使用，对齐 $uc['need_pwd']。
	NeedPwd bool
}

// decodeArray 复刻 `$j = json_decode($body, true); if (!is_array($j)) ...`：
// 第二返回值对应 is_array()——顶层是对象或数组都为 true（数组当空 map 处理，
// 因为 PHP 里对数组取 $j['x'] 同样只是 isset 为 false）。
func decodeArray(body string) (map[string]any, bool) {
	if body == "" {
		return nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return nil, false
	}
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case []any:
		return map[string]any{}, true
	default:
		return nil, false
	}
}

// asMap 取子对象，类型不符时给空 map（对齐 PHP 的 isset(...)?...:array()）。
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// asList 取子数组，类型不符时给 nil（对齐 PHP 的 isset(...['list'])?...:array()）。
func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

// isset 复刻 isset($m[$key])：键不存在或值为 null 都算未设置。
func isset(m map[string]any, key string) bool {
	v, ok := m[key]
	return ok && v != nil
}
