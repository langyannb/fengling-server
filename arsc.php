<?php
/**
 * arsc.php — 纯 PHP 解析 Android resources.arsc，把「资源 ID」还原为「APK 内文件路径」
 *
 * 为什么需要它：很多 APK（游戏包、二次打包 / 加固包）会把 res/ 下的文件名混淆成
 * "-9.xml"、"2V.png"、"vL.png" 这类两字符短名，此时按文件名猜图标（ic_launcher/icon…）
 * 必然失败。正确做法是：读 AndroidManifest.xml 里 <application android:icon="…">
 * 指向的「资源 ID」（如 0x7f0e0012），再来这张表里查出真正的条目值：
 *   - dataType = TYPE_STRING(0x03) → data 是全局字符串池下标 → 字符串即 res/xxx.png
 *   - dataType = TYPE_REFERENCE(0x01) → data 是另一个资源 ID，递归解引用
 *
 * 只依赖 PHP 自身，不需要任何扩展（ZipArchive 由调用方负责，本类只吃 arsc 二进制字符串）。
 * 解析失败一律静默返回 ''，绝不冒泡警告。
 *
 * 禁止直接访问（仅允许被 apk.php / api.php include）。
 */
if (isset($_SERVER['SCRIPT_FILENAME']) && basename($_SERVER['SCRIPT_FILENAME']) === 'arsc.php') {
    http_response_code(404);
    exit;
}

class ArscParser
{
    /* ---- ResChunk_header.type ---- */
    const CHUNK_STRING_POOL     = 0x0001; // RES_STRING_POOL_TYPE
    const CHUNK_TABLE           = 0x0002; // RES_TABLE_TYPE
    const CHUNK_TABLE_PACKAGE   = 0x0200; // RES_TABLE_PACKAGE_TYPE
    const CHUNK_TABLE_TYPE      = 0x0201; // RES_TABLE_TYPE_TYPE
    const CHUNK_TABLE_TYPE_SPEC = 0x0202; // RES_TABLE_TYPE_SPEC_TYPE

    /* ---- Res_value.dataType ---- */
    const TYPE_NULL          = 0x00;
    const TYPE_REFERENCE     = 0x01;
    const TYPE_ATTRIBUTE     = 0x02;
    const TYPE_STRING        = 0x03;
    const TYPE_DYNAMIC_REF   = 0x07;

    /* ---- ResTable_entry.flags ---- */
    const ENTRY_FLAG_COMPLEX = 0x0001; // bag / map（样式、数组），不是文件路径
    const ENTRY_FLAG_COMPACT = 0x0008; // 紧凑条目（压缩表示），本实现不解析

    /* ---- ResTable_type.flags ---- */
    const TYPE_FLAG_SPARSE   = 0x01;   // 稀疏条目表：ResTable_sparseTypeEntry[]
    const TYPE_FLAG_OFFSET16 = 0x02;   // 16 位偏移表，偏移值需 *4，0xFFFF 表示无

    /* ---- ResTable_config 里的 density ---- */
    const DENSITY_ANY  = 0xFFFE; // 视为最小（任意密度都不如真实密度）
    const DENSITY_NONE = 0xFFFF; // 视为最小

    /** 解引用最大递归深度（防循环引用） */
    const MAX_DEPTH = 8;

    private $data = '';          // arsc 二进制
    private $len = 0;            // 长度缓存
    private $strings = array();  // 全局字符串池
    private $packages = array(); // 每个 package：['id'=>int,'types'=>[typeId=>chunk列表],'typeStrings'=>[],'keyStrings'=>[]]
    private $chunkCache = array();
    private $ready = false;

    /**
     * @param string $arscData resources.arsc 的原始二进制内容
     */
    public function __construct(string $arscData)
    {
        $this->data = (string)$arscData;
        $this->len  = strlen($this->data);
        // 任何异常都吞掉：构造失败的解析器 resolveFile() 恒返回 ''
        try {
            $this->parse();
        } catch (Throwable $e) {
            $this->ready = false;
        }
    }

