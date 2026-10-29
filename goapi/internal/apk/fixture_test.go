package apk

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"unicode/utf16"

	xdraw "golang.org/x/image/draw"
)

// TestMain 把临时目录挪出 /tmp：这台机器（Termux）上 /tmp 不可写，
// 不设 TMPDIR 的话 t.TempDir() 会直接失败。
func TestMain(m *testing.M) {
	dir := filepath.Join(os.Getenv("HOME"), "fl-tmp", "go-test-apk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic("创建测试临时目录失败: " + err.Error())
	}
	_ = os.Setenv("TMPDIR", dir)
	os.Exit(m.Run())
}

// ---------------------------------------------------------------- 字符串池

// strPool 是测试用的 ResStringPool 构造器，UTF-8 与 UTF-16 两种编码都支持。
type strPool struct {
	utf8  bool
	items []string
	index map[string]int
}

func newStrPool(utf8Pool bool) *strPool {
	return &strPool{utf8: utf8Pool, index: map[string]int{}}
}

func (p *strPool) add(v string) int {
	if i, ok := p.index[v]; ok {
		return i
	}
	i := len(p.items)
	p.items = append(p.items, v)
	p.index[v] = i
	return i
}

func (p *strPool) idx(v string) int {
	i, ok := p.index[v]
	if !ok {
		panic("字符串池里没有这个串: " + v)
	}
	return i
}

// u8len 编码 UTF-8 池的变长长度前缀（1~3 字节）。
func u8len(v int) []byte {
	switch {
	case v < 0x80:
		return []byte{byte(v)}
	case v < 0x8000:
		return []byte{byte(0x80 | (v >> 8)), byte(v)}
	default:
		return []byte{byte(0x80 | (v >> 16)), byte(v >> 8), byte(v)}
	}
}

// u16len 编码 UTF-16 池的长度前缀（码元数）。
func u16len(v int) []byte {
	out := make([]byte, 2)
	if v < 0x8000 {
		binary.LittleEndian.PutUint16(out, uint16(v))
		return out
	}
	binary.LittleEndian.PutUint16(out, uint16(0x8000|(v>>16)))
	return append(out, byte(v), byte(v>>8))
}

func (p *strPool) chunk() []byte {
	var data bytes.Buffer
	offs := make([]uint32, len(p.items))
	for i, s := range p.items {
		offs[i] = uint32(data.Len())
		if p.utf8 {
			data.Write(u8len(len([]rune(s)))) // 字符数
			data.Write(u8len(len(s)))         // 字节数
			data.WriteString(s)
			data.WriteByte(0)
			continue
		}
		units := utf16.Encode([]rune(s))
		data.Write(u16len(len(units)))
		for _, u := range units {
			var buf [2]byte
			binary.LittleEndian.PutUint16(buf[:], u)
			data.Write(buf[:])
		}
		data.Write([]byte{0, 0})
	}

	const headerSize = 28
	stringsStart := headerSize + 4*len(p.items)
	out := make([]byte, stringsStart)
	binary.LittleEndian.PutUint16(out[0:], chunkStringPool)
	binary.LittleEndian.PutUint16(out[2:], headerSize)
	binary.LittleEndian.PutUint32(out[4:], uint32(stringsStart+data.Len()))
	binary.LittleEndian.PutUint32(out[8:], uint32(len(p.items)))
	binary.LittleEndian.PutUint32(out[12:], 0) // styleCount
	flags := uint32(0)
	if p.utf8 {
		flags = 0x100
	}
	binary.LittleEndian.PutUint32(out[16:], flags)
	binary.LittleEndian.PutUint32(out[20:], uint32(stringsStart))
	binary.LittleEndian.PutUint32(out[24:], 0) // stylesStart
	for i, o := range offs {
		binary.LittleEndian.PutUint32(out[28+i*4:], o)
	}
	return append(out, data.Bytes()...)
}

// ---------------------------------------------------------------- AXML

// xmlAttr 描述一个 ResXMLTree_attribute。hasStr 为 true 时按字符串型编码
// （rawValue 与 data 都指向字符串池），否则用 dataType/data 自带的类型。
type xmlAttr struct {
	name     string
	str      string
	hasStr   bool
	dataType byte
	data     uint32
}

