package sourcetools

import (
	"bytes"
	"encoding/json"
	"html/template"
)

func renderHTML(report diffReport) ([]byte, error) {
	files := []fileResult{}
	for _, f := range report.Files {
		if f.Status != "unchanged" {
			files = append(files, f)
		}
	}
	payload, e := json.Marshal(files)
	if e != nil {
		return nil, e
	}
	data := struct {
		Report  diffReport
		Payload template.JS
	}{report, template.JS(payload)}
	t, e := template.New("report").Parse(reportHTML)
	if e != nil {
		return nil, e
	}
	var out bytes.Buffer
	e = t.Execute(&out, data)
	return out.Bytes(), e
}

const reportHTML = `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>客户端源码 Clean Diff</title>
<style>body{font:15px system-ui;margin:0;background:#10151d;color:#e0e6ed}header{padding:20px 24px;position:sticky;top:0;background:#17202df5;z-index:1}h1{font-size:24px;margin:0 0 12px}input,select,button{padding:9px;background:#10151d;color:#e0e6ed;border:1px solid #506079;border-radius:5px}button{cursor:pointer}button:disabled{opacity:.4}input{width:min(45vw,600px)}main{padding:10px 24px}details.file{border:1px solid #344256;border-radius:7px;margin:12px 0}summary{cursor:pointer;padding:12px;overflow-wrap:anywhere}details.member{margin:8px 16px;border-left:2px solid #526581}pre{font:13px/1.6 Consolas,monospace;overflow:auto;margin:0;padding:12px;background:#0c1118}.add{display:block;background:#163d2b;color:#b9f3c7}.del{display:block;background:#48212a;color:#ffc3c9}.hunk{display:block;color:#8bc4ff}.line{display:block}.muted{color:#9eafc2}.raw{margin:10px 14px}#count{margin-left:12px}nav{padding-top:12px}.reason{margin:10px 16px;color:#e8c079}</style>
<header><h1>客户端源码 Clean Diff</h1><p>{{range $k,$v := .Report.Counts}}{{$k}}: {{$v}} / {{end}}</p><p class="muted">{{.Report.OldRoot}} → {{.Report.NewRoot}}</p><p class="muted">按文件 → 成员展开。行号为归一化成员行号；局部变量显示为 localN，可展开保留原名称的 diff。复核区保留 async / 状态机与无法确定的结构变化。</p>
<input id="q" placeholder="搜索路径、成员、差异文本"><select id="filter"><option value="changes">主差异</option><option value="review">需复核结构</option><option value="noise">疑似写法噪声</option><option value="all">全部</option></select><span id="count"></span><nav><button id="prev">上一页</button> <span id="page"></span> <button id="next">下一页</button></nav></header><main id="files"></main>{{range .Report.Warnings}}<p class="reason">{{.}}</p>{{end}}
<script type="application/json" id="data">{{.Payload}}</script><script>
const data=JSON.parse(document.querySelector('#data').textContent);document.querySelector('#data').remove();
const q=document.querySelector('#q'),filter=document.querySelector('#filter'),list=document.querySelector('#files');
const labels={modified:'变化',added:'新增',removed:'删除',noise:'疑似噪声',review:'需复核',error:'读取失败'};
let matches=[],page=0,timer;const size=100;
function node(tag,text,cls){const el=document.createElement(tag);if(text!==undefined)el.textContent=text;if(cls)el.className=cls;return el}
function showDiff(diff){const pre=node('pre');for(const line of diff.split('\n')){const cls=line.startsWith('+')?'add':line.startsWith('-')?'del':line.startsWith('@@')?'hunk':'line';pre.append(node('span',line||' ',cls))}return pre}
function member(entry,title){const el=node('details',undefined,'member');el.append(node('summary',title+' · '+entry.name));el.addEventListener('toggle',()=>{if(!el.open||el.dataset.loaded)return;el.dataset.loaded='1';if(entry.reason)el.append(node('p',entry.reason,'reason'));el.append(showDiff(entry.diff));if(entry.source_diff&&entry.source_diff!==entry.diff){const raw=node('details',undefined,'raw');raw.append(node('summary','查看保留局部名 / 括号 / 泛型的差异'));raw.addEventListener('toggle',()=>{if(raw.open&&!raw.dataset.loaded){raw.dataset.loaded='1';raw.append(showDiff(entry.source_diff))}});el.append(raw)}});return el}
function draw(){list.replaceChildren();const mode=filter.value;for(const item of matches.slice(page*size,(page+1)*size)){const el=node('details',undefined,'file');el.append(node('summary',labels[item.status]+' · '+item.path+'   ('+item.members.length+' 变化 / '+(item.review||[]).length+' 复核 / '+item.noise.length+' 噪声)'));el.addEventListener('toggle',()=>{if(!el.open||el.dataset.loaded)return;el.dataset.loaded='1';el.append(node('p','旧: '+(item.old_path||'∅')+' → 新: '+(item.new_path||'∅'),'muted raw'));if(item.error)el.append(node('pre',item.error));for(const [key,title] of [['members','变化'],['review','需复核'],['noise','疑似噪声']]){if(mode==='review'&&key!=='review'||mode==='noise'&&key!=='noise')continue;for(const entry of item[key]||[])el.append(member(entry,title))}});list.append(el)}document.querySelector('#count').textContent=matches.length+' 文件';document.querySelector('#page').textContent=(matches.length?page+1:0)+' / '+Math.ceil(matches.length/size);document.querySelector('#prev').disabled=page===0;document.querySelector('#next').disabled=(page+1)*size>=matches.length}
function search(){page=0;const s=q.value.toLowerCase(),mode=filter.value;matches=data.filter(item=>{if(mode==='changes'&&!['modified','added','removed','error'].includes(item.status))return false;if(mode==='noise'&&!item.noise.length)return false;if(mode==='review'&&!(item.review||[]).length)return false;if(!s||item.path.toLowerCase().includes(s))return true;return ['members','review','noise'].some(key=>(item[key]||[]).some(m=>(m.name+' '+m.diff+' '+(m.source_diff||'')).toLowerCase().includes(s)))});draw()}
q.addEventListener('input',()=>{clearTimeout(timer);timer=setTimeout(search,150)});filter.addEventListener('change',search);document.querySelector('#prev').onclick=()=>{page--;draw()};document.querySelector('#next').onclick=()=>{page++;draw()};search();
</script></html>`
