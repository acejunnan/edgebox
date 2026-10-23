package setup

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"edgebox/internal/management"
)

//go:embed remote.py
var remoteScript string

type Machine interface {
	Run(context.Context, string, string) (string, error)
	Reverse(context.Context, Host, Identity) error
	Close() error
}

func (r Remote) Close() error { return r.Client.Close() }
func (r Remote) Reverse(ctx context.Context, target Host, id Identity) error {
	methods, e := auth(target)
	if e != nil {
		return e
	}
	conn, e := r.Client.Dial("tcp", "127.0.0.1:22001")
	if e != nil {
		return errors.New("反向 SSH 端口尚未就绪")
	}
	c, e := authenticate(conn, target, id, methods)
	if e != nil {
		return e
	}
	defer c.Close()
	_, e = (Remote{Client: c, Host: target}).Run(ctx, "true", "")
	return e
}

type Installer struct {
	SourceRef string
	Dial      func(context.Context, Host, Identity) (Machine, error)
	// Emit receives only fixed progress text, never passwords, keys or command output.
	Emit func(string)
}
type Result struct {
	ClientLink string `json:"client_link"`
	PublicIP   string `json:"public_ip"`
}

func operation(ctx context.Context, m Machine, mode string, params map[string]any) (string, error) {
	if params == nil {
		params = map[string]any{}
	}
	params["mode"] = mode
	input, e := json.Marshal(params)
	if e != nil {
		return "", e
	}
	return m.Run(ctx, "python3 -c "+quote(remoteScript), string(input))
}

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

