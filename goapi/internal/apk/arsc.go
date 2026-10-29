package apk

import (
	"sort"
	"strings"
)

// Res_value.dataType 里我们关心的几种。
const (
	typeNull      = 0x00
	typeReference = 0x01 // 另一个资源 ID，需要递归解引用
	typeAttribute = 0x02
	typeString    = 0x03 // data 是全局字符串池下标，即 APK 内文件路径
)

// ResTable_entry.flags。
const (
	entryFlagComplex = 0x0001 // bag / map（style、array），不是文件路径
	entryFlagCompact = 0x0008 // 紧凑条目，本实现不支持
)

// ResTable_type.flags。
const (
	typeFlagSparse   = 0x01 // 稀疏条目表：ResTable_sparseTypeEntry{idx:u16, offset:u16}
	typeFlagOffset16 = 0x02 // 16 位偏移表，偏移值需 *4，0xFFFF 表示无
)

// ResTable_config.density 的两个特殊值：都视为「比任何真实密度都小」。
const (
	densityAny  = 0xFFFE
	densityNone = 0xFFFF
)

// arscMaxDepth 是解引用最大递归深度（防循环引用），与 arsc.php::MAX_DEPTH 一致。
const arscMaxDepth = 8

// arscTypeChunk 是一个 ResTable_type chunk 的定位信息：
// 同一个「包+类型+条目」在不同 density 下会有多个这样的 chunk，每个 chunk 覆盖同一批条目索引。
type arscTypeChunk struct {
	flags       byte
	entryCount  int
	entriesBase int // entries 数据起点（chunk 内绝对偏移）
	offsetsBase int // 条目偏移表起点
	density     int // -1 表示 ANY/NONE，排序时当作最小
}

// arscPackage 对应一个 ResTable_package。
// PHP 侧还保存了 typeStrings / keyStrings / typeIdOffset，但它们只服务于 debugCandidates()，
// resolveFile() 完全不碰，所以这里不保留（读取它们没有任何行为影响）。
type arscPackage struct {
	id    uint32
	types map[byte][]arscTypeChunk
}

type arscValue struct {
	typ  byte
	data uint32
	key  uint32
}

// arscParser 对齐 arsc.php::ArscParser：把资源 ID 还原成 APK 内文件路径。
//
// 为什么需要它：很多游戏包/二次打包包会把 res/ 下的文件名混淆成 2V.png、vL.png 这种短名，
// 此时靠文件名猜 ic_launcher 必然失败，只有走 resources.arsc 才能拿到真图标。
type arscParser struct {
	data  []byte
	strs  []string // 全局值字符串池
	pkgs  []arscPackage
	cache map[arscChunkKey][]arscTypeChunk
	ready bool
}

type arscChunkKey struct {
	pkg byte
	typ byte
}

// newArscParser 永不失败：解析不出来的解析器 resolveFile() 恒返回 ""（PHP 也是吞掉构造期异常）。
func newArscParser(data []byte) *arscParser {
	p := &arscParser{data: data, cache: map[arscChunkKey][]arscTypeChunk{}}
	func() {
		// 对齐 PHP 构造器里的 try/catch：任何意外都只让 ready 保持 false，绝不冒泡。
		defer func() { _ = recover() }()
		p.parse()
	}()
	return p
}

func (p *arscParser) parse() {
	b := bin{p.data}
	if len(p.data) < 12 {
		return
	}
	if b.u16(0) != chunkTable {
		return // 不是 ResTable
	}
	headerSize := int(b.u16(2))
	if headerSize < 12 || headerSize > len(p.data) {
		headerSize = 12
	}
	tableSize := int(b.u32(4))
	if tableSize <= 0 || tableSize > len(p.data) {
		tableSize = len(p.data)
	}

	off := headerSize
	for off+8 <= tableSize {
		typ := b.u16(off)
		hs := int(b.u16(off + 2))
		size := int(b.u32(off + 4))
		if size < 8 || off+size > len(p.data) {
			break // 长度异常，停止
		}
		switch typ {
		case chunkStringPool:
			if len(p.strs) == 0 { // 只要第一个（全局池）
				p.strs = readStringPool(p.data, off)
			}
		case chunkTablePackage:
			p.readPackage(off, hs, size)
		}
		off += size
	}
	p.ready = true
}

