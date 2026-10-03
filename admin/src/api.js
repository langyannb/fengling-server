import { Message } from '@arco-design/web-vue'

const TOKEN_KEY = 'fl_token'
const USER_KEY = 'fl_user'

/** 登录态: 仍用原来的 localStorage 键, 保证与旧后台/其它标签页互通 */
export const auth = {
  get token() { return localStorage.getItem(TOKEN_KEY) || '' },
  set token(v) { v ? localStorage.setItem(TOKEN_KEY, v) : localStorage.removeItem(TOKEN_KEY) },
  get user() { return localStorage.getItem(USER_KEY) || '' },
  set user(v) { v ? localStorage.setItem(USER_KEY, v) : localStorage.removeItem(USER_KEY) },
}

/**
 * 后端入口自适应:
 *  - 后台放在 /admin/ 子目录 -> ../api.php
 *  - 后台与 api.php 同目录  -> api.php
 * 也可用构建变量 VITE_API_BASE 强制指定
 */
export const API_BASE =
  import.meta.env.VITE_API_BASE ||
  (location.pathname.includes('/admin/') ? '../api.php' : 'api.php')

function onUnauthorized() {
  auth.token = ''
  auth.user = ''
  Message.warning('登录已失效, 请重新登录')
  if (!location.hash.startsWith('#/login')) location.hash = '#/login'
}

/** 统一请求: 返回 { code, msg, data } —— 与原后台 api() 行为一致 */
export async function api(action, params = {}, method = 'GET') {
  let url = `${API_BASE}?action=${encodeURIComponent(action)}`
  const opt = { method, headers: { Authorization: 'Bearer ' + auth.token } }
  if (method === 'POST') {
    opt.headers['Content-Type'] = 'application/json'
    opt.body = JSON.stringify(params)
  } else {
    // GET 加时间戳, 防 HTTP 强缓存导致改完刷新还是旧值
    url += '&_=' + Date.now()
    const qs = new URLSearchParams(
      Object.entries(params).filter(([, v]) => v !== undefined && v !== null)
    ).toString()
    if (qs) url += '&' + qs
  }
  let res
  try {
    res = await fetch(url, opt)
  } catch (e) {
    Message.error('网络错误: ' + e.message)
    return { code: -1, msg: '网络错误' }
  }
  let body
  try { body = await res.json() } catch {
    const snippet = (await res.text().catch(() => '')).replace(/\s+/g, ' ').slice(0, 140)
    body = { code: -1, msg: `响应解析失败 (HTTP ${res.status})` + (snippet ? ': ' + snippet : '') }
  }
  if (res.status === 401 || body.code === 401) { onUnauthorized(); return { code: 401, msg: '未登录' } }
  return body
}

/** 文件上传 (FormData): action = 'upload'(图片) | 'upload_apk'(安装包) */
export async function uploadFile(action, file, onProgress) {
  return new Promise((resolve) => {
    const fd = new FormData()
    fd.append('file', file)
    const xhr = new XMLHttpRequest()
    xhr.open('POST', `${API_BASE}?action=${action}`)
    xhr.setRequestHeader('Authorization', 'Bearer ' + auth.token)
    if (onProgress) xhr.upload.onprogress = (e) => e.lengthComputable && onProgress(Math.round(e.loaded * 100 / e.total))
    xhr.onload = () => {
      let r = { code: -1, msg: '响应解析失败' }
      try { r = JSON.parse(xhr.responseText) } catch {
        const snippet = (xhr.responseText || '').replace(/\s+/g, ' ').slice(0, 140)
        r = { code: -1, msg: `上传失败 (HTTP ${xhr.status})` + (snippet ? ': ' + snippet : '') }
      }
      if (xhr.status === 401 || r.code === 401) onUnauthorized()
      resolve(r)
    }
    xhr.onerror = () => resolve({ code: -1, msg: '上传失败' })
    xhr.send(fd)
  })
}

/** 表格分页/搜索通用的小工具 */
export function pickList(res) {
  if (!res || res.code !== 0) { if (res && res.code !== 401) Message.error(res?.msg || '加载失败'); return [] }
  const d = res.data
  if (Array.isArray(d)) return d
  // 分页类接口返回 { list, total, page, page_size }, 也要兼容,
  // 否则 v-for 会去遍历对象的 key —— 表现为「只有一个未命名的 XXX」
  if (d && Array.isArray(d.list)) return d.list
  return []
}
