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
  window.installTerminalLinks?.(terminal,url=>{if(generation===current)send({type:'terminal-link',generation:current,url});});
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
   if(note)note.textContent='无法打开终端，请重试';
   return false;
  }
 };
 window.endSessionTerminal = (id, failed = false) => {
  if(id!==generation || !terminal)return;
  connected=false;
  terminal.options.disableStdin=true;
  const note=document.querySelector('#session-detail-status');
  if(note)note.textContent=failed?'无法打开终端，请重试':'终端连接已结束';
  window.agentLoginTerminalEnded?.();
 };
 window.writeSessionTerminal = (id,base64) => {
  if(id!==generation||!terminal)return;
  const bytes=Uint8Array.from(atob(base64),c=>c.charCodeAt(0));
  terminal.write(bytes,()=>{if(id===generation)send({type:'terminal-credit',generation:id,bytes:bytes.length});});
 };
})();
