<template>
  <div>
    <a-card :bordered="false" class="page-card">
      <div class="page-toolbar">
        <a-input v-model="kw" placeholder="搜索软件名 / 包名 / 投稿QQ" allow-clear style="width: 240px">
          <template #prefix><icon-search /></template>
        </a-input>
        <a-select v-model="filterCat" placeholder="全部分类" allow-clear style="width: 180px" :options="catFilterOptions" />
        <a-select v-model="filterStatus" style="width: 140px" :options="statusOptions" />
        <a-button type="primary" @click="openApp()">
          <template #icon><icon-plus /></template>添加软件
        </a-button>
        <a-button @click="loadAll">
          <template #icon><icon-refresh /></template>刷新
        </a-button>
      </div>

      <a-table
        :data="filteredList"
        row-key="id"
        size="small"
        :loading="loading"
        :pagination="{ pageSize: 20, showTotal: true, showPageSize: true }"
      >
        <template #columns>
          <a-table-column title="图标" :width="72">
            <template #cell="{ record }">
              <img v-if="record.icon" :src="record.icon" class="thumb" />
              <a-avatar v-else :size="40">{{ (record.name || '?')[0] }}</a-avatar>
            </template>
          </a-table-column>
          <a-table-column title="名称" :width="240">
            <template #cell="{ record }">
              <span class="app-name">{{ record.name }}</span>
              <a-tag v-if="record.is_new" size="small" color="arcoblue">新</a-tag>
              <a-tag v-if="record.is_pack" size="small" color="purple">整合</a-tag>
              <a-tag v-if="!Number(record.is_active)" size="small" color="red">下架</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="分类" :width="120">
            <template #cell="{ record }">{{ record.category_name || '未分类' }}</template>
          </a-table-column>
          <a-table-column title="版本" data-index="version" :width="90" />
          <a-table-column title="包名" data-index="package_name" :width="180" />
          <a-table-column title="投稿QQ" data-index="contributor_qq" :width="110" />
          <a-table-column title="标记" :width="130">
            <template #cell="{ record }">
              <a-tag v-if="Number(record.is_top)" size="small" color="orange">置顶</a-tag>
              <a-tag v-if="Number(record.is_featured)" size="small" color="gold">精选</a-tag>
              <span v-if="!Number(record.is_top) && !Number(record.is_featured)">-</span>
            </template>
          </a-table-column>
          <a-table-column title="下载" data-index="download_count" :width="90" />
          <a-table-column title="发布日期" data-index="release_date" :width="120" />
          <a-table-column title="排序" data-index="sort_order" :width="80" />
          <a-table-column title="操作" :width="140" fixed="right">
            <template #cell="{ record }">
              <a-button type="text" size="mini" @click="openApp(record)">编辑</a-button>
              <a-popconfirm content="确认删除该软件?" @ok="delApp(record)">
                <a-button type="text" status="danger" size="mini">删除</a-button>
              </a-popconfirm>
            </template>
          </a-table-column>
        </template>
        <template #empty>暂无数据</template>
      </a-table>
    </a-card>

    <a-modal
      v-model:visible="showApp"
      :title="appForm.id ? '编辑软件' : '添加软件'"
      width="720px"
      :ok-loading="saving"
      ok-text="保存软件"
      cancel-text="取消"
      unmount-on-close
      @ok="saveApp"
    >
      <a-form :model="appForm" layout="vertical">
        <a-divider orientation="left">基本信息</a-divider>
        <a-form-item field="name" label="名称 *" :rules="[{ required: true, message: '请输入软件名' }]">
          <a-input v-model="appForm.name" placeholder="软件名" />
        </a-form-item>
        <a-row :gutter="12">
          <a-col :span="12">
            <a-form-item field="category_id" label="分类">
              <a-select v-model="appForm.category_id" :options="catOptions" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item field="version" label="版本">
              <a-input v-model="appForm.version" placeholder="1.0" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-form-item field="package_name" label="包名">
          <a-input v-model="appForm.package_name" placeholder="com.example.app" />
        </a-form-item>
        <a-form-item field="description" label="描述">
          <a-textarea v-model="appForm.description" :auto-size="{ minRows: 3, maxRows: 6 }" placeholder="软件介绍" />
        </a-form-item>

        <a-divider orientation="left">图标与介绍图</a-divider>
        <a-form-item label="图标 (点击上传)">
          <div class="upload-box" @click="pickImage('icon')">
            <img v-if="appForm.icon" :src="appForm.icon" class="upload-img" />
            <template v-else>
              <span class="upload-plus">+</span>
              <span>上传图标</span>
            </template>
            <div v-if="uploading === 'icon'" class="up-tip">上传中...</div>
          </div>
        </a-form-item>
        <a-form-item label="介绍图片 (应用市场风格, 可一次多选, 点击上传)">
          <div class="shot-wrap">
            <div v-for="(shot, si) in appForm.screenshots" :key="si" class="shot-item">
              <img :src="shot" class="shot-img" />
              <button type="button" class="shot-del" @click.stop="appForm.screenshots.splice(si, 1)">×</button>
            </div>
            <div class="upload-box upload-box-sm" @click="pickImage('screenshots')">
              <template v-if="uploading === 'screenshots'">
                <span class="up-txt">{{ uploadProgress || '上传中...' }}</span>
              </template>
              <template v-else><span class="upload-plus">+</span></template>
            </div>
          </div>
        </a-form-item>

        <a-divider orientation="left">发布设置</a-divider>
        <a-row :gutter="12">
          <a-col :span="12">
            <a-form-item field="release_date" label="发布日期 (显示用)">
              <a-date-picker
                :model-value="appForm.release_date || undefined"
                value-format="YYYY-MM-DD"
                style="width: 100%"
                @change="v => (appForm.release_date = v || '')"
              />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item field="sort_order" label="排序 (已改为按时间, 保留备用)">
              <a-input-number v-model="appForm.sort_order" :min="0" style="width: 100%" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-row :gutter="12">
          <a-col :span="12">
            <a-form-item label="状态">
              <a-switch :model-value="!!Number(appForm.is_active)" @change="onActiveChange">
                <template #checked>上架</template>
                <template #unchecked>下架</template>
              </a-switch>
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="标记">
              <a-switch :model-value="!!Number(appForm.is_top)" @change="v => (appForm.is_top = v ? 1 : 0)">
                <template #checked>🔝 置顶</template>
                <template #unchecked>不置顶</template>
              </a-switch>
            </a-form-item>
          </a-col>
        </a-row>
        <a-row :gutter="12">
          <a-col :span="12">
            <a-form-item label="精选">
              <a-switch :model-value="!!Number(appForm.is_featured)" @change="v => (appForm.is_featured = v ? 1 : 0)">
                <template #checked>⭐ 精选</template>
                <template #unchecked>非精选</template>
              </a-switch>
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="新版本标 (选新=3天后回普通)">
              <a-select v-model="appForm.is_new" :options="newOptions" @change="newFlagTouched = true" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-form-item field="contributor_qq" label="投稿人 QQ (关于页头像, 不显示号码)">
          <a-input v-model="appForm.contributor_qq" placeholder="QQ号, 可留空" />
        </a-form-item>
        <div class="form-tip">「新」标完全手动控制：选「✨ 标新」→ 3 天后自动回到普通；选「不标」→ 立即取消。改版本号不会自动标新。</div>
        <a-form-item field="pack_id" label="整合包 (选择后此软件作为整合包的子项)">
          <a-select v-model="appForm.pack_id" allow-clear placeholder="独立软件" :options="packOptions" />
        </a-form-item>

        <a-divider orientation="left">网盘推广链接</a-divider>
        <div v-for="(link, i) in appForm.links" :key="link._k || i" class="link-item">
          <div class="link-info">
            <div>
              {{ link.label || '网盘链接' }}
              <span v-if="link.password">(提取码:{{ link.password }})</span>
              <a-tag v-if="link._new" size="small" color="arcoblue">新</a-tag>
            </div>
            <div class="link-url">{{ link.url }}</div>
          </div>
          <a-button size="mini" @click="link._editing = !link._editing">{{ link._editing ? '完成' : '改' }}</a-button>
          <a-button size="mini" status="danger" @click="appForm.links.splice(i, 1)">删</a-button>
        </div>
        <a-button long style="margin-bottom: 8px" @click="addLink()">+ 添加网盘链接</a-button>
        <div v-for="link in editingLinks" :key="'e' + link._k" class="link-editor">
          <a-row :gutter="12">
            <a-col :span="12">
              <a-form-item label="类型">
                <a-select v-model="link.pan_type" :options="panOptions" />
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item label="名称">
                <a-input v-model="link.label" />
              </a-form-item>
            </a-col>
          </a-row>
          <a-form-item label="链接URL *">
            <a-input v-model="link.url" placeholder="https://..." />
          </a-form-item>
          <a-form-item label="提取码">
            <a-input v-model="link.password" />
          </a-form-item>
        </div>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { Message } from '@arco-design/web-vue'
