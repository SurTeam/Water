'use strict';
(() => {
 const $=id=>document.getElementById(id), status=text=>{$('status').textContent=text;};
 const maxFrame=16*1024*1024, maxPending=16*1024*1024;
 const systemMono='ui-monospace, "SFMono-Regular", Menlo, Consolas, "Liberation Mono", monospace';
 const fontChoices=[
  {id:'default',label:'System default',ui:'system-ui, sans-serif',terminal:systemMono},
  {id:'monospace',label:'System monospace',ui:systemMono,terminal:systemMono},
  ...['Menlo','Monaco','SF Mono','Consolas','Cascadia Mono','Courier New','DejaVu Sans Mono','Liberation Mono','Ubuntu Mono','Noto Sans Mono','Droid Sans Mono'].map(name=>({id:name,label:name,ui:`"${name}", monospace`,terminal:`"${name}", monospace`,name}))
 ];
 const settingsKey='water.web.settings';
 let availableFonts=[],fontChoice=fontChoices[0];
 let socket, descriptor, requestID=0, requests=new Map(), panes=[], selected='', active=null;
 let generation=0, reconnectTimer, ctrl=false, retry=0, userClosed=false;
 let keyboardHidden=true, servers=[], controlsCollapsed=false;
 const serversKey='water.web.servers';
 const decoder=new TextDecoder(), encoder=new TextEncoder();
 function fail(error){status(error instanceof Error?error.message:String(error));}
 function serverTarget(text,requireInvitation=false){
  let target;try{target=new URL(text);}catch{throw new Error('Enter an http:// or https:// server address.');}
  const invitation=/^#pair=[A-Za-z0-9_-]{43}$/.test(target.hash);
  if(!['http:','https:'].includes(target.protocol)||target.username||target.password||target.pathname!=='/'||target.search||(target.hash&&!invitation)||(requireInvitation&&!invitation))throw new Error(requireInvitation?'This is not a Water pairing QR code.':'Use a server address or a Water pairing link.');
  return target;
 }
 function rememberServer(origin){servers=[origin,...servers.filter(value=>value!==origin)].slice(0,16);}
 function saveServers(){
  try{localStorage.setItem(serversKey,JSON.stringify({version:1,origins:servers}));}
  catch{$('server-note').textContent='Website storage is unavailable. Addresses are kept for this visit only.';}
 }
 function importServers(text){
  if(!text||text.length>8192)return;
  try{
   const saved=JSON.parse(text);
   if(saved.version!==1||!Array.isArray(saved.origins)||saved.origins.length>16)return;
   const valid=saved.origins.map(origin=>{if(typeof origin!=='string'||origin.length>512)throw new Error('Invalid address');const target=serverTarget(origin);if(target.origin!==origin)throw new Error('Invalid address');return origin;});
   for(const origin of valid.reverse())rememberServer(origin);
  }catch{}
 }
 function initServers(){
  try{importServers(localStorage.getItem(serversKey));}catch{}
  importServers(new URLSearchParams(location.hash.slice(1)).get('servers'));
  rememberServer(location.origin);saveServers();
  $('servers').replaceChildren();
  for(const origin of servers){const option=document.createElement('option');option.value=origin;option.textContent=origin;$('servers').append(option);}
  $('servers').value=location.origin;
 }
 function openServer(target){
  rememberServer(location.origin);rememberServer(target.origin);saveServers();
  const fragment=new URLSearchParams();
  if(target.hash)fragment.set('pair',target.hash.slice(6));
  fragment.set('servers',JSON.stringify({version:1,origins:servers}));
  target.hash=fragment.toString();
  // Same-origin hash navigation does not rerun boot or exchange the invitation.
  if(target.origin===location.origin){history.replaceState(null,'',target.href);location.reload();}
  else location.assign(target.href);
 }
 function updateKeyboardButton(){
  const label=keyboardHidden?'Show keyboard':'Hide keyboard';
  $('keyboard-toggle').textContent=label;$('keyboard-toggle').setAttribute('aria-label',label);
 }
 function setControlsCollapsed(collapsed,save=false){
  controlsCollapsed=collapsed;$('top-controls').hidden=collapsed;
  $('controls-toggle').textContent=collapsed?'▸':'▾';
  $('controls-toggle').setAttribute('aria-expanded',String(!collapsed));
  $('controls-toggle').setAttribute('aria-label',collapsed?'Expand controls':'Collapse controls');
  if(save)try{localStorage.setItem('water.web.controls',JSON.stringify({version:1,collapsed}));}catch{}
  requestAnimationFrame(resize);
 }
 function initControls(){
  try{const saved=JSON.parse(localStorage.getItem('water.web.controls')||'{}');controlsCollapsed=saved.version===1&&saved.collapsed===true;}catch{}
  setControlsCollapsed(controlsCollapsed);
 }
 $('controls-toggle').addEventListener('pointerdown',event=>event.preventDefault());
 $('controls-toggle').onclick=()=>setControlsCollapsed(!controlsCollapsed,true);
 function restoreKeyboardFocus(){if(active&&!keyboardHidden)active.term.focus();}
 function installedFont(name){
  const context=document.createElement('canvas').getContext('2d');
  if(!context)return false;
  const sample='mmmmmmmmmmWWWWWWWWWWiiiii0123456789';
  return ['monospace','serif','sans-serif'].some(fallback=>{
   context.font=`72px ${fallback}`;const baseline=context.measureText(sample).width;
   context.font=`72px "${name}", ${fallback}`;
   return Math.abs(context.measureText(sample).width-baseline)>.01;
  });
 }
 function applyFont(){
  document.documentElement.style.setProperty('--web-font',fontChoice.ui);
  if(active)active.term.options.fontFamily=fontChoice.terminal;
  Promise.resolve(document.fonts?.ready).then(()=>requestAnimationFrame(resize));
 }
 function initSettings(){
  availableFonts=fontChoices.filter(font=>!font.name || installedFont(font.name));
  for(const font of availableFonts){const option=document.createElement('option');option.value=font.id;option.textContent=font.label;$('font-family').append(option);}
  try{
   const saved=JSON.parse(localStorage.getItem(settingsKey)||'{}');
   fontChoice=availableFonts.find(font=>font.id===saved?.font)||fontChoices[0];
  }catch{
   $('settings-note').textContent='Website storage is unavailable or invalid. Changes still apply for this visit.';
  }
  $('font-family').value=fontChoice.id;applyFont();
 }
 $('settings-open').onclick=()=>$('web-settings').showModal();
 $('font-family').onchange=()=>{
  fontChoice=availableFonts.find(font=>font.id===$('font-family').value)||fontChoices[0];
  applyFont();
  try{
   localStorage.setItem(settingsKey,JSON.stringify({version:1,font:fontChoice.id}));
   $('settings-note').textContent='Font saved in this browser for this website.';
  }catch{
   $('settings-note').textContent='Website storage is unavailable. Changes apply for this visit only.';
  }
 };
 $('web-settings').addEventListener('close',()=>{if(active&&!keyboardHidden)restoreKeyboardFocus();else $('settings-open').focus();});
 $('server-add').onclick=()=>$('server-dialog').showModal();
 $('server-close').onclick=()=>$('server-dialog').close();
 $('server-form').onsubmit=event=>{
  event.preventDefault();
  try{const value=$('server-address').value.trim();openServer(serverTarget(value.includes('://')?value:`http://${value}`));}
  catch(error){$('server-note').textContent=error.message;}
 };
 $('servers').onchange=()=>{try{openServer(serverTarget($('servers').value));}catch(error){fail(error);}};
 $('scan-dismiss').onclick=()=>{$('scan-feedback').hidden=true;};
 function disconnect(){
  generation++; clearTimeout(reconnectTimer);
  if(socket){socket.onclose=null;socket.close();socket=null;}
  for(const p of requests.values()){clearTimeout(p.timer);p.reject(new Error('Connection closed'));} requests.clear();
  if(active){active.term.dispose();active=null;}
 }
 function rpc(method,params={}) {
  return new Promise((resolve,reject)=>{
   if(!socket || socket.readyState!==WebSocket.OPEN){reject(new Error('Not connected'));return;}
   if(requests.size>=64 || socket.bufferedAmount>1024*1024){reject(new Error('Connection is busy'));return;}
   const id=++requestID;
   const data=encoder.encode(JSON.stringify({build_variant:descriptor.build_variant,protocol_version:descriptor.protocol_version,request_id:id,method,params}));
   const frame=new Uint8Array(data.length+4); new DataView(frame.buffer).setUint32(0,data.length);frame.set(data,4);
   const timer=setTimeout(()=>{requests.delete(id);reject(new Error('Request timed out'));},10000);
   requests.set(id,{resolve,reject,timer});socket.send(frame);
  });
 }
 async function command(command){
  const result=await rpc('command.dispatch',{command});
  if(result.status==='failed')throw new Error(result.error?.message||'Command failed');
  return result.result;
 }
 function input(data){
  if(!active || active.replaying)return;
  if(ctrl && data.length===1){data=String.fromCharCode(data.toUpperCase().charCodeAt(0)&31);ctrl=false;$('ctrl').setAttribute('aria-pressed','false');}
  command({type:'terminal.send_text',terminal_id:active.id,text:data}).catch(fail);
 }
 function output(slot,bytes){
  slot.pending+=bytes.length;
  if(slot.pending>maxPending){fail('Terminal output exceeded the buffer; reconnect to restore');socket?.close();return;}
  slot.term.write(bytes,()=>{slot.pending-=bytes.length;});
 }
 function event(id,seq,kind,bytes,columns,lines,code){
  const slot=active;
  if(!slot || slot.id!==id)return;
  if(!slot.ready){
   slot.waitingBytes+=bytes?.length||0;
   if(slot.waiting.length>=256 || slot.waitingBytes>maxPending){socket?.close();return;}
   slot.waiting.push([id,seq,kind,bytes,columns,lines,code]);return;
  }
  if(seq<=slot.seq)return;
  if(slot.seq && seq!==slot.seq+1n){fail('Terminal sequence gap; reconnecting');socket?.close();return;}
  slot.seq=seq;
  if(kind===1)output(slot,bytes);
  else if(kind===2)slot.term.resize(columns,lines);
  else if(kind===3)status('Terminal exited'+(code===null?'':': '+code));
 }
 function binary(data){
  if(data.length<29)throw new Error('Invalid terminal frame');
  const v=new DataView(data.buffer,data.byteOffset,data.byteLength);
  const hex=Array.from(data.subarray(5,21),b=>b.toString(16).padStart(2,'0')).join('');
  const id=`${hex.slice(0,8)}-${hex.slice(8,12)}-${hex.slice(12,16)}-${hex.slice(16,20)}-${hex.slice(20)}`;
  const kind=data[4],seq=v.getBigUint64(21);
  if(kind===1 || kind===2){
   if(data.length<33)throw new Error('Invalid terminal geometry');
   event(id,seq,kind,data.subarray(33),v.getUint16(29),v.getUint16(31),null);
  }else if(kind===3){if(data.length<34)throw new Error('Invalid terminal exit');event(id,seq,kind,null,0,0,data[29]?v.getInt32(30):null);}
  else throw new Error('Unknown terminal event');
 }
 function frame(data){
  if(data[0]===0 && data[1]===87 && data[2]===84 && data[3]===52){binary(data);return;}
  const msg=JSON.parse(decoder.decode(data));
  if(msg.ok!==undefined){
   const p=requests.get(msg.request_id);if(!p)return;requests.delete(msg.request_id);clearTimeout(p.timer);
   if(msg.ok)p.resolve(msg.result);else p.reject(new Error(msg.error?.message||'Request failed'));
  }else if(msg.method==='push.snapshot')render(msg.params);
 }
 function leaves(node,workspace,tab,result){
  if(!node)return;
  if(node.type==='split'){leaves(node.first,workspace,tab,result);leaves(node.second,workspace,tab,result);return;}
  const terminal=node.surface_state?.Terminal;
  if(terminal)result.push({id:terminal.terminal_id,pane:node.pane_id,workspace:workspace.id,tab:tab.id,label:`${workspace.title||'Workspace'} / ${tab.title||'Tab'} / ${terminal.title||terminal.process_name||'Terminal'}`});
 }
 function render(state){
  const next=[];
  for(const workspace of state.workspaces||[])for(const tab of workspace.tabs||[])leaves(tab.tree,workspace,tab,next);
  panes=next; const select=$('panes');select.replaceChildren();
  for(const pane of panes){const option=document.createElement('option');option.value=pane.id;option.textContent=pane.label;select.append(option);}
  if(!panes.some(p=>p.id===selected))selected=panes[0]?.id||'';
  select.value=selected;
  if(active?.id!==selected)attach(selected).catch(fail);
 }
 async function attach(id){
  const old=active;
  if(old){active=null;old.term.dispose();rpc('terminal.detach',{terminal_id:old.id}).catch(()=>{});}
  $('terminal').replaceChildren();
  if(!id){status('No terminals. Create a workspace.');return;}
  const term=new Terminal({cursorBlink:true,fontSize:14,fontFamily:fontChoice.terminal,scrollback:2000,screenReaderMode:true,theme:{background:'#1e1e20',foreground:'#e4e4e4'}});
  const fit=new FitAddon.FitAddon();term.loadAddon(fit);term.open($('terminal'));
  term.textarea.addEventListener('focus',()=>{keyboardHidden=false;updateKeyboardButton();});
  const slot={id,term,fit,seq:0n,ready:false,replaying:true,pending:0,waiting:[],waitingBytes:0};active=slot;
  // Replay must not send terminal query replies or other input side effects.
  term.onData(data=>{if(active===slot && !slot.replaying)input(data);});
  const replay=await rpc('terminal.attach',{terminal_id:id});
  if(active!==slot)return;
  term.resize(replay.size.columns,replay.size.lines);
  if(replay.first_seq && replay.first_seq>1)term.writeln('[Earlier terminal history is no longer retained]');
  slot.ready=true;
  for(const e of replay.replay||[]){
   if(typeof e.seq!=='string' || !/^\d+$/.test(e.seq))throw new Error('Invalid terminal sequence');
   const data=e.bytes?Uint8Array.from(atob(e.bytes),c=>c.charCodeAt(0)):null;
   event(id,BigInt(e.seq),e.type==='output'?1:e.type==='resize'?2:3,data,e.columns,e.lines,e.code);
  }
  term.write('',()=>{if(active===slot){slot.replaying=false;status('Connected');resize();}});
  const waiting=slot.waiting;slot.waiting=[];slot.waitingBytes=0;
  for(const args of waiting)event(...args);
 }
 function resize(){
  if(!active?.ready || active.replaying || document.hidden || !document.hasFocus())return;
  try{
   active.fit.fit();
   const size=`${active.term.cols}:${active.term.rows}`;
   if(size===active.sentSize)return;active.sentSize=size;
   command({type:'terminal.resize',terminal_id:active.id,columns:active.term.cols,lines:active.term.rows}).catch(fail);
  }catch(error){fail(error);}
 }
 async function focus(){
  if(!socket || socket.readyState!==WebSocket.OPEN)return;
  await rpc('session.focus',{focused:!document.hidden && document.hasFocus()});
  if(active)active.sentSize='';resize();
 }
 function paired(value){
  $('pairing-panel').hidden=value;
  for(const id of ['terminal-controls','terminal','keyboard-helpers','reconnect','logout'])$(id).hidden=!value;
 }
 async function exchangePairing(token){
  status('Pairing…');
  const result=await fetch('/api/pair',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify({token,name:navigator.userAgent.slice(0,80)})});
  if(!result.ok)throw new Error('Pairing failed: '+await result.text());
 }
 async function pairIfNeeded(token){
  const existing=await fetch('/api/session',{credentials:'same-origin',cache:'no-store'});
  if(existing.status===401)await exchangePairing(token);
  else if(!existing.ok)throw new Error('Server unavailable');
 }
 function scanStatus(text){setControlsCollapsed(false);$('scan-feedback').hidden=false;$('scan-message').textContent=text;}
 async function loadPhoto(file){
  const url=URL.createObjectURL(file);
  try{return await new Promise((resolve,reject)=>{
   const image=new Image(),timer=setTimeout(()=>reject(new Error('Photo loading timed out. Try a smaller JPEG or PNG.')),10000);
   image.onload=()=>{clearTimeout(timer);resolve(image);};
   image.onerror=()=>{clearTimeout(timer);reject(new Error('Could not read this photo. Try JPEG/PNG, or paste the pairing link with Add server.'));};
   image.src=url;
  });}finally{URL.revokeObjectURL(url);}
 }
 function decodePixels(pixels,remaining){
  return new Promise((resolve,reject)=>{
   const worker=new Worker('/qr-worker.js');
   const timer=setTimeout(()=>finish(new Error('QR decoding timed out. Try a closer, clearer photo.')),Math.max(1,Math.min(8000,remaining)));
   function finish(error,text){clearTimeout(timer);worker.terminate();error?reject(error):resolve(text);}
   worker.onmessage=event=>finish(event.data.error?new Error(event.data.error):null,event.data.text);
   worker.onerror=()=>finish(new Error('QR decoder could not start. Reload this page and try again.'));
   worker.postMessage({pixels:pixels.data.buffer,width:pixels.width,height:pixels.height},[pixels.data.buffer]);
  });
 }
 async function scanImage(input){
  const file=input.files?.[0];input.value='';if(!file)return;
  $('scan-qr').disabled=$('choose-qr').disabled=true;$('scan-dismiss').disabled=true;
  try{
   scanStatus('Reading photo on this device…');
   if(file.size>20*1024*1024)throw new Error('Choose an image smaller than 20 MB.');
   const image=await loadPhoto(file),width=image.naturalWidth,height=image.naturalHeight;
   if(!width||!height||width*height>64000000)throw new Error('This image is too large. Choose a smaller photo.');
   const deadline=Date.now()+15000;
   let text='';
   for(const [edge,crop] of [[1600,1],[2400,1],[2400,.5],[1000,1],[800,1]]){
    scanStatus('Looking for a QR code on this device…');
    if(Date.now()>deadline)throw new Error('QR decoding timed out. Try a closer, clearer photo.');
    const sourceWidth=width*crop,sourceHeight=height*crop,scale=Math.min(1,edge/Math.max(sourceWidth,sourceHeight));
    const canvas=document.createElement('canvas');canvas.width=Math.max(1,Math.round(sourceWidth*scale));canvas.height=Math.max(1,Math.round(sourceHeight*scale));
    const context=canvas.getContext('2d',{willReadFrequently:true});
    if(!context)throw new Error('This browser could not read the photo.');
    context.drawImage(image,(width-sourceWidth)/2,(height-sourceHeight)/2,sourceWidth,sourceHeight,0,0,canvas.width,canvas.height);
    const pixels=context.getImageData(0,0,canvas.width,canvas.height);
    try{text=await decodePixels(pixels,deadline-Date.now());}finally{canvas.width=canvas.height=1;}
    if(text)break;
   }
   if(!text)throw new Error('No QR code found. Move closer, keep the whole code in view, and try again. You can also paste its pairing link with Add server.');
   const target=serverTarget(text,true);
   scanStatus(`QR recognized. Opening ${target.origin}…`);openServer(target);
  }catch(error){scanStatus(error instanceof Error?error.message:String(error));}
  finally{$('scan-qr').disabled=$('choose-qr').disabled=false;$('scan-dismiss').disabled=false;}
 }
 async function connect(){
  disconnect();const current=generation;userClosed=false;status('Connecting…');
  const info=await fetch('/api/session',{credentials:'same-origin',cache:'no-store'});
  if(info.status===401){paired(false);status('Not paired. Scan a QR code to connect.');return;}
  if(!info.ok)throw new Error('Server unavailable');
  const data=await info.json();if(current!==generation)return;
  paired(true);
  descriptor=data.server;
  if(descriptor.api_signature!=='water-control/v6' || descriptor.protocol_version!==5)throw new Error('This Web client and server are incompatible');
  const ws=new WebSocket(location.origin.replace(/^http/,'ws')+'/ws');ws.binaryType='arraybuffer';socket=ws;
  let buffer=new Uint8Array(),processing=Promise.resolve();
  ws.onmessage=e=>{
   processing=processing.then(async()=>{
    if(current!==generation)return;
    const incoming=new Uint8Array(e.data);
    if(buffer.length+incoming.length>maxFrame+1024*1024)throw new Error('Frame buffer limit exceeded');
    const joined=new Uint8Array(buffer.length+incoming.length);joined.set(buffer);joined.set(incoming,buffer.length);buffer=joined;
    while(buffer.length>=4){
     const length=new DataView(buffer.buffer,buffer.byteOffset,4).getUint32(0);
     if(length>maxFrame)throw new Error('Frame size limit exceeded');
     if(buffer.length<length+4)break;
     const payload=buffer.slice(4,length+4);buffer=buffer.slice(length+4);frame(payload);await Promise.resolve();
    }
   }).catch(error=>{fail(error);ws.close();});
  };
  ws.onopen=async()=>{
   try{
    await rpc('session.open',{role:'web',compact_snapshots:true,client:{build_variant:descriptor.build_variant,protocol_version:5,api_signature:'water-control/v6',capabilities:['terminal-stream/v1','window-selection/v1']}});
    if(current!==generation)return;retry=0;await focus();render(await rpc('state.dump'));
   }catch(error){fail(error);ws.close();}
  };
  ws.onclose=()=>{
   if(current!==generation || userClosed)return;
   status('Disconnected. Reconnecting…');
   reconnectTimer=setTimeout(()=>connect().catch(fail),Math.min(1000*2**retry++,15000));
  };
  ws.onerror=()=>status('Connection error');
 }
 async function boot(){
  const fragment=new URLSearchParams(location.hash.slice(1)),token=fragment.get('pair');
  history.replaceState(null,'',location.pathname);
  if(token)await pairIfNeeded(token);
  await connect();
 }
 $('scan-qr').onclick=()=>$('qr-camera').click();
 $('choose-qr').onclick=()=>$('qr-image').click();
 for(const id of ['qr-camera','qr-image'])$(id).onchange=()=>scanImage($(id));
 $('panes').onchange=()=>{selected=$('panes').value;attach(selected).catch(fail);};
 $('reconnect').onclick=()=>connect().catch(fail);
 $('logout').onclick=async()=>{try{const response=await fetch('/api/logout',{method:'POST'});if(!response.ok)throw new Error('Could not unpair');userClosed=true;disconnect();paired(false);status('Unpaired. Scan a new QR code to reconnect.');}catch(error){fail(error);}};
 $('new-workspace').onclick=()=>command({type:'workspace.new'}).catch(fail);
 $('new-tab').onclick=()=>{const p=panes.find(p=>p.id===selected);if(p)command({type:'tab.new_in_workspace',workspace_id:p.workspace}).catch(fail);};
 $('split').onclick=()=>{const p=panes.find(p=>p.id===selected);if(p)command({type:'pane.split',pane_id:p.pane,direction:'right'}).catch(fail);};
 $('keyboard-toggle').onclick=()=>{
  if(!active)return;
  keyboardHidden=!keyboardHidden;
  if(keyboardHidden){ctrl=false;$('ctrl').setAttribute('aria-pressed','false');active.term.blur();}
  else active.term.focus();
  updateKeyboardButton();viewportChanged();
 };
 $('ctrl').onclick=()=>{ctrl=!ctrl;$('ctrl').setAttribute('aria-pressed',String(ctrl));restoreKeyboardFocus();};
 document.querySelectorAll('[data-key],#ctrl,#keyboard-toggle').forEach(button=>button.addEventListener('pointerdown',e=>e.preventDefault()));
 document.querySelectorAll('[data-key]').forEach(button=>{button.onclick=()=>{input(JSON.parse('"'+button.dataset.key+'"'));restoreKeyboardFocus();};});
 new ResizeObserver(()=>resize()).observe($('terminal'));
 window.addEventListener('focus',()=>focus().catch(fail));window.addEventListener('blur',()=>focus().catch(fail));
 document.addEventListener('visibilitychange',()=>{if(!document.hidden && socket?.readyState!==WebSocket.OPEN)connect().catch(fail);else focus().catch(fail);});
 // Mobile keyboards can shrink/pan the visual viewport without changing
 // layout viewport units. Keep the whole flex layout inside the visible area.
 let viewportFrame=0;
 function syncViewport(){
  viewportFrame=0;
  const viewport=window.visualViewport,style=document.documentElement.style;
  style.setProperty('--viewport-height',`${viewport?.height ?? window.innerHeight}px`);
  style.setProperty('--viewport-top',`${viewport?.offsetTop ?? 0}px`);
  resize();
 }
 function viewportChanged(){if(!viewportFrame)viewportFrame=requestAnimationFrame(syncViewport);}
 window.addEventListener('resize',viewportChanged);
 if(window.visualViewport){
  window.visualViewport.addEventListener('resize',viewportChanged);
  window.visualViewport.addEventListener('scroll',viewportChanged);
 }
 initSettings();
 initServers();
 initControls();
 updateKeyboardButton();
 syncViewport();
 boot().catch(fail);
})();
