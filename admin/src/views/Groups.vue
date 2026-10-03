<template>
  <a-card :bordered="false" class="page-card">
    <div class="page-toolbar">
      <a-space>
        <a-button type="primary" @click="openGroup()">
          <template #icon><icon-plus /></template>新建群组
        </a-button>
        <a-button :loading="loading" @click="load">
          <template #icon><icon-refresh /></template>刷新
        </a-button>
      </a-space>
      <span class="muted">共 {{ list.length }} 个群组</span>
    </div>

    <!-- ============ 手机端: 卡片列表 (替代必须横滑的表格) ============ -->
    <template v-if="isMobile">
      <a-spin :loading="loading" style="width: 100%">
        <div class="m-cards">
          <div v-for="record in list" :key="record.id" class="m-card">
            <div class="m-card-head">
              <img v-if="record.icon" class="m-card-thumb" :src="record.icon" />
              <div v-else class="m-card-thumb m-card-thumb--empty">{{ initial(record) }}</div>
              <div class="m-card-title">
                {{ record.name || '未命名' }}
                <a-tag size="small" :color="Number(record.is_active) ? 'green' : 'gray'">
                  {{ Number(record.is_active) ? '启用' : '停用' }}
                </a-tag>
              </div>
            </div>

            <div class="m-card-row">
              <span class="m-card-label">ID</span>
              <span class="m-card-value">{{ record.id }}</span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">简介</span>
              <span class="m-card-value">{{ record.description || '-' }}</span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">排序</span>
              <span class="m-card-value">{{ record.sort_order }}</span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">消息数</span>
              <span class="m-card-value">{{ record.message_count ?? 0 }}</span>
            </div>
            <div class="m-card-row">
              <span class="m-card-label">创建时间</span>
              <span class="m-card-value">{{ record.created_at || '-' }}</span>
            </div>

            <div class="m-card-actions">
              <a-button type="text" size="small" @click="openGroup(record)">编辑</a-button>
              <a-button
                type="text"
                size="small"
                :status="Number(record.is_active) ? 'warning' : 'success'"
                @click="toggleActive(record)"
              >
                {{ Number(record.is_active) ? '停用' : '启用' }}
              </a-button>
              <a-button type="text" size="small" status="danger" @click="delGroup(record)">删除</a-button>
            </div>
          </div>
        </div>
      </a-spin>
      <a-empty v-if="!loading && !list.length" description="暂无群组" />
    </template>

    <!-- ============ 桌面端: 表格 ============ -->
    <!-- 横向总宽: 70+140+110+240+80+90+100+180+200 = 1210 -->
    <a-table v-else :data="list" :loading="loading" row-key="id" size="small" :scroll="scrollX">
      <template #columns>
        <a-table-column title="ID" data-index="id" :width="70" />

        <a-table-column title="图标" :width="140">
          <template #cell="{ record }">
            <img v-if="record.icon" class="thumb" :src="record.icon" />
            <span v-else class="muted">未设置</span>
          </template>
        </a-table-column>

        <a-table-column title="名称" :width="110">
          <template #cell="{ record }"><span class="group-name">{{ record.name }}</span></template>
        </a-table-column>

        <a-table-column title="简介" :width="240">
          <template #cell="{ record }">
            <span v-if="record.description">{{ record.description }}</span>
            <span v-else class="muted">-</span>
          </template>
        </a-table-column>

        <a-table-column title="排序" data-index="sort_order" :width="80" />

        <a-table-column title="消息数" :width="90">
          <template #cell="{ record }">{{ record.message_count ?? 0 }}</template>
        </a-table-column>

        <a-table-column title="状态" :width="100">
          <template #cell="{ record }">
            <a-tag :color="Number(record.is_active) ? 'green' : 'gray'">
              {{ Number(record.is_active) ? '启用' : '停用' }}
            </a-tag>
          </template>
        </a-table-column>

        <a-table-column title="创建时间" data-index="created_at" :width="180" />

        <a-table-column title="操作" :width="200" fixed="right">
          <template #cell="{ record }">
            <a-space>
              <a-button type="text" size="small" @click="openGroup(record)">编辑</a-button>
              <a-button
                type="text"
                size="small"
                :status="Number(record.is_active) ? 'warning' : 'success'"
                @click="toggleActive(record)"
              >
                {{ Number(record.is_active) ? '停用' : '启用' }}
              </a-button>
              <a-button type="text" size="small" status="danger" @click="delGroup(record)">删除</a-button>
            </a-space>
          </template>
        </a-table-column>
      </template>
      <template #empty>暂无群组</template>
    </a-table>

    <a-modal
      v-model:visible="showGroup"
      :title="form.id ? '编辑群组' : '新建群组'"
      :width="modalWidth(560)"
      :ok-loading="saving"
      ok-text="保存"
      cancel-text="取消"
      unmount-on-close
      @ok="save"
    >
      <a-form :model="form" layout="vertical">
        <a-form-item field="name" label="群组名称 *">
          <a-input v-model="form.name" :max-length="50" placeholder="例如: Aevum" allow-clear />
        </a-form-item>

        <a-form-item field="icon" label="图标 URL">
          <a-input v-model="form.icon" placeholder="https://... (可留空)" allow-clear />
        </a-form-item>

        <a-form-item field="description" label="简介 (最多 100 字)">
          <a-textarea
            v-model="form.description"
            :max-length="100"
            show-word-limit
            :auto-size="{ minRows: 2, maxRows: 4 }"
            placeholder="一句话介绍该群组"
          />
        </a-form-item>

        <!-- 手机端排序/状态各占整行, 桌面保持半宽 -->
        <a-row :gutter="16">
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item field="sort_order" label="排序 (越小越靠前)">
              <a-input-number v-model="form.sort_order" :min="0" style="width: 100%" />
            </a-form-item>
          </a-col>
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item field="is_active" label="状态">
              <a-switch :model-value="!!Number(form.is_active)" @change="onActiveChange">
                <template #checked>启用</template>
                <template #unchecked>停用</template>
              </a-switch>
            </a-form-item>
          </a-col>
        </a-row>

        <div class="form-tip">停用后 App 端社交页不再展示该群组, 但已有群消息保留。</div>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { Message, Modal } from '@arco-design/web-vue'
