<template>
  <a-card class="page-card" :bordered="false">
    <template #title>反馈和谐</template>
    <template #extra>
      <a-button size="small" :loading="loading" @click="load">
        <template #icon><icon-refresh /></template>刷新
      </a-button>
    </template>

    <a-alert class="tip" type="info" :show-icon="false">
      App 端用户提交的「哪里被和谐了」反馈（软件 / 说明 / IP / 联系方式），处理完点「已处理」。
    </a-alert>

    <!-- ===== 手机端: 卡片列表 (桌面端渲染路径完全不变) ===== -->
    <div v-if="isMobile">
      <a-spin :loading="loading" style="width: 100%">
        <div class="m-cards">
          <div v-for="record in pagedHarms" :key="record.id" class="m-card">
            <div class="m-card-head">
              <div class="m-card-title">{{ record.app_name || '未知软件' }}</div>
              <span class="m-card-sub">
                <a-tag v-if="record.status == 1" color="green">已处理</a-tag>
                <a-tag v-else color="orange">待处理</a-tag>
              </span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">软件</span>
              <span class="m-card-value">{{ record.app_name || '未知软件' }}</span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">用户 QQ</span>
              <span class="m-card-value">
                <span v-if="record.contact">{{ record.contact }}</span>
                <span v-else class="sub">—</span>
              </span>
            </div>
            <div class="m-card-row row-block">
              <span class="m-card-label">原因</span>
              <span class="m-card-value">
                <span v-if="record.content" class="content">{{ record.content }}</span>
                <span v-else class="sub">—</span>
              </span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">IP</span>
              <span class="m-card-value"><span class="sub">{{ record.ip || '?' }}</span></span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">时间</span>
              <span class="m-card-value">{{ record.created_at || '—' }}</span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">状态</span>
              <span class="m-card-value">
                <a-tag v-if="record.status == 1" color="green">已处理</a-tag>
                <a-tag v-else color="orange">待处理</a-tag>
              </span>
            </div>
            <div class="m-card-actions">
              <a-button
                v-if="record.status == 0"
                type="text"
                size="small"
                :loading="busyId === record.id"
                @click="markHarm(record.id, 1)"
              >✅ 已处理</a-button>
              <a-button
                v-else
                type="text"
                size="small"
                :loading="busyId === record.id"
                @click="markHarm(record.id, 0)"
              >↩ 重开</a-button>
              <a-popconfirm content="确定删除这条反馈吗？" @ok="deleteHarm(record.id)">
                <a-button type="text" status="danger" size="small">🗑 删除</a-button>
              </a-popconfirm>
            </div>
          </div>
        </div>
      </a-spin>
      <a-empty v-if="!loading && !harms.length" />
      <a-pagination
        v-model:current="pagination.current"
        v-model:page-size="pagination.pageSize"
        :total="harms.length"
        :show-total="true"
        :show-page-size="true"
        :page-size-options="pagination.pageSizeOptions"
        class="m-pager"
      />
    </div>

    <!-- ===== 桌面端: 原表格 (一字不改) ===== -->
    <a-table
      v-else
      :data="harms"
      :loading="loading"
      :pagination="pagination"
      :scroll="scroll"
      row-key="id"
      size="small"
    >
      <template #columns>
        <a-table-column title="软件" :width="180">
          <template #cell="{ record }">
            {{ record.app_name || '未知软件' }}
          </template>
        </a-table-column>
        <a-table-column title="用户 QQ" :width="150">
          <template #cell="{ record }">
            <span v-if="record.contact">{{ record.contact }}</span>
            <span v-else class="sub">—</span>
          </template>
        </a-table-column>
        <!-- 原因列给足宽度，长文本靠 ellipsis 展开，不挤压其它列 -->
        <a-table-column title="原因" data-index="content" :width="320">
          <template #cell="{ record }">
            <a-typography-paragraph
              v-if="record.content"
              class="content"
              :ellipsis="{ rows: 2, expandable: true, suffix: '展开', showTooltip: false }"
            >
              {{ record.content }}
            </a-typography-paragraph>
            <span v-else class="sub">—</span>
          </template>
        </a-table-column>
        <a-table-column title="IP" :width="140">
          <template #cell="{ record }">
            <span class="sub">{{ record.ip || '?' }}</span>
          </template>
        </a-table-column>
        <a-table-column title="时间" data-index="created_at" :width="170" />
        <a-table-column title="状态" :width="90">
          <template #cell="{ record }">
            <a-tag v-if="record.status == 1" color="green">已处理</a-tag>
            <a-tag v-else color="orange">待处理</a-tag>
          </template>
        </a-table-column>
        <!-- 操作列固定右侧，手机端横滑时按钮始终可见且不换行 -->
        <a-table-column title="操作" :width="170" fixed="right">
          <template #cell="{ record }">
            <a-space :size="2" :wrap="false">
              <a-button
                v-if="record.status == 0"
                type="text"
                size="small"
                :loading="busyId === record.id"
                @click="markHarm(record.id, 1)"
              >✅ 已处理</a-button>
              <a-button
                v-else
                type="text"
                size="small"
                :loading="busyId === record.id"
                @click="markHarm(record.id, 0)"
              >↩ 重开</a-button>
              <a-popconfirm content="确定删除这条反馈吗？" @ok="deleteHarm(record.id)">
                <a-button type="text" status="danger" size="small">🗑 删除</a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </a-table-column>
      </template>
      <template #empty>暂无反馈</template>
    </a-table>
  </a-card>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { Message } from '@arco-design/web-vue'