    /**
     * 把资源 ID 解析为 APK 内文件路径（如 'res/2V.png'）；找不到返回 ''。
     * 同一资源 ID 有多个密度变体时（多个 Type chunk 对应不同 ResTable_config），取 density 最大的。
     */
    public function resolveFile(int $resId): string
    {
        if (!$this->ready || $resId === 0) return '';
        $r = $this->resolveInternal($resId, 0, array());
        return is_string($r) ? $r : '';
    }

    /**
     * 调试用：列出某资源 ID 的全部候选（已按 density 降序），附带条目 key 名与解析结果。
     * 生产代码不需要它，仅用于验证「选最大密度」这一行为是否正确。
     */
    public function debugCandidates(int $resId): array
    {
        if (!$this->ready) return array();
        $pkgId    = ($resId >> 24) & 0xFF;
        $typeId   = ($resId >> 16) & 0xFF;
        $entryIdx = $resId & 0xFFFF;
        $out = array();
        foreach ($this->typeChunks($pkgId, $typeId) as $ch) {
            $e = $this->entryValue($ch, $entryIdx);
            $keyName = '';
            if ($e !== null) {
                $ks = isset($this->packages[$ch['pkgIndex']]['keyStrings']) ? $this->packages[$ch['pkgIndex']]['keyStrings'] : array();
                $keyName = isset($ks[$e['key']]) ? $ks[$e['key']] : '';
            }
            $str = null;
            $refId = null;
            if ($e !== null) {
                if ($e['type'] === self::TYPE_STRING && isset($this->strings[$e['data']])) $str = $this->strings[$e['data']];
                if ($e['type'] === self::TYPE_REFERENCE) $refId = $e['data'];
            }
            $out[] = array(
                'density' => $ch['density'],
                'flags'   => $ch['flags'],
                'found'   => ($e !== null),
                'key'     => $keyName,
                'type'    => ($e !== null ? $e['type'] : null),
                'data'    => ($e !== null ? $e['data'] : null),
                'string'  => $str,
                'refId'   => $refId,
            );
        }
        return $out;
    }

    /** 全局字符串池条数（调试） */
    public function stringCount(): int
    {
        return count($this->strings);
    }

    /* ================= 内部实现 ================= */

    private function parse()
    {
        if ($this->len < 12) return;
        if ($this->u16(0) !== self::CHUNK_TABLE) return; // 不是 ResTable
        $headerSize = $this->u16(2);
        if ($headerSize < 12 || $headerSize > $this->len) $headerSize = 12;
        $tableSize = $this->u32(4);
        if ($tableSize <= 0 || $tableSize > $this->len) $tableSize = $this->len;

        $off = $headerSize;
        while ($off + 8 <= $tableSize) {
            $type = $this->u16($off);
            $hs   = $this->u16($off + 2);
            $size = $this->u32($off + 4);
            if ($size < 8 || $off + $size > $this->len) break; // 长度异常，停止
            if ($type === self::CHUNK_STRING_POOL) {
                if (!$this->strings) $this->strings = $this->readStringPool($off); // 全局字符串池
            } elseif ($type === self::CHUNK_TABLE_PACKAGE) {
                $this->readPackage($off, $hs, $size);
            }
            $off += $size;
        }
        $this->ready = true;
    }

