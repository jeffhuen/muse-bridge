package protocols

import (
 "encoding/json"
 "fmt"
 "net/http/httptest"
 "strings"
 "testing"

 "github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func TestV21ResponsesToolOnlyReplay(t *testing.T) {
 for _,streaming:=range []bool{false,true} {
  t.Run(fmt.Sprint(streaming),func(t *testing.T){
   body:=`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_a","name":"lookup_code","args":{}},"thoughtSignature":"signature-a"}]},"finishReason":"STOP"}]}}`+"\n\n"
   rec:=httptest.NewRecorder();cache:=upstream.NewSignatureCache(10)
   var response map[string]any
   if streaming {
    handleStreamingResponses(rec,strings.NewReader(body),"resp","msg",0,"gemini",cache)
    for _,e:=range auditEvents(t,rec.Body.String()){if e["type"]=="response.completed"{response=e["response"].(map[string]any)}}
   }else{
    handleNonStreamingResponses(rec,strings.NewReader(body),"resp","msg",0,"gemini",cache)
    if err:=json.Unmarshal(rec.Body.Bytes(),&response);err!=nil{t.Fatal(err)}
   }
   input:=[]any{map[string]any{"role":"user","content":"lookup alpha"}}
   input=append(input,response["output"].([]any)...)
   input=append(input,map[string]any{"type":"function_call_output","call_id":"call_a","output":"ALPHA"})
   raw,_:=json.Marshal(input)
   pred,err:=ConvertResponsesToPrediction(&ResponsesRequest{Input:raw},cache);if err!=nil{t.Fatal(err)}
   for i,c:=range pred.Request.Contents {for j,p:=range c.Parts{
    if p.Text==""&&p.FunctionCall==nil&&p.FunctionResponse==nil {t.Fatalf("replaying returned output produced invalid empty contents[%d].parts[%d]",i,j)}
   }}
  })
 }
}

func TestV21StreamCompletionValidation(t *testing.T) {
 cases:=map[string]string{
  "empty_eof":"",
  "partial_eof":`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]}}]}}`+"\n\n",
  "upstream_error":`data: {"error":{"code":429,"message":"test quota error","status":"RESOURCE_EXHAUSTED"}}`+"\n\n",
 }
 for name,body:=range cases {for _,api:=range []string{"chat","responses"}{for _,streaming:=range []bool{false,true}{
  t.Run(fmt.Sprintf("%s/%s/stream=%v",name,api,streaming),func(t *testing.T){
   rec:=httptest.NewRecorder();reader:=strings.NewReader(body)
   if api=="chat" {
    if streaming {handleStreamingChat(rec,httptest.NewRequest("POST","/",nil),reader,"cmpl",0,"gemini",nil)}else{handleNonStreamingChat(rec,reader,"cmpl",0,"gemini",nil)}
   }else{
    if streaming{handleStreamingResponses(rec,reader,"resp","msg",0,"gemini",nil)}else{handleNonStreamingResponses(rec,reader,"resp","msg",0,"gemini",nil)}
   }
   output:=rec.Body.String()
   if rec.Code<400&&!strings.Contains(output,`"error"`)&&!strings.Contains(output,`"response.failed"`){t.Fatal("non-terminal or error stream reported success")}
  })
 }}}
}

func v21SignedReply(text,signature string,cache *upstream.SignatureCache) ChatMessage {
 p:=upstream.Part{Text:text,ThoughtSignature:signature}
 event:=upstream.SSEStreamEvent{Response:&upstream.PredictionResponse{Candidates:[]upstream.Candidate{{Content:upstream.Content{Role:"model",Parts:[]upstream.Part{p}},FinishReason:"STOP"}}}}
 raw,_:=json.Marshal(event);rec:=httptest.NewRecorder()
 handleNonStreamingChat(rec,strings.NewReader("data: "+string(raw)+"\n\n"),"cmpl",0,"gemini",cache)
 var out struct{Choices []struct{Message ChatMessage `json:"message"`} `json:"choices"`}
 json.Unmarshal(rec.Body.Bytes(),&out)
 return out.Choices[0].Message
}

