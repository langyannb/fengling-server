<template>
  <a-card class="page-card" :bordered="false">
    <template #title>崩溃日志</template>
    <template #extra>
      <a-button size="small" :loading="loading" @click="load">
        <template #icon><icon-refresh /></template>刷新
      </a-button>
    </template>

    <a-alert class="tip" type="info" :show-icon="false">
      App 端每次启动会把上次崩溃的堆栈自动上传到这里（含设备 / 系统 / App 版本）。
    </a-alert>

    <a-table
      :data="crashes"
      :loading="loading"
      :pagination="pagination"
      row-key="id"
      size="small"
    >
      <template #columns>
        <a-table-column title="时间" data-index="created_at" :width="170" />
        <a-table-column title="机型 / 版本" :width="250">
          <template #cell="{ record }">
            <div class="device">{{ record.device || '未知设备' }}</div>
            <div class="sub">
              Android {{ record.android_version || '?' }} · v{{ record.app_version || '?' }}
            </div>
          </template>
        </a-table-column>
        <a-table-column title="堆栈内容">
          <template #cell="{ record }">
            <template v-if="record.stack">
              <div class="sub">堆栈 {{ lineCount(record.stack) }} 行</div>
              <a-typography-paragraph
                class="stack"
                :ellipsis="{ rows: 3, expandable: true, suffix: '展开', showTooltip: false }"
              >
                {{ record.stack }}
              </a-typography-paragraph>
            </template>
            <span v-else class="sub">无堆栈</span>
          </template>
        </a-table-column>
      </template>
      <template #empty>暂无崩溃上报</template>
    </a-table>
  </a-card>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { Message } from '@arco-design/web-vue'
import { api, pickList } from '../api'

const crashes = ref([])
const loading = ref(false)
const pagination = ref({
  pageSize: 10,
  showTotal: true,
  showPageSize: true,
  pageSizeOptions: [10, 20, 50],
})

function lineCount(s) {
  return String(s || '').split('\n').length
}

async function load() {
  loading.value = true
  try {
    const r = await api('crash_reports', {}, 'POST')
    crashes.value = pickList(r)
  } catch (e) {
    Message.error('崩溃日志加载失败')
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.tip { margin-bottom: 14px; }
.device { font-weight: 600; }
.sub { font-size: 12px; color: var(--color-text-3); }
.stack {
  margin: 4px 0 0;
  font-size: 11px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-all;
}
.stack :deep(.arco-typography) { margin-bottom: 0; }
</style>
