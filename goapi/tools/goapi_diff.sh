#!/usr/bin/env bash
# goapi 双跑逐字对比：同一个 action 分别打 Go 网关与纯 PHP，逐字节 diff 状态码/响应头/响应体。
#
#   Go   (被测)  默认 http://127.0.0.1:9100
#   PHP  (基准)   默认 http://127.0.0.1:9101   <-- 纯 PHP 对照端点，**不要**用 9845
#                 (9845 切换后就是 Go 了，用它当基准会变成"自己跟自己比")
#
# 用法:
#   ./goapi_diff.sh                      # 跑默认清单
#   ./goapi_diff.sh --only version,apps  # 只跑指定 action
#   TOKEN=xxx ./goapi_diff.sh            # 带上鉴权 token(需要登录的只读接口)
#   STREAM_SECONDS=3 ./goapi_diff.sh     # SSE 采样窗口(默认 2 秒)
#
# 只读 action 清单刻意不含任何写库/扣抽奖次数的接口。
set -uo pipefail

GO_BASE="${GO_BASE:-http://127.0.0.1:9100}"
PHP_BASE="${PHP_BASE:-http://127.0.0.1:9101}"
TOKEN="${TOKEN:-}"
STREAM_SECONDS="${STREAM_SECONDS:-2}"
ONLY="${ONLY:-}"
WORK="${WORK:-${TMPDIR:-/tmp}/goapi-diff}"

# action|额外查询串   (全部只读: version/health 是原生, 其余透传 PHP)
ACTIONS=(
  "version|"
  "health|"
  "banners|"
  "categories|"
  "apps|"
  "app_detail|id=1"
  "contributors|"
  "about_config_get|"
  "notice_get|"
  "stream|pm_id=0&group_id=0"
)

# 这些头一律不比较(传输层/易变/后端身份), 缺了会误报
IGNORE_HDR='^(date|server|connection|keep-alive|transfer-encoding|content-length|vary|x-powered-by):'

pass=0
fail=0

rm -rf "$WORK"; mkdir -p "$WORK"

say()  { printf '%s\n' "$*"; }
ok()   { printf '  \033[32m✅ %s\033[0m\n' "$*"; }
bad()  { printf '  \033[31m❌ %s\033[0m\n' "$*"; }

# 取一次响应: 状态码写到 $4.code, 头到 $4.hdr, 体到 $4.body
fetch() {
  local base="$1" action="$2" q="$3" prefix="$4" url code
  url="${base}/api.php?action=${action}"
  [ -n "$q" ] && url="${url}&${q}"
  [ -n "$TOKEN" ] && url="${url}&token=${TOKEN}"
  code="$(curl -sS --max-time 20 -H 'Accept-Encoding: identity' \
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
    timeout "$STREAM_SECONDS" curl -sS -N -H 'Accept-Encoding: identity' \
      "${GO_BASE}/api.php?action=stream&${q}${TOKEN:+&token=$TOKEN}" > "$WORK/go.stream" 2>"$WORK/go.stream.err"
    timeout "$STREAM_SECONDS" curl -sS -N -H 'Accept-Encoding: identity' \
      "${PHP_BASE}/api.php?action=stream&${q}${TOKEN:+&token=$TOKEN}" > "$WORK/php.stream" 2>"$WORK/php.stream.err"
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

say "Go  : $GO_BASE   (被测)"
say "PHP : $PHP_BASE  (基准, 纯 PHP 对照端点)"
say "工作目录: $WORK"
say "只读清单: ${#ACTIONS[@]} 个 action (无写库/无扣抽奖次数)"

for item in "${ACTIONS[@]}"; do
  a="${item%%|*}"; q="${item#*|}"
  selected "$a" && diff_case "$a" "$q"
done

say ""
say "──────────────────────────────────────────"
say "结果: 通过 $pass / 失败 $fail"
if [ "$fail" -gt 0 ]; then
  say "差异清单见上面 ❌ 行; 原始报文在 $WORK/"
  exit 1
fi
say "全部 action 逐字一致 ✅"
