<template>
  <div class="login-page">
    <div class="glow glow-a" />
    <div class="glow glow-b" />
    <a-card class="login-card" :bordered="false">
      <div class="brand">
        <div class="brand-mark">风</div>
        <div>
          <div class="brand-title">风铃分享库</div>
          <div class="brand-sub">管理后台 · Admin</div>
        </div>
      </div>
      <a-form :model="form" layout="vertical" @submit-success="login">
        <a-form-item field="username" hide-label>
          <a-input v-model="form.username" size="large" placeholder="用户名" allow-clear @press-enter="login">
            <template #prefix><icon-user /></template>
          </a-input>
        </a-form-item>
        <a-form-item field="password" hide-label>
          <a-input-password v-model="form.password" size="large" placeholder="密码" @press-enter="login">
            <template #prefix><icon-lock /></template>
          </a-input-password>
        </a-form-item>
        <a-alert v-if="err" type="error" class="err">{{ err }}</a-alert>
        <a-button type="primary" long size="large" :loading="loading" @click="login">登 录</a-button>
      </a-form>
      <div class="tip">仅限管理员登录 · 请勿泄露账号</div>
    </a-card>
  </div>
</template>

<script setup>
import { reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Message } from '@arco-design/web-vue'
import { api, auth } from '../api'

const router = useRouter()
const form = reactive({ username: '', password: '' })
const err = ref('')
const loading = ref(false)

async function login() {
  err.value = ''
  if (!form.username || !form.password) { err.value = '请输入用户名和密码'; return }
  loading.value = true
  const r = await api('login', { username: form.username, password: form.password }, 'POST')
  loading.value = false
  if (r.code === 0 && r.data && r.data.token) {
    auth.token = r.data.token
    auth.user = (r.data.user && r.data.user.username) || form.username
    Message.success('登录成功, 欢迎 ' + auth.user)
    router.push({ name: 'stats' })
  } else if (r.code !== 401) {
    err.value = r.msg || '登录失败'
  }
}
</script>

<style scoped>
.login-page {
  position: relative;
  min-height: 100vh;
  min-height: 100dvh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
  overflow: hidden;
  background: linear-gradient(160deg, #eef4ff 0%, #f7f8fa 45%, #eafaf3 100%);
}
/* 背景光斑, 纯装饰 */
.glow {
  position: absolute;
  width: 340px;
  height: 340px;
  border-radius: 50%;
  filter: blur(64px);
  opacity: 0.55;
  pointer-events: none;
}
.glow-a { background: rgba(22, 93, 255, 0.30); top: -90px; left: -70px; }
.glow-b { background: rgba(0, 180, 42, 0.22); bottom: -110px; right: -80px; }

.login-card {
  position: relative;
  z-index: 1;
  width: min(380px, 92vw);
  border-radius: 16px;
  box-shadow: 0 12px 36px rgba(21, 45, 90, 0.10);
}
.brand {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 22px;
}
.brand-mark {
  width: 46px;
  height: 46px;
  border-radius: 13px;
  background: linear-gradient(135deg, rgb(var(--primary-5)), rgb(var(--primary-7)));
  color: #fff;
  font-size: 21px;
  font-weight: 700;
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 auto;
  box-shadow: 0 4px 12px rgba(var(--primary-6), 0.32);
}
.brand-title { font-size: 18px; font-weight: 600; }
.brand-sub { font-size: 12px; color: var(--color-text-3); }
.err { margin-bottom: 12px; }
.tip {
  margin-top: 16px;
  text-align: center;
  font-size: 12px;
  color: var(--color-text-3);
}

@media (max-width: 820px) {
  .login-page {
    align-items: flex-start;
    padding: 16px;
    padding-top: 10vh;
  }
  .login-card { border-radius: 14px; }
  .login-card :deep(.arco-card-body) { padding: 20px 18px; }
}
</style>
