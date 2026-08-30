<template>
  <div class="agent-container">
    <el-card class="agent-card" shadow="hover">
      <template #header>
        <div class="card-header">
          <span>被控管理面板</span>
          <div class="header-buttons">
            <el-button @click="goHelp">
              <el-icon><QuestionFilled /></el-icon>
              使用说明
            </el-button>
            <el-button @click="handleLogout">
              <el-icon><SwitchButton /></el-icon>
              退出
            </el-button>
          </div>
        </div>
      </template>

      <!-- 当前状态 -->
      <div class="status-section">
        <h3>当前状态</h3>
        <div class="status-info">
          <div class="status-row">
            <span class="status-label">连接状态:</span>
            <el-tag :type="connected ? 'success' : 'danger'" size="small">
              {{ connected ? '已连接' : '未连接' }}
            </el-tag>
          </div>
          <div class="status-row">
            <span class="status-label">主控地址:</span>
            <span>{{ status.masterHost }}:{{ status.masterPort }}</span>
          </div>
          <div class="status-row">
            <span class="status-label">本机名称:</span>
            <span>{{ status.agentName }} (由主控设置)</span>
          </div>
        </div>
      </div>

      <el-divider />

      <!-- 修改连接配置 -->
      <div class="config-section">
        <h3>修改连接配置</h3>
        <el-form
          ref="formRef"
          :model="form"
          :rules="rules"
          label-position="top"
          class="config-form"
        >
          <el-form-item label="主控 IP" prop="host">
            <el-input
              v-model="form.host"
              placeholder="请输入主控IP地址"
              size="large"
            />
          </el-form-item>

          <el-form-item label="主控端口" prop="port">
            <el-input
              v-model="form.port"
              placeholder="请输入主控端口"
              size="large"
            />
          </el-form-item>

          <el-form-item label="主控密码" prop="password">
            <el-input
              v-model="form.password"
              type="password"
              show-password
              placeholder="请输入主控密码"
              size="large"
            />
          </el-form-item>

          <el-form-item>
            <el-button
              type="primary"
              size="large"
              class="submit-btn"
              :loading="loading"
              @click="handleReconnect"
            >
              重新连接
            </el-button>
          </el-form-item>
        </el-form>
      </div>

      <el-alert
        title="此面板仅用于连接配置，无法下发任务"
        type="warning"
        :closable="false"
        show-icon
      />
    </el-card>
  </div>
</template>

<script setup>
import { ref, reactive, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { QuestionFilled, SwitchButton } from '@element-plus/icons-vue'
import { getAgentStatus, updateAgentConfig } from '../api'
import { useWebSocket } from '../composables/useWebSocket'

const router = useRouter()
const formRef = ref(null)
const loading = ref(false)

const status = reactive({
  masterHost: '',
  masterPort: '',
  agentName: ''
})

const form = reactive({
  host: '',
  port: '',
  password: ''
})

const rules = {
  host: [
    { required: true, message: '请输入主控IP', trigger: 'blur' }
  ],
  port: [
    { required: true, message: '请输入主控端口', trigger: 'blur' }
  ],
  password: [
    { required: true, message: '请输入主控密码', trigger: 'blur' }
  ]
}

const { connected } = useWebSocket('/ws/stats', (data) => {
  if (data.type === 'config_update') {
    if (data.agent_name) {
      status.agentName = data.agent_name
    }
  }
})

async function handleReconnect() {
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return

  loading.value = true
  try {
    await updateAgentConfig(form.host, form.port, form.password)
    localStorage.setItem('token', form.password)
    status.masterHost = form.host
    status.masterPort = form.port
    ElMessage.success('配置已更新，正在重新连接')
  } catch (e) {
    ElMessage.error(e.response?.data?.msg || '更新失败')
  } finally {
    loading.value = false
  }
}

function handleLogout() {
  localStorage.removeItem('token')
  router.push('/login')
}

function goHelp() {
  router.push('/help')
}

onMounted(async () => {
  try {
    const res = await getAgentStatus()
    status.masterHost = res.master_host || ''
    status.masterPort = res.master_port || ''
    status.agentName = res.agent_name || ''

    form.host = res.master_host || ''
    form.port = res.master_port || ''
  } catch {
    // ignore
  }
})
</script>

<style scoped>
.agent-container {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: 100vh;
  padding: 20px;
}

.agent-card {
  width: 500px;
  max-width: 90%;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 20px;
  font-weight: bold;
  color: #303133;
}

.header-buttons {
  display: flex;
  gap: 10px;
}

.status-section,
.config-section {
  margin-bottom: 20px;
}

.status-section h3,
.config-section h3 {
  font-size: 16px;
  color: #303133;
  margin-bottom: 15px;
}

.status-info {
  background: rgba(64, 158, 255, 0.05);
  padding: 15px;
  border-radius: 8px;
}

.status-row {
  display: flex;
  align-items: center;
  margin-bottom: 10px;
}

.status-row:last-child {
  margin-bottom: 0;
}

.status-label {
  font-weight: bold;
  color: #606266;
  margin-right: 10px;
  min-width: 80px;
}

.config-form {
  margin-bottom: 15px;
}

.submit-btn {
  width: 100%;
  margin-top: 10px;
}
</style>
