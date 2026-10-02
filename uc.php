<?php
// 防止该文件被直接访问（仅允许被 api.php include）
if (isset($_SERVER['SCRIPT_FILENAME']) && basename($_SERVER['SCRIPT_FILENAME']) === 'uc.php') {
    http_response_code(404);
    exit;
}
/**
 * uc.php — UC 网盘（drive.uc.cn）扫码登录 + 分享链接解析
 * 依赖：curl 扩展（宝塔 PHP 默认已开）
 * 凭据存储：同目录 uc_auth.php（PHP 文件，直接访问不会泄露内容，且已加入 .gitignore）
 */

class UcDrive
{
    const UA        = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36';
    const API       = 'https://pc-api.uc.cn/1/clouddrive';
    const CAS       = 'https://api.open.uc.cn/cas/ajax';
    const CLIENT_ID = '381';

    private static function authFile(): string
    {
        return __DIR__ . '/uc_auth.php';
    }

    /* ==================== 凭据存取 ==================== */

    public static function loadAuth(): array
    {
        $f = self::authFile();
        if (!is_file($f)) return array();
        $d = @include $f;
        return is_array($d) ? $d : array();
    }

    public static function saveAuth(array $d): bool
    {
        $php = "<?php\n// UC 网盘登录凭据（扫码登录自动生成，请勿手动编辑）\nreturn " . var_export($d, true) . ";\n";
        return @file_put_contents(self::authFile(), $php, LOCK_EX) !== false;
    }

    public static function clearAuth()
    {
        @unlink(self::authFile());
    }

    public static function status(): array
    {
        $a = self::loadAuth();
        if (empty($a['cookie'])) return array('logged_in' => false);
        return array(
            'logged_in'  => true,
            'login_time' => isset($a['ts']) ? $a['ts'] : 0,
            'nickname'   => isset($a['nickname']) ? $a['nickname'] : '',
            'account'    => isset($a['account']) ? $a['account'] : '',
            'cookie_len' => strlen($a['cookie']),
        );
    }

    /* ==================== HTTP 基础 ==================== */

    private static function req(string $url, array $opt = array()): array
    {
        $ch = curl_init($url);
        $headers = isset($opt['headers']) ? $opt['headers'] : array();
        $timeout = isset($opt['timeout']) ? $opt['timeout'] : 30;
        curl_setopt_array($ch, array(
            CURLOPT_RETURNTRANSFER => true,
            CURLOPT_HEADER         => true,
            CURLOPT_FOLLOWLOCATION => false,
            CURLOPT_TIMEOUT        => $timeout,
            CURLOPT_CONNECTTIMEOUT => 10,
            CURLOPT_SSL_VERIFYPEER => false,
            CURLOPT_SSL_VERIFYHOST => 0,
            CURLOPT_CUSTOMREQUEST  => isset($opt['method']) ? $opt['method'] : 'GET',
            CURLOPT_HTTPHEADER     => $headers,
            CURLOPT_ENCODING       => '',
        ));
        if (isset($opt['body'])) curl_setopt($ch, CURLOPT_POSTFIELDS, $opt['body']);
        if (!empty($opt['cookie'])) curl_setopt($ch, CURLOPT_COOKIE, $opt['cookie']);
        $raw  = curl_exec($ch);
        $err  = curl_error($ch);
        $code = (int)curl_getinfo($ch, CURLINFO_HTTP_CODE);
        $hsiz = (int)curl_getinfo($ch, CURLINFO_HEADER_SIZE);
        curl_close($ch);

        if ($raw === false) {
            return array('ok' => false, 'status' => 0, 'body' => '', 'setcookie' => array(), 'location' => '', 'error' => $err);
        }
        $head = substr($raw, 0, $hsiz);
        $body = substr($raw, $hsiz);
        $sc   = array();
        if (preg_match_all('/^set-cookie:\s*(.+)$/im', $head, $m)) {
            foreach ($m[1] as $c) $sc[] = trim($c);
        }
        $loc = '';
        if (preg_match('/^location:\s*(.+)$/im', $head, $m)) $loc = trim($m[1]);
        return array('ok' => true, 'status' => $code, 'body' => $body, 'setcookie' => $sc, 'location' => $loc, 'error' => '');
    }

    private static function absorb(array &$jar, array $setcookies)
    {
        foreach ($setcookies as $c) {
            $main = trim(explode(';', $c)[0]);
            $p = strpos($main, '=');
            if ($p > 0) {
                $k = trim(substr($main, 0, $p));
                $v = trim(substr($main, $p + 1));
                if ($k !== '') $jar[$k] = $v;
            }
        }
    }

