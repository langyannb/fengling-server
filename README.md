# 风铃分享库 (flfxk)

软件库 + 网盘推广管理系统。PHP 原生后端 + Vue 3 移动端适配的管理后台。

用户端是 Android App (com.fengling.share)，通过本系统 API 拉取软件数据；
Web 端仅为管理后台，用于维护软件/分类/网盘推广链接和查看统计。

## 在线地址
- 管理后台: http://REDACTED_SERVER_HOST:9845/
- 默认管理员: zjyzjy / (密码见部署备忘, 不写进仓库)

## 技术栈
- 后端: PHP 8 + MySQL (PDO)
- 管理后台: Vue 3 + Vite + Arco Design Vue (源码在 `admin/`, 构建走 GitHub Actions)
- 旧版后台: `admin.html` (单文件, 保留作回退)
- Web 服务器: Nginx + PHP-FPM

## 文件结构
```
├── api.php        # API 入口 (所有接口, action 参数路由)
├── config.php     # 数据库配置
├── admin/         # 管理后台源码 (Vue 3 + Arco Design Vue, Vite 构建)
├── admin.html     # 旧版单文件后台 (回退用)
└── schema.sql     # 数据库表结构 + 默认管理员
```

## API 接口
| action | 方法 | 说明 | 需登录 |
|--------|------|------|--------|
| login | POST | 登录获取 token | - |
| logout | POST | 退出 | - |
| categories | GET | 分类列表 | - |
| category_create / update / delete | POST | 分类管理 | ✅ |
| apps | GET | 软件列表 (category_id/keyword 过滤) | - |
| app_detail | GET | 软件详情 (含网盘链接) | - |
| app_create / update / delete | POST | 软件管理 | ✅ |
| link_create / link_delete | POST | 网盘推广链接管理 | ✅ |
| link_click | POST | 点击下载 (记录统计+返回URL) | - |
| stats | GET | 推广统计 | ✅ |

认证方式: `Authorization: Bearer <token>` (登录后返回)

## 部署
1. 宝塔建库: `flfxk` / `flfxk` / 密码
2. 导入 `schema.sql`
3. 站点目录放本项目文件, PHP 8.5 + Nginx
4. 数据库配置改 `config.php`
