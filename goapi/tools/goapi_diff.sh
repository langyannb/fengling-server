#!/usr/bin/env bash
# goapi 双跑逐字对比：同一个 action 分别打 Go 网关与纯 PHP，逐字节 diff 状态码/响应头/响应体。
#
#   Go   (被测)  默认 http://127.0.0.1:9100
#   PHP  (基准)   默认 http://127.0.0.1:9101   <-- 纯 PHP 对照端点，**不要**用 9845
#                 (9845 切换后就是 Go 了，用它当基准会变成"自己跟自己比")
#
# 用法:
#   ./goapi_diff.sh                      # 跑默认清单(公开只读, 不需要 token)
#   ./goapi_diff.sh --only version,apps  # 只跑指定 action
#   TOKEN=xxx ./goapi_diff.sh            # 额外跑「需要登录」的只读 action
#   STREAM_SECONDS=3 ./goapi_diff.sh     # SSE 采样窗口(默认 2 秒)
#
# 鉴权走 `Authorization: Bearer <token>`, 与 api.php 的 current_user() 一致
# (**不是** &token= 查询参数, PHP 侧不认那种写法)。
# 只读 action 清单刻意不含任何写库 / 扣抽奖次数 / 新增删除的接口。
set -uo pipefail

GO_BASE="${GO_BASE:-http://127.0.0.1:9100}"
PHP_BASE="${PHP_BASE:-http://127.0.0.1:9101}"
TOKEN="${TOKEN:-}"
STREAM_SECONDS="${STREAM_SECONDS:-2}"
UPLOAD_CASES="${UPLOAD_CASES:-0}"   # 1 = 额外跑阶段 1 的上传类 action（写操作，需要 TOKEN）
ONLY="${ONLY:-}"
WORK="${WORK:-${TMPDIR:-/tmp}/goapi-diff}"

# action|额外查询串|need_token     need_token=1 表示没有 TOKEN 时跳过
# 全部只读: 只调 SELECT / 只读缓存, 不写库、不扣抽奖次数、不新增删除任何行。
# ⚠️ 破坏性 action(admin_lottery_*_reset / *_delete / *_import / quota_all / uc_import …)
#    **永远不进这张表** —— 它们只在后台面板由人手工点。
ACTIONS=(
  # ---- 公开只读(不需要 token) ----
  "version||0"
  "banners||0"
  "categories||0"
  "apps||0"
  "app_detail|id=1|0"
  "contributors||0"
  "contributor_apps|id=1|0"
  "about_config_get||0"
  "notice_get||0"
  "stats||0"
  # ---- 普通用户只读(需要登录 token) ----
  "user_me||1"
  "pm_conversations||1"
  "notifications||1"
  "social_groups||1"
  "lottery_info||1"
  "lottery_records||1"
  "stream|pm_id=0&group_id=0|1"
  # ---- 管理员只读(需要管理员 token) ----
  "admin_users||1"
  "admin_groups||1"
  "admin_social_messages||1"
  "admin_notification_list||1"
  "admin_lottery_prizes||1"
  "admin_lottery_codes||1"
  "admin_lottery_draws||1"
  "admin_lottery_config_get||1"
  "admin_video_config_get||1"
  "admin_video_usage||1"
  "admin_pm_conversations||1"
  "admin_tags||1"
  "crash_reports||1"
  "harm_reports||1"
  "uc_status||1"
)

# 这些头一律不比较(传输层/易变/后端身份), 缺了会误报
IGNORE_HDR='^(date|server|connection|keep-alive|transfer-encoding|content-length|vary|x-powered-by):'

pass=0
fail=0
skip=0

rm -rf "$WORK"; mkdir -p "$WORK"

say()  { printf '%s\n' "$*"; }
ok()   { printf '  \033[32m✅ %s\033[0m\n' "$*"; }
bad()  { printf '  \033[31m❌ %s\033[0m\n' "$*"; }