// readPackage 解析一个 ResTable_package 及它的子 chunk（TypeSpec / Type）。
func (p *arscParser) readPackage(off, hs, size int) {
	b := bin{p.data}
	// 包 chunk 头固定 0x120 = 288 字节；不够就直接放弃这个包
	if hs < 284 || off+hs > len(p.data) {
		return
	}
	pkg := arscPackage{
		id:    b.u32(off + 8),
		types: map[byte][]arscTypeChunk{},
	}

	end := off + size
	if end > len(p.data) {
		end = len(p.data)
	}
	c := off + hs
	for c+8 <= end {
		ctype := b.u16(c)
		chs := int(b.u16(c + 2))
		csize := int(b.u32(c + 4))
		if csize < 8 || c+csize > len(p.data) {
			break
		}
		if ctype == chunkTableType && chs >= 20 {
			tid := b.u8(c + 8)
			flags := b.u8(c + 9)
			entryCount := int(b.u32(c + 12))
			entriesStart := int(b.u32(c + 16))
			if entryCount < 0 || entryCount > 2000000 {
				entryCount = 0
			}
			// ResTable_config 紧跟 chunk 头：size(u32) + imsi(4) + locale(4) +
			// orientation(1) + touchscreen(1) + density(u16)，所以 density 落在 c+34。
			density := 0
			if chs >= 36 {
				density = int(b.u16(c + 34))
			}
			if density == densityAny || density == densityNone {
				density = -1 // 特殊值当最小密度，真实密度优先
			}
			pkg.types[tid] = append(pkg.types[tid], arscTypeChunk{
				flags:       flags,
				entryCount:  entryCount,
				entriesBase: c + entriesStart,
				offsetsBase: c + chs,
				density:     density,
			})
		} else if ctype == chunkTableTypeSpec {
			// TypeSpec 只记录每个 entry 的 flag 位，取文件路径用不到；这里保持 PHP 的
			// 「头部不完整就跳过」判断，避免把它误当成 Type chunk。
			if chs < 16 || csize < chs {
				c += csize
				continue
			}
		}
		c += csize
	}
	p.pkgs = append(p.pkgs, pkg)
}

// typeChunks 返回某「包+类型」下的全部 Type chunk，按 density 降序（同密度保持原顺序）。
func (p *arscParser) typeChunks(pkgID, typeID uint32) []arscTypeChunk {
	key := arscChunkKey{pkg: byte(pkgID & 0xFF), typ: byte(typeID & 0xFF)}
	if v, ok := p.cache[key]; ok {
		return v
	}

	var picked *arscPackage
	for i := range p.pkgs {
		if p.pkgs[i].id == pkgID {
			picked = &p.pkgs[i]
			break
		}
	}
	// 包 ID 不匹配（0x00 / 动态包）时退化为第一个包，与 PHP 一致
	if picked == nil && len(p.pkgs) > 0 {
		picked = &p.pkgs[0]
	}

	var chunks []arscTypeChunk
	if picked != nil {
		// 拷贝一份再排序：PHP 的 usort 作用在数组副本上，不能改动解析结果本身
		chunks = append(chunks, picked.types[key.typ]...)
		sort.SliceStable(chunks, func(i, j int) bool { return chunks[i].density > chunks[j].density })
	}
	p.cache[key] = chunks
	return chunks
}

// resolveFile 把资源 ID 解析成 APK 内文件路径（如 "res/2V.png"）；找不到返回 ""。
func (p *arscParser) resolveFile(resID uint32) string {
	if !p.ready || resID == 0 {
		return ""
	}
	return p.resolve(resID, 0, map[uint32]bool{})
}

