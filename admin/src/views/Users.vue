<template>
  <div>
    <a-card :bordered="false" class="page-card">
      <!-- 筛选工具栏: 手机端自动换行、逐行铺满 -->
      <div class="page-toolbar">
        <a-input
          v-model="kw"
          class="grow"
          placeholder="搜索用户名 / 昵称 / 邮箱"
          allow-clear
          :style="isMobile ? 'width:100%' : 'width:240px'"
          @press-enter="search"
        >
          <template #prefix><icon-search /></template>
        </a-input>
        <a-select
          v-model="filterRole"
          :options="roleFilterOptions"
          :style="isMobile ? 'width:calc(50% - 3px)' : 'width:140px'"
          @change="search"
        />
        <a-select
          v-model="filterActive"
          :options="activeFilterOptions"
          :style="isMobile ? 'width:calc(50% - 3px)' : 'width:140px'"
          @change="search"
        />
        <div class="toolbar-spacer"></div>
        <a-button type="primary" @click="openUser()">
          <template #icon><icon-plus /></template>新增用户
        </a-button>
        <a-button @click="openQuotaAll">
          <template #icon><icon-gift /></template>一键设置所有人抽奖次数
        </a-button>
        <a-button :loading="loading" @click="load">
          <template #icon><icon-refresh /></template>刷新
        </a-button>
      </div>

      <!-- ===== 手机端: 卡片列表 (桌面端渲染路径完全不变) ===== -->
      <template v-if="isMobile">
        <a-spin :loading="loading" style="width: 100%">
          <div class="m-cards">
            <div v-for="record in list" :key="record.id" class="m-card">
              <div class="m-card-head">
                <a-avatar v-if="record.avatar" :size="44" :image-url="record.avatar" />
                <a-avatar v-else :size="44">{{ initial(record) }}</a-avatar>
                <div class="m-card-title">
                  {{ record.nickname || record.username }}
                  <a-tag size="small" :color="record.role === 'admin' ? 'orange' : 'arcoblue'">
                    {{ record.role === 'admin' ? '管理员' : '普通用户' }}
                  </a-tag>
                  <a-tag size="small" :color="Number(record.is_active) ? 'green' : 'red'">
                    {{ Number(record.is_active) ? '正常' : '禁用' }}
                  </a-tag>
                </div>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">ID</span>
                <span class="m-card-value">{{ record.id }}</span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">用户名</span>
                <span class="m-card-value">{{ record.username }}</span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">昵称</span>
                <span class="m-card-value">{{ record.nickname || '-' }}</span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">邮箱</span>
                <span class="m-card-value">
                  {{ record.email || '-' }}
                  <a-tag size="small" :color="Number(record.email_verified) ? 'green' : 'gray'">
                    {{ Number(record.email_verified) ? '已验证' : '未验证' }}
                  </a-tag>
                </span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">注册时间</span>
                <span class="m-card-value">{{ record.created_at || '-' }}</span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">抽奖次数</span>
                <span class="m-card-value">
                  <a-tag size="small" :color="quotaOf(record) === -1 ? 'gray' : 'arcoblue'">
                    {{ quotaOf(record) === -1 ? '默认' : ('设为 ' + quotaOf(record)) }}
                  </a-tag>
                  <span class="muted">剩余 {{ qnum(record.lottery_left) }} / 已抽 {{ qnum(record.lottery_drawn) }}</span>
                </span>
              </div>
              <div class="m-card-actions">
                <a-button type="text" size="small" @click="openUser(record)">编辑</a-button>
                <a-button type="text" size="small" @click="openMute(record)">禁言</a-button>
                <a-button type="text" status="success" size="small" @click="unmute(record)">解除禁言</a-button>
                <a-button type="text" size="small" :status="Number(record.is_active) ? 'warning' : 'success'" @click="toggleActive(record)">
                  {{ Number(record.is_active) ? '禁用' : '启用' }}
                </a-button>
                <a-button type="text" size="small" @click="openQuota(record)">设置抽奖次数</a-button>
                <a-button type="text" status="danger" size="small" @click="delUser(record)">删除</a-button>
              </div>
            </div>
          </div>
        </a-spin>
        <a-empty v-if="!loading && !list.length" description="暂无用户" />
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
      <template v-else>
      <!-- 列宽合计 1602 (原 1372 + 抽奖次数 170 + 操作列加宽 60), 手机端由 tableScroll 兜底 >=720 -->
      <a-table
        :data="list"
        row-key="id"
        size="small"
        :loading="loading"
        :scroll="scrollX"
        :pagination="pagination"
        @page-change="onPageChange"
        @page-size-change="onPageSizeChange"
      >
        <template #columns>
          <a-table-column title="ID" data-index="id" :width="70" />
          <a-table-column title="头像" :width="80">
            <template #cell="{ record }">
              <a-avatar v-if="record.avatar" :size="40" :image-url="record.avatar" />
              <a-avatar v-else :size="40">{{ initial(record) }}</a-avatar>
            </template>
          </a-table-column>
          <a-table-column title="用户名" :width="140">
            <template #cell="{ record }"><span class="user-name">{{ record.username }}</span></template>
          </a-table-column>
          <a-table-column title="昵称" :width="140">
            <template #cell="{ record }">{{ record.nickname || '-' }}</template>
          </a-table-column>
          <a-table-column title="邮箱" :width="210">
            <template #cell="{ record }">{{ record.email || '-' }}</template>
          </a-table-column>
          <a-table-column title="邮箱验证" :width="110">
            <template #cell="{ record }">
              <a-tag size="small" :color="Number(record.email_verified) ? 'green' : 'gray'">
                {{ Number(record.email_verified) ? '已验证' : '未验证' }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column title="角色" :width="110">
            <template #cell="{ record }">
              <a-tag size="small" :color="record.role === 'admin' ? 'orange' : 'arcoblue'">
                {{ record.role === 'admin' ? '管理员' : '普通用户' }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column title="状态" :width="100">
            <template #cell="{ record }">
              <a-tag size="small" :color="Number(record.is_active) ? 'green' : 'red'">
                {{ Number(record.is_active) ? '正常' : '禁用' }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column title="抽奖次数" :width="170">
            <template #cell="{ record }">
              <div class="quota-cell">
                <a-tag size="small" :color="quotaOf(record) === -1 ? 'gray' : 'arcoblue'">
                  {{ quotaOf(record) === -1 ? '默认' : ('设为 ' + quotaOf(record)) }}
                </a-tag>
                <span class="muted">剩余 {{ qnum(record.lottery_left) }} / 已抽 {{ qnum(record.lottery_drawn) }}</span>
              </div>
            </template>
          </a-table-column>
          <a-table-column title="注册时间" data-index="created_at" :width="180" />
          <a-table-column title="操作" :width="340" fixed="right">
            <template #cell="{ record }">
              <a-button type="text" size="small" @click="openUser(record)">编辑</a-button>
              <a-button type="text" size="small" @click="openMute(record)">禁言</a-button>
              <a-button type="text" status="success" size="small" @click="unmute(record)">解除禁言</a-button>
              <a-button type="text" size="small" :status="Number(record.is_active) ? 'warning' : 'success'" @click="toggleActive(record)">
                {{ Number(record.is_active) ? '禁用' : '启用' }}
              </a-button>
              <a-button type="text" size="small" @click="openQuota(record)">设置抽奖次数</a-button>
              <a-button type="text" status="danger" size="small" @click="delUser(record)">删除</a-button>
            </template>
          </a-table-column>
        </template>
        <template #empty>暂无用户</template>
      </a-table>
      </template>
    </a-card>

    <a-modal
      v-model:visible="showUser"
      :title="userForm.id ? '编辑用户' : '新增用户'"
      :width="modalWidth(560)"
      :ok-loading="saving"
      ok-text="保存"
      cancel-text="取消"
      unmount-on-close
      @ok="saveUser"
    >
      <a-form :model="userForm" layout="vertical">
        <a-form-item field="username" label="用户名 *">
          <a-input
            v-model="userForm.username"
            :disabled="!!userForm.id"
            placeholder="3-20 位字母 / 数字 / 下划线"
            allow-clear
          />
        </a-form-item>
        <a-form-item field="password" :label="userForm.id ? '密码 (留空表示不修改)' : '密码 *'">
          <a-input-password
            v-model="userForm.password"
            :placeholder="userForm.id ? '留空 = 保持原密码不变' : '至少 6 位'"
            allow-clear
          />
        </a-form-item>
        <a-form-item field="nickname" label="昵称 (最多 20 字)">
          <a-input v-model="userForm.nickname" :max-length="20" placeholder="昵称" allow-clear />
        </a-form-item>
        <a-form-item field="email" label="邮箱">
          <a-input v-model="userForm.email" placeholder="user@example.com" allow-clear />
        </a-form-item>
        <a-form-item field="bio" label="简介 (最多 100 字)">
          <a-textarea
            v-model="userForm.bio"
            :max-length="100"
            show-word-limit
            :auto-size="{ minRows: 2, maxRows: 4 }"
            placeholder="一句话简介"
          />
        </a-form-item>
        <a-row :gutter="12">
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item field="role" label="角色">
              <a-select v-model="userForm.role" :options="roleOptions" />
            </a-form-item>
          </a-col>
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item label="状态">
              <a-switch :model-value="!!Number(userForm.is_active)" @change="onActiveChange">
                <template #checked>正常</template>
                <template #unchecked>禁用</template>
              </a-switch>
            </a-form-item>
          </a-col>
        </a-row>
        <div class="form-tip">
          用户名创建后不可修改；密码留空即保持原密码；禁用后该用户无法登录 App。
        </div>
      </a-form>
    </a-modal>

    <!-- 禁言弹窗: 全站禁言 (group_id 固定传 0) -->
    <a-modal
      v-model:visible="showMute"
      title="禁言用户"
      :width="modalWidth(480)"
      :ok-loading="muting"
      ok-text="确认禁言"
      cancel-text="取消"
      unmount-on-close
      @ok="confirmMute"
    >
      <a-form :model="muteForm" layout="vertical">
        <div class="mute-target">
          被禁言用户：
          <span class="mute-name">{{ muteForm.nickname || muteForm.username }}</span>
          <span class="mute-id">(ID {{ muteForm.user_id }})</span>
        </div>
        <a-form-item label="禁言时长">
          <a-radio-group v-model="muteForm.minutes">
            <a-radio v-for="opt in muteOptions" :key="opt.value" :value="opt.value">
              {{ opt.label }}
            </a-radio>
          </a-radio-group>
        </a-form-item>
        <a-form-item label="禁言原因 (可选, 最多 60 字)">
          <a-input
            v-model="muteForm.reason"
            :max-length="60"
            placeholder="可选，会展示给对方"
            allow-clear
          />
        </a-form-item>
        <div class="form-tip">
          全站禁言：该用户在所有群组内均无法发言；「永久」需管理员手动解除。
        </div>
      </a-form>
    </a-modal>

    <!-- 设置单个用户的抽奖次数 -->
    <a-modal
      v-model:visible="showQuota"
      title="设置抽奖次数"
      :width="modalWidth(480)"
      :ok-loading="quotaSaving"
      ok-text="保存"
      cancel-text="取消"
      unmount-on-close
      @ok="saveQuota"
    >
      <a-form :model="quotaForm" layout="vertical">
        <div class="mute-target">
          设置用户：
          <span class="mute-name">{{ quotaForm.nickname || quotaForm.username }}</span>
          <span class="mute-id">(ID {{ quotaForm.user_id }})</span>
        </div>
        <a-form-item label="抽奖次数">
          <a-input-number v-model="quotaForm.quota" :min="-1" :precision="0" style="width: 100%" />
          <div class="form-tip">
            -1 = 跟随活动默认每人次数；&gt;=0 = 该用户总共可抽这么多次
            (该用户当前已抽 {{ quotaForm.drawn }} 次，剩余会自动按已抽次数计算)。
          </div>
        </a-form-item>
      </a-form>
    </a-modal>

    <!-- 一键设置所有人抽奖次数 (二次确认后生效) -->
    <a-modal
      v-model:visible="showQuotaAll"
      title="一键设置所有人抽奖次数"
      :width="modalWidth(480)"
      :ok-loading="quotaSavingAll"
      ok-text="确定"
      cancel-text="取消"
      unmount-on-close
      @ok="saveQuotaAll"
    >
      <a-form layout="vertical">
        <a-form-item label="抽奖次数">
          <a-input-number v-model="quotaAll" :min="-1" :precision="0" style="width: 100%" />
          <div class="form-tip">
            -1 = 所有用户跟随活动默认每人次数；&gt;=0 = 每个用户总共可抽这么多次
            (会覆盖所有用户已有的单独设置，不可撤销)。
          </div>
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { Message, Modal } from '@arco-design/web-vue'
import { api } from '../api'
import { isMobile, modalWidth, tableScroll } from '../composables/useResponsive'

const loading = ref(false)
const saving = ref(false)
const list = ref([])
const total = ref(0)
const kw = ref('')
const filterRole = ref('')
const filterActive = ref('')

// 表格横向滚动宽度: 所有列宽合计 1602px (桌面按此宽度, 手机端至少 720px)
const scrollX = tableScroll(1602)

// 分页: 服务端分页, 桌面端表格与手机端卡片共用同一 pagination
const pagination = ref({
  current: 1,
  pageSize: 20,
  total: 0,
  showTotal: true,
  showPageSize: true,
  pageSizeOptions: [10, 20, 50],
})

const roleOptions = [
  { label: '普通用户', value: 'user' },
  { label: '管理员', value: 'admin' },
]
const roleFilterOptions = [
  { label: '全部角色', value: '' },
  { label: '普通用户', value: 'user' },
  { label: '管理员', value: 'admin' },
]
const activeFilterOptions = [
  { label: '全部状态', value: '' },
  { label: '正常', value: '1' },
  { label: '禁用', value: '0' },
]

const showUser = ref(false)
const emptyForm = () => ({ id: 0, username: '', password: '', nickname: '', email: '', bio: '', role: 'user', is_active: 1 })
const userForm = ref(emptyForm())

// 禁言: 全站禁言 (group_id 固定 0), minutes = 0 表示永久
const showMute = ref(false)
const muting = ref(false)
const muteForm = ref({ user_id: 0, username: '', nickname: '', minutes: 60, reason: '' })
const muteOptions = [
  { label: '10 分钟', value: 10 },
  { label: '1 小时', value: 60 },
  { label: '1 天', value: 1440 },
  { label: '7 天', value: 10080 },
  { label: '30 天', value: 43200 },
  { label: '永久', value: 0 },
]

/** 头像兜底: 昵称 > 用户名 > ? 的首字 */
function initial(record) {
  const s = String(record.nickname || record.username || '?')
  return s.slice(0, 1).toUpperCase()
}

/** 列表加载: 服务端分页 + 关键字/角色/状态筛选 */
async function load() {
  loading.value = true
  const params = {
    page: pagination.value.current,
    page_size: pagination.value.pageSize,
  }
  if (kw.value.trim()) params.keyword = kw.value.trim()
  if (filterRole.value) params.role = filterRole.value
  if (filterActive.value !== '' && filterActive.value !== null) params.is_active = filterActive.value
  let r
  try {
    r = await api('admin_users', params)
  } finally {
    loading.value = false
  }
  if (r.code !== 0) {
    list.value = []
    total.value = 0
    if (r.code !== 401) Message.error(r.msg || '用户列表加载失败')
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

function onActiveChange(v) {
  userForm.value.is_active = v ? 1 : 0
}

/** 打开弹窗: 新建或编辑 (编辑时密码留空 = 不修改) */
function openUser(record) {
  const f = emptyForm()
  if (record) {
    Object.assign(f, {
      id: Number(record.id) || 0,
      username: record.username || '',
      password: '',
      nickname: record.nickname || '',
      email: record.email || '',
      bio: record.bio || '',
      role: record.role === 'admin' ? 'admin' : 'user',
      is_active: Number(record.is_active) ? 1 : 0,
    })
  }
  userForm.value = f
  showUser.value = true
}

/** 表单校验: 与后端契约保持一致的错误文案 */
function validate(f) {
  if (!f.id) {
    if (!/^[A-Za-z0-9_]{3,20}$/.test(f.username || '')) return '用户名只能包含字母数字下划线(3-20位)'
    if (!f.password || f.password.length < 6) return '密码至少 6 位'
  } else if (f.password && f.password.length < 6) {
    return '密码至少 6 位'
  }
  if (f.nickname && f.nickname.length > 20) return '昵称不能超过 20 个字'
  if (f.bio && f.bio.length > 100) return '简介不能超过 100 个字'
  if (f.email && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(f.email)) return '邮箱格式不正确'
  return ''
}

async function saveUser() {
  const f = userForm.value
  const err = validate(f)
  if (err) { Message.warning(err); return }
  saving.value = true
  try {
    const r = await api('admin_user_save', {
      id: Number(f.id) || 0,
      username: f.username,
      password: f.password || '',
      nickname: f.nickname,
      email: f.email,
      role: f.role,
      is_active: Number(f.is_active) ? 1 : 0,
      bio: f.bio,
    }, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error(r.msg || '保存失败')
      return
    }
    showUser.value = false
    Message.success(f.id ? '保存成功' : '新增成功')
    load()
  } finally {
    saving.value = false
  }
}

/** 列表一键启用 / 禁用 (提交该用户完整字段, 密码留空不修改) */
async function toggleActive(record) {
  const next = Number(record.is_active) ? 0 : 1
  const r = await api('admin_user_save', {
    id: record.id,
    username: record.username,
    password: '',
    nickname: record.nickname || '',
    email: record.email || '',
    role: record.role === 'admin' ? 'admin' : 'user',
    is_active: next,
    bio: record.bio || '',
  }, 'POST')
  if (r.code !== 0) {
    if (r.code !== 401) Message.error(r.msg || '操作失败')
    return
  }
  record.is_active = next
  Message.success(next ? '已启用' : '已禁用')
  load()
}

/** 打开禁言弹窗: 默认 1 小时, 原因清空 */
function openMute(record) {
  muteForm.value = {
    user_id: Number(record.id) || 0,
    username: record.username || '',
    nickname: record.nickname || '',
    minutes: 60,
    reason: '',
  }
  showMute.value = true
}

/** 确认禁言: 全站禁言 (group_id = 0), minutes = 0 表示永久 */
async function confirmMute() {
  const f = muteForm.value
  const reason = (f.reason || '').trim()
  if (reason.length > 60) { Message.warning('禁言原因不能超过 60 个字'); return }
  muting.value = true
  try {
    const r = await api('admin_user_mute', {
      user_id: Number(f.user_id) || 0,
      group_id: 0,
      minutes: Number(f.minutes) || 0,
      reason,
    }, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error(r.msg || '禁言失败')
      return
    }
    showMute.value = false
    Message.success('已禁言')
    load()
  } finally {
    muting.value = false
  }
}

/** 解除全站禁言 (group_id = 0); removed = 0 表示本来就没被禁言 */
async function unmute(record) {
  const r = await api('admin_user_unmute', { user_id: Number(record.id) || 0, group_id: 0 }, 'POST')
  if (r.code !== 0) {
    if (r.code !== 401) Message.error(r.msg || '解除禁言失败')
    return
  }
  Message.success(r.data && Number(r.data.removed) ? '已解除禁言' : '该用户当前未被禁言')
  load()
}

/** 删除: 二次确认 (后端禁止删除自己 / 最后一个管理员) */
function delUser(record) {
  Modal.warning({
    title: '确认删除该用户?',
    content: `删除后不可恢复: ${record.username}${record.nickname ? ' (' + record.nickname + ')' : ''}`,
    okText: '删除',
    cancelText: '取消',
    hideCancel: false,
    okButtonProps: { status: 'danger' },
    onOk: async () => {
      const r = await api('admin_user_delete', { id: record.id }, 'POST')
      if (r.code !== 0) {
        if (r.code !== 401) Message.error(r.msg || '删除失败')
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

// ===== 抽奖次数 (与活动设置里的「默认每人抽奖次数」配合) =====

/** lottery_quota: -1(或字段缺失) = 跟随活动默认; >=0 = 单独设置 */
function quotaOf(record) {
  const v = record ? record.lottery_quota : undefined
  if (v === undefined || v === null || v === '') return -1
  const n = Number(v)
  return Number.isFinite(n) ? n : -1
}

/** 剩余 / 已抽次数兜底成数字 */
function qnum(v) {
  const n = Number(v)
  return Number.isFinite(n) ? n : 0
}

// 单个用户
const showQuota = ref(false)
const quotaSaving = ref(false)
const quotaForm = ref({ user_id: 0, username: '', nickname: '', quota: -1, drawn: 0 })

function openQuota(record) {
  quotaForm.value = {
    user_id: Number(record.id) || 0,
    username: record.username || '',
    nickname: record.nickname || '',
    quota: quotaOf(record),
    drawn: qnum(record.lottery_drawn),
  }
  showQuota.value = true
}

/** 保存单个用户抽奖次数: -1 = 跟随默认, >=0 = 总共可抽次数 */
async function saveQuota() {
  const f = quotaForm.value
  const quota = Number(f.quota)
  if (!Number.isFinite(quota) || quota < -1 || Math.floor(quota) !== quota) {
    Message.warning('抽奖次数必须是 -1 或 >=0 的整数')
    return
  }
  quotaSaving.value = true
  try {
    const r = await api('admin_lottery_quota_set', { user_id: Number(f.user_id) || 0, quota }, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error(r.msg || '设置失败')
      return
    }
    showQuota.value = false
    Message.success('已保存')
    load()
  } finally {
    quotaSaving.value = false
  }
}

// 全部用户
const showQuotaAll = ref(false)
const quotaSavingAll = ref(false)
const quotaAll = ref(-1)

function openQuotaAll() {
  quotaAll.value = -1
  showQuotaAll.value = true
}

/** 一键设置所有人: 先二次确认, 再调 admin_lottery_quota_all */
function saveQuotaAll() {
  const quota = Number(quotaAll.value)
  if (!Number.isFinite(quota) || quota < -1 || Math.floor(quota) !== quota) {
    Message.warning('抽奖次数必须是 -1 或 >=0 的整数')
    return
  }
  showQuotaAll.value = false
  Modal.warning({
    title: '确认修改所有用户?',
    content: `将把所有用户的抽奖次数设为 ${quota}，是否继续？` +
      (quota === -1 ? '（-1 = 跟随活动默认每人次数）' : '（所有用户原有的单独设置都会被覆盖）'),
    okText: '继续',
    cancelText: '取消',
    hideCancel: false,
    onOk: async () => {
      quotaSavingAll.value = true
      try {
        const r = await api('admin_lottery_quota_all', { quota }, 'POST')
        if (r.code !== 0) {
          if (r.code !== 401) Message.error(r.msg || '设置失败')
          return false
        }
        Message.success(`已更新 ${qnum(r.data && r.data.updated)} 个用户`)
        load()
        return true
      } finally {
        quotaSavingAll.value = false
      }
    },
  })
}

onMounted(load)
</script>

<style scoped>
/* 桌面端把操作按钮推到右侧; 手机端隐藏(按钮换行平分) */
.toolbar-spacer { flex: 1 1 auto; min-width: 0; }

.muted { color: var(--color-text-3); font-size: 12px; }
.quota-cell { display: flex; flex-direction: column; gap: 2px; line-height: 1.4; }

.user-name { font-weight: 600; }
.mute-target { margin-bottom: 14px; font-size: 13px; color: var(--color-text-2); }
.mute-name { font-weight: 600; color: var(--color-text-1); }
.mute-id { margin-left: 4px; color: var(--color-text-3); }
.form-tip { font-size: 12px; color: var(--color-text-3); line-height: 1.6; margin-top: 4px; }
.m-pager { justify-content: flex-end; margin-top: 12px; }

@media (max-width: 820px) {
  .toolbar-spacer { display: none; }
}
</style>