    /** 解析一个 ResTable_package（0x0200）及其子 chunk（typeStrings / keyStrings / TypeSpec / Type） */
    private function readPackage(int $off, int $hs, int $size)
    {
        if ($hs < 284 || $off + $hs > $this->len) return; // 包 chunk 头固定 0x120 = 288
        $pkgId        = $this->u32($off + 8);
        $typeIdOffset = ($hs >= 288) ? $this->u32($off + 284) : 0; // 用于 typeId → typeStrings 下标换算
        $typeStringsOff = $this->u32($off + 268);
        $keyStringsOff  = $this->u32($off + 276);

        $typeStrings = array();
        if ($typeStringsOff > 0 && $off + $typeStringsOff + 28 <= $this->len) {
            $typeStrings = $this->readStringPool($off + $typeStringsOff);
        }
        $keyStrings = array();
        if ($keyStringsOff > 0 && $off + $keyStringsOff + 28 <= $this->len) {
            $keyStrings = $this->readStringPool($off + $keyStringsOff);
        }

        $pkgIndex = count($this->packages);
        $types = array();
        $end = $off + $size;
        if ($end > $this->len) $end = $this->len;

        $c = $off + $hs;
        while ($c + 8 <= $end) {
            $ctype = $this->u16($c);
            $chs   = $this->u16($c + 2);
            $csize = $this->u32($c + 4);
            if ($csize < 8 || $c + $csize > $this->len) break;
            if ($ctype === self::CHUNK_TABLE_TYPE && $chs >= 20) {
                $tid          = $this->u8($c + 8);
                $flags        = $this->u8($c + 9);
                $entryCount   = $this->u32($c + 12);
                $entriesStart = $this->u32($c + 16);
                if ($entryCount < 0 || $entryCount > 2000000) $entryCount = 0;
                // ResTable_config 紧跟 chunk 头：size(u32) + imsi(4) + locale(4) + orientation(1) + touchscreen(1) + density(u16)
                // → density 位于配置块内偏移 14，即 chunk 内偏移 20 + 14 = 34
                $density = 0;
                if ($chs >= 36) $density = $this->u16($c + 34);
                if ($density === self::DENSITY_ANY || $density === self::DENSITY_NONE) $density = -1; // 特殊值当最小值处理
                $types[$tid][] = array(
                    'flags'       => $flags,
                    'entryCount'  => $entryCount,
                    'entriesBase' => $c + $entriesStart,
                    'offsetsBase' => $c + $chs,
                    'density'     => $density,
                    'pkgIndex'    => $pkgIndex,
                    'chunk'       => $c,
                );
            } elseif ($ctype === self::CHUNK_TABLE_TYPE_SPEC) {
                // TypeSpec 只描述每个 entry 的 flag 位，用于本功能（取文件路径）无需读取，
                // 这里只做一次边界确认，保证不会被误判为 Type chunk。
                if ($chs < 16 || $csize < $chs) { $c += $csize; continue; }
            }
            $c += $csize;
        }

        $this->packages[] = array(
            'id'          => $pkgId,
            'typeIdOffset'=> $typeIdOffset,
            'types'       => $types,
            'typeStrings' => $typeStrings,
            'keyStrings'  => $keyStrings,
        );
    }

    /** 取出某资源 ID 对应的全部 Type chunk（已按 density 降序，同密度保持原顺序，PHP8 sort 稳定） */
    private function typeChunks(int $pkgId, int $typeId): array
    {
        $ck = $pkgId . ':' . $typeId;
        if (isset($this->chunkCache[$ck])) return $this->chunkCache[$ck];

        $picked = null;
        foreach ($this->packages as $p) {
            if ($p['id'] === $pkgId) { $picked = $p; break; }
        }
        if ($picked === null && $this->packages) $picked = $this->packages[0]; // 包 ID 不匹配（如 0x00/动态包）时退化为第一个包

        $chunks = array();
        if ($picked !== null && isset($picked['types'][$typeId])) {
            $chunks = $picked['types'][$typeId];
            usort($chunks, function ($a, $b) { return $b['density'] <=> $a['density']; });
        }
        $this->chunkCache[$ck] = $chunks;
        return $chunks;
    }

    /** 递归解引用；$seen 防循环，$depth 限深 */
    private function resolveInternal(int $resId, int $depth, array $seen)
    {
        if ($depth > self::MAX_DEPTH) return '';
        $resId = $resId & 0xFFFFFFFF;
        if ($resId === 0 || isset($seen[$resId])) return '';
        $seen[$resId] = true;

        $pkgId    = ($resId >> 24) & 0xFF;
        $typeId   = ($resId >> 16) & 0xFF;
        $entryIdx = $resId & 0xFFFF;

        foreach ($this->typeChunks($pkgId, $typeId) as $ch) {
            $e = $this->entryValue($ch, $entryIdx);
            if ($e === null) continue;
            if ($e['type'] === self::TYPE_STRING) {
                $s = isset($this->strings[$e['data']]) ? $this->strings[$e['data']] : '';
                $s = trim(str_replace("\0", '', (string)$s));
                if ($s !== '') return $s; // 命中路径，直接返回
            } elseif ($e['type'] === self::TYPE_REFERENCE) {
                // 注意：不能按「高 8 位为 0」过滤——资源 ID 的高 8 位就是包 ID（如 0x7F0E0000），
                // 那样会把所有跨条目引用误杀。data == 0 视为无效引用，其余交给 $seen/$depth 防环。
                if ($e['data'] === 0) continue;
                $r = $this->resolveInternal($e['data'], $depth + 1, $seen);
                if (is_string($r) && $r !== '') return $r;
            }
            // TYPE_ATTRIBUTE / TYPE_NULL / 其它类型：继续找下一档密度
        }
        return '';
    }

