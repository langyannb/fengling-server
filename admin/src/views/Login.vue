<template>
  <div class="login-page">
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
.login-page { height: 100vh; display: flex; align-items: center; justify-content: center; background: linear-gradient(135deg, #eef3ff 0%, #f7f8fa 60%, #eafaf3 100%); }
.login-card { width: 380px; box-shadow: 0 10px 30px rgba(0, 0, 0, .08); border-radius: 14px; }
.brand { display: flex; align-items: center; gap: 12px; margin-bottom: 22px; }
.brand-mark { width: 44px; height: 44px; border-radius: 12px; background: rgb(var(--primary-6)); color: #fff; font-size: 20px; font-weight: 700; display: flex; align-items: center; justify-content: center; }
.brand-title { font-size: 18px; font-weight: 600; }
.brand-sub { font-size: 12px; color: var(--color-text-3); }
.err { margin-bottom: 12px; }
.tip { margin-top: 16px; text-align: center; font-size: 12px; color: var(--color-text-3); }
</style>
