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
              <div class="m-card-actions">
                <a-button type="text" size="small" @click="openUser(record)">编辑</a-button>
                <a-button type="text" size="small" :status="Number(record.is_active) ? 'warning' : 'success'" @click="toggleActive(record)">
                  {{ Number(record.is_active) ? '禁用' : '启用' }}
                </a-button>
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
      <!-- 列宽合计 1292, 手机端由 tableScroll 兜底 >=720 -->
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
          <a-table-column title="注册时间" data-index="created_at" :width="180" />
          <a-table-column title="操作" :width="200" fixed="right">
            <template #cell="{ record }">
              <a-button type="text" size="small" @click="openUser(record)">编辑</a-button>
              <a-button type="text" size="small" :status="Number(record.is_active) ? 'warning' : 'success'" @click="toggleActive(record)">
                {{ Number(record.is_active) ? '禁用' : '启用' }}
              </a-button>
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

// 表格横向滚动宽度: 所有列宽合计 1292px (桌面按此宽度, 手机端至少 720px)
const scrollX = tableScroll(1292)

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

onMounted(load)
</script>

<style scoped>
/* 桌面端把操作按钮推到右侧; 手机端隐藏(按钮换行平分) */
.toolbar-spacer { flex: 1 1 auto; min-width: 0; }

.user-name { font-weight: 600; }
.form-tip { font-size: 12px; color: var(--color-text-3); line-height: 1.6; margin-top: 4px; }
.m-pager { justify-content: flex-end; margin-top: 12px; }

@media (max-width: 820px) {
  .toolbar-spacer { display: none; }
}
</style>
