# VPS 与 N1 安装及使用

推荐先使用[可视化安装向导](SETUP.zh-CN.md)，它会自动完成本文的首次管理通道、
配对、启动和验证步骤。以下命令行流程保留给高级用户和人工排障。
默认小盒插上网线已经能正常上网，不处理酒店认证。

本教程部署一台 AMD64 Debian/Ubuntu VPS 和一台已经运行 64 位 Debian/Armbian 的 N1。
不刷固件、不改启动器。所有真实 IP、公钥、设备编号和管理端口均由部署者提供。
快速入口准备程序及密钥；独立管理通道建立后，才允许启动数据通道。

## 1. 准备和一键安装

VPS 必须能被小盒主动访问。准备 VPS 管理 SSH、云厂商控制台，以及小盒本地密钥 SSH。
小盒接有线 WAN，必须具有普通 IPv4 默认网关。现有部署不会被覆盖。
需要允许 VPS 入站 TCP/443、UDP/51820 和已有管理 SSH 端口；
云安全组和本机防火墙由管理员按现状开放，安装器不会清空或改写它们。
TCP/443 被网站使用时安装器拒绝安装，应先决定服务端口规划。
住宅路由器不需要任何入站端口；不要给 N1 TCP/22 做公网映射。

分别执行：

```bash
# VPS
curl -fsSL https://raw.githubusercontent.com/acejunnan/edgebox/main/install.sh -o /tmp/edgebox-install.sh && sudo bash /tmp/edgebox-install.sh vps
```

```bash
# N1
curl -fsSL https://raw.githubusercontent.com/acejunnan/edgebox/main/install.sh -o /tmp/edgebox-install.sh && sudo bash /tmp/edgebox-install.sh n1
```