# 取一次响应: 状态码写到 $4.code, 头到 $4.hdr, 体到 $4.body
fetch() {
  local base="$1" action="$2" q="$3" prefix="$4" url code
  url="${base}/api.php?action=${action}"
  [ -n "$q" ] && url="${url}&${q}"
  # 鉴权头: 与 api.php 的 current_user() 一致(Authorization: Bearer),
  # 公开 action 带上也不影响(PHP 只是读一下用户, 不强制)。
  local auth=()
  [ -n "$TOKEN" ] && auth=(-H "Authorization: Bearer ${TOKEN}")
  code="$(curl -sS --max-time 20 -H 'Accept-Encoding: identity' "${auth[@]+"${auth[@]}"}" \
            -D "${prefix}.hdr" -o "${prefix}.body" -w '%{http_code}' "$url" 2>"${prefix}.err")" || code=000
  printf '%s' "$code" > "${prefix}.code"
}

# 头规范化: 去 CR、小写名、丢忽略表、排序(Go 的 Header 是 map, 顺序无意义)
norm_hdr() {
  tr -d '\r' < "$1" \
    | awk '/^[^:]+:/{k=tolower($0); sub(/:.*/,"",k); v=$0; sub(/^[^:]*:[ \t]*/,"",v); print k": "v}' \
    | grep -viE "$IGNORE_HDR" \
    | sort
}

body_stat() { printf '%s 字节' "$(wc -c < "$1" | tr -d ' ')"; }

diff_case() {
  local action="$1" q="$2"
  say ""
  say "=== ${action}${q:+?$q} ==="

  if [ "$action" = "stream" ]; then
    # SSE: 采样 STREAM_SECONDS 秒, 按行比对(事件流是逐行协议)
    local sauth=()
    [ -n "$TOKEN" ] && sauth=(-H "Authorization: Bearer ${TOKEN}")
    timeout "$STREAM_SECONDS" curl -sS -N -H 'Accept-Encoding: identity' "${sauth[@]+"${sauth[@]}"}" \
      "${GO_BASE}/api.php?action=stream&${q}" > "$WORK/go.stream" 2>"$WORK/go.stream.err"
    timeout "$STREAM_SECONDS" curl -sS -N -H 'Accept-Encoding: identity' "${sauth[@]+"${sauth[@]}"}" \
      "${PHP_BASE}/api.php?action=stream&${q}" > "$WORK/php.stream" 2>"$WORK/php.stream.err"
    if cmp -s "$WORK/go.stream" "$WORK/php.stream"; then
      ok "SSE 前 ${STREAM_SECONDS}s 逐字一致 ($(body_stat "$WORK/go.stream"))"
      pass=$((pass+1))
    else
      bad "SSE 输出不一致 ($(body_stat "$WORK/go.stream") vs $(body_stat "$WORK/php.stream"))"
      diff -u "$WORK/php.stream" "$WORK/go.stream" | head -30
      fail=$((fail+1))
    fi
    return
  fi

  fetch "$GO_BASE"  "$action" "$q" "$WORK/go"
  fetch "$PHP_BASE" "$action" "$q" "$WORK/php"

  local okall=1
  # 1) 状态码
  local gc pc; gc="$(cat "$WORK/go.code")"; pc="$(cat "$WORK/php.code")"
  if [ "$gc" = "$pc" ]; then ok "状态码 $gc"; else bad "状态码 Go=$gc PHP=$pc"; okall=0; fi

  # 2) 响应头
  norm_hdr "$WORK/go.hdr"  > "$WORK/go.hdr.n"
  norm_hdr "$WORK/php.hdr" > "$WORK/php.hdr.n"
  if cmp -s "$WORK/go.hdr.n" "$WORK/php.hdr.n"; then
    ok "响应头一致 ($(wc -l < "$WORK/go.hdr.n" | tr -d ' ') 项, 已忽略 date/server/content-length 等)"
  else
    bad "响应头差异:"; diff -u "$WORK/php.hdr.n" "$WORK/go.hdr.n" | sed 's/^/     /' | head -20; okall=0
  fi

  # 3) 响应体(逐字节)
  if cmp -s "$WORK/go.body" "$WORK/php.body"; then
    ok "响应体逐字一致 ($(body_stat "$WORK/go.body"))"
  else
    bad "响应体差异 ($(body_stat "$WORK/go.body") vs $(body_stat "$WORK/php.body"))"
    cmp "$WORK/go.body" "$WORK/php.body" 2>&1 | head -2 | sed 's/^/     /'
    say "     Go : $(head -c 300 "$WORK/go.body")"
    say "     PHP: $(head -c 300 "$WORK/php.body")"
    okall=0
  fi

  # 4) gzip 往返(native JSON 接口): 两条路径都必须自己压, 且解压后一致
  if [ "$action" = "version" ] || [ "$action" = "health" ]; then
    local gz_url="${GO_BASE}/api.php?action=${action}${q:+&$q}"
    local pz_url="${PHP_BASE}/api.php?action=${action}${q:+&$q}"
    local gce pce
    gce="$(curl -sS --max-time 20 -H 'Accept-Encoding: gzip' -D - -o "$WORK/go.gz" "$gz_url" 2>/dev/null | tr -d '\r' | awk 'tolower($1)=="content-encoding:"{print $2}')"
    pce="$(curl -sS --max-time 20 -H 'Accept-Encoding: gzip' -D - -o "$WORK/php.gz" "$pz_url" 2>/dev/null | tr -d '\r' | awk 'tolower($1)=="content-encoding:"{print $2}')"
    if [ "$gce" = "gzip" ] && [ "$pce" = "gzip" ]; then
      if cmp -s <(gzip -dc "$WORK/go.gz" 2>/dev/null) <(gzip -dc "$WORK/php.gz" 2>/dev/null); then
        ok "gzip: 两侧都压缩且解压后逐字一致"
      else
        bad "gzip: 解压后内容不一致"; okall=0
      fi
    else
      bad "gzip: Content-Encoding Go='${gce:-无}' PHP='${pce:-无}' (期望都是 gzip)"; okall=0
    fi
  fi

  if [ "$okall" = 1 ]; then pass=$((pass+1)); else fail=$((fail+1)); fi
}

