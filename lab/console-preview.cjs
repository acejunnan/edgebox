// Loopback-only visual/interaction fixture; never installed on an EdgeBox.
const http=require('node:http'),fs=require('node:fs'),path=require('node:path');
const assets=path.join(__dirname,'../internal/console/assets');
let nodes={schema:1,revision:0,profiles:[],preferred:'',active:'',operational_status:'not_implemented'};
http.createServer(async(req,res)=>{
  const send=data=>{res.setHeader('Content-Type','application/json');res.end(JSON.stringify(data));};
  if(req.url==='/api/session')return send({csrf:'preview-only'});
  if(req.url==='/api/nodes'){
    if(req.method==='POST'){let body='';for await(const c of req)body+=c;const d=JSON.parse(body);nodes={...d.desired,revision:nodes.revision+1};}
    return send(nodes);
  }
  if(req.url==='/api/dashboard'||req.url==='/api/diagnostics')return send({version:'界面预览（模拟数据）',uptime_seconds:90061.5,temperature:'49.5 °C',memory_usage:{total_bytes:1879048192,used_bytes:335544320,used_percent:18},disk_usage:{total_bytes:6871947674,used_bytes:2147483648,used_percent:31},ethernet:'up',hotspot:{ssid:'EdgeBox-Preview',revision:1},management:{status:'healthy'},vpn_core:{installed:false,running:false},public_ip:''});
  if(req.url==='/api/updates')return send({state:{phase:'committed',current:'预览版本',highest_sequence:1},busy:false,result:'',available:[]});
  const name={'/':'index.html','/app.js':'app.js','/ui.js':'ui.js','/style.css':'style.css'}[req.url];
  if(!name){res.writeHead(404);return res.end();}
  res.setHeader('Content-Type',name.endsWith('.js')?'application/javascript':name.endsWith('.css')?'text/css':'text/html; charset=utf-8');
  let data=fs.readFileSync(path.join(assets,name));if(name==='index.html')data=Buffer.from(data.toString().replace('<main>','<main><p>界面测试：以下为模拟数据，不代表 N1 状态。</p>'));res.end(data);
}).listen(18081,'127.0.0.1',()=>console.log('UI fixture on http://127.0.0.1:18081 (mock data only)'));
