package upload

import (
	"crypto/rand"
	"encoding/hex"
	mrand "math/rand/v2"
	"strings"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
)

// S3Key 对齐 api.php:147 s3_key：`<dir>/<YmdHis>_<8位小写hex>.<ext>`。
//
// 时间用 config.LocalZone()（Asia/Shanghai），与 PHP 的
// `date_default_timezone_set('Asia/Shanghai')` + `date('YmdHis')` 对齐；
// 刻意不用进程本地时区，否则服务器 TZ 与 PHP 不一致时键里的时间戳会整体错位。
func S3Key(dir, ext string) string {
	return dir + "/" + time.Now().In(config.LocalZone()).Format("20060102150405") +
		"_" + randomHex8() + "." + ext
}

// APKKey 对齐 api.php:2766 `'apk/fengling_' . date('Ymd_His') . '.apk'`
// （注意与 S3Key 的命名规则不同：没有随机段、日期与时间之间带下划线）。
func APKKey(now time.Time) string {
	return "apk/fengling_" + now.In(config.LocalZone()).Format("20060102_150405") + ".apk"
}

// ExtFromFilename 复刻 `strtolower(pathinfo($name, PATHINFO_EXTENSION))`。
func ExtFromFilename(name string) string {
	base := name
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndexByte(base, '\\'); i >= 0 {
		base = base[i+1:]
	}
	dot := strings.LastIndexByte(base, '.')
	if dot < 0 {
		return ""
	}
	return strings.ToLower(base[dot+1:])
}

// randomHex8 对齐 bin2hex(random_bytes(4))（8 位小写 hex）。
// crypto/rand 读失败时退回 math/rand（键里还有秒级时间戳，且桶是公共读，
// 不存在「靠键名保密」的安全属性），绝不因为取随机数失败把上传打死。
func randomHex8() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		v := mrand.Uint32()
		b[0] = byte(v >> 24)
		b[1] = byte(v >> 16)
		b[2] = byte(v >> 8)
		b[3] = byte(v)
	}
	return hex.EncodeToString(b[:])
}
