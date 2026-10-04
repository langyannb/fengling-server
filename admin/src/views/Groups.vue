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

    <!-- 服务端尚未部署 all_muted 字段时明确提示, 避免开关点了没反应却看不出来 -->
    <div v-if="!allMuteSupported" class="allmute-warn">
      当前服务端未返回「全体禁言」(all_muted) 字段, 该功能暂不可用, 需等服务端部署完成后生效。
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
              <span class="m-card-label">全体禁言</span>
              <span class="m-card-value">
                <a-switch
                  size="small"
                  :model-value="isAllMuted(record)"
                  :loading="allMuteBusyId === Number(record.id)"
                  :disabled="!allMuteSupported"
                  @change="(v) => toggleAllMute(record, v)"
                >
                  <template #checked>已开启</template>
                  <template #unchecked>未开启</template>
                </a-switch>
              </span>
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
    <!-- 横向总宽: 70+140+110+240+80+90+100+110+180+200 = 1320 -->
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

        <a-table-column title="全体禁言" :width="110">
          <template #cell="{ record }">
            <!-- 行内直接切换: 受控开关 (model-value 绑定行数据), 保存成功后由 load() 刷新 -->
            <a-switch
              size="small"
              :model-value="isAllMuted(record)"
              :loading="allMuteBusyId === Number(record.id)"
              :disabled="!allMuteSupported"
              @change="(v) => toggleAllMute(record, v)"
            >
              <template #checked>已开启</template>
              <template #unchecked>未开启</template>
            </a-switch>
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

        <a-form-item field="icon" label="群组头像 (上传后自动存到对象存储)">
          <div class="icon-upload">
            <img v-if="iconPreview" class="icon-preview" :src="iconPreview" alt="" />
            <div v-else class="icon-preview icon-preview-empty"><icon-image /></div>
            <div class="icon-upload-side">
              <input ref="iconInputRef" type="file" accept="image/*" class="icon-file-input" @change="onIconPick" />
              <a-space>
                <a-button size="small" :loading="iconUploading" @click="pickIcon">
                  {{ iconPreview ? '更换图片' : '上传图片' }}
                </a-button>
                <a-button v-if="iconPreview" size="small" status="danger" @click="clearIcon">移除</a-button>
              </a-space>
              <div class="icon-tip">支持 jpg / png / gif / webp, 建议正方形, 保存时一并上传</div>
            </div>
          </div>
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

        <a-form-item field="notice" label="群公告 (最多 500 字, App 端聊天页顶部展示)">
          <a-textarea
            v-model="form.notice"
            :max-length="500"
            show-word-limit
            :auto-size="{ minRows: 2, maxRows: 5 }"
            placeholder="留空表示不显示公告"
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

        <a-form-item field="all_muted" label="全体禁言">
          <a-switch :model-value="!!Number(form.all_muted)" @change="onAllMuteChange">
            <template #checked>已开启</template>
            <template #unchecked>未开启</template>
          </a-switch>
          <div class="form-tip">
            开启后仅群管理员可发言, 普通成员发送消息会被服务端拒绝。
            <template v-if="!allMuteSupported">(服务端暂未返回 all_muted 字段, 现在保存不会生效)</template>
          </div>
        </a-form-item>

        <div class="form-tip">停用后 App 端社交页不再展示该群组, 但已有群消息保留。</div>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { Message, Modal } from '@arco-design/web-vue'
import { api, apiForm, pickList } from '../api'
import { isMobile, modalWidth, tableScroll } from '../composables/useResponsive'

// 表格横向滚动: 桌面按列宽总和 1210, 手机端至少 720
const scrollX = tableScroll(1320)

const list = ref([])
const loading = ref(false)
const saving = ref(false)
const showGroup = ref(false)
// 行内开关正在保存的群 id (0 = 空闲); 服务端是否已返回 all_muted 字段
const allMuteBusyId = ref(0)
const allMuteSupported = ref(true)

// 群组头像上传: 选中文件后本地预览, 保存时作为 icon_file 一起提交
const iconInputRef = ref(null)
const iconFile = ref(null)
const iconPreview = ref('')
const iconUploading = ref(false)

const form = reactive({
  id: 0,
  name: '',
  icon: '',
  description: '',
  notice: '',
  sort_order: 0,
  is_active: 1,
  all_muted: 0,
})

function initial(record) {
  return String(record.name || '?').slice(0, 1).toUpperCase()
}

async function load() {
  loading.value = true
  try {
    const r = await api('admin_groups')
    list.value = pickList(r)
    // 服务端未部署 all_muted 时该字段整体缺失: 按「未开启」显示 + 禁用开关, 不做静默失败
    if (list.value.length) {
      allMuteSupported.value = list.value.some((g) => Object.prototype.hasOwnProperty.call(g, 'all_muted'))
    }
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
      notice: record.notice || '',
      sort_order: Number(record.sort_order) || 0,
      is_active: Number(record.is_active) ? 1 : 0,
      // 字段缺失 (服务端未部署) 时按未开启显示
      all_muted: Number(record.all_muted ?? 0) === 1 ? 1 : 0,
    })
    iconFile.value = null
    iconPreview.value = record.icon || ''
  } else {
    Object.assign(form, {
      id: 0,
      name: '',
      icon: '',
      description: '',
      notice: '',
      sort_order: 0,
      is_active: 1,
      all_muted: 0,
    })
    iconFile.value = null
    iconPreview.value = ''
  }
  showGroup.value = true
}