import { api, pickList, uploadFile } from '../api'

const loading = ref(false)
const saving = ref(false)
const apps = ref([])
const cats = ref([])
const kw = ref('')
const filterCat = ref('')
const filterStatus = ref('')

const showApp = ref(false)
const uploading = ref('')
const uploadProgress = ref('')
const newFlagTouched = ref(false)
const origLinkIds = ref([])

const emptyForm = () => ({
  id: 0, name: '', category_id: 0, icon: '', version: '', description: '',
  screenshots: [], contributor_qq: '', package_name: '', sort_order: 0,
  is_active: 1, pack_id: null, is_top: 0, is_featured: 0, release_date: '', is_new: 0,
  links: [],
})
const appForm = ref(emptyForm())

const statusOptions = [
  { label: '全部状态', value: '' },
  { label: '上架', value: '1' },
  { label: '下架', value: '0' },
]
const newOptions = [
  { label: '✨ 标「新」', value: 1 },
  { label: '不标', value: 0 },
]
const panOptions = [
  { label: 'UC网盘', value: 'uc' },
  { label: '夸克', value: 'quark' },
  { label: '百度', value: 'baidu' },
  { label: '阿里', value: 'ali' },
  { label: '其他', value: 'other' },
]

/** 分类排序: 顶级在前, 子分类跟随父分类 */
const sortedCats = computed(() =>
  [...cats.value].sort((a, b) =>
    (a.parent_id || 0) - (b.parent_id || 0) || (a.sort_order || 0) - (b.sort_order || 0) || a.id - b.id)
)
const catOptions = computed(() => [
  { label: '未分类', value: 0 },
  ...sortedCats.value.map(c => ({ label: c.parent_id ? '— ' + c.name : c.name, value: c.id })),
])
const catFilterOptions = computed(() => catOptions.value.filter(o => o.value !== 0))
const packOptions = computed(() =>
  apps.value.filter(a => a.id !== appForm.value.id && !a.pack_id).map(a => ({ label: a.name + ' (整合包)', value: a.id }))
)
const editingLinks = computed(() => appForm.value.links.filter(l => l._editing))
const filteredList = computed(() => {
  const k = kw.value.trim().toLowerCase()
  return apps.value.filter(a => {
    if (filterCat.value !== '' && filterCat.value !== null && Number(a.category_id || 0) !== Number(filterCat.value)) return false
    if (filterStatus.value !== '' && Number(a.is_active) !== Number(filterStatus.value)) return false
    if (!k) return true
    return [a.name, a.package_name, a.contributor_qq].some(v => String(v || '').toLowerCase().includes(k))
  })
})

