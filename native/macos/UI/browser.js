(() => {
 const token=location.hash.slice(1);
 const terminal=new Terminal({cursorBlink:true,fontFamily:'Menlo, Monaco, monospace',fontSize:14,theme:{background:'#101114'},scrollback:10000});
 const fit=new FitAddon.FitAddon();terminal.loadAddon(fit);terminal.open(document.querySelector('#terminal'));fit.fit();
 window.installTerminalLinks?.(terminal,text=>{const url=new URL(text);if(url.protocol==='https:'&&!url.username&&!url.password)window.open(url.href,'_blank','noopener,noreferrer');});
 let fitting=false;function scheduleFit(){if(fitting)return;fitting=true;requestAnimationFrame(()=>{fitting=false;fit.fit();});}
 document.fonts.ready.then(scheduleFit);
 const loading=document.querySelector('#loading'),reconnect=document.querySelector('#reconnect');
 let socket,generation=0,credit=0,pending=[],ready=false,imagePaste=false;
 const bytes64=bytes=>btoa(String.fromCharCode(...bytes));
 function send(frame){if(socket?.readyState===WebSocket.OPEN)socket.send(JSON.stringify(frame));}
 function flush(){while(ready&&credit>0&&pending.length){const item=pending[0];if(!item.data)return;const size=Math.min(credit,item.data.length,16384);send({type:item.type,data:bytes64(item.data.subarray(0,size)),bytes:item.total||0});item.total=0;item.data=item.data.slice(size);credit-=size;if(!item.data.length)pending.shift();}}
 function input(bytes){if(!ready||pending.filter(item=>item.type==='input').reduce((n,item)=>n+item.data.length,0)+bytes.length>65536)return;pending.push({type:'input',data:bytes});flush();}
 terminal.onData(text=>input(new TextEncoder().encode(text)));
 terminal.onBinary(text=>input(Uint8Array.from(text,c=>c.charCodeAt(0))));
 terminal.onResize(({cols,rows})=>{if(ready)send({type:'resize',columns:cols,rows});});
 new ResizeObserver(scheduleFit).observe(document.querySelector('#terminal'));
 function connect(){
  const epoch=++generation;socket?.close();pending=[];credit=0;ready=false;imagePaste=false;loading.hidden=false;reconnect.hidden=true;
  socket=new WebSocket(`ws://${location.host}/socket`);
  socket.onopen=()=>{if(epoch!==generation)return;fit.fit();send({token,columns:terminal.cols,rows:terminal.rows});};
  socket.onmessage=event=>{
   if(epoch!==generation)return;
   const frame=JSON.parse(event.data);
   if(frame.type==='metadata'){
    if(frame.title)document.title=frame.title;
    if(/^\/icons\/[a-z0-9-]+\.svg$/.test(frame.icon)){let icon=document.querySelector('link[rel="icon"]');if(!icon){icon=document.createElement('link');icon.rel='icon';document.head.append(icon);}icon.href=frame.icon;}
   }
   else if(frame.type==='receipt'){ready=true;imagePaste=(frame.capabilities||[]).includes('image_paste_v1');credit=65536;terminal.reset();terminal.focus();scheduleFit();flush();}
   else if(frame.type==='data'){loading.hidden=true;const data=Uint8Array.from(atob(frame.data),c=>c.charCodeAt(0));terminal.write(data,()=>{if(epoch===generation){send({type:'credit',bytes:data.length});scheduleFit();}});}
   else if(frame.type==='input_credit'){credit+=frame.bytes;flush();}
   else if(frame.type==='paste_error'){showPasteError(frame.message);}
   else if(frame.type==='exit'){ready=false;loading.hidden=true;reconnect.hidden=false;reconnect.textContent='Session ended';reconnect.disabled=true;}
   else if(frame.type==='error'){ready=false;loading.hidden=true;reconnect.hidden=false;}
  };
  socket.onclose=()=>{if(epoch!==generation)return;ready=false;loading.hidden=true;reconnect.hidden=false;};
  socket.onerror=()=>socket.close();
 }
 // Some prompts (Claude Code's sign-in code) do not show what is pasted:
 // confirm the paste here so it does not look ignored.
 const pasted=document.querySelector('#pasted');let pastedTimer=0;
 function showPasteError(message){if(pasted){pasted.textContent=message==='Start a new Kimi session to enable image paste'?message:'Could not paste image, try copying it again';pasted.hidden=false;}}
 terminal.textarea?.addEventListener('paste',async event=>{
  const file=Array.from(event.clipboardData?.items||[]).find(item=>item.type==='image/png')?.getAsFile();
  if(!file||!ready)return;
  event.preventDefault();event.stopImmediatePropagation();
  if(!imagePaste||file.size<1||file.size>20*1024*1024||pending.some(item=>item.type==='image')){showPasteError();return;}
  const epoch=generation,item={type:'image',data:null,total:file.size};pending.push(item);
  try{const data=new Uint8Array(await file.arrayBuffer());if(epoch!==generation||!ready)return;if(data.length!==file.size)throw Error('size');item.data=data;flush();}
  catch{if(epoch!==generation)return;pending=pending.filter(entry=>entry!==item);showPasteError();flush();}
 },true);
 terminal.textarea?.addEventListener('paste',event=>{const count=(event.clipboardData?.getData('text')||'').length;if(!count||!ready||!pasted)return;pasted.textContent='Pasted '+count+' characters. Press Return to send.';pasted.hidden=false;clearTimeout(pastedTimer);pastedTimer=setTimeout(()=>{pasted.hidden=true;},4000);});
 reconnect.onclick=()=>connect();window.addEventListener('pagehide',()=>socket?.close());
 connect();
})();
