<template>
  <div class="dashboard-container">
    <div class="dashboard-header">
      <h1>压力测试控制台</h1>
      <div class="header-buttons">
        <el-button @click="goHelp">
          <el-icon><QuestionFilled /></el-icon>
          使用说明
        </el-button>
        <el-button @click="goSettings">
          <el-icon><Setting /></el-icon>
          设置
        </el-button>
        <el-button @click="handleLogout">
          <el-icon><SwitchButton /></el-icon>
          退出登录
        </el-button>
      </div>
    </div>

    <div class="dashboard-content">
      <!-- Agent 列表 -->
      <el-card class="section-card" shadow="hover">
        <template #header>
          <div class="section-header">
            <span>在线 Agent</span>
            <div class="section-actions">
              <el-button size="small" @click="selectAll">全选</el-button>
              <el-button size="small" @click="invertSelect">反选</el-button>
              <el-button size="small" :type="sortMode ? 'primary' : ''" @click="toggleSortMode">
                <el-icon><Sort /></el-icon>
                {{ sortMode ? '完成排序' : '排序' }}
              </el-button>
            </div>
          </div>
        </template>

        <!-- 排序模式 -->
        <draggable 
          v-if="sortMode"
          :list="agents" 
          item-key="id"
          handle=".drag-handle"
          ghost-class="ghost"
          @end="onDragEnd"
        >
          <template #item="{ element }">
            <div class="agent-row">
              <el-icon class="drag-handle"><Rank /></el-icon>
              <el-checkbox v-model="element.selected" />
              <span class="agent-name">{{ element.name }}</span>
              <el-tag :type="element.running ? 'success' : 'info'" size="small">
                {{ element.running ? '运行中' : '空闲' }}
              </el-tag>
            </div>
          </template>
        </draggable>

        <!-- 普通模式 -->
        <el-table v-else :data="agents" style="width: 100%" size="default">
          <el-table-column width="50">
            <template #default="{ row }">
              <el-checkbox 
                v-model="row.selected" 
                @change="(val) => onSelectionChange(row.id, val)"
              />
            </template>
          </el-table-column>
          <el-table-column prop="name" label="名称" min-width="150">
            <template #default="{ row }">
              <span>{{ row.name }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="status" label="状态" width="100">
            <template #default="{ row }">
              <el-tag :type="row.running ? 'success' : 'info'" size="small">
                {{ row.running ? '运行中' : '空闲' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="120">
            <template #default="{ row }">
              <el-button
                link
                size="small"
                @click="editName(row)"
              >
                编辑
              </el-button>
              <el-button
                v-if="row.running"
                type="warning"
                link
                size="small"
                @click="stopSingle(row)"
              >
                停止
              </el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-card>

      <!-- 任务配置 -->
      <el-card class="section-card" shadow="hover">
        <template #header>
          <div class="section-header">
            <span>任务配置</span>
          </div>
        </template>

        <el-form :model="taskForm" label-width="120px" class="task-form">
          <!-- 模式选择 -->
          <el-form-item label="模式">
            <el-select v-model="taskForm.mode" placeholder="请选择模式" style="width: 100%" @change="onModeChange">
              <el-option label="TCP" value="tcp" />
              <el-option label="UDP" value="udp" />
              <el-option label="HTTP" value="http" />
              <el-option label="Minecraft" value="mc" />
              <el-option label="Flood" value="flood" />
              <el-option label="ICMP" value="icmp" />
            </el-select>
          </el-form-item>

          <!-- 目标地址 -->
          <el-form-item label="目标地址">
            <el-input v-model="taskForm.target" :placeholder="targetPlaceholder" />
          </el-form-item>

          <!-- 并发数 -->
          <el-form-item label="并发数">
            <el-input-number v-model="taskForm.workers" :min="1" :max="10000" style="width: 100%" />
          </el-form-item>

          <!-- 包大小 (tcp/udp/http) -->
          <el-form-item v-if="['tcp', 'udp', 'http'].includes(taskForm.mode)" label="包大小">
            <el-input v-model="taskForm.size" placeholder="字节数，例如: 1400">
              <template #append>字节</template>
            </el-input>
          </el-form-item>

          <!-- 限速 (tcp/udp/http) -->
          <el-form-item v-if="['tcp', 'udp', 'http'].includes(taskForm.mode)" label="限速">
            <el-input v-model="taskForm.rate" placeholder="MB/s，0表示不限速">
              <template #append>MB/s</template>
            </el-input>
          </el-form-item>

          <!-- 时长 -->
          <el-form-item label="时长">
            <el-input v-model="taskForm.duration" placeholder="秒，0表示无限时长">
              <template #append>秒</template>
            </el-input>
          </el-form-item>

          <!-- HTTP 专属参数 -->
          <template v-if="taskForm.mode === 'http'">
            <el-divider content-position="left">HTTP 参数</el-divider>
            <el-form-item label="请求方法">
              <el-select v-model="taskForm.httpMethod" style="width: 100%">
                <el-option label="POST" value="POST" />
                <el-option label="GET" value="GET" />
                <el-option label="PUT" value="PUT" />
                <el-option label="DELETE" value="DELETE" />
              </el-select>
            </el-form-item>
            <el-form-item label="跳过证书验证">
              <el-switch v-model="taskForm.httpTlsSkipVerify" />
              <span class="form-tip">请求 https 时跳过证书校验</span>
            </el-form-item>
          </template>

          <!-- UDP 专属参数 -->
          <template v-if="taskForm.mode === 'udp'">
            <el-divider content-position="left">UDP 参数</el-divider>
            <el-form-item label="已连接Socket">
              <el-switch v-model="taskForm.udpConnected" />
              <span class="form-tip">性能更好</span>
            </el-form-item>
          </template>

          <!-- Minecraft 专属参数 -->
          <template v-if="taskForm.mode === 'mc'">
            <el-divider content-position="left">Minecraft 参数</el-divider>
            <el-form-item label="动作">
              <el-select v-model="taskForm.mcAction" style="width: 100%">
                <el-option label="login - 模拟玩家登录并挂机" value="login" />
                <el-option label="status - 仅查服状态" value="status" />
              </el-select>
            </el-form-item>
            <el-form-item label="协议版本">
              <el-input-number v-model="taskForm.mcProtocol" :min="1" :max="9999" style="width: 100%" />
              <span class="form-tip">763 = 1.20.1</span>
            </el-form-item>
            <el-form-item label="用户名前缀">
              <el-input v-model="taskForm.mcUsernamePrefix" placeholder="stress" />
            </el-form-item>
            <el-form-item label="服务器地址">
              <el-input v-model="taskForm.mcServerAddress" placeholder="默认取目标地址的host部分" />
              <span class="form-tip">一般无需改动</span>
            </el-form-item>
            <el-form-item v-if="taskForm.mcAction === 'login'" label="保活">
              <el-switch v-model="taskForm.mcKeepalive" />
              <span class="form-tip">登录后响应KeepAlive维持连接</span>
            </el-form-item>
            <el-form-item label="自动重连">
              <el-switch v-model="taskForm.mcReconnect" />
              <span class="form-tip">断线后自动重连</span>
            </el-form-item>
            <el-form-item label="KeepAlive ID">
              <el-input-number v-model="taskForm.mcKeepaliveServerboundId" :min="0" :max="255" style="width: 100%" />
              <span class="form-tip">Serverbound KeepAlive包ID，随协议版本变化</span>
            </el-form-item>
          </template>

          <!-- Flood 专属参数 -->
          <template v-if="taskForm.mode === 'flood'">
            <el-divider content-position="left">Flood 参数</el-divider>
            <el-form-item label="协议">
              <el-select v-model="taskForm.floodProto" style="width: 100%">
                <el-option label="TCP" value="tcp" />
                <el-option label="UDP" value="udp" />
                <el-option label="HTTP" value="http" />
              </el-select>
            </el-form-item>
            <el-form-item label="包大小">
              <el-input v-model="taskForm.floodSize" placeholder="字节数">
                <template #append>字节</template>
              </el-input>
              <span class="form-tip">小包提高pps</span>
            </el-form-item>
            <el-form-item v-if="taskForm.floodProto === 'tcp'" label="关闭Nagle">
              <el-switch v-model="taskForm.floodNodelay" />
              <span class="form-tip">TCP_NODELAY，立即发送，pps更高</span>
            </el-form-item>
            <el-form-item label="每次重新建连">
              <el-switch v-model="taskForm.floodChurn" />
              <span class="form-tip">TCP:重新握手; UDP:重新建socket; HTTP:不用keep-alive</span>
            </el-form-item>
            <el-form-item v-if="taskForm.floodProto === 'http'" label="HTTP路径">
              <el-input v-model="taskForm.floodHttpPath" placeholder="/" />
            </el-form-item>
            <el-form-item v-if="taskForm.floodProto === 'http'" label="跳过证书验证">
              <el-switch v-model="taskForm.floodTlsSkipVerify" />
            </el-form-item>
            <el-form-item v-if="taskForm.floodProto === 'udp' && !taskForm.floodChurn" label="批量发送">
              <el-input-number v-model="taskForm.floodBatch" :min="1" :max="1024" style="width: 100%" />
              <span class="form-tip">sendmmsg批量包数(仅Linux)</span>
            </el-form-item>
          </template>

          <!-- ICMP 专属参数 -->
          <template v-if="taskForm.mode === 'icmp'">
            <el-divider content-position="left">ICMP 参数</el-divider>
            <el-form-item label="包大小">
              <el-input v-model="taskForm.icmpSize" placeholder="字节数">
                <template #append>字节</template>
              </el-input>
              <span class="form-tip">默认56字节（与标准ping一致）</span>
            </el-form-item>
            <el-form-item label="限速">
              <el-input v-model="taskForm.icmpRatePps" placeholder="包/秒，0表示不限速">
                <template #append>pps</template>
              </el-input>
              <span class="form-tip">类似ping -f</span>
            </el-form-item>
            <el-form-item label="超时时间">
              <el-input v-model="taskForm.icmpTimeout" placeholder="秒">
                <template #append>秒</template>
              </el-input>
              <span class="form-tip">等待Echo Reply的超时时间</span>
            </el-form-item>
          </template>

          <!-- 操作按钮 -->
          <el-form-item>
            <div class="task-buttons">
              <el-button
                type="primary"
                size="large"
                :loading="startLoading"
                @click="handleStart"
              >
                <el-icon><VideoPlay /></el-icon>
                开始压测
              </el-button>
              <el-button
                type="danger"
                size="large"
                :loading="stopLoading"
                @click="handleStop"
              >
                <el-icon><VideoPause /></el-icon>
                停止全部
              </el-button>
            </div>
          </el-form-item>
        </el-form>
      </el-card>

      <!-- 实时统计 -->
      <el-card class="section-card" shadow="hover">
        <template #header>
          <div class="section-header">
            <span>实时统计</span>
          </div>
        </template>

        <div class="stats-summary">
          <div class="stat-item">
            <div class="stat-value">{{ formatBytes(stats.totalBytesPerSec) }}/s</div>
            <div class="stat-label">每秒吞吐</div>
          </div>
          <div class="stat-item">
            <div class="stat-value">{{ formatBytes(stats.totalBytes) }}</div>
            <div class="stat-label">累计吞吐</div>
          </div>
          <div class="stat-item">
            <div class="stat-value">{{ formatNumber(stats.totalPacketsPerSec) }} pkt/s</div>
            <div class="stat-label">每秒包速率</div>
          </div>
          <div class="stat-item">
            <div class="stat-value">{{ formatNumber(stats.totalPackets) }}</div>
            <div class="stat-label">累计包数</div>
          </div>
          <div class="stat-item">
            <div class="stat-value">{{ stats.totalErrors }}</div>
            <div class="stat-label">总错误</div>
          </div>
          <div class="stat-item">
            <div class="stat-value">{{ stats.uptime || '00:00:00' }}</div>
            <div class="stat-label">运行时间</div>
          </div>
        </div>

        <!-- ICMP 模式显示 -->
        <div v-if="taskForm.mode === 'icmp'" class="stats-summary">
          <div class="stat-item">
            <div class="stat-value">{{ stats.icmpSent || 0 }}</div>
            <div class="stat-label">已发送</div>
          </div>
          <div class="stat-item">
            <div class="stat-value">{{ stats.icmpReceived || 0 }}</div>
            <div class="stat-label">已收到</div>
          </div>
          <div class="stat-item">
            <div class="stat-value">{{ stats.icmpLoss || '0.00' }}%</div>
            <div class="stat-label">丢包率</div>
          </div>
          <div class="stat-item">
            <div class="stat-value">{{ stats.icmpRttAvg || '0.00' }} ms</div>
            <div class="stat-label">RTT 平均</div>
          </div>
          <div class="stat-item">
            <div class="stat-value">{{ stats.icmpRttMin || '0.00' }} ms</div>
            <div class="stat-label">RTT 最小</div>
          </div>
          <div class="stat-item">
            <div class="stat-value">{{ stats.icmpRttMax || '0.00' }} ms</div>
            <div class="stat-label">RTT 最大</div>
          </div>
        </div>

        <!-- MC 模式显示 -->
        <div v-if="taskForm.mode === 'mc'" class="stats-summary">
          <div class="stat-item">
            <div class="stat-value">{{ stats.mcActive || 0 }}</div>
            <div class="stat-label">当前连接</div>
          </div>
          <div class="stat-item">
            <div class="stat-value">{{ stats.mcLoginOk || 0 }}</div>
            <div class="stat-label">登录成功</div>
          </div>
          <div class="stat-item">
            <div class="stat-value">{{ stats.mcLoginFail || 0 }}</div>
            <div class="stat-label">登录失败</div>
          </div>
          <div class="stat-item">
            <div class="stat-value">{{ stats.mcKeepalives || 0 }}</div>
            <div class="stat-label">KeepAlive</div>
          </div>
        </div>

        <el-table :data="agentStats" style="width: 100%" size="default">
          <el-table-column prop="name" label="Agent" min-width="120" />
          <el-table-column prop="status" label="状态" width="100">
            <template #default="{ row }">
              <el-tag :type="row.running ? 'success' : 'info'" size="small">
                {{ row.running ? '运行中' : '空闲' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="吞吐" width="120">
            <template #default="{ row }">
              {{ row.running ? formatBytes(row.bytesPerSec) + '/s' : '-' }}
            </template>
          </el-table-column>
          <el-table-column label="包速率" width="120">
            <template #default="{ row }">
              {{ row.running ? formatNumber(row.packetsPerSec) + ' pkt/s' : '-' }}
            </template>
          </el-table-column>
          <el-table-column label="错误" width="80">
            <template #default="{ row }">
              {{ row.running ? row.errors : '-' }}
            </template>
          </el-table-column>
        </el-table>
      </el-card>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Setting, Edit, VideoPlay, VideoPause, QuestionFilled, SwitchButton, Sort, Rank } from '@element-plus/icons-vue'
import draggable from 'vuedraggable'
import { getAgents, updateAgentName, updateAgentOrder, removeAgent as apiRemoveAgent, startTask, stopTask } from '../api'
import { useWebSocket } from '../composables/useWebSocket'

const router = useRouter()

const agents = ref([])
const sortMode = ref(false)
const agentStats = ref([])
const startLoading = ref(false)
const stopLoading = ref(false)

const stats = reactive({
  totalBytesPerSec: 0,
  totalPacketsPerSec: 0,
  totalBytes: 0,
  totalPackets: 0,
  totalErrors: 0,
  uptime: '',
  // ICMP 统计
  icmpSent: 0,
  icmpReceived: 0,
  icmpLoss: '0.00',
  icmpRttAvg: '0.00',
  icmpRttMin: '0.00',
  icmpRttMax: '0.00',
  // MC 统计
  mcActive: 0,
  mcLoginOk: 0,
  mcLoginFail: 0,
  mcKeepalives: 0,
  mcStatusOk: 0
})

// 任务配置
const taskForm = reactive({
  mode: 'tcp',
  target: '',
  workers: 64,
  size: '1400',
  rate: '0',
  duration: '0',
  stats: '1',
  // HTTP 参数
  httpMethod: 'POST',
  httpTlsSkipVerify: false,
  // UDP 参数
  udpConnected: true,
  // MC 参数
  mcAction: 'login',
  mcProtocol: 763,
  mcUsernamePrefix: 'stress',
  mcServerAddress: '',
  mcKeepalive: true,
  mcReconnect: true,
  mcKeepaliveServerboundId: 18,
  // Flood 参数
  floodProto: 'tcp',
  floodSize: '64',
  floodNodelay: true,
  floodChurn: false,
  floodHttpPath: '/',
  floodTlsSkipVerify: false,
  floodBatch: 64,
  // ICMP 参数
  icmpSize: '56',
  icmpRatePps: '0',
  icmpTimeout: '2'
})

// 目标地址占位符
const targetPlaceholder = computed(() => {
  switch (taskForm.mode) {
    case 'http':
      return '例如: http://192.168.1.100:8080/upload'
    case 'mc':
      return '例如: 192.168.1.100:25565'
    default:
      return '例如: 192.168.1.100:8080'
  }
})

// 模式切换时重置部分参数
function onModeChange(mode) {
  if (mode === 'flood') {
    taskForm.size = taskForm.floodSize
  }
}

// 记录用户手动取消选择的 Agent
const deselectedAgents = new Set()

// 不使用 WebSocket
const connected = ref(false)

// 定时轮询（主要方式）
let pollTimer = null

function startPolling() {
  pollTimer = setInterval(async () => {
    // 排序模式下不更新 agents 列表
    if (sortMode.value) return
    
    try {
      const res = await getAgents()
      const agentsData = res.agents || []
      
      // 更新 agents 列表，保留用户的选择状态
      agents.value = agentsData.map(a => ({
        ...a,
        selected: !deselectedAgents.has(a.id)
      }))
      
      // 更新统计数据
      updateStats(agentsData)
    } catch {
      // ignore
    }
  }, 300)
}

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

async function toggleSortMode() {
  if (sortMode.value) {
    // 退出排序模式，保存排序
    const orders = agents.value.map((a, index) => ({
      id: a.id,
      order: index
    }))
    try {
      await updateAgentOrder(orders)
      ElMessage.success('排序已保存')
    } catch {
      ElMessage.error('保存排序失败')
    }
  }
  sortMode.value = !sortMode.value
}

function onDragEnd() {
  // 拖动结束，数组已自动更新
}

function onSelectionChange(agentId, selected) {
  if (selected) {
    deselectedAgents.delete(agentId)
  } else {
    deselectedAgents.add(agentId)
  }
}

function updateStats(data) {
  if (!data || !Array.isArray(data)) return

  agentStats.value = data

  let totalBytesPerSec = 0
  let totalPacketsPerSec = 0
  let totalBytes = 0
  let totalPackets = 0
  let totalErrors = 0
  let mcActive = 0
  let mcLoginOk = 0
  let mcLoginFail = 0
  let mcKeepalives = 0
  let mcStatusOk = 0
  let icmpSent = 0
  let icmpReceived = 0
  let icmpRttSum = 0
  let icmpRttMin = 999999
  let icmpRttMax = 0
  let uptime = ''

  data.forEach(a => {
    // 累计统计（不管是否运行中）
    totalBytes += a.totalBytes || 0
    totalPackets += a.totalPackets || 0
    
    if (a.running) {
      totalBytesPerSec += a.bytesPerSec || 0
      totalPacketsPerSec += a.packetsPerSec || 0
      totalErrors += a.errors || 0
      if (a.uptime) uptime = a.uptime

      // MC 统计
      mcActive += a.mc_active || 0
      mcLoginOk += a.mc_login_ok || 0
      mcLoginFail += a.mc_login_fail || 0
      mcKeepalives += a.mc_keepalives || 0
      mcStatusOk += a.mc_status_ok || 0

      // ICMP 统计
      icmpSent += a.icmp_sent || 0
      icmpReceived += a.icmp_received || 0
      if (a.icmp_rtt_avg) icmpRttSum += a.icmp_rtt_avg
      if (a.icmp_rtt_min && a.icmp_rtt_min < icmpRttMin) icmpRttMin = a.icmp_rtt_min
      if (a.icmp_rtt_max && a.icmp_rtt_max > icmpRttMax) icmpRttMax = a.icmp_rtt_max
    }
  })

  stats.totalBytesPerSec = totalBytesPerSec
  stats.totalPacketsPerSec = totalPacketsPerSec
  stats.totalBytes = totalBytes
  stats.totalPackets = totalPackets
  stats.totalErrors = totalErrors
  stats.uptime = uptime

  // MC 统计
  stats.mcActive = mcActive
  stats.mcLoginOk = mcLoginOk
  stats.mcLoginFail = mcLoginFail
  stats.mcKeepalives = mcKeepalives
  stats.mcStatusOk = mcStatusOk

  // ICMP 统计
  stats.icmpSent = icmpSent
  stats.icmpReceived = icmpReceived
  if (icmpSent > 0) {
    stats.icmpLoss = ((1 - icmpReceived / icmpSent) * 100).toFixed(2)
  }
  if (icmpReceived > 0) {
    stats.icmpRttAvg = (icmpRttSum / data.filter(a => a.running && a.icmp_rtt_avg).length).toFixed(2)
    stats.icmpRttMin = icmpRttMin === 999999 ? '0.00' : icmpRttMin.toFixed(2)
    stats.icmpRttMax = icmpRttMax.toFixed(2)
  }

  if (data.length > 0 && data[0].uptime) {
    stats.uptime = data[0].uptime
  }
}

function formatBytes(bytes) {
  if (bytes === 0) return '0 B'
  if (bytes === undefined || bytes === null) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

function formatNumber(num) {
  if (num === undefined || num === null) return '0'
  return num.toLocaleString()
}

function selectAll() {
  deselectedAgents.clear()
  agents.value.forEach(a => {
    a.selected = true
  })
}

function invertSelect() {
  agents.value.forEach(a => {
    if (a.selected) {
      deselectedAgents.add(a.id)
    } else {
      deselectedAgents.delete(a.id)
    }
    a.selected = !a.selected
  })
}

async function editName(agent) {
  try {
    const { value } = await ElMessageBox.prompt('请输入新名称', '修改名称', {
      inputValue: agent.name,
      confirmButtonText: '确认',
      cancelButtonText: '取消'
    })

    if (value && value !== agent.name) {
      await updateAgentName(agent.id, value)
      agent.name = value
      ElMessage.success('名称已更新')
    }
  } catch {
    // 取消
  }
}

async function removeAgent(agent) {
  try {
    await ElMessageBox.confirm('确定要删除该 Agent 吗？', '确认', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning'
    })

    await apiRemoveAgent(agent.id)
    agents.value = agents.value.filter(a => a.id !== agent.id)
    ElMessage.success('已删除')
  } catch {
    // 取消
  }
}

// 构建任务配置
function buildTaskConfig() {
  const config = {
    mode: taskForm.mode,
    target: taskForm.target,
    workers: taskForm.workers,
    duration: taskForm.duration,
    stats: "0.5"
  }

  // 通用参数
  if (['tcp', 'udp', 'http'].includes(taskForm.mode)) {
    config.size = parseInt(taskForm.size) || 1400
    config.rate = parseFloat(taskForm.rate) || 0
  }

  // HTTP 参数
  if (taskForm.mode === 'http') {
    config.http_method = taskForm.httpMethod
    config.http_tls_skip_verify = taskForm.httpTlsSkipVerify
  }

  // UDP 参数
  if (taskForm.mode === 'udp') {
    config.udp_connected = taskForm.udpConnected
  }

  // MC 参数
  if (taskForm.mode === 'mc') {
    config.mc_action = taskForm.mcAction
    config.mc_protocol = taskForm.mcProtocol
    config.mc_username_prefix = taskForm.mcUsernamePrefix
    config.mc_server_address = taskForm.mcServerAddress
    config.mc_keepalive = taskForm.mcKeepalive
    config.mc_reconnect = taskForm.mcReconnect
    config.mc_keepalive_serverbound_id = taskForm.mcKeepaliveServerboundId
  }

  // Flood 参数
  if (taskForm.mode === 'flood') {
    config.flood_proto = taskForm.floodProto
    config.flood_size = parseInt(taskForm.floodSize) || 64
    config.flood_nodelay = taskForm.floodNodelay
    config.flood_churn = taskForm.floodChurn
    config.size = parseInt(taskForm.floodSize) || 64

    if (taskForm.floodProto === 'http') {
      config.flood_http_path = taskForm.floodHttpPath
      config.flood_tls_skip_verify = taskForm.floodTlsSkipVerify
    }

    if (taskForm.floodProto === 'udp' && !taskForm.floodChurn) {
      config.flood_batch = taskForm.floodBatch
    }
  }

  // ICMP 参数
  if (taskForm.mode === 'icmp') {
    config.icmp_size = parseInt(taskForm.icmpSize) || 56
    config.icmp_rate_pps = parseInt(taskForm.icmpRatePps) || 0
    config.icmp_timeout = taskForm.icmpTimeout + 's'
  }

  return config
}

async function handleStart() {
  if (!taskForm.target) {
    ElMessage.warning('请输入目标地址')
    return
  }

  const selectedAgents = agents.value.filter(a => a.selected)
  if (selectedAgents.length === 0) {
    ElMessage.warning('请至少选择一个 Agent')
    return
  }

  startLoading.value = true
  try {
    const config = buildTaskConfig()
    config.agents = selectedAgents.map(a => a.id)
    await startTask(config)
    ElMessage.success('任务已下发')
  } catch (e) {
    ElMessage.error(e.response?.data?.msg || '启动失败')
  } finally {
    startLoading.value = false
  }
}

async function handleStop() {
  stopLoading.value = true
  try {
    await stopTask()
    ElMessage.success('停止指令已下发')
  } catch (e) {
    ElMessage.error(e.response?.data?.msg || '停止失败')
  } finally {
    stopLoading.value = false
  }
}

async function stopSingle(agent) {
  try {
    await stopTask([agent.id])
    ElMessage.success(`已向 ${agent.name} 下发停止指令`)
  } catch (e) {
    ElMessage.error(e.response?.data?.msg || '停止失败')
  }
}

function goSettings() {
  router.push('/settings')
}

function goHelp() {
  router.push('/help')
}

function handleLogout() {
  localStorage.removeItem('token')
  router.push('/login')
}

onMounted(async () => {
  try {
    const res = await getAgents()
    agents.value = (res.agents || []).map(a => ({
      ...a,
      selected: a.selected !== false
    }))
  } catch {
    // ignore
  }
  startPolling()
})

onUnmounted(() => {
  stopPolling()
})
</script>

<style scoped>
.dashboard-container {
  min-height: 100vh;
  padding: 20px;
  max-width: 1200px;
  margin: 0 auto;
}

.dashboard-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.dashboard-header h1 {
  font-size: 24px;
  color: #303133;
}

.header-buttons {
  display: flex;
  gap: 10px;
}

.dashboard-content {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.section-card {
  background: rgba(255, 255, 255, 0.85) !important;
}

.section-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 16px;
  font-weight: bold;
  color: #303133;
}

.section-actions {
  display: flex;
  gap: 8px;
}

.task-form {
  max-width: 700px;
}

.task-buttons {
  display: flex;
  gap: 15px;
}

.stats-summary {
  display: flex;
  justify-content: space-around;
  flex-wrap: wrap;
  margin-bottom: 20px;
  padding: 15px;
  background: rgba(64, 158, 255, 0.05);
  border-radius: 8px;
}

.stat-item {
  text-align: center;
  min-width: 80px;
  margin: 5px;
}

.stat-value {
  font-size: 20px;
  font-weight: bold;
  color: #409eff;
}

.stat-label {
  font-size: 12px;
  color: #909399;
  margin-top: 5px;
}

.form-tip {
  font-size: 12px;
  color: #909399;
  margin-left: 10px;
}

:deep(.el-divider__text) {
  font-size: 13px;
  color: #909399;
}

.agent-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 12px;
  background: rgba(255, 255, 255, 0.8);
  border-radius: 8px;
  margin-bottom: 8px;
  border: 1px solid #ebeef5;
}

.drag-handle {
  cursor: grab;
  color: #909399;
  font-size: 18px;
}

.drag-handle:active {
  cursor: grabbing;
}

.agent-name {
  flex: 1;
  font-size: 14px;
}

.ghost {
  opacity: 0.5;
  background: #c8ebfb;
}
</style>
