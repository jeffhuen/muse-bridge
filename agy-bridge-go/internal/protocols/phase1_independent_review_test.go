package protocols

import (
 "encoding/json"
 "fmt"
 "io"
 "net/http"
 "strings"
 "sync"
 "testing"
 "time"

 "github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func TestPhase1ReviewEditedToolsCannotUseFallbacks(t *testing.T) {
 for _,streaming:=range []bool{false,true} {
  for _,sibling:=range []bool{false,true} {
   for _,warm:=range []bool{false,true} {
    t.Run(fmt.Sprintf("stream=%v/sibling=%v/warm=%v",streaming,sibling,warm),func(t *testing.T){
     cache:=upstream.NewSignatureCache(100)
     parts:=[]upstream.Part{{FunctionCall:&upstream.FunctionCall{ID:"call_lead",Name:"lookup",Args:map[string]any{"key":"original"}},ThoughtSignature:"synthetic_owned_signature"}}
     target:="call_lead"
     if sibling {parts=append(parts,upstream.Part{FunctionCall:&upstream.FunctionCall{ID:"call_sibling",Name:"lookup",Args:map[string]any{"key":"second"}}});target="call_sibling"}
     _,outputs:=helperExecuteResponsesTurn(t,streaming,parts,"STOP",cache)
     stripped:=helperSimulateClientStrip(outputs)
     for _,raw:=range stripped {
         item:=raw.(map[string]any)
         if item["type"]=="function_call" {
             cID, _ := item["call_id"].(string)
             isTarget := cID == target
             if !isTarget && cache != nil {
                 if rec, ok := cache.GetToolRecord(cID); ok && rec != nil && rec.UpstreamID == target {
                     isTarget = true
                 }
             }
             if isTarget {
                 item["arguments"]=`{"key":"edited"}`
             }
         }
     }
     history:=[]any{map[string]any{"role":"user","content":"look up"}}
     history=append(history,stripped...)
     history=append(history,map[string]any{"type":"function_call_output","call_id":"call_lead","output":"one"})
     if sibling {history=append(history,map[string]any{"type":"function_call_output","call_id":"call_sibling","output":"two"})}
     raw,_:=json.Marshal(history)
     if !warm {cache=upstream.NewSignatureCache(100)}
     pred,err:=ConvertResponsesToPrediction(&ResponsesRequest{Model:"gemini-3.8-flash-high",Input:raw},cache)
     if err!=nil {return} // Explicit rejection is an allowed integrity policy.
     for _,c:=range pred.Request.Contents {for _,p:=range c.Parts {if p.FunctionCall!=nil && p.FunctionCall.ID==target && p.ThoughtSignature=="synthetic_owned_signature" {t.Fatalf("edited call %s acquired original signature through fallback (warm=%v sibling=%v)",target,warm,sibling)}}}
    })
   }
  }
 }
}

func TestPhase1ReviewStreamIndexesMatchFinalOutput(t *testing.T) {
 cases:=map[string]struct{parts []upstream.Part;finish string}{
  "reasoning_only_token_limit":{[]upstream.Part{{Thought:true,Text:"Synthetic summary"}},"MAX_TOKENS"},
  "reasoning_after_text":{[]upstream.Part{{Text:"Before",ThoughtSignature:"synthetic_before"},{Thought:true,Text:"Synthetic later summary"},{Text:"After",ThoughtSignature:"synthetic_after"}},"STOP"},
 }
 for name,tc:=range cases {t.Run(name,func(t *testing.T){
  response,sse:=v26Response(t,true,tc.parts,tc.finish,upstream.NewSignatureCache(100))
  ids:=map[int]string{}
  for _,e:=range auditEvents(t,sse) {if e["type"]=="response.output_item.added" {ids[int(e["output_index"].(float64))]=e["item"].(map[string]any)["id"].(string)}}
  outputs:=response["output"].([]any)
  if len(ids)!=len(outputs) {t.Errorf("stream added %d items but final output contains %d",len(ids),len(outputs))}
  for i,raw:=range outputs {id:=raw.(map[string]any)["id"].(string);if ids[i]!=id {t.Errorf("final output[%d]=%s differs from streamed id=%s",i,id,ids[i])}}
 })}
}

type phase1StreamProbe struct {header http.Header; body strings.Builder; delta chan struct{}; once sync.Once}
func (p *phase1StreamProbe) Header() http.Header {return p.header}
func (p *phase1StreamProbe) WriteHeader(int) {}
func (p *phase1StreamProbe) Flush() {}
func (p *phase1StreamProbe) Write(b []byte)(int,error){n,err:=p.body.Write(b);if strings.Contains(p.body.String(),"response.output_text.delta") {p.once.Do(func(){close(p.delta)})};return n,err}

func TestPhase1ReviewStreamsBeforeUpstreamCompletion(t *testing.T) {
 r,w:=io.Pipe();defer r.Close();defer w.Close()
 probe:=&phase1StreamProbe{header:make(http.Header),delta:make(chan struct{})}
 done:=make(chan struct{})
 go func(){defer close(done);handleStreamingResponses(probe,r,"resp_probe","msg_probe",0,"gemini-3.8-flash-high",upstream.NewSignatureCache(10))}()
 chunk:=upstream.SSEStreamEvent{Response:&upstream.PredictionResponse{Candidates:[]upstream.Candidate{{Content:upstream.Content{Role:"model",Parts:[]upstream.Part{{Text:"first fragment"}}}}}}}
 raw,_:=json.Marshal(chunk)
 if _,err:=fmt.Fprintf(w,"data: %s\n\n",raw);err!=nil {t.Fatal(err)}
 select {case <-probe.delta:case <-time.After(2*time.Second):t.Error("no text delta before upstream completion")}
 chunk.Response.Candidates[0].Content.Parts=nil;chunk.Response.Candidates[0].FinishReason="STOP";raw,_=json.Marshal(chunk)
 if _,err:=fmt.Fprintf(w,"data: %s\n\n",raw);err!=nil {t.Fatal(err)}
 w.Close()
 select {case <-done:case <-time.After(2*time.Second):t.Fatal("stream did not finish")}
}
