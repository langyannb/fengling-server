<template>
  <div class="page-card">
    <a-card :bordered="false" title="UC 网盘账号">
      <a-alert type="info" style="margin-bottom: 16px">
        UC 网盘的分享链接必须用你自己的账号登录才能解析（官方限制，第三方免费接口一律不支持 UC）。
        扫码登录一次即可长期使用，Cookie 只保存在你自己的服务器上（uc_auth.php）。
      </a-alert>

      <a-descriptions :column="isMobile ? 1 : 2" bordered size="large">
        <a-descriptions-item label="登录状态">
          <a-tag :color="st.logged_in ? 'green' : 'gray'">{{ st.logged_in ? '已登录' : '未登录' }}</a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="账号昵称">{{ st.nickname || '—' }}</a-descriptions-item>
        <a-descriptions-item label="登录时间">{{ st.login_time ? fmtTime(st.login_time) : '—' }}</a-descriptions-item>
        <a-descriptions-item label="凭据大小">{{ st.cookie_len ? st.cookie_len + ' 字节' : '—' }}</a-descriptions-item>
      </a-descriptions>

      <a-space style="margin-top: 16px" wrap>
        <a-button type="primary" :loading="loading" @click="startQr">扫码登录</a-button>
        <a-button @click="loadStatus">刷新状态</a-button>
        <a-popconfirm content="确定要退出 UC 登录吗？" @ok="logout">
          <a-button status="danger" :disabled="!st.logged_in">退出登录</a-button>
        </a-popconfirm>
      </a-space>

      <div v-if="qrImg" class="qr-box">
        <img :src="qrImg" alt="UC 登录二维码" />
        <div class="qr-tip">{{ qrTip }}</div>
        <a-button size="small" type="text" @click="stopQr">收起二维码</a-button>
      </div>

      <a-divider>方式二：手工粘贴 Cookie</a-divider>
      <a-textarea
        v-model="cookieText"
        placeholder="浏览器登录 drive.uc.cn 后，F12 控制台输入 document.cookie，把整串粘到这里"
        :auto-size="{ minRows: 3, maxRows: 6 }"
      />
      <a-button style="margin-top: 10px" :loading="saving" @click="saveCookie">保存并验证</a-button>

      <a-divider>说明</a-divider>
      <ul class="tips">
        <li>扫码入口在手机 UC 浏览器或「UC 网盘」App 的「扫一扫」。</li>
        <li>二维码有效期约 10 分钟，过期后点「扫码登录」重新生成。</li>
        <li>Cookie 失效时（解析报「未登录」）重扫一次即可。</li>
        <li>新增软件时，在弹窗顶部粘贴 UC 分享链接就能自动填名称 / 版本 / 大小 / 文件清单。</li>
      </ul>
    </a-card>
  </div>
</template>

<script setup>
import { ref, reactive, onMounted, onBeforeUnmount } from 'vue'
import { Message } from '@arco-design/web-vue'
import QRCode from 'qrcode'
import { api } from '../api'
import { isMobile } from '../composables/useResponsive'

const st = reactive({ logged_in: false, nickname: '', login_time: 0, cookie_len: 0 })
const loading = ref(false)
const saving = ref(false)
const qrImg = ref('')
const qrTip = ref('')
const cookieText = ref('')
let pollTimer = null
let qrToken = ''
let tick = 0

function fmtTime(t) {
  try { return new Date(t * 1000).toLocaleString('zh-CN', { hour12: false }) } catch { return '—' }
}

async function loadStatus() {
  const r = await api('uc_status')
  if (r.code === 0) Object.assign(st, { logged_in: false, nickname: '', login_time: 0, cookie_len: 0 }, r.data || {})
}

function stopQr() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null }
  qrImg.value = ''
  qrToken = ''
  tick = 0
}

async function startQr() {
  stopQr()
  loading.value = true
  const r = await api('uc_qr_create', {}, 'POST')
  loading.value = false
  if (r.code !== 0) { Message.error(r.msg || '获取二维码失败'); return }
  qrToken = r.data.token
  try {
    qrImg.value = await QRCode.toDataURL(r.data.qr_url, { width: 260, margin: 1 })
  } catch (e) {
    Message.error('二维码渲染失败: ' + e.message)
    return
  }
  qrTip.value = '请用手机 UC 浏览器 / UC 网盘 App 扫码，并在手机上确认登录'
  pollTimer = setInterval(poll, 2500)
}

async function poll() {
  if (!qrToken) return
  tick++
  if (tick > 240) { qrTip.value = '二维码已过期，请重新点击「扫码登录」'; stopQr(); return }
  const r = await api('uc_qr_poll', { token: qrToken }, 'POST')
  if (r.code !== 0) { qrTip.value = r.msg || '轮询失败'; return }
  const s = r.data || {}
  if (s.state === 'scanned') qrTip.value = '已扫码，请在手机上点击确认登录'
  else if (s.state === 'ok') {
    Message.success('UC 登录成功' + (s.nickname ? '：' + s.nickname : ''))
    stopQr()
    loadStatus()
  }
}

async function logout() {
  const r = await api('uc_logout', {}, 'POST')
  if (r.code === 0) { Message.success('已退出 UC 登录'); stopQr(); loadStatus() }
  else Message.error(r.msg || '退出失败')
}

async function saveCookie() {
  if (!cookieText.value.trim()) { Message.warning('请先粘贴 Cookie'); return }
  saving.value = true
  const r = await api('uc_cookie_set', { cookie: cookieText.value.trim() }, 'POST')
  saving.value = false
  if (r.code !== 0) { Message.error(r.msg || '保存失败'); return }
  if (r.data && r.data.verified) Message.success('Cookie 有效，已保存')
  else Message.warning('已保存，但验证未通过（可能已过期）')
  cookieText.value = ''
  loadStatus()
}

onMounted(loadStatus)
onBeforeUnmount(stopQr)
</script>

<style scoped>
.qr-box {
  margin-top: 18px;
  padding: 16px;
  border: 1px solid var(--color-border-2);
  border-radius: 12px;
  background: var(--color-bg-2);
  text-align: center;
  max-width: 340px;
}
.qr-box img { width: 260px; height: 260px; max-width: 100%; display: block; margin: 0 auto 10px; }
.qr-tip { font-size: 13px; color: var(--color-text-2); margin-bottom: 8px; line-height: 1.6; }
.tips { margin: 0; padding-left: 20px; color: var(--color-text-2); font-size: 13px; line-height: 1.9; }
</style>
