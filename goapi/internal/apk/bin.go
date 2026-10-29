package apk

import (
	"encoding/binary"
	"strings"
)

// Android 资源容器里的 chunk 类型（frameworks/base 的 ResourceTypes.h 命名）。
const (
	chunkStringPool    = 0x0001 // RES_STRING_POOL_TYPE
	chunkTable         = 0x0002 // RES_TABLE_TYPE
	chunkXML           = 0x0003 // RES_XML_TYPE
	chunkXMLStartElem  = 0x0102 // RES_XML_START_ELEMENT_TYPE
	chunkTablePackage  = 0x0200 // RES_TABLE_PACKAGE_TYPE
	chunkTableType     = 0x0201 // RES_TABLE_TYPE_TYPE
	chunkTableTypeSpec = 0x0202 // RES_TABLE_TYPE_SPEC_TYPE
)

// stringPoolMaxCount 是字符串池条数上限，与 PHP 侧 3000000 的门槛一致：
// 用来在损坏数据上避免按离谱的 count 预分配。
const stringPoolMaxCount = 3000000

// bin 是只读的小端视图。
//
// 对齐点：PHP 侧每个读取函数前面都有一道长度守卫（unpack 之前先比 strlen），越界一律返回 0，
// 所以被截断的 APK 只会读出空字段、不会抛异常。这里用同样的"越界即 0"语义，
// 让 Go 版本对损坏输入的行为与 PHP 完全相同：调用方永远拿到 Info，而不是 error。
type bin struct{ d []byte }

func (b bin) u8(o int) byte {
	if o < 0 || o >= len(b.d) {
		return 0
	}
	return b.d[o]
}

func (b bin) u16(o int) uint16 {
	if o < 0 || o+2 > len(b.d) {
		return 0
	}
	return binary.LittleEndian.Uint16(b.d[o:])
}

func (b bin) u32(o int) uint32 {
	if o < 0 || o+4 > len(b.d) {
		return 0
	}
	return binary.LittleEndian.Uint32(b.d[o:])
}

// bytes 等价 PHP 的 substr($d, $o, $n)：起点越界给空，长度不足则截到末尾。
func (b bin) bytes(o, n int) []byte {
	if o < 0 || o >= len(b.d) || n <= 0 {
		return nil
	}
	if o+n > len(b.d) {
		n = len(b.d) - o
	}
	return b.d[o : o+n]
}

// u8var 读 UTF-8 字符串池的变长长度前缀（1~3 字节，最高位表示还要续读），并推进 *o。
//
// PHP 侧这份逻辑有两份拷贝（apk.php::u8var 与 arsc.php::u8var），差别只在末尾越界时是否推进游标；
// 这里取 arsc.php 的推进版，两者对合法数据的结果完全一致。
func u8var(d []byte, o *int) int {
	if *o >= len(d) {
		*o++
		return 0
	}
	v := int(d[*o])
	*o++
	if v&0x80 != 0 {
		if *o >= len(d) {
			return v & 0x7f
		}
		v = ((v & 0x7f) << 8) | int(d[*o])
		*o++
		if v&0x8000 != 0 {
			if *o >= len(d) {
				return v & 0x7fff
			}
			v = ((v & 0x7fff) << 8) | int(d[*o])
			*o++
		}
	}
	return v
}

// u16var 读 UTF-16 字符串池的长度前缀：一个 u16，若最高位 0x8000 置位
// 则表示还要再读一个 u16 拼成 4 字节长度（只有超大池才会出现）。
func u16var(d []byte, o *int) int {
	b := bin{d}
	if *o+2 > len(d) {
		*o += 2
		return 0
	}
	v := int(b.u16(*o))
	*o += 2
	if v&0x8000 != 0 {
		if *o+2 > len(d) {
			return v & 0x7fff
		}
		v = ((v & 0x7fff) << 16) | int(b.u16(*o))
		*o += 2
	}
	return v
}

// readStringPool 解析一个 ResStringPool chunk，返回按下标排列的字符串表。
//
// apk.php::readStrings 与 arsc.php::readStringPool 是同一算法的两份拷贝，只在损坏数据的兜底上
// 略有出入（前者靠 substr 截断、后者直接给空串）。这里统一取带完整边界检查的版本：
// 截断出来的半截字符串既不会是合法包名也不会是合法资源路径，两种行为在真实数据上等价。
func readStringPool(d []byte, base int) []string {
	b := bin{d}
	if base < 0 || base+28 > len(d) {
		return nil
	}
	if b.u16(base) != chunkStringPool {
		return nil
	}
	count := int(b.u32(base + 8))
	flags := b.u32(base + 16)
	stringsStart := int(b.u32(base + 20))
	utf8Pool := flags&0x100 != 0 // ResStringPool_header::UTF8_FLAG
	if count <= 0 || count > stringPoolMaxCount {
		return nil
	}

	offsArr := base + 28
	dataBase := base + stringsStart
	out := make([]string, count)
	for i := 0; i < count; i++ {
		p := offsArr + i*4
		if p+4 > len(d) {
			continue // 越界项保持空串
		}
		o := dataBase + int(b.u32(p))
		if o < 0 || o >= len(d) {
			continue
		}
		// 越界一律给空串（与 arsc.php 的 readStringPool 一致）：截断出来的半截字符串
		// 既不会是合法包名也不会是合法路径，给空让上层走兜底分支。
		if utf8Pool {
			u8var(d, &o)         // 字符数：UTF-8 池里用不到，只用来跳过前缀
			blen := u8var(d, &o) // 字节数
			if blen <= 0 || o+blen > len(d) {
				continue
			}
			out[i] = string(d[o : o+blen])
			continue
		}
		clen := u16var(d, &o) // UTF-16 码元数
		if clen <= 0 || o+clen*2 > len(d) {
			continue
		}
		out[i] = decodeUTF16LE(d[o : o+clen*2])
	}
	return out
}

// decodeUTF16LE 等价 PHP 的 iconv('UTF-16LE', 'UTF-8//IGNORE', $raw)。
//
// 两个要点：
//   - //IGNORE 意味着非法序列被丢弃而不是替换成 U+FFFD，所以孤立代理项直接跳过；
//   - 奇数长度的尾巴（半对 UTF-16 码元）同样丢弃，与 iconv 行为一致。
func decodeUTF16LE(raw []byte) string {
	if len(raw) < 2 {
		return ""
	}
	var sb strings.Builder
	sb.Grow(len(raw))
	for i := 0; i+1 < len(raw); i += 2 {
		cp := rune(binary.LittleEndian.Uint16(raw[i:]))
		switch {
		case cp >= 0xD800 && cp <= 0xDBFF:
			// 高代理：必须紧跟一个低代理才能组成码点
			if i+3 < len(raw) {
				lo := rune(binary.LittleEndian.Uint16(raw[i+2:]))
				if lo >= 0xDC00 && lo <= 0xDFFF {
					cp = 0x10000 + ((cp - 0xD800) << 10) + (lo - 0xDC00)
					i += 2
					break
				}
			}
			continue
		case cp >= 0xDC00 && cp <= 0xDFFF:
			continue // 孤立低代理
		}
		sb.WriteRune(cp)
	}
	return sb.String()
}

// strAt 是 PHP `isset($list[$i]) ? $list[$i] : ”` 的等价物。
func strAt(list []string, i int) string {
	if i < 0 || i >= len(list) {
		return ""
	}
	return list[i]
}
