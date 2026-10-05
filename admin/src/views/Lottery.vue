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
                    <span class="m-card-label">权重</span>
                    <span class="m-card-value">{{ num(p.weight) }}（0 = 不参与）</span>
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

          <!-- 桌面端: 表格; 列宽合计 70+160+110+200+80+80+110+190+170+250 = 1420 -->
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
              <a-table-column title="权重" :width="80">
                <template #cell="{ record }">
                  <span :style="num(record.weight) === 0 ? 'color: var(--color-text-3)' : ''">
                    {{ num(record.weight) }}
                  </span>
                </template>
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
          <!-- ---------- 当前状态条: admin_lottery_window_preview ---------- -->
          <a-card :bordered="true" class="win-state-card">
            <div class="win-state">
              <a-tag :color="windowColor" size="medium">{{ windowStateText }}</a-tag>
              <span v-if="windowState.text" class="muted">时段: {{ windowState.text }}</span>
              <span v-if="windowState.next_open_at" class="muted">下次开放: {{ windowState.next_open_at }}</span>
              <span v-else-if="windowState.reason_text" class="muted">{{ windowState.reason_text }}</span>
              <span v-if="windowState.daily_total_left !== undefined" class="muted">
                今日剩余可发: {{ num(windowState.daily_total_left) }}
              </span>
              <div class="toolbar-spacer"></div>
              <span class="muted">服务器时间: {{ windowState.server_time || '-' }}</span>
              <a-button size="small" :loading="windowLoading" @click="loadWindowState">
                <template #icon><icon-refresh /></template>刷新状态
              </a-button>
            </div>
          </a-card>

          <a-alert type="info" style="margin-bottom: 12px">
            <div><b>下拉/数字项里的 0 都表示「不限」</b>（每日次数、每周次数、冷却秒数、全站每日发放上限）。</div>
            <div>保存会一次性提交本页全部设置；服务端校验失败时会原样显示中文错误提示。</div>
          </a-alert>

          <a-spin :loading="configLoading" style="width: 100%">
            <a-form :model="configForm" layout="vertical" class="config-form">
              <!-- ========== 1. 基本 ========== -->
              <a-card :bordered="false" class="cfg-group">
                <a-divider orientation="left">基本</a-divider>
                <a-form-item label="抽奖开关">
                  <a-switch v-model="configForm.enabled" :checked-value="1" :unchecked-value="0">
                    <template #checked>开启</template>
                    <template #unchecked>关闭</template>
                  </a-switch>
                  <div class="form-tip">关闭后客户端抽奖会提示「抽奖活动已关闭」。</div>
                </a-form-item>
                <a-form-item label="抽奖标题">
                  <a-input v-model="configForm.title" placeholder="例如: 免费抽卡密 (不超过 50 字)" allow-clear />
                </a-form-item>
                <a-form-item label="抽奖内容 / 说明">
                  <a-textarea
                    v-model="configForm.content"
                    placeholder="公众号关注后回复「抽奖」即可参与 (支持换行, 不超过 2000 字)"
                    :auto-size="{ minRows: 4, maxRows: 12 }"
                  />
                </a-form-item>
                <a-form-item label="抽中弹窗文案">
                  <a-input v-model="configForm.success_text" placeholder="留空=客户端用默认" allow-clear />
                  <div class="form-tip">中奖弹窗顶部显示的自定义文案；留空 = 客户端用默认文案，不超过 200 字。</div>
                </a-form-item>
                <a-form-item label="抽完提示文案">
                  <a-input v-model="configForm.empty_text" placeholder="奖品已抽完, 请稍后再来" allow-clear />
                  <div class="form-tip">奖品池抽空时客户端显示的提示；默认「奖品已抽完, 请稍后再来」，不超过 200 字。</div>
                </a-form-item>
              </a-card>

              <!-- ========== 2. 次数与重置 ========== -->
              <a-card :bordered="false" class="cfg-group">
                <a-divider orientation="left">次数与重置</a-divider>
                <a-row :gutter="12">
                  <a-col :span="isMobile ? 24 : 12">
                    <a-form-item label="每人总次数">
                      <a-input-number
                        v-model="configForm.per_user_limit"
                        :min="0"
                        :max="9999"
                        :precision="0"
                        style="width: 100%"
                      />
                      <div class="form-tip">0 = 默认每人不能抽；单个用户可在「用户管理 → 设置抽奖次数」里覆盖。</div>
                    </a-form-item>
                  </a-col>
                  <a-col :span="isMobile ? 24 : 12">
                    <a-form-item label="每人每日次数">
                      <a-input-number
                        v-model="configForm.daily_limit"
                        :min="0"
                        :max="999"
                        :precision="0"
                        style="width: 100%"
                      />
                      <div class="form-tip"><b>0 = 不限</b>；1-999 = 每人每天最多抽这么多次。</div>
                    </a-form-item>
                  </a-col>
                  <a-col :span="isMobile ? 24 : 12">
                    <a-form-item label="每人每周次数">
                      <a-input-number
                        v-model="configForm.week_limit"
                        :min="0"
                        :max="999"
                        :precision="0"
                        style="width: 100%"
                      />
                      <div class="form-tip"><b>0 = 不限</b>；1-999 = 每人每周（周一起算）最多抽这么多次。</div>
                    </a-form-item>
                  </a-col>
                  <a-col :span="isMobile ? 24 : 12">
                    <a-form-item label="每日重置时间">
                      <a-time-picker
                        v-model="configForm.daily_reset_time"
                        format="HH:mm"
                        :allow-clear="false"
                        placeholder="00:00"
                        style="width: 100%"
                      />
                      <div class="form-tip">格式 HH:mm，默认 00:00；决定「今日 / 本周」的起算边界。</div>
                    </a-form-item>
                  </a-col>
                  <a-col :span="isMobile ? 24 : 12">
                    <a-form-item label="两次抽奖冷却秒数">
                      <a-input-number
                        v-model="configForm.cooldown_seconds"
                        :min="0"
                        :max="86400"
                        :precision="0"
                        style="width: 100%"
                      />
                      <div class="form-tip"><b>0 = 不限</b>；两次抽奖的最小间隔，最大 86400 秒。</div>
                    </a-form-item>
                  </a-col>
                </a-row>
              </a-card>

              <!-- ========== 3. 开放时段 ========== -->
              <a-card :bordered="false" class="cfg-group">
                <a-divider orientation="left">开放时段</a-divider>
                <a-form-item label="启用自定义开放时段">
                  <a-switch v-model="configForm.windows_enabled" :checked-value="1" :unchecked-value="0">
                    <template #checked>启用</template>
                    <template #unchecked>关闭</template>
                  </a-switch>
                  <div class="form-tip">关闭或留空 = 全天都能抽，例：每天 19:30-20:00</div>
                </a-form-item>
                <div class="win-list">
                  <div class="win-head">
                    <span class="muted">最多 10 条；不选星期 = 每天；结束时间不晚于开始时间 = 跨天到次日；多条取并集。</span>
                    <a-button size="small" :disabled="configForm.windows.length >= 10" @click="addWindow">
                      <template #icon><icon-plus /></template>添加时段
                    </a-button>
                  </div>
                  <a-empty v-if="!configForm.windows.length" description="还没有时段, 点「添加时段」" />
                  <div v-for="(w, i) in configForm.windows" :key="i" class="win-row">
                    <a-time-picker
                      v-model="w.start"
                      format="HH:mm"
                      :allow-clear="false"
                      placeholder="开始 19:30"
                      class="win-time"
                    />
                    <span class="win-sep muted">至</span>
                    <a-time-picker
                      v-model="w.end"
                      format="HH:mm"
                      :allow-clear="false"
                      placeholder="结束 20:00"
                      class="win-time"
                    />
                    <a-checkbox-group v-model="w.days" :options="dayOptions" class="win-days" />
                    <a-button type="text" status="danger" size="small" @click="removeWindow(i)">删除</a-button>
                  </div>
                  <div v-if="configForm.windows.length" class="form-tip">
                    共 {{ configForm.windows.length }} / 10 条时段；不选星期表示每天都生效。
                  </div>
                </div>
              </a-card>

              <!-- ========== 4. 活动有效期 ========== -->
              <a-card :bordered="false" class="cfg-group">
                <a-divider orientation="left">活动有效期</a-divider>
                <a-row :gutter="12">
                  <a-col :span="isMobile ? 24 : 12">
                    <a-form-item label="开始日期">
                      <a-date-picker
                        v-model="configForm.start_date"
                        format="YYYY-MM-DD"
                        value-format="YYYY-MM-DD"
                        placeholder="留空 = 不限"
                        allow-clear
                        style="width: 100%"
                      />
                    </a-form-item>
                  </a-col>
                  <a-col :span="isMobile ? 24 : 12">
                    <a-form-item label="结束日期">
                      <a-date-picker
                        v-model="configForm.end_date"
                        format="YYYY-MM-DD"
                        value-format="YYYY-MM-DD"
                        placeholder="留空 = 不限 (含当天 23:59:59)"
                        allow-clear
                        style="width: 100%"
                      />
                    </a-form-item>
                  </a-col>
                </a-row>
                <div class="form-tip">两个都留空 = 不限日期；结束日期不能早于开始日期。</div>
              </a-card>

              <!-- ========== 5. 发放与展示 ========== -->
              <a-card :bordered="false" class="cfg-group">
                <a-divider orientation="left">发放与展示</a-divider>
                <a-form-item label="全站每日发放上限">
                  <a-input-number
                    v-model="configForm.daily_total_limit"
                    :min="0"
                    :max="999999"
                    :precision="0"
                    style="width: 100%"
                  />
                  <div class="form-tip"><b>0 = 不限</b>；今天全站最多发出这么多张卡密，最大 999999。</div>
                </a-form-item>
                <a-row :gutter="12">
                  <a-col :span="isMobile ? 24 : 8">
                    <a-form-item label="客户端显示奖项列表">
                      <a-switch v-model="configForm.show_prizes" :checked-value="1" :unchecked-value="0">
                        <template #checked>显示</template>
                        <template #unchecked>隐藏</template>
                      </a-switch>
                    </a-form-item>
                  </a-col>
                  <a-col :span="isMobile ? 24 : 8">
                    <a-form-item label="显示剩余数量">
                      <a-switch v-model="configForm.show_stock" :checked-value="1" :unchecked-value="0">
                        <template #checked>显示</template>
                        <template #unchecked>隐藏</template>
                      </a-switch>
                    </a-form-item>
                  </a-col>
                  <a-col :span="isMobile ? 24 : 8">
                    <a-form-item label="中奖发系统通知">
                      <a-switch v-model="configForm.notify_winner" :checked-value="1" :unchecked-value="0">
                        <template #checked>发送</template>
                        <template #unchecked>不发送</template>
                      </a-switch>
                      <div class="form-tip">关闭后中奖不再往消息中心插入含卡密的通知。</div>
                    </a-form-item>
                  </a-col>
                </a-row>
              </a-card>

              <a-form-item>
                <a-button type="primary" :loading="configSaving" @click="saveConfig">保存全部设置</a-button>
                <a-button :loading="configLoading" style="margin-left: 8px" @click="loadConfig">刷新</a-button>
              </a-form-item>
            </a-form>
          </a-spin>
        </a-tab-pane>
      </a-tabs>
    </a-card>

    <!-- ============ 重置与清空 ============ -->
    <a-card :bordered="false" class="page-card reset-card">
      <template #title>重置与清空</template>
      <a-alert type="warning" class="reset-alert">
        重置只影响次数和记录，不影响奖项和卡密池；整个活动重开会把已发出去的卡密收回池子，请谨慎操作。
      </a-alert>

      <div class="reset-switches">
        <span class="reset-switch">
          <a-switch v-model="resetReleaseCodes" />
          <span>把已发卡密退回卡密池</span>
        </span>
        <span class="reset-switch">
          <a-switch v-model="resetClearNotices" />
          <span>删除含卡密的通知</span>
        </span>
        <span class="muted">(两个开关仅在「整个活动重开」时生效)</span>
      </div>

      <div class="reset-actions">
        <a-button
          :loading="resetting === 'quota'"
          :disabled="!!resetting && resetting !== 'quota'"
          @click="doReset('quota')"
        >
          只清所有人总次数
        </a-button>
        <a-button
          :loading="resetting === 'daily'"
          :disabled="!!resetting && resetting !== 'daily'"
          @click="doReset('daily')"
        >
          只清所有人今日次数
        </a-button>
        <a-button
          status="danger"
          :loading="resetting === 'activity'"
          :disabled="!!resetting && resetting !== 'activity'"
          @click="doReset('activity')"
        >
          整个活动重开
        </a-button>
      </div>
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
          <a-col :span="isMobile ? 24 : 8">
            <a-form-item label="排序">
              <a-input-number v-model="prizeForm.sort_order" :min="0" :precision="0" style="width: 100%" />
            </a-form-item>
          </a-col>
          <a-col :span="isMobile ? 24 : 8">
            <a-form-item label="权重">
              <a-input-number v-model="prizeForm.weight" :min="0" :max="1000" :precision="0" style="width: 100%" />
            </a-form-item>
          </a-col>
          <a-col :span="isMobile ? 24 : 8">
            <a-form-item label="状态">
              <a-switch v-model="prizeForm.is_active" :checked-value="1" :unchecked-value="0">
                <template #checked>启用</template>
                <template #unchecked>停用</template>
              </a-switch>
            </a-form-item>
          </a-col>
        </a-row>
        <div class="form-tip">排序越小越靠前；停用或库存为 0 的奖项不会出现在客户端抽奖列表里。</div>
        <div class="form-tip">权重越大越容易被抽到；0 = 不参与（默认 100，最大 1000）。</div>
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
import { computed, h, onMounted, ref } from 'vue'
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
const emptyPrizeForm = () => ({ id: 0, name: '', card_type: '天卡', description: '', sort_order: 0, weight: 100, is_active: 1 })
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
      weight: (record.weight === undefined || record.weight === null || record.weight === '')
        ? 100
        : num(record.weight),
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
      weight: num(f.weight),
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
    weight: (record.weight === undefined || record.weight === null || record.weight === '')
      ? 100
      : num(record.weight),
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

