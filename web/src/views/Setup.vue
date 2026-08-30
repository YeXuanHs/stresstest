<template>
  <div class="setup-container">
    <el-card class="setup-card" shadow="hover">
      <template #header>
        <div class="card-header">
          <span>压力测试工具 - 初始化设置</span>
        </div>
      </template>

      <!-- 步骤1：选择模式 -->
      <div v-if="step === 0" class="step-content">
        <p class="step-title">请选择本机角色</p>
        <div class="mode-cards">
          <el-card
            class="mode-card"
            :class="{ active: selectedMode === 'master' }"
            shadow="hover"
            @click="selectedMode = 'master'"
          >
            <el-icon class="mode-icon"><Monitor /></el-icon>
            <h3>主控</h3>
            <p>Master</p>
            <p class="mode-desc">管理被控，不参与发包</p>
          </el-card>

          <el-card
            class="mode-card"
            :class="{ active: selectedMode === 'agent' }"
            shadow="hover"
            @click="selectedMode = 'agent'"
          >
            <el-icon class="mode-icon"><Connection /></el-icon>
            <h3>被控</h3>
            <p>Agent</p>
            <p class="mode-desc">连接主控，执行发包</p>
          </el-card>

          <el-card
            class="mode-card"
            :class="{ active: selectedMode === 'both' }"
            shadow="hover"
            @click="selectedMode = 'both'"
          >
            <el-icon class="mode-icon"><Cpu /></el-icon>
            <h3>主控+被控</h3>
            <p>Both</p>
            <p class="mode-desc">管理被控，自己也参与发包</p>
          </el-card>
        </div>
        <el-button
          type="primary"
          size="large"
          :disabled="!selectedMode"
          style="width: 100%; margin-top: 20px"
          @click="nextStep"
        >
          下一步
        </el-button>
      </div>

      <!-- 步骤2：设置密码 -->
      <div v-if="step === 1" class="step-content">
        <!-- 主控/主控+被控：设置密码 -->
        <el-form
          v-if="selectedMode !== 'agent'"
          ref="passwordFormRef"
          :model="passwordForm"
          :rules="passwordRules"
          label-position="top"
        >
          <el-form-item label="设置管理密码" prop="password">
            <el-input
              v-model="passwordForm.password"
              type="password"
              show-password
              placeholder="请输入密码（至少6位）"
              size="large"
            />
          </el-form-item>
          <el-form-item label="确认密码" prop="confirmPassword">
            <el-input
              v-model="passwordForm.confirmPassword"
              type="password"
              show-password
              placeholder="请再次输入密码"
              size="large"
            />
          </el-form-item>
        </el-form>

        <!-- 被控：填写主控信息 -->
        <el-form
          v-else
          ref="agentFormRef"
          :model="agentForm"
          :rules="agentRules"
          label-position="top"
        >
          <el-form-item label="主控 IP" prop="host">
            <el-input v-model="agentForm.host" placeholder="请输入主控IP地址" size="large" />
          </el-form-item>
          <el-form-item label="主控端口" prop="port">
            <el-input v-model="agentForm.port" placeholder="请输入主控端口" size="large" />
          </el-form-item>
          <el-form-item label="主控密码" prop="password">
            <el-input
              v-model="agentForm.password"
              type="password"
              show-password
              placeholder="请输入主控密码"
              size="large"
            />
          </el-form-item>
          <el-form-item label="本机名称（可选）">
            <el-input v-model="agentForm.name" placeholder="请输入本机显示名称" size="large" />
          </el-form-item>
        </el-form>

        <div style="display: flex; gap: 10px; margin-top: 20px">
          <el-button size="large" style="flex: 1" @click="step = 0">
            上一步
          </el-button>
          <el-button
            type="primary"
            size="large"
            :loading="loading"
            style="flex: 2"
            @click="handleSubmit"
          >
            完成设置
          </el-button>
        </div>
      </div>
    </el-card>
  </div>
