package protocols

import (
 "encoding/json"
 "fmt"
 "net/http/httptest"
 "os"
 "path/filepath"
 "reflect"
 "strings"
 "testing"

 "github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func v26Response(t *testing.T, streaming bool, parts []upstream.Part, finish string, cache *upstream.SignatureCache) (map[string]any, string) {
 t.Helper()
 event := upstream.SSEStreamEvent{Response: &upstream.PredictionResponse{Candidates: []upstream.Candidate{{Content: upstream.Content{Role:"model", Parts:parts}, FinishReason:finish}}}}
 raw, err := json.Marshal(event); if err != nil { t.Fatal(err) }
 reader := strings.NewReader("data: " + string(raw) + "\n\n")
 w := httptest.NewRecorder()
 if streaming { handleStreamingResponses(w, reader, "resp_audit", "msg_audit", 0, "gemini-3.8-flash-high", cache) } else { handleNonStreamingResponses(w, reader, "resp_audit", "msg_audit", 0, "gemini-3.8-flash-high", cache) }
 var response map[string]any
 if !streaming { if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil { t.Fatal(err) } } else {
  for _, e := range auditEvents(t,w.Body.String()) { if e["type"] == "response.completed" || e["type"] == "response.incomplete" { response=e["response"].(map[string]any) } }
 }
 if response == nil { t.Fatalf("missing response: %s",w.Body.String()) }
 return response,w.Body.String()
}

func TestV26OrderedSignedPartsReplay(t *testing.T) {
 for _, streaming := range []bool{false,true} { t.Run(fmt.Sprint(streaming),func(t *testing.T) {
  cache:=upstream.NewSignatureCache(20)
  original:=[]upstream.Part{
   {Text:"Before", ThoughtSignature:"synthetic_text_before"},
   {FunctionCall:&upstream.FunctionCall{ID:"call_order", Name:"lookup", Args:map[string]any{}}, ThoughtSignature:"synthetic_tool"},
   {Text:"After", ThoughtSignature:"synthetic_text_after"},
  }
  response,_:=v26Response(t,streaming,original,"STOP",cache)
  history:=[]any{map[string]any{"role":"user","content":"look it up"}}
  history=append(history,response["output"].([]any)...)
  history=append(history,map[string]any{"type":"function_call_output","call_id":"call_order","output":"result"})
  raw,_:=json.Marshal(history)
  pred,err:=ConvertResponsesToPrediction(&ResponsesRequest{Model:"gemini-3.8-flash-high",Input:raw},cache); if err!=nil {t.Fatal(err)}
  var got []upstream.Part
  for _, content:=range pred.Request.Contents { if content.Role=="model" {got=append(got,content.Parts...)} }
  if !reflect.DeepEqual(got,original) {wantJSON,_:=json.Marshal(original);gotJSON,_:=json.Marshal(got);t.Fatalf("ordered signed parts changed: want %s; got %s",wantJSON,gotJSON)}
 }) }
}

func TestV26UnknownAdjacentCallDoesNotBorrowSignature(t *testing.T) {
 cache:=upstream.NewSignatureCache(10)
 cache.PutToolInfo("call_verified", "lookup", "synthetic_verified_signature")
 input:=json.RawMessage(`[{"role":"user","content":"lookup"},{"type":"function_call","call_id":"call_verified","name":"lookup","arguments":"{}"},{"type":"function_call","call_id":"call_never_issued","name":"unrelated","arguments":"{}"},{"type":"function_call_output","call_id":"call_verified","output":"one"},{"type":"function_call_output","call_id":"call_never_issued","output":"two"}]`)
 pred,err:=ConvertResponsesToPrediction(&ResponsesRequest{Input:input},cache)
 if err==nil { t.Fatalf("unissued adjacent call inherited %q; no cached issuance or turn provenance exists for call_never_issued",pred.Request.Contents[1].Parts[1].ThoughtSignature) }
}

func TestV26ResponsesTokenLimitIsIncomplete(t *testing.T) {
 for _, streaming:=range []bool{false,true} {t.Run(fmt.Sprint(streaming),func(t *testing.T){
  response,_:=v26Response(t,streaming,[]upstream.Part{{Text:"unfinished answer"}},"MAX_TOKENS",upstream.NewSignatureCache(10))
  if response["status"]!="incomplete" { t.Fatalf("MAX_TOKENS reported as status=%v instead of incomplete",response["status"]) }
 })}
}

func TestV26ExportPiFixture(t *testing.T) {
 dir:=os.Getenv("V26_AUDIT_DIR");if dir=="" {t.Skip("set V26_AUDIT_DIR to export synthetic fixture")}
 _,sse:=v26Response(t,true,[]upstream.Part{{Thought:true,Text:"Synthetic thought summary"},{FunctionCall:&upstream.FunctionCall{ID:"call_fixture",Name:"lookup",Args:map[string]any{"key":"alpha"}},ThoughtSignature:"synthetic_fixture_signature"}},"STOP",upstream.NewSignatureCache(10))
 if err:=os.WriteFile(filepath.Join(dir,"responses-fixture.sse"),[]byte(sse),0600);err!=nil {t.Fatal(err)}
}

func TestV26PiReplayWithEmptyCache(t *testing.T) {
 path:=os.Getenv("V26_PI_REPLAY");if path=="" {t.Skip("set V26_PI_REPLAY to actual Pi adapter output")}
 raw,err:=os.ReadFile(path);if err!=nil {t.Fatal(err)}
 var req ResponsesRequest;if err=json.Unmarshal(raw,&req);err!=nil {t.Fatal(err)}
 // Verify the client round trip works while cached IDs remain available.
 warm:=upstream.NewSignatureCache(10);warm.PutToolInfo("call_fixture","lookup","synthetic_fixture_signature")
 if _,err=ConvertResponsesToPrediction(&req,warm);err!=nil {t.Fatalf("warm cache failed: %v",err)}
 // The same full history must carry enough state to survive cache loss.
 if _,err=ConvertResponsesToPrediction(&req,upstream.NewSignatureCache(10));err!=nil {t.Fatalf("actual Pi Responses replay loses required state when cache is empty: %v",err)}
}
