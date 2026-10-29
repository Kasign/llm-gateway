package main

import (
 "fmt"
 "os"
 "reflect"
 "regexp"
 "sort"
 "strings"

 cfg "github.com/whl/llm-gateway/internal/config"
 "gopkg.in/yaml.v3"
)

var (
 envRef=regexp.MustCompile("\\$\\{([A-Za-z_][A-Za-z0-9_]*)\\}")
 assignment=regexp.MustCompile("^([A-Za-z_][A-Za-z0-9_]*)=")
 requiredComposeVar=regexp.MustCompile("\\$\\{([A-Za-z_][A-Za-z0-9_]*)\\}")
 composeKey=regexp.MustCompile("^([A-Z][A-Z0-9_]*):")
)
type tree struct { root *yaml.Node; paths map[string]bool; values map[string]string }
func main(){if e:=run();e!=nil{fmt.Fprintln(os.Stderr,"配置契约检查失败：",e);os.Exit(1)}}
func run()error{
 actual,e:=readTree("config.yaml");if e!=nil{return e};example,e:=readTree("config.example.yaml");if e!=nil{return e}
 typ:=reflect.TypeOf(cfg.Config{});if e=knownFields(actual.root,typ,"");e!=nil{return fmt.Errorf("config.yaml: %w",e)};if e=knownFields(example.root,typ,"");e!=nil{return fmt.Errorf("config.example.yaml: %w",e)}
 if e=compareTrees(actual,example);e!=nil{return e}
 envData,e:=os.ReadFile(".env.example");if e!=nil{return e};composeData,e:=os.ReadFile("docker-compose.yml");if e!=nil{return e}
 envs:=parseAssignments(string(envData));passed:=composeEnvironment(string(composeData))
 for _,t:=range []*tree{actual,example}{refs:=map[string]bool{};collectRefs(t.root,refs);for n:=range refs{if !envs[n]{return fmt.Errorf("YAML 引用的变量 %s 未写入 .env.example",n)};if !passed[n]{return fmt.Errorf("YAML 变量 %s 未由 Compose 显式注入容器",n)}}}
 for _,line:=range strings.Split(string(composeData),"\n"){line=strings.TrimSpace(line);if line==""||strings.HasPrefix(line,"#"){continue};for _,m:=range requiredComposeVar.FindAllStringSubmatch(line,-1){if len(m)==2&&!envs[m[1]]{return fmt.Errorf("Compose 必需变量 %s 未写入 .env.example",m[1])}}}
 if e=checkNoProviderPresets(actual,example,string(envData),string(composeData));e!=nil{return e}
 if e=checkProviderDocumentation();e!=nil{return e}
 fmt.Printf("配置契约检查通过：两份 YAML %d 个路径；远程 Provider 仅由 Web 管理台维护。\n",len(actual.paths));return nil
}
func readTree(path string)(*tree,error){data,e:=os.ReadFile(path);if e!=nil{return nil,e};var root yaml.Node;if e=yaml.Unmarshal(data,&root);e!=nil{return nil,fmt.Errorf("解析 %s: %w",path,e)};t:=&tree{root:&root,paths:map[string]bool{},values:map[string]string{}};shape(&root,"",t.paths,t.values);return t,nil}
func deref(t reflect.Type)reflect.Type{for t!=nil&&t.Kind()==reflect.Pointer{t=t.Elem()};return t}
func fields(t reflect.Type)map[string]reflect.Type{out:=map[string]reflect.Type{};t=deref(t);if t==nil||t.Kind()!=reflect.Struct{return out};for i:=0;i<t.NumField();i++{f:=t.Field(i);n:=strings.Split(f.Tag.Get("yaml"),",")[0];if n!=""&&n!="-"{out[n]=f.Type}};return out}
func join(p,k string)string{if p==""{return k};return p+"."+k}
func knownFields(n *yaml.Node,t reflect.Type,p string)error{
 t=deref(t);if n.Kind==yaml.DocumentNode{if len(n.Content)!=1{return fmt.Errorf("YAML 根节点无效")};return knownFields(n.Content[0],t,p)}
 if n.Kind==yaml.AliasNode{if n.Alias==nil{return fmt.Errorf("%s alias 无效",p)};return knownFields(n.Alias,t,p)}
 switch n.Kind{
 case yaml.MappingNode:
  if t.Kind()==reflect.Map{for i:=0;i<len(n.Content);i+=2{if e:=knownFields(n.Content[i+1],t.Elem(),join(p,n.Content[i].Value));e!=nil{return e}};return nil}
  if t.Kind()!=reflect.Struct{return fmt.Errorf("%s 应是 %s 而不是 mapping",p,t.Kind())};fs:=fields(t)
  for i:=0;i<len(n.Content);i+=2{k:=n.Content[i].Value;ft,ok:=fs[k];if !ok{return fmt.Errorf("%s: Go 配置结构中不存在字段",join(p,k))};if e:=knownFields(n.Content[i+1],ft,join(p,k));e!=nil{return e}};return nil
 case yaml.SequenceNode:
  if t.Kind()!=reflect.Slice&&t.Kind()!=reflect.Array{return fmt.Errorf("%s 应是 %s 而不是 sequence",p,t.Kind())};for i,c:=range n.Content{if e:=knownFields(c,t.Elem(),fmt.Sprintf("%s[%d]",p,i));e!=nil{return e}};return nil
 case yaml.ScalarNode:
  if t.Kind()==reflect.Struct||t.Kind()==reflect.Map||t.Kind()==reflect.Slice||t.Kind()==reflect.Array{return fmt.Errorf("%s 应是 %s 而不是 scalar",p,t.Kind())};return nil
 default:return fmt.Errorf("%s YAML 节点类型不支持",p)
 }
}
func shape(n *yaml.Node,p string,paths map[string]bool,values map[string]string){if n.Kind==yaml.DocumentNode{for _,c:=range n.Content{shape(c,p,paths,values)};return};switch n.Kind{case yaml.MappingNode:for i:=0;i<len(n.Content);i+=2{k:=n.Content[i].Value;path:=join(p,k);paths[path]=true;shape(n.Content[i+1],path,paths,values)};case yaml.SequenceNode:if len(n.Content)==0{values[p]="[]";return};for _,c:=range n.Content{shape(c,p+"[]",paths,values)};case yaml.ScalarNode:values[p]=n.Value}}
func compareTrees(a,b *tree)error{onlyA,onlyB:=[]string{},[]string{};for p:=range b.paths{if !a.paths[p]{onlyA=append(onlyA,p)}};for p:=range a.paths{if !b.paths[p]{onlyB=append(onlyB,p)}};sort.Strings(onlyA);sort.Strings(onlyB);if len(onlyA)+len(onlyB)>0{return fmt.Errorf("config.yaml 与 config.example.yaml 字段路径不一致；实际缺少 %v，模板缺少 %v",onlyA,onlyB)}
 diff:=[]string{};all:=map[string]bool{};for p:=range a.values{all[p]=true};for p:=range b.values{all[p]=true};for p:=range all{if a.values[p]!=b.values[p]{diff=append(diff,p)}};sort.Strings(diff);if len(diff)>0{return fmt.Errorf("config.yaml 与 config.example.yaml 存在未登记的值差异：%v；部署差异应通过环境变量注入，避免维护两套静态配置",diff)};return nil}
