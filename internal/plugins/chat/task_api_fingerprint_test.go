package chat

import "testing"

func TestOperationFingerprintExcludesBusEnvelope(t *testing.T) {
	base := map[string]any{"chat_id": "c1", "title": "same"}
	first := map[string]any{"chat_id": "c1", "title": "same", "_reply_to": "_inbox:a", "_corr": "1"}
	retry := map[string]any{"chat_id": "c1", "title": "same", "_reply_to": "_inbox:b", "_corr": "2"}
	if operationFingerprint(first) != operationFingerprint(retry) || operationFingerprint(first) != operationFingerprint(base) {
		t.Fatal("bus envelope changed the idempotency fingerprint")
	}
	retry["title"] = "different"
	if operationFingerprint(first) == operationFingerprint(retry) {
		t.Fatal("different operation payload shared a fingerprint")
	}
}
