<template>
  <div class="help-container">
    <el-card class="help-card" shadow="hover">
      <template #header>
        <div class="card-header">
          <span>使用说明</span>
          <el-button @click="goBack">
            <el-icon><Back /></el-icon>
            返回
          </el-button>
        </div>
      </template>

      <div class="help-content">
        <h2>压力测试工具 - 使用说明</h2>

        <el-divider />

        <h3>一、架构说明</h3>
        <p>本工具支持三种运行模式：</p>
        <el-table :data="modes" style="width: 100%" size="default">
          <el-table-column prop="mode" label="模式" width="150" />
          <el-table-column prop="desc" label="说明" />
          <el-table-column prop="use" label="适用场景" />
        </el-table>

        <el-divider />

        <h3>二、快速开始</h3>

        <h4>1. 安装服务（首次运行）</h4>
        <div class="code-block">
          <code>./stresstest</code>
        </div>
        <p>运行后会询问面板端口和运行模式，然后自动创建 systemd 服务并启动。</p>

        <h4>2. 直接指定模式运行</h4>
        <div class="code-block">
          <code>./stresstest master</code><br>
          <code>./stresstest agent</code><br>
          <code>./stresstest both</code>
        </div>

        <h4>3. 服务管理命令</h4>
        <el-table :data="serviceCmds" style="width: 100%" size="default">
          <el-table-column prop="cmd" label="命令" width="250" />
          <el-table-column prop="desc" label="说明" />
        </el-table>

        <el-divider />

        <h3>三、主控面板功能</h3>

        <h4>1. 在线 Agent 列表</h4>
        <ul>
          <li>显示所有已连接的被控机器</li>
          <li>支持全选/反选操作</li>
          <li>可修改 Agent 显示名称</li>
          <li>可删除离线的 Agent</li>
          <li>运行中的 Agent 无法取消选择</li>
        </ul>

        <h4>2. 任务配置</h4>
        <p>选择压测模式和参数后，点击"开始压测"下发任务。</p>

        <h4>3. 实时统计</h4>
        <ul>
          <li>总吞吐：所有 Agent 的总发送速率</li>
          <li>总包速率：所有 Agent 的总包数/秒</li>
          <li>总错误：累计错误数</li>
          <li>运行时间：当前任务运行时长</li>
          <li>ICMP 模式：显示发送数、接收数、丢包率、RTT</li>
          <li>MC 模式：显示当前连接数、登录成功/失败数、KeepAlive</li>
        </ul>

        <el-divider />

        <h3>四、压测模式详解</h3>

        <h4>1. TCP 模式</h4>
        <p>适用于测试 TCP 服务的吞吐能力。</p>
        <el-table :data="tcpParams" style="width: 100%" size="small">
          <el-table-column prop="param" label="参数" width="180" />
          <el-table-column prop="desc" label="说明" />
          <el-table-column prop="default" label="默认值" width="100" />
        </el-table>

        <h4>2. UDP 模式</h4>
        <p>适用于测试 UDP 服务或网络设备的包处理能力。</p>
        <el-table :data="udpParams" style="width: 100%" size="small">
          <el-table-column prop="param" label="参数" width="180" />
          <el-table-column prop="desc" label="说明" />
          <el-table-column prop="default" label="默认值" width="100" />
        </el-table>

        <h4>3. HTTP 模式</h4>
        <p>适用于测试 Web 服务器或 API 接口。</p>
        <el-table :data="httpParams" style="width: 100%" size="small">
          <el-table-column prop="param" label="参数" width="180" />
          <el-table-column prop="desc" label="说明" />
          <el-table-column prop="default" label="默认值" width="100" />
        </el-table>

        <h4>4. Minecraft 模式</h4>
        <p>适用于测试 Minecraft 服务器的玩家承载能力。</p>
        <el-table :data="mcParams" style="width: 100%" size="small">
          <el-table-column prop="param" label="参数" width="180" />
          <el-table-column prop="desc" label="说明" />
          <el-table-column prop="default" label="默认值" width="100" />
        </el-table>

        <div class="warning-box">
          <p><strong>Minecraft 模式重要限制：</strong></p>
          <ul>
            <li>必须关闭正版验证：server.properties 中设置 online-mode=false</li>
            <li>必须关闭数据包压缩：设置 network-compression-threshold=-1</li>
            <li>KeepAlive 包ID是版本的，如果连接频繁掉线请调整</li>
            <li>不支持正版加密验证，故意如此</li>
          </ul>
        </div>

        <h4>5. Flood 极限模式</h4>
        <p>用于测试网络栈处理速率上限，不限速、默认小包。</p>
        <el-table :data="floodParams" style="width: 100%" size="small">
          <el-table-column prop="param" label="参数" width="180" />
          <el-table-column prop="desc" label="说明" />
          <el-table-column prop="default" label="默认值" width="100" />
        </el-table>

        <div class="info-box">
          <p><strong>Flood 模式说明：</strong></p>
          <ul>
            <li><strong>长连接模式</strong>：默认64字节小包、不限速地连续写，测的是内核网络栈单位时间能处理多少个包</li>
            <li><strong>churn 模式</strong>：TCP 下每次都完整走一遍握手、写一次、关闭，测服务端 accept() 循环</li>
            <li>UDP churn 模式每次都新建一个socket，测的是处理"新五元组"流量的能力</li>
            <li>因为是真实握手/真实源IP，无法伪装来源</li>
          </ul>
        </div>

        <h4>6. ICMP 模式</h4>
        <p>用于测试网络连通性和延迟，类似 ping 命令但支持高并发。</p>
        <el-table :data="icmpParams" style="width: 100%" size="small">
          <el-table-column prop="param" label="参数" width="180" />
          <el-table-column prop="desc" label="说明" />
          <el-table-column prop="default" label="默认值" width="100" />
        </el-table>

        <p>显示指标：发送数、接收数、丢包率、RTT 平均/最小/最大值。</p>

        <el-divider />

        <h3>五、参数建议</h3>

        <h4>1. 并发数设置</h4>
        <ul>
          <li>建议设为 CPU 核数的 2~8 倍</li>
          <li>过高的并发数可能导致系统资源耗尽</li>
        </ul>

        <h4>2. 包大小设置</h4>
        <ul>
          <li><strong>UDP</strong>：建议 1200~1400 字节（避免IP分片）</li>
          <li><strong>TCP/HTTP</strong>：建议 32KB~256KB（减少系统调用）</li>
          <li><strong>Flood</strong>：默认 64 字节（最大化 pps）</li>
          <li><strong>ICMP</strong>：默认 56 字节（与标准 ping 一致）</li>
        </ul>

        <h4>3. 高吞吐优化</h4>
        <ul>
          <li>需要万兆（10G）及以上网卡才能达到 2GB/s</li>
          <li>UDP 比 TCP 更容易跑高吞吐（没有握手和拥塞控制开销）</li>
          <li>单机不够可多台机器同时发压（分布式）</li>
        </ul>

        <el-divider />

        <h3>六、被控面板功能</h3>
        <ul>
          <li>查看当前连接状态</li>
          <li>修改主控连接配置（IP、端口、密码）</li>
          <li>注意：被控面板无法下发任务</li>
        </ul>

        <el-divider />

        <h3>七、设置页面</h3>
        <ul>
          <li><strong>修改密码</strong>：需要输入当前密码验证</li>
          <li><strong>修改端口</strong>：修改后服务会自动重启</li>
        </ul>

        <el-divider />

        <h3>八、注意事项</h3>
        <div class="warning-box">
          <ul>
            <li>请仅对自己拥有或已获得明确授权的目标进行压测</li>
            <li>未经授权对他人服务器发起流量攻击可能触犯法律</li>
            <li>UDP 模式下发送成功不代表对方收到，需在目标端统计</li>
          </ul>
        </div>
      </div>
    </el-card>
  </div>