/** 星期多选: 1=周一 ... 7=周日 (不选 = 每天) */
const dayOptions = [
  { label: '周一', value: 1 },
  { label: '周二', value: 2 },
  { label: '周三', value: 3 },
  { label: '周四', value: 4 },
  { label: '周五', value: 5 },
  { label: '周六', value: 6 },
  { label: '周日', value: 7 },
]

/** 时间统一成 HH:mm; 非法值回 fallback */
function normHm(v, fallback) {
  const s = String(v === undefined || v === null ? '' : v).trim()
  const m = s.match(/^(\d{1,2}):(\d{1,2})/)
  if (!m) return fallback === undefined ? '' : fallback
  const h = Math.min(23, Math.max(0, parseInt(m[1], 10)))
  const mi = Math.min(59, Math.max(0, parseInt(m[2], 10)))
  return String(h).padStart(2, '0') + ':' + String(mi).padStart(2, '0')
}

/** 日期统一成 YYYY-MM-DD; 非法/空 → '' */
function normDate(v) {
  const s = String(v === undefined || v === null ? '' : v).trim()
  const m = s.match(/^(\d{4})-(\d{2})-(\d{2})/)
  return m ? m[1] + '-' + m[2] + '-' + m[3] : ''
}

const emptyWindow = () => ({ start: '', end: '', days: [] })