async function loadApps() {
  loading.value = true
  const r = await api('apps')
  loading.value = false
  if (r.code === 0) apps.value = r.data || []
  else pickList(r)
}
async function loadCats() {
  const r = await api('categories')
  if (r.code === 0) cats.value = r.data || []
}
function loadAll() { loadApps(); loadCats() }

/** 图片上传: 直接选文件 (介绍图支持多选) */
function pickImage(target) {
  const input = document.createElement('input')
  input.type = 'file'
  input.accept = 'image/*'
  input.multiple = target === 'screenshots'
  input.onchange = async (e) => {
    const files = Array.from(e.target.files || [])
    if (!files.length) return
    const multi = target === 'screenshots' && files.length > 1
    uploading.value = target
    let uploaded = 0
    for (let fi = 0; fi < files.length; fi++) {
      const file = files[fi]
      if (multi) uploadProgress.value = `上传中 ${fi + 1}/${files.length}...`
      const r = await uploadFile('upload', file, p => { if (multi) uploadProgress.value = `上传中 ${fi + 1}/${files.length} (${p}%)` })
      if (r.code === 0) {
        if (target === 'icon') appForm.value.icon = r.data.url
        else if (target === 'screenshots') {
          if (!appForm.value.screenshots) appForm.value.screenshots = []
          appForm.value.screenshots.push(r.data.url)
        }
        uploaded++
      } else if (r.code !== 401) Message.error(r.msg || '上传失败')
      else return
    }
    if (multi) Message.success(`已上传 ${uploaded}/${files.length} 张`)
    else if (uploaded > 0) Message.success('上传成功')
    uploading.value = ''
    uploadProgress.value = ''
  }
  input.click()
}