/** 选择头像文件 (只本地预览, 保存时再上传) */
function onIconPick(e) {
  const f = e.target.files && e.target.files[0]
  // 允许重复选中同一个文件
  e.target.value = ''
  if (!f) return
  if (!/^image\//.test(f.type)) {
    Message.warning('请选择图片文件')
    return
  }
  if (f.size > 10 * 1024 * 1024) {
    Message.warning('图片不能超过 10MB')
    return
  }
  iconFile.value = f
  const reader = new FileReader()
  reader.onload = () => { iconPreview.value = String(reader.result || '') }
  reader.readAsDataURL(f)
}

function pickIcon() {
  iconInputRef.value && iconInputRef.value.click()
}

function clearIcon() {
  iconFile.value = null
  iconPreview.value = ''
  form.icon = ''
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
    const fields = {
      id: Number(form.id) || 0,
      name: form.name.trim(),
      description: form.description || '',
      notice: form.notice || '',
      sort_order: Number(form.sort_order) || 0,
      is_active: Number(form.is_active) ? 1 : 0,
      all_muted: Number(form.all_muted) ? 1 : 0,
    }
    let r
    if (iconFile.value) {
      // 带图片上传 -> FormData (服务端压缩后传对象存储, 返回 icon URL)
      iconUploading.value = true
      fields.icon_file = iconFile.value
      r = await apiForm('admin_group_save', fields)
    } else {
      fields.icon = form.icon || ''
      r = await api('admin_group_save', fields, 'POST')
    }
    if (r.code !== 0) {
      if (r.code !== 401) Message.error('保存失败: ' + (r.msg || '未知错误'))
      return
    }
    const wantMuted = Number(form.all_muted) ? 1 : 0
    const savedId = Number((r.data && r.data.id) || form.id) || 0
    showGroup.value = false
    iconFile.value = null
    Message.success(form.id ? '保存成功' : '新建成功')
    // 服务端未支持 all_muted 时会静默忽略, 保存后核对一次并提示
    await verifyAllMuted(savedId, wantMuted)
  } finally {
    saving.value = false
    iconUploading.value = false
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
    notice: record.notice || '',
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

/** 行数据是否已开启全体禁言 (字段缺失按未开启) */
function isAllMuted(record) {
  return Number(record && record.all_muted) === 1
}

function onAllMuteChange(v) {
  form.all_muted = v ? 1 : 0
}

/**
 * 列表行内直接切换「全体禁言」:
 * 立即调 admin_group_save (带 id 与 all_muted), 成功/失败都提示, 失败回滚开关。
 * 注意: 必须带上整行字段 —— 服务端保存时 description/icon/sort_order/is_active
 * 是按参数写入的, 只传 id + all_muted 会把简介/头像/排序/状态冲掉;
 * 而 name / notice 服务端有「未提交则沿用原值」保护。此处沿用 toggleActive 的整行提交方式。
 */
async function toggleAllMute(record, v) {
  const id = Number(record && record.id) || 0
  if (!id) return
  const next = v ? 1 : 0
  const prev = isAllMuted(record) ? 1 : 0
  allMuteBusyId.value = id
  try {
    const r = await api('admin_group_save', {
      id,
      name: record.name,
      icon: record.icon || '',
      description: record.description || '',
      notice: record.notice || '',
      sort_order: Number(record.sort_order) || 0,
      is_active: Number(record.is_active) ? 1 : 0,
      all_muted: next,
    }, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error('保存失败: ' + (r.msg || '未知错误'))
      record.all_muted = prev // 回滚开关
      return
    }
    const applied = await verifyAllMuted(id, next)
    if (!applied) record.all_muted = prev // 服务端没接受该字段 -> 回滚开关
    else Message.success(next ? '已开启全体禁言' : '已关闭全体禁言')
  } finally {
    allMuteBusyId.value = 0
  }
}

/** 保存后核对 all_muted 是否真的生效 (服务端未部署该字段时会静默忽略) */
async function verifyAllMuted(id, expected) {
  await load()
  if (allMuteSupported.value === false) {
    if (expected !== 1) return true
    Message.warning('「全体禁言」未生效: 服务端暂未返回 all_muted 字段, 请等服务端部署后重试')
    return false
  }
  const rec = list.value.find((g) => Number(g.id) === Number(id))
  if (!rec) return true
  if ((Number(rec.all_muted ?? 0) === 1 ? 1 : 0) !== expected) {
    Message.warning('「全体禁言」未生效: 服务端未保存该字段')
    return false
  }
  return true
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
/* ===== 群组头像上传 ===== */
.icon-upload {
  display: flex;
  align-items: center;
  gap: 14px;
}
.icon-file-input {
  display: none;
}
.icon-preview {
  width: 72px;
  height: 72px;
  border-radius: 16px;
  object-fit: cover;
  border: 1px solid var(--color-border-2);
  background: var(--color-fill-2);
  flex: 0 0 auto;
}
.icon-preview-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 26px;
  color: var(--color-text-3);
}
.icon-upload-side {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.icon-tip {
  font-size: 12px;
  color: var(--color-text-3);
  line-height: 1.5;
}

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
/* 服务端未支持 all_muted 时的提示条 */
.allmute-warn {
  margin-bottom: 12px;
  padding: 8px 12px;
  border-radius: 6px;
  background: var(--color-warning-light-1, #fff7e8);
  color: rgb(var(--warning-6, 255 125 0));
  font-size: 12px;
  line-height: 1.6;
}

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
