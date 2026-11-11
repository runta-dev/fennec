(() => {
 const token=location.hash.slice(1);
 const terminal=new Terminal({cursorBlink:true,fontFamily:'Menlo, Monaco, monospace',fontSize:14,theme:{background:'#101114'},scrollback:10000});
 const fit=new FitAddon.FitAddon();terminal.loadAddon(fit);terminal.open(document.querySelector('#terminal'));fit.fit();
 window.installTerminalLinks?.(terminal,text=>{const url=new URL(text);if(url.protocol==='https:'&&!url.username&&!url.password)window.open(url.href,'_blank','noopener,noreferrer');});
 let fitting=false;function scheduleFit(){if(fitting)return;fitting=true;requestAnimationFrame(()=>{fitting=false;fit.fit();});}
 document.fonts.ready.then(scheduleFit);
 const loading=document.querySelector('#loading'),reconnect=document.querySelector('#reconnect');
 let socket,generation=0,credit=0,pending=new Uint8Array(0),ready=false;
 const bytes64=bytes=>btoa(String.fromCharCode(...bytes));
 function send(frame){if(socket?.readyState===WebSocket.OPEN)socket.send(JSON.stringify(frame));}
 function flush(){while(ready&&credit>0&&pending.length){const size=Math.min(credit,pending.length,16384);send({type:'input',data:bytes64(pending.subarray(0,size))});pending=pending.slice(size);credit-=size;}}
 function input(bytes){if(!ready||pending.length+bytes.length>65536)return;const merged=new Uint8Array(pending.length+bytes.length);merged.set(pending);merged.set(bytes,pending.length);pending=merged;flush();}
 terminal.onData(text=>input(new TextEncoder().encode(text)));
 terminal.onBinary(text=>input(Uint8Array.from(text,c=>c.charCodeAt(0))));
 terminal.onResize(({cols,rows})=>{if(ready)send({type:'resize',columns:cols,rows});});
 new ResizeObserver(scheduleFit).observe(document.querySelector('#terminal'));
 function connect(){
  const epoch=++generation;socket?.close();pending=new Uint8Array(0);credit=0;ready=false;loading.hidden=false;reconnect.hidden=true;
  socket=new WebSocket(`ws://${location.host}/socket`);
  socket.onopen=()=>{if(epoch!==generation)return;fit.fit();send({token,columns:terminal.cols,rows:terminal.rows});};
  socket.onmessage=event=>{
   if(epoch!==generation)return;
   const frame=JSON.parse(event.data);
   if(frame.type==='metadata'){
    if(frame.title)document.title=frame.title;
    if(/^\/icons\/[a-z0-9-]+\.svg$/.test(frame.icon)){let icon=document.querySelector('link[rel="icon"]');if(!icon){icon=document.createElement('link');icon.rel='icon';document.head.append(icon);}icon.href=frame.icon;}
   }
   else if(frame.type==='receipt'){ready=true;credit=65536;terminal.reset();terminal.focus();scheduleFit();flush();}
   else if(frame.type==='data'){loading.hidden=true;const data=Uint8Array.from(atob(frame.data),c=>c.charCodeAt(0));terminal.write(data,()=>{if(epoch===generation){send({type:'credit',bytes:data.length});scheduleFit();}});}
   else if(frame.type==='input_credit'){credit+=frame.bytes;flush();}
   else if(frame.type==='exit'){ready=false;loading.hidden=true;reconnect.hidden=false;reconnect.textContent='Session ended';reconnect.disabled=true;}
   else if(frame.type==='error'){ready=false;loading.hidden=true;reconnect.hidden=false;}
  };
  socket.onclose=()=>{if(epoch!==generation)return;ready=false;loading.hidden=true;reconnect.hidden=false;};
  socket.onerror=()=>socket.close();
 }
 reconnect.onclick=()=>connect();window.addEventListener('pagehide',()=>socket?.close());
 connect();
})();
