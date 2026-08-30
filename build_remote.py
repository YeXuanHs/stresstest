import paramiko
import os
import time

LOCAL_DIR = r"C:\Users\Administrator\Desktop\压力测试"

client = paramiko.SSHClient()
client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
client.connect("23.147.56.91", 22, "root", "xbceVPFC0885", timeout=15)

# 上传所有 Go 源码
print("上传源码...")
client.exec_command("mkdir -p /tmp/stresstest")
time.sleep(1)

sftp = client.open_sftp()
for f in ["main.go", "master.go", "agent.go", "flood.go", "httpflood.go", "icmp.go", "mc.go",
          "udpflood_sendmmsg_linux.go", "udpflood_sendmmsg_other.go", "go.mod", "go.sum"]:
    local = os.path.join(LOCAL_DIR, f)
    if os.path.exists(local):
        sftp.put(local, f"/tmp/stresstest/{f}")
        print(f"  上传 {f}")
sftp.close()

# 编译
print("编译中...")
stdin, stdout, stderr = client.exec_command("cd /tmp/stresstest && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /root/stresstest .", timeout=120)
exit_code = stdout.channel.recv_exit_status()
err = stderr.read().decode()

if exit_code == 0:
    print("编译成功!")
else:
    print(f"编译失败:\n{err}")

client.close()