const emptyConfigForm = () => ({
  enabled: 0,
  title: '',
  content: '',
  success_text: '',
  empty_text: '奖品已抽完, 请稍后再来',
  per_user_limit: 1,
  daily_limit: 0,
  week_limit: 0,
  daily_reset_time: '00:00',
  cooldown_seconds: 0,
  windows_enabled: 0,
  windows: [],
  start_date: '',
  end_date: '',
  daily_total_limit: 0,
  show_prizes: 1,
  show_stock: 1,
  notify_winner: 1,
})
const configForm = ref(emptyConfigForm())

/** 服务端返回的配置 → 表单 (缺字段/非法值一律回默认, 避免把 undefined 塞给组件) */
function pickConfig(d) {
  const f = emptyConfigForm()
  const s = d || {}
  f.enabled = Number(s.enabled) ? 1 : 0
  f.title = s.title || ''
  f.content = s.content || ''
  f.success_text = s.success_text || ''
  f.empty_text = s.empty_text || '奖品已抽完, 请稍后再来'
  f.per_user_limit = num(s.per_user_limit)
  f.daily_limit = num(s.daily_limit)
  f.week_limit = num(s.week_limit)
  f.daily_reset_time = normHm(s.daily_reset_time, '00:00')
  f.cooldown_seconds = num(s.cooldown_seconds)
  f.windows_enabled = Number(s.windows_enabled) ? 1 : 0
  f.windows = Array.isArray(s.windows)
    ? s.windows.slice(0, 10).map((w) => ({
        start: normHm(w && w.start, ''),
        end: normHm(w && w.end, ''),
        days: Array.isArray(w && w.days)
          ? w.days.map((x) => Number(x)).filter((x) => x >= 1 && x <= 7)
          : [],
      }))
    : []
  f.start_date = normDate(s.start_date)
  f.end_date = normDate(s.end_date)
  f.daily_total_limit = num(s.daily_total_limit)
  f.show_prizes = s.show_prizes === undefined ? 1 : (Number(s.show_prizes) ? 1 : 0)
  f.show_stock = s.show_stock === undefined ? 1 : (Number(s.show_stock) ? 1 : 0)
  f.notify_winner = s.notify_winner === undefined ? 1 : (Number(s.notify_winner) ? 1 : 0)
  return f
}

