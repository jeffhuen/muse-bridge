package protocols

import (
 "encoding/json"
 "fmt"
 "testing"

 "github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func TestAuthoritativeRecheckSignedUnsignedParts(t *testing.T) {
 for _, streaming := range []bool{false,true} { t.Run(fmt.Sprintf("streaming=%v",streaming),func(t *testing.T){
  original:=[]upstream.Part{{Text:"Signed segment",ThoughtSignature:"synthetic_segment_sig"},{Text:"Unsigned segment"}}
  _,outputs:=helperExecuteResponsesTurn(t,streaming,original,"STOP",upstream.NewSignatureCache(100))
  history:=[]any{map[string]any{"role":"user","content":"respond"}}
  history=append(history,helperSimulateClientStrip(outputs)...)
  history=append(history,map[string]any{"role":"user","content":"continue"})
  raw,_:=json.Marshal(history)
  pred,err:=ConvertResponsesToPrediction(&ResponsesRequest{Model:"gemini-3.8-flash-high",Input:raw},upstream.NewSignatureCache(100))
  if err!=nil {t.Fatal(err)}
  var got []upstream.Part
  for _,c:=range pred.Request.Contents {if c.Role=="model" {got=append(got,c.Parts...)}}
  if len(got)!=2 {t.Fatalf("original signed/unsigned parts were merged: want 2, got %d: %+v",len(got),got)}
  for i:=range original {if got[i].Text!=original[i].Text || got[i].ThoughtSignature!=original[i].ThoughtSignature {t.Errorf("part %d changed: want %+v got %+v",i,original[i],got[i])}}
 })}
}

func TestAuthoritativeRecheckForeignTextDoesNotBorrowNativeSignature(t *testing.T) {
 for _,streaming:=range []bool{false,true} {t.Run(fmt.Sprintf("streaming=%v",streaming),func(t *testing.T){
  _,outputs:=helperExecuteResponsesTurn(t,streaming,[]upstream.Part{{Text:"Done",ThoughtSignature:"synthetic_native_only"}},"STOP",upstream.NewSignatureCache(100))
  history:=[]any{
   map[string]any{"role":"user","content":"Earlier request to a different model"},
   map[string]any{"role":"assistant","content":"Done"},
   map[string]any{"role":"user","content":"New request to Gemini"},
  }
  history=append(history,helperSimulateClientStrip(outputs)...)
  history=append(history,map[string]any{"role":"user","content":"Continue"})
  raw,_:=json.Marshal(history)
  pred,err:=ConvertResponsesToPrediction(&ResponsesRequest{Model:"gemini-3.8-flash-high",Input:raw},upstream.NewSignatureCache(100))
  if err!=nil {t.Fatal(err)}
  var sigs []string
  for _,c:=range pred.Request.Contents {if c.Role=="model" {for _,p:=range c.Parts {if p.Text=="Done" {sigs=append(sigs,p.ThoughtSignature)}}}}
  if len(sigs)!=2 {t.Fatalf("want two model turns, got %v",sigs)}
  if sigs[0]!="" {t.Errorf("foreign earlier text borrowed later Gemini signature: %q",sigs[0])}
  if sigs[1]!="synthetic_native_only" {t.Errorf("native turn lost own signature: %q",sigs[1])}
 })}
}
