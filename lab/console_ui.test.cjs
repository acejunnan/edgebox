const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const ui=require('../internal/console/assets/ui.js');
test('uptime uses elapsed seconds, readable YMD HH:MM, not idle CPU time',()=>{
  assert.equal(ui.duration(90061.75),'0年0月1日 01:01');
  assert.equal(ui.duration(0),'0年0月0日 00:00');
  assert.equal(ui.duration((365+30+2)*86400+3660),'1年1月2日 01:01');
  for(const value of [null,undefined,-1,NaN,'90061 999999']) assert.equal(ui.duration(value),'暂时无法读取');
});
test('capacity shows used and total amounts',()=>{
  assert.match(ui.usage({total_bytes:2*1073741824,used_bytes:1073741824}),/已用 1.00 GB \/ 共 2.00 GB/);
  assert.equal(ui.usage(null),'暂时无法读取');
});
test('node form builds addresses without JSON and rejects credentials',()=>{
  assert.equal(ui.endpoint('vpn.example.org','51820','wireguard'),'udp://vpn.example.org:51820');
  assert.equal(ui.endpoint('2001:db8::1','443','tls'),'tls://[2001:db8::1]:443');
  for(const address of ['https://vpn.example.org','user:pass@vpn.example.org','vpn.example.org?token=x'])assert.throws(()=>ui.endpoint(address,443,'tls'));
  for(const port of [0,65536,'oops'])assert.throws(()=>ui.endpoint('vpn.example.org',port,'wireguard'));
});
test('everyday pages do not expose raw configuration or device identifier cards',()=>{
  const app=fs.readFileSync(path.join(__dirname,'../internal/console/assets/app.js'),'utf8');
  assert.ok(!app.includes('节点配置（JSON 数组）'));
  assert.ok(!app.includes("['设备',d.device_id"));
  assert.ok(app.includes('网络加速'));
  assert.ok(app.includes('网页版本'));
  assert.ok(app.includes('updates/download'));
  assert.ok(app.includes('计划使用的节点'));
  assert.ok(!app.includes('hotel-portal'));
  assert.ok(!app.includes('酒店'));
});
