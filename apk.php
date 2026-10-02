<?php
/**
 * apk.php — 纯 PHP 解析 APK：包名 / versionName / versionCode + 挑出最高清启动图标
 * 不依赖 aapt，靠 ZipArchive + 自写的二进制 AXML 解析
 * 禁止直接访问（仅允许被 api.php include）
 */
if (isset($_SERVER['SCRIPT_FILENAME']) && basename($_SERVER['SCRIPT_FILENAME']) === 'apk.php') {
    http_response_code(404);
    exit;
}

class ApkParser
{
    /** @return array{package:string,version_name:string,version_code:int,icon_data:?string,icon_ext:string,icon_path:string} */
    public static function parse(string $file): array
    {
        $out = array(
            'package' => '', 'version_name' => '', 'version_code' => 0,
            'icon_data' => null, 'icon_ext' => 'png', 'icon_path' => '',
        );
        if (!class_exists('ZipArchive')) { $out['error'] = 'PHP 缺少 zip 扩展'; return $out; }
        $zip = new ZipArchive();
        if ($zip->open($file) !== true) { $out['error'] = 'APK 打开失败'; return $out; }

        $manifest = $zip->getFromName('AndroidManifest.xml');
        if ($manifest !== false && $manifest !== '') {
            $m = self::parseManifest($manifest);
            $out['package']      = $m['package'];
            $out['version_name'] = $m['version_name'];
            $out['version_code'] = $m['version_code'];
            $out['label']        = isset($m['label']) ? $m['label'] : '';
        } else {
            $out['error'] = 'APK 内没有 AndroidManifest.xml';
        }

        $icon = self::pickIcon($zip);
        if ($icon !== null) {
            $out['icon_data'] = $icon['data'];
            $out['icon_ext']  = $icon['ext'];
            $out['icon_path'] = $icon['path'];
        }
        $zip->close();
        return $out;
    }

    /* ---------- 二进制 AXML ---------- */

    private static function parseManifest(string $d): array
    {
        $out = array('package' => '', 'version_name' => '', 'version_code' => 0, 'label' => '');
        $len = strlen($d);
        if ($len < 16 || self::u32($d, 0) !== 0x00080003) return $out;
        $strs = array();
        $off = 8;
        while ($off + 8 <= $len) {
            $type = self::u16($d, $off);
            $size = self::u32($d, $off + 4);
            if ($size < 8 || $off + $size > $len) break;
            if ($type === 0x0001) {
                $strs = self::readStrings($d, $off);
            } elseif ($type === 0x0102 && $off + 36 <= $len) {
                $nameIdx = self::u32($d, $off + 20);
                $name = isset($strs[$nameIdx]) ? $strs[$nameIdx] : '';
                if ($name === 'manifest' || $name === 'application') {
                    $attrStart = self::u16($d, $off + 24);
                    $attrCount = self::u16($d, $off + 28);
                    $ap = $off + 16 + $attrStart;
                    for ($i = 0; $i < $attrCount; $i++) {
                        $a = $ap + $i * 20;
                        if ($a + 20 > $len) break;
                        $anIdx = self::u32($d, $a + 4);
                        $aname = isset($strs[$anIdx]) ? $strs[$anIdx] : '';
                        $raw   = self::u32($d, $a + 8);
                        $data  = self::u32($d, $a + 16);
                        $val = ($raw !== 0xFFFFFFFF && isset($strs[$raw])) ? $strs[$raw] : '';
                        if ($name === 'manifest') {
                            if ($aname === 'package')     $out['package'] = $val;
                            if ($aname === 'versionName') $out['version_name'] = ($val !== '' ? $val : (string)$data);
                            if ($aname === 'versionCode') $out['version_code'] = (int)$data;
                        }
                        if ($name === 'application' && $aname === 'label') $out['label'] = $val;
                    }
                }
            }
            $off += $size;
        }
        return $out;
    }

