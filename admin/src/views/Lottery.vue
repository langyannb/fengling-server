<template>
  <div>
    <a-card :bordered="false" class="page-card">
      <a-tabs v-model:active-key="activeTab" type="line">
        <!-- ==================== 一、奖项与卡密 ==================== -->
        <a-tab-pane key="prizes" title="奖项与卡密">
          <div class="page-toolbar">
            <a-button type="primary" @click="openPrize()">
              <template #icon><icon-plus /></template>新增奖项
            </a-button>
            <a-button @click="openImport()">
              <template #icon><icon-upload /></template>上传卡密
            </a-button>
            <div class="toolbar-spacer"></div>
            <a-button :loading="prizeLoading" @click="loadPrizes">
              <template #icon><icon-refresh /></template>刷新
            </a-button>
          </div>

          <!-- 手机端: 卡片列表 -->
          <template v-if="isMobile">
            <a-spin :loading="prizeLoading" style="width: 100%">
              <div class="m-cards">
                <div v-for="p in prizes" :key="p.id" class="m-card">
                  <div class="m-card-head">
                    <div class="m-card-title">
                      {{ p.name }}
                      <a-tag size="small" :color="cardColor(p.card_type)">{{ p.card_type || '未设置' }}</a-tag>
                      <a-tag size="small" :color="Number(p.is_active) ? 'green' : 'gray'">
                        {{ Number(p.is_active) ? '启用' : '停用' }}
                      </a-tag>
                    </div>
                  </div>
                  <div class="m-card-row">
                    <span class="m-card-label">ID</span>
                    <span class="m-card-value">{{ p.id }}</span>
                  </div>
                  <div class="m-card-row">
                    <span class="m-card-label">描述</span>
                    <span class="m-card-value">{{ p.description || '-' }}</span>
                  </div>
                  <div class="m-card-row">
                    <span class="m-card-label">排序</span>
                    <span class="m-card-value">{{ Number(p.sort_order) || 0 }}</span>
                  </div>
                  <div class="m-card-row">
                    <span class="m-card-label">库存</span>
                    <span class="m-card-value">
                      总 {{ num(p.total) }} / 已用 {{ num(p.used) }} /
                      <span :class="{ 'stock-zero': num(p.left) === 0 }">剩余 {{ num(p.left) }}</span>
                    </span>
                  </div>
                  <div class="m-card-row">
                    <span class="m-card-label">创建时间</span>
                    <span class="m-card-value">{{ p.created_at || '-' }}</span>
                  </div>
                  <div class="m-card-actions">
                    <a-button type="text" size="small" @click="openPrize(p)">编辑</a-button>
                    <a-button type="text" size="small" @click="openImport(p)">上传卡密</a-button>
                    <a-button type="text" size="small" @click="openCodes(p)">查看卡密</a-button>
                    <a-button type="text" status="danger" size="small" @click="delPrize(p)">删除</a-button>
                  </div>
                </div>
              </div>
            </a-spin>
            <a-empty v-if="!prizeLoading && !prizes.length" description="暂无奖项" />
          </template>

          <!-- 桌面端: 表格; 列宽合计 70+160+110+200+80+110+190+170+250 = 1340 -->
          <a-table
            v-else
            :data="prizes"
            :loading="prizeLoading"
            row-key="id"
            size="small"
            :scroll="prizeScroll"
            :pagination="false"
          >
            <template #columns>
              <a-table-column title="ID" data-index="id" :width="70" />
              <a-table-column title="奖项名称" :width="160">
                <template #cell="{ record }"><span class="prize-name">{{ record.name }}</span></template>
              </a-table-column>
              <a-table-column title="卡密类型" :width="110">
                <template #cell="{ record }">
                  <a-tag size="small" :color="cardColor(record.card_type)">{{ record.card_type || '未设置' }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="描述" :width="200">
                <template #cell="{ record }">{{ record.description || '-' }}</template>
              </a-table-column>
              <a-table-column title="排序" :width="80">
                <template #cell="{ record }">{{ Number(record.sort_order) || 0 }}</template>
              </a-table-column>
              <a-table-column title="状态" :width="110">
                <template #cell="{ record }">
                  <a-switch
                    size="small"
                    :model-value="!!Number(record.is_active)"
                    @change="(v) => togglePrize(record, v)"
                  >
                    <template #checked>启用</template>
                    <template #unchecked>停用</template>
                  </a-switch>
                </template>
              </a-table-column>
              <a-table-column title="库存 (总/已用/剩余)" :width="190">
                <template #cell="{ record }">
                  总 {{ num(record.total) }} / 已用 {{ num(record.used) }} /
                  <span :class="{ 'stock-zero': num(record.left) === 0 }">剩余 {{ num(record.left) }}</span>
                </template>
              </a-table-column>
              <a-table-column title="创建时间" data-index="created_at" :width="170" />
              <a-table-column title="操作" :width="250" fixed="right">
                <template #cell="{ record }">
                  <a-button type="text" size="small" @click="openPrize(record)">编辑</a-button>
                  <a-button type="text" size="small" @click="openImport(record)">上传卡密</a-button>
                  <a-button type="text" size="small" @click="openCodes(record)">查看卡密</a-button>
                  <a-button type="text" status="danger" size="small" @click="delPrize(record)">删除</a-button>
                </template>
              </a-table-column>
            </template>
            <template #empty>暂无奖项, 点右上角「新增奖项」开始</template>
          </a-table>
        </a-tab-pane>

        <!-- ==================== 二、中奖记录 ==================== -->
        <a-tab-pane key="draws" title="中奖记录">
          <div class="page-toolbar">
            <a-select
              v-model="drawFilterPrize"
              :options="prizeFilterOptions"
              :style="isMobile ? 'width:100%' : 'width:200px'"
              @change="searchDraws"
            />
            <a-input
              v-model="drawKw"
              class="grow"
              placeholder="搜索卡密 / 用户名 / 昵称"
              allow-clear
              :style="isMobile ? 'width:100%' : 'width:240px'"
              @press-enter="searchDraws"
            >
              <template #prefix><icon-search /></template>
            </a-input>
            <div class="toolbar-spacer"></div>
            <a-button :loading="drawLoading" @click="loadDraws">
              <template #icon><icon-refresh /></template>刷新
            </a-button>
            <span class="muted">共 {{ drawTotal }} 条</span>
          </div>

          <template v-if="isMobile">
            <a-spin :loading="drawLoading" style="width: 100%">
              <div class="m-cards">
                <div v-for="d in draws" :key="d.id" class="m-card">
                  <div class="m-card-head">
                    <div class="m-card-title">
                      {{ d.nickname || d.username || ('用户' + d.user_id) }}
                      <a-tag size="small" :color="cardColor(d.card_type)">{{ d.card_type || '未设置' }}</a-tag>
                    </div>
                  </div>
                  <div class="m-card-row">
                    <span class="m-card-label">ID</span>
                    <span class="m-card-value">{{ d.id }}</span>
                  </div>
                  <div class="m-card-row">
                    <span class="m-card-label">奖项</span>
                    <span class="m-card-value">{{ d.prize_name || '-' }}</span>
                  </div>
                  <div class="m-card-row">
                    <span class="m-card-label">卡密</span>
                    <span class="m-card-value mono">{{ d.code || '-' }}</span>
                  </div>
                  <div class="m-card-row">
                    <span class="m-card-label">中奖时间</span>
                    <span class="m-card-value">{{ d.created_at || '-' }}</span>
                  </div>
                  <div class="m-card-actions">
                    <a-button type="text" size="small" @click="copyText(d.code)">复制卡密</a-button>
                  </div>
                </div>
              </div>
            </a-spin>
            <a-empty v-if="!drawLoading && !draws.length" description="暂无中奖记录" />
            <a-pagination
              v-if="drawTotal > 0"
              v-model:current="drawPagination.current"
              v-model:page-size="drawPagination.pageSize"
              :total="drawTotal"
              :show-total="true"
              :show-page-size="true"
              :page-size-options="drawPagination.pageSizeOptions"
              class="m-pager"
              @change="onDrawPageChange"
              @page-size-change="onDrawPageSizeChange"
            />
          </template>

          <!-- 列宽合计 70+210+160+110+260+170 = 980 -->
          <a-table
            v-else
            :data="draws"
            :loading="drawLoading"
            row-key="id"
            size="small"
            :scroll="drawScroll"
            :pagination="drawPagination"
            @page-change="onDrawPageChange"
            @page-size-change="onDrawPageSizeChange"
          >
            <template #columns>
              <a-table-column title="ID" data-index="id" :width="70" />
              <a-table-column title="用户" :width="210">
                <template #cell="{ record }">
                  <div class="sender">
                    <span class="sender-name">{{ record.nickname || '未设置昵称' }}</span>
                    <span class="muted">@{{ record.username || ('用户' + record.user_id) }}</span>
                  </div>
                </template>
              </a-table-column>
              <a-table-column title="奖项名" :width="160">
                <template #cell="{ record }">{{ record.prize_name || '-' }}</template>
              </a-table-column>
              <a-table-column title="卡密类型" :width="110">
                <template #cell="{ record }">
                  <a-tag size="small" :color="cardColor(record.card_type)">{{ record.card_type || '未设置' }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="卡密" :width="260">
                <template #cell="{ record }">
                  <div class="code-cell">
                    <span class="mono">{{ record.code || '-' }}</span>
                    <a-button type="text" size="mini" @click="copyText(record.code)">复制</a-button>
                  </div>
                </template>
              </a-table-column>
              <a-table-column title="中奖时间" data-index="created_at" :width="170" />
            </template>
            <template #empty>暂无中奖记录</template>
          </a-table>
        </a-tab-pane>

        <!-- ==================== 三、活动设置 ==================== -->
        <a-tab-pane key="config" title="活动设置">
          <!-- 显著说明: 0 = 不限 (服务端 >999 会报错) -->
          <a-alert type="info" style="margin-bottom: 12px">
            <div><b>每日抽奖次数：0 = 不限</b>（该用户每天都能抽，只受「默认每人抽奖次数 / 用户单独设置」限制）。</div>
            <div>填 1-999 = 每人每天最多抽这么多次，次日自动重置；超过 999 服务端会报错。</div>
          </a-alert>
          <a-spin :loading="configLoading" style="width: 100%">
            <a-form :model="configForm" layout="vertical" class="config-form">
              <a-form-item label="抽奖开关">
                <a-switch v-model="configForm.enabled" :checked-value="1" :unchecked-value="0">
                  <template #checked>开启</template>
                  <template #unchecked>关闭</template>
                </a-switch>
              </a-form-item>
              <a-form-item label="抽奖标题">
                <a-input v-model="configForm.title" placeholder="例如: 抽卡密活动" allow-clear />
              </a-form-item>
              <a-form-item label="抽奖内容 / 说明">
                <a-textarea
                  v-model="configForm.content"
                  placeholder="公众号关注后回复「抽奖」即可参与 (支持换行)"
                  :auto-size="{ minRows: 5, maxRows: 14 }"
                />
              </a-form-item>
              <a-form-item label="默认每人抽奖次数">
                <a-input-number v-model="configForm.per_user_limit" :min="0" :precision="0" style="width: 100%" />
                <div class="form-tip">0 = 默认每人不能抽；每个用户可以用「用户管理 → 设置抽奖次数」单独覆盖。</div>
              </a-form-item>
              <a-form-item label="每日抽奖次数">
                <a-input-number v-model="configForm.daily_limit" :min="0" :max="999" :precision="0" style="width: 100%" />
                <div class="form-tip">
                  <b>0 = 不限</b>（每人每天都能抽）；1-999 = 每人每天最多抽这么多次，次日自动重置。
                </div>
              </a-form-item>
              <a-form-item>
                <a-button type="primary" :loading="configSaving" @click="saveConfig">保存</a-button>
                <a-button :loading="configLoading" style="margin-left: 8px" @click="loadConfig">刷新</a-button>
              </a-form-item>
            </a-form>
          </a-spin>
        </a-tab-pane>
      </a-tabs>
    </a-card>

    <!-- ============ 新增 / 编辑奖项 ============ -->
    <a-modal
      v-model:visible="showPrize"
      :title="prizeForm.id ? '编辑奖项' : '新增奖项'"
      :width="modalWidth(520)"
      :ok-loading="prizeSaving"
      ok-text="保存"
      cancel-text="取消"
      unmount-on-close
      @ok="savePrize"
    >
      <a-form :model="prizeForm" layout="vertical">
        <a-form-item label="奖项名称 *">
          <a-input v-model="prizeForm.name" placeholder="例如: 宇宙卡密" allow-clear />
        </a-form-item>
        <a-form-item label="卡密类型 *">
          <a-select
            v-model="prizeForm.card_type"
            :options="cardTypeOptions"
            allow-create
            allow-search
            allow-clear
            placeholder="选择或直接输入, 例如: 天卡 / 周卡 / 月卡 / Aevum 专属"
          />
        </a-form-item>
        <a-form-item label="描述">
          <a-textarea
            v-model="prizeForm.description"
            placeholder="可选, 客户端会展示"
            :auto-size="{ minRows: 2, maxRows: 5 }"
          />
        </a-form-item>
        <a-row :gutter="12">
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item label="排序">
              <a-input-number v-model="prizeForm.sort_order" :min="0" :precision="0" style="width: 100%" />
            </a-form-item>
          </a-col>
          <a-col :span="isMobile ? 24 : 12">
            <a-form-item label="状态">
              <a-switch v-model="prizeForm.is_active" :checked-value="1" :unchecked-value="0">
                <template #checked>启用</template>
                <template #unchecked>停用</template>
              </a-switch>
            </a-form-item>
          </a-col>
        </a-row>
        <div class="form-tip">排序越小越靠前；停用或库存为 0 的奖项不会出现在客户端抽奖列表里。</div>
      </a-form>
    </a-modal>

    <!-- ============ 上传卡密 ============ -->
    <a-modal
      v-model:visible="showImport"
      title="上传卡密"
      :width="modalWidth(640)"
      :ok-loading="importing"
      ok-text="导入"
      cancel-text="取消"
      unmount-on-close
      @ok="doImport"
    >
      <a-form :model="importForm" layout="vertical">
        <a-form-item label="奖项 *">
          <a-select
            v-model="importForm.prize_id"
            :options="prizeImportOptions"
            placeholder="选择要导入到哪个奖项"
          />
        </a-form-item>
        <a-form-item label="卡密列表 (每行一个) *">
          <a-textarea
            v-model="importForm.text"
            placeholder="每行一个卡密, 例如 GY-ME9NY8R7QGF5KBZ1O2WU4TPLHAI6V"
            :auto-size="{ minRows: 8, maxRows: 16 }"
          />
          <div class="form-tip">已识别 {{ importLineCount }} 行（自动去空行、去重；长度 &lt; 6 或含空格的行会被跳过）</div>
        </a-form-item>
      </a-form>
      <div class="form-tip">导入后卡密全局唯一, 与库内已有卡密重复的行会被忽略。</div>
    </a-modal>

    <!-- ============ 查看卡密 (抽屉) ============ -->
    <a-drawer
      v-model:visible="showCodes"
      :width="isMobile ? '94%' : 920"
      title="卡密管理"
      unmount-on-close
      :footer="false"
    >
      <div class="page-toolbar">
        <a-select
          v-model="codeFilterPrize"
          :options="prizeFilterOptions"
          :style="isMobile ? 'width:100%' : 'width:210px'"
          @change="searchCodes"
        />
        <a-select
          v-model="codeFilterStatus"
          :options="codeStatusOptions"
          :style="isMobile ? 'width:calc(50% - 3px)' : 'width:130px'"
          @change="searchCodes"
        />
        <a-input
          v-model="codeKw"
          class="grow"
          placeholder="搜索卡密"
          allow-clear
          :style="isMobile ? 'width:100%' : 'width:200px'"
          @press-enter="searchCodes"
        >
          <template #prefix><icon-search /></template>
        </a-input>
        <div class="toolbar-spacer"></div>
        <a-button status="danger" @click="delUnusedCodes">删除未使用的卡密</a-button>
        <a-button :loading="codeLoading" @click="loadCodes">
          <template #icon><icon-refresh /></template>刷新
        </a-button>
      </div>
      <div class="code-summary muted">未使用 {{ codeCounts.unused }} 条 / 已使用 {{ codeCounts.used }} 条 / 共 {{ codeTotal }} 条</div>

      <template v-if="isMobile">
        <a-spin :loading="codeLoading" style="width: 100%">
          <div class="m-cards">
            <div v-for="c in codes" :key="c.id" class="m-card">
              <div class="m-card-head">
                <div class="m-card-title">{{ c.code }}</div>
                <a-tag size="small" :color="c.status === 'used' ? 'gray' : 'green'">
                  {{ c.status === 'used' ? '已使用' : '未使用' }}
                </a-tag>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">奖项</span>
                <span class="m-card-value">{{ c.prize_name || '-' }} ({{ c.card_type || '-' }})</span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">使用者</span>
                <span class="m-card-value">
                  {{ c.user_id ? ((c.user_nickname || '用户' + c.user_id) + ' @' + (c.username || c.user_id)) : '-' }}
                </span>
              </div>
              <div class="m-card-row">
                <span class="m-card-label">使用时间</span>
                <span class="m-card-value">{{ c.used_at || '-' }}</span>
              </div>
              <div class="m-card-actions">
                <a-button type="text" size="small" @click="copyText(c.code)">复制</a-button>
              </div>
            </div>
          </div>
        </a-spin>
        <a-empty v-if="!codeLoading && !codes.length" description="暂无卡密" />
        <a-pagination
          v-if="codeTotal > 0"
          v-model:current="codePagination.current"
          v-model:page-size="codePagination.pageSize"
          :total="codeTotal"
          :show-total="true"
          :show-page-size="true"
          :page-size-options="codePagination.pageSizeOptions"
          class="m-pager"
          @change="onCodePageChange"
          @page-size-change="onCodePageSizeChange"
        />
      </template>

      <!-- 列宽合计 70+230+110+120+200+180 = 910 -->
      <a-table
        v-else
        :data="codes"
        :loading="codeLoading"
        row-key="id"
        size="small"
        :scroll="codeScroll"
        :pagination="codePagination"
        @page-change="onCodePageChange"
        @page-size-change="onCodePageSizeChange"
      >
        <template #columns>
          <a-table-column title="ID" data-index="id" :width="70" />
          <a-table-column title="卡密" :width="230">
            <template #cell="{ record }">
              <div class="code-cell">
                <span class="mono">{{ record.code }}</span>
                <a-button type="text" size="mini" @click="copyText(record.code)">复制</a-button>
              </div>
            </template>
          </a-table-column>
          <a-table-column title="状态" :width="110">
            <template #cell="{ record }">
              <a-tag size="small" :color="record.status === 'used' ? 'gray' : 'green'">
                {{ record.status === 'used' ? '已使用' : '未使用' }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column title="奖项" :width="120">
            <template #cell="{ record }">{{ record.prize_name || '-' }}</template>
          </a-table-column>
          <a-table-column title="使用者" :width="200">
            <template #cell="{ record }">
              <span v-if="!record.user_id" class="muted">-</span>
              <span v-else>
                {{ record.user_nickname || ('用户' + record.user_id) }}
                <span class="muted">@{{ record.username || record.user_id }}</span>
              </span>
            </template>
          </a-table-column>
          <a-table-column title="使用时间" :width="180">
            <template #cell="{ record }">{{ record.used_at || '-' }}</template>
          </a-table-column>
        </template>
        <template #empty>暂无卡密, 点上方「上传卡密」批量导入</template>
      </a-table>
    </a-drawer>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { Message, Modal } from '@arco-design/web-vue'
import { api } from '../api'
import { isMobile, modalWidth, tableScroll } from '../composables/useResponsive'

// ============ 通用 ============
const activeTab = ref('prizes')
const prizeScroll = tableScroll(1340)
const drawScroll = tableScroll(980)
const codeScroll = tableScroll(910)

function num(v) {
  const n = Number(v)
  return Number.isFinite(n) ? n : 0
}

/** 卡密类型 → Arco tag 颜色 */
function cardColor(type) {
  const t = String(type || '')
  if (t.includes('天')) return 'arcoblue'
  if (t.includes('周')) return 'green'
  if (t.includes('月')) return 'purple'
  return t ? 'gold' : 'gray'
}

/** 一键复制: 优先 navigator.clipboard, 失败降级 execCommand */
async function copyText(text) {
  const s = String(text ?? '')
  if (!s) return
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(s)
      Message.success('已复制')
      return
    }
  } catch (e) { /* 降级到 execCommand */ }
  try {
    const ta = document.createElement('textarea')
    ta.value = s
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
    Message.success('已复制')
  } catch (e) {
    Message.error('复制失败, 请手动长按选择')
  }
}

// ============ 奖项列表 ============
const prizeLoading = ref(false)
const prizes = ref([])
const cardTypeOptions = [
  { label: '天卡', value: '天卡' },
  { label: '周卡', value: '周卡' },
  { label: '月卡', value: '月卡' },
  { label: 'Aevum 专属', value: 'Aevum 专属' },
]
// 各下拉共用: 第一项是「全部奖项」(value = 0)
const prizeFilterOptions = computed(() => [
  { label: '全部奖项', value: 0 },
  ...prizes.value.map((p) => ({ label: `${p.name} (${p.card_type || '未设置'})`, value: Number(p.id) })),
])
// 上传卡密用: 显示 名称 + 类型 + 剩余
const prizeImportOptions = computed(() =>
  prizes.value.map((p) => ({
    label: `${p.name} · ${p.card_type || '未设置'} · 剩余 ${num(p.left)}`,
    value: Number(p.id),
  }))
)

async function loadPrizes() {
  prizeLoading.value = true
  let r
  try {
    r = await api('admin_lottery_prizes')
  } finally {
    prizeLoading.value = false
  }
  if (r.code !== 0) {
    prizes.value = []
    if (r.code !== 401) Message.error(r.msg || '奖项列表加载失败')
    return
  }
  prizes.value = (r.data && r.data.list) || []
}

// ============ 新增 / 编辑奖项 ============
const showPrize = ref(false)
const prizeSaving = ref(false)
const emptyPrizeForm = () => ({ id: 0, name: '', card_type: '天卡', description: '', sort_order: 0, is_active: 1 })
const prizeForm = ref(emptyPrizeForm())

function openPrize(record) {
  const f = emptyPrizeForm()
  if (record) {
    Object.assign(f, {
      id: Number(record.id) || 0,
      name: record.name || '',
      card_type: record.card_type || '',
      description: record.description || '',
      sort_order: num(record.sort_order),
      is_active: Number(record.is_active) ? 1 : 0,
    })
  }
  prizeForm.value = f
  showPrize.value = true
}

async function savePrize() {
  const f = prizeForm.value
  if (!String(f.name || '').trim()) { Message.warning('奖项名称不能为空'); return }
  if (!String(f.card_type || '').trim()) { Message.warning('卡密类型不能为空'); return }
  prizeSaving.value = true
  try {
    const r = await api('admin_lottery_prize_save', {
      id: Number(f.id) || 0,
      name: String(f.name).trim(),
      card_type: String(f.card_type).trim(),
      description: f.description || '',
      sort_order: num(f.sort_order),
      is_active: Number(f.is_active) ? 1 : 0,
    }, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error(r.msg || '保存失败')
      return
    }
    showPrize.value = false
    Message.success(f.id ? '已保存' : '已新增')
    loadPrizes()
  } finally {
    prizeSaving.value = false
  }
}

/** 列表内直接启用 / 停用 */
async function togglePrize(record, v) {
  const next = v ? 1 : 0
  const r = await api('admin_lottery_prize_save', {
    id: Number(record.id) || 0,
    name: record.name,
    card_type: record.card_type,
    description: record.description || '',
    sort_order: num(record.sort_order),
    is_active: next,
  }, 'POST')
  if (r.code !== 0) {
    if (r.code !== 401) Message.error(r.msg || '操作失败')
    return
  }
  record.is_active = next
  Message.success(next ? '已启用' : '已停用')
  loadPrizes()
}

/** 删除奖项: 二次确认; 已有人中奖时后端会拒绝, 直接把 msg 弹出来 */
function delPrize(record) {
  Modal.warning({
    title: '确认删除该奖项?',
    content: `删除后连同该奖项下未使用的卡密一起删除, 且不可恢复: ${record.name}`,
    okText: '删除',
    cancelText: '取消',
    hideCancel: false,
    okButtonProps: { status: 'danger' },
    onOk: async () => {
      const r = await api('admin_lottery_prize_delete', { id: Number(record.id) || 0 }, 'POST')
      if (r.code !== 0) {
        if (r.code !== 401) Message.error(r.msg || '删除失败')
        return false
      }
      const n = num(r.data && r.data.deleted_codes)
      Message.success(`已删除奖项 (连带删除 ${n} 条卡密)`)
      loadPrizes()
      return true
    },
  })
}

// ============ 上传卡密 ============
const showImport = ref(false)
const importing = ref(false)
const importForm = ref({ prize_id: null, text: '' })
const importLineCount = computed(() =>
  String(importForm.value.text || '')
    .split(/\r?\n/)
    .map((s) => s.trim())
    .filter(Boolean).length
)

function openImport(record) {
  importForm.value = { prize_id: record ? Number(record.id) : null, text: '' }
  showImport.value = true
}

async function doImport() {
  const f = importForm.value
  if (!f.prize_id) { Message.warning('请先选择奖项'); return }
  if (!String(f.text || '').trim()) { Message.warning('请粘贴卡密, 每行一个'); return }
  importing.value = true
  try {
    const r = await api('admin_lottery_codes_import', {
      prize_id: Number(f.prize_id),
      text: f.text,
    }, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error(r.msg || '导入失败')
      return
    }
    const d = r.data || {}
    Message.success(`成功导入 ${num(d.added)} 条，重复 ${num(d.duplicate)} 条，跳过 ${num(d.invalid)} 条`)
    // 导入成功后清空文本域并刷新奖项/卡密列表
    importForm.value = { prize_id: f.prize_id, text: '' }
    await loadPrizes()
    if (showCodes.value) {
      codeFilterPrize.value = Number(f.prize_id)
      codePagination.value.current = 1
      loadCodes()
    }
  } finally {
    importing.value = false
  }
}

// ============ 卡密列表 (抽屉) ============
const showCodes = ref(false)
const codeLoading = ref(false)
const codes = ref([])
const codeTotal = ref(0)
const codeCounts = ref({ unused: 0, used: 0 })
const codeFilterPrize = ref(0)
const codeFilterStatus = ref('all')
const codeKw = ref('')
const codeStatusOptions = [
  { label: '全部状态', value: 'all' },
  { label: '未使用', value: 'unused' },
  { label: '已使用', value: 'used' },
]
const codePagination = ref({
  current: 1,
  pageSize: 20,
  total: 0,
  showTotal: true,
  showPageSize: true,
  pageSizeOptions: [10, 20, 50, 100],
})

function openCodes(record) {
  codeFilterPrize.value = record ? Number(record.id) : 0
  codeFilterStatus.value = 'all'
  codeKw.value = ''
  codePagination.value.current = 1
  showCodes.value = true
  loadCodes()
}

async function loadCodes() {
  codeLoading.value = true
  const params = {
    prize_id: Number(codeFilterPrize.value) || 0,
    status: codeFilterStatus.value || 'all',
    page: codePagination.value.current,
    page_size: codePagination.value.pageSize,
  }
  if (String(codeKw.value || '').trim()) params.keyword = String(codeKw.value).trim()
  let r
  try {
    r = await api('admin_lottery_codes', params)
  } finally {
    codeLoading.value = false
  }
  if (r.code !== 0) {
    codes.value = []
    codeTotal.value = 0
    codePagination.value.total = 0
    if (r.code !== 401) Message.error(r.msg || '卡密列表加载失败')
    return
  }
  const d = r.data || {}
  codes.value = d.list || []
  codeTotal.value = num(d.total)
  codeCounts.value = { unused: num(d.unused), used: num(d.used) }
  codePagination.value.total = codeTotal.value
  if (d.page) codePagination.value.current = Number(d.page)
  if (d.page_size) codePagination.value.pageSize = Number(d.page_size)
}

function searchCodes() {
  codePagination.value.current = 1
  loadCodes()
}
function onCodePageChange(current) {
  codePagination.value.current = current
  loadCodes()
}
function onCodePageSizeChange(pageSize) {
  codePagination.value.pageSize = pageSize
  codePagination.value.current = 1
  loadCodes()
}

/** 删除未使用的卡密: 二次确认 (status 固定传 unused) */
function delUnusedCodes() {
  const prizeTxt = Number(codeFilterPrize.value)
    ? `奖项「${(prizes.value.find((p) => Number(p.id) === Number(codeFilterPrize.value)) || {}).name || codeFilterPrize.value}」`
    : '全部奖项'
  Modal.warning({
    title: '确认删除未使用的卡密?',
    content: `将删除${prizeTxt}下所有「未使用」的卡密 (当前筛选共 ${codeCounts.value.unused} 条), 且无法恢复。`,
    okText: '删除',
    cancelText: '取消',
    hideCancel: false,
    okButtonProps: { status: 'danger' },
    onOk: async () => {
      const r = await api('admin_lottery_codes_delete', {
        prize_id: Number(codeFilterPrize.value) || 0,
        status: 'unused',
      }, 'POST')
      if (r.code !== 0) {
        if (r.code !== 401) Message.error(r.msg || '删除失败')
        return false
      }
      Message.success(`已删除 ${num(r.data && r.data.deleted)} 条未使用卡密`)
      loadCodes()
      loadPrizes()
      return true
    },
  })
}

// ============ 中奖记录 ============
const drawLoading = ref(false)
const draws = ref([])
const drawTotal = ref(0)
const drawFilterPrize = ref(0)
const drawKw = ref('')
const drawPagination = ref({
  current: 1,
  pageSize: 20,
  total: 0,
  showTotal: true,
  showPageSize: true,
  pageSizeOptions: [10, 20, 50, 100],
})

async function loadDraws() {
  drawLoading.value = true
  const params = {
    prize_id: Number(drawFilterPrize.value) || 0,
    page: drawPagination.value.current,
    page_size: drawPagination.value.pageSize,
  }
  if (String(drawKw.value || '').trim()) params.keyword = String(drawKw.value).trim()
  let r
  try {
    r = await api('admin_lottery_draws', params)
  } finally {
    drawLoading.value = false
  }
  if (r.code !== 0) {
    draws.value = []
    drawTotal.value = 0
    drawPagination.value.total = 0
    if (r.code !== 401) Message.error(r.msg || '中奖记录加载失败')
    return
  }
  const d = r.data || {}
  draws.value = d.list || []
  drawTotal.value = num(d.total)
  drawPagination.value.total = drawTotal.value
  if (d.page) drawPagination.value.current = Number(d.page)
  if (d.page_size) drawPagination.value.pageSize = Number(d.page_size)
}

function searchDraws() {
  drawPagination.value.current = 1
  loadDraws()
}
function onDrawPageChange(current) {
  drawPagination.value.current = current
  loadDraws()
}
function onDrawPageSizeChange(pageSize) {
  drawPagination.value.pageSize = pageSize
  drawPagination.value.current = 1
  loadDraws()
}

// ============ 活动设置 ============
const configLoading = ref(false)
const configSaving = ref(false)
const configForm = ref({ enabled: 0, title: '', content: '', per_user_limit: 1, daily_limit: 0 })

async function loadConfig() {
  configLoading.value = true
  let r
  try {
    r = await api('admin_lottery_config_get')
  } finally {
    configLoading.value = false
  }
  if (r.code !== 0) {
    if (r.code !== 401) Message.error(r.msg || '活动配置加载失败')
    return
  }
  const d = r.data || {}
  configForm.value = {
    enabled: Number(d.enabled) ? 1 : 0,
    title: d.title || '',
    content: d.content || '',
    per_user_limit: num(d.per_user_limit),
    daily_limit: num(d.daily_limit),
  }
}

async function saveConfig() {
  const f = configForm.value
  configSaving.value = true
  try {
    const r = await api('admin_lottery_config_set', {
      enabled: Number(f.enabled) ? 1 : 0,
      title: f.title || '',
      content: f.content || '',
      per_user_limit: num(f.per_user_limit),
      daily_limit: num(f.daily_limit),
    }, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error(r.msg || '保存失败')
      return
    }
    Message.success('已保存')
  } finally {
    configSaving.value = false
  }
}

onMounted(() => {
  loadPrizes()
  loadDraws()
  loadConfig()
})
</script>

<style scoped>
.toolbar-spacer { flex: 1 1 auto; min-width: 0; }
.muted { color: var(--color-text-3); font-size: 13px; }
.prize-name { font-weight: 600; }
.stock-zero { color: rgb(var(--danger-6)); font-weight: 600; }
.code-cell { display: flex; align-items: center; gap: 6px; }
.code-cell .mono { flex: 1 1 auto; min-width: 0; }
.config-form { max-width: 640px; }
.code-summary { margin-bottom: 10px; }
.m-pager { justify-content: flex-end; margin-top: 12px; }
.form-tip { font-size: 12px; color: var(--color-text-3); line-height: 1.6; margin-top: 4px; }

@media (max-width: 820px) {
  .toolbar-spacer { display: none; }
  .config-form { max-width: 100%; }
}
</style>