// resolve 递归解引用：TYPE_STRING 直接就是路径；TYPE_REFERENCE 指向另一个资源 ID，继续解。
func (p *arscParser) resolve(resID uint32, depth int, seen map[uint32]bool) string {
	if depth > arscMaxDepth {
		return ""
	}
	if resID == 0 || seen[resID] {
		return ""
	}
	seen[resID] = true

	pkgID := (resID >> 24) & 0xFF
	typeID := (resID >> 16) & 0xFF
	entryIdx := int(resID & 0xFFFF)

	for _, ch := range p.typeChunks(pkgID, typeID) {
		e, ok := p.entryValue(ch, entryIdx)
		if !ok {
			continue
		}
		switch e.typ {
		case typeString:
			s := ""
			if int(e.data) < len(p.strs) {
				s = p.strs[e.data]
			}
			// PHP: trim(str_replace("\0", '', $s)) —— 去掉 NUL 再按 PHP trim 的字符集去空白
			s = strings.Trim(strings.ReplaceAll(s, "\x00", ""), " \t\n\r\x00\v")
			if s != "" {
				return s // 命中路径，直接返回
			}
		case typeReference:
			// 不能按「高 8 位为 0」过滤：资源 ID 的高 8 位就是包 ID（如 0x7F0E0000），
			// 那样会把所有跨条目引用误杀。data == 0 视为无效引用，其余交给 seen/depth 防环。
			if e.data == 0 {
				continue
			}
			if r := p.resolve(e.data, depth+1, seen); r != "" {
				return r
			}
		}
		// typeAttribute / typeNull / 其它类型：继续看下一档密度
	}
	return ""
}

// entryValue 读出一个 entry 的 Res_value；ok=false 表示这个 chunk 里没有该条目。
func (p *arscParser) entryValue(ch arscTypeChunk, entryIdx int) (arscValue, bool) {
	b := bin{p.data}
	if entryIdx < 0 || entryIdx >= ch.entryCount {
		return arscValue{}, false
	}

	entryOff := 0
	found := false
	switch {
	case ch.flags&typeFlagSparse != 0:
		// 稀疏表：entryCount 个 ResTable_sparseTypeEntry{idx u16, offset u16（单位 4 字节）}
		p0 := ch.offsetsBase
		end := p0 + ch.entryCount*4
		if end > len(p.data) {
			end = len(p.data)
		}
		for q := p0; q+4 <= end; q += 4 {
			if int(b.u16(q)) == entryIdx {
				entryOff = int(b.u16(q+2)) * 4
				found = true
				break
			}
		}
		if !found {
			return arscValue{}, false
		}
	case ch.flags&typeFlagOffset16 != 0:
		q := ch.offsetsBase + entryIdx*2
		if q+2 > len(p.data) {
			return arscValue{}, false
		}
		o := b.u16(q)
		if o == 0xFFFF {
			return arscValue{}, false
		}
		entryOff = int(o) * 4
	default:
		q := ch.offsetsBase + entryIdx*4
		if q+4 > len(p.data) {
			return arscValue{}, false
		}
		o := b.u32(q)
		if o == 0xFFFFFFFF {
			return arscValue{}, false // 这个 config 下没有该 entry
		}
		entryOff = int(o)
	}

	e := ch.entriesBase + entryOff
	if e+8 > len(p.data) {
		return arscValue{}, false
	}
	entrySize := int(b.u16(e))
	entryFlags := b.u16(e + 2)
	keyIdx := b.u32(e + 4)
	if entryFlags&entryFlagCompact != 0 {
		return arscValue{}, false // 紧凑条目：不支持
	}
	if entryFlags&entryFlagComplex != 0 {
		return arscValue{}, false // bag / map：不是文件路径
	}
	if entrySize < 8 {
		entrySize = 8
	}

	v := e + entrySize
	if v+8 > len(p.data) {
		return arscValue{}, false
	}
	if b.u16(v) < 8 { // Res_value.size，正常为 8
		return arscValue{}, false
	}
	return arscValue{
		typ:  b.u8(v + 3),  // Res_value.dataType
		data: b.u32(v + 4), // Res_value.data
		key:  keyIdx,
	}, true
}
