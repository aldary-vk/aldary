#!/bin/bash

# 配置,依次为
#代理服务器
PROXY_ADDR="127.0.0.1:1080"
#生成的隧道接口名称，ip a命令可以查看到虚拟网卡
TUN_NAME="tun6"
#隧道的IP配置，本地是TUN_IP，虚拟网关TUN_GW
TUN_IP="198.18.0.1"
TUN_GW="198.18.0.2"
#服务器原有网关
ORIGINAL_GW="192.168.0.253"
#服务器对外服务地址，WAN口实际地址，用ip a命令查看
WAN_IP="192.168.0.243"
#服务器WAN口网卡
INTERFACE="eth0"
#tun2socks隧道对接代理软件的安装地址
TUN2SOCKS_BIN="/usr/local/bin/tun2socks"

# 错误处理
cleanup() {
    echo "清理环境，恢复网络..."
    # 删除可能存在的 tun 路由
    ip route del default via $TUN_GW dev $TUN_NAME 2>/dev/null || true

    # 恢复原始默认路由（如果不存在则添加）
    if ! ip route | grep -q "default via $ORIGINAL_GW"; then
        ip route add default via $ORIGINAL_GW dev $INTERFACE 2>/dev/null || true
    fi

    ip link set $TUN_NAME down 2>/dev/null || true
    ip tuntap del mode tun dev $TUN_NAME 2>/dev/null || true
    pkill -f "$TUN2SOCKS_BIN" 2>/dev/null || true

    echo "网络已恢复"
    exit 1
}
trap cleanup ERR INT TERM

echo "=== 步骤1: 清理环境 ==="
pkill -f "$TUN2SOCKS_BIN" 2>/dev/null || true
ip link set $TUN_NAME down 2>/dev/null || true
ip tuntap del mode tun dev $TUN_NAME 2>/dev/null || true
sleep 1

# 清理残留的路由（关键：先清理所有默认路由，避免 File exists）
echo "清理残留默认路由..."
ip route flush default 2>/dev/null || true
# 恢复一条原始路由（临时用，避免断网）
ip route add default via $ORIGINAL_GW dev $INTERFACE metric 200 2>/dev/null || true

echo "=== 步骤2: 创建TUN设备 ==="
ip tuntap add mode tun dev $TUN_NAME
ip addr add $TUN_IP/30 dev $TUN_NAME
ip link set dev $TUN_NAME up

echo "=== 步骤3: 启动tun2socks ==="
if [ ! -f "$TUN2SOCKS_BIN" ]; then
    echo "错误：找不到 $TUN2SOCKS_BIN"
    cleanup
fi

nohup $TUN2SOCKS_BIN -device $TUN_NAME -proxy socks5://$PROXY_ADDR -interface $INTERFACE > /var/log/tun2socks.log 2>&1 &
sleep 3

# 检查启动状态
if ! ip link show $TUN_NAME | grep -q "state UP"; then
    echo "错误：tun0 启动失败"
    cat /var/log/tun2socks.log | tail -5
    cleanup
fi

if ! pgrep -f "$TUN2SOCKS_BIN" > /dev/null; then
    echo "错误：tun2socks 进程不存在"
    cleanup
fi

echo "tun2socks 启动成功"

echo "=== 步骤4: 配置例外路由（不走代理的网段）==="
# 这些网段走原始网关
for net in "38.60.163.177/32"  "114.114.114.114/32" "8.153.194.23/32" "192.168.0.0/16"  "172.16.0.0/12" "172.17.0.0/16"; do
    ip route add $net via $ORIGINAL_GW dev $INTERFACE 2>/dev/null || true
done

echo "=== 步骤5: 切换默认路由到TUN ==="
# 关键：先删除所有默认路由（包括刚才临时加的），再添加新的
ip route flush default
# 添加 TUN 默认路由（metric 100）
ip route add default via $TUN_GW dev $TUN_NAME metric 100
# 保留原始路由作为备用（metric 300，优先级低）
ip route add default via $ORIGINAL_GW dev $INTERFACE metric 300


grep -q "200 proxy" /etc/iproute2/rt_tables || echo "200 proxy" >> /etc/iproute2/rt_tables


ip route add default via $ORIGINAL_GW dev $INTERFACE table proxy

ip route flush table proxy

#以下二条是关键配置，否则运行后本地服务地址不能连接，比如SSH端口不能连接
ip rule add to $WAN_IP table proxy

ip rule add from $WAN_IP table proxy


ip route add default via $ORIGINAL_GW dev $INTERFACE table proxy


ip route flush cache

echo "=== 验证 ==="
echo "当前默认路由："
ip route | grep default
echo ""

echo "测试代理连接..."
if curl -s --max-time 8 -o /dev/null -w "%{http_code}" http://httpbin.org/ip | grep -q "200"; then
    echo "✓ 代理成功！"
    echo "查看日志: tail -f /var/log/tun2socks.log"
    echo "停止命令: sudo /usr/local/bin/stop-tun-proxy.sh"
else
    echo "✗ 测试失败，查看日志排查"
    tail -10 /var/log/tun2socks.log
fi

trap - ERR INT TERM