import { api, pickList } from '../api'
import { isMobile, modalWidth, tableScroll } from '../composables/useResponsive'

// 表格横向滚动: 桌面按列宽总和 1210, 手机端至少 720
const scrollX = tableScroll(1210)

const list = ref([])
const loading = ref(false)
const saving = ref(false)
const showGroup = ref(false)

const form = reactive({
  id: 0,
  name: '',
  icon: '',
  description: '',
  sort_order: 0,
  is_active: 1,
})

function initial(record) {
  return String(record.name || '?').slice(0, 1).toUpperCase()
}

async function load() {
  loading.value = true
  try {
    const r = await api('admin_groups')
    list.value = pickList(r)
  } finally {
    loading.value = false
  }
}

/** 打开弹窗: 新建 (无参) 或编辑 (传 record) */
function openGroup(record) {
  if (record) {
    Object.assign(form, {
      id: Number(record.id) || 0,
      name: record.name || '',
      icon: record.icon || '',
      description: record.description || '',
      sort_order: Number(record.sort_order) || 0,
      is_active: Number(record.is_active) ? 1 : 0,
    })
  } else {
    Object.assign(form, {
      id: 0,
      name: '',
      icon: '',
      description: '',
      sort_order: 0,
      is_active: 1,
    })
  }
  showGroup.value = true
}

function onActiveChange(v) {
  form.is_active = v ? 1 : 0
}

async function save() {
  if (!form.name || !form.name.trim()) {
    Message.warning('请填写群组名称')
    return
  }
  saving.value = true
  try {
    const r = await api('admin_group_save', {
      id: Number(form.id) || 0,
      name: form.name.trim(),
      icon: form.icon || '',
      description: form.description || '',
      sort_order: Number(form.sort_order) || 0,
      is_active: Number(form.is_active) ? 1 : 0,
    }, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error('保存失败: ' + (r.msg || '未知错误'))
      return
    }
    showGroup.value = false
    Message.success(form.id ? '保存成功' : '新建成功')
    load()
  } finally {
    saving.value = false
  }
}

/** 列表一键启用 / 停用 */
async function toggleActive(record) {
  const next = Number(record.is_active) ? 0 : 1
  const r = await api('admin_group_save', {
    id: record.id,
    name: record.name,
    icon: record.icon || '',
    description: record.description || '',
    sort_order: Number(record.sort_order) || 0,
    is_active: next,
  }, 'POST')
  if (r.code !== 0) {
    if (r.code !== 401) Message.error(r.msg || '操作失败')
    return
  }
  Message.success(next ? '已启用' : '已停用')
  load()
}

/** 删除: 二次确认, 明确提示会连同群消息一起删除 */
function delGroup(record) {
  Modal.warning({
    title: '确认删除该群组?',
    content: `将删除群组「${record.name}」及其全部群消息 (${record.message_count ?? 0} 条), 删除后不可恢复。`,
    okText: '删除',
    cancelText: '取消',
    hideCancel: false,
    okButtonProps: { status: 'danger' },
    onOk: async () => {
      const r = await api('admin_group_delete', { id: record.id }, 'POST')
      if (r.code !== 0) {
        if (r.code !== 401) Message.error('删除失败: ' + (r.msg || '未知错误'))
        return false
      }
      Message.success('已删除')
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
  justify-content: space-between;
  margin-bottom: 12px;
}
.muted {
  color: var(--color-text-3);
  font-size: 13px;
}
.group-name { font-weight: 600; }
.form-tip { font-size: 12px; color: var(--color-text-3); line-height: 1.6; margin-top: 4px; }

/* 图标是方形小图, 在全局 .thumb 基础上只定尺寸 */
.thumb {
  width: 40px;
  height: 40px;
  object-fit: cover;
  border-radius: 8px;
  vertical-align: middle;
}
.m-card-thumb--empty {
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-fill-2);
  color: var(--color-text-3);
  font-weight: 600;
}

@media (max-width: 820px) {
  .page-toolbar {
    flex-direction: column;
    align-items: stretch;
    gap: 8px;
  }
}
</style>