</template>

<script setup>
import { useRouter } from 'vue-router'
import { Back } from '@element-plus/icons-vue'

const router = useRouter()

const modes = [
  { mode: '主控 (master)', desc: '管理被控，下发任务', use: '管理多台机器集中压测' },
  { mode: '被控 (agent)', desc: '连接主控，执行发包', use: '作为发包机使用' },
  { mode: '主控+被控 (both)', desc: '管理被控，自己也参与发包', use: '单机或小规模压测' }
]

const serviceCmds = [
  { cmd: './stresstest', desc: '首次运行，安装服务并启动' },
  { cmd: './stresstest status', desc: '查看服务状态' },
  { cmd: './stresstest uninstall', desc: '卸载服务' },
  { cmd: 'systemctl start stresstest', desc: '启动服务' },
  { cmd: 'systemctl stop stresstest', desc: '停止服务' },
  { cmd: 'systemctl restart stresstest', desc: '重启服务' },
  { cmd: 'journalctl -u stresstest -f', desc: '查看实时日志' }
]

const tcpParams = [
  { param: '目标地址', desc: '格式为 host:port', default: '必填' },
  { param: '并发数', desc: '每个 Agent 启动的 worker 数量', default: 'CPUx4' },
  { param: '包大小', desc: '每次发送的 payload 字节数', default: '65536' },
  { param: '限速', desc: '全局限速，单位 MB/s，0 为不限速', default: '0' },
  { param: '统计间隔', desc: '统计输出间隔（秒）', default: '1' },
  { param: '时长', desc: '运行时长（秒），0 为无限时长', default: '0' }
]

