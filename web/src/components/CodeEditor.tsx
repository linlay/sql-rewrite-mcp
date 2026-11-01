import CodeMirror, { EditorView } from '@uiw/react-codemirror';
import { javascript } from '@codemirror/lang-javascript';
import { sql } from '@codemirror/lang-sql';
import { markdown } from '@codemirror/lang-markdown';
import { html } from '@codemirror/lang-html';
import { usePreferences } from '../ui/preferences';
export function CodeEditor({value='',onChange,language='javascript',height='360px',minHeight,readOnly=false,wrap=false}:{value?:string;onChange?:(value:string)=>void;language?:'javascript'|'sql'|'markdown'|'json'|'html';height?:string;minHeight?:string;readOnly?:boolean;wrap?:boolean}){
 const {dark}=usePreferences();
 return <div className="code-editor"><CodeMirror aria-label={`${language} 代码编辑器`} value={value} height={height} minHeight={minHeight} theme={dark?'dark':'light'} extensions={[language==='html'?html():language==='sql'?sql():language==='markdown'?markdown():javascript(),...(wrap?[EditorView.lineWrapping]:[])]} onChange={onChange} readOnly={readOnly} basicSetup={{lineNumbers:true,foldGutter:true,highlightActiveLine:true,autocompletion:true}}/></div>;
}
