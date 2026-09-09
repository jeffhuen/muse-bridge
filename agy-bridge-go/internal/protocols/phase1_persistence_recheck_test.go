package protocols

import (
 "encoding/json"
 "fmt"
 "path/filepath"
 "testing"

 "github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func TestPhase1RecheckRestoredCacheValidatesToolContent(t *testing.T) {
 for _,streaming:=range []bool{false,true} {t.Run(fmt.Sprintf("stream=%v",streaming),func(t *testing.T){
  cache:=upstream.NewSignatureCache(100)
  original:=[]upstream.Part{{FunctionCall:&upstream.FunctionCall{ID:"call_restore",Name:"lookup",Args:map[string]any{"key":"original"}},ThoughtSignature:"synthetic_restore_sig"}}
  _,outputs:=helperExecuteResponsesTurn(t,streaming,original,"STOP",cache)
  path:=filepath.Join(t.TempDir(),"cache.json")
  if err:=cache.SaveToFile(path);err!=nil {t.Fatal(err)}
  restored:=upstream.NewSignatureCache(100)
  if err:=restored.LoadFromFile(path);err!=nil {t.Fatal(err)}
  stripped:=helperSimulateClientStrip(outputs)
  var call map[string]any
  for _,raw:=range stripped {item:=raw.(map[string]any);if item["type"]=="function_call" {call=item;break}}
  if call==nil {t.Fatal("tool call missing")}
  cID, _ := call["call_id"].(string)
  if restored.GetToolArgs(cID)["key"]!="original" {t.Fatal("original arguments did not survive save/load")}
  replay:=func()(*upstream.PredictionRequest,error){
   // Omit the reasoning carrier deliberately to exercise restored-cache validation.
   raw,_:=json.Marshal([]any{map[string]any{"role":"user","content":"look up"},call,map[string]any{"type":"function_call_output","call_id":cID,"output":"result"}})
   return ConvertResponsesToPrediction(&ResponsesRequest{Model:"gemini-3.8-flash-high",Input:raw},restored)
  }
  pred,err:=replay();if err!=nil {t.Fatalf("unchanged cache replay failed: %v",err)}
  if pred.Request.Contents[1].Parts[0].ThoughtSignature!="synthetic_restore_sig" {t.Fatal("unchanged signature was lost")}
  call["arguments"]=`{"key":"edited"}`
  if _,err=replay();err==nil {t.Fatal("edited call was accepted using restored native state")}
 })}
}