selected() {
  [ -z "$ONLY" ] && return 0
  case ",$ONLY," in *",$1,"*) return 0 ;; *) return 1 ;; esac
}

# ---------- 阶段 1：上传类 action 的双跑对比（默认关闭，见 README） ----------
#
# 为什么默认关闭：这几个 action 是**写操作**（会往对象存储写真实对象、user_avatar 还会改
# users.avatar），需要真实登录 token，并且会与「每人每 10 分钟 30 次上传」的限流交互。
# 显式打开：TOKEN=<token> UPLOAD_CASES=1 ./goapi_diff.sh
#
# 归一化说明（上传响应里不可逐字比较的部分）：
#   - url/key 含随机段：<YmdHis>_<8hex>、apk 的 fengling_Ymd_His；
#   - 「需要缩放」的图片在 Go 侧可能改扩展名（见 README 阶段 1 差异说明），这类只算 ⚠️ 已知差异；
#   - 图片类 action 的 size 允许不同（Go 不重编码 = 原始字节），APK 的 size 必须逐字相同。
norm_upload() {
  local f="$1" mask_size="${2:-0}"
  if [ "$mask_size" = "1" ]; then
    sed -E -e 's#[0-9]{14}_[0-9a-f]{8}#<RAND>#g' \
           -e 's#fengling_[0-9]{8}_[0-9]{6}#fengling_<TS>#g' \
           -e 's#"size":[0-9]+#"size":<N>#g' "$f"
  else
    sed -E -e 's#[0-9]{14}_[0-9a-f]{8}#<RAND>#g' \
           -e 's#fengling_[0-9]{8}_[0-9]{6}#fengling_<TS>#g' "$f"
  fi
}

# 把图片扩展名也抹掉（只用于判定「差异是否仅限于扩展名」这种已知差异）
norm_upload_ext() {
  norm_upload "$1" "${2:-0}" | sed -E 's#<RAND>\.(png|jpe?g|gif|webp)#<RAND>.<EXT>#g'
}