const udpParams = [
  { param: '目标地址', desc: '格式为 host:port', default: '必填' },
  { param: '并发数', desc: '每个 Agent 启动的 worker 数量', default: 'CPUx4' },
  { param: '包大小', desc: '每次发送的 payload 字节数', default: '65536' },
  { param: '限速', desc: '全局限速，单位 MB/s，0 为不限速', default: '0' },
  { param: '已连接Socket', desc: '使用已连接 socket，性能更好', default: '开启' },
  { param: '统计间隔', desc: '统计输出间隔（秒）', default: '1' },
  { param: '时长', desc: '运行时长（秒），0 为无限时长', default: '0' }
]

const httpParams = [
  { param: '目标地址', desc: '完整 URL，如 http://host:port/path', default: '必填' },
  { param: '并发数', desc: '每个 Agent 启动的 worker 数量', default: 'CPUx4' },
  { param: '包大小', desc: '每次发送的 payload 字节数', default: '65536' },
  { param: '限速', desc: '全局限速，单位 MB/s，0 为不限速', default: '0' },
  { param: '请求方法', desc: 'POST / GET / PUT / DELETE', default: 'POST' },
  { param: '跳过证书验证', desc: '请求 https 时跳过证书校验', default: '关闭' },
  { param: '统计间隔', desc: '统计输出间隔（秒）', default: '1' },
  { param: '时长', desc: '运行时长（秒），0 为无限时长', default: '0' }
]

const mcParams = [
  { param: '目标地址', desc: '格式为 host:port（Minecraft 服务器）', default: '必填' },
  { param: '并发数', desc: '模拟的玩家数量', default: 'CPUx4' },
  { param: '动作', desc: 'login（登录挂机）/ status（查服状态）', default: 'login' },
  { param: '协议版本', desc: 'Minecraft 协议版本号', default: '763' },
  { param: '用户名前缀', desc: '实际用户名 = 前缀 + 序号', default: 'stress' },
  { param: '服务器地址', desc: '握手包中的服务器地址字段', default: '自动' },
  { param: '保活', desc: '登录后响应 KeepAlive 维持连接', default: '开启' },
  { param: '自动重连', desc: '断线后自动重连', default: '开启' },
  { param: 'KeepAlive ID', desc: 'Serverbound KeepAlive包ID', default: '18' },
  { param: '统计间隔', desc: '统计输出间隔（秒）', default: '1' },
  { param: '时长', desc: '运行时长（秒），0 为无限时长', default: '0' }
]

