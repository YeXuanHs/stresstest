import axios from 'axios'

const api = axios.create({
  baseURL: '',
  timeout: 10000
})

api.interceptors.request.use(config => {
  const token = localStorage.getItem('token')
  if (token) {
    config.headers['Authorization'] = `Bearer ${token}`
  }
  return config
})

api.interceptors.response.use(
  response => response.data,
  error => {
    // 只在非登录接口返回 401 时才跳转登录页
    if (error.response?.status === 401 && !error.config.url.includes('/api/login')) {
      localStorage.removeItem('token')
      window.location.href = '/login'
    }
    return Promise.reject(error)
  }
)

export default api

export function getMode() {
  return api.get('/api/mode')
}

export function setupMaster(password) {
  return api.post('/api/setup', { mode: 'master', password })
}

export function setupBoth(password) {
  return api.post('/api/setup', { mode: 'both', password })
}

export function setupAgent(host, port, password, name) {
  return api.post('/api/setup', { mode: 'agent', host, port, password, name })
}

export function login(password) {
  return api.post('/api/login', { password })
}

export function changePassword(oldPassword, newPassword) {
  return api.post('/api/password', { old_password: oldPassword, new_password: newPassword })
}

export function changePort(port) {
  return api.post('/api/settings/port', { port })
}

export function getAgents() {
  return api.get('/api/agents')
}

export function updateAgentName(agentId, name) {
  return api.post('/api/agents/name', { agent_id: agentId, name })
}

export function updateAgentOrder(orders) {
  return api.post('/api/agents/order', { orders })
}

export function removeAgent(agentId) {
  return api.delete(`/api/agents/${agentId}`)
}

export function startTask(config) {
  return api.post('/api/task/start', config)
}

export function stopTask(agents) {
  return api.post('/api/task/stop', { agents: agents || [] })
}

export function getAgentStatus() {
  return api.get('/api/agent/status')
}

export function updateAgentConfig(host, port, password) {
  return api.post('/api/agent/config', { host, port, password })
}
