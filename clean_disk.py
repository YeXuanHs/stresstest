import paramiko, time

c = paramiko.SSHClient()
c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
c.connect("23.147.56.91", 22, "root", "xbceVPFC0885", timeout=15)

# 清理日志
c.exec_command("truncate -s 0 /var/log/*.log 2>/dev/null")
c.exec_command("rm -rf /var/log/journal/* /var/log/*.gz /var/log/*.1 /var/log/*.2")
c.exec_command("journalctl --vacuum-size=10M")
time.sleep(3)

# 检查空间
stdin, stdout, stderr = c.exec_command("df -h /")
print(stdout.read().decode())

c.close()
