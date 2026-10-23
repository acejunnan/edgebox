# EdgeBox

把 N1 或其他低功耗 Linux 设备变成可以远程维护的住宅 IPv4 出口。
设备主动连接 VPS，适用于 NAT、CGNAT 和动态公网 IP；不需要在住宅路由器上开放端口。

```text
电脑 / 手机（VLESS + REALITY）
          ↓
VPS：Xray 客户端入口 → WireGuard 内部运输
          ↓（连接由小盒主动建立）
N1 / ARM64 Linux：独立策略路由 → nftables NAT → 住宅 WAN → Internet

运维人员 → VPS 本地回环转发端口 → 独立反向 SSH → 小盒
```

目标是出口故障时仍能修复设备。管理 SSH、设备本机的 DNS/HTTPS 和普通 WAN
路径独立于转发数据通道；停止数据通道会清理 EdgeBox 自有网络状态。

## 安装入口

适用：Debian/Ubuntu AMD64 VPS，以及已安装 64 位 Debian/Armbian 的 N1。
N1 裸机刷写、引导程序、磁盘分区不在安装脚本范围内。其他 ARM64 Linux 设备也可作为 edge。
运行前阅读 **[完整安装与配对教程](docs/INSTALL.zh-CN.md)**。

VPS：

```bash
curl -fsSL https://raw.githubusercontent.com/acejunnan/edgebox/main/install.sh -o /tmp/edgebox-install.sh && sudo bash /tmp/edgebox-install.sh vps
```

N1：

```bash
curl -fsSL https://raw.githubusercontent.com/acejunnan/edgebox/main/install.sh -o /tmp/edgebox-install.sh && sudo bash /tmp/edgebox-install.sh n1
```

这是“一条命令安装程序并准备密钥”。首次部署还必须配置独立反向 SSH、交换两端公钥、
填写真实 VPS 地址，再验证并提交。脚本不会猜测凭证，也不会直接修改现有部署。
源码构建需要访问 GitHub、go.dev 和 Go 模块服务，N1 上可能需要数分钟及足够的可用磁盘/内存。

## 组件与完成范围

| 组件 | 当前作用 |
| --- | --- |
| `edge-agent` | 身份、持久状态、状态机和结构化健康的基础实现；不是完整中央控制后台 |
| `edge-dataplane` | 两端 WireGuard 生命周期；IPv4 策略路由、NAT、限速、故障清理及恢复 |
| `edge-management-render` | 生成独立反向 SSH 服务、固定主机密钥和堡垒机限制 |
| Xray（外部项目） | VPS 的 VLESS + REALITY 入口，只经小盒出口转发 |
| `edge-web` / `edge-updater` | 本地 Web 控制台与独立签名应用更新器；源码已包含，快速安装不自动启用 |
| `edge-ap-render` | 隔离管理热点配置生成器；具体无线硬件须单独验收 |
| `edge-release` | 签名应用发布工具；签名私钥由部署者单独保管 |

快速安装路径目前面向**新部署的一台 VPS + 一台小盒**。内部资源 `ebwg0`、
`10.77.0.0/24`、路由表 `177`、规则优先级 `10777` 和 `inet edgebox` 固定保留，
安装前检查冲突。多个盒子、多租户、中央配置下发、MASQUE、完整 IPv6 和整机 OTA 尚未实现。
原有 Web 的节点表不会自动切换这里的真实数据通道。

## 日常使用

1. 配对并提交后，在 VPS 读取 `/var/lib/edgebox/dataplane/client-link.txt`，私下导入支持 VLESS + REALITY 的电脑或手机客户端。
2. 启用客户端代理；访问出口 IP 查询服务，结果应与小盒所在网络的公网 IPv4 一致。
3. `sudo edgeboxctl status` 查看数据通道状态；`journalctl -u edge-dataplane -n 100` 查看日志。
4. `sudo edgeboxctl safe-mode` 停止数据通道并清理其自有状态，独立管理 SSH 保留。

`ONLINE` 代表运输握手和本机配置的观测，仍需真实客户端测试 TCP、UDP、DNS 与最终出口。
N1 更换地点后接入新的有线网络，住宅出口随 WAN 改变；客户仍连接同一 VPS 地址。
管理热点不作为上网热点。IPv6、直连分流流量须在客户端另行控制。

## 开发与资料

```bash
go test ./...
go vet ./...
python3 -m unittest discover -s deploy -p 'test_*.py' -v
```

- [架构及当前边界](docs/ARCHITECTURE.md)
- [管理面设计](docs/REMOTE_MANAGEMENT.md)
- [管理配置生成器](deploy/management/README.md)
- [失败模型](docs/FAILURE_MODEL.md)
- [v0.1 计划](docs/V0.1_PLAN.md)
- [开发和远程操作约束](AGENTS.md)

公开源码不包含设备私钥、客户端链接、发布签名私钥、生产配置、个人审计记录或构建 ZIP。
`deploy/fallback` 等历史运维工具仅供参考，不属于快速安装流程，不应未经审核用于现有机器。
公开源码不等同于已经完成新安装路径的实机验收；具体测试范围见安装教程。