function onActiveChange(v) { appForm.value.is_active = v ? 1 : 0 }

/** 打开弹窗: 新建 or 编辑 (编辑时异步拉详情含网盘链接) */
async function openApp(app) {
  newFlagTouched.value = false
  const f = emptyForm()
  if (app) {
    Object.assign(f, {
      id: app.id, name: app.name || '', category_id: Number(app.category_id) || 0,
      icon: app.icon || '', version: app.version || '', description: app.description || '',
      screenshots: app.screenshots ? [...app.screenshots] : [],
      contributor_qq: app.contributor_qq || '', package_name: app.package_name || '',
      sort_order: app.sort_order || 0, is_active: Number(app.is_active),
      pack_id: app.pack_id || null, is_top: Number(app.is_top) || 0,
      is_featured: Number(app.is_featured) || 0, release_date: app.release_date || '',
      is_new: Number(app.is_new) || 0,
    })
  }
  appForm.value = f
  origLinkIds.value = []
  showApp.value = true
  if (app && app.id) {
    const r = await api('app_detail', { id: app.id })
    if (r.code === 0 && r.data) {
      const d = r.data
      appForm.value = {
        ...appForm.value,
        name: d.name || appForm.value.name,
        icon: d.icon || appForm.value.icon,
        version: d.version || appForm.value.version,
        description: d.description || appForm.value.description,
        screenshots: (d.screenshots && d.screenshots.length) ? [...d.screenshots] : appForm.value.screenshots,
        contributor_qq: d.contributor_qq !== undefined ? (d.contributor_qq || '') : appForm.value.contributor_qq,
        package_name: d.package_name || appForm.value.package_name,
        pack_id: d.pack_id || null,
        is_top: Number(d.is_top) || 0,
        is_featured: Number(d.is_featured) || 0,
        release_date: d.release_date || '',
        is_new: Number(d.is_new) || 0,
        links: (d.pan_links || []).map(l => ({ ...l, _k: 'l' + l.id + Math.random(), _editing: false })),
      }
      // 记录原始链接 id (用于检测保存时被删除的链接)
      origLinkIds.value = (d.pan_links || []).map(l => l.id)
    } else if (r.code !== 401) {
      Message.warning('详情加载失败')
    }
  }
}

function addLink() {
  appForm.value.links.push({
    pan_type: 'uc', label: 'UC网盘', url: '', password: '',
    _new: true, _k: 'n' + Date.now() + Math.random(), _editing: true,
  })
}

