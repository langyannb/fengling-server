<template>
  <div>
    <a-card title="版本发布" class="page-card" :bordered="false">
      <template #extra>
        <a-button size="small" :loading="loading" @click="loadVersion">
          <template #icon><icon-refresh /></template>重置
        </a-button>
      </template>

      <a-form :model="verForm" layout="vertical" :style="{ maxWidth: '720px' }">
        <a-form-item field="version" label="版本号" required>
          <a-input v-model="verForm.version" placeholder="1.0.1" allow-clear />
        </a-form-item>

        <a-form-item label="APK 文件 (上传, 或下方填外链)">
          <div class="upload-box" @click="pickApk">
            <template v-if="!verForm.url">
              <span class="plus">+</span>
              <span>上传 APK</span>
            </template>
            <template v-else>
              <span class="ok">✅</span>
              <span class="url-text">{{ verForm.url }}</span>
            </template>
          </div>
          <input ref="apkInput" type="file" accept=".apk" class="hidden-input" @change="onApkChange" />
          <div v-if="uploading" class="up-tip">
            <span>上传中...</span>
            <a-progress :percent="uploadPercent / 100" :show-text="false" size="small" />
            <span class="pct">{{ uploadPercent }}%</span>
          </div>
          <div class="tip">仅支持 .apk，单文件不超过 200MB</div>
        </a-form-item>

        <a-form-item field="url" label="外链下载地址 (不传 APK 时填, 如网盘/网页链接)">
          <a-input v-model="verForm.url" placeholder="https://..." allow-clear />
        </a-form-item>

        <a-form-item field="update_log" label="更新日志">
          <a-textarea v-model="verForm.update_log" placeholder="更新内容，每行一条" :auto-size="{ minRows: 4, maxRows: 10 }" />
        </a-form-item>

        <a-row :gutter="16">
          <a-col :xs="24" :md="12">
            <a-form-item field="update_mode" label="更新方式">
              <a-select v-model="verForm.update_mode" placeholder="请选择更新方式">
                <a-option value="internal">内置更新 (App 内置浏览器)</a-option>
                <a-option value="external">外置更新 (系统浏览器)</a-option>
              </a-select>
            </a-form-item>
          </a-col>
          <a-col :xs="24" :md="12">
            <a-form-item field="force_update" label="强制更新">
              <a-select v-model="verForm.force_update" placeholder="请选择">
                <a-option :value="1">🔒 强制 (用户必须更新)</a-option>
                <a-option :value="0">非强制 (可稍后)</a-option>
              </a-select>
            </a-form-item>
          </a-col>
        </a-row>

        <a-row :gutter="16">
          <a-col :xs="24" :md="12">
            <a-form-item field="size_mb" label="APK 大小 (MB)">
              <a-input-number v-model="verForm.size_mb" :min="0" :step="0.1" :precision="1" placeholder="12.5" />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :md="12">
            <a-form-item field="release_date" label="发布日期">
              <a-date-picker v-model="verForm.release_date" value-format="YYYY-MM-DD" placeholder="请选择发布日期" style="width: 100%" />
            </a-form-item>
          </a-col>
        </a-row>

        <a-form-item>
          <a-space>
            <a-button type="primary" :loading="saving" @click="saveVersion">发布版本</a-button>
            <a-button @click="loadVersion">重置</a-button>
          </a-space>
        </a-form-item>
      </a-form>
    </a-card>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { Message } from '@arco-design/web-vue'
import { api, uploadFile } from '../api'

const MAX_APK_SIZE = 200 * 1024 * 1024 // 200MB

const apkInput = ref(null)
const loading = ref(false)
const saving = ref(false)
const uploading = ref(false)
const uploadPercent = ref(0)

const defaultForm = () => ({
  version: '1.0.0',
  url: '',
  update_log: '',
  update_mode: 'internal',
  force_update: 0,
  size_mb: 0,
  release_date: '',
})

const verForm = ref(defaultForm())

async function loadVersion() {
  loading.value = true
  try {
    const r = await api('version')
    if (r.code === 0 && r.data) {
      verForm.value = {
        version: r.data.version || '1.0.0',
        url: r.data.url || '',
        update_log: r.data.update_log || '',
        update_mode: r.data.update_mode || 'internal',
        force_update: Number(r.data.force_update) || 0,
        size_mb: Number(r.data.size_mb) || 0,
        release_date: r.data.release_date || '',
      }
    } else if (r.code !== 401) {
      Message.error('版本信息加载失败' + (r.msg ? ': ' + r.msg : ''))
    }
  } catch (e) {
    Message.error('版本信息加载失败')
  } finally {
    loading.value = false
  }
}

function pickApk() {
  if (uploading.value) return
  apkInput.value && apkInput.value.click()
}

async function onApkChange(e) {
  const file = e.target.files && e.target.files[0]
  e.target.value = ''
  if (!file) return
  if (file.size > MAX_APK_SIZE) {
    Message.warning('APK 超过 200MB')
    return
  }
  uploading.value = true
  uploadPercent.value = 0
  try {
    const res = await uploadFile('upload_apk', file, (p) => { uploadPercent.value = p })
    if (res.code === 0 && res.data) {
      verForm.value.url = res.data.url
      Message.success('APK 上传成功')
    } else if (res.code !== 401) {
      Message.error(res.msg || '上传失败')
    }
  } catch (err) {
    Message.error('上传失败')
  } finally {
    uploading.value = false
  }
}

async function saveVersion() {
  const f = verForm.value
  if (!f.version) { Message.warning('请输入版本号'); return }
  if (!f.url) { Message.warning('请上传 APK 或填写外链下载地址'); return }
  saving.value = true
  try {
    const r = await api('version_update', {
      version: f.version,
      url: f.url,
      update_log: f.update_log,
      update_mode: f.update_mode || 'internal',
      force_update: Number(f.force_update) || 0,
      size_mb: Number(f.size_mb) || 0,
      release_date: f.release_date || '',
    }, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error('发布失败: ' + (r.msg || '未知错误'))
      return
    }
    Message.success('版本发布成功: v' + f.version)
    loadVersion()
  } finally {
    saving.value = false
  }
}

onMounted(loadVersion)
</script>

<style scoped>
.page-card { margin-bottom: 16px; }
.upload-box {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  width: 100%;
  min-height: 96px;
  padding: 12px;
  border: 1px dashed var(--color-border-2);
  border-radius: 6px;
  background: var(--color-fill-1);
  cursor: pointer;
  transition: border-color 0.15s, background 0.15s;
  text-align: center;
}
.upload-box:hover { border-color: rgb(var(--primary-6)); background: rgb(var(--primary-1)); }
.upload-box .plus { font-size: 24px; color: rgb(var(--primary-6)); }
.upload-box .ok { font-size: 20px; }
.upload-box .url-text { word-break: break-all; font-size: 13px; color: var(--color-text-2); }
.hidden-input { display: none; }
.up-tip {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
  font-size: 13px;
  color: var(--color-text-2);
}
.up-tip .arco-progress { flex: 1; }
.up-tip .pct { width: 42px; text-align: right; }
.tip { margin-top: 6px; font-size: 12px; color: var(--color-text-3); }
</style>