function addWindow() {
  if (configForm.value.windows.length >= 10) { Message.warning('抽奖时段最多 10 条'); return }
  configForm.value.windows.push(emptyWindow())
}

function removeWindow(index) {
  configForm.value.windows.splice(index, 1)
}

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
  configForm.value = pickConfig(r.data)
}

/** 一次性提交全部字段; 失败时把服务端中文错误原文原样弹出来 */
async function saveConfig() {
  const f = configForm.value
  // 不论开关是否打开都校验: 关闭时也会把已配好的时段原样存回去, 避免开关一关就丢配置
  for (let i = 0; i < f.windows.length; i++) {
    const w = f.windows[i]
    if (!w.start || !w.end) { Message.warning('第 ' + (i + 1) + ' 个时段请把开始/结束时间都选上'); return }
    if (w.start === w.end) { Message.warning('第 ' + (i + 1) + ' 个时段的开始和结束时间不能相同'); return }
  }
  if (f.start_date && f.end_date && f.end_date < f.start_date) {
    Message.warning('结束日期不能早于开始日期')
    return
  }
  const payload = {
    enabled: Number(f.enabled) ? 1 : 0,
    title: f.title || '',
    content: f.content || '',
    success_text: f.success_text || '',
    empty_text: f.empty_text || '',
    per_user_limit: num(f.per_user_limit),
    daily_limit: num(f.daily_limit),
    week_limit: num(f.week_limit),
    daily_reset_time: normHm(f.daily_reset_time, '00:00'),
    cooldown_seconds: num(f.cooldown_seconds),
    windows_enabled: Number(f.windows_enabled) ? 1 : 0,
    windows: f.windows.map((w) => ({
      start: normHm(w.start, '00:00'),
      end: normHm(w.end, '00:00'),
      days: Array.isArray(w.days) ? w.days.map((x) => Number(x)).filter((x) => x >= 1 && x <= 7) : [],
    })),
    start_date: normDate(f.start_date),
    end_date: normDate(f.end_date),
    daily_total_limit: num(f.daily_total_limit),
    show_prizes: Number(f.show_prizes) ? 1 : 0,
    show_stock: Number(f.show_stock) ? 1 : 0,
    notify_winner: Number(f.notify_winner) ? 1 : 0,
  }
  configSaving.value = true
  try {
    const r = await api('admin_lottery_config_set', payload, 'POST')
    if (r.code !== 0) {
      if (r.code !== 401) Message.error(r.msg || '保存失败')
      return
    }
    // 回显服务端归一化后的配置
    if (r.data) configForm.value = pickConfig(r.data)
    Message.success('已保存')
    loadWindowState()
  } finally {
    configSaving.value = false
  }
}