func ResolveSource(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/acejunnan/edgebox/commits/main", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, e := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if e != nil {
		return "", errors.New("无法查询 GitHub 安装版本，请检查本机网络")
	}
	defer resp.Body.Close()
	var v struct {
		SHA string `json:"sha"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&v) != nil || !commitSHA.MatchString(v.SHA) {
		return "", errors.New("无法取得有效安装版本")
	}
	return v.SHA, nil
}
func (i Installer) Install(ctx context.Context, req Request, vpsID, edgeID Identity) (result Result, err error) {
	if e := req.Validate(); e != nil {
		return result, e
	}
	if !commitSHA.MatchString(i.SourceRef) {
		return result, errors.New("源码版本无效")
	}
	dial := i.Dial
	if dial == nil {
		dial = func(ctx context.Context, h Host, id Identity) (Machine, error) {
			c, e := Connect(ctx, h, id)
			if e != nil {
				return nil, e
			}
			return Remote{Client: c, Host: h}, nil
		}
	}
	emit := i.Emit
	if emit == nil {
		emit = func(string) {}
	}
	emit("正在登录 VPS 和小盒")
	vps, e := dial(ctx, req.VPS, vpsID)
	if e != nil {
		return result, fmt.Errorf("VPS：%w", e)
	}
	defer vps.Close()
	edge, e := dial(ctx, req.Edge, edgeID)
	if e != nil {
		return result, fmt.Errorf("小盒：%w", e)
	}
	defer edge.Close()
	step := func(m Machine, mode string, p map[string]any) (string, error) { return operation(ctx, m, mode, p) }
	emit("检查两端系统、现有部署和 WAN，并保存变更前状态")
	for index, m := range []Machine{vps, edge} {
		role := "vps"
		if index == 1 {
			role = "edge"
		}
		if _, e = step(m, "preflight", map[string]any{"role": role}); e != nil {
			return result, fmt.Errorf("首次安装检查未通过：%w", e)
		}
	}
	for index, m := range []Machine{vps, edge} {
		role := "vps"
		label := "VPS"
		if index == 1 {
			role = "n1"
			label = "小盒"
		}
		emit("正在为" + label + "安装依赖和编译程序，首次构建可能需要数分钟")
		script := "set -eu\ncommand -v curl >/dev/null || { apt-get update; apt-get install -y --no-install-recommends ca-certificates curl; }\nwork=$(mktemp -d /var/tmp/edgebox-wizard.XXXXXXXX)\ntrap 'rm -rf -- \"$work\"' EXIT\ncurl -fsSL " + quote("https://raw.githubusercontent.com/acejunnan/edgebox/"+i.SourceRef+"/install.sh") + " -o \"$work/install.sh\"\nEDGEBOX_REF=" + quote(i.SourceRef) + " bash \"$work/install.sh\" " + role + "\n"
		if _, e = m.Run(ctx, "bash -s", script); e != nil {
			return result, fmt.Errorf("%s 程序准备失败：%w", label, e)
		}
	}
	emit("为小盒生成独立管理密钥并配置受限的反向 SSH")
	key, e := step(edge, "edge-key", nil)
	if e != nil {
		return result, e
	}
	deviceKey, e := management.ParsePublicKey(strings.TrimSpace(key))
	if e != nil {
		return result, errors.New("小盒返回的 SSH 公钥无效")
	}
	hostKey, e := management.ParsePublicKey(vpsID.PublicKey)
	if e != nil {
		return result, errors.New("VPS 主机公钥无效")
	}
	random := make([]byte, 8)
	if _, e = rand.Read(random); e != nil {
		return result, e
	}
	deviceID := "edge-" + hex.EncodeToString(random)
	artifacts, e := management.Render(management.RenderInput{Config: management.RenderConfig{
		DeviceID: deviceID, BastionHost: req.VPS.IP, BastionSSHPort: uint16(req.VPS.Port), TunnelUser: "edge-tunnel", ServiceUser: "edge-management", ServiceGroup: "edge-management", AssignedPort: 22001,
		PrivateKeyPath: "/var/lib/edgebox/management/id_ed25519", KnownHostsPath: "/var/lib/edgebox/management/known_hosts", ClientConfigPath: "/etc/edgebox/edge-management.ssh_config", SSHBinaryPath: "/usr/bin/ssh", LocalSSHPort: uint16(req.Edge.Port), ServerAliveInterval: 30, ServerAliveCountMax: 3, ConnectTimeout: 15,
	}, DevicePublicKey: deviceKey, BastionHostKey: hostKey})
	if e != nil {
		return result, e
	}
	files := map[string]string{}
	for _, f := range artifacts.Files() {
		files[f.Name] = string(f.Content)
	}
	if _, e = step(vps, "bastion", map[string]any{"port": 22001, "operator": req.VPS.User, "device_id": deviceID, "authorized_keys": files["tunnel-authorized_keys"], "policy": files["edge-tunnel-sshd.conf"]}); e != nil {
		return result, fmt.Errorf("堡垒机管理配置未通过验证，十分钟内自动撤销：%w", e)
	}
	if _, e = step(edge, "edge-management", map[string]any{"files": files, "device_id": deviceID}); e != nil {
		return result, e
	}
	// Fresh login through the bastion, pinned to the original edge host key.
	emit("通过 VPS 反向端口新建小盒 SSH 会话，验证独立管理通道")
	if e = retry(ctx, 30*time.Second, func() error { return vps.Reverse(ctx, req.Edge, edgeID) }); e != nil {
		return result, errors.New("反向 SSH 验证未通过；未启动数据通道，堡垒机变更将自动撤销")
	}
	if _, e = step(vps, "management-commit", nil); e != nil {
		return result, e
	}
	// Once verified, management deliberately survives any later data-plane failure.
	rollback := false
	defer func() {
		if err != nil && rollback {
			emit("数据配置未通过验证，正在回到安全模式；反向管理保留")
			for _, m := range []Machine{edge, vps} {
				cleanup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				_, _ = operation(cleanup, m, "safe-mode", nil)
				cancel()
			}
		}
	}()
	vpsKey, e := step(vps, "public-key", nil)
	if e != nil {
		return result, e
	}
	edgeKey, e := step(edge, "public-key", nil)
	if e != nil {
		return result, e
	}
	emit("自动交换 WireGuard 公钥，暂存配置并设置独立回退计时器")
	rollback = true
	if _, e = step(vps, "configure", map[string]any{"peer": strings.TrimSpace(edgeKey), "relay": req.VPS.IP}); e != nil {
		return result, e
	}
	if _, e = step(edge, "configure", map[string]any{"peer": strings.TrimSpace(vpsKey), "relay": req.VPS.IP}); e != nil {
		return result, e
	}
	for _, m := range []Machine{vps, edge} {
		if _, e = step(m, "start", nil); e != nil {
			return result, e
		}
	}
	emit("验证真实客户链路和小盒住宅出口，确认 WAN 和管理通道仍可用")
	e = retry(ctx, 100*time.Second, func() error {
		direct, e := step(edge, "egress", map[string]any{"role": "edge"})
		if e != nil {
			return e
		}
		via, e := step(vps, "egress", map[string]any{"role": "relay"})
		if e != nil {
			return e
		}
		if strings.TrimSpace(direct) != strings.TrimSpace(via) {
			return errors.New("出口不一致")
		}
		for _, m := range []Machine{edge, vps} {
			if _, e = step(m, "healthy", nil); e != nil {
				return e
			}
		}
		result.PublicIP = strings.TrimSpace(direct)
		return vps.Reverse(ctx, req.Edge, edgeID)
	})
	if e != nil {
		return result, errors.New("真实出口或管理验证失败，正在自动回退；请检查 VPS TCP/443、UDP/51820 和小盒 WAN")
	}
	emit("验证通过，保存开机启动配置")
	for _, m := range []Machine{edge, vps} {
		if _, e = step(m, "commit", nil); e != nil {
			return result, e
		}
	}
	link, e := step(vps, "client-link", nil)
	if e != nil {
		return result, e
	}
	link = strings.TrimSpace(link)
	if !strings.HasPrefix(link, "vless://") || len(link) > 4096 {
		return result, errors.New("客户端链接无效")
	}
	result.ClientLink = link
	rollback = false
	emit("安装完成，可复制私人连接链接导入客户端")
	return result, nil
}
func retry(ctx context.Context, timeout time.Duration, fn func() error) error {
	end := time.Now().Add(timeout)
	for {
		e := fn()
		if e == nil {
			return nil
		}
		if time.Now().After(end) {
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}