mk_upload_samples() {
  local dir="$WORK/samples"
  mkdir -p "$dir"
  python3 - "$dir" <<'PY'
import os, struct, sys, zlib

d = sys.argv[1]

def chunk(tag, data):
    return struct.pack('>I', len(data)) + tag + data + struct.pack('>I', zlib.crc32(tag + data) & 0xffffffff)

def write_png(path, w, h):
    raw = bytearray()
    for y in range(h):
        raw.append(0)
        for x in range(w):
            raw += bytes([(x * 7) % 256, (y * 5) % 256, 128])
    blob = (b'\x89PNG\r\n\x1a\n'
            + chunk(b'IHDR', struct.pack('>IIBBBBB', w, h, 8, 2, 0, 0, 0))
            + chunk(b'IDAT', zlib.compress(bytes(raw), 9))
            + chunk(b'IEND', b''))
    with open(path, 'wb') as fh:
        fh.write(blob)

# 不需要缩放（<=1600 宽）与需要缩放（>1600 宽）各一张
write_png(os.path.join(d, 'small.png'), 64, 48)
write_png(os.path.join(d, 'big.png'), 2400, 1200)
# 伪 mp4：只要 ftyp box（容器判定只看前 4096 字节的文件头）
with open(os.path.join(d, 'clip.mp4'), 'wb') as fh:
    fh.write(struct.pack('>I', 24) + b'ftypisom' + b'\x00' * 16 + b'\x00' * 512)
# 伪 apk：服务端只校验文件名扩展名
with open(os.path.join(d, 'fake.apk'), 'wb') as fh:
    fh.write(b'PK\x03\x04' + b'\x00' * 4096)
# 非图片（走「这不是一张有效的图片」）与非白名单扩展名
with open(os.path.join(d, 'notimage.png'), 'wb') as fh:
    fh.write(b'this is definitely not a png\n')
with open(os.path.join(d, 'plain.txt'), 'wb') as fh:
    fh.write(b'hello\n')
print('samples ->', d)
PY
  ls -l "$dir" | tail -n +2 | sed 's/^/     /'
}

# 上传双跑：状态码必须一致；响应体归一化后比对（随机 key / 时间戳 / 图片 size）。
upload_case() {
  local action="$1" file="$2" extra="$3" mask_size="${4:-0}"
  say ""
  say "=== ${action} ($(basename "$file")${extra:+, $extra}) ==="

  local args=()
  [ -n "$extra" ] && args=(-F "$extra")
  local gc pc
  gc="$(curl -sS --max-time 120 -D "$WORK/go.u.hdr" -o "$WORK/go.u.body" -w '%{http_code}' \
        -H "Authorization: Bearer $TOKEN" -F "file=@${file}" ${args[@]+"${args[@]}"} \
        "${GO_BASE}/api.php?action=${action}" 2>"$WORK/go.u.err")" || gc=000
  pc="$(curl -sS --max-time 120 -D "$WORK/php.u.hdr" -o "$WORK/php.u.body" -w '%{http_code}' \
        -H "Authorization: Bearer $TOKEN" -F "file=@${file}" ${args[@]+"${args[@]}"} \
        "${PHP_BASE}/api.php?action=${action}" 2>"$WORK/php.u.err")" || pc=000

  if [ "$gc" = "$pc" ]; then
    ok "状态码 $gc"
  else
    bad "状态码 Go=$gc PHP=$pc"
    say "     Go : $(head -c 300 "$WORK/go.u.body")"
    say "     PHP: $(head -c 300 "$WORK/php.u.body")"
    fail=$((fail+1))
    return
  fi

  norm_upload "$WORK/go.u.body"  "$mask_size" > "$WORK/go.u.n"
  norm_upload "$WORK/php.u.body" "$mask_size" > "$WORK/php.u.n"
  if cmp -s "$WORK/go.u.n" "$WORK/php.u.n"; then
    ok "响应体（已归一化随机 key）一致: $(cat "$WORK/go.u.n")"
    pass=$((pass+1))
    return
  fi

  norm_upload_ext "$WORK/go.u.body"  "$mask_size" > "$WORK/go.u.ne"
  norm_upload_ext "$WORK/php.u.body" "$mask_size" > "$WORK/php.u.ne"
  if cmp -s "$WORK/go.u.ne" "$WORK/php.u.ne"; then
    say "  \033[33m⚠️ 仅扩展名不同（阶段 1 已知差异：Go 没有纯 Go 的 lossy webp 编码器，需要缩放时按有无 alpha 输出 png/jpg）\033[0m"
    say "     Go : $(cat "$WORK/go.u.n")"
    say "     PHP: $(cat "$WORK/php.u.n")"
    pass=$((pass+1))
    return
  fi

  bad "响应体差异:"
  say "     Go : $(cat "$WORK/go.u.n")"
  say "     PHP: $(cat "$WORK/php.u.n")"
  fail=$((fail+1))
}

