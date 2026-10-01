<template>
  <a-card :bordered="false" class="page-card">
    <div class="page-toolbar">
      <a-space>
        <a-button type="primary" @click="openBanner()">
          <template #icon><icon-plus /></template>添加轮播
        </a-button>
        <a-button @click="load">
          <template #icon><icon-refresh /></template>刷新
        </a-button>
      </a-space>
      <span class="muted">共 {{ list.length }} 条</span>
    </div>

    <!-- 横向总宽: 130+200+260+90+100+160 = 940, 取 940, 手机端由 tableScroll 兜底 >=720 -->
    <a-table :data="list" :loading="loading" row-key="id" size="small" :scroll="scrollX">
      <template #columns>
        <a-table-column title="图片" :width="130">
          <template #cell="{ record }">
            <img v-if="record.image" class="thumb thumb--wide" :src="record.image" />
            <span v-else class="muted">未上传</span>
          </template>
        </a-table-column>

        <a-table-column title="标题" :width="200">
          <template #cell="{ record }">{{ record.title || '未命名' }}</template>
        </a-table-column>

        <a-table-column title="跳转" :width="260">
          <template #cell="{ record }">
            <a-link v-if="record.url" :href="record.url" target="_blank">{{ record.url }}</a-link>
            <a-tag v-else-if="record.app_name" color="arcoblue">→ {{ record.app_name }}</a-tag>
            <span v-else class="muted">无</span>
          </template>
        </a-table-column>

        <a-table-column title="排序" data-index="sort_order" :width="90" />

        <a-table-column title="状态" :width="100">
          <template #cell="{ record }">
            <a-tag :color="Number(record.is_active) ? 'green' : 'gray'">
              {{ Number(record.is_active) ? '启用' : '停用' }}
            </a-tag>
          </template>
        </a-table-column>

        <a-table-column title="操作" :width="160">
          <template #cell="{ record }">
            <a-space>
              <a-button type="text" size="small" @click="openBanner(record)">编辑</a-button>
              <a-popconfirm content="删除该轮播?" @ok="delBanner(record)">
                <a-button type="text" size="small" status="danger">删除</a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </a-table-column>
      </template>
      <template #empty>暂无数据</template>
    </a-table>

    <a-modal
      v-model:visible="showBanner"
      :title="form.id ? '编辑轮播' : '添加轮播'"
      :width="modalWidth(600)"
      :ok-loading="saving"
      unmount-on-close
      @ok="save"
    >
      <a-form :model="form" layout="vertical">
        <a-form-item field="image" label="图片 (点击上传)">
          <a-upload
            :show-file-list="false"
            accept="image/*"
            :custom-request="customUpload"
            :disabled="uploading"
          >
            <template #upload-button>
              <div class="up-box" :class="{ 'up-box--disabled': uploading }">
                <img v-if="form.image" :src="form.image" class="up-img" />
                <template v-else>
                  <icon-plus :size="20" />
                  <span>上传轮播图</span>
                </template>
                <div v-if="uploading" class="up-tip">上传中...</div>
              </div>
            </template>
          </a-upload>
          <a-progress v-if="uploading" :percent="progress" size="small" style="margin-top: 6px" />
        </a-form-item>

        <a-form-item field="title" label="标题">
          <a-input v-model="form.title" placeholder="轮播标题" allow-clear />
        </a-form-item>

        <a-form-item field="url" label="跳转链接 (填写后优先于关联软件, 内置浏览器打开)">
          <a-input v-model="form.url" placeholder="https://..." allow-clear />
        </a-form-item>

        <a-form-item field="app_id" label="关联软件 (点击跳转)">
          <a-select v-model="form.app_id" placeholder="不关联" allow-clear>
            <a-option v-for="a in apps" :key="a.id" :value="a.id">{{ a.name }}</a-option>
          </a-select>
        </a-form-item>

        <!-- 手机端排序/状态各占整行, 桌面保持半宽 -->
        <a-row :gutter="16">
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item field="sort_order" label="排序">
              <a-input-number v-model="form.sort_order" :min="0" style="width: 100%" />
            </a-form-item>
          </a-col>
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item field="is_active" label="状态">
              <a-switch v-model="form.is_active" :checked-value="1" :unchecked-value="0" />
              <span class="muted" style="margin-left: 8px">
                {{ Number(form.is_active) ? '启用' : '停用' }}
              </span>
            </a-form-item>
          </a-col>
        </a-row>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { Message } from '@arco-design/web-vue'
