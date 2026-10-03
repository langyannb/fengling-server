<template>
  <a-card :bordered="false" class="page-card">
    <!-- 筛选工具栏: 手机端自动换行、逐行铺满 -->
    <div class="page-toolbar">
      <a-select
        v-model="filterGroup"
        :options="groupOptions"
        :style="isMobile ? 'width:100%' : 'width:200px'"
        placeholder="全部群组"
        allow-clear
        @change="search"
      />
      <a-input
        v-model="kw"
        class="grow"
        placeholder="搜索消息内容 / 发送人"
        allow-clear
        :style="isMobile ? 'width:100%' : 'width:260px'"
        @press-enter="search"
      >
        <template #prefix><icon-search /></template>
      </a-input>
      <div class="toolbar-spacer"></div>
      <a-button :loading="loading" @click="load">
        <template #icon><icon-refresh /></template>刷新
      </a-button>
      <span class="muted">共 {{ total }} 条</span>
    </div>

    <!-- ===== 手机端: 卡片列表 (桌面端渲染路径完全不变) ===== -->
    <template v-if="isMobile">
      <a-spin :loading="loading" style="width: 100%">
        <div class="m-cards">
          <div v-for="record in list" :key="record.id" class="m-card">
            <div class="m-card-head">
              <div class="m-card-title">
                {{ senderName(record) }}
                <a-tag v-if="hasAt(record)" size="small" color="orangered">含 @</a-tag>
                <a-tag v-if="Number(record.is_recalled)" size="small" color="gray">已撤回</a-tag>
              </div>
            </div>

            <div class="m-card-row">
              <span class="m-card-label">群组</span>
              <span class="m-card-value">{{ groupName(record) }}</span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">内容</span>
              <span class="m-card-value">
                <span v-if="Number(record.is_recalled)" class="recalled">已撤回</span>
                <span v-else class="msg-content">{{ record.content || '-' }}</span>
              </span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">时间</span>
              <span class="m-card-value">{{ record.created_at || '-' }}</span>
            </div>

            <div class="m-card-actions">
              <a-button type="text" size="small" status="danger" @click="delMessage(record)">删除</a-button>
            </div>
          </div>
        </div>
      </a-spin>
      <a-empty v-if="!loading && !list.length" description="暂无群消息" />
      <a-pagination
        v-if="total > 0"
        v-model:current="pagination.current"
        v-model:page-size="pagination.pageSize"
        :total="total"
        :show-total="true"
        :show-page-size="true"
        :page-size-options="pagination.pageSizeOptions"
        class="m-pager"
        @change="onPageChange"
        @page-size-change="onPageSizeChange"
      />
    </template>

    <!-- ===== 桌面端: 表格 ===== -->
    <!-- 横向总宽: 80+130+180+330+90+170+100 = 1080 -->
    <a-table
      v-else
      :data="list"
      :loading="loading"
      row-key="id"
      size="small"
      :scroll="scrollX"
      :pagination="pagination"
      @page-change="onPageChange"
      @page-size-change="onPageSizeChange"
    >
      <template #columns>
        <a-table-column title="ID" data-index="id" :width="80" />
        <a-table-column title="群组" :width="130">
          <template #cell="{ record }">{{ groupName(record) }}</template>
        </a-table-column>
        <a-table-column title="发送人" :width="180">
          <template #cell="{ record }">
            <div class="sender">
              <span class="sender-name">{{ record.nickname || '未设置昵称' }}</span>
              <span class="muted">@{{ record.username || ('用户' + record.user_id) }}</span>
            </div>
          </template>
        </a-table-column>
        <a-table-column title="内容" :width="330">
          <template #cell="{ record }">
            <span v-if="Number(record.is_recalled)" class="recalled">已撤回</span>
            <span v-else class="msg-content">{{ record.content || '-' }}</span>
          </template>
        </a-table-column>
        <a-table-column title="是否含 @" :width="90">
          <template #cell="{ record }">
            <a-tag v-if="hasAt(record)" size="small" color="orangered">含 @</a-tag>
            <span v-else class="muted">否</span>
          </template>
        </a-table-column>
        <a-table-column title="时间" data-index="created_at" :width="170" />
        <a-table-column title="操作" :width="100" fixed="right">
          <template #cell="{ record }">
            <a-button type="text" size="small" status="danger" @click="delMessage(record)">删除</a-button>
          </template>
        </a-table-column>
      </template>
      <template #empty>暂无群消息</template>
    </a-table>
  </a-card>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { Message, Modal } from '@arco-design/web-vue'
import { api } from '../api'
import { isMobile, tableScroll } from '../composables/useResponsive'

// 表格横向滚动: 桌面按列宽总和 1080, 手机端至少 720
const scrollX = tableScroll(1080)

