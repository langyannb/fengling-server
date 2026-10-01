<template>
  <div>
    <a-card :bordered="false" class="page-card">
      <template #title>
        添加投稿人
        <a-tooltip content="输入 QQ 号, App 端只显示头像/名字/说明, 不显示号码">
          <icon-question-circle style="margin-left:6px;color:rgb(var(--gray-6));" />
        </a-tooltip>
      </template>
      <a-form :model="addForm" layout="vertical">
        <a-row :gutter="12">
          <a-col :xs="24" :md="6">
            <a-form-item field="qq" label="QQ 号">
              <a-input v-model="addForm.qq" placeholder="QQ号" allow-clear @press-enter="addContributor" />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :md="8">
            <a-form-item field="name" label="昵称 (App 端显示)">
              <a-input v-model="addForm.name" placeholder="昵称" allow-clear />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :md="10">
            <a-form-item field="bio" label="投稿说明 (一句话, App 端显示)">
              <a-input v-model="addForm.bio" placeholder="一句话说明" allow-clear @press-enter="addContributor" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-button type="primary" :loading="adding" @click="addContributor">
          <template #icon><icon-plus /></template>添加
        </a-button>
      </a-form>
    </a-card>

    <a-card :bordered="false" class="page-card">
      <template #title>投稿名单</template>
      <template #extra>
        <a-button size="small" :loading="loading" @click="load">
          <template #icon><icon-refresh /></template>刷新
        </a-button>
      </template>
      <div class="tip">投稿人投稿的应用: 在「软件」里编辑软件时填写「投稿人 QQ」即可关联, 此处会自动显示 ta 投稿了哪些软件。</div>
      <a-table :data="contributors" :loading="loading" row-key="id" size="small">
        <template #columns>
          <a-table-column title="头像" :width="80">
            <template #cell="{ record }">
              <img class="avatar" :src="qqAvatar(record.qq)" alt="" />
            </template>
          </a-table-column>
          <a-table-column title="QQ" data-index="qq" :width="130" />
          <a-table-column title="昵称" :width="160">
            <template #cell="{ record }">{{ record.name || record.qq }}</template>
          </a-table-column>
          <a-table-column title="投稿说明">
            <template #cell="{ record }">{{ record.bio || '暂无说明' }}</template>
          </a-table-column>
          <a-table-column title="排序" data-index="sort_order" :width="80" />
          <a-table-column title="其投稿软件" :width="280">
            <template #cell="{ record }">
              <template v-if="appNames(record).length">
                <a-tag v-for="n in appNames(record)" :key="n" color="arcoblue" size="small">{{ n }}</a-tag>
              </template>
              <span v-else class="muted">暂无投稿应用</span>
            </template>
          </a-table-column>
          <a-table-column title="操作" :width="140" fixed="right">
            <template #cell="{ record }">
              <a-button type="text" size="small" @click="editContributor(record)">编辑</a-button>
              <a-popconfirm content="确认删除该投稿人?" @ok="delContributor(record)">
                <a-button type="text" status="danger" size="small">删除</a-button>
              </a-popconfirm>
            </template>
          </a-table-column>
        </template>
        <template #empty>暂无投稿人, 上方输入 QQ 号添加</template>
      </a-table>
    </a-card>

    <a-modal
      v-model:visible="showContrib"
      :title="'编辑投稿人 ' + contribForm.qq"
      @ok="saveContributor"
      :ok-loading="saving"
      unmount-on-close
    >
      <a-form :model="contribForm" layout="vertical">
        <a-form-item field="name" label="昵称 (App 端显示)">
          <a-input v-model="contribForm.name" placeholder="昵称" allow-clear />
        </a-form-item>
        <a-form-item field="bio" label="投稿说明 (App 端显示)">
          <a-input v-model="contribForm.bio" placeholder="一句话说明" allow-clear />
        </a-form-item>
        <a-form-item field="sort_order" label="排序 (小的在前)">
          <a-input-number v-model="contribForm.sort_order" :min="0" :precision="0" style="width:180px;" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { Message } from '@arco-design/web-vue'
import { api, pickList } from '../api'

const QQ_RE = /^\d{5,12}$/

const contributors = ref([])
const loading = ref(false)

const addForm = reactive({ qq: '', name: '', bio: '' })
const adding = ref(false)

const showContrib = ref(false)
const saving = ref(false)
const contribForm = reactive({ id: 0, qq: '', name: '', bio: '', sort_order: 0 })

/** QQ 头像 (与旧后台同一图床, App 端同样只显示头像不显示号码) */
function qqAvatar(qq) {
  return 'https://q1.qlogo.cn/g?b=qq&nk=' + qq + '&s=100'
}

/** 兼容后端可能给出的字段形状: app_names: ['A','B'] 或 apps: [{name}] */
function appNames(record) {
  if (Array.isArray(record.app_names)) return record.app_names
  if (Array.isArray(record.apps)) {
    return record.apps.map((a) => (typeof a === 'string' ? a : a && a.name)).filter(Boolean)
  }
  return []
}

async function load() {
  loading.value = true
  try {
    // 旧后台同样用 POST 调用 contributor_apps (返回投稿人 + 其投稿软件)
    const r = await api('contributor_apps', {}, 'POST')
    contributors.value = pickList(r)
  } finally {
    loading.value = false
  }
}

async function addContributor() {
  const qq = (addForm.qq || '').trim()
  if (!qq) return Message.warning('请输入 QQ 号')
  if (!QQ_RE.test(qq)) return Message.warning('QQ 号格式不正确')
  adding.value = true
  try {
    const r = await api(
      'contributor_create',
      { qq, name: (addForm.name || '').trim(), bio: (addForm.bio || '').trim() },
      'POST'
    )
    if (r.code !== 0) {
      if (r.code !== 401) Message.error('添加失败: ' + (r.msg || '未知错误'))
      return
    }
    addForm.qq = ''
    addForm.name = ''
    addForm.bio = ''
    Message.success('投稿人已添加')
    load()
  } finally {
    adding.value = false
  }
}

function editContributor(c) {
  contribForm.id = c.id
  contribForm.qq = c.qq
  contribForm.name = c.name || ''
  contribForm.bio = c.bio || ''
  contribForm.sort_order = Number(c.sort_order) || 0
  showContrib.value = true
}

async function saveContributor() {
  saving.value = true
  try {
    const r = await api(
      'contributor_update',
      {
        id: contribForm.id,
        name: contribForm.name || '',
        bio: contribForm.bio || '',
        sort_order: Number(contribForm.sort_order) || 0,
      },
      'POST'
    )
    if (r.code !== 0) {
      if (r.code !== 401) Message.error('保存失败: ' + (r.msg || '未知错误'))
      return
    }
    showContrib.value = false
    Message.success('已保存')
    load()
  } finally {
    saving.value = false
  }
}

async function delContributor(c) {
  const r = await api('contributor_delete', { id: c.id }, 'POST')
  if (r.code !== 0) {
    if (r.code !== 401) Message.error('删除失败: ' + (r.msg || '未知错误'))
    return
  }
  Message.success('已删除')
  load()
}

onMounted(load)
</script>

<style scoped>
.tip { font-size: 12px; color: var(--color-text-3); margin-bottom: 12px; }
.muted { color: var(--color-text-3); }
.avatar { width: 44px; height: 44px; border-radius: 50%; object-fit: cover; display: block; }
</style>
