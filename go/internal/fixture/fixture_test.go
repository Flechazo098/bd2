package fixture

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/cryptox"
	"bd2server/internal/wire"
)

func captureRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "data", "capture", "2.34.13", "20260920-003254"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCapturedQuestUpdateResponseShape(t *testing.T) {
	set, err := Load(captureRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		captureSequence int
		questID         uint64
	}{{60, 1}, {71, 4}, {120, 12}} {
		payload, _, err := set.RecordedPayloadAt("/QuestUpdate", test.captureSequence)
		if err != nil {
			t.Fatal(err)
		}
		updated, found, err := wire.Varint(payload.Proto, 1)
		if err != nil || !found || updated != test.questID {
			t.Fatalf("sequence %d: updateQuestId=%d found=%v err=%v", test.captureSequence, updated, found, err)
		}
		rewardBundle, reward, err := wire.Bytes(payload.Proto, 2)
		if err != nil || !reward || len(rewardBundle) != 0 {
			t.Fatalf("sequence %d: reward bundle bytes=%d found=%v err=%v", test.captureSequence, len(rewardBundle), reward, err)
		}
		if payload.PacketCode != 19 {
			t.Fatalf("sequence %d: packet code %d", test.captureSequence, payload.PacketCode)
		}
	}
}

func TestLogCapturedQuestClearShape(t *testing.T) {
	set, err := Load(captureRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, sequence := range []int{61, 63, 121} {
		payload, _, err := set.RecordedPayloadAt("/QuestClear", sequence)
		if err != nil {
			t.Fatal(err)
		}
		var fields []string
		if err := wire.Walk(payload.Proto, func(field wire.Field) error {
			fields = append(fields, fmt.Sprintf("#%d/type%d/%dB", field.Number, field.Type, len(field.Value)))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		t.Logf("sequence=%d packet=%d fields=%v", sequence, payload.PacketCode, fields)
	}
}

func TestLogNewPlayerQuestRewards(t *testing.T) {
	set, err := Load(captureRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range set.RecordedResponses("/QuestClear") {
		payload, _, err := set.RecordedPayloadAt("/QuestClear", record.RequestSequence)
		if err != nil {
			t.Fatal(err)
		}
		quest, _, _ := wire.Varint(payload.Proto, 3)
		nextRaw, _, _ := wire.Bytes(payload.Proto, 2)
		next, _, _ := wire.Varint(nextRaw, 1)
		reward, _, _ := wire.Bytes(payload.Proto, 1)
		var items, chars, costumes []string
		var responseChars, decks []string
		_ = wire.Walk(reward, func(field wire.Field) error {
			if field.Type != 2 {
				return nil
			}
			switch field.Number {
			case 1:
				idx, _, _ := wire.Varint(field.Value, 1)
				id, _, _ := wire.Varint(field.Value, 2)
				typ, _, _ := wire.Varint(field.Value, 3)
				count, _, _ := wire.Varint(field.Value, 4)
				items = append(items, fmt.Sprintf("idx=%d id=%d type=%d count=%d", idx, id, typ, count))
			case 2:
				idx, _, _ := wire.Varint(field.Value, 1)
				id, _, _ := wire.Varint(field.Value, 2)
				hp, _, _ := wire.Varint(field.Value, 3)
				level, _, _ := wire.Varint(field.Value, 4)
				costume, _, _ := wire.Varint(field.Value, 7)
				chars = append(chars, fmt.Sprintf("idx=%d id=%d hp=%d level=%d costumeIdx=%d raw=%x", idx, id, hp, level, costume, field.Value))
			case 3:
				idx, _, _ := wire.Varint(field.Value, 1)
				id, _, _ := wire.Varint(field.Value, 2)
				useChar, _, _ := wire.Varint(field.Value, 4)
				costumes = append(costumes, fmt.Sprintf("idx=%d id=%d useChar=%d raw=%x", idx, id, useChar, field.Value))
			}
			return nil
		})
		_ = wire.Walk(payload.Proto, func(field wire.Field) error {
			if field.Type != 2 {
				return nil
			}
			if field.Number == 4 {
				idx, _, _ := wire.Varint(field.Value, 1)
				costume, _, _ := wire.Varint(field.Value, 2)
				slot, _, _ := wire.Varint(field.Value, 3)
				decks = append(decks, fmt.Sprintf("charIdx=%d costume=%d slot=%d", idx, costume, slot))
			} else if field.Number == 5 {
				idx, _, _ := wire.Varint(field.Value, 1)
				id, _, _ := wire.Varint(field.Value, 2)
				hp, _, _ := wire.Varint(field.Value, 3)
				level, _, _ := wire.Varint(field.Value, 4)
				useCostume, _, _ := wire.Varint(field.Value, 7)
				connect, _, _ := wire.Varint(field.Value, 13)
				responseChars = append(responseChars, fmt.Sprintf("idx=%d id=%d hp=%d level=%d costumeIdx=%d connect=%d raw=%x", idx, id, hp, level, useCostume, connect, field.Value))
			}
			return nil
		})
		t.Logf("quest=%d next=%d requestSeq=%d items=%v rewardChars=%v costumes=%v decks=%v responseChars=%v", quest, next, record.RequestSequence, items, chars, costumes, decks, responseChars)
	}
}

func TestCapturedBootstrapResponsesDecode(t *testing.T) {
	c, err := Open(captureRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range Bootstrap {
		response, err := c.BootstrapResponse(spec.Name)
		if err != nil {
			t.Fatalf("%s: %v", spec.Name, err)
		}
		key := []byte(nil)
		if spec.Key == FixedLogin {
			key = cryptox.Key()
		}
		env, proto, err := DecodeEnvelope(response.Body, key)
		if err != nil {
			t.Fatalf("%s: %v", spec.Name, err)
		}
		if env.ErrorType != 0 || len(proto) == 0 {
			t.Fatalf("%s: errorType=%d proto=%d length=%d", spec.Name, env.ErrorType, len(proto), env.Length)
		}
	}
}

func TestEnvelopeReencryptsAndKeepsLength(t *testing.T) {
	c, err := Open(captureRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	login, err := c.Login()
	if err != nil {
		t.Fatal(err)
	}
	env, proto, err := DecodeEnvelope(login.Body, cryptox.Key())
	if err != nil {
		t.Fatal(err)
	}
	reencoded, err := EncodeEnvelope(env, proto, cryptox.Key(), true)
	if err != nil {
		t.Fatal(err)
	}
	gotEnv, got, err := DecodeEnvelope(reencoded, cryptox.Key())
	if err != nil {
		t.Fatal(err)
	}
	if gotEnv.Length != base64.StdEncoding.EncodedLen(len(proto)) || !bytes.Equal(got, proto) {
		t.Fatalf("reencrypted payload changed: got=%d want=%d", len(got), len(proto))
	}
}

func TestBatchRequestsAndResponsesAreComplete(t *testing.T) {
	c, err := Open(captureRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	key, err := cryptox.SessionKey("7eeb5c2480c298f6fc4c00d22b1b0b15")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		number  int
		request string
		want    int
	}{
		{1, "00040_req_BatchRequest.bin", 57},
		{2, "00076_req_BatchRequest.bin", 5},
	} {
		reqBody, err := os.ReadFile(filepath.Join(c.Root, "bodies", tc.request))
		if err != nil {
			t.Fatal(err)
		}
		reqs, reqPayloads, err := DecodeBatchRequest(reqBody, key)
		if err != nil {
			t.Fatalf("batch %d request: %v", tc.number, err)
		}
		response, err := c.Batch(tc.number)
		if err != nil {
			t.Fatal(err)
		}
		items, responsePayloads, err := DecodeBatch(response.Body, key)
		if err != nil {
			t.Fatalf("batch %d response: %v", tc.number, err)
		}
		if len(reqs) != tc.want || len(items) != tc.want || len(reqPayloads) != tc.want || len(responsePayloads) != tc.want {
			t.Fatalf("batch %d: req=%d resp=%d (want %d)", tc.number, len(reqs), len(items), tc.want)
		}
		for path := range reqPayloads {
			if _, ok := responsePayloads[path]; !ok {
				t.Fatalf("batch %d lacks response path %s", tc.number, path)
			}
		}
	}
}

func TestSetProvidesIntegrationAPIAndOrdersBatchByRequest(t *testing.T) {
	set, err := Load(captureRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.CoreResponse("/MaintenanceInfo"); err != nil {
		t.Fatal(err)
	}
	key, err := set.SessionKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 {
		t.Fatalf("session key has length %d", len(key))
	}
	req, err := os.ReadFile(filepath.Join(set.Root, "bodies", "00040_req_BatchRequest.bin"))
	if err != nil {
		t.Fatal(err)
	}
	requests, _, err := DecodeBatchRequest(req, []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, len(requests))
	for i := range requests {
		paths[i] = requests[i].Path
	}
	ordered, err := set.BatchForPaths(1, paths)
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range ordered {
		if item.Path != paths[i] {
			t.Fatalf("ordered[%d]=%s want %s", i, item.Path, paths[i])
		}
	}
}

func TestRecordedResponsesUseRequestSequenceAndCanReencrypt(t *testing.T) {
	set, err := Load(captureRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	// Sequence 10 is EventScheduleInfo in this capture. It is a session-key
	// encrypted response; use a different local key to prove re-encryption.
	payload, record, err := set.RecordedPayloadAt("/EventScheduleInfo", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.Encrypted || record.File != "00018_resp_EventScheduleInfo.bin" {
		t.Fatalf("unexpected selected record: %#v encrypted=%t", record, payload.Encrypted)
	}
	newKey, err := cryptox.SessionKey("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	reencoded, _, err := set.ReencryptRecordedAt("/EventScheduleInfo", 10, newKey)
	if err != nil {
		t.Fatal(err)
	}
	_, proto, err := DecodeEnvelope(reencoded, newKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(proto, payload.Proto) {
		t.Fatal("re-encrypted selected payload changed")
	}
	if _, _, err := set.RecordedResponseAt("/EventScheduleInfo", 999); err == nil {
		t.Fatal("unsafe/nonexistent sequence unexpectedly resolved")
	}
}

func TestRecordedResponseResolvesClientSequence(t *testing.T) {
	set, err := Load(captureRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	record, err := set.RecordedResponseForClientSequence("/TutorialClear", 103)
	if err != nil {
		t.Fatal(err)
	}
	if record.RequestSequence != 57 || record.File != "00109_resp_TutorialClear.bin" {
		t.Fatalf("unexpected TutorialClear record: %+v", record)
	}
	if _, err := set.RecordedResponseForClientSequence("/TutorialClear", 999999); err == nil {
		t.Fatal("unknown client sequence unexpectedly resolved")
	}
}

func TestHandleBatchReencryptsAllResponseDataForLocalSession(t *testing.T) {
	set, err := Load(captureRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	capturedKey, err := set.SessionKey()
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(set.Root, "bodies", "00040_req_BatchRequest.bin"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := cryptox.DecryptBase64(string(original), []byte(capturedKey))
	if err != nil {
		t.Fatal(err)
	}
	localKey, err := cryptox.SessionKey("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	localRequest, err := cryptox.EncryptBase64(plain, localKey)
	if err != nil {
		t.Fatal(err)
	}
	response, err := set.HandleBatch(1, []byte(localRequest), localKey)
	if err != nil {
		t.Fatal(err)
	}
	items, payloads, err := DecodeBatch(response, localKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 57 || len(payloads) != 57 || items[0].Path != "/MailInfo" {
		t.Fatalf("local batch response did not preserve request order/count: first=%q count=%d", items[0].Path, len(items))
	}
	if items[0].Response.ServerNowTime == 0 || items[0].Response.Notify == "" {
		t.Fatal("batch response lost envelope metadata while re-encrypting")
	}
}