type xmlElement struct {
	name  string
	attrs []xmlAttr
}

func strAttr(name, val string) xmlAttr {
	return xmlAttr{name: name, str: val, hasStr: true}
}

func intAttr(name string, v uint32, dataType byte) xmlAttr {
	return xmlAttr{name: name, dataType: dataType, data: v}
}

// buildAXML 拼一段最小可用的二进制 XML：XML 头 + 字符串池 + 若干个 start element。
func buildAXML(utf8Pool bool, elems []xmlElement) []byte {
	pool := newStrPool(utf8Pool)
	for _, e := range elems {
		pool.add(e.name)
		for _, a := range e.attrs {
			pool.add(a.name)
			if a.hasStr {
				pool.add(a.str)
			}
		}
	}

	var body bytes.Buffer
	for _, e := range elems {
		body.Write(startElemChunk(pool, e))
	}
	poolChunk := pool.chunk()

	out := make([]byte, 8)
	binary.LittleEndian.PutUint16(out[0:], chunkXML)
	binary.LittleEndian.PutUint16(out[2:], 8)
	binary.LittleEndian.PutUint32(out[4:], uint32(8+len(poolChunk)+body.Len()))
	out = append(out, poolChunk...)
	out = append(out, body.Bytes()...)
	return out
}

func startElemChunk(pool *strPool, e xmlElement) []byte {
	size := 36 + 20*len(e.attrs) // 16 字节头（含 attrExt）+ 20 字节/属性
	out := make([]byte, size)
	binary.LittleEndian.PutUint16(out[0:], chunkXMLStartElem)
	binary.LittleEndian.PutUint16(out[2:], 16) // headerSize
	binary.LittleEndian.PutUint32(out[4:], uint32(size))
	binary.LittleEndian.PutUint32(out[8:], 1)           // lineNumber
	binary.LittleEndian.PutUint32(out[12:], 0xFFFFFFFF) // comment
	binary.LittleEndian.PutUint32(out[16:], 0xFFFFFFFF) // ns
	binary.LittleEndian.PutUint32(out[20:], uint32(pool.idx(e.name)))
	binary.LittleEndian.PutUint16(out[24:], 20) // attributeStart（相对 chunk+16）
	binary.LittleEndian.PutUint16(out[26:], 20) // attributeSize
	binary.LittleEndian.PutUint16(out[28:], uint16(len(e.attrs)))
	for i, a := range e.attrs {
		base := 36 + i*20
		binary.LittleEndian.PutUint32(out[base:], 0xFFFFFFFF) // 属性命名空间：这里统一留空
		binary.LittleEndian.PutUint32(out[base+4:], uint32(pool.idx(a.name)))
		raw := uint32(0xFFFFFFFF)
		data := a.data
		dtype := a.dataType
		if a.hasStr {
			idx := uint32(pool.idx(a.str))
			raw, data, dtype = idx, idx, typeString
		}
		binary.LittleEndian.PutUint32(out[base+8:], raw)
		binary.LittleEndian.PutUint16(out[base+12:], 8) // Res_value.size
		out[base+14] = 0                                // res0
		out[base+15] = dtype
		binary.LittleEndian.PutUint32(out[base+16:], data)
	}
	return out
}

// manifestFixture 描述要造出来的 AndroidManifest。
type manifestFixture struct {
	UTF8             bool
	Package          string
	VersionName      string
	VersionCode      uint32
	Label            string
	IconRes          uint32 // 0 表示不放 android:icon
	VersionNameAsInt bool   // true 时把 versionName 写成 dataType=0x10 的整数
	OmitLabel        bool
}

