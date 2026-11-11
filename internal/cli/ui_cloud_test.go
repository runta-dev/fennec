package cli

import (
	"os"
	"os/exec"
	"testing"
)

func TestCloudUIRequiresConfirmationAndPreservesUnknownOutcome(t *testing.T) {
	source, err := os.ReadFile("../../native/macos/UI/cloud-computer.js")
	if err != nil {
		t.Fatal(err)
	}
	script := `const vm=require('node:vm'),assert=require('node:assert/strict');
const element=()=>({disabled:false,hidden:false,open:false,value:'',textContent:'',attributes:{},events:{},classList:{toggle(){}},setAttribute(k,v){this.attributes[k]=v},removeAttribute(k){delete this.attributes[k]},append(){},focus(){},addEventListener(k,v){this.events[k]=v},showModal(){this.open=true},close(){this.open=false;this.events.close?.()}});
const account=element(),card=element(),dialog=element(),action=element();
const nodes=Object.fromEntries(['input','.cloud-delete-confirm','.cloud-delete-cancel','.cloud-delete-error','.cloud-delete-target'].map(k=>[k,element()]));
card.querySelector=()=>action;dialog.querySelector=k=>nodes[k];
const sent=[];const window={};
vm.runInNewContext(process.argv[1],{window,document:{querySelector:()=>account,createElement:tag=>tag==='dialog'?dialog:card,body:{append(){}}},translate:x=>x,send:x=>sent.push(x),clearTimeout(){},setTimeout(){return 1}});
window.renderCloudState({stage:'available',runtime_id:'original'});
action.onclick();assert.equal(sent.length,1);assert.ok(sent[0].startsWith('cloud-preview:'));assert.equal(action.disabled,true);
window.renderCloudPreview({id:'plan',target:{runtime:'other'}});assert.equal(dialog.open,false,'mismatched preview must not open');
action.onclick();window.renderCloudPreview({id:'plan',target:{runtime:'original'}});assert.equal(dialog.open,true);
assert.equal(nodes['.cloud-delete-confirm'].disabled,true);
nodes.input.value='other';nodes.input.oninput();nodes['.cloud-delete-confirm'].onclick();assert.equal(sent.length,2,'unmatched ID must not send DELETE');
nodes.input.value='original';nodes.input.oninput();assert.equal(nodes['.cloud-delete-confirm'].disabled,false);
nodes['.cloud-delete-confirm'].onclick();assert.equal(sent.length,3);assert.ok(sent[2].startsWith('cloud-delete:'));
let prevented=false;dialog.events.cancel({preventDefault(){prevented=true}});assert.equal(prevented,true);
window.renderCloudDeleteResult(false);assert.equal(dialog.open,true);assert.equal(nodes['.cloud-delete-confirm'].disabled,true,'failed confirmation needs a fresh preview');assert.notEqual(nodes['.cloud-delete-error'].textContent,'');
nodes['.cloud-delete-cancel'].onclick();assert.equal(dialog.open,false);
window.renderCloudState({stage:'unconfirmed',runtime_id:'original',request_id:'request',can_create:false});action.onclick();assert.ok(sent.at(-1).startsWith('cloud-preview:'),'unknown outcome cannot create');
window.renderCloudPreview(null);
window.renderCloudState({stage:'deleted',runtime_id:'original',request_id:'request',can_create:true});action.onclick();assert.ok(sent.at(-1).startsWith('cloud-create:'));
window.resetCloudComputer();assert.equal(card.hidden,true);assert.equal(dialog.open,false);
`
	output, err := exec.Command("node", "-e", script, string(source)).CombinedOutput()
	if err != nil {
		t.Fatalf("cloud UI state: %v\n%s", err, output)
	}
}
