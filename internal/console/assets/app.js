'use strict';
let csrf='',page='dashboard',poll;
const $=s=>document.querySelector(s), U=globalThis.EdgeUI;
function message(text){$('#message').textContent=text;}
async function api(path,body){
  let response;try{response=await fetch('/api/'+path,{method:body===undefined?'GET':'POST',credentials:'same-origin',headers:body===undefined?{}:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:body===undefined?undefined:JSON.stringify(body)});}catch{throw Error('暂时连接不到盒子。若刚更新软件或更改热点，请稍等后重新连接。');}
  let data;try{data=await response.json();}catch{throw Error('服务正在切换，请稍后刷新页面。');}
  if(!response.ok){if(response.status===401)showLogin();throw Error(U.error(data.error));}return data;
}
async function uploadPackage(formData){
  let response;try{response=await fetch('/api/updates/upload',{method:'POST',credentials:'same-origin',headers:{'X-CSRF-Token':csrf},body:formData});}catch{throw Error('暂时连接不到盒子或上传中断，请检查网络后重试。');}
  let data;try{data=await response.json();}catch{throw Error('服务正在切换或返回异常，请稍后刷新。');}
  if(!response.ok){if(response.status===401)showLogin();throw Error(U.error(data.error));}return data;
}
function showLogin(){$('#login').hidden=false;$('#console').hidden=true;$('#logout').hidden=true;csrf='';clearTimeout(poll);}
function showConsole(s){csrf=s.csrf;$('#login').hidden=true;$('#console').hidden=false;$('#logout').hidden=false;}
function textNode(tag,text){const n=document.createElement(tag);n.textContent=text;return n;}
function card(title,value,detail){const n=document.createElement('article');n.className='card';n.append(textNode('small',title));const v=textNode('div',String(value??'暂时无法读取'));v.className='value';n.append(v);if(detail)n.append(textNode('small',detail));return n;}
function button(label,action){const b=textNode('button',label);b.type='button';b.onclick=async()=>{b.disabled=true;try{await action();}catch(e){message(e.message);}finally{b.disabled=false;}};return b;}
function form(template,submit){const f=document.createElement('form');f.className='panel';f.innerHTML=template;f.addEventListener('submit',async e=>{e.preventDefault();const b=f.querySelector('button[type=submit]')||f.querySelector('button');b.disabled=true;try{await submit(Object.fromEntries(new FormData(f)));}catch(err){message(err.message);}finally{b.disabled=false;}});return f;}
function rows(root,items){const list=document.createElement('dl');list.className='facts';items.forEach(([k,v])=>{list.append(textNode('dt',k),textNode('dd',v||'暂时无法读取'));});root.append(list);}
function selected(n){return n.profiles.find(p=>p.id===n.preferred);}
async function saveNodes(n,profiles,preferred){return api('nodes',{expected:n.revision,desired:{schema:1,revision:0,profiles,preferred,active:'',operational_status:''}});}
function nodeEditor(root,n,item){
  const editor=form('<h2>节点资料</h2><label>节点名称<input name="name" required maxlength="80" placeholder="例如：我的服务器"></label><label>服务器 IP 或域名<input name="address" required placeholder="填写服务商提供的地址" autocomplete="off"></label><label>端口<input name="port" type="number" min="1" max="65535" required placeholder="填写服务商提供的端口"></label><label>连接方式<select name="transport"><option value="wireguard">WireGuard</option><option value="masque">MASQUE（预留）</option><option value="tls">TLS（预留）</option></select></label><label>备注（可不填）<input name="description" maxlength="512"></label><p class="muted">不知道地址或端口？请向节点提供方索取，盒子不能自动猜测。这里只保存资料，暂时不会启动加速。</p><button type="submit">保存节点</button>',async d=>{
    const id=item?.id||'node-'+Array.from(crypto.getRandomValues(new Uint8Array(8)),x=>x.toString(16).padStart(2,'0')).join('');
    const next={id,name:d.name.trim(),endpoint:U.endpoint(d.address,d.port,d.transport),transport:d.transport,description:d.description};
    const profiles=item?n.profiles.map(p=>p.id===id?next:p):[...n.profiles,next];await saveNodes(n,profiles,n.preferred||id);await render();message('节点资料已保存，尚未开始加速。');
  });
  if(item){const a=U.address(item.endpoint);for(const [k,v]of Object.entries({...item,...a}))if(editor.elements[k])editor.elements[k].value=v;}
  editor.append(button('取消',()=>render()));root.append(editor);editor.scrollIntoView({behavior:'smooth',block:'start'});
}
async function render(){
  clearTimeout(poll);message('');const root=$('#page');root.replaceChildren();root.append(textNode('h1',({dashboard:'设备概览',nodes:'加速节点',diagnostics:'网络检查',settings:'设置',updates:'软件更新'})[page]));document.querySelectorAll('nav button').forEach(b=>b.classList.toggle('active',b.dataset.page===page));
  try{
    if(page==='dashboard'){
      const [d,n]=await Promise.all([api('dashboard'),api('nodes')]);const grid=document.createElement('div');grid.className='cards';
      const dp=d.dataplane||{}, fb=d.fallbacks||{}, hm=d.hotel_mode||{};
      const dpOnline=dp.state==='ONLINE';
      const dpText=dpOnline?(dp.transport==='wireguard'?'已接通（WireGuard 主线路）':'已接通'):(fb.edge_bridge?'TCP 备用就绪（VLESS 443）':'未连接');
      const dpDetail=dpOnline?`接口: ${dp.interface||'ebwg0'} · 带宽: ${dp.bandwidth_mbit?dp.bandwidth_mbit+' Mbps':'不限速'}`:(fb.edge_bridge?'主线路异常时自动切换 TCP 备用':'加速核心未启动');
      const fbText=(fb.edge_bridge&&fb.edge_mgmt_alt)?'TCP/443 数据与应急管理均就绪':'部分就绪';
      const fbDetail=`数据备用: ${fb.edge_bridge?'就绪 (VLESS 443)':'未就绪'} · 应急管理: ${fb.edge_mgmt_alt?'就绪 (端口 22011)':'未就绪'}`;
      if(hm.active){
        const remain=Math.max(0,Math.round(((hm.expires_at||0)-Date.now()/1000)/60));
        grid.append(card('酒店认证协助模式','生效中',`热点临时直通酒店外网，约剩余 ${remain} 分钟后自动恢复防护`));
      }
      [['网页版本',d.version],['网络加速',dpText,dpDetail],['双重备用通道',fbText,fbDetail],['当前出口 IP',d.public_ip||'尚未检测',dpOnline?'经由国内中继/加速节点出口':'可到“网络检查”页面手动检测'],['网线连接',d.ethernet==='up'?'已连接':'未连接'],['热点名称',d.hotspot?.ssid],['远程维护',d.management?.status==='healthy'?'已连接':'暂未连接','用于维护盒子，不是加速节点'],['本次开机已运行',U.duration(d.uptime_seconds),'格式：年 月 日 小时:分钟'],['CPU 温度',d.temperature||'暂时无法读取'],['内存',U.usage(d.memory_usage),d.memory_usage?`已用 ${Math.round(d.memory_usage.used_percent)}%`:''],['磁盘',U.usage(d.disk_usage),d.disk_usage?`已用 ${Math.round(d.disk_usage.used_percent)}%`:'']].forEach(f=>grid.append(card(...f)));root.append(grid);
    }else if(page==='nodes'){
      const n=await api('nodes'),node=selected(n);root.append(card('当前加速连接','尚未启用','没有正在使用的加速节点或实际连接 IP。添加节点只保存资料，不会假装已经连接。'));
      root.append(card('计划使用的节点',node?.name||'尚未选择',node?.endpoint||'添加服务器资料后，可以在下方一键选用。'));
      if(!n.profiles.length)root.append(textNode('p','还没有添加节点，所以列表是空的。准备好节点的 IP 或域名、端口后，点击“添加节点”。'));
      root.append(button('添加节点',()=>nodeEditor(root,n,null)));
      for(const p of n.profiles){const c=card(p.name,U.addressLabel(p.endpoint),p.description);c.append(textNode('p',p.id===n.preferred?'已选为计划使用的节点 · 尚未连接':'已保存 · 尚未连接'));
        c.append(button(p.id===n.preferred?'重新保存为首选':'选用此节点',async()=>{await saveNodes(n,n.profiles,p.id);await render();message('已保存选择；加速功能尚未接通，不会立即连接。');}),button('修改资料',()=>nodeEditor(root,n,p)),button('删除',async()=>{if(!confirm('确定删除这个节点的资料？'))return;await saveNodes(n,n.profiles.filter(x=>x.id!==p.id),n.preferred===p.id?'':n.preferred);await render();}));root.append(c);}
      root.append(button('恢复上一次保存的节点设置',async()=>{if(!confirm('确定恢复上一次保存的节点设置？'))return;await api('nodes/rollback',{expected:n.revision});await render();}));
    }else if(page==='diagnostics'){
      const d=await api('diagnostics'), dp=d.dataplane||{}, fb=d.fallbacks||{}, hm=d.hotel_mode||{};
      rows(root,[['网线连接',d.ethernet==='up'?'已连接':'未连接'],['热点名称',d.hotspot?.ssid],['网络加速',dp.state==='ONLINE'?'已接通（'+(dp.transport||'wireguard')+'）':'未接通'],['TCP 备用通道',fb.edge_bridge?'运行中 (TCP/443)':'未运行'],['应急管理备用',fb.edge_mgmt_alt?'运行中 (端口 22011)':'未运行'],['远程维护',d.management?.status==='healthy'?'已连接':'暂未连接'],['当前出口 IP',d.public_ip||'尚未检测']]);
      const hPanel=document.createElement('div');hPanel.className='panel';
      hPanel.innerHTML='<h2>酒店网页认证模式</h2><p>在酒店等场所插上网线后若无法上网，通常需要用手机在浏览器中完成酒店认证（如输入房号与姓氏）。开启后将临时允许热点设备直连酒店网络 10 分钟以完成登录。认证完成后会自动恢复安全隔离。</p>';
      const remainMin=Math.max(0,Math.round(((hm.expires_at||0)-Date.now()/1000)/60));
      hPanel.append(textNode('p',hm.active?`当前状态：已开启认证模式（约剩余 ${remainMin} 分钟）。完成认证后可提前手动关闭。`:'当前状态：已关闭（热点处于安全隔离模式）'));
      if(!hm.active){
        hPanel.append(button('开启酒店认证模式（10 分钟）',async()=>{await api('hotel-portal',{action:'enable',duration:600});await render();message('已开启酒店认证模式，请用手机打开任意网页完成酒店登录。');}));
      }else{
        hPanel.append(button('立即关闭认证模式',async()=>{await api('hotel-portal',{action:'disable'});await render();message('已恢复安全隔离模式。');}));
      }
      root.append(hPanel);
      const result=document.createElement('div');root.append(button('开始网络检查',async()=>{message('正在检查，请稍等……');const r=await api('probes',{});result.replaceChildren();rows(result,[['域名查询',r.dns?'正常':'未通过'],['外部网站连接',r.outbound_https?'正常':'未通过'],['维护服务器连接',r.bastion_tcp?'正常':'未通过'],['当前出口 IP',r.public_ip||'暂未获取']]);message('检查完成。');}));root.append(result);
      const a=textNode('a','下载检查报告（给维护人员）');a.href='/api/bundle';a.className='button';root.append(a);const details=document.createElement('details');details.append(textNode('summary','查看维护详情'));details.append(textNode('pre',JSON.stringify(d,null,2)));root.append(details);
    }else if(page==='settings'){
      root.append(form('<h2>修改网页登录密码</h2><p>至少八位，不要求特殊符号。不要使用常见默认密码。</p><label>现在的密码<input type="password" name="current" autocomplete="current-password" required></label><label>新的密码<input type="password" name="next" minlength="8" maxlength="128" autocomplete="new-password" required></label><button type="submit">保存新密码</button>',async d=>{await api('password',d);showLogin();message('密码已修改，请用新密码登录。');}));
      const d=await api('dashboard'),h=d.hotspot||{};const f=form('<h2>修改盒子热点</h2><p>保存后手机会暂时断开，请用新名称和密码重新连接。不会修改家里的路由器。</p><label>热点名称<input name="ssid" maxlength="32" required></label><label>新的热点密码<input name="password" type="password" minlength="8" maxlength="63" required></label><button type="submit">保存热点设置</button>',async value=>{if(!confirm('保存后会断开热点，确定继续？'))return;await api('hotspot',{...value,expected:h.revision});message('设置已保存，请重新连接热点。');});f.elements.ssid.value=h.ssid||'';root.append(f);
      root.append(button('重新启动管理页面',async()=>{if(!confirm('管理页面将短暂断开，但不会重启盒子。继续？'))return;await api('restart',{service:'web'});message('正在重新启动，请稍后刷新页面。');}));
    }else if(page==='updates'){
      const u=await api('updates'),s=u.state||{};const grid=document.createElement('div');grid.className='cards';[['当前版本',s.current||'尚未读取'],['更新状态',u.busy?'正在下载或更新，请稍等':U.update(s.phase)],['上一个可用版本',s.previous||'暂无'],['恢复保障',u.rollback_available?'新版本失败时自动恢复':'首次安装，尚无上一个版本']].forEach(f=>grid.append(card(...f)));root.append(grid);
      const result=({downloading:'正在从网址下载并检查签名……',download_verified:'下载完成，签名已通过。请在下方选择版本安装。',download_rejected:'下载或校验失败，当前软件未被替换。',running:'更新任务正在执行……',committed:'更新完成，已检查新版本。',update_failed_or_rolled_back:'更新未成功，请查看上方状态；不要重复安装同一个失败版本。'})[u.result];if(result)root.append(textNode('p',result));
      const upForm=form('<h2>离线上传更新包</h2><p>若网络受限或无法从外网下载，可在此直接上传维护人员提供的签名更新包（.zip 文件）。上传后会自动校验数字签名并放入待安装列表。</p><label>选择更新包文件（.zip）<input type="file" name="package" accept=".zip" required></label><button type="submit">上传并校验更新包</button>',async()=>{
        const fileInput=upForm.querySelector('input[type=file]');
        if(!fileInput?.files?.length){message('请先选择要上传的 .zip 更新包');return;}
        const fd=new FormData();fd.append('package',fileInput.files[0]);
        message('正在上传更新包并验证签名，请稍候……');
        const res=await uploadPackage(fd);
        await render();
        message(`更新包 ${res.version} 上传成功并通过数字签名校验！请在下方选择该版本并点击安装。`);
      });
      root.append(upForm);
      root.append(form('<h2>从网址获取更新</h2><p>粘贴维护人员提供的 HTTPS 签名应用包地址。盒子会自动下载并验签，下载本身不会立即安装。不支持普通压缩包或整机刷机包。</p><label>更新包网址<input name="url" type="url" required placeholder="粘贴维护人员提供的完整 HTTPS 地址" autocomplete="off"></label><button type="submit">下载并检查更新包</button>',async d=>{await api('updates/download',d);await render();message('下载任务已交给盒子，你可以留在此页等待结果。');}));
      const f=form('<h2>安装已检查的更新</h2><label>可安装版本<select name="release" required></select></label><p>安装时网页会短暂断开。如果新版本无法启动，盒子会尝试恢复原来的版本。</p><button type="submit">确认安装此版本</button>',async d=>{if(!confirm('确定安装所选版本？页面会短暂断开。'))return;await api('updates/apply',d);await render();});
      const available=(u.available||[]).filter(r=>r.sequence>s.highest_sequence&&r.version!==s.current);available.forEach(r=>{const o=textNode('option',r.version+' · 已通过签名检查');o.value=r.version;f.elements.release.append(o);});f.querySelector('button').disabled=u.busy||!available.length;if(!available.length)f.append(textNode('p','目前没有可安装的新版本。'));root.append(f);if(u.busy){root.querySelectorAll('button').forEach(b=>b.disabled=true);poll=setTimeout(()=>{if(page==='updates')render();},3000);}
    }
  }catch(e){message(e.message);}
}
$('#login-form').addEventListener('submit',async e=>{e.preventDefault();try{const s=await api('login',Object.fromEntries(new FormData(e.target)));e.target.elements.password.value='';showConsole(s);await render();}catch(err){message(err.message);}});
$('#logout').onclick=async()=>{try{await api('logout',{});}finally{showLogin();}};
document.querySelectorAll('nav button').forEach(b=>b.onclick=()=>{page=b.dataset.page;render();});
api('session').then(s=>{showConsole(s);render();}).catch(()=>showLogin());
