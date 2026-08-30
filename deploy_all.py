import paramiko
import time

servers = [
    {"host": "23.147.56.91", "port": 22, "user": "root", "pwd": "xbceVPFC0885", "name": "eagle1", "type": "systemd"},
    {"host": "23.147.56.211", "port": 22, "user": "root", "pwd": "pdvnUHPQ9305", "name": "eagle2", "type": "systemd"},
    {"host": "2401:b60:e0fd:5b::a01", "port": 22, "user": "root", "pwd": "h2PVvKb#HkBrLn&y", "name": "香港lxc1", "type": "openrc"},
    {"host": "2401:b60:e0fd:5b::10f4", "port": 22, "user": "root", "pwd": "sM0Pm%C1bPoJg@lQ", "name": "香港lxc2", "type": "openrc"},
    {"host": "69.12.72.176", "port": 37403, "user": "root", "pwd": "zoFVU5ExJc@-@N8w", "name": "美国凤凰城", "type": "openrc"},
    {"host": "74.222.12.184", "port": 22, "user": "root", "pwd": "6X5l5skPKa", "name": "东京伊雷娜", "type": "systemd"},
]

for s in servers:
    print(f"\n部署到 {s['name']}...")
    try:
        client = paramiko.SSHClient()
        client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
        client.connect(s["host"], s["port"], s["user"], s["pwd"], timeout=10)
        
        if s["type"] == "systemd":
            client.exec_command("systemctl stop stresstest")
            time.sleep(1)
            stdin, stdout, stderr = client.exec_command("lsof -ti:8443")
            pids = stdout.read().decode().strip()
            if pids:
                client.exec_command(f"kill -9 {pids}")
                time.sleep(1)
        else:
            client.exec_command("rc-service stresstest stop")
            time.sleep(1)
            client.exec_command("pkill -9 stresstest")
            time.sleep(1)
        
        client.exec_command("rm -f /root/stresstest")
        time.sleep(1)
        sftp = client.open_sftp()
        sftp.put(r"C:\Users\Administrator\Desktop\压力测试\stresstest", "/root/stresstest")
        sftp.close()
        client.exec_command("chmod +x /root/stresstest")
        
        if s["type"] == "systemd":
            client.exec_command("systemctl start stresstest")
        else:
            client.exec_command("rc-service stresstest start")
        
        print(f"  成功!")
        client.close()
    except Exception as e:
        print(f"  失败: {e}")

print("\n全部完成!")