脚本通过 apt 安装基础工具，从官方 Go HTTPS 索引取得稳定工具链并校验 SHA-256，
在临时目录编译本仓库的程序。VPS 另外下载并校验固定版本 Xray 26.3.27 AMD64。
它不会安装或修改系统 Go，不会启动数据通道、Web、AP 或 OTA。
程序暂存于 `/opt/edgebox-install/`；唯一 WireGuard 私钥只写在各自主机
`/var/lib/edgebox/dataplane/wireguard.key`，0600。终端只显示公钥。
工具链来源：[Go 官方下载索引](https://go.dev/dl/?mode=json)；
代理来源：[Xray 固定版本](https://github.com/XTLS/Xray-core/releases/tag/v26.3.27)。

需要固定源码时，可先下载指定提交的安装脚本，再传入完整提交 SHA：

```bash
sudo env EDGEBOX_REF=实际的40位提交SHA bash /tmp/edgebox-install.sh n1
```

普通入口跟随 main，并非不可变发布。安装记录 `/opt/edgebox-install/source-commit`
保存本次源码提交。安装准备失败时先查看原因；不要直接删除已有设备配置或私钥后重试。
构建失败且未生成 staging 时可以重试。staging 已存在则应保留并检查，不支持自动覆盖升级。

## 2. 建立独立反向 SSH（首次部署必做）

已有合格的 `edge-management.service` 时，先用它新建一次远程 SSH 会话，
再进入第 3 节。没有时，按以下步骤建立。不要把历史设备的私钥复制给新盒子。
以下管理账户安装命令需 root；先确认账户、输出目录、目标文件和端口没有被占用。

N1 创建专用服务账户和每设备 Ed25519 密钥：

```bash
sudo useradd --system --home-dir /var/lib/edgebox/management --shell /usr/sbin/nologin edge-management
sudo install -d -o edge-management -g edge-management -m 0700 /var/lib/edgebox/management
sudo -u edge-management ssh-keygen -q -t ed25519 -N '' -f /var/lib/edgebox/management/id_ed25519
sudo cat /var/lib/edgebox/management/id_ed25519.pub
```

只把这份 `.pub` 公钥送到管理员工作目录，命名 `device.pub`。
在 VPS 读取 `/etc/ssh/ssh_host_ed25519_key.pub`，经已认证管理 SSH 传给 N1 的管理员，
命名 `bastion.pub`；用 `ssh-keygen -lf bastion.pub` 比对指纹。
不要仅信任未经核对的 `ssh-keyscan`。

在 N1 的 root 工作目录准备 `registry.json`。从
[`port-registry.example.json`](../deploy/management/port-registry.example.json) 复制结构，
将 `assignments` 改为真实设备编号和已确认空闲的每设备反向端口。
同一 VPS 的反向端口不能重复。示例 `lab-device-a` 不是生产设备编号。

在 root shell 中填写真实值，再渲染（`rendered-management` 必须不存在）：

```bash
read -r -p '设备编号: ' DEVICE_ID
read -r -p 'VPS 公网 IPv4 或管理域名: ' BASTION
read -r -p 'VPS 现有 SSH 端口: ' SSH_PORT
/opt/edgebox-install/edge-management-render \
  -registry ./registry.json -device-id "$DEVICE_ID" \
  -bastion-host "$BASTION" -bastion-ssh-port "$SSH_PORT" \
  -device-public-key ./device.pub -bastion-host-key ./bastion.pub \
  -output ./rendered-management
```

输出中 `RemoteForward` 必须是 `127.0.0.1:已分配端口 127.0.0.1:22`。
把 `edge-tunnel-sshd.conf` 和 `tunnel-authorized_keys` 通过管理员通道送到 VPS。
在 VPS 创建全新专用账户，安装这两份文件：

```bash
sudo useradd --system --create-home --home-dir /var/lib/edgebox-tunnel --shell /usr/sbin/nologin edge-tunnel
sudo install -d -o edge-tunnel -g edge-tunnel -m 0700 /var/lib/edgebox-tunnel/.ssh
sudo install -o edge-tunnel -g edge-tunnel -m 0600 tunnel-authorized_keys /var/lib/edgebox-tunnel/.ssh/authorized_keys
sudo install -o root -g root -m 0600 edge-tunnel-sshd.conf /etc/ssh/sshd_config.d/90-edgebox-tunnel.conf
sudo /usr/sbin/sshd -t
```

先确认主 sshd 配置确实 Include 了 `/etc/ssh/sshd_config.d/*.conf`，再用
`sudo sshd -T -C user=edge-tunnel,host=localhost,addr=127.0.0.1` 检查生效设置：
`authenticationmethods publickey`、`passwordauthentication no`、
`kbdinteractiveauthentication no`、`gatewayports no`、`allowtcpforwarding remote`、`maxsessions 0`。
同时确认管理员账户的有效配置未变。
账户如被系统锁定，按[管理账户说明](../deploy/management/README.md)验证解锁方案，
不要为了登录而启用密码认证。

SSH 配置修改前保持原管理会话、保存配置备份并准备独立恢复手段。
对于上述**新建且原先不存在**的 drop-in，可先设置五分钟自动撤销：

```bash
sudo sh -c 'printf "#!/bin/sh\nset -eu\nrm -f /etc/ssh/sshd_config.d/90-edgebox-tunnel.conf\n/usr/sbin/sshd -t\nsystemctl reload ssh\n" > /root/edgebox-ssh-rollback.sh; chmod 700 /root/edgebox-ssh-rollback.sh'
sudo systemd-run --unit=edgebox-ssh-rollback --on-active=5m /root/edgebox-ssh-rollback.sh
sudo systemctl reload ssh
```

该示例的服务名为 Debian/Ubuntu 的 `ssh`。实际系统服务名不同应按实际名称修改，
并在 reload 前检查。撤销脚本不能用于覆盖已有同名文件的场景。

N1 安装渲染产物：

```bash
sudo install -d -m 0755 /etc/edgebox
sudo install -o root -g edge-management -m 0640 rendered-management/edge-management.ssh_config /etc/edgebox/edge-management.ssh_config
sudo install -o edge-management -g edge-management -m 0600 rendered-management/known_hosts /var/lib/edgebox/management/known_hosts
sudo install -o root -g root -m 0644 rendered-management/edge-management.service /etc/systemd/system/edge-management.service
sudo systemd-analyze verify /etc/systemd/system/edge-management.service
sudo systemctl daemon-reload
sudo systemctl enable --now edge-management.service
```

从管理员电脑通过 VPS 的 SSH ProxyJump，新建到 VPS `127.0.0.1:已分配端口`
的 SSH 会话，验证实际进入 N1。N1 用户登录使用独立的管理员公钥，
与仅用于建立隧道的 `edge-management` 私钥不同。
确认新建的 VPS 管理登录和 N1 反向登录都成功后，在 VPS 取消 SSH 回退：
`sudo systemctl stop edgebox-ssh-rollback.timer`。
N1 本地 sshd 也应配置为管理员公钥登录，先验证密钥，再关闭密码认证。

## 3. 两端配对并暂存配置

取两端安装时显示的 **WireGuard 公钥**，它不同于 SSH 公钥。
丢失输出时执行 `sudo cat /var/lib/edgebox/dataplane/wireguard.pub`。

在 VPS 执行，下列参数文字要换成真实值：

```bash
sudo edgeboxctl configure --peer 'N1的WireGuard公钥' --relay-host 'VPS真实公网IPv4'
```

在 N1 执行：

```bash
sudo edgeboxctl configure --peer 'VPS的WireGuard公钥' --relay-host 'VPS真实公网IPv4' --mtu 1380 --bandwidth-mbit 10
```

此时不会启动服务。配置、服务和回退脚本已暂存；30 分钟安装回退计时器已设置。
本安装配置固定使用 `ebwg0`、内部 `10.77.0.1/32`（VPS）及 `10.77.0.2/32`（N1）、
路由表 177、规则 10777。它们是本实现的明确保留资源，预检查有冲突即拒绝；
不要在已有 VPN 或 EdgeBox 上强行跳过检查。
MTU 1380 和 10 Mbps 是本次安装默认值，可以通过参数在首次配置时调整；不是通用最佳值。
VPS 的 Xray 配置只允许客户经 N1 出口；N1 不通时不能切回 VPS 直出。

## 4. 启动、验证、提交

先 VPS、再 N1：

```bash
sudo edgeboxctl start
sudo edgeboxctl status
```

启动前再次检查管理保护文件；N1 还要求管理服务 active。
独立 systemd 回退计时器先于网络变更设置，30 分钟内未提交则自动停止数据服务、
清理自有网络状态，并将 N1 IPv4 forwarding 恢复为启动前的值。
启动异常立即回退。不会替换主默认路由，也不会清空全局防火墙。
`active` 不能证明反向 SSH 真能访问，务必完成下面的实际检查。

从 VPS root 权限读取私有客户端链接：

```bash
sudo cat /var/lib/edgebox/dataplane/client-link.txt
```

将链接私下导入支持 VLESS + REALITY 的电脑或手机客户端，选择此节点并启用代理。
验证网页、DNS 和 UDP；出口 IPv4 应等于 N1 当前 WAN 的公网出口，而非 VPS。
另从管理员电脑**新建**一次 VPS 登录和 N1 反向 SSH 登录，检查普通 DNS、HTTPS、WAN
默认路由及其他原有服务。不要把一直未断的旧 SSH 会话当作重新连接成功。

确认通过后，在**两端分别**执行：

```bash
sudo edgeboxctl commit --verified
```

提交要求本机观测为 ONLINE；`--verified` 表示操作员已完成上面的外部检查，
程序不能代替客户端验收。提交启用开机启动并取消该主机的自动回退。
N1 保存仅含 `net.ipv4.ip_forward=1` 的自有 sysctl 文件；不改 WAN 默认路由。

## 5. 维护、安全模式及限制

```bash
sudo edgeboxctl status
sudo journalctl -u edge-dataplane -n 100 --no-pager
sudo journalctl -u edge-management -n 100 --no-pager   # N1
sudo journalctl -u edge-proxy -n 100 --no-pager        # VPS
sudo edgeboxctl safe-mode
```

安全模式不删除密钥和暂存配置。当前安装控制器只面向一次新安装；进入安全模式后，
不要重新跑安装覆盖。重新启用前管理员应检查故障、重新建立回退计时器，
在 N1 恢复 IPv4 forwarding，再启动原服务并重新验收。
普通已提交服务重启可使用 `systemctl restart edge-dataplane`。
服务支持握手超时后的清理重试及 WAN 网关重新发现；仍需测试实际路由器、内核和运营商。

快速安装不自动启用 Web、管理 AP 和 OTA，因为其绑定地址、无线接口、密码散列及
更新信任根必须由部署者确定。相应程序已编译在 `/opt/edgebox-install/`，
可用 `--help` 查看参数；服务模板位于 `deploy/console/`，热点逻辑位于 `internal/ap/`。
Web 节点设置尚未接入真实数据通道；应用 OTA 也不负责升级内核、系统或特权网络执行器。

本次新增安装器的验证包括本地 Go 单测、Python 无网络安全测试、Shell 语法和 Linux
交叉编译；这些不等于新机器完整安装验收。新安装器尚未在干净 VPS/Armbian 上端到端实测。
上线前必须在自己的环境验证重启、断线重连、DHCP 更新、UDP、实际出口、
管理隧道重建、定时回退和数据故障时管理仍可访问。