// ============ 当前状态 (开放时段预览) ============
const windowLoading = ref(false)
const windowState = ref({})

/** 服务端 reason → 中文状态; 接口未部署时显示「状态未知」 */
const windowStateText = computed(() => {
  const r = String(windowState.value.reason || '')
  if (r === 'open') return '开放中'
  if (r === 'outside_window') return '未在开放时间'
  if (r === 'before_start') return '活动还没开始'
  if (r === 'after_end') return '活动已经结束'
  if (r === 'disabled') return '活动已关闭'
  return '状态未知'
})

const windowColor = computed(() => {
  const r = String(windowState.value.reason || '')
  if (r === 'open') return 'green'
  if (r === 'disabled') return 'gray'
  return 'orange'
})

async function loadWindowState() {
  windowLoading.value = true
  let r
  try {
    r = await api('admin_lottery_window_preview', {}, 'POST')
  } finally {
    windowLoading.value = false
  }
  if (r.code !== 0) {
    windowState.value = {}
    if (r.code !== 401) Message.warning(r.msg || '抽奖状态获取失败')
    return
  }
  windowState.value = r.data || {}
}

// ============ 重置与清空 ============
const resetting = ref('')
const resetReleaseCodes = ref(true)
const resetClearNotices = ref(true)

/** 三档操作的二次确认文案 */
const RESET_CONFIRM = {
  quota: '重置后所有用户都可以重新抽满次数，中奖记录与已发卡密保留。此操作不可撤销。',
  daily: '重置后所有用户今天可以重新抽，总次数不受影响。',
  activity:
    '将删除所有中奖记录、把所有已发出去的卡密退回卡密池、并删除消息中心里含卡密的通知。' +
    '用户自己保存过的卡密截图不会被收回。此操作不可撤销！',
}