async function saveApp() {
  const f = appForm.value
  if (!f.name) { Message.warning('请输入软件名'); return }
  saving.value = true
  try {
    // 已有链接先更新
    for (const link of f.links) {
      if (link.id && !link._new) {
        const r = await api('link_update', { id: link.id, pan_type: link.pan_type, label: link.label, url: link.url, password: link.password }, 'POST')
        if (r.code !== 0 && r.code !== 401) { Message.error('链接更新失败: ' + (r.msg || '')); return }
        if (r.code === 401) return
      }
    }
    // 删除被移除的链接
    if (f.id && origLinkIds.value.length) {
      const keptIds = new Set(f.links.filter(l => l.id).map(l => l.id))
      for (const oldId of origLinkIds.value) {
        if (!keptIds.has(oldId)) await api('link_delete', { id: oldId }, 'POST')
      }
    }
    const base = {
      name: f.name, category_id: f.category_id || null, icon: f.icon, version: f.version,
      description: f.description, screenshots: JSON.stringify(f.screenshots || []),
      contributor_qq: f.contributor_qq || '', package_name: f.package_name,
      sort_order: Number(f.sort_order) || 0, pack_id: f.pack_id || null,
      is_top: Number(f.is_top) || 0, is_featured: Number(f.is_featured) || 0,
      release_date: f.release_date || '',
      ...(newFlagTouched.value ? { new_flag: Number(f.is_new) ? 1 : 0 } : {}),
    }
    let r
    if (f.id) {
      r = await api('app_update', { id: f.id, ...base, is_active: Number(f.is_active) }, 'POST')
    } else {
      r = await api('app_create', base, 'POST')
      if (r.code === 0 && r.data) f.id = r.data.id
    }
    if (r.code !== 0) {
      if (r.code !== 401) Message.error('保存失败: ' + (r.msg || '未知错误'))
      return
    }
    // 新建链接
    for (const link of f.links) {
      if (link._new && link.url) {
        const lr = await api('link_create', { app_id: f.id, pan_type: link.pan_type, label: link.label, url: link.url, password: link.password }, 'POST')
        if (lr.code !== 0 && lr.code !== 401) { Message.error('链接保存失败: ' + (lr.msg || '')); return }
        if (lr.code === 401) return
      }
    }
    showApp.value = false
    Message.success('保存成功')
    loadApps()
  } finally {
    saving.value = false
  }
}

async function delApp(app) {
  const r = await api('app_delete', { id: app.id }, 'POST')
  if (r.code !== 0 && r.code !== 401) { Message.error(r.msg || '删除失败'); return }
  Message.success('已删除')
  loadApps()
}

onMounted(loadAll)
</script>

<style scoped>
.page-toolbar { display: flex; flex-wrap: wrap; gap: 10px; align-items: center; margin-bottom: 14px; }
.thumb { width: 48px; height: 48px; object-fit: cover; border-radius: 8px; display: block; }
.app-name { font-weight: 600; margin-right: 6px; }
.upload-box {
  width: 96px; height: 96px; border: 1.5px dashed #C9CDD4; border-radius: 8px;
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 4px; cursor: pointer; position: relative; overflow: hidden; color: #86909C; font-size: 12px;
  background: rgb(var(--gray-1));
}
.upload-box:hover { border-color: rgb(var(--primary-6)); color: rgb(var(--primary-6)); }
.upload-box-sm { width: 108px; height: 108px; flex-shrink: 0; }
.upload-img { width: 100%; height: 100%; object-fit: cover; }
.upload-plus { font-size: 24px; line-height: 1; }
.up-tip { position: absolute; inset: 0; background: rgba(0, 0, 0, .45); color: #fff; display: flex; align-items: center; justify-content: center; font-size: 12px; }
.up-txt { font-size: 11px; text-align: center; padding: 4px; line-height: 1.4; }
.shot-wrap { display: flex; flex-wrap: wrap; gap: 10px; }
.shot-item { position: relative; width: 108px; height: 108px; border-radius: 8px; overflow: hidden; border: 1.5px solid #E8EAF2; }
.shot-img { width: 100%; height: 100%; object-fit: cover; }
.shot-del { position: absolute; top: 2px; right: 2px; background: rgba(245, 69, 92, .9); color: #fff; width: 20px; height: 20px; border: none; border-radius: 50%; font-size: 12px; line-height: 20px; padding: 0; cursor: pointer; }
.form-tip { font-size: 12px; color: #86909C; line-height: 1.6; margin: -4px 0 12px; }
.link-item { display: flex; align-items: center; gap: 8px; padding: 8px 10px; border: 1px solid #E5E6EB; border-radius: 6px; margin-bottom: 8px; }
.link-info { flex: 1; min-width: 0; font-size: 13px; }
.link-url { color: #86909C; font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.link-editor { background: rgb(var(--gray-1)); padding: 12px; border-radius: 6px; margin-bottom: 8px; }
</style>
