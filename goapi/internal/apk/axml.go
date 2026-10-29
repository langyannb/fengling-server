package apk

import "strconv"

// manifestInfo 是二进制 AndroidManifest 里我们关心的全部字段，键名对应 PHP parseManifest 的返回数组。
type manifestInfo struct {
	pkg         string
	versionName string
	versionCode int64
	label       string
	iconRes     uint32 // android:icon 指向的资源 ID；0 表示没有/不可用
}

// parseManifest 对齐 apk.php::parseManifest：只认顶层 <manifest> 与 <application> 两个元素，
// 其它元素（activity/service/…）的属性一概不看。
func parseManifest(d []byte) manifestInfo {
	var out manifestInfo
	b := bin{d}
	// 0x00080003 是「chunk type 0x0003(XML) + headerSize 8」压成的一个 u32，PHP 就是这么判的
	if len(d) < 16 || b.u32(0) != 0x00080003 {
		return out
	}

	var strs []string
	off := 8
	for off+8 <= len(d) {
		typ := b.u16(off)
		size := int(b.u32(off + 4))
		if size < 8 || off+size > len(d) {
			break // chunk 长度异常，停止（PHP 也是 break 而不是 continue）
		}
		switch {
		case typ == chunkStringPool:
			strs = readStringPool(d, off)
		case typ == chunkXMLStartElem && off+36 <= len(d):
			name := strAt(strs, int(b.u32(off+20)))
			if name != "manifest" && name != "application" {
				break // 不是这两个元素就整块跳过（PHP 同样是「不匹配就什么也不做」）
			}
			// ResXMLTree_attrExt 里 attributeStart 相对「chunk 起点 + 16」，PHP 同样硬编码 16
			attrStart := int(b.u16(off + 24))
			attrCount := int(b.u16(off + 28))
			ap := off + 16 + attrStart
			for i := 0; i < attrCount; i++ {
				a := ap + i*20 // ResXMLTree_attribute 固定 20 字节
				if a+20 > len(d) {
					break
				}
				aname := strAt(strs, int(b.u32(a+4)))
				raw := b.u32(a + 8)   // rawValue：字符串型属性在这里给字符串池下标
				data := b.u32(a + 16) // Res_value.data
				val := ""
				if raw != 0xFFFFFFFF {
					val = strAt(strs, int(raw))
				}
				if name == "manifest" {
					switch aname {
					case "package":
						out.pkg = val
					case "versionName":
						// PHP: $out['version_name'] = ($val !== '' ? $val : (string)$data);
						// 少数包把 versionName 写成纯整数（dataType=0x10），此时回退到 data 的十进制
						if val != "" {
							out.versionName = val
						} else {
							out.versionName = strconv.FormatUint(uint64(data), 10)
						}
					case "versionCode":
						out.versionCode = int64(data)
					}
				}
				if name == "application" {
					switch aname {
					case "label":
						out.label = val
					case "icon":
						// 编译后的 android:icon 是资源引用（dataType 0x01）；也接受非零的裸 data，
						// 这是 PHP 的宽松判断，保持不动。
						if b.u8(a+15) == 0x01 || data > 0 {
							out.iconRes = data
						}
					}
				}
			}
		}
		off += size
	}
	return out
}