func TestV21TextSignatureIsolation(t *testing.T) {
 cache:=upstream.NewSignatureCache(10)
 a:=v21SignedReply("Same reply","signature-conversation-a",cache)
 v21SignedReply("Same reply","signature-conversation-b",cache)
 req:=&ChatRequest{Messages:[]ChatMessage{{Role:"user",Content:json.RawMessage(`"conversation A"`)},a,{Role:"user",Content:json.RawMessage(`"continue A"`)}}}
 pred,err:=ConvertChatToPrediction(req,cache);if err!=nil{t.Fatal(err)}
 if pred.Request.Contents[1].Parts[0].ThoughtSignature!="signature-conversation-a"{t.Fatal("conversation A replay received conversation B's signature")}
}

func TestV21ChunkedTextSignatures(t *testing.T) {
 for _,tailOnly:=range []bool{false,true}{t.Run(fmt.Sprintf("signature_only_tail=%v",tailOnly),func(t *testing.T){
  parts:=[]upstream.Part{{Text:"First "},{Text:"second",ThoughtSignature:"signed-part"}}
  if tailOnly{parts[1].ThoughtSignature="";parts=append(parts,upstream.Part{ThoughtSignature:"signed-tail"})}
  var sse strings.Builder
  for i,p:=range parts {
   candidate:=upstream.Candidate{Content:upstream.Content{Role:"model",Parts:[]upstream.Part{p}}}
   if i==len(parts)-1{candidate.FinishReason="STOP"}
   b,_:=json.Marshal(upstream.SSEStreamEvent{Response:&upstream.PredictionResponse{Candidates:[]upstream.Candidate{candidate}}})
   fmt.Fprintf(&sse,"data: %s\n\n",b)
  }
  cache:=upstream.NewSignatureCache(10);rec:=httptest.NewRecorder()
  handleNonStreamingChat(rec,strings.NewReader(sse.String()),"cmpl",0,"gemini",cache)
  var out struct{Choices []struct{Message ChatMessage `json:"message"`} `json:"choices"`}
  json.Unmarshal(rec.Body.Bytes(),&out)
  pred,err:=ConvertChatToPrediction(&ChatRequest{Messages:[]ChatMessage{{Role:"user",Content:json.RawMessage(`"question"`)},out.Choices[0].Message,{Role:"user",Content:json.RawMessage(`"continue"`)}}},cache)
  if err!=nil{t.Fatal(err)}
  found:=false
  for _,part:=range pred.Request.Contents[1].Parts{if part.ThoughtSignature!=""{found=true}}
  if !found{t.Fatal("signature from multi-chunk assistant response was lost on replay")}
 })}
}

func TestV21UnsupportedContinuationAndTools(t *testing.T) {
 for name,body:=range map[string]string{
  "previous_response_id":`{"previous_response_id":"resp_unknown","input":"Continue from the previous turn"}`,
  "custom_tool":`{"input":"Use the patch tool","tools":[{"type":"custom","name":"apply_patch","format":{"type":"text"}}]}`,
  "hosted_mcp_tool":`{"input":"Use the MCP tool","tools":[{"type":"mcp","server_label":"fixture","server_url":"https://example.com/mcp"}]}`,
 } {t.Run(name,func(t *testing.T){
  var req ResponsesRequest
  if err:=json.Unmarshal([]byte(body),&req);err!=nil{t.Fatal(err)}
  pred,err:=ConvertResponsesToPrediction(&req,upstream.NewSignatureCache(10))
  if err==nil&&len(pred.Request.Tools)==0&&len(pred.Request.Contents)==1 {t.Fatal("unsupported continuation/tool metadata silently discarded without error")}
 })}
}