    /**
     * 读出一个 entry 的 Res_value。
     * @return array{type:int,data:int,key:int}|null null 表示该 chunk 里没有这个 entry
     */
    private function entryValue(array $ch, int $entryIdx)
    {
        if ($entryIdx < 0 || $entryIdx >= $ch['entryCount']) return null;
        $flags = $ch['flags'];
        $entryOff = null;

        if ($flags & self::TYPE_FLAG_SPARSE) {
            // 稀疏表：entryCount 个 ResTable_sparseTypeEntry{ idx:u16, offset:u16(单位4字节) }
            $p = $ch['offsetsBase'];
            $end = $p + $ch['entryCount'] * 4;
            if ($end > $this->len) $end = $this->len;
            for (; $p + 4 <= $end; $p += 4) {
                if ($this->u16($p) === $entryIdx) { $entryOff = $this->u16($p + 2) * 4; break; }
            }
            if ($entryOff === null) return null;
        } elseif ($flags & self::TYPE_FLAG_OFFSET16) {
            $p = $ch['offsetsBase'] + $entryIdx * 2;
            if ($p + 2 > $this->len) return null;
            $o = $this->u16($p);
            if ($o === 0xFFFF) return null;
            $entryOff = $o * 4;
        } else {
            $p = $ch['offsetsBase'] + $entryIdx * 4;
            if ($p + 4 > $this->len) return null;
            $o = $this->u32($p);
            if ($o === 0xFFFFFFFF) return null; // 该 config 下没有这个 entry
            $entryOff = $o;
        }

        $e = $ch['entriesBase'] + $entryOff;
        if ($e + 8 > $this->len) return null;
        $entrySize  = $this->u16($e);
        $entryFlags = $this->u16($e + 2);
        $keyIdx     = $this->u32($e + 4);
        if ($entryFlags & self::ENTRY_FLAG_COMPACT) return null; // 紧凑条目：本实现不支持，跳过
        if ($entryFlags & self::ENTRY_FLAG_COMPLEX) return null; // bag / map：不是文件路径
        if ($entrySize < 8) $entrySize = 8;

        $v = $e + $entrySize;
        if ($v + 8 > $this->len) return null;
        $vSize = $this->u16($v);          // Res_value.size，正常为 8
        if ($vSize < 8) return null;
        return array(
            'type' => $this->u8($v + 3),  // Res_value.dataType
            'data' => $this->u32($v + 4), // Res_value.data
            'key'  => $keyIdx,
        );
    }

    /* ---------- 字符串池（全局 / typeStrings / keyStrings 通用） ---------- */