import { api, uploadFile, pickList } from '../api'
import { isMobile, modalWidth, tableScroll } from '../composables/useResponsive'

// 表格横向滚动: 桌面按列宽总和, 手机端至少 720
const scrollX = tableScroll(940)

const list = ref([])
const apps = ref([])
const loading = ref(false)
const showBanner = ref(false)
const saving = ref(false)
const uploading = ref(false)
const progress = ref(0)

const form = reactive({
  id: 0,
  image: '',
  title: '',
  app_id: null,
  url: '',
  sort_order: 0,
  is_active: 1,
})

async function load() {
  loading.value = true
  try {
    const r = await api('banners')
    list.value = pickList(r)
  } finally {
    loading.value = false
  }
}

async function loadApps() {
  const r = await api('apps')
  if (r.code === 0) apps.value = r.data || []
}

function openBanner(record) {
  if (record) {
    Object.assign(form, {
      id: record.id,
      image: record.image || '',
      title: record.title || '',
      app_id: record.app_id || null,
      url: record.url || '',
      sort_order: Number(record.sort_order) || 0,
      is_active: Number(record.is_active),
    })
  } else {
    Object.assign(form, {
      id: 0,
      image: '',
      title: '',
      app_id: null,
      url: '',
      sort_order: 0,
      is_active: 1,
    })
  }
  progress.value = 0
  showBanner.value = true
}

/** a-upload 自定义上传: 走 uploadFile('upload'), 成功取 res.data.url */
async function customUpload(option) {
  const file = option.fileItem && option.fileItem.file
  if (!file) return
  uploading.value = true
  progress.value = 0
  const up = await uploadFile('upload', file, (p) => {
    progress.value = p
  })
  uploading.value = false
  if (up.code === 0 && up.data && up.data.url) {
    form.image = up.data.url
    progress.value = 100
    option.onSuccess(up.data)
    Message.success('上传成功')
  } else {
    progress.value = 0
    option.onError(up)
    if (up.code !== 401) Message.error('上传失败: ' + (up.msg || '未知错误'))
  }
}

async function save() {
  if (!form.image) {
    Message.warning('请上传图片')
    return
  }
  saving.value = true
  try {
    const payload = {
      image: form.image,
      title: form.title,
      app_id: form.app_id || null,
      url: form.url || '',
      sort_order: Number(form.sort_order) || 0,
      is_active: Number(form.is_active),
    }
    const r = form.id
      ? await api('banner_update', { id: form.id, ...payload }, 'POST')
      : await api('banner_create', payload, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error('保存失败: ' + (r.msg || '未知错误'))
      return
    }
    showBanner.value = false
    Message.success('保存成功')
    load()
  } finally {
    saving.value = false
  }
}

async function delBanner(record) {
  const r = await api('banner_delete', { id: record.id }, 'POST')
  if (r.code !== 0) {
    if (r.code !== 401) Message.error('删除失败: ' + (r.msg || '未知错误'))
    return
  }
  Message.success('删除成功')
  load()
}

onMounted(() => {
  load()
  loadApps()
})
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
/* 轮播图是 16:9 横图, 在全局 .thumb 基础上只放宽比例 (用双类名提高优先级) */
.thumb.thumb--wide {
  width: 100px;
  height: 48px;
  object-fit: cover;
  border-radius: 6px;
  vertical-align: middle;
}
.up-box {
  position: relative;
  width: 220px;
  height: 110px;
  border: 1px dashed var(--color-border-3);
  border-radius: 6px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  color: var(--color-text-3);
  overflow: hidden;
  cursor: pointer;
}
.up-box--disabled {
  cursor: not-allowed;
  opacity: 0.7;
}
.up-img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.up-tip {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-mask-bg, rgba(0, 0, 0, 0.45));
  color: var(--color-white, #fff);
  font-size: 13px;
}

/* ============ 手机 (<=820px) ============ */
@media (max-width: 820px) {
  .page-toolbar {
    flex-direction: column;
    align-items: stretch;
  }
  /* 上传控件占满一行 */
  .up-box {
    width: 100%;
    max-width: 100%;
  }
}
</style>
