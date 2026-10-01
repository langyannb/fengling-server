<template>
  <div>
    <a-card title="关于页配置 (官方频道)" class="page-card" :bordered="false">
      <template #extra>
        <a-space>
          <a-button size="small" :loading="loading" @click="load">
            <template #icon><icon-refresh /></template>刷新
          </a-button>
        </a-space>
      </template>

      <a-form :model="form" layout="vertical" auto-label-width>
        <a-row :gutter="16">
          <a-col :xs="24" :md="12">
            <a-form-item field="banner_text" label="横幅标题">
              <a-input v-model="form.banner_text" placeholder="风铃分享库 · 官方频道" allow-clear />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :md="12">
            <a-form-item field="banner_sub" label="横幅副标题">
              <a-input v-model="form.banner_sub" placeholder="最新软件 · 更新通知 · 交流反馈" allow-clear />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :md="12">
            <a-form-item field="qq_group" label="QQ 群号">
              <a-input v-model="form.qq_group" placeholder="740266099" allow-clear />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :md="12">
            <a-form-item field="qq_key" label="QQ 加群 Key (mqqapi 用)">
              <a-input v-model="form.qq_key" placeholder="可选" allow-clear />
            </a-form-item>
          </a-col>
          <a-col :xs="24">
            <a-form-item field="qq_url" label="QQ 加群网页链接">
              <a-input v-model="form.qq_url" placeholder="http://qm.qq.com/cgi-bin/qm/qr?..." allow-clear />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :md="12">
            <a-form-item field="website" label="官网链接">
              <a-input v-model="form.website" placeholder="https://..." allow-clear />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :md="12">
            <a-form-item field="github" label="GitHub 链接">
              <a-input v-model="form.github" placeholder="https://github.com/..." allow-clear />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :md="12">
            <a-form-item field="feedback" label="反馈链接">
              <a-input v-model="form.feedback" placeholder="https://..." allow-clear />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :md="12">
            <a-form-item field="donate" label="捐赠链接">
              <a-input v-model="form.donate" placeholder="https://..." allow-clear />
            </a-form-item>
          </a-col>
        </a-row>

        <a-space>
          <a-button type="primary" :loading="saving" @click="save">保存配置</a-button>
          <a-button :loading="loading" @click="load">重置</a-button>
        </a-space>
      </a-form>
    </a-card>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { Message } from '@arco-design/web-vue'
import { api } from '../api'

const loading = ref(false)
const saving = ref(false)

const form = reactive({
  banner_text: '风铃分享库 · 官方频道',
  banner_sub: '最新软件 · 更新通知 · 交流反馈',
  qq_group: '',
  qq_key: '',
  qq_url: '',
  website: '',
  github: '',
  feedback: '',
  donate: '',
})

async function load() {
  loading.value = true
  try {
    const r = await api('about_config_get')
    if (r.code === 0 && r.data) {
      Object.assign(form, {
        banner_text: r.data.banner_text || '',
        banner_sub: r.data.banner_sub || '',
        qq_group: r.data.qq_group || '',
        qq_key: r.data.qq_key || '',
        qq_url: r.data.qq_url || '',
        website: r.data.website || '',
        github: r.data.github || '',
        feedback: r.data.feedback || '',
        donate: r.data.donate || '',
      })
    } else if (r.code !== 401) {
      Message.error(r.msg || '关于配置加载失败')
    }
  } catch (e) {
    Message.error('关于配置加载失败')
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  try {
    const r = await api('about_config_set', {
      qq_group: form.qq_group,
      qq_key: form.qq_key,
      qq_url: form.qq_url,
      website: form.website,
      github: form.github,
      feedback: form.feedback,
      donate: form.donate,
      banner_text: form.banner_text,
      banner_sub: form.banner_sub,
    }, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error('保存失败: ' + (r.msg || '未知错误'))
      return
    }
    Message.success('关于配置已保存')
    await load()
  } catch (e) {
    Message.error('保存失败: ' + (e.message || '未知错误'))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.page-card { margin-bottom: 16px; }
</style>
