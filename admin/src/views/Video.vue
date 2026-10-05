<template>
  <div class="video-page">
    <a-card :bordered="false" class="page-card">
      <template #title>视频消息</template>
      <a-alert type="info" class="video-alert">
        <div>群聊 / 私聊支持发送短视频；这里控制开关、单条大小、总容量与自动清理。</div>
        <div v-if="serverMax">服务器当前允许的单条上传上限：<b>{{ serverMax }} MB</b>（由 php.ini 的 upload_max_filesize 决定，上面的「单个视频上限」不能超过它）。</div>
      </a-alert>

      <a-spin :loading="loading" style="width: 100%">
        <a-form :model="form" layout="vertical" class="config-form">
          <a-form-item label="视频开关">
            <a-switch v-model="form.enabled" :checked-value="1" :unchecked-value="0">
              <template #checked>开启</template>
              <template #unchecked>关闭</template>
            </a-switch>
            <div class="form-tip">关闭后客户端上传视频会被拒绝，已存在的视频不受影响。</div>
          </a-form-item>

          <a-form-item label="单个视频上限 (MB)">
            <a-input-number v-model="form.max_mb" :min="1" :max="500" :precision="0" style="width: 100%" />
            <div class="form-tip">1 - 500 MB，且不能超过服务器上传上限。</div>
          </a-form-item>

          <a-form-item label="总容量上限 (MB)">
            <a-input-number v-model="form.total_limit_mb" :min="100" :max="100000" :precision="0" style="width: 100%" />
            <div class="form-tip">100 - 100000 MB，且不能小于单个视频上限；存量总字节超过它时自动清理最旧的视频。</div>
          </a-form-item>

          <a-form-item label="保留天数">
            <a-input-number v-model="form.keep_days" :min="0" :max="3650" :precision="0" style="width: 100%" />
            <div class="form-tip">0 = 不按天数清理；大于 0 时，超过天数的视频会被自动清理。</div>
          </a-form-item>

          <a-form-item label="自动清理">
            <a-switch v-model="form.auto_clean" :checked-value="1" :unchecked-value="0">
              <template #checked>开启</template>
              <template #unchecked>关闭</template>
            </a-switch>
            <div class="form-tip">关闭后只统计不删除（下面的手动清理仍然可用）。</div>
          </a-form-item>

          <a-form-item>
            <a-button type="primary" :loading="saving" @click="save">保存</a-button>
            <a-button :loading="loading" style="margin-left: 8px" @click="load">刷新</a-button>
          </a-form-item>
        </a-form>
      </a-spin>
    </a-card>

    <a-card :bordered="false" class="page-card usage-card">
      <template #title>当前占用</template>
      <a-spin :loading="loading" style="width: 100%">
        <div class="usage-grid">
          <div class="usage-item"><span class="k">视频条数</span><span class="v">{{ num(usage.count) }} 条</span></div>
          <div class="usage-item"><span class="k">占用总容量</span><span class="v">{{ totalMb }} MB</span></div>
          <div class="usage-item"><span class="k">最大单条</span><span class="v">{{ maxMb }} MB</span></div>
          <div class="usage-item"><span class="k">最老一条</span><span class="v">{{ usage.oldest_at || '—' }}</span></div>
          <div class="usage-item"><span class="k">已清理条数</span><span class="v">{{ num(usage.cleaned_count) }} 条</span></div>
        </div>

        <a-divider class="usage-divider" />

        <div class="clean-actions">
          <a-button :loading="cleaning === 'over_limit'" @click="confirmClean('over_limit')">一键清理超限</a-button>
          <a-button status="danger" :loading="cleaning === 'all'" @click="confirmClean('all')">清空全部视频</a-button>
        </div>
        <div class="form-tip">
          清理只删 S3 上的视频文件，消息本身保留并追加「[视频已清理]」标记；「一键清理超限」按最旧优先删到总容量不超上限。
        </div>
      </a-spin>
    </a-card>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { Message, Modal } from '@arco-design/web-vue'
import { api } from '../api'

const loading = ref(false)
const saving = ref(false)
const cleaning = ref('')
const serverMax = ref(0)
const usage = ref({})

