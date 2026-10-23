// Package setup implements the workstation-only first-install wizard.
package setup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type Host struct {
	IP         string `json:"ip"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Password   string `json:"password,omitempty"`
	KeyFile    string `json:"key_file,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}
type Request struct {
	VPS  Host `json:"vps"`
	Edge Host `json:"edge"`
}
type Identity struct {
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key"`
}

var username = regexp.MustCompile(`^[a-z_][a-zA-Z0-9_-]{0,31}$`)

func (r *Request) Validate() error {
	for i, h := range []*Host{&r.VPS, &r.Edge} {
		a, e := netip.ParseAddr(h.IP)
		if e != nil || !a.Is4() || !a.IsGlobalUnicast() || a.IsLinkLocalUnicast() {
			return errors.New("请填写有效的 IPv4 地址")
		}
		if i == 0 && (a.IsPrivate() || netip.MustParsePrefix("100.64.0.0/10").Contains(a)) {
			return errors.New("VPS 必须填写公网 IPv4；小盒填写本机能访问的局域网 IP")
		}
		if h.Port == 0 {
			h.Port = 22
		}
		if h.User == "" {
			h.User = "root"
		}
		if h.Port < 1 || h.Port > 65535 || !username.MatchString(h.User) {
			return errors.New("SSH 用户名或端口无效")
		}
		if len(h.Password) > 1024 || len(h.KeyFile) > 4096 || len(h.Passphrase) > 1024 {
			return errors.New("登录信息过长")
		}
	}
	if r.VPS.IP == r.Edge.IP {
		return errors.New("VPS 和小盒必须是不同的机器")
	}
	return nil
}
func address(h Host) string { return net.JoinHostPort(h.IP, fmt.Sprint(h.Port)) }

// Inspect aborts at the host-key callback, before authentication can send a password.
func Inspect(ctx context.Context, h Host) (Identity, error) {
	var id Identity
	stop := errors.New("host identity captured")
	conf := &ssh.ClientConfig{User: h.User, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		id = Identity{ssh.FingerprintSHA256(k), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))}
		return stop
	}}
	c, e := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address(h))
	if e != nil {
		return id, errors.New("无法连接 SSH，请检查 IP、端口和网络")
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(12 * time.Second))
	_, _, _, _ = ssh.NewClientConn(c, address(h), conf)
	if id.Fingerprint == "" {
		return id, errors.New("未能读取 Ed25519 SSH 主机指纹，请检查 SSH 服务")
	}
	return id, nil
}
func auth(h Host) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	files := []string{h.KeyFile}
	if h.KeyFile == "" {
		home, _ := os.UserHomeDir()
		files = []string{filepath.Join(home, ".ssh", "id_ed25519"), filepath.Join(home, ".ssh", "id_rsa")}
	}
	for _, file := range files {
		data, e := os.ReadFile(file)
		if e != nil {
			if h.KeyFile != "" {
				return nil, errors.New("无法读取所选 SSH 私钥文件")
			}
			continue
		}
		var signer ssh.Signer
		if h.Passphrase != "" {
			signer, e = ssh.ParsePrivateKeyWithPassphrase(data, []byte(h.Passphrase))
		} else {
			signer, e = ssh.ParsePrivateKey(data)
		}
		if e != nil {
			if h.KeyFile != "" {
				return nil, errors.New("私钥无效或需要正确的解密口令")
			}
			continue
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if h.Password != "" {
		methods = append(methods, ssh.Password(h.Password))
	}
	if len(methods) == 0 {
		return nil, errors.New("没有可用 SSH 密钥，请展开登录设置填写私钥路径或 SSH 密码")
	}
	return methods, nil
}
func Connect(ctx context.Context, h Host, id Identity) (*ssh.Client, error) {
	methods, e := auth(h)
	if e != nil {
		return nil, e
	}
	conn, e := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address(h))
	if e != nil {
		return nil, errors.New("SSH 连接失败")
	}
	return authenticate(conn, h, id, methods)
}
func authenticate(conn net.Conn, h Host, id Identity, methods []ssh.AuthMethod) (*ssh.Client, error) {
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	conf := &ssh.ClientConfig{User: h.User, Auth: methods, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		if ssh.FingerprintSHA256(k) != id.Fingerprint {
			return errors.New("SSH 主机指纹已变化，连接已停止")
		}
		return nil
	}}
	c, ch, req, e := ssh.NewClientConn(conn, address(h), conf)
	if e != nil {
		conn.Close()
		return nil, errors.New("SSH 登录失败或主机指纹变化，请检查登录设置")
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, ch, req), nil
}

type Remote struct {
	Client *ssh.Client
	Host   Host
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (r Remote) Run(ctx context.Context, command, input string) (string, error) {
	s, e := r.Client.NewSession()
	if e != nil {
		return "", errors.New("SSH 会话中断")
	}
	defer s.Close()
	s.Stdin = strings.NewReader(input)
	if r.Host.User != "root" {
		command = "sudo -n " + command
	}
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() { b, e := s.CombinedOutput(command); done <- result{b, e} }()
	select {
	case <-ctx.Done():
		_ = s.Close()
		return "", errors.New("操作超时或已停止，远端回退计时器仍独立运行")
	case res := <-done:
		if res.err != nil {
			return "", errors.New("远端步骤失败（已停止后续操作；请检查系统条件、免密 sudo 和安装状态）")
		}
		if len(res.out) > 2<<20 {
			return "", errors.New("远端输出超出限制")
		}
		return string(res.out), nil
	}
}