const list = ref([])
const total = ref(0)
const kw = ref('')
const filterGroup = ref(null)
const loading = ref(false)
const groups = ref([])

// 分页: 服务端分页, 桌面端表格与手机端卡片共用同一 pagination
const pagination = ref({
  current: 1,
  pageSize: 20,
  total: 0,
  showTotal: true,
  showPageSize: true,
  pageSizeOptions: [10, 20, 50],
})

const groupOptions = computed(() => groups.value.map((g) => ({ label: g.name, value: Number(g.id) })))

/** 群组名: 后端可能返回 group_name, 也可能返回嵌套 group 对象, 都没有就按 id 兜底 */
function groupName(record) {
  if (record.group_name) return record.group_name
  if (record.group && record.group.name) return record.group.name
  const hit = groups.value.find((g) => Number(g.id) === Number(record.group_id))
  return hit ? hit.name : `群组 #${record.group_id}`
}

/** 来源群组下拉: 复用 admin_groups (契约第五节) */
async function loadGroups() {
  const r = await api('admin_groups')
  if (r.code === 0) groups.value = (r.data && r.data.list) || []
}

/**
 * 发送人显示名 (手机端卡片标题用)。
 *
 * 注意: 模板里原来直接调用了 senderName(record), 但脚本里并没有定义这个函数 ——
 * 手机端渲染到这一行就抛错, 整个卡片列表渲染不出来, 表现是「上边显示共 N 条,
 * 下面一片空白, 连删除按钮都没有」。这里把函数补上。
 */
function senderName(record) {
  const nick = String(record.nickname || '').trim()
  const user = String(record.username || '').trim()
  if (nick && user) return `${nick} (@${user})`
  if (nick) return nick
  if (user) return `@${user}`
  return `用户 ${record.user_id || ''}`
}

/** 是否含 @: at_users 为逗号分隔 id 串或 JSON 数组, 空串 / '[]' / '0' 视为没有 */
function hasAt(record) {
  const raw = record.at_users ?? record.at
  if (raw === undefined || raw === null) return false
  const s = String(raw).trim()
  return s !== '' && s !== '[]' && s !== '0'
}

async function load() {
  loading.value = true
  const params = {
    page: pagination.value.current,
    page_size: pagination.value.pageSize,
  }
  if (filterGroup.value) params.group_id = filterGroup.value
  if (kw.value.trim()) params.keyword = kw.value.trim()
  let r
  try {
    r = await api('admin_social_messages', params)
  } finally {
    loading.value = false
  }
  if (r.code !== 0) {
    list.value = []
    total.value = 0
    pagination.value.total = 0
    if (r.code !== 401) Message.error(r.msg || '群消息加载失败')
    return
  }
  const d = r.data || {}
  list.value = d.list || []
  total.value = Number(d.total) || 0
  pagination.value.total = total.value
  if (d.page) pagination.value.current = Number(d.page)
  if (d.page_size) pagination.value.pageSize = Number(d.page_size)
}

/** 筛选条件变化后回到第一页重新查询 */
function search() {
  pagination.value.current = 1
  load()
}

function onPageChange(current) {
  pagination.value.current = current
  load()
}
function onPageSizeChange(pageSize) {
  pagination.value.pageSize = pageSize
  pagination.value.current = 1
  load()
}

/** 删除单条消息: 二次确认 */
function delMessage(record) {
  Modal.warning({
    title: '确认删除这条群消息?',
    content: `删除后不可恢复 (来自「${groupName(record)}」, ${record.created_at || ''})`,
    okText: '删除',
    cancelText: '取消',
    hideCancel: false,
    okButtonProps: { status: 'danger' },
    onOk: async () => {
      const r = await api('admin_social_message_delete', { id: record.id }, 'POST')
      if (r.code !== 0) {
        if (r.code !== 401) Message.error('删除失败: ' + (r.msg || '未知错误'))
        return false
      }
      Message.success('已删除')
      // 删掉当前页最后一条时回退一页
      if (list.value.length === 1 && pagination.value.current > 1) pagination.value.current--
      load()
      return true
    },
  })
}

onMounted(() => {
  loadGroups()
  load()
})
</script>

<style scoped>
.page-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}
.toolbar-spacer { flex: 1 1 auto; min-width: 0; }
.muted { color: var(--color-text-3); font-size: 13px; }
.grow { min-width: 0; }
.m-pager { justify-content: flex-end; margin-top: 12px; }

.sender { display: flex; flex-direction: column; line-height: 1.4; }
.sender-name { font-weight: 600; }
/* 撤回的消息统一灰色显示 */
.recalled { color: var(--color-text-3); font-size: 13px; }
.msg-content { word-break: break-word; }

@media (max-width: 820px) {
  .page-toolbar { flex-direction: column; align-items: stretch; }
  .toolbar-spacer { display: none; }
}
</style>
