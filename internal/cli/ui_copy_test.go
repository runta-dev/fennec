package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUICopyPunctuationPreservesTechnicalDots(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "native/macos/UI/locale.js"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start, end := strings.Index(source, "function uiCopy("), strings.Index(source, "function translate(")
	if start < 0 || end <= start {
		t.Fatal("UI copy formatter unavailable")
	}
	cases := []map[string]string{{"input": "为云端智能体自由选择模型。", "language": "zh-CN"}, {"input": "Could not connect. Try again.", "language": "en-US"}, {"input": "Fennec 0.2.123 · config.toml · https://example.com/a.json", "language": "en-US"}, {"input": "Connecting...", "language": "en-US"}, {"input": "前半。後半。", "language": "ja"}}
	raw, _ := json.Marshal(cases)
	script := source[start:end] + `process.stdout.write(JSON.stringify(JSON.parse(process.argv[1]).map(x=>uiCopy(x.input,x.language))))`
	output, err := exec.Command("node", "-e", script, string(raw)).CombinedOutput()
	if err != nil {
		t.Fatalf("UI renderer check: %v %s", err, output)
	}
	var got []string
	if json.Unmarshal(output, &got) != nil {
		t.Fatal("invalid UI renderer result")
	}
	want := []string{"为云端智能体自由选择模型", "Could not connect · Try again", "Fennec 0.2.123 · config.toml · https://example.com/a.json", "Connecting…", "前半、後半"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected UI copy: %#v", got)
	}
}

func TestUITranslationDoesNotInterpretPrototypeNames(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "native/macos/UI/locale.js"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start, end := strings.Index(source, "function messageText("), strings.Index(source, "function localize(")
	if start < 0 || end <= start {
		t.Fatal("translation functions unavailable")
	}
	script := `const messages={'ui.test':{'zh-CN':'测试。','en-US':'Test.'}};const messageToken=/\[\[i18n:([a-zA-Z][a-zA-Z0-9_.]*)\]\]/g;let language='en-US';` + source[start:end] + `process.stdout.write(JSON.stringify(['constructor','__proto__','model.json','[[i18n:constructor]]','[[i18n:ui.test]]'].map(translate)))`
	raw, err := exec.Command("node", "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("translation check: %v %s", err, raw)
	}
	var values []string
	_ = json.Unmarshal(raw, &values)
	if !reflect.DeepEqual(values, []string{"constructor", "__proto__", "model.json", "[[i18n:constructor]]", "Test"}) {
		t.Fatal(values)
	}
}

func TestStableMessageIDsSwitchLocalesWithoutChangingUserData(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "native/macos/UI/locale.js"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := os.ReadFile(filepath.Join("..", "..", "native/macos/UI/translations.json"))
	if err != nil {
		t.Fatal(err)
	}
	script := `const vm=require('node:vm'),assert=require('node:assert/strict');
const messages=JSON.parse(process.argv[1]).messages;
class Element {
 constructor(text,attributes={},kind='ui'){this.attributes={...attributes};this.kind=kind;this.nodeType=1;this.node={nodeType:3,parentElement:this,nodeValue:text};this.value='literal [[i18n:ui.settings]] input';}
 get textContent(){return this.node.nodeValue;} set textContent(v){this.node.nodeValue=v;}
 getAttribute(k){return this.attributes[k]??null;} hasAttribute(k){return Object.hasOwn(this.attributes,k);} setAttribute(k,v){this.attributes[k]=v;}
 closest(s){if(this.kind==='terminal'&&s.includes('#session-terminal'))return this;
 if(this.kind==='identity'&&!this.hasAttribute('data-i18n')&&s.includes('.header-login.account-link'))return this;
 if(this.kind==='runtime'&&s.includes('#runtime-list'))return this;return null;}
}
const label=new Element('Settings',{'data-i18n':'ui.settings'}),clonedLabel=new Element('设置',{'data-i18n':'ui.settings'}),dynamic=new Element('[[i18n:ui.retry]]'),count=new Element('9');
const identity=new Element('Settings',{},'identity'),literalIdentity=new Element('[[i18n:ui.retry]]',{},'identity');
const terminal=new Element('[[i18n:ui.settings]]',{},'terminal');
const input=new Element('',{placeholder:'Account','data-i18n-placeholder':'ui.account'});
const title=new Element('查看仓库',{},'runtime'),untitled=new Element('Untitled session',{'data-i18n':'ui.untitled_session'},'runtime');
const status=new Element('[[i18n:ui.working]]',{},'runtime');
const elements=[label,clonedLabel,dynamic,count,identity,literalIdentity,terminal,input,title,untitled,status];
const select={value:'',querySelectorAll(){return []}};
const document={body:{},documentElement:{},querySelector(){return select},createTreeWalker(){let i=0;return{nextNode(){return elements[i++]?.node||null}}},querySelectorAll(s){return s.startsWith('[aria-label]')?[input,identity,literalIdentity]:[status,untitled]}};
const context=vm.createContext({window:{FennecMessages:messages},document,NodeFilter:{SHOW_TEXT:4},localStorage:{getItem(){return 'en-US'},setItem(){}},MutationObserver:class{observe(){}disconnect(){}}});
vm.runInContext(process.argv[2],context);
for(const [locale,expected] of [['zh-CN','设置'],['zh-TW','設定'],['ja','設定'],['ko','설정'],['en-GB','Settings'],['en-US','Settings']]){
 vm.runInContext('language='+JSON.stringify(locale)+';localize()',context);
 assert.equal(label.textContent,expected);assert.equal(clonedLabel.textContent,expected);assert.equal(count.textContent,'9');assert.equal(identity.textContent,'Settings');
 assert.equal(literalIdentity.textContent,'[[i18n:ui.retry]]');assert.equal(terminal.textContent,'[[i18n:ui.settings]]');assert.equal(title.textContent,'查看仓库');assert.equal(input.value,'literal [[i18n:ui.settings]] input');
}
vm.runInContext("language='zh-CN';localize()",context);assert.equal(dynamic.textContent,'重试');assert.equal(input.getAttribute('placeholder'),'账户');assert.equal(untitled.textContent,'未命名会话');assert.equal(status.textContent,'正在工作');
dynamic.textContent='[[i18n:ui.account]]';vm.runInContext('localize()',context);assert.equal(dynamic.textContent,'账户');
vm.runInContext("language='en-US';localize()",context);assert.equal(dynamic.textContent,'Account');
assert.equal(vm.runInContext("messageText('installation.install_here','ja')",context),'Install Here');
process.stdout.write('PASS locale switching, stable IDs, fallback, and user-content preservation');`
	output, err := exec.Command("node", "-e", script, string(catalog), string(source)).CombinedOutput()
	if err != nil {
		t.Fatalf("locale switching: %v\n%s", err, output)
	}
}