run_upload_cases() {
  mk_upload_samples
  local d="$WORK/samples"
  upload_case user_avatar          "$d/small.png"    ""            0
  upload_case social_image_upload  "$d/small.png"    ""            1
  upload_case social_image_upload  "$d/big.png"      ""            1
  upload_case social_image_upload  "$d/notimage.png" ""            0
  upload_case social_video_upload  "$d/clip.mp4"     "video_w=1080" 1
  upload_case social_video_upload  "$d/notimage.png" ""            0
  upload_case upload               "$d/fake.apk"     "type=apk"    0
  upload_case upload               "$d/big.png"      ""            1
  upload_case upload               "$d/plain.txt"    ""            0
  upload_case upload_apk           "$d/fake.apk"     ""            0
}

say "Go  : $GO_BASE   (被测)"
say "PHP : $PHP_BASE  (基准, 纯 PHP 对照端点)"
say "工作目录: $WORK"
say "只读清单: ${#ACTIONS[@]} 个 action (无写库/无扣抽奖次数)"
if [ -z "$TOKEN" ]; then
  say "TOKEN 未设置: 只跑公开只读 action, 需要登录的会被跳过"
else
  say "TOKEN 已设置: 额外覆盖需要登录/管理员权限的只读 action"
fi

# Go 独有的 action: PHP 侧根本没有, 只能自检 Go 侧(不能拿来对比)。
check_go_only() {
  say ""
  say "=== action=health（Go 独有）==="
  local code
  code="$(curl -sS --max-time 20 -o "$WORK/health.body" -w '%{http_code}' \
            "${GO_BASE}/api.php?action=health" 2>/dev/null || echo 000)"
  local body; body="$(head -c 200 "$WORK/health.body" 2>/dev/null)"
  if [ "$code" = "200" ]; then
    ok "Go 返回 200: $body"
    pass=$((pass+1))
  else
    bad "Go 返回 $code: $body"
    say "     (PHP_FALLBACK=0 时这不影响任何客户端, 但如果这里是 404 说明二进制是旧版)"
    fail=$((fail+1))
  fi
  say "  说明: api.php 没有 health 这个 action(PHP 侧会回「未知操作」404), 因此不参与双跑对比。"
}

for item in "${ACTIONS[@]}"; do
  a="${item%%|*}"
  rest="${item#*|}"
  q="${rest%%|*}"
  nt="${rest##*|}"
  selected "$a" || continue
  if [ "$nt" = "1" ] && [ -z "$TOKEN" ]; then
    say ""
    say "=== ${a} ==="
    printf '  \033[33m⏭  需要登录 token, 已跳过\033[0m  (TOKEN=<登录 token> %s)\n' "$0"
    skip=$((skip+1))
    continue
  fi
  diff_case "$a" "$q"
done

selected "health" && check_go_only

if [ "$UPLOAD_CASES" = "1" ]; then
  if [ -z "$TOKEN" ]; then
    say ""
    say "UPLOAD_CASES=1 需要 TOKEN=<登录 token>, 已跳过上传类 action"
  else
    say ""
    say "阶段 1 上传类 action（写操作：会往对象存储写对象、user_avatar 会改 users.avatar）"
    run_upload_cases
  fi
else
  say ""
  say "阶段 1 上传类 action 已跳过（写操作）。需要时: TOKEN=<token> UPLOAD_CASES=1 $0"
fi

say ""
say "──────────────────────────────────────────"
say "结果: 通过 $pass / 失败 $fail / 跳过 $skip"
if [ "$fail" -gt 0 ]; then
  say "差异清单见上面 ❌ 行; 原始报文在 $WORK/"
  exit 1
fi
say "全部 action 逐字一致 ✅"