func buildManifestAXML(fx manifestFixture) []byte {
	manAttrs := []xmlAttr{strAttr("package", fx.Package)}
	if fx.VersionNameAsInt {
		manAttrs = append(manAttrs, intAttr("versionName", fx.VersionCode, 0x10))
	} else {
		manAttrs = append(manAttrs, strAttr("versionName", fx.VersionName))
	}
	manAttrs = append(manAttrs, intAttr("versionCode", fx.VersionCode, 0x10))

	var appAttrs []xmlAttr
	if !fx.OmitLabel {
		appAttrs = append(appAttrs, strAttr("label", fx.Label))
	}
	if fx.IconRes != 0 {
		appAttrs = append(appAttrs, intAttr("icon", fx.IconRes, typeReference))
	}
	return buildAXML(fx.UTF8, []xmlElement{
		{name: "manifest", attrs: manAttrs},
		{name: "application", attrs: appAttrs},
	})
}

// ---------------------------------------------------------------- resources.arsc

// arscFixtureEntry 描述一条「资源 ID + 密度 → 值」。
type arscFixtureEntry struct {
	resID    uint32
	density  uint16
	value    string // dataType 为 typeString（或 0）时作为字符串池里的路径
	dataType byte
	data     uint32
}

// buildTestARSC 拼一个最小但结构完整的 resources.arsc：
// ResTable 头 + 全局字符串池 + 一个 ResTable_package（typeStrings/keyStrings + TypeSpec + Type）。
func buildTestARSC(entries []arscFixtureEntry, utf8Pool bool) []byte {
	pool := newStrPool(utf8Pool)
	for _, e := range entries {
		if e.dataType == 0 || e.dataType == typeString {
			pool.add(e.value)
		}
	}

	type group struct {
		typ      byte
		density  uint16
		entryMax int
		entries  map[int]arscFixtureEntry
	}
	var groups []*group
	findGroup := func(typ byte, density uint16) *group {
		for _, g := range groups {
			if g.typ == typ && g.density == density {
				return g
			}
		}
		g := &group{typ: typ, density: density, entries: map[int]arscFixtureEntry{}}
		groups = append(groups, g)
		return g
	}
	for _, e := range entries {
		g := findGroup(byte(e.resID>>16), e.density)
		idx := int(e.resID & 0xFFFF)
		g.entries[idx] = e
		if idx > g.entryMax {
			g.entryMax = idx
		}
	}

	// 每个 type 的条目总数（TypeSpec 与所有 Type chunk 共用）
	var typeOrder []byte
	typeMax := map[byte]int{}
	for _, g := range groups {
		if _, seen := typeMax[g.typ]; !seen {
			typeOrder = append(typeOrder, g.typ)
		}
		if g.entryMax > typeMax[g.typ] {
			typeMax[g.typ] = g.entryMax
		}
	}

	var children [][]byte
	for _, tid := range typeOrder {
		children = append(children, buildTypeSpec(tid, typeMax[tid]+1))
	}
	for _, g := range groups {
		children = append(children, buildTypeChunk(g.typ, g.density, typeMax[g.typ]+1, g.entries, pool))
	}

	typeStrs := newStrPool(utf8Pool)
	typeStrs.add("mipmap")
	keyStrs := newStrPool(utf8Pool)
	keyStrs.add("icon")
	pkg := buildPackageChunk(0x7f, typeStrs.chunk(), keyStrs.chunk(), children)
	return buildTable(pool.chunk(), pkg)
}

func buildTable(globalPool, pkg []byte) []byte {
	out := make([]byte, 12)
	binary.LittleEndian.PutUint16(out[0:], chunkTable)
	binary.LittleEndian.PutUint16(out[2:], 12) // headerSize
	binary.LittleEndian.PutUint32(out[4:], uint32(12+len(globalPool)+len(pkg)))
	binary.LittleEndian.PutUint32(out[8:], 1) // packageCount
	out = append(out, globalPool...)
	out = append(out, pkg...)
	return out
}

