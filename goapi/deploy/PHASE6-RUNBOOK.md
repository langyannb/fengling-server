# 阶段 6 切换 Runbook（退休 PHP）

> 目标：把线上 `/api.php` 从「Go 网关 + PHP-FPM 兜底」切成「Go 全原生，PHP-FPM 可停」。
> 全程**不改客户端、不改 api.php、不改数据库结构**。每一步都可以停在这里，前一步都可回滚。

## 已核实的线上事实（2026-10-29 22:30 探查）

| 项 | 实际值 |
| --- | --- |
| 站点根 | `/www/wwwroot/flfxk` |
| systemd 服务 | **`goapi.service`**（不是 `fengling-goapi`） |
| 二进制 | `/opt/fengling/goapi`（原文件日期 10-06 00:40，跑的是阶段 0/1 版本） |
| 环境变量 | `/etc/fengling/goapi.env`（23 个键，**没有** `PHP_FALLBACK`） |
| nginx 片段 | `/www/server/panel/vhost/nginx/extension/flfxk/goapi.conf`（已生效） |
| PHP 对照端 | `flfxk-phpref.conf` → `http://127.0.0.1:9101`，用 `php-cgi-85.sock` |
| 用 85 的站点 | 只有 `flfxk.conf` 与 `flfxk-phpref.conf`（`aevum_cloud` 用 74，不受影响） |
| 已经在跑 | `php-fpm-70/74/80/85` 四个实例；停 85 只影响风铃 |

新版本的 DB/S3/Redis/PHP 路径配置项全部有内置默认值且默认值就是线上路径，
所以**旧 env 不需要补任何键**，只加一行 `PHP_FALLBACK`。

## 准备（已由开发侧完成）

新二进制已上传到服务器，**没有**动任何运行中的东西：

```
/tmp/goapi.new.dc32429   # sha256 1b0f8c6a48c036daa33189d619aeabcce2cafda9e45a985ee490b63add323772
```

---

## 第 1 步：备份（30 秒）

```bash
TS=$(date +%Y%m%d-%H%M%S)
cd /opt/fengling && cp -a goapi "goapi.bak.$TS"
cp -a /etc/fengling/goapi.env "/etc/fengling/goapi.env.bak.$TS"
echo "备份完成 TS=$TS  记住这个时间戳，回滚要用"
ls -la /opt/fengling/goapi.bak.* | tail -2
```

预期：两行备份文件，第二行是刚建的。

## 第 2 步：换二进制 + 加开关=1（先保守，验证新版能起来）

```bash
install -m 755 /tmp/goapi.new.dc32429 /opt/fengling/goapi
grep -q '^PHP_FALLBACK=' /etc/fengling/goapi.env || echo 'PHP_FALLBACK=1' >> /etc/fengling/goapi.env
systemctl restart goapi
sleep 2
systemctl is-active goapi
```

预期：`active`
不是 `active` → `journalctl -u goapi -n 50 --no-pager` 看报错，然后跳到最后的「回滚」。

## 第 3 步：新二进制功能验证（仍在透传模式，最安全）

```bash
curl -s 'http://127.0.0.1:9100/api.php?action=version' | head -c 100; echo
curl -s -o /dev/null -w 'health=%{http_code}\n' 'http://127.0.0.1:9100/api.php?action=health'
curl -s 'http://127.0.0.1:9100/api.php?action=apps' | head -c 100; echo
curl -s -o /dev/null -w 'bad=%{http_code}\n' 'http://127.0.0.1:9100/api.php?action=__probe__'
```

预期：
- `version` → `{"code":0,"msg":"ok","data":{"version":...`
- `health=200`（Go 新加的 action，旧版没有；返回 200 说明**新二进制真的在跑**）
- `apps` → 正常 JSON
- `bad=404`

## 第 4 步：双跑逐字对比（趁 PHP 还活着，这一步最有价值）

```bash
for a in version banners categories apps stats notice_get about_config_get contributors; do
  g=$(curl -s "http://127.0.0.1:9100/api.php?action=$a" | md5sum | cut -c1-8)
  p=$(curl -s "http://127.0.0.1:9101/api.php?action=$a" | md5sum | cut -c1-8)
  [ "$g" = "$p" ] && echo "OK    $a" || echo "DIFF  $a  go=$g php=$p"
done
```

预期：8 行全 `OK`。

