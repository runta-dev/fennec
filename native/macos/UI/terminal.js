// The native bridge owns transport; Herdr owns the remote PTY and its lifetime.
(() => {
 let terminal, fit, observer, generation, connected = false;
 const send = value => window.webkit.messageHandlers.fennec.postMessage(JSON.stringify(value));
 window.closeSessionTerminal = () => {
  if (generation) send({type:'terminal-close',generation});
  generation=null; connected=false;
  observer?.disconnect(); observer=null;
  terminal?.dispose();terminal=null;fit=null;
 };
 window.addEventListener('pagehide', () => window.closeSessionTerminal());
 window.openSessionTerminal = (element, identity) => {
  window.closeSessionTerminal();
  generation=crypto.randomUUID();
  const current=generation; connected=true;
  try {
  terminal=new Terminal({fontSize:12,cursorBlink:true,scrollback:3000,allowProposedApi:false});
  fit=new FitAddon.FitAddon();terminal.loadAddon(fit);terminal.open(element);fit.fit();
  terminal.registerLinkProvider?.({provideLinks(row,callback){
   const buffer=terminal.buffer.active;let first=row-1;
   while(first>0 && buffer.getLine(first)?.isWrapped && row-first<32)first--;
   let text='',positions=[];
   for(let line=first;line<first+32;line++){
    const value=buffer.getLine(line);if(!value || (line>first&&!value.isWrapped))break;
    for(let x=0;x<terminal.cols;x++){
     const cell=value.getCell(x);if(!cell||cell.getWidth()===0)continue;
     const chars=cell.getChars()||' ';text+=chars;
     for(let i=0;i<chars.length;i++)positions.push({x:x+1,y:line+1});
    }
   }
   const links=[];
   for(const match of text.matchAll(/https:\/\/[^\s<>"\x00-\x1f]+/g)){
    const url=match[0].replace(/[.,;]+$/,'');const start=positions[match.index],end=positions[match.index+url.length-1];
    if(start&&end&&row>=start.y&&row<=end.y)links.push({range:{start,end},text:url,activate(){if(generation===current)send({type:'terminal-link',generation:current,url});}});
   }
   callback(links);
  }});
  terminal.onData(data=>{if(generation===current && connected)send({type:'terminal-input',generation:current,data});});
  terminal.onResize(size=>{if(generation===current && connected)send({type:'terminal-resize',generation:current,...size});});
  send({type:'terminal-open',generation:current,...identity,cols:terminal.cols,rows:terminal.rows});
  observer=new ResizeObserver(()=>{if(generation===current)fit.fit();});observer.observe(element);
  terminal.focus();
  return true;
  } catch (error) {
   send({type:"terminal-error",generation:current,stage:"renderer-init",kind:error?.name || "Error"});
   window.closeSessionTerminal();
   const note=document.querySelector('#session-detail-status');
   if(note)note.textContent="[[i18n:ui.could_not_open_terminal_try_again]]";
   return false;
  }
 };
 window.endSessionTerminal = (id, failed = false) => {
  if(id!==generation || !terminal)return;
  connected=false;
  terminal.options.disableStdin=true;
  const note=document.querySelector('#session-detail-status');
  if(note)note.textContent=failed?"[[i18n:ui.could_not_open_terminal_try_again]]":"[[i18n:ui.terminal_connection_closed]]";
  window.agentLoginTerminalEnded?.();
 };
 window.writeSessionTerminal = (id,base64) => {
  if(id!==generation||!terminal)return;
  const bytes=Uint8Array.from(atob(base64),c=>c.charCodeAt(0));
  terminal.write(bytes,()=>{if(id===generation)send({type:'terminal-credit',generation:id,bytes:bytes.length});});
 };
})();