/** 危险文案用红色渲染 (Modal content 支持渲染函数) */
function dangerText(text) {
  return h('div', { style: 'color: rgb(var(--danger-6)); font-weight: 600; line-height: 1.75;' }, text)
}

/**
 * 重置抽奖活动数据
 *   mode=quota    只清所有人「总抽奖次数」(中奖记录与已发卡密保留)
 *   mode=daily    只清所有人「今日已抽次数」(总次数不变, 今天能重新抽)
 *   mode=activity 整个活动重开 (可选退回已发卡密 / 删除含卡密通知)
 * 注意: action 必须放 URL 查询串, 由 api() 封装处理, 这里只管传参。
 */
function doReset(mode) {
  if (resetting.value) return
  const isActivity = mode === 'activity'
  Modal.warning({
    title: isActivity
      ? '确认整个活动重开?'
      : (mode === 'daily' ? '确认只清所有人今日次数?' : '确认只清所有人总次数?'),
    // 这里传的是 VNode(不是函数): 方法式 Modal 会把函数直接当 slot 渲染函数调用并传入 props
    content: isActivity ? dangerText(RESET_CONFIRM.activity) : (RESET_CONFIRM[mode] || ''),
    okText: isActivity ? '重开活动' : '确认重置',
    cancelText: '取消',
    hideCancel: false,
    okButtonProps: isActivity ? { status: 'danger' } : undefined,
    // 用 onBeforeOk: 请求期间「确认」按钮自带 loading, 失败(返回 false)时弹窗保持打开
    onBeforeOk: async () => {
      resetting.value = mode
      try {
        const params = { mode }
        if (isActivity) {
          params.release_codes = resetReleaseCodes.value ? 1 : 0
          params.clear_notices = resetClearNotices.value ? 1 : 0
        }
        const r = await api('admin_lottery_activity_reset', params, 'POST')
        if (r.code !== 0) {
          if (r.code !== 401) Message.error(r.msg || '重置失败')
          return false
        }
        const d = r.data || {}
        Message.success(
          '已重置 ' + num(d.users) + ' 个用户；删除中奖记录 ' + num(d.draws_deleted) + ' 条；' +
          '退回卡密 ' + num(d.codes_released) + ' 张；删除通知 ' + num(d.notices_deleted) + ' 条'
        )
        loadDraws()
        loadPrizes()
        return true
      } finally {
        resetting.value = ''
      }
    },
  })
}