    /** 解析 ResStringPool（支持 UTF-8 与 UTF-16LE 两种编码） */
    private function readStringPool(int $base): array
    {
        $out = array();
        if ($base < 0 || $base + 28 > $this->len) return $out;
        if ($this->u16($base) !== self::CHUNK_STRING_POOL) return $out;
        $count        = $this->u32($base + 8);
        $flags        = $this->u32($base + 16);
        $stringsStart = $this->u32($base + 20);
        $utf8 = (($flags & 0x100) !== 0); // UTF8_FLAG
        if ($count <= 0 || $count > 3000000) return $out;

        $offsArr  = $base + 28;
        $dataBase = $base + $stringsStart;
        for ($i = 0; $i < $count; $i++) {
            $p = $offsArr + $i * 4;
            if ($p + 4 > $this->len) { $out[$i] = ''; continue; }
            $o = $dataBase + $this->u32($p);
            if ($o < 0 || $o >= $this->len) { $out[$i] = ''; continue; }
            if ($utf8) {
                $this->u8var($o);                       // 字符数（UTF-8 下忽略）
                $blen = $this->u8var($o);               // 字节数
                if ($blen <= 0 || $o + $blen > $this->len) { $out[$i] = ''; continue; }
                $out[$i] = substr($this->data, $o, $blen);
            } else {
                $clen = $this->u16var($o);              // UTF-16 码元数
                if ($clen <= 0 || $o + $clen * 2 > $this->len) { $out[$i] = ''; continue; }
                $raw  = substr($this->data, $o, $clen * 2);
                $conv = @iconv('UTF-16LE', 'UTF-8//IGNORE', $raw);
                $out[$i] = ($conv === false) ? $this->utf16leToUtf8($raw) : $conv;
            }
        }
        return $out;
    }

    /** 无 iconv 时的兜底：UTF-16LE → UTF-8 */
    private function utf16leToUtf8(string $raw): string
    {
        $s = '';
        $n = strlen($raw);
        for ($i = 0; $i + 1 < $n; $i += 2) {
            $cp = ord($raw[$i]) | (ord($raw[$i + 1]) << 8);
            if ($cp >= 0xD800 && $cp <= 0xDBFF && $i + 3 < $n) {
                $lo = ord($raw[$i + 2]) | (ord($raw[$i + 3]) << 8);
                if ($lo >= 0xDC00 && $lo <= 0xDFFF) {
                    $cp = 0x10000 + (($cp - 0xD800) << 10) + ($lo - 0xDC00);
                    $i += 2;
                }
            }
            $s .= $this->cpToUtf8($cp);
        }
        return $s;
    }

    private function cpToUtf8(int $cp): string
    {
        if ($cp < 0x80) return chr($cp);
        if ($cp < 0x800) return chr(0xC0 | ($cp >> 6)) . chr(0x80 | ($cp & 0x3F));
        if ($cp < 0x10000) return chr(0xE0 | ($cp >> 12)) . chr(0x80 | (($cp >> 6) & 0x3F)) . chr(0x80 | ($cp & 0x3F));
        return chr(0xF0 | ($cp >> 18)) . chr(0x80 | (($cp >> 12) & 0x3F)) . chr(0x80 | (($cp >> 6) & 0x3F)) . chr(0x80 | ($cp & 0x3F));
    }

    /* ---------- 变长整数（UTF-8 池用 u8 变体，UTF-16 池用 u16 变体） ---------- */

    private function u8var(int &$o): int
    {
        if ($o >= $this->len) { $o++; return 0; }
        $v = ord($this->data[$o++]);
        if ($v & 0x80) {
            if ($o >= $this->len) return $v & 0x7F;
            $v = (($v & 0x7F) << 8) | ord($this->data[$o++]);
            if ($v & 0x8000) {
                if ($o >= $this->len) return $v & 0x7FFF;
                $v = (($v & 0x7FFF) << 8) | ord($this->data[$o++]);
            }
        }
        return $v;
    }

    private function u16var(int &$o): int
    {
        if ($o + 2 > $this->len) { $o += 2; return 0; }
        $v = $this->u16($o);
        $o += 2;
        if ($v & 0x8000) {
            if ($o + 2 > $this->len) return $v & 0x7FFF;
            $v = (($v & 0x7FFF) << 16) | $this->u16($o);
            $o += 2;
        }
        return $v;
    }

    /* ---------- 小端读取（越界一律返回 0，绝不抛 Notice） ---------- */

    private function u8(int $o): int
    {
        return ($o >= 0 && $o < $this->len) ? ord($this->data[$o]) : 0;
    }

    private function u16(int $o): int
    {
        return ($o >= 0 && $o + 2 <= $this->len) ? unpack('v', substr($this->data, $o, 2))[1] : 0;
    }

    private function u32(int $o): int
    {
        return ($o >= 0 && $o + 4 <= $this->len) ? unpack('V', substr($this->data, $o, 4))[1] : 0;
    }
}
