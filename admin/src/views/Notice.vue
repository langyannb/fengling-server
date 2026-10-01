<template>
  <div>
    <a-row :gutter="16">
      <a-col :xs="24" :md="14">
        <a-card title="公告设置" class="page-card" :bordered="false">
          <a-form :model="form" layout="vertical">
            <a-form-item field="content" label="公告内容 (富文本编辑器, 支持加粗/颜色/链接/图片)">
              <div class="editor-wrap">
                <div class="editor-toolbar">
                  <a-button-group size="small">
                    <a-tooltip content="加粗"><a-button @mousedown.prevent @click="execCmd('bold')"><template #icon><icon-bold /></template></a-button></a-tooltip>
                    <a-tooltip content="斜体"><a-button @mousedown.prevent @click="execCmd('italic')"><template #icon><icon-italic /></template></a-button></a-tooltip>
                    <a-tooltip content="下划线"><a-button @mousedown.prevent @click="execCmd('underline')"><template #icon><icon-underline /></template></a-button></a-tooltip>
                    <a-tooltip content="无序列表"><a-button @mousedown.prevent @click="execCmd('insertUnorderedList')"><template #icon><icon-unordered-list /></template></a-button></a-tooltip>
                    <a-tooltip content="有序列表"><a-button @mousedown.prevent @click="execCmd('insertOrderedList')"><template #icon><icon-ordered-list /></template></a-button></a-tooltip>
                  </a-button-group>
                  <a-button-group size="small">
                    <a-tooltip content="插入链接"><a-button @mousedown.prevent @click="openLink"><template #icon><icon-link /></template></a-button></a-tooltip>
                    <a-tooltip content="插入图片"><a-button :loading="uploading" @mousedown.prevent @click="pickImage"><template #icon><icon-image /></template></a-button></a-tooltip>
                  </a-button-group>
                  <a-tooltip content="文字颜色">
                    <input type="color" class="etb-color" value="#4C6FFF" @change="execCmd('foreColor', $event.target.value)" />
                  </a-tooltip>
                  <a-tooltip content="清除格式">
                    <a-button size="small" @mousedown.prevent @click="execCmd('removeFormat')"><template #icon><icon-brush /></template></a-button>
                  </a-tooltip>
                </div>
                <div
                  ref="editorEl"
                  class="editor-area"
                  contenteditable="true"
                  data-placeholder="输入公告内容..."
                  @input="onNoticeInput"
                ></div>
              </div>
              <template #extra>
                <span class="hint">链接需以 http:// 或 https:// 开头, App 端点击链接会打开内置浏览器。</span>
              </template>
            </a-form-item>

            <a-row :gutter="16">
              <a-col :xs="24" :md="12">
                <a-form-item field="mode" label="显示频率">
                  <a-select v-model="form.mode" placeholder="请选择显示频率">
                    <a-option value="daily">每日显示一次</a-option>
                    <a-option value="every">每次打开都显示</a-option>
                  </a-select>
                </a-form-item>
              </a-col>
              <a-col :xs="24" :md="12">
                <a-form-item field="enabled" label="状态">
                  <a-switch v-model="form.enabled" :checked-value="1" :unchecked-value="0">
                    <template #checked>✅ 启用</template>
                    <template #unchecked>停用</template>
                  </a-switch>
                </a-form-item>
              </a-col>
            </a-row>

            <div class="tip">App 端会显示「今日不再提示」复选框, 勾选后当天不再弹出 (每日模式下)。</div>

            <a-space>
              <a-button type="primary" :loading="saving" @click="saveNotice">保存公告</a-button>
              <a-button :loading="loading" @click="loadNotice">重置</a-button>
            </a-space>
          </a-form>
        </a-card>
      </a-col>

      <a-col :xs="24" :md="10">
        <a-card title="预览 (App 端展示效果)" class="page-card" :bordered="false">
          <template #extra><a-tag :color="form.enabled ? 'green' : 'gray'">{{ form.enabled ? '启用中' : '已停用' }}</a-tag></template>

          <div class="phone">
            <div class="phone-mask">
              <div class="notice-dialog">
                <div class="notice-title">公告</div>
                <div class="notice-body">
                  <!-- eslint-disable-next-line vue/no-v-html -->
                  <div v-if="contentClean" class="notice-text" v-html="form.content"></div>
                  <div v-else class="notice-empty">（暂无公告内容）</div>
                </div>
                <div class="notice-foot">
                  <a-checkbox v-if="form.mode === 'daily'" :model-value="false" disabled>今日不再提示</a-checkbox>
                  <span class="notice-ok">我知道了</span>
                </div>
              </div>
            </div>
          </div>

          <div class="preview-meta">
            <div>显示频率：{{ modeText }}</div>
            <div>内容长度：{{ form.content.length }} 字符</div>
          </div>
        </a-card>
      </a-col>
    </a-row>

    <a-modal v-model:visible="linkVisible" title="插入链接" unmount-on-close @ok="confirmLink">
      <a-form :model="linkForm" layout="vertical">
        <a-form-item field="url" label="链接地址 (http:// 或 https:// 开头)">
          <a-input v-model="linkForm.url" placeholder="https://" allow-clear />
        </a-form-item>
        <a-form-item field="text" label="链接文字">
          <a-input v-model="linkForm.text" placeholder="点击打开" allow-clear />
        </a-form-item>
      </a-form>
    </a-modal>

    <input ref="fileEl" type="file" accept="image/*" class="hidden-file" @change="onPickImage" />
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { Message } from '@arco-design/web-vue'
import { api, uploadFile } from '../api'