    private static function jarStr(array $jar): string
    {
        $out = array();
        foreach ($jar as $k => $v) $out[] = $k . '=' . $v;
        return implode('; ', $out);
    }

    private static function ctoken(string $cookie): string
    {
        if (preg_match('/(?:^|;\s*)ctoken=([^;]+)/', $cookie, $m)) return $m[1];
        return '';
    }

    private static function casHeaders(): array
    {
        return array(
            'accept: application/json, text/plain, */*',
            'content-type: application/x-www-form-urlencoded',
            'origin: https://drive.uc.cn',
            'referer: https://drive.uc.cn/',
            'user-agent: ' . self::UA,
        );
    }

    private static function apiHeaders(string $cookie = ''): array
    {
        $h = array(
            'accept: application/json, text/plain, */*',
            'content-type: application/json;charset=UTF-8',
            'origin: https://drive.uc.cn',
            'referer: https://drive.uc.cn/',
            'user-agent: ' . self::UA,
        );
        if ($cookie !== '') {
            $ct = self::ctoken($cookie);
            if ($ct !== '') $h[] = 'x-ctoken: ' . $ct;
        }
        return $h;
    }

    private static function ts(): string
    {
        return (string)(int)(microtime(true) * 1000);
    }

    /* ==================== 扫码登录 ==================== */

    /** 第一步：取二维码 token */
    public static function qrCreate(): array
    {
        $ts = self::ts();
        $r = self::req(self::CAS . '/getTokenForQrcodeLogin?__dt=792565&__t=' . $ts, array(
            'method'  => 'POST',
            'headers' => self::casHeaders(),
            'body'    => http_build_query(array('client_id' => self::CLIENT_ID, 'v' => '1.2', 'request_id' => $ts)),
        ));
        $j = json_decode($r['body'], true);
        if (!is_array($j)) return array('code' => 502, 'msg' => 'UC 登录接口无响应 (HTTP ' . $r['status'] . ')');
        if ((int)(isset($j['status']) ? $j['status'] : 0) !== 2000000) {
            return array('code' => 502, 'msg' => '获取二维码失败：' . (isset($j['message']) ? $j['message'] : '未知'));
        }
        $token = isset($j['data']['members']['token']) ? $j['data']['members']['token'] : '';
        if ($token === '') return array('code' => 502, 'msg' => '二维码 token 为空');
        $qr = 'https://su.uc.cn/1_n0ZCv?uc_param_str=dsdnfrpfbivesscpgimibtbmnijblauputogpintnwktprchmt'
            . '&token=' . $token . '&client_id=' . self::CLIENT_ID
            . '&uc_biz_str=' . rawurlencode('S:custom|C:titlebar_fix');
        return array('code' => 0, 'data' => array('token' => $token, 'qr_url' => $qr, 'expires_in' => 600));
    }

    /** 第二步：轮询扫码状态；成功则换 Cookie 并保存 */
    public static function qrPoll(string $token): array
    {
        if ($token === '') return array('code' => 400, 'msg' => '缺少 token');
        $ts = self::ts();
        $r = self::req(self::CAS . '/getServiceTicketByQrcodeToken', array(
            'method'  => 'POST',
            'headers' => self::casHeaders(),
            'body'    => http_build_query(array('client_id' => self::CLIENT_ID, 'v' => '1.2', 'request_id' => $ts, 'token' => $token)),
        ));
        $j = json_decode($r['body'], true);
        if (!is_array($j)) return array('code' => 502, 'msg' => '轮询接口无响应 (HTTP ' . $r['status'] . ')');
        $st = (int)(isset($j['status']) ? $j['status'] : 0);
        if ($st !== 2000000) {
            $msg = isset($j['message']) ? $j['message'] : '';
            $state = 'waiting';
            if (stripos($msg, 'scan') !== false || $st === 4000101) $state = 'scanned';
            return array('code' => 0, 'data' => array('state' => $state, 'uc_status' => $st, 'msg' => $msg));
        }
        $members = isset($j['data']['members']) ? $j['data']['members'] : array();
        $ticket = '';
        if (!empty($members['ticket'])) $ticket = $members['ticket'];
        elseif (!empty($members['service_ticket'])) $ticket = $members['service_ticket'];
        if ($ticket === '') return array('code' => 0, 'data' => array('state' => 'waiting', 'msg' => '尚未确认'));
        $cookie = self::ticketToCookie($ticket);
        if ($cookie === '') return array('code' => 502, 'msg' => '换取 Cookie 失败，请重试');
        $info = self::fetchUserInfo($cookie);
        self::saveAuth(array(
            'cookie'   => $cookie,
            'ts'       => time(),
            'nickname' => isset($info['nickname']) ? $info['nickname'] : '',
            'account'  => isset($info['account']) ? $info['account'] : '',
        ));
        return array('code' => 0, 'data' => array('state' => 'ok', 'nickname' => isset($info['nickname']) ? $info['nickname'] : ''));
    }