func parseAssignments(s string)map[string]bool{out:=map[string]bool{};for _,l:=range strings.Split(s,"\n"){l=strings.TrimSpace(l);if l==""||strings.HasPrefix(l,"#"){continue};m:=assignment.FindStringSubmatch(l);if len(m)==2{out[m[1]]=true}};return out}
func composeEnvironment(s string)map[string]bool{out:=map[string]bool{};in,base:=false,-1;for _,line:=range strings.Split(s,"\n"){tr:=strings.TrimSpace(line);if tr==""||strings.HasPrefix(tr,"#"){continue};indent:=len(line)-len(strings.TrimLeft(line," \t"));if tr=="environment:"{in=true;base=indent;continue};if in&&indent<=base{in=false};if !in||indent<=base{continue};m:=composeKey.FindStringSubmatch(tr);if len(m)==2{out[m[1]]=true}};return out}
func collectRefs(n *yaml.Node,out map[string]bool){if n.Kind==yaml.DocumentNode||n.Kind==yaml.SequenceNode{for _,c:=range n.Content{collectRefs(c,out)};return};if n.Kind==yaml.MappingNode{for i:=0;i<len(n.Content);i+=2{collectRefs(n.Content[i+1],out)};return};if n.Kind!=yaml.ScalarNode{return};for _,m:=range envRef.FindAllStringSubmatch(n.Value,-1){if len(m)==2{out[m[1]]=true}}}
func checkNoProviderPresets(actual, example *tree, env, compose string) error {
	if actual.paths["providers"] || example.paths["providers"] {
		return fmt.Errorf("远程 Provider 不允许出现在 YAML 预置配置中；请统一通过 Web 管理台维护")
	}
	for _, item := range []struct{name, content string}{
		{name: ".env.example", content: env},
		{name: "docker-compose.yml", content: compose},
	} {
		if strings.Contains(item.content, "DEEPSEEK_API_KEY") || strings.Contains(item.content, "OPENAI_API_KEY") ||
			strings.Contains(item.content, "LLM_GATEWAY_PROVIDER_DEEPSEEK_") ||
			strings.Contains(item.content, "LLM_GATEWAY_PROVIDER_OPENAI_") {
			return fmt.Errorf("%s 仍包含 DeepSeek/OpenAI Provider 预置字段；远程连接必须通过 Web 管理台创建", item.name)
		}
	}
	if strings.Contains(compose, "providers.env") || strings.Contains(compose, "env_file:") {
		return fmt.Errorf("docker-compose.yml 不应通过 env_file 预置远程 Provider；API Key 仅从 Web 管理台加密保存")
	}
	return nil
}


// checkProviderDocumentation 防止旧版远程 Provider 预置配置说明重新进入文档。
func checkProviderDocumentation() error {
	paths := []string{"配置说明.md", "docs/configuration-matrix.md", "docs/api-config-db-contracts.md"}
	obsolete := []string{"providers.openai_compatible", "providers.env", "LLM_GATEWAY_PROVIDER_<ID>_*"}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("读取 Provider 配置文档 %s: %w", path, err)
		}
		for _, marker := range obsolete {
			if strings.Contains(string(content), marker) {
				return fmt.Errorf("%s 仍包含旧的 Provider 预置配置标记 %q；远程 Provider 统一通过 Web 管理台维护", path, marker)
			}
		}
	}
	return nil
}
