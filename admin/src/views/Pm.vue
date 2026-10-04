<template>
  <a-card :bordered="false" class="page-card">
    <!-- 筛选工具栏: 手机端自动换行、逐行铺满 -->
    <div class="page-toolbar">
      <a-input
        v-model="kw"
        class="grow"
        placeholder="搜索双方昵称 / 用户名"
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
      <span class="muted">共 {{ total }} 个会话</span>
    </div>

    <!-- ===== 手机端: 卡片列表 (桌面端渲染路径完全不变) ===== -->
    <template v-if="isMobile">
      <a-spin :loading="loading" style="width: 100%">
        <div class="m-cards">
          <div v-for="record in list" :key="record.id" class="m-card">
            <div class="m-card-head">
              <div class="pair-avatars">
                <a-avatar v-if="userOf(record, 'a').avatar" :size="34" :image-url="userOf(record, 'a').avatar" />
                <a-avatar v-else :size="34">{{ initial(userOf(record, 'a')) }}</a-avatar>
                <a-avatar v-if="userOf(record, 'b').avatar" :size="34" :image-url="userOf(record, 'b').avatar" />
                <a-avatar v-else :size="34">{{ initial(userOf(record, 'b')) }}</a-avatar>
              </div>
              <div class="m-card-title">
                {{ pairName(record) }}
                <div class="m-card-sub">
                  {{ userName(userOf(record, 'a')) }} ⇄ {{ userName(userOf(record, 'b')) }}
                </div>
              </div>
            </div>

            <div class="m-card-row">
              <span class="m-card-label">会话 ID</span>
              <span class="m-card-value">{{ record.id }}</span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">消息数</span>
              <span class="m-card-value">{{ record.message_count || 0 }}</span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">最后一条</span>
              <span class="m-card-value">{{ lastText(record) }}</span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">最后时间</span>
              <span class="m-card-value">{{ record.last_at || '-' }}</span>
            </div>

            <div class="m-card-actions">
              <a-button type="text" size="small" @click="openMessages(record)">查看消息</a-button>
              <a-button type="text" status="danger" size="small" @click="clearConv(record)">清空会话</a-button>
            </div>
          </div>
        </div>
      </a-spin>
      <a-empty v-if="!loading && !list.length" description="暂无私聊会话" />
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

    <!-- ===== 桌面端: 表格 (列宽合计 1190) ===== -->
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
        <a-table-column title="会话 ID" data-index="id" :width="90" />
        <a-table-column title="双方" :width="330">
          <template #cell="{ record }">
            <div class="pair">
              <div class="pair-side">
                <a-avatar v-if="userOf(record, 'a').avatar" :size="30" :image-url="userOf(record, 'a').avatar" />
                <a-avatar v-else :size="30">{{ initial(userOf(record, 'a')) }}</a-avatar>
                <span class="pair-name">{{ pairSideText(userOf(record, 'a')) }}</span>
              </div>
              <div class="pair-side">
                <a-avatar v-if="userOf(record, 'b').avatar" :size="30" :image-url="userOf(record, 'b').avatar" />
                <a-avatar v-else :size="30">{{ initial(userOf(record, 'b')) }}</a-avatar>
                <span class="pair-name">{{ pairSideText(userOf(record, 'b')) }}</span>
              </div>
            </div>
          </template>
        </a-table-column>
        <a-table-column title="消息数" :width="90">
          <template #cell="{ record }">{{ record.message_count || 0 }}</template>
        </a-table-column>
        <a-table-column title="最后一条" :width="300">
          <template #cell="{ record }">
            <span class="msg-content">{{ lastText(record) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="最后时间" :width="180">
          <template #cell="{ record }">{{ record.last_at || '-' }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="200" fixed="right">
          <template #cell="{ record }">
            <a-button type="text" size="small" @click="openMessages(record)">查看消息</a-button>
            <a-button type="text" status="danger" size="small" @click="clearConv(record)">清空会话</a-button>
          </template>
        </a-table-column>
      </template>
      <template #empty>暂无私聊会话</template>
    </a-table>

    <!-- ===== 查看消息抽屉 (admin_pm_messages: 关键词 + 分页) ===== -->
    <a-drawer
      v-model:visible="showMsgs"
      :width="drawerWidth"
      :footer="false"
      unmount-on-close
    >
      <template #title>
        <div class="drawer-title">
          <span>{{ convTitle }}</span>
          <span class="muted">共 {{ msgTotal }} 条</span>
        </div>
      </template>

      <div class="drawer-toolbar">
        <a-input
          v-model="msgKw"
          placeholder="搜索消息内容"
          allow-clear
          @press-enter="searchMsgs"
        >
          <template #prefix><icon-search /></template>
        </a-input>
        <a-button :loading="msgLoading" @click="searchMsgs">搜索</a-button>
        <a-button :loading="msgLoading" @click="loadMsgs">
          <template #icon><icon-refresh /></template>刷新
        </a-button>
      </div>

      <a-spin :loading="msgLoading" style="width: 100%">
        <div class="msg-list">
          <div
            v-for="m in msgList"
            :key="m.id"
            class="msg-item"
            :class="{ recalled: Number(m.is_recalled) }"
          >
            <div class="msg-head">
              <span class="msg-from">{{ m.from_name || ('用户' + m.from_user) }}</span>
              <span class="msg-arrow">→</span>
              <span class="msg-to">{{ m.to_name || ('用户' + m.to_user) }}</span>
              <span class="msg-time">{{ m.created_at || '-' }}</span>
            </div>
            <div class="msg-body">
              <span v-if="Number(m.is_recalled)" class="recalled-text">该消息已撤回</span>
              <span v-else class="msg-text">{{ m.content || (m.image ? '[图片]' : '-') }}</span>
              <a-image
                v-if="m.image && !Number(m.is_recalled)"
                :src="m.image"
                :width="isMobile ? 56 : 72"
                class="msg-thumb"
              />
            </div>
            <div class="msg-actions">
              <a-button type="text" size="small" status="danger" @click="delMessage(m)">删除</a-button>
            </div>
          </div>
        </div>
      </a-spin>
      <a-empty v-if="!msgLoading && !msgList.length" description="暂无消息" />
      <a-pagination
        v-if="msgTotal > 0"
        v-model:current="msgPagination.current"
        v-model:page-size="msgPagination.pageSize"
        :total="msgTotal"
        :show-total="true"
        :show-page-size="true"
        :page-size-options="msgPagination.pageSizeOptions"
        size="small"
        class="m-pager"
        @change="onMsgPageChange"
        @page-size-change="onMsgPageSizeChange"
      />
    </a-drawer>
  </a-card>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { Message, Modal } from '@arco-design/web-vue'
import { api } from '../api'
import { isMobile, tableScroll } from '../composables/useResponsive'

// 表格横向滚动: 桌面按列宽总和 1190, 手机端至少 720
const scrollX = tableScroll(1190)

const loading = ref(false)
const list = ref([])
const total = ref(0)
const kw = ref('')

// 分页: 服务端分页, 桌面端表格与手机端卡片共用同一 pagination
const pagination = ref({
  current: 1,
  pageSize: 20,
  total: 0,
  showTotal: true,
  showPageSize: true,
  pageSizeOptions: [10, 20, 50],
})

/** 抽屉宽度: 手机近全屏, 桌面 760px */
const drawerWidth = computed(() => (isMobile.value ? '94vw' : '760px'))

/** 会话一侧的用户对象: 契约字段是 user_a / user_b */
function userOf(record, side) {
  const u = side === 'b' ? record && record.user_b : record && record.user_a
  return u || {}
}

/** 头像兜底: 昵称 > 用户名 > ? 的首字 */
function initial(u) {
  const s = String((u && (u.nickname || u.username)) || '?')
  return s.slice(0, 1).toUpperCase()
}

/** 单个用户显示名: 昵称 (@用户名) */
function userName(u) {
  if (!u || !u.id) return '未知用户'
  const nick = String(u.nickname || '').trim()
  const user = String(u.username || '').trim()
  if (nick && user) return nick + ' (@' + user + ')'
  if (nick) return nick
  if (user) return '@' + user
  return '用户 ' + u.id
}

/** 双方列单行文案 (昵称优先, 太长由 CSS 截断) */
function pairSideText(u) {
  if (!u || !u.id) return '未知用户'
  return String(u.nickname || '').trim() || String(u.username || '').trim() || ('用户 ' + u.id)
}

/** 卡片标题: 双方昵称 */
function pairName(record) {
  return pairSideText(userOf(record, 'a')) + ' ⇄ ' + pairSideText(userOf(record, 'b'))
}

/** 最后一条内容: 纯图片显示 [图片], 什么都没有显示 - */
function lastText(record) {
  const t = String((record && record.last_content) || '').trim()
  if (t) return t
  if (record && record.last_image) return '[图片]'
  return '-'
}

/** 会话列表: 服务端分页 + 关键词 (搜双方昵称 / 用户名) */
async function load() {
  loading.value = true
  const params = {
    page: pagination.value.current,
    page_size: pagination.value.pageSize,
  }
  if (kw.value.trim()) params.keyword = kw.value.trim()
  let r
  try {
    r = await api('admin_pm_conversations', params)
  } finally {
    loading.value = false
  }
  if (r.code !== 0) {
    list.value = []
    total.value = 0
    pagination.value.total = 0
    if (r.code !== 401) Message.error(r.msg || '私聊会话加载失败')
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

// ===== 查看消息 (抽屉) =====
const showMsgs = ref(false)
const msgLoading = ref(false)
const msgList = ref([])
const msgTotal = ref(0)
const msgKw = ref('')
const curConv = ref(null)

const msgPagination = ref({
  current: 1,
  pageSize: 20,
  total: 0,
  showTotal: true,
  showPageSize: true,
  pageSizeOptions: [10, 20, 50],
})

/** 抽屉标题: 双方昵称 */
const convTitle = computed(() => {
  const c = curConv.value
  if (!c) return '私聊消息'
  return pairSideText(userOf(c, 'a')) + ' ⇄ ' + pairSideText(userOf(c, 'b'))
})

function openMessages(record) {
  curConv.value = record
  msgKw.value = ''
  msgPagination.value.current = 1
  msgTotal.value = 0
  msgList.value = []
  showMsgs.value = true
  loadMsgs()
}

async function loadMsgs() {
  const c = curConv.value
  if (!c) return
  msgLoading.value = true
  const params = {
    conv_id: Number(c.id) || 0,
    page: msgPagination.value.current,
    page_size: msgPagination.value.pageSize,
  }
  if (msgKw.value.trim()) params.keyword = msgKw.value.trim()
  let r
  try {
    r = await api('admin_pm_messages', params)
  } finally {
    msgLoading.value = false
  }
  if (r.code !== 0) {
    msgList.value = []
    msgTotal.value = 0
    msgPagination.value.total = 0
    if (r.code !== 401) Message.error(r.msg || '消息加载失败')
    return
  }
  const d = r.data || {}
  msgList.value = d.list || []
  msgTotal.value = Number(d.total) || 0
  msgPagination.value.total = msgTotal.value
  if (d.page) msgPagination.value.current = Number(d.page)
  if (d.page_size) msgPagination.value.pageSize = Number(d.page_size)
}

/** 抽屉内搜索: 回到第 1 页 */
function searchMsgs() {
  msgPagination.value.current = 1
  loadMsgs()
}

function onMsgPageChange(current) {
  msgPagination.value.current = current
  loadMsgs()
}
function onMsgPageSizeChange(pageSize) {
  msgPagination.value.pageSize = pageSize
  msgPagination.value.current = 1
  loadMsgs()
}

/** 删除单条私聊消息: 二次确认 */
function delMessage(m) {
  Modal.warning({
    title: '确认删除这条私聊消息?',
    content: '删除后不可恢复 (' + (m.from_name || ('用户' + m.from_user)) + ' → ' +
      (m.to_name || ('用户' + m.to_user)) + ', ' + (m.created_at || '') + ')',
    okText: '删除',
    cancelText: '取消',
    hideCancel: false,
    okButtonProps: { status: 'danger' },
    onOk: async () => {
      const r = await api('admin_pm_message_delete', { id: Number(m.id) || 0 }, 'POST')
      if (r.code !== 0) {
        if (r.code !== 401) Message.error(r.msg || '删除失败')
        return false
      }
      Message.success('已删除')
      // 删掉当前页最后一条时回退一页
      if (msgList.value.length === 1 && msgPagination.value.current > 1) msgPagination.value.current--
      loadMsgs()
      load()
      return true
    },
  })
}

/** 清空整个会话: 二次确认 (提示不可恢复) */
function clearConv(record) {
  Modal.warning({
    title: '确认清空该会话?',
    content: '将删除「' + pairName(record) + '」之间的全部 ' + (record.message_count || 0) +
      ' 条消息，删除后不可恢复（会话本身保留，双方之后仍可继续发消息）。',
    okText: '清空',
    cancelText: '取消',
    hideCancel: false,
    okButtonProps: { status: 'danger' },
    onOk: async () => {
      const r = await api('admin_pm_clear', { conv_id: Number(record.id) || 0 }, 'POST')
      if (r.code !== 0) {
        if (r.code !== 401) Message.error(r.msg || '清空失败')
        return false
      }
      Message.success('已清空 ' + Number((r.data && r.data.deleted) || 0) + ' 条消息')
      load()
      return true
    },
  })
}

onMounted(load)
</script>

<style scoped>
.toolbar-spacer { flex: 1 1 auto; min-width: 0; }
.muted { color: var(--color-text-3); font-size: 12px; }
.grow { min-width: 0; }
.m-pager { justify-content: flex-end; margin-top: 12px; }

/* 会话双方 */
.pair { display: flex; flex-direction: column; gap: 4px; }
.pair-side { display: flex; align-items: center; gap: 6px; min-width: 0; }
.pair-name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pair-avatars { display: flex; }
.pair-avatars .arco-avatar { border: 2px solid var(--color-bg-2); }
.pair-avatars .arco-avatar + .arco-avatar { margin-left: -12px; }
.msg-content { word-break: break-word; }

/* 抽屉内消息列表 */
.drawer-title { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
.drawer-toolbar { display: flex; gap: 8px; margin-bottom: 12px; }
.drawer-toolbar .arco-input-wrapper { flex: 1 1 auto; min-width: 0; }
.msg-list { display: flex; flex-direction: column; gap: 10px; }
.msg-item {
  border: 1px solid var(--color-border-1);
  border-radius: 8px;
  padding: 10px 12px;
  background: var(--color-bg-2);
}
/* 撤回的消息整体灰色显示, 与正常消息区分 */
.msg-item.recalled { background: var(--color-fill-1); }
.msg-head { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; font-size: 13px; }
.msg-from { font-weight: 600; color: var(--color-text-1); }
.msg-arrow { color: var(--color-text-3); }
.msg-to { color: var(--color-text-2); }
.msg-time { margin-left: auto; color: var(--color-text-3); font-size: 12px; }
.msg-body { display: flex; align-items: flex-start; gap: 10px; margin-top: 6px; }
.msg-text { flex: 1 1 auto; min-width: 0; word-break: break-word; color: var(--color-text-1); }
.recalled-text { flex: 1 1 auto; color: var(--color-text-3); font-size: 13px; }
/* 图片缩略图 (点开可放大) */
.msg-thumb { border-radius: 6px; overflow: hidden; cursor: zoom-in; flex: 0 0 auto; }
.msg-actions { display: flex; justify-content: flex-end; margin-top: 6px; }

@media (max-width: 820px) {
  .toolbar-spacer { display: none; }
}
</style>