    /** 用 serviceTicket 走一遍 UC 各页面，累积 Cookie */
    private static function ticketToCookie(string $ticket): string
    {
        $jar = array();
        $steps = array(
            'https://drive.uc.cn/account/info?st=' . rawurlencode($ticket),
            'https://drive.uc.cn/',
            'https://drive.uc.cn/main',
            'https://drive.uc.cn/account/setting',
            'https://drive.uc.cn/list',
        );
        foreach ($steps as $u) {
            $cur = $u;
            $n = 0;
            while ($cur !== '' && $n++ < 6) {
                $r = self::req($cur, array(
                    'method'  => 'GET',
                    'cookie'  => self::jarStr($jar),
                    'headers' => array(
                        'user-agent: ' . self::UA,
                        'accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8',
                        'referer: https://drive.uc.cn/',
                    ),
                ));
                self::absorb($jar, $r['setcookie']);
                if ($r['status'] >= 300 && $r['status'] < 400 && $r['location'] !== '') {
                    $cur = (strncmp($r['location'], 'http', 4) === 0) ? $r['location'] : 'https://drive.uc.cn' . $r['location'];
                } else {
                    break;
                }
            }
        }
        $ck = self::jarStr($jar);
        $r = self::req(self::API . '/user/info', array(
            'method'  => 'GET',
            'cookie'  => $ck,
            'headers' => self::apiHeaders($ck),
        ));
        self::absorb($jar, $r['setcookie']);
        return self::jarStr($jar);
    }

    /** 手工 Cookie 兜底：直接保存并验证 */
    public static function loginByCookie(string $cookie): array
    {
        $cookie = trim($cookie);
        if ($cookie === '') return array('code' => 400, 'msg' => 'Cookie 为空');
        $info = self::fetchUserInfo($cookie);
        self::saveAuth(array(
            'cookie'   => $cookie,
            'ts'       => time(),
            'nickname' => isset($info['nickname']) ? $info['nickname'] : '',
            'account'  => isset($info['account']) ? $info['account'] : '',
            'source'   => 'manual',
        ));
        return array('code' => 0, 'data' => array('verified' => !empty($info['ok']), 'nickname' => isset($info['nickname']) ? $info['nickname'] : ''));
    }

    /** 调 UC 用户信息接口，验证 Cookie 是否有效 */
    public static function fetchUserInfo(string $cookie): array
    {
        $r = self::req(self::API . '/user/info', array(
            'method'  => 'GET',
            'cookie'  => $cookie,
            'headers' => self::apiHeaders($cookie),
        ));
        $j = json_decode($r['body'], true);
        if (!is_array($j)) return array('ok' => false);
        if ((int)(isset($j['code']) ? $j['code'] : -1) !== 0) return array('ok' => false, 'raw' => $j);
        $d = isset($j['data']) ? $j['data'] : array();
        return array(
            'ok'       => true,
            'nickname' => isset($d['nickname']) ? $d['nickname'] : (isset($d['name']) ? $d['name'] : ''),
            'account'  => isset($d['mask_mobile']) ? $d['mask_mobile'] : '',
        );
    }

    /* ==================== 分享解析 ==================== */

    public static function shareKey(string $url): string
    {
        $url = trim($url);
        if (preg_match('#/s/([a-zA-Z0-9]+)#', $url, $m)) return $m[1];
        if (preg_match('#(?:pwd_id|share_id)=([a-zA-Z0-9]+)#', $url, $m)) return $m[1];
        if (preg_match('#^[a-zA-Z0-9]{8,}$#', $url)) return $url;
        return '';
    }

