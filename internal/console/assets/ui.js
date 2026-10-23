'use strict';
const EdgeUI = {
  duration(value) {
    if (value === null || value === undefined || !Number.isFinite(Number(value)) || Number(value) < 0) return '暂时无法读取';
    let minutes = Math.floor(Number(value) / 60);
    const m = minutes % 60; minutes = Math.floor(minutes / 60);
    const h = minutes % 24; let days = Math.floor(minutes / 24);
    const years = Math.floor(days / 365); days %= 365;
    const months = Math.floor(days / 30); days %= 30;
    return `${years}年${months}月${days}日 ${String(h).padStart(2,'0')}:${String(m).padStart(2,'0')}`;
  },
  bytes(value) {
    if (!Number.isFinite(Number(value)) || Number(value) < 0) return '未知';
    const n = Number(value); return n >= 1073741824 ? (n/1073741824).toFixed(2)+' GB' : (n/1048576).toFixed(0)+' MB';
  },
  usage(value) {
    if (!value || !(value.total_bytes > 0)) return '暂时无法读取';
    return `已用 ${this.bytes(value.used_bytes)} / 共 ${this.bytes(value.total_bytes)}`;
  },
  endpoint(address, port, transport) {
    address=address.trim();
    if (!address || /[\s\/@?#\\]/.test(address)) throw Error('请填写服务器的 IP 或域名，不要填写网址、用户名或密码。');
    const n=Number(port); if (!Number.isInteger(n)||n<1||n>65535) throw Error('端口需要填写 1 到 65535 的整数。');
    if (!['wireguard','masque','tls'].includes(transport)) throw Error('请选择连接方式。');
    if(address.includes(':')&&!address.startsWith('[')) address='['+address+']';
    return (transport==='wireguard'?'udp':transport==='masque'?'https':'tls')+'://'+address+':'+n;
  },
  address(endpoint) { try { const u=new URL(endpoint);return {address:u.hostname,port:u.port||(u.protocol==='https:'?'443':'')}; } catch {return {address:'',port:''};} },
  addressLabel(endpoint) {const a=this.address(endpoint);return a.address+(a.port?':'+a.port:'');},
  error(code) {
    return ({invalid_credentials:'用户名或密码不正确。',rate_limited:'尝试次数较多，请等一分钟后再试。',unauthorized:'请先登录。',csrf_rejected:'登录状态已变化，请刷新页面后重试。',origin_rejected:'请求来源不受信任，请重新打开本机页面。',revision_conflict:'设置已被修改，请刷新后重试。',configuration_rejected:'设置未保存，请检查地址、端口和名称。',operation_rejected_or_failed:'操作未完成，请检查填写内容或到网络检查页查看状态。',current_password_invalid:'当前密码不正确。',invalid_password:'新密码不符合要求，请换一个至少八位、不是常见默认值的密码。',invalid_request:'填写内容有误，请检查后重试。',rollback_unavailable_or_conflict:'没有可恢复的设置，或设置已更新，请刷新重试。',diagnostics_unavailable:'暂时读不到设备状态，请稍后重试。',updater_unavailable:'更新服务暂时不可用，请稍后重试。',upload_too_large_or_invalid:'上传的文件过大或格式无效（仅支持 128MB 以内的 .zip 签名更新包）。',missing_package_file:'请选择要上传的更新包文件。',invalid_or_untrusted_package:'更新包未通过签名验证或不兼容当前设备，已被拒绝。'})[code] || '操作未完成，请检查填写内容并稍后重试。';
  },
  update(phase) {return ({unprovisioned:'尚未初始化',committed:'当前版本运行正常',rolled_back:'更新未成功，已恢复上一个可用版本',prepared:'已校验，正在准备更新',activated:'正在启动新版本',verifying:'正在检查新版本是否正常',failed:'自动恢复未完成，请联系维护人员'})[phase]||'正在读取状态';}
};
if (typeof module !== 'undefined') module.exports=EdgeUI;
else globalThis.EdgeUI=EdgeUI;