func buildPackageChunk(pkgID uint32, typeStrs, keyStrs []byte, children [][]byte) []byte {
	const headerSize = 288
	total := headerSize + len(typeStrs) + len(keyStrs)
	for _, c := range children {
		total += len(c)
	}
	out := make([]byte, headerSize)
	binary.LittleEndian.PutUint16(out[0:], chunkTablePackage)
	binary.LittleEndian.PutUint16(out[2:], headerSize)
	binary.LittleEndian.PutUint32(out[4:], uint32(total))
	binary.LittleEndian.PutUint32(out[8:], pkgID)
	// name[128] 保持全 0（php 侧不读包名）
	binary.LittleEndian.PutUint32(out[268:], headerSize)                       // typeStrings 相对包起点
	binary.LittleEndian.PutUint32(out[272:], 0)                                // lastPublicType
	binary.LittleEndian.PutUint32(out[276:], uint32(headerSize+len(typeStrs))) // keyStrings
	binary.LittleEndian.PutUint32(out[280:], 0)                                // lastPublicKey
	binary.LittleEndian.PutUint32(out[284:], 0)                                // typeIdOffset
	out = append(out, typeStrs...)
	out = append(out, keyStrs...)
	for _, c := range children {
		out = append(out, c...)
	}
	return out
}

func buildTypeSpec(tid byte, entryCount int) []byte {
	const headerSize = 16
	size := headerSize + 4*entryCount
	out := make([]byte, size)
	binary.LittleEndian.PutUint16(out[0:], chunkTableTypeSpec)
	binary.LittleEndian.PutUint16(out[2:], headerSize)
	binary.LittleEndian.PutUint32(out[4:], uint32(size))
	out[8] = tid
	return out
}

func buildTypeChunk(tid byte, density uint16, entryCount int, entries map[int]arscFixtureEntry, pool *strPool) []byte {
	// config 只填到 density（size=16），于是 chunk 头 = 20 + 16 = 36，
	// 正好满足 PHP 侧「chs >= 36 才读 c+34 的 density」。
	const headerSize = 36
	entriesStart := headerSize + 4*entryCount

	idxs := make([]int, 0, len(entries))
	for idx := range entries {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)

	var body bytes.Buffer
	offs := make([]uint32, entryCount)
	for i := range offs {
		offs[i] = 0xFFFFFFFF // 该 config 下没有这个 entry
	}
	for _, idx := range idxs {
		e := entries[idx]
		// ResTable_type 的 offsets 值是「相对 entriesStart」的偏移（AOSP 里是
		// (uint8_t*)header + entriesStart + offset），不是相对 chunk 起点。
		offs[idx] = uint32(body.Len())
		dtype := e.dataType
		data := e.data
		if dtype == 0 {
			dtype = typeString
			data = uint32(pool.idx(e.value))
		}
		var ent [16]byte
		binary.LittleEndian.PutUint16(ent[0:], 8) // ResTable_entry.size
		binary.LittleEndian.PutUint16(ent[2:], 0) // flags：不是 bag/map
		binary.LittleEndian.PutUint32(ent[4:], 0) // key 下标（php 只在 debug 用）
		binary.LittleEndian.PutUint16(ent[8:], 8) // Res_value.size
		ent[10] = 0
		ent[11] = dtype
		binary.LittleEndian.PutUint32(ent[12:], data)
		body.Write(ent[:])
	}

	size := entriesStart + body.Len()
	out := make([]byte, entriesStart)
	binary.LittleEndian.PutUint16(out[0:], chunkTableType)
	binary.LittleEndian.PutUint16(out[2:], headerSize)
	binary.LittleEndian.PutUint32(out[4:], uint32(size))
	out[8] = tid
	out[9] = 0 // flags：非稀疏、非 offset16
	binary.LittleEndian.PutUint32(out[12:], uint32(entryCount))
	binary.LittleEndian.PutUint32(out[16:], uint32(entriesStart))
	binary.LittleEndian.PutUint32(out[20:], 16) // ResTable_config.size
	binary.LittleEndian.PutUint16(out[34:], density)
	for i, o := range offs {
		binary.LittleEndian.PutUint32(out[headerSize+i*4:], o)
	}
	return append(out, body.Bytes()...)
}

// ---------------------------------------------------------------- zip / 图片

type zipEntry struct {
	name string
	data []byte
}

func buildTestAPK(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatalf("zip.Create(%s): %v", e.name, err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatalf("zip.Write(%s): %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip.Close: %v", err)
	}
	return buf.Bytes()
}

func makePNG(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, xdraw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// writeAPK 把字节落盘到 t.TempDir()（TMPDIR 已在 TestMain 里挪出 /tmp）。
func writeAPK(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("写临时 APK 失败: %v", err)
	}
	return p
}
