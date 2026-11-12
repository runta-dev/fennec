// ui-preview serves the actual bundled UI with isolated display fixtures.
// It never connects to the daemon or modifies account/cloud preferences.
package main

import (
	"context"
	"encoding/json"
	"fennec/internal/browserterminal"
	"fennec/internal/localization"
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
	mux.HandleFunc("/locale-data.js", func(w http.ResponseWriter, r *http.Request) {
		raw, err := os.ReadFile(filepath.Join(absolute, "translations.json"))
		if err != nil {
			http.Error(w, "Catalog unavailable", 500)
			return
		}
		catalog, err := localization.Decode(raw)
		if err != nil {
			http.Error(w, "Catalog invalid", 500)
			return
		}
		data, _ := json.Marshal(catalog.Messages)
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, "window.FennecMessages = %s;", data)
	})
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
		html := strings.Replace(string(raw), "<head>", `<head><script src="/preview-bridge.js"></script><style>html{background:#dedfe4;min-height:100vh;padding:20px;display:flex;align-items:center;justify-content:center}body{width:440px;max-width:calc(100vw - 40px);height:390px;border:1px solid #bdbec5;border-radius:12px;overflow:hidden;box-shadow:0 8px 30px #0002}</style>`, 1)
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
let previewUpdate={state:'latest',current:'0.2.11121023',latest:'0.2.11121023'};
const previewCloud=new Set(['claude','codex','pi']);
const previewRoutes=()=>previewAgents.slice(0,3).map(a=>({agent:a.id,status:a.id==='codex'?'configured':'credentials_required',model:'codex/gpt-preview'}));
window.webkit={messageHandlers:{fennec:{postMessage(message){
 if(typeof message!=='string')return;
 const colon=message.indexOf(':');const action=colon<0?message:message.slice(0,colon);let p={};try{p=JSON.parse(message.slice(colon+1))}catch{}
 if(message==='load-billing'){const q=new URLSearchParams(location.search);const mode=q.get('billing');const rt=q.get('rt');queueMicrotask(()=>{window.renderBilling(mode==='reauth'?{reauth_required:true}:mode==='web_only'?{web_only:true}:mode==='offline'?{offline:true}:{currency:'USD',balance:5000,spend:0,cpu:'0.00 vCPU-h',memory:'0.00 GiB-h',disk:'0.00 GiB-h'});window.renderRuntimeMetrics(rt==='not_created'?{ready:false,status:'not_created'}:rt==='billing_restricted'?{ready:false,status:'billing_restricted'}:{ready:mode!=='offline',cpu_percent:8.4,vcpus:2,memory_used_mib:382,memory_current_mib:4096,memory_min_mib:4096,memory_max_mib:16384,net_tx_bytes:977531,net_rx_bytes:465975553,net_tx_bps:18432,net_rx_bps:65536});});}
 if(message==='update-check'){previewUpdate={...previewUpdate,state:'checking'};queueMicrotask(()=>window.renderUpdateState(previewUpdate));setTimeout(()=>{previewUpdate={state:'ready',current:'0.2.11121023',latest:'0.2.11121230',notes:'### Features\n- Magpie-style background auto updater\n### Improvements\n- Zero-interruption cloud session continuity'};window.renderUpdateState(previewUpdate);},300);}
 if(message==='update-download'){previewUpdate={...previewUpdate,state:'downloading',done:12500000,total:20000000,speed:4200000};queueMicrotask(()=>window.renderUpdateState(previewUpdate));setTimeout(()=>{previewUpdate={...previewUpdate,state:'ready'};window.renderUpdateState(previewUpdate);},400);}
 if(message==='update-install'){setTimeout(()=>{previewUpdate={state:'latest',current:previewUpdate.latest||'0.2.11121230'};window.renderUpdateState(previewUpdate);},400);}
 if(action==='copy-attach')queueMicrotask(()=>window.renderAttachCopyResult(p.session_id,true));
 if(action==='get-agent-account')queueMicrotask(()=>window.renderNativeAccount({agent:p.agent,location:p.location,status:p.agent==='codex'?'signed_in':'signed_out',accounts:p.agent==='codex'?[{provider:'openai',email:'developer@example.com',subscription:'ChatGPT Pro',auth_type:'oauth'}]:[]}));
 if(action==='get-model' && p.agent!=='codex')queueMicrotask(()=>window.renderNativeModels({agent:p.agent,model:'',models:[{id:'',name:'Default'}]}));
 if(action==='get-model' && p.agent==='codex')queueMicrotask(()=>window.renderNativeModels({agent:p.agent,model:'gpt-preview',effort:'low',models:[{id:'',name:'Default'},{id:'gpt-preview',name:'GPT Preview',efforts:['low','medium','high','xhigh','max']}]}));
 if(action==='settings'){if('auto_update' in p)previewPrefs.no_auto_update=!p.auto_update;if('update_skip' in p)previewPrefs.update_skip=p.update_skip;if('environment' in p){previewPrefs.environment=p.environment;previewPrefs.api_endpoint=p.environment==='integration'?'https://api.runta.xyz':p.environment==='production'?'https://api.runta.com':p.environment;previewPrefs.ssh_host=p.environment==='integration'?'ssh.runta.xyz':'ssh.runta.com';}if('ssh_nodelay' in p)previewPrefs.ssh_nodelay=p.ssh_nodelay;previewPrefs={...previewPrefs,...p};queueMicrotask(()=>window.renderPreferences(previewPrefs));}
 if(action==='toggle-takeover'){const agent=message.slice(colon+1);previewCloud.has(agent)?previewCloud.delete(agent):previewCloud.add(agent);queueMicrotask(()=>window.renderTakeover({agents:[...previewCloud],activeAgents:[...previewCloud],selectionKnown:true}));}
 if(action==='select-model')setTimeout(()=>{window.renderNativeModels({agent:p.agent,model:p.model,effort:p.effort||'low',models:[{id:'gpt-preview',name:'GPT Preview',efforts:['low','medium','high','xhigh','max']}]});window.renderModelSaving(false)},350);
 if(action==='get-local-model')queueMicrotask(()=>window.renderLocalModel({agent:p.agent,model:'codex/gpt-preview',models:['codex/gpt-preview']}));
 if(action==='start-agent-login'){const tab=window.open('about:blank','_blank');fetch('/preview-login?agent='+encodeURIComponent(p.agent)).then(r=>r.json()).then(value=>{if(tab)tab.location=value.url;window.renderAgentLoginStarted({session_id:'preview'},p.agent)}).catch(()=>{tab?.close();window.renderAgentLoginStarted(null,p.agent)});}
}}}};
window.addEventListener('DOMContentLoaded',()=>{
 document.title='Fennec development preview';
 const params=new URLSearchParams(location.search);
 if(params.get('lang'))previewPrefs.language=params.get('lang');
 if(params.get('theme'))previewPrefs.theme=params.get('theme');
 if(params.get('env'))previewPrefs.environment=params.get('env');
 window.renderPreferences(previewPrefs);window.renderAuthorized();
 window.renderAppVersion('Fennec 0.2.11121023');
 const updateMode=params.get('update');
 if(updateMode==='ready')previewUpdate={state:'ready',current:'0.2.11121023',latest:'0.2.11121230',notes:'### Features\n- Magpie-style quiet update pill and automatic background staging\n- CLI control via \x60fen update\x60 and \x60fen settings auto-update\x60\n### Improvements\n- Cloud agent sessions stay alive across restarts'};
 else if(updateMode==='downloading')previewUpdate={state:'downloading',current:'0.2.11121023',latest:'0.2.11121230',done:12400000,total:20000000,speed:4200000};
 window.renderUpdateState(previewUpdate);
 document.querySelector('.header-login').textContent='Development preview';
 window.renderAgents(previewAgents);window.renderRouteResults(previewRoutes());
 window.renderTakeover({agents:[...previewCloud],activeAgents:[...previewCloud],selectionKnown:true});
 window.renderModelCatalog(['codex/gpt-preview']);
 if(params.get('tab')==='preferences')document.querySelector('#settings')?.click();
 else if(params.get('tab'))document.querySelector('[data-tab="'+params.get('tab')+'"]')?.click();
 const scenario=params.get('scenario');
 if(scenario==='offline'){window.renderPreparation({stage:'offline',status:'network_offline'});window.renderSessionRefreshError();document.querySelector('[data-tab="sessions"]')?.click();}
 else if(scenario==='failed'){window.renderPreparation({stage:'failed',status:'runtime_preparation_failed'});window.renderSessionRefreshError();document.querySelector('[data-tab="sessions"]')?.click();}
 else window.renderPreparation({stage:'agents_installed',status:'ready'});
 const ob=params.get('onboarding');
 if(ob==='signin'||ob==='setup')window.showOnboardingPreview?.(ob);
});
`