const form = reactive({ content: '', mode: 'daily', enabled: 0 })
const loading = ref(false)
const saving = ref(false)
const uploading = ref(false)
const linkVisible = ref(false)
const linkForm = reactive({ url: 'https://', text: '点击打开' })

const editorEl = ref(null)
const fileEl = ref(null)
let savedRange = null

const contentClean = computed(() =>
  (form.content || '').replace(/<br\s*\/?>/gi, '').replace(/&nbsp;/g, '').replace(/<[^>]+>/g, '').trim()
)
const modeText = computed(() => (form.mode === 'every' ? '每次打开都显示' : '每日显示一次'))

/** 记录编辑器内的选区, 避免工具栏/上传/弹窗抢焦点后插入位置丢失 */
function trackSelection() {
  const sel = window.getSelection()
  const ed = editorEl.value
  if (!sel || !sel.rangeCount || !ed) return
  const node = sel.anchorNode
  if (node && (node === ed || ed.contains(node))) savedRange = sel.getRangeAt(0).cloneRange()
}

function restoreSelection() {
  const ed = editorEl.value
  if (!ed) return
  ed.focus()
  if (!savedRange) return
  const sel = window.getSelection()
  sel.removeAllRanges()
  sel.addRange(savedRange)
}

function onNoticeInput() {
  const ed = editorEl.value
  if (ed) form.content = ed.innerHTML
  trackSelection()
}

function execCmd(cmd, arg) {
  const ed = editorEl.value
  if (!ed) return
  restoreSelection()
  document.execCommand(cmd, false, arg)
  onNoticeInput()
}

