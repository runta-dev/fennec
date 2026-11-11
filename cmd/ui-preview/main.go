// ui-preview serves the actual bundled UI with isolated display fixtures.
// It never connects to the daemon or modifies account/cloud preferences.
package main

import (
	"context"
	"encoding/json"
	"fennec/internal/browserterminal"
	"fennec/internal/terminalstream"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	root := flag.String("ui", "native/macos/UI", "UI source directory")
	address := flag.String("listen", "127.0.0.1:0", "loopback listener")
	flag.Parse()
	absolute, err := filepath.Abs(*root)
	if err != nil {
		panic(err)
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		panic(err)
	}
	if tcp, ok := listener.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		listener.Close()
		panic("preview must bind loopback")
	}
	mux := http.NewServeMux()
	terminals := &browserterminal.Server{}
	defer terminals.Close()
	mux.HandleFunc("/preview-login", func(w http.ResponseWriter, r *http.Request) {
		agent := r.URL.Query().Get("agent")
		if !map[string]bool{"codex": true, "claude": true, "pi": true}[agent] {
			http.Error(w, "Unknown preview agent", 400)
			return
		}
		url, err := terminals.URL(context.Background(), "preview-login", func(ctx context.Context) (net.Conn, string, error) {
			client, fixture := net.Pipe()
			go func() {
				defer fixture.Close()
				decoder := json.NewDecoder(fixture)
				encoder := json.NewEncoder(fixture)
				var frame terminalstream.Frame
				if decoder.Decode(&frame) != nil {
					return
				}
				if encoder.Encode(terminalstream.Frame{Type: "receipt", ID: "preview"}) != nil {
					return
				}
				message := "Fennec browser sign-in preview\r\n\r\nThis is a layout preview, not a live authentication session\r\nNo account or cloud settings are changed\r\n"
				if encoder.Encode(terminalstream.Frame{Type: "data", ID: "preview", Data: []byte(message)}) != nil {
					return
				}
				for decoder.Decode(&frame) == nil {
					if frame.Type == "input" {
						if encoder.Encode(terminalstream.Frame{Type: "input_credit", ID: "preview", Bytes: len(frame.Data)}) != nil {
							return
						}
					}
				}
			}()
			return client, "preview", nil
		}, func() browserterminal.Metadata {
			return browserterminal.Metadata{Title: "Sign in preview — " + agent, Icon: "/icons/fennec.svg"}
		})
		if err != nil {
			http.Error(w, "Preview unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"url": url})
	})
	files := http.FileServer(http.Dir(absolute))
	mux.HandleFunc("/preview-bridge.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		fmt.Fprint(w, bridge)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			files.ServeHTTP(w, r)
			return
		}
		raw, err := os.ReadFile(filepath.Join(absolute, "index.html"))
		if err != nil {
			http.Error(w, "UI source unavailable", 500)
			return
		}
		html := strings.Replace(string(raw), "<head>", `<head><script src="/preview-bridge.js"></script><style>html{background:#dedfe4;min-height:100vh;padding:20px;display:flex;justify-content:center}body{width:440px;max-width:calc(100vw - 40px);height:600px;border:1px solid #bdbec5;border-radius:18px;overflow:hidden;box-shadow:0 8px 30px #0002}</style>`, 1)
		html = strings.Replace(html, "script-src 'self';", "script-src 'self'; connect-src 'self';", 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, html)
	})
	fmt.Printf("Fennec development preview: http://%s\n", listener.Addr())
	server := http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := server.Serve(listener); err != nil {
		panic(err)
	}
}

const bridge = `
const previewAgents=[
 {id:'claude',name:'Claude Code',icon:'claudecode-color.svg',installed:true,cloudSupported:true},
 {id:'codex',name:'Codex',icon:'codex-color.svg',installed:true,cloudSupported:true},
 {id:'pi',name:'Pi',icon:'pi.svg',installed:true,cloudSupported:true},
 {id:'kimi',name:'Kimi Code',icon:'kimi.svg',installed:false,cloudSupported:false},
 {id:'claude-desktop',name:'Claude Desktop',icon:'claude-color.svg',installed:false,cloudSupported:false},
 {id:'opencode',name:'OpenCode',icon:'opencode.svg',installed:false,cloudSupported:false}];
let previewPrefs={magpie:false,theme:'light',language:'en-US',initialized:true};
const previewCloud=new Set(['claude','codex','pi']);
const previewRoutes=()=>previewAgents.slice(0,3).map(a=>({agent:a.id,status:a.id==='codex'?'configured':'credentials_required',model:'codex/gpt-preview'}));
window.webkit={messageHandlers:{fennec:{postMessage(message){
 if(typeof message!=='string')return;
 const colon=message.indexOf(':');const action=colon<0?message:message.slice(0,colon);let p={};try{p=JSON.parse(message.slice(colon+1))}catch{}
 if(action==='get-agent-account')queueMicrotask(()=>window.renderNativeAccount({agent:p.agent,location:p.location,status:p.agent==='codex'?'signed_in':'signed_out',accounts:p.agent==='codex'?[{provider:'openai',email:'developer@example.com',subscription:'ChatGPT Pro',auth_type:'oauth'}]:[]}));
 if(action==='get-model' && p.agent!=='codex')queueMicrotask(()=>window.renderNativeModels({agent:p.agent,model:'',models:[{id:'',name:'Default'}]}));
 if(action==='get-model' && p.agent==='codex')queueMicrotask(()=>window.renderNativeModels({agent:p.agent,model:'gpt-preview',effort:'low',models:[{id:'',name:'Default'},{id:'gpt-preview',name:'GPT Preview',efforts:['low','medium','high','xhigh','max']}]}));
 if(action==='settings'){previewPrefs={...previewPrefs,...p};queueMicrotask(()=>window.renderPreferences(previewPrefs));}
 if(action==='toggle-takeover'){const agent=message.slice(colon+1);previewCloud.has(agent)?previewCloud.delete(agent):previewCloud.add(agent);queueMicrotask(()=>window.renderTakeover({agents:[...previewCloud],activeAgents:[...previewCloud],selectionKnown:true}));}
 if(action==='select-model')setTimeout(()=>{window.renderNativeModels({agent:p.agent,model:p.model,effort:p.effort||'low',models:[{id:'gpt-preview',name:'GPT Preview',efforts:['low','medium','high','xhigh','max']}]});window.renderModelSaving(false)},350);
 if(action==='get-local-model')queueMicrotask(()=>window.renderLocalModel({agent:p.agent,model:'codex/gpt-preview',models:['codex/gpt-preview']}));
 if(action==='start-agent-login'){const tab=window.open('about:blank','_blank');fetch('/preview-login?agent='+encodeURIComponent(p.agent)).then(r=>r.json()).then(value=>{if(tab)tab.location=value.url;window.renderAgentLoginStarted({session_id:'preview'},p.agent)}).catch(()=>{tab?.close();window.renderAgentLoginStarted(null,p.agent)});}
}}}};
window.addEventListener('DOMContentLoaded',()=>{
 document.title='Fennec development preview';
 window.renderPreferences(previewPrefs);window.renderAuthorized();
 document.querySelector('.header-login').textContent='Development preview';
 window.renderAgents(previewAgents);window.renderRouteResults(previewRoutes());
 window.renderTakeover({agents:[...previewCloud],activeAgents:[...previewCloud],selectionKnown:true});
 window.renderModelCatalog(['codex/gpt-preview']);
 window.renderPreparation({stage:'agents_installed',status:'ready'});
});
`
