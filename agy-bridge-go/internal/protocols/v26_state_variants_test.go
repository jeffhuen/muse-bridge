package protocols

import (
 "encoding/json"
 "fmt"
 "os"
 "path/filepath"
 "reflect"
 "testing"

 "github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func TestRecheckAdjacentSignedTextParts(t *testing.T) {
 for _, streaming:=range []bool{false,true} {t.Run(fmt.Sprint(streaming),func(t *testing.T){
  cache:=upstream.NewSignatureCache(10)
  parts:=[]upstream.Part{{Text:"First",ThoughtSignature:"synthetic_first"},{Text:"Second",ThoughtSignature:"synthetic_second"}}
  resp,_:=v26Response(t,streaming,parts,"STOP",cache)
  history:=[]any{map[string]any{"role":"user","content":"reply"}}
  history=append(history,resp["output"].([]any)...)
  history=append(history,map[string]any{"role":"user","content":"continue"})
  raw,_:=json.Marshal(history)
  pred,err:=ConvertResponsesToPrediction(&ResponsesRequest{Input:raw},cache);if err!=nil {t.Fatal(err)}
  got:=pred.Request.Contents[1].Parts
  if !reflect.DeepEqual(got,parts) {wantJSON,_:=json.Marshal(parts);gotJSON,_:=json.Marshal(got);t.Fatalf("distinct signed parts merged: want %s; got %s",wantJSON,gotJSON)}
 })}
}

func TestRecheckEmptyTailSignature(t *testing.T) {
 for _, streaming:=range []bool{false,true} {t.Run(fmt.Sprint(streaming),func(t *testing.T){
  cache:=upstream.NewSignatureCache(10)
  resp,_:=v26Response(t,streaming,[]upstream.Part{{Text:"Tail answer"},{ThoughtSignature:"synthetic_tail"}},"STOP",cache)
  encoded,_:=json.Marshal(resp)
  // It may be represented directly or in opaque state; check both supported forms.
  found:=false
  for _,v:=range resp["output"].([]any) {
   item:=v.(map[string]any)
   if item["thought_signature"]=="synthetic_tail" {found=true}
   if enc,ok:=item["encrypted_content"].(string);ok {state:=DecodeReasoningEncryptedContent(enc);if state!=nil {for _,sig:=range state.TextSignatures {if sig=="synthetic_tail" {found=true}}}}
  }
  if !found && cache.GetMessageSignature("msg_audit")!="synthetic_tail" {t.Fatalf("tail signature dropped from both response and cache: %s",encoded)}
 })}
}

func TestRecheckExportVariants(t *testing.T) {
 dir:=os.Getenv("V26_AUDIT_DIR");if dir=="" {t.Skip("set V26_AUDIT_DIR")}
 cases:=map[string][]upstream.Part{
  "tool_without_summary":{{FunctionCall:&upstream.FunctionCall{ID:"call_variant",Name:"lookup",Args:map[string]any{}},ThoughtSignature:"synthetic_variant_tool"}},
  "text_with_summary":{{Thought:true,Text:"A synthetic summary"},{Text:"Signed answer",ThoughtSignature:"synthetic_variant_text"}},
 }
 for name,parts:=range cases {_,sse:=v26Response(t,true,parts,"STOP",upstream.NewSignatureCache(10));if err:=os.WriteFile(filepath.Join(dir,name+".sse"),[]byte(sse),0600);err!=nil {t.Fatal(err)}}
}

func TestRecheckPiStateVariants(t *testing.T) {
 dir:=os.Getenv("V26_VARIANT_REPLAYS");if dir=="" {t.Skip("set V26_VARIANT_REPLAYS after Pi adapter replay")}
 for _,name:=range []string{"tool_without_summary","text_with_summary"} {t.Run(name,func(t *testing.T){
  raw,err:=os.ReadFile(filepath.Join(dir,name+"-replay.json"));if err!=nil {t.Fatal(err)}
  var req ResponsesRequest;if err=json.Unmarshal(raw,&req);err!=nil {t.Fatal(err)}
  pred,err:=ConvertResponsesToPrediction(&req,upstream.NewSignatureCache(10));if err!=nil {t.Fatalf("Pi full-history replay with empty cache failed: %v",err)}
  want:="synthetic_variant_text";if name=="tool_without_summary" {want="synthetic_variant_tool"}
  found:=false;for _,content:=range pred.Request.Contents {for _,part:=range content.Parts {if part.ThoughtSignature==want {found=true}}}
  if !found {t.Fatalf("Pi full-history replay lost %s",want)}
 })}
}
