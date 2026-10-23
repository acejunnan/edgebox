#!/usr/bin/python3
"""Build an offline owner-only client handoff; never print its credential."""
import argparse
import html
import pathlib
import sys

p = argparse.ArgumentParser()
p.add_argument('directory')
p.add_argument('--qr-lib')
a = p.parse_args()
if a.qr_lib:
    sys.path.insert(0, a.qr_lib)
import qrcode

root = pathlib.Path(a.directory)
link = (root / 'EdgeBox-N1.txt').read_text(encoding='utf-8').strip()
assert link.startswith('vless://') and 'sni=www.cloudflare.com' in link
image = qrcode.make(link)
image.save(root / 'EdgeBox-N1-QR.png')
assert image.size[0] >= 300 and image.size[0] == image.size[1]
page = '''<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>EdgeBox 连接说明</title>
<style>body{max-width:720px;margin:40px auto;padding:0 24px;font:17px/1.8 system-ui;color:#182b36}img{width:310px;max-width:100%%}textarea{width:100%%;height:130px;word-break:break-all}button{padding:12px 24px;font-size:17px}small{color:#536573}</style>
<h1>连接你的 N1 出口</h1><p>路径：电脑／手机 → VPS 中转机器 → N1 → N1 所在地网络。</p>
<h2>电脑：使用已安装的 v2rayN</h2><ol><li>复制下方完整节点链接。</li><li>切换到 v2rayN，按 Ctrl+V 导入。</li><li>选择 EdgeBox-N1 作为活动节点，启用系统代理。首次测试可选择全局代理，测试后按需切回分流。</li></ol>
<textarea id="node" readonly>%s</textarea><p><button onclick="const e=document.getElementById('node');e.select();document.execCommand('copy');this.textContent='已复制'">复制节点</button></p>
<h2>手机：使用支持 VLESS + REALITY 的客户端</h2><p>例如 iPhone 上的 Shadowrocket、Android 上的 v2rayNG。在客户端中选择扫码导入，再连接这个节点。</p><img src="EdgeBox-N1-QR.png" alt="私人连接二维码">
<h2>如何确认</h2><p>连接后打开 <a href="https://checkip.amazonaws.com">出口 IP 查询</a>。2026-10-15 在当前住宅网络实测为 <b>（以实际查询结果为准）</b>。N1 到当地接入网络后，出口应随当地网络变化；节点中的VPS 地址不需要因此改变。</p>
<p>目前为 IPv4 出口，带宽上限 10 Mbps。N1 离线时此节点停止转发。网页管理热点只供管理，不能当上网热点使用。</p>
<p>只会代理客户端规则指定的流量；未启用代理或规则选择直连的流量仍走设备原网络。手机、当地网络及更换路由器后的表现还需现场测试。</p>
<small>节点链接和二维码相当于连接凭证，请勿公开。电脑完整链路、TCP、UDP、断线后不改走德国出口及崩溃恢复已经实测。现有 Web 2c.2 的节点管理仍未接入新通道；请以客户端实际访问结果为准。</small></html>''' % html.escape(link)
(root / '使用说明.html').write_text(page, encoding='utf-8')
try:
    import cv2
    decoded, _, _ = cv2.QRCodeDetector().detectAndDecode(cv2.imread(str(root / 'EdgeBox-N1-QR.png')))
    assert decoded == link, 'QR decode verification failed'
    print('QR decode verification passed')
except ImportError:
    pass
print('Offline client handoff generated; bearer credentials suppressed')
