import paramiko

client = paramiko.SSHClient()
client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
client.connect("23.147.56.91", 22, "root", "xbceVPFC0885", timeout=15)

# go mod tidy
print("go mod tidy...")
stdin, stdout, stderr = client.exec_command("cd /tmp/stresstest && go mod tidy", timeout=60)
exit_code = stdout.channel.recv_exit_status()
print(f"exit: {exit_code}")
print(stderr.read().decode()[:500])

# 再次编译
print("\n编译中...")
stdin, stdout, stderr = client.exec_command("cd /tmp/stresstest && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /root/stresstest .", timeout=120)
exit_code = stdout.channel.recv_exit_status()
err = stderr.read().decode()

if exit_code == 0:
    print("编译成功!")
    # 下载
    sftp = client.open_sftp()
    sftp.get("/root/stresstest", r"C:\Users\Administrator\Desktop\压力测试\stresstest")
    sftp.close()
    print("已下载二进制")
else:
    print(f"编译失败:\n{err}")

client.close()