    /** 解析分享：返回文件清单（含一层子目录展开） */
    public static function resolve(string $shareUrl, string $pwd = ''): array
    {
        $auth = self::loadAuth();
        if (empty($auth['cookie'])) return array('code' => 401, 'msg' => 'UC 账号未登录，请先在后台扫码登录');
        $key = self::shareKey($shareUrl);
        if ($key === '') return array('code' => 400, 'msg' => '无法识别 UC 分享链接');
        $cookie = $auth['cookie'];

        $r = self::req(self::API . '/share/sharepage/token?entry=ft&fr=pc&pr=UCBrowser', array(
            'method'  => 'POST',
            'cookie'  => $cookie,
            'headers' => self::apiHeaders($cookie),
            'body'    => json_encode(array('share_for_transfer' => true, 'pwd_id' => $key, 'passcode' => $pwd), JSON_UNESCAPED_UNICODE),
        ));
        $j = json_decode($r['body'], true);
        if (!is_array($j)) return array('code' => 502, 'msg' => 'UC 接口无响应 (HTTP ' . $r['status'] . ')');
        if ((int)(isset($j['code']) ? $j['code'] : -1) !== 0) {
            $code = (int)$j['code'];
            $msg  = isset($j['message']) ? $j['message'] : '未知错误';
            if ($code === 40004 || stripos($msg, 'passcode') !== false || stripos($msg, '密码') !== false) {
                return array('code' => 403, 'msg' => '提取码不正确或需要提取码', 'need_pwd' => true, 'raw' => $msg);
            }
            return array('code' => 403, 'msg' => '解析失败：' . $msg, 'raw' => $j);
        }
        $stoken = isset($j['data']['stoken']) ? $j['data']['stoken'] : '';
        if ($stoken === '') return array('code' => 403, 'msg' => '未取到 stoken');

        $files = self::listDir($key, $stoken, '0', $cookie);
        if (isset($files['error'])) return array('code' => 502, 'msg' => $files['error']);

        $flat = array();
        foreach ($files['list'] as $f) {
            $flat[] = self::normFile($f);
        }
        // 一级子目录展开
        foreach ($files['list'] as $f) {
            if (!empty($f['dir']) && count($flat) < 60) {
                $sub = self::listDir($key, $stoken, $f['fid'], $cookie);
                if (empty($sub['error'])) {
                    foreach ($sub['list'] as $sf) {
                        $n = self::normFile($sf);
                        $n['parent'] = $f['file_name'];
                        $flat[] = $n;
                    }
                }
            }
        }
        return array('code' => 0, 'data' => array('share_key' => $key, 'files' => $flat));
    }

    private static function listDir(string $key, string $stoken, string $pdir, string $cookie): array
    {
        $url = self::API . '/share/sharepage/detail?pr=ucpro&fr=pc'
             . '&pwd_id=' . urlencode($key) . '&stoken=' . urlencode($stoken)
             . '&pdir_fid=' . urlencode($pdir) . '&force=0&_page=1&_size=100'
             . '&_sort=file_type:asc,updated_at:desc';
        $r = self::req($url, array('method' => 'GET', 'cookie' => $cookie, 'headers' => self::apiHeaders($cookie)));
        $j = json_decode($r['body'], true);
        if (!is_array($j)) return array('error' => 'UC 列表接口无响应 (HTTP ' . $r['status'] . ')');
        if ((int)(isset($j['code']) ? $j['code'] : -1) !== 0) {
            return array('error' => '列表获取失败：' . (isset($j['message']) ? $j['message'] : '未知'));
        }
        $list = isset($j['data']['list']) ? $j['data']['list'] : array();
        return array('list' => $list, 'raw' => $j['data']);
    }

    private static function normFile(array $f): array
    {
        $isDir = (isset($f['dir']) && $f['dir']) || (isset($f['file_type']) && (int)$f['file_type'] === 0 && empty($f['file_name']) === false && !empty($f['dir']));
        return array(
            'name'    => isset($f['file_name']) ? $f['file_name'] : '',
            'size'    => isset($f['size']) ? (int)$f['size'] : 0,
            'size_mb' => isset($f['size']) ? round($f['size'] / 1048576, 2) : 0,
            'isdir'   => !empty($f['dir']),
            'fid'     => isset($f['fid']) ? $f['fid'] : '',
            'ext'     => strtolower(pathinfo(isset($f['file_name']) ? $f['file_name'] : '', PATHINFO_EXTENSION)),
            'updated' => isset($f['updated_at']) ? $f['updated_at'] : '',
        );
    }

    /** 从文件名推断软件名与版本号 */
    public static function guessNameVersion(string $fileName): array
    {
        $base = preg_replace('/\.[a-zA-Z0-9]{2,5}$/', '', $fileName);
        $base = str_replace(array('[', ']', '(', ')'), ' ', $base);
        $ver = '';
        if (preg_match('/v?(\d+(?:\.\d+){1,3}(?:[-_][A-Za-z0-9\.]+)?)/i', $base, $m)) {
            $ver = $m[1];
        }
        $name = $base;
        if ($ver !== '') {
            $name = str_replace(array('v' . $ver, $ver), ' ', $name);
        }
        $name = trim(preg_replace('/[\s_\-]+/', ' ', $name));
        $name = trim($name, " -_");
        if ($name === '') $name = $base;
        return array('name' => $name, 'version' => $ver);
    }
}