    private static function readStrings(string $d, int $base): array
    {
        $count    = self::u32($d, $base + 8);
        $flags    = self::u32($d, $base + 16);
        $strStart = self::u32($d, $base + 20);
        $utf8     = ($flags & 0x100) !== 0;
        $offArr   = $base + 28;
        $dataBase = $base + $strStart;
        $len      = strlen($d);
        $out      = array();
        for ($i = 0; $i < $count; $i++) {
            $p = $offArr + $i * 4;
            if ($p + 4 > $len) break;
            $o = $dataBase + self::u32($d, $p);
            if ($o < 0 || $o >= $len) { $out[$i] = ''; continue; }
            if ($utf8) {
                self::u8var($d, $o);
                $blen = self::u8var($d, $o);
                $out[$i] = substr($d, $o, $blen);
            } else {
                // UTF-16 池: 长度前缀是 u16 变体(2 或 4 字节), 每字符 2 字节
                $clen = self::u16var($d, $o);
                $raw  = substr($d, $o, $clen * 2);
                $conv = @iconv('UTF-16LE', 'UTF-8//IGNORE', $raw);
                $out[$i] = ($conv === false) ? '' : $conv;
            }
        }
        return $out;
    }

    private static function u8var(string $d, int &$o): int
    {
        $len = strlen($d);
        if ($o >= $len) return 0;
        $v = ord($d[$o++]);
        if ($v & 0x80) {
            if ($o >= $len) return $v & 0x7f;
            $v = (($v & 0x7f) << 8) | ord($d[$o++]);
            if ($v & 0x8000) {
                if ($o >= $len) return $v & 0x7fff;
                $v = (($v & 0x7fff) << 8) | ord($d[$o++]);
            }
        }
        return $v;
    }

    /** UTF-16 字符串长度: u16 变体, 高位 0x8000 表示再读一个 u16 拼成 4 字节 */
    private static function u16var(string $d, int &$o): int
    {
        $len = strlen($d);
        if ($o + 2 > $len) return 0;
        $v = self::u16($d, $o);
        $o += 2;
        if ($v & 0x8000) {
            if ($o + 2 > $len) return $v & 0x7fff;
            $v = (($v & 0x7fff) << 16) | self::u16($d, $o);
            $o += 2;
        }
        return $v;
    }

    private static function u16(string $d, int $o): int
    {
        if ($o + 2 > strlen($d)) return 0;
        return unpack('v', substr($d, $o, 2))[1];
    }

    private static function u32(string $d, int $o): int
    {
        if ($o + 4 > strlen($d)) return 0;
        return unpack('V', substr($d, $o, 4))[1];
    }

    /* ---------- 图标挑选 ---------- */

    private static function pickIcon(ZipArchive $zip): ?array
    {
        $density = array('xxxhdpi' => 60, 'xxhdpi' => 50, 'xhdpi' => 40, 'hdpi' => 30, 'mdpi' => 20, 'nodpi' => 10);
        $exact = array();
        $round = array();
        $other = array();
        $fore  = array();
        $back  = array();

        for ($i = 0; $i < $zip->numFiles; $i++) {
            $n = $zip->getNameIndex($i);
            if (!preg_match('#^res/(mipmap|drawable)[^/]*/([^/]+)\.(png|webp|jpg)$#i', $n, $m)) continue;
            $base = strtolower($m[2]);
            $dens = 0;
            foreach ($density as $k2 => $w) { if (strpos($n, $k2) !== false) { $dens = $w; break; } }
            $item = array('path' => $n, 'ext' => strtolower($m[3]), 'dens' => $dens);

            // 自适应图标的分层资源不能单独当图标用
            if (strpos($base, 'monochrome') !== false) continue;
            if (strpos($base, 'fore') !== false) { $fore[] = $item; continue; }
            if (strpos($base, 'back') !== false) { $back[] = $item; continue; }

            $isLauncher = (strpos($base, 'ic_launcher') !== false || strpos($base, 'app_icon') !== false
                || strpos($base, 'ic_icon') !== false || $base === 'icon');
            if (!$isLauncher) continue;
            if ($base === 'ic_launcher' || $base === 'app_icon' || $base === 'icon') { $exact[] = $item; continue; }
            if (strpos($base, 'round') !== false) { $round[] = $item; continue; }
            $other[] = $item;
        }

        // 1) 完整图标（ic_launcher.png 优先级最高，其次圆形，再其它命名）
        foreach (array($exact, $round, $other) as $grp) {
            $got = self::bestOfGroup($zip, $grp);
            if ($got !== null) return $got;
        }
        // 2) 只有自适应分层时：背景 + 前景合成，再按 72/108 裁切
        return self::composeAdaptive($zip, $fore, $back);
    }

