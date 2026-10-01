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
      :scroll="scroll"
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
        <!-- 堆栈列给足宽度，手机端靠横向滚动查看，不压缩其它列 -->
        <a-table-column title="堆栈内容" :width="420">
          <template #cell="{ record }">
            <div v-if="record.stack" class="stack-box">
              <div class="sub">堆栈 {{ lineCount(record.stack) }} 行</div>
              <a-typography-paragraph
                class="stack"
                :ellipsis="{ rows: 3, expandable: true, suffix: '展开', showTooltip: false }"
              >
                {{ record.stack }}
              </a-typography-paragraph>
            </div>
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
// 响应式：手机端表格横向滚动（tableScroll 返回 computed）
import { tableScroll } from '../composables/useResponsive'

const crashes = ref([])
const loading = ref(false)
const pagination = ref({
  pageSize: 10,
  showTotal: true,
  showPageSize: true,
  pageSizeOptions: [10, 20, 50],
})

// 时间 170 + 机型 250 + 堆栈 420 = 840，手机端由 tableScroll 抬到至少 720
const scroll = tableScroll(840)

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
.stack-box { margin: 0; }
.stack {
  margin: 4px 0 0;
  font-size: 11px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-all;
}
.stack :deep(.arco-typography) { margin-bottom: 0; }

/* 手机端：堆栈容器限高 + 可滚动，超长堆栈不撑破页面 */
@media (max-width: 820px) {
  .stack-box {
    max-height: 200px;
    overflow-x: auto;
    overflow-y: auto;
    -webkit-overflow-scrolling: touch;
    padding-right: 2px;
  }
  .stack {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 11px;
  }
}
</style>