function escapeHtml(s) {
  return String(s || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

function openLink() {
  trackSelection()
  linkForm.url = 'https://'
  linkForm.text = '点击打开'
  linkVisible.value = true
}

function confirmLink() {
  const url = (linkForm.url || '').trim()
  const text = (linkForm.text || '').trim()
  if (!url) { Message.warning('请输入链接地址'); return }
  if (!/^https?:\/\//i.test(url)) { Message.warning('链接需以 http:// 或 https:// 开头'); return }
  if (!text) { Message.warning('请输入链接文字'); return }
  linkVisible.value = false
  execCmd('insertHTML', '<a href="' + escapeHtml(url) + '" target="_blank">' + escapeHtml(text) + '</a>')
}

function pickImage() {
  trackSelection()
  if (fileEl.value) fileEl.value.value = ''
  fileEl.value && fileEl.value.click()
}

async function onPickImage(e) {
  const file = e.target.files && e.target.files[0]
  if (!file) return
  uploading.value = true
  try {
    const r = await uploadFile('upload', file)
    if (r.code === 0 && r.data && r.data.url) {
      execCmd('insertImage', r.data.url)
      Message.success('图片已插入')
    } else if (r.code !== 401) {
      Message.error(r.msg || '上传失败')
    }
  } catch (err) {
    Message.error('上传失败')
  }
  uploading.value = false
}

async function loadNotice() {
  loading.value = true
  try {
    const r = await api('notice_get')
    if (r.code === 0 && r.data) {
      form.content = r.data.content || ''
      form.mode = r.data.mode || 'daily'
      form.enabled = Number(r.data.enabled) ? 1 : 0
      // 编辑器只在加载/重置时回填一次, 输入过程中不重写 innerHTML (避免光标跳动)
      await nextTick()
      const ed = editorEl.value
      if (ed) {
        ed.innerHTML = form.content
        savedRange = null
      }
    } else if (r.code !== 401) {
      Message.error(r.msg || '公告加载失败')
    }
  } catch (e) {
    Message.error('公告加载失败')
  }
  loading.value = false
}

async function saveNotice() {
  const ed = editorEl.value
  if (ed) form.content = ed.innerHTML
  if (!contentClean.value) { Message.warning('请填写公告内容'); return }
  saving.value = true
  const r = await api('notice_set', { content: form.content, mode: form.mode, enabled: Number(form.enabled) ? 1 : 0 }, 'POST')
  saving.value = false
  if (r.code !== 0) {
    if (r.code !== 401) Message.error('保存失败: ' + (r.msg || '未知错误'))
    return
  }
  Message.success('公告已保存')
  await loadNotice()
}

onMounted(() => {
  document.addEventListener('selectionchange', trackSelection)
  loadNotice()
})
onBeforeUnmount(() => document.removeEventListener('selectionchange', trackSelection))
</script>

<style scoped>
.page-card { margin-bottom: 16px; }
.hint { font-size: 11px; color: var(--color-text-3); }
.tip { font-size: 12px; color: var(--color-text-3); margin-bottom: 14px; }
.editor-wrap { width: 100%; }
.editor-toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin-bottom: 6px; }
.etb-color { width: 34px; height: 28px; padding: 0 2px; border: 1px solid var(--color-border-2); border-radius: 4px; background: #fff; cursor: pointer; }
.editor-area {
  min-height: 180px;
  max-height: 420px;
  overflow: auto;
  padding: 8px 10px;
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
  background: #fff;
  font-size: 14px;
  line-height: 1.7;
  outline: none;
  word-break: break-word;
}
.editor-area:focus { border-color: rgb(var(--primary-6)); }
.editor-area:empty::before { content: attr(data-placeholder); color: var(--color-text-3); pointer-events: none; }
.editor-area :deep(img) { max-width: 100%; }
.hidden-file { display: none; }
.phone { display: flex; justify-content: center; padding: 8px 0 4px; }
.phone-mask { width: 100%; max-width: 300px; border-radius: 12px; background: rgba(0, 0, 0, .45); padding: 22px 14px; }
.notice-dialog { background: #fff; border-radius: 10px; padding: 14px 14px 10px; box-shadow: 0 6px 20px rgba(0, 0, 0, .18); }
.notice-title { font-size: 15px; font-weight: 700; text-align: center; margin-bottom: 10px; color: #1d2129; }
.notice-body { max-height: 220px; overflow: auto; }
.notice-text { font-size: 13px; line-height: 1.7; color: #4e5969; word-break: break-word; }
.notice-text :deep(img) { max-width: 100%; }
.notice-text :deep(a) { color: rgb(var(--primary-6)); }
.notice-empty { font-size: 13px; color: #c9cdd4; text-align: center; padding: 12px 0; }
.notice-foot { display: flex; align-items: center; justify-content: space-between; margin-top: 12px; padding-top: 10px; border-top: 1px solid #f2f3f5; }
.notice-ok { font-size: 13px; color: rgb(var(--primary-6)); font-weight: 600; }
.preview-meta { font-size: 12px; color: var(--color-text-3); margin-top: 10px; line-height: 1.9; }
</style>