    /** 同组候选里取密度最高、像素最多且能解码的那张 */
    private static function bestOfGroup(ZipArchive $zip, array $grp): ?array
    {
        if (!$grp) return null;
        usort($grp, function ($a, $b) { return $b['dens'] - $a['dens']; });
        $best = null;
        $bestPx = -1;
        foreach (array_slice($grp, 0, 6) as $c) {
            $data = $zip->getFromName($c['path']);
            if ($data === false || $data === '') continue;
            $sz = @getimagesizefromstring($data);
            if (!is_array($sz)) continue;
            $px = (int)$sz[0] * (int)$sz[1];
            if ($px > $bestPx) {
                $bestPx = $px;
                $best = array('data' => $data, 'ext' => $c['ext'], 'path' => $c['path'], 'px' => $px);
            }
        }
        if ($best === null) return null;
        return array('data' => $best['data'], 'ext' => $best['ext'], 'path' => $best['path']);
    }

    /** 合成自适应图标：背景铺满 + 前景叠加，按 Android 规范裁中央 72/108 作为最终图标 */
    private static function composeAdaptive(ZipArchive $zip, array $fore, array $back): ?array
    {
        if (!$fore || !function_exists('imagecreatefromstring')) return null;
        usort($fore, function ($a, $b) { return $b['dens'] - $a['dens']; });
        $f = $fore[0];
        $b = null;
        foreach ($back as $c) { if ($c['dens'] === $f['dens']) { $b = $c; break; } }
        if ($b === null && $back) {
            usort($back, function ($a, $b2) { return $b2['dens'] - $a['dens']; });
            $b = $back[0];
        }
        $fi = @imagecreatefromstring($zip->getFromName($f['path']));
        if (!$fi) return null;
        $w = imagesx($fi);
        $h = imagesy($fi);
        if ($w < 8 || $h < 8) { imagedestroy($fi); return null; }

        $canvas = imagecreatetruecolor($w, $h);
        imagesavealpha($canvas, true);
        imagealphablending($canvas, false);
        imagefill($canvas, 0, 0, imagecolorallocatealpha($canvas, 0, 0, 0, 127));

        if ($b !== null) {
            $bi = @imagecreatefromstring($zip->getFromName($b['path']));
            if ($bi) {
                imagealphablending($canvas, true);
                imagecopyresampled($canvas, $bi, 0, 0, 0, 0, $w, $h, imagesx($bi), imagesy($bi));
                imagedestroy($bi);
            }
        }
        imagealphablending($canvas, true);
        imagecopyresampled($canvas, $fi, 0, 0, 0, 0, $w, $h, $w, $h);
        imagedestroy($fi);

        $side = (int)round($w * 0.6667);
        $x = (int)round(($w - $side) / 2);
        $y = (int)round(($h - $side) / 2);
        $crop = imagecreatetruecolor($side, $side);
        imagesavealpha($crop, true);
        imagealphablending($crop, false);
        imagefill($crop, 0, 0, imagecolorallocatealpha($crop, 0, 0, 0, 127));
        imagecopy($crop, $canvas, 0, 0, $x, $y, $side, $side);

        ob_start();
        imagepng($crop);
        $out = ob_get_clean();
        imagedestroy($canvas);
        imagedestroy($crop);
        if (!is_string($out) || $out === '') return null;

        return array(
            'data' => $out,
            'ext'  => 'png',
            'path' => $f['path'] . ' + ' . ($b !== null ? $b['path'] : '(无背景)') . ' [合成裁切]',
        );
    }
}