import { api, pickList } from '../api'
// 响应式: 手机端卡片列表 / 桌面端表格横向滚动 (tableScroll 返回 computed)
import { isMobile, tableScroll } from '../composables/useResponsive'

const harms = ref([])
const loading = ref(false)
const busyId = ref(null)
const pagination = ref({
  pageSize: 10,
  showTotal: true,
  showPageSize: true,
  pageSizeOptions: [10, 20, 50],
})

// 软件 180 + QQ 150 + 原因 320 + IP 140 + 时间 170 + 状态 90 + 操作 170 = 1220
const scroll = tableScroll(1220)

// 手机端卡片: 手动分页切片 (与桌面端共用同一 pagination)
const pagedHarms = computed(() => {
  const size = Number(pagination.value.pageSize) || 10
  const cur = Number(pagination.value.current) || 1
  const start = (cur - 1) * size
  return harms.value.slice(start, start + size)
})

async function load() {
  loading.value = true
  try {
    const r = await api('harm_reports', {}, 'POST')
    harms.value = pickList(r)
  } catch (e) {
    Message.error('反馈加载失败')
  } finally {
    loading.value = false
  }
}

async function markHarm(id, status) {
  busyId.value = id
  try {
    const r = await api('harm_report_status', { id, status }, 'POST')
    if (r.code !== 0) {
      Message.error(r.msg || '操作失败')
      return
    }
    Message.success(status == 1 ? '已标记为已处理' : '已重新打开')
    await load()
  } catch (e) {
    Message.error('操作失败')
  } finally {
    busyId.value = null
  }
}

async function deleteHarm(id) {
  busyId.value = id
  try {
    const r = await api('harm_report_delete', { id }, 'POST')
    if (r.code !== 0) {
      Message.error(r.msg || '删除失败')
      return
    }
    Message.success('已删除')
    await load()
  } catch (e) {
    Message.error('删除失败')
  } finally {
    busyId.value = null
  }
}

onMounted(load)
</script>

<style scoped>
.tip { margin-bottom: 14px; }
.sub { font-size: 12px; color: var(--color-text-3); }
.content {
  margin: 0;
  font-size: 13px;
  white-space: pre-wrap;
  word-break: break-all;
}
.content :deep(.arco-typography) { margin-bottom: 0; }

/* 手机端：操作按钮不换行、不互相挤压 */
@media (max-width: 820px) {
  :deep(.arco-table-td .arco-space) { flex-wrap: nowrap; }
  :deep(.arco-table-td .arco-space-item) { flex: 0 0 auto; }
}

/* ---- 手机卡片: 原因整块左对齐折行 + 分页右对齐 ---- */
.m-card-row.row-block { flex-direction: column; align-items: stretch; gap: 4px; }
.row-block .m-card-value { text-align: left; }
.m-pager { justify-content: flex-end; margin-top: 12px; }
</style>