const form = ref({
  enabled: 1,
  max_mb: 100,
  total_limit_mb: 600,
  keep_days: 0,
  auto_clean: 1,
})

function num(v) {
  const n = Number(v)
  return Number.isFinite(n) ? n : 0
}

const totalMb = computed(() => (num(usage.value.total_bytes) / 1048576).toFixed(2))
const maxMb = computed(() => (num(usage.value.max_size) / 1048576).toFixed(2))

/** 读取配置 + 占用统计 (admin_video_config_get 一次返回两样) */
async function load() {
  loading.value = true
  let r
  try {
    r = await api('admin_video_config_get')
  } finally {
    loading.value = false
  }
  if (r.code !== 0) {
    if (r.code !== 401) Message.error(r.msg || '视频配置加载失败')
    return
  }
  const d = r.data || {}
  form.value = {
    enabled: Number(d.enabled) ? 1 : 0,
    max_mb: num(d.max_mb) || 100,
    total_limit_mb: num(d.total_limit_mb) || 600,
    keep_days: num(d.keep_days),
    auto_clean: Number(d.auto_clean) ? 1 : 0,
  }
  usage.value = d.usage || {}
  serverMax.value = num(d.server_upload_max_mb)
}

async function save() {
  const f = form.value
  saving.value = true
  try {
    const r = await api('admin_video_config_set', {
      enabled: Number(f.enabled) ? 1 : 0,
      max_mb: num(f.max_mb),
      total_limit_mb: num(f.total_limit_mb),
      keep_days: num(f.keep_days),
      auto_clean: Number(f.auto_clean) ? 1 : 0,
    }, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error(r.msg || '保存失败')
      return
    }
    const d = r.data || {}
    usage.value = d.usage || usage.value
    Message.success('已保存')
  } finally {
    saving.value = false
  }
}

/** 危险操作: 先二次确认 (清空全部还要再确认一次), 确认后才调接口 */
function confirmClean(mode) {
  const all = mode === 'all'
  Modal.warning({
    title: all ? '清空全部视频' : '一键清理超限',
    content: all
      ? `将删除全部 ${num(usage.value.count)} 条视频文件，消息内容会保留并标记「[视频已清理]」。此操作不可撤销！`
      : '将按最旧优先删除视频，直到占用总量不超过「总容量上限」。视频文件删除后不可恢复。',
    hideCancel: false,
    okText: all ? '继续' : '确认清理',
    cancelText: '取消',
    onOk: async () => {
      if (all) {
        // 清空是不可逆操作, 再确认一次
        const sure = await new Promise((resolve) => {
          Modal.error({
            title: '再次确认',
            content: '真的要清空全部视频吗？这是不可恢复的操作。',
            okText: '确认清空',
            cancelText: '我放弃了',
            onOk: () => resolve(true),
            onCancel: () => resolve(false),
          })
        })
        if (!sure) return
      }
      await doClean(mode)
    },
  })
}

async function doClean(mode) {
  cleaning.value = mode
  try {
    const r = await api('admin_video_clean', { mode }, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error(r.msg || '清理失败')
      return
    }
    const d = r.data || {}
    usage.value = d.usage || usage.value
    Message.success(`已清理 ${num(d.deleted)} 条，释放 ${num(d.freed_mb)} MB`)
  } finally {
    cleaning.value = ''
  }
}

onMounted(load)
</script>

<style scoped>
.page-card + .page-card { margin-top: 16px; }
.video-alert { margin-bottom: 14px; }
.config-form { max-width: 640px; }
.form-tip { font-size: 12px; color: var(--color-text-3); line-height: 1.6; margin-top: 4px; }

.usage-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 12px;
}
.usage-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 12px 14px;
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
  background: var(--color-fill-1);
}
.usage-item .k { font-size: 12px; color: var(--color-text-3); }
.usage-item .v { font-size: 16px; font-weight: 600; word-break: break-all; }
.usage-divider { margin: 16px 0; }
.clean-actions { display: flex; flex-wrap: wrap; gap: 10px; }

@media (max-width: 820px) {
  .config-form { max-width: 100%; }
  .clean-actions { flex-direction: column; }
}
</style>