onMounted(() => {
  loadPrizes()
  loadDraws()
  loadConfig()
  loadWindowState()
})
</script>

<style scoped>
.toolbar-spacer { flex: 1 1 auto; min-width: 0; }
.muted { color: var(--color-text-3); font-size: 13px; }
.prize-name { font-weight: 600; }
.stock-zero { color: rgb(var(--danger-6)); font-weight: 600; }
.code-cell { display: flex; align-items: center; gap: 6px; }
.code-cell .mono { flex: 1 1 auto; min-width: 0; }
.config-form { max-width: 900px; }
.code-summary { margin-bottom: 10px; }
.m-pager { justify-content: flex-end; margin-top: 12px; }
.form-tip { font-size: 12px; color: var(--color-text-3); line-height: 1.6; margin-top: 4px; }

/* ---- 活动设置: 当前状态条 / 分组卡片 / 开放时段 ---- */
.win-state-card { margin-bottom: 12px; }
.win-state { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.cfg-group { margin-bottom: 12px; background: var(--color-fill-1); }
.cfg-group :deep(.arco-divider) { margin: 0 0 14px; }
.cfg-group :deep(.arco-divider-text) { font-weight: 600; }
.win-list { margin-bottom: 6px; }
.win-head { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 8px; }
.win-row { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-bottom: 8px; }
.win-time { width: 128px; }
.win-sep { font-size: 12px; }
.win-days { display: flex; flex-wrap: wrap; gap: 10px; }

.reset-card { margin-top: 16px; }
.reset-alert { margin-bottom: 14px; }
.reset-switches { display: flex; flex-wrap: wrap; align-items: center; gap: 20px; margin-bottom: 16px; }
.reset-switch { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; color: var(--color-text-2); }
.reset-actions { display: flex; flex-wrap: wrap; gap: 10px; }

@media (max-width: 820px) {
  .toolbar-spacer { display: none; }
  .config-form { max-width: 100%; }
  .reset-actions { flex-direction: column; }
  .reset-switches { gap: 12px; }
}
</style>
