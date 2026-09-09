package upstream

import (
 "path/filepath"
 "testing"
)

func TestV26AmbiguitySurvivesRestart(t *testing.T) {
 cache:=NewSignatureCache(10)
 cache.PutContextSignature("same_history_and_answer","synthetic_A")
 cache.PutContextSignature("same_history_and_answer","synthetic_B")
 if got:=cache.GetContextSignature("same_history_and_answer");got!="" {t.Fatalf("pre-restart ambiguity not detected: %q",got)}
 path:=filepath.Join(t.TempDir(),"cache.json")
 if err:=cache.SaveToFile(path);err!=nil {t.Fatal(err)}
 restarted:=NewSignatureCache(10)
 if err:=restarted.LoadFromFile(path);err!=nil {t.Fatal(err)}
 restarted.PutContextSignature("same_history_and_answer","synthetic_C")
 if got:=restarted.GetContextSignature("same_history_and_answer");got!="" {t.Fatalf("known ambiguous history became usable again after restart: got %q",got)}
}
