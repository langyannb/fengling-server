<template>
  <div>
    <!-- ============ 上半部分: 通知下发表单 ============ -->
    <a-card :bordered="false" class="page-card" title="通知下发">
      <a-form :model="form" layout="vertical">
        <a-form-item field="title" label="标题 *">
          <a-input v-model="form.title" :max-length="60" placeholder="例如: 版本更新通知" allow-clear />
        </a-form-item>

        <a-row :gutter="16">
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item field="type" label="类型">
              <a-select v-model="form.type" :options="typeOptions" />
            </a-form-item>
          </a-col>
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item field="targetMode" label="目标">
              <a-radio-group v-model="form.targetMode" type="button">
                <a-radio value="all">全体用户</a-radio>
                <a-radio value="user">指定用户</a-radio>
              </a-radio-group>
            </a-form-item>
          </a-col>
        </a-row>

        <a-form-item v-if="form.targetMode === 'user'" field="user_id" label="指定用户 (可搜索昵称 / 用户名)">
          <a-select
            v-model="form.user_id"
            :options="userOptions"
            :loading="userLoading"
            placeholder="输入昵称或用户名搜索"
            allow-search
            allow-clear
            :filter-option="false"
            @search="searchUsers"
            @dropdown-visible-change="onUserDropdown"
          />
        </a-form-item>

        <a-form-item field="link" label="链接 (可选, 点击通知跳转)">
          <a-input v-model="form.link" placeholder="https://... 或 app 内路径" allow-clear />
        </a-form-item>

        <a-form-item field="content" label="内容 *">
          <a-textarea
            v-model="form.content"
            :max-length="500"
            show-word-limit
            :auto-size="{ minRows: 4, maxRows: 8 }"
            placeholder="通知正文, 最多 500 字"
          />
        </a-form-item>

        <a-space>
          <a-button type="primary" :loading="sending" @click="send">
            <template #icon><icon-send /></template>立即发送
          </a-button>
          <a-button @click="resetForm">重置</a-button>
        </a-space>
        <span class="muted send-tip">选「全体用户」时会给每个用户各插入一条通知。</span>
      </a-form>
    </a-card>

    <!-- ============ 下半部分: 已下发通知列表 ============ -->
    <a-card :bordered="false" class="page-card" style="margin-top: 12px">
      <div class="page-toolbar">
        <a-input
          v-model="kw"
          class="grow"
          placeholder="搜索通知标题 / 内容"
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
        <a-button status="danger" @click="clearAll">
          <template #icon><icon-delete /></template>清空全部
        </a-button>
      </div>

      <!-- ===== 手机端: 卡片列表 ===== -->
      <template v-if="isMobile">
        <a-spin :loading="loading" style="width: 100%">
          <div class="m-cards">
            <div v-for="record in list" :key="record.id" class="m-card">
              <div class="m-card-head">
                <div class="m-card-title">
                  {{ record.title || '无标题' }}
                  <a-tag size="small" :color="typeColor(record.type)">{{ typeText(record.type) }}</a-tag>
                </div>
              </div>

              <div class="m-card-row">
                <span class="m-card-label">ID</span>
                <span class="m-card-value">{{ record.id }}</span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">接收人</span>
                <span class="m-card-value">{{ record.user_name || record.user_id || '-' }}</span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">已读</span>
                <span class="m-card-value">
                  <a-tag size="small" :color="Number(record.is_read) ? 'green' : 'gray'">
                    {{ Number(record.is_read) ? '已读' : '未读' }}
                  </a-tag>
                </span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">内容</span>
                <span class="m-card-value msg-content">{{ record.content || '-' }}</span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">时间</span>
                <span class="m-card-value">{{ record.created_at || '-' }}</span>
              </div>

              <div class="m-card-actions">
                <a-button type="text" size="small" status="danger" @click="delOne(record)">删除</a-button>
              </div>
            </div>
          </div>
        </a-spin>
        <a-empty v-if="!loading && !list.length" description="暂无通知" />
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
      <!-- 横向总宽: 80+220+110+140+90+170+100 = 910 -->
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
          <a-table-column title="标题" :width="220">
            <template #cell="{ record }">{{ record.title || '无标题' }}</template>
          </a-table-column>
          <a-table-column title="类型" :width="110">
            <template #cell="{ record }">
              <a-tag size="small" :color="typeColor(record.type)">{{ typeText(record.type) }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="接收人" :width="140">
            <template #cell="{ record }">{{ record.user_name || record.user_id || '-' }}</template>
          </a-table-column>
          <a-table-column title="是否已读" :width="90">
            <template #cell="{ record }">
              <a-tag size="small" :color="Number(record.is_read) ? 'green' : 'gray'">
                {{ Number(record.is_read) ? '已读' : '未读' }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column title="时间" data-index="created_at" :width="170" />
          <a-table-column title="操作" :width="100" fixed="right">
            <template #cell="{ record }">
              <a-button type="text" size="small" status="danger" @click="delOne(record)">删除</a-button>
            </template>
          </a-table-column>
        </template>
        <template #empty>暂无通知</template>
      </a-table>
    </a-card>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { Message, Modal } from '@arco-design/web-vue'
import { api } from '../api'
import { isMobile, tableScroll } from '../composables/useResponsive'

// 表格横向滚动: 桌面按列宽总和 910, 手机端至少 720
const scrollX = tableScroll(910)

const typeOptions = [
  { label: '管理员通知', value: 'admin' },
  { label: '系统通知', value: 'system' },
]

const form = reactive({
  title: '',
  type: 'admin',
  targetMode: 'all',
  user_id: null,
  link: '',
  content: '',
})

const sending = ref(false)
const userLoading = ref(false)
const userOptions = ref([])

/* ---------------- 表单: 指定用户搜索 ---------------- */

/** 远程搜索用户: 复用已有 admin_users(契约外但项目现有), 显示昵称 + 用户名 */
async function searchUsers(kw) {
  userLoading.value = true
  try {
    const params = { page: 1, page_size: 20 }
    if (kw && kw.trim()) params.keyword = kw.trim()
    const r = await api('admin_users', params)
    const rows = (r.code === 0 && r.data && r.data.list) || []
    userOptions.value = rows.map((u) => ({
      label: `${u.nickname || u.username} (@${u.username}, ID ${u.id})`,
      value: Number(u.id),
    }))
  } finally {
    userLoading.value = false
  }
}

function onUserDropdown(visible) {
  if (visible && !userOptions.value.length) searchUsers('')
}

function resetForm() {
  Object.assign(form, { title: '', type: 'admin', targetMode: 'all', user_id: null, link: '', content: '' })
}

async function send() {
  if (!form.title.trim()) { Message.warning('请填写通知标题'); return }
  if (!form.content.trim()) { Message.warning('请填写通知内容'); return }
  if (form.targetMode === 'user' && !form.user_id) { Message.warning('请选择接收用户'); return }

  const payload = {
    title: form.title.trim(),
    content: form.content,
    type: form.type,
    link: form.link || '',
    target: form.targetMode === 'user' ? Number(form.user_id) : 'all',
  }
  sending.value = true
  try {
    const r = await api('admin_notify_send', payload, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error('发送失败: ' + (r.msg || '未知错误'))
      return
    }
    const n = Number((r.data && r.data.count) || 0)
    Message.success(`已发送给 ${n} 位用户`)
    resetForm()
    pagination.value.current = 1
    load()
  } finally {
    sending.value = false
  }
}

/* ---------------- 列表: 已下发通知 ---------------- */

const list = ref([])
const total = ref(0)
const kw = ref('')
const loading = ref(false)

// 分页: 服务端分页, 桌面端表格与手机端卡片共用同一 pagination
const pagination = ref({
  current: 1,
  pageSize: 20,
  total: 0,
  showTotal: true,
  showPageSize: true,
  pageSizeOptions: [10, 20, 50],
})

function typeText(t) {
  if (t === 'system') return '系统通知'
  if (t === 'social') return '社交'
  return '管理员通知'
}
function typeColor(t) {
  if (t === 'system') return 'arcoblue'
  if (t === 'social') return 'green'
  return 'orange'
}

async function load() {
  loading.value = true
  const params = { page: pagination.value.current, page_size: pagination.value.pageSize }
  if (kw.value.trim()) params.keyword = kw.value.trim()
  let r
  try {
    r = await api('admin_notification_list', params)
  } finally {
    loading.value = false
  }
  if (r.code !== 0) {
    list.value = []
    total.value = 0
    pagination.value.total = 0
    if (r.code !== 401) Message.error(r.msg || '通知列表加载失败')
    return
  }
  const d = r.data || {}
  list.value = d.list || []
  total.value = Number(d.total) || 0
  pagination.value.total = total.value
  if (d.page) pagination.value.current = Number(d.page)
  if (d.page_size) pagination.value.pageSize = Number(d.page_size)
}

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

/** 删除单条通知 */
function delOne(record) {
  Modal.warning({
    title: '确认删除这条通知?',
    content: `将删除「${record.title || '无标题'}」, 删除后用户端不再显示。`,
    okText: '删除',
    cancelText: '取消',
    hideCancel: false,
    okButtonProps: { status: 'danger' },
    onOk: async () => {
      const r = await api('admin_notification_delete', { id: record.id }, 'POST')
      if (r.code !== 0) {
        if (r.code !== 401) Message.error('删除失败: ' + (r.msg || '未知错误'))
        return false
      }
      Message.success('已删除')
      if (list.value.length === 1 && pagination.value.current > 1) pagination.value.current--
      load()
      return true
    },
  })
}

/** 清空全部: id=0, 二次确认 (不可恢复) */
function clearAll() {
  Modal.warning({
    title: '确认清空全部通知?',
    content: `将删除所有用户的所有通知 (共 ${total.value} 条, 当前筛选范围内)。此操作不可恢复!`,
    okText: '清空全部',
    cancelText: '取消',
    hideCancel: false,
    okButtonProps: { status: 'danger' },
    onOk: async () => {
      const r = await api('admin_notification_delete', { id: 0 }, 'POST')
      if (r.code !== 0) {
        if (r.code !== 401) Message.error('清空失败: ' + (r.msg || '未知错误'))
        return false
      }
      Message.success('已清空全部通知')
      pagination.value.current = 1
      load()
      return true
    },
  })
}

onMounted(load)
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
.grow { min-width: 0; }
.muted { color: var(--color-text-3); font-size: 13px; }
.send-tip { margin-left: 8px; }
.msg-content { word-break: break-word; }
.m-pager { justify-content: flex-end; margin-top: 12px; }

@media (max-width: 820px) {
  .page-toolbar { flex-direction: column; align-items: stretch; }
  .toolbar-spacer { display: none; }
  .send-tip { display: block; margin: 6px 0 0; }
}
</style>