带登录态的用例需要 token（PHP 只认 `Authorization: Bearer`，`&token=` 无效）：

```bash
TOKEN='<管理员 token>'
for a in admin_users admin_groups admin_lottery_prizes uc_status; do
  g=$(curl -s -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:9100/api.php?action=$a" | md5sum | cut -c1-8)
  p=$(curl -s -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:9101/api.php?action=$a" | md5sum | cut -c1-8)
  [ "$g" = "$p" ] && echo "OK    $a" || echo "DIFF  $a  go=$g php=$p"
done
```

## 第 5 步：真机冒烟（重要，别跳）

App 走一遍：首页列表 → 分类 → 详情 → 搜索 → 登录 → 我的 → 私聊列表 → 群聊 → 抽奖页。
后台（`http://61.184.23.123:9845/admin.html`）走一遍：用户管理 / 应用审核 / 抽奖奖品 / 通知。
**抽奖的 6 个破坏性操作（重置配额、重置活动、清空奖池、删码、导码、删奖品）先别点**，
等第 7 步之后再单独验（它们现在也是 Go 原生了，但会真的删数据）。

## 第 6 步：关透传（PHP_FALLBACK=0）

```bash
sed -i 's/^PHP_FALLBACK=.*/PHP_FALLBACK=0/' /etc/fengling/goapi.env
systemctl restart goapi
sleep 2
systemctl is-active goapi
curl -s 'http://127.0.0.1:9100/api.php?action=__probe__'; echo
curl -s -o /dev/null -w 'version=%{http_code} ' 'http://127.0.0.1:9100/api.php?action=version'
curl -s -o /dev/null -w 'apps=%{http_code}\n' 'http://127.0.0.1:9100/api.php?action=apps'
```

预期：
- `active`
- `{"code":404,"msg":"未知操作: __probe__","data":null}` ← **这条由 Go 自己回**（透传模式下是 PHP 回的，内容一样）
- `version=200`、`apps=200`

## 第 7 步：停 PHP-FPM（真的退休）

宝塔面板：软件商店 → PHP-85 → 停止，或命令行：

```bash
systemctl stop php-fpm-85
systemctl is-active php-fpm-85   # 期望 inactive
```

停完立刻再验一遍：

```bash
curl -s -o /dev/null -w 'version=%{http_code} ' 'http://127.0.0.1:9100/api.php?action=version'
curl -s -o /dev/null -w 'apps=%{http_code} ' 'http://127.0.0.1:9100/api.php?action=apps'
curl -s -o /dev/null -w 'health=%{http_code}\n' 'http://127.0.0.1:9100/api.php?action=health'
curl -s -o /dev/null -w 'pref9101=%{http_code}\n' 'http://127.0.0.1:9101/api.php?action=version'
```

预期：`version=200 apps=200 health=200`，`pref9101=502`（对照端没 PHP 了，正常）。
之后 App 再走一遍首页/详情/登录（App 走 9845 → nginx → goapi，与 fpm 无关）。

## 第 8 步（可选）：清理迁移期残留

```bash
mv /www/server/panel/vhost/nginx/flfxk-phpref.conf /root/flfxk-phpref.conf.bak
nginx -t && /etc/init.d/nginx reload
```

`api.php` / `uc.php` / `apk.php` / `arsc.php` / `mailer.php` / `config.php` **都不要删** ——
它们是唯一契约参考实现，fpm 停着就等于一堆静态文件。

---

## 回滚（任何一步不对劲，60 秒回原样）

```bash
sed -i 's/^PHP_FALLBACK=.*/PHP_FALLBACK=1/' /etc/fengling/goapi.env
cd /opt/fengling && cp -a goapi.bak.<TS> goapi
systemctl restart goapi && sleep 2 && systemctl is-active goapi
systemctl start php-fpm-85 && systemctl is-active php-fpm-85   # 若停过
```

整站退回纯 PHP（极端方案）：

```bash
mv /www/server/panel/vhost/nginx/extension/flfxk/goapi.conf /root/goapi.conf.bak
nginx -t && /etc/init.d/nginx reload
systemctl stop goapi && systemctl disable goapi
systemctl start php-fpm-85
```

## 注意事项

- **重启 goapi 后约 30 秒**会跑一次视频清理（`VIDEO_CLEANUP_INITIAL=30s`），属预期行为，
  日志里能看到 `video_cleanup`。