</template>

<script setup>
import { ref, reactive } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Monitor, Connection, Cpu } from '@element-plus/icons-vue'
import { setupMaster, setupBoth, setupAgent } from '../api'
import { resetConfig } from '../router'

const router = useRouter()
const step = ref(0)
const selectedMode = ref('')
const loading = ref(false)

const passwordFormRef = ref(null)
const agentFormRef = ref(null)

const passwordForm = reactive({
  password: '',
  confirmPassword: ''
})

const agentForm = reactive({
  host: '',
  port: '',
  password: '',
  name: ''
})

const validateConfirmPassword = (rule, value, callback) => {
  if (value !== passwordForm.password) {
    callback(new Error('两次输入的密码不一致'))
  } else {
    callback()
  }
}

const passwordRules = {
  password: [
    { required: true, message: '请输入密码', trigger: 'blur' },
    { min: 6, message: '密码长度不能少于6位', trigger: 'blur' }
  ],
  confirmPassword: [
    { required: true, message: '请再次输入密码', trigger: 'blur' },
    { validator: validateConfirmPassword, trigger: 'blur' }
  ]
}

const agentRules = {
  host: [{ required: true, message: '请输入主控IP', trigger: 'blur' }],
  port: [{ required: true, message: '请输入主控端口', trigger: 'blur' }],
  password: [{ required: true, message: '请输入主控密码', trigger: 'blur' }]
}

function nextStep() {
  if (!selectedMode.value) {
    ElMessage.warning('请选择模式')
    return
  }
  step.value = 1
}

async function handleSubmit() {
  loading.value = true
  try {
    if (selectedMode.value === 'agent') {
      // 被控模式
      const valid = await agentFormRef.value.validate().catch(() => false)
      if (!valid) { loading.value = false; return }

      await setupAgent(agentForm.host, agentForm.port, agentForm.password, agentForm.name)
      localStorage.setItem('token', agentForm.password)
    } else {
      // 主控或主控+被控
      const valid = await passwordFormRef.value.validate().catch(() => false)
      if (!valid) { loading.value = false; return }

      if (selectedMode.value === 'master') {
        await setupMaster(passwordForm.password)
      } else {
        await setupBoth(passwordForm.password)
      }
      localStorage.setItem('token', passwordForm.password)
    }

    resetConfig()
    ElMessage.success('设置完成')
    router.push(selectedMode.value === 'agent' ? '/agent' : '/dashboard')
  } catch (e) {
    ElMessage.error(e.response?.data?.msg || '设置失败')
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.setup-container {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: 100vh;
  padding: 20px;
}

.setup-card {
  width: 600px;
  max-width: 95%;
}

.card-header {
  text-align: center;
  font-size: 20px;
  font-weight: bold;
  color: #303133;
}

.step-content {
  padding: 10px 0;
}

.step-title {
  text-align: center;
  font-size: 16px;
  color: #606266;
  margin-bottom: 20px;
}

.mode-cards {
  display: flex;
  justify-content: center;
  gap: 15px;
}

.mode-card {
  width: 160px;
  cursor: pointer;
  transition: all 0.3s;
  text-align: center;
  background: rgba(64, 158, 255, 0.05) !important;
  border: 2px solid transparent !important;
}

.mode-card:hover {
  transform: translateY(-3px);
}

.mode-card.active {
  border-color: #409eff !important;
  background: rgba(64, 158, 255, 0.1) !important;
}

.mode-icon {
  font-size: 40px;
  color: #409eff;
  margin-bottom: 10px;
}

.mode-card h3 {
  font-size: 16px;
  color: #303133;
  margin-bottom: 5px;
}

.mode-card p {
  font-size: 13px;
  color: #909399;
  margin: 3px 0;
}

.mode-desc {
  font-size: 12px !important;
  color: #C0C4CC !important;
}
</style>
