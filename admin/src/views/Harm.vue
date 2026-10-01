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

    <a-table
      :data="harms"
      :loading="loading"
      :pagination="pagination"
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
        <a-table-column title="原因" data-index="content">
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
        <a-table-column title="操作" :width="170" fixed="right">
          <template #cell="{ record }">
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
          </template>
        </a-table-column>
      </template>
      <template #empty>暂无反馈</template>
    </a-table>
  </a-card>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { Message } from '@arco-design/web-vue'
import { api, pickList } from '../api'

const harms = ref([])
const loading = ref(false)
const busyId = ref(null)
const pagination = ref({
  pageSize: 10,
  showTotal: true,
  showPageSize: true,
  pageSizeOptions: [10, 20, 50],
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
</style>