- `env` 其它键保持原样即可，新二进制对缺失键回落默认值
  （`PHP_SCRIPT=/www/wwwroot/flfxk/api.php`、`FPM_ADDR=/tmp/php-cgi-85.sock` 等）。
- 服务器上**不要装 Go、不要编译**，二进制只从开发侧/CI 产物来。
- 停 fpm 前最后确认：`grep -l 'php-cgi-85.sock' /www/server/panel/vhost/nginx/*.conf`
  只应剩 `flfxk.conf`（和可选的 `flfxk-phpref.conf`）。

---

## 执行记录：2026-11-03 线上切换完成（已完成，可作事后核对）

按上面 8 步实际跑完的结果，作为下一个人核对用。

### 结果

| 步骤 | 结果 |
| --- | --- |
| 1 备份 | `/opt/fengling/goapi.bak.20261030-005502`、`/etc/fengling/goapi.env.bak.20261030-005502` |
| 2 换二进制 | `goapi` active；`health=200`（证明新二进制在跑） |
| 3 功能验证 | version/apps/health 全 200，`__probe__` 404 |
| 4 双跑对比 | PHP_FALLBACK=1 时 35 项（公开 10 + 管理员 17 + 普通用户 6 + 参数化 2）全一致 |
| 5 真机冒烟 | 由用户侧执行 |
| 6 关透传 | `PHP_FALLBACK=0` + restart；`__probe__` → `{"code":404,"msg":"未知操作: __probe_x__","data":null}`（Go 自己回） |
| 6.5 关透传后双跑 | **35/35 全一致**（完全不走 PHP 的情况下与 9101 纯 PHP 逐字相同） |
| 7 停 fpm | `systemctl stop php-fpm-85` → inactive；`goapi` 仍 active；`/tmp/php-cgi-85.sock` 已消失；70/74/80 未动 |

### 停掉 fpm 后多出来的两条经验（务必记住）

**1. 访问线上必须带端口 9845。**
`flfxk.conf` 是 `listen 9845;`，直接 `curl http://127.0.0.1/api.php` 命中的是 nginx 默认 server
（返回「极简验证系统」页面，HTTP 200 但内容完全无关）。正确写法：

```bash
curl -s 'http://127.0.0.1:9845/api.php?action=version'
```

**2. nginx 片段里不能有指向 fpm 的 `error_page` 回退。**
线上扩展目录里的 `goapi.conf` 曾是手改版，多了一段：

```nginx
error_page 502 503 504 = @goapi_php_fallback;   # ← 危险
location @goapi_php_fallback { fastcgi_pass unix:/tmp/php-cgi-85.sock; ... }
```

fpm 停掉之后，goapi 一旦返回 502 就会被 nginx 改成「502 → 去连已经不存在的 socket」，
把一个本来有明确错误 JSON 的响应变成 nginx 的 502 页面，排障时会被误导。
**已用仓库版 `deploy/goapi.conf` 覆盖线上文件**（备份在
`/www/server/panel/vhost/nginx/extension/flfxk/goapi.conf.bak.20261103-080238`），
`nginx -t` 通过后 reload，复验 8 个只读 action 全 200、`__probe_x__` 404。

阶段 6 之后 nginx 侧**只应**有仓库版的这段（无 `error_page`、无 `@goapi_php_fallback`），
核对命令：

```bash
grep -c 'goapi_php_fallback' /www/server/panel/vhost/nginx/extension/flfxk/goapi.conf   # 期望 0
```

### 停 fpm 后的线上基线（2026-11-03 08:02，端口 9845）

```
probe(__probe_x__)  -> HTTP 404  {"code":404,"msg":"未知操作: __probe_x__","data":null}
version             -> 200  1352B
apps                -> 200  27606B
categories          -> 200  2111B
health              -> 200  117B
banners             -> 200  838B
notice_get          -> 200  74B
about_config_get    -> 200  240B
contributors        -> 200  582B
stats               -> 401  42B     (该 action 是 require_admin，未带 token 时 401 属正确行为)
GET /               -> 200
```

回滚：`sed -i 's/^PHP_FALLBACK=.*/PHP_FALLBACK=1/' /etc/fengling/goapi.env` + 恢复
`/opt/fengling/goapi.bak.20261030-005502` + `systemctl start php-fpm-85`。
