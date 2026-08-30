<template>
  <div class="setup-container">
    <el-card class="setup-card" shadow="hover">
      <template #header>
        <div class="card-header">
          <span>连接主控</span>
        </div>
      </template>

      <el-form
        ref="formRef"
        :model="form"
        :rules="rules"
        label-position="top"
        class="setup-form"
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

        <el-form-item label="本机名称 (可选)" prop="name">
          <el-input
            v-model="form.name"
            placeholder="请输入本机显示名称"
            size="large"
          />
        </el-form-item>

        <el-form-item>
          <el-button
            type="primary"
            size="large"
            class="submit-btn"
            :loading="loading"
            @click="handleSubmit"
          >
            连接主控
          </el-button>
        </el-form-item>
      </el-form>
    </el-card>
  </div>
</template>

<script setup>
import { ref, reactive } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { setupAgent } from '../api'
import { resetConfig } from '../router'

const router = useRouter()
const formRef = ref(null)
const loading = ref(false)

const form = reactive({
  host: '',
  port: '',
  password: '',
  name: ''
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

async function handleSubmit() {
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return

  loading.value = true
  try {
    await setupAgent(form.host, form.port, form.password, form.name)
    localStorage.setItem('token', form.password)
    resetConfig()
    ElMessage.success('连接成功')
    router.push('/agent')
  } catch (e) {
    ElMessage.error(e.response?.data?.msg || '连接失败')
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
  width: 450px;
  max-width: 90%;
}

.card-header {
  text-align: center;
  font-size: 20px;
  font-weight: bold;
  color: #303133;
}

.setup-form {
  padding: 10px 0;
}

.submit-btn {
  width: 100%;
  margin-top: 10px;
}
</style>
