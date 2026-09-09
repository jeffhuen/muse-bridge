package protocols

import (
 "encoding/json"
 "testing"

 "github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func phase2ReviewConvert(t *testing.T, api, history string, cache *upstream.SignatureCache) (*upstream.PredictionRequest,error) {
 t.Helper()
 if api=="responses" {return ConvertResponsesToPrediction(&ResponsesRequest{Model:"gemini-3.8-flash-high",Input:json.RawMessage(history)},cache)}
 var messages []ChatMessage
 if err:=json.Unmarshal([]byte(history),&messages);err!=nil {t.Fatal(err)}
 return ConvertChatToPrediction(&ChatRequest{Model:"gemini-3.8-flash-high",Messages:messages},cache)
}

var phase2ReviewSingleHistory=map[string]string{
 "responses":`[{"role":"user","content":"lookup"},{"type":"function_call","call_id":"call_known","name":"lookup","arguments":"{\"key\":\"a\"}"},{"type":"function_call_output","call_id":"call_known","output":"result"}]`,
 "chat":`[{"role":"user","content":"lookup"},{"role":"assistant","tool_calls":[{"id":"call_known","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"a\"}"}}]},{"role":"tool","tool_call_id":"call_known","content":"result"}]`,
}

func TestPhase2ReviewKnownNativeCacheMissingSignature(t *testing.T) {
 for api,history:=range phase2ReviewSingleHistory {t.Run(api,func(t *testing.T){
  cache:=upstream.NewSignatureCache(100)
  cache.PutToolDetails("call_known","lookup",map[string]any{"key":"a"},"")
  pred,err:=phase2ReviewConvert(t,api,history,cache)
  if err==nil {t.Fatalf("known native call lacking required signature was recovered as unknown: signature=%q",pred.Request.Contents[1].Parts[0].ThoughtSignature)}
 })}
}

var phase2ReviewParallelHistory=map[string]string{
 "responses":`[{"role":"user","content":"lookup"},{"type":"function_call","call_id":"call_lead","name":"lookup","arguments":"{\"key\":\"a\"}"},{"type":"function_call","call_id":"call_sibling","name":"lookup","arguments":"{\"key\":\"b\"}"},{"type":"function_call_output","call_id":"call_lead","output":"one"},{"type":"function_call_output","call_id":"call_sibling","output":"two"}]`,
 "chat":`[{"role":"user","content":"lookup"},{"role":"assistant","tool_calls":[{"id":"call_lead","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"a\"}"}},{"id":"call_sibling","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"b\"}"}}]},{"role":"tool","tool_call_id":"call_lead","content":"one"},{"role":"tool","tool_call_id":"call_sibling","content":"two"}]`,
}

func TestPhase2ReviewCachedSiblingRetainsNativeState(t *testing.T) {
 for api,history:=range phase2ReviewParallelHistory {t.Run(api,func(t *testing.T){
  cache:=upstream.NewSignatureCache(100)
  cache.PutToolDetails("call_lead","lookup",map[string]any{"key":"a"},"synthetic_lead_sig")
  cache.PutToolDetails("call_sibling","lookup",map[string]any{"key":"b"},"")
  cache.RecordTurnSiblings("call_lead",[]string{"call_sibling"})
  pred,err:=phase2ReviewConvert(t,api,history,cache);if err!=nil {t.Fatal(err)}
  for _,c:=range pred.Request.Contents {for _,p:=range c.Parts {if p.FunctionCall!=nil && p.FunctionCall.ID=="call_sibling" && p.ThoughtSignature!="synthetic_lead_sig" {t.Fatalf("verified native sibling did not use available native state: got %q",p.ThoughtSignature)}}}
 })}
}

func TestPhase2ReviewPairingValidationAppliesToEveryOrigin(t *testing.T) {
 cases:=map[string]map[string]string{
  "native_missing_result":{
   "responses":`[{"role":"user","content":"lookup"},{"type":"function_call","call_id":"call_known","name":"lookup","arguments":"{\"key\":\"a\"}"}]`,
   "chat":`[{"role":"user","content":"lookup"},{"role":"assistant","tool_calls":[{"id":"call_known","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"a\"}"}}]}]`,
  },
  "orphan_result":{
   "responses":`[{"role":"user","content":"lookup"},{"type":"function_call_output","call_id":"call_orphan","output":"result"}]`,
   "chat":`[{"role":"user","content":"lookup"},{"role":"tool","tool_call_id":"call_orphan","content":"result"}]`,
  },
  "unknown_result_before_call":{
   "responses":`[{"role":"user","content":"lookup"},{"type":"function_call_output","call_id":"call_unknown","output":"result"},{"type":"function_call","call_id":"call_unknown","name":"lookup","arguments":"{}"}]`,
   "chat":`[{"role":"user","content":"lookup"},{"role":"tool","tool_call_id":"call_unknown","content":"result"},{"role":"assistant","tool_calls":[{"id":"call_unknown","type":"function","function":{"name":"lookup","arguments":"{}"}}]}]`,
  },
  "duplicate_results":{
   "responses":`[{"role":"user","content":"lookup"},{"type":"function_call","call_id":"call_unknown","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_unknown","output":"one"},{"type":"function_call_output","call_id":"call_unknown","output":"two"}]`,
   "chat":`[{"role":"user","content":"lookup"},{"role":"assistant","tool_calls":[{"id":"call_unknown","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_unknown","content":"one"},{"role":"tool","tool_call_id":"call_unknown","content":"two"}]`,
  },
 }
 for name,protocols:=range cases {for api,history:=range protocols {t.Run(name+"/"+api,func(t *testing.T){
  cache:=upstream.NewSignatureCache(100);cache.PutToolDetails("call_known","lookup",map[string]any{"key":"a"},"synthetic_native_sig")
  if _,err:=phase2ReviewConvert(t,api,history,cache);err==nil {t.Fatal("invalid call/result pairing accepted")}
 })}}
}

func TestPhase2ReviewExplicitForeignOriginNotOverruledByAdjacency(t *testing.T) {
 for api,history:=range phase2ReviewParallelHistory {t.Run(api,func(t *testing.T){
  var items []map[string]any;if err:=json.Unmarshal([]byte(history),&items);err!=nil {t.Fatal(err)}
  if api=="responses" {items[2]["provider"]="anthropic"} else {calls:=items[1]["tool_calls"].([]any);calls[1].(map[string]any)["provider"]="anthropic"}
  raw,_:=json.Marshal(items)
  cache:=upstream.NewSignatureCache(100)
  cache.PutToolDetails("call_lead","lookup",map[string]any{"key":"a"},"synthetic_native_sig")
  pred,err:=phase2ReviewConvert(t,api,string(raw),cache)
  if err!=nil {t.Fatalf("affirmatively foreign paired call treated as native because of adjacency: %v",err)}
  for _,c:=range pred.Request.Contents {for _,p:=range c.Parts {if p.FunctionCall!=nil && p.FunctionCall.ID=="call_sibling" && p.ThoughtSignature!="skip_thought_signature_validator" {t.Errorf("foreign call did not receive migration marker")}}}
 })}
}
