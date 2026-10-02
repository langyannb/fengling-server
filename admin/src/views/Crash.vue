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

    <!-- ===== 手机端: 卡片列表 (桌面端渲染路径完全不变) ===== -->
    <div v-if="isMobile">
      <a-spin :loading="loading" style="width: 100%">
        <div class="m-cards">
          <div v-for="record in pagedCrashes" :key="record.id" class="m-card">
            <div class="m-card-head">
              <div class="m-card-title">{{ record.device || '未知设备' }}</div>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">机型 / 版本</span>
              <span class="m-card-value">
                Android {{ record.android_version || '?' }} · v{{ record.app_version || '?' }}
              </span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">时间</span>
              <span class="m-card-value">{{ record.created_at || '—' }}</span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">堆栈</span>
              <span class="m-card-value">{{ record.stack ? lineCount(record.stack) + ' 行' : '无堆栈' }}</span>
            </div>
            <!-- 堆栈整块等宽展示, 不放进 .m-card-value, 保留换行与折行 -->
            <pre v-if="record.stack" class="m-stack">{{ record.stack }}</pre>
          </div>
        </div>
      </a-spin>
      <a-empty v-if="!loading && !crashes.length" />
      <a-pagination
        v-model:current="pagination.current"
        v-model:page-size="pagination.pageSize"
        :total="crashes.length"
        :show-total="true"
        :show-page-size="true"
        :page-size-options="pagination.pageSizeOptions"
        class="m-pager"
      />
    </div>

    <!-- ===== 桌面端: 原表格 (一字不改) ===== -->
    <a-table
      v-else
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
import { computed, onMounted, ref } from 'vue'
import { Message } from '@arco-design/web-vue'
import { api, pickList } from '../api'
// 响应式: 手机端卡片列表 / 桌面端表格横向滚动 (tableScroll 返回 computed)
import { isMobile, tableScroll } from '../composables/useResponsive'

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

// 手机端卡片: 手动分页切片 (与桌面端共用同一 pagination)
const pagedCrashes = computed(() => {
  const size = Number(pagination.value.pageSize) || 10
  const cur = Number(pagination.value.current) || 1
  const start = (cur - 1) * size
  return crashes.value.slice(start, start + size)
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

/* ---- 手机卡片: 堆栈等宽块 ---- */
.m-stack {
  margin: 8px 0 0;
  padding: 8px 10px;
  max-height: 220px;
  overflow: auto;
  -webkit-overflow-scrolling: touch;
  background: var(--color-fill-1);
  border-radius: 8px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-all;
}
.m-pager { justify-content: flex-end; margin-top: 12px; }
</style>