const floodParams = [
  { param: '目标地址', desc: '格式为 host:port', default: '必填' },
  { param: '并发数', desc: '每个 Agent 启动的 worker 数量', default: 'CPUx4' },
  { param: '协议', desc: 'TCP / UDP / HTTP', default: 'tcp' },
  { param: '包大小', desc: 'payload 字节数，小包提高 pps', default: '64' },
  { param: '关闭Nagle', desc: 'TCP_NODELAY，立即发送', default: '开启' },
  { param: '每次重新建连', desc: 'churn 模式，测连接建立速率', default: '关闭' },
  { param: 'HTTP路径', desc: 'HTTP flood 请求的路径', default: '/' },
  { param: '跳过证书验证', desc: 'HTTPS flood 跳过证书校验', default: '关闭' },
  { param: '批量发送', desc: 'UDP sendmmsg 批量包数（仅Linux）', default: '64' },
  { param: '统计间隔', desc: '统计输出间隔（秒）', default: '1' },
  { param: '时长', desc: '运行时长（秒），0 为无限时长', default: '0' }
]

const icmpParams = [
  { param: '目标地址', desc: '格式为 host 或 host:port', default: '必填' },
  { param: '并发数', desc: '每个 Agent 启动的 worker 数量', default: 'CPUx4' },
  { param: '包大小', desc: 'ICMP echo 请求的 payload 大小', default: '56' },
  { param: '限速', desc: '限速，单位包/秒，0 为不限速', default: '0' },
  { param: '超时时间', desc: '等待 Echo Reply 的超时时间', default: '2' },
  { param: '统计间隔', desc: '统计输出间隔（秒）', default: '1' },
  { param: '时长', desc: '运行时长（秒），0 为无限时长', default: '0' }
]

function goBack() {
  router.back()
}
</script>

<style scoped>
.help-container {
  display: flex;
  justify-content: center;
  align-items: flex-start;
  min-height: 100vh;
  padding: 20px;
}

.help-card {
  width: 900px;
  max-width: 95%;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 20px;
  font-weight: bold;
  color: #303133;
}

.help-content {
  line-height: 1.8;
}

.help-content h2 {
  text-align: center;
  color: #303133;
  margin-bottom: 20px;
}

.help-content h3 {
  color: #409eff;
  margin-top: 25px;
  margin-bottom: 15px;
}

.help-content h4 {
  color: #606266;
  margin-top: 20px;
  margin-bottom: 10px;
}

.help-content p {
  color: #606266;
  margin: 10px 0;
}

.help-content ul {
  padding-left: 20px;
  color: #606266;
}

.help-content li {
  margin: 8px 0;
}

.code-block {
  background: #f5f7fa;
  border-radius: 8px;
  padding: 15px 20px;
  margin: 10px 0;
  font-family: 'Consolas', 'Monaco', monospace;
}

.code-block code {
  color: #409eff;
  font-size: 14px;
}

.warning-box {
  background: #fef0f0;
  border-left: 4px solid #f56c6c;
  border-radius: 4px;
  padding: 15px 20px;
  margin: 15px 0;
}

.warning-box p,
.warning-box li {
  color: #f56c6c;
}

.info-box {
  background: #f0f9ff;
  border-left: 4px solid #409eff;
  border-radius: 4px;
  padding: 15px 20px;
  margin: 15px 0;
}

.info-box p,
.info-box li {
  color: #409eff;
}

:deep(.el-table) {
  margin: 15px 0;
}

:deep(.el-divider) {
  margin: 25px 0;
}
</style>
