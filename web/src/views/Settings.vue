<template>
  <div class="settings-container">
    <el-card class="settings-card" shadow="hover">
      <template #header>
        <div class="card-header">
          <span>设置</span>
          <el-button @click="goBack">
            <el-icon><Back /></el-icon>
            返回
          </el-button>
        </div>
      </template>

      <!-- 修改密码 -->
      <div class="section">
        <h3>修改密码</h3>
        <el-form
          ref="passwordFormRef"
          :model="passwordForm"
          :rules="passwordRules"
          label-position="top"
          class="settings-form"
        >
          <el-form-item label="当前密码" prop="oldPassword">
            <el-input
              v-model="passwordForm.oldPassword"
              type="password"
              show-password
              placeholder="请输入当前密码"
              size="large"
            />
          </el-form-item>

          <el-form-item label="新密码" prop="newPassword">
            <el-input
              v-model="passwordForm.newPassword"
              type="password"
              show-password
              placeholder="请输入新密码"
              size="large"
            />
          </el-form-item>

          <el-form-item label="确认密码" prop="confirmPassword">
            <el-input
              v-model="passwordForm.confirmPassword"
              type="password"
              show-password
              placeholder="请再次输入新密码"
              size="large"
            />
          </el-form-item>

          <el-form-item>
            <el-button
              type="primary"
              size="large"
              :loading="passwordLoading"
              @click="handleChangePassword"
            >
              修改密码
            </el-button>
          </el-form-item>
        </el-form>
      </div>

      <el-divider />

      <!-- 修改端口 -->
      <div class="section">
        <h3>修改端口</h3>
        <el-form
          ref="portFormRef"
          :model="portForm"
          :rules="portRules"
          label-position="top"
          class="settings-form"
        >
          <el-form-item label="当前端口">
            <el-input
              :value="currentPort"
              disabled
              size="large"
            />
          </el-form-item>

          <el-form-item label="新端口" prop="port">
            <el-input
              v-model="portForm.port"
              placeholder="请输入新端口"
              size="large"
            />
          </el-form-item>

          <el-alert
            title="修改端口将自动重启服务"
            type="warning"
            :closable="false"
            show-icon
            style="margin-bottom: 15px"
          />

          <el-form-item>
            <el-button
              type="warning"
              size="large"
              :loading="portLoading"
              @click="handleChangePort"
            >
              修改并重启
            </el-button>
          </el-form-item>
        </el-form>
      </div>
    </el-card>
  </div>
</template>

<script setup>
import { ref, reactive, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Back } from '@element-plus/icons-vue'
import { changePassword, changePort } from '../api'

const router = useRouter()
const passwordFormRef = ref(null)
const portFormRef = ref(null)
const passwordLoading = ref(false)
const portLoading = ref(false)
const currentPort = ref('')

const passwordForm = reactive({
  oldPassword: '',
  newPassword: '',
  confirmPassword: ''
})

const portForm = reactive({
  port: ''
})

const validateConfirmPassword = (rule, value, callback) => {
  if (value !== passwordForm.newPassword) {
    callback(new Error('两次输入的密码不一致'))
  } else {
    callback()
  }
}

const passwordRules = {
  oldPassword: [
    { required: true, message: '请输入当前密码', trigger: 'blur' }
  ],
  newPassword: [
    { required: true, message: '请输入新密码', trigger: 'blur' },
    { min: 6, message: '密码长度不能少于6位', trigger: 'blur' }
  ],
  confirmPassword: [
    { required: true, message: '请再次输入新密码', trigger: 'blur' },
    { validator: validateConfirmPassword, trigger: 'blur' }
  ]
}

const portRules = {
  port: [
    { required: true, message: '请输入新端口', trigger: 'blur' }
  ]
}

async function handleChangePassword() {
  const valid = await passwordFormRef.value.validate().catch(() => false)
  if (!valid) return

  passwordLoading.value = true
  try {
    await changePassword(passwordForm.oldPassword, passwordForm.newPassword)
    localStorage.setItem('token', passwordForm.newPassword)
    ElMessage.success('密码已修改')
    passwordForm.oldPassword = ''
    passwordForm.newPassword = ''
    passwordForm.confirmPassword = ''
  } catch (e) {
    ElMessage.error(e.response?.data?.msg || '修改失败')
  } finally {
    passwordLoading.value = false
  }
}

async function handleChangePort() {
  const valid = await portFormRef.value.validate().catch(() => false)
  if (!valid) return

  try {
    await ElMessageBox.confirm('修改端口将自动重启服务，确定继续吗？', '确认', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning'
    })
  } catch {
    return
  }

  portLoading.value = true
  try {
    await changePort(portForm.port)
    ElMessage.success('端口已更新，服务即将重启')
    setTimeout(() => {
      window.location.href = `${window.location.protocol}//${window.location.hostname}:${portForm.port}`
    }, 2000)
  } catch (e) {
    ElMessage.error(e.response?.data?.msg || '修改失败')
  } finally {
    portLoading.value = false
  }
}

function goBack() {
  router.back()
}

onMounted(() => {
  currentPort.value = window.location.port || '443'
})
</script>

<style scoped>
.settings-container {
  display: flex;
  justify-content: center;
  align-items: flex-start;
  min-height: 100vh;
  padding: 40px 20px;
}

.settings-card {
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

.section {
  margin-bottom: 10px;
}

.section h3 {
  font-size: 16px;
  color: #303133;
  margin-bottom: 15px;
}

.settings-form {
  max-width: 400px;
}
</style>
