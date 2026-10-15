package i18n

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestTextAPIFormatsActualArgumentsUsingNativeGoRules(t *testing.T) {
	pattern := "%[2]s / %[1]08d / %.[3]*[4]f / %[2]q / %%"
	raw, err := json.Marshal(map[string]string{"message": pattern, "english.only": "Fallback %[1]s"})
	if err != nil {
		t.Fatal(err)
	}
	handler := TextHandler(fstest.MapFS{"lang/en_us.json": &fstest.MapFile{Data: raw}}, "lang")
	arguments := []any{int64(23), "{0} %[1]s", int64(2), 1.2345}
	body, err := json.Marshal(TextRequest{Locale: "ja_jp", Messages: []TextMessage{{Key: "message", Args: arguments}, {Key: "english.only", Args: []any{"日本語"}}, {Key: "%999999s", Args: []any{"untrusted"}}}})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodPost, "/text/format", bytes.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("format request failed: %d %s", response.Code, response.Body)
	}
	var result struct{ Texts []string }
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	want := []string{fmt.Sprintf(pattern, arguments...), "Fallback 日本語", "%999999s"}
	if len(result.Texts) != len(want) {
		t.Fatalf("response messages = %v", result.Texts)
	}
	for i, text := range want {
		if result.Texts[i] != text {
			t.Errorf("text %d = %q; native Go format requires %q", i, result.Texts[i], text)
		}
	}
}

func TestTextAPIRejectsUnsupportedLanguagesAndNonPrimitiveArguments(t *testing.T) {
	handler := TextHandler(fstest.MapFS{"lang/en_us.json": &fstest.MapFile{Data: []byte(`{"message":"%[1]s"}`)}}, "lang")
	for _, body := range []string{
		`{"locale":"de_de","messages":[{"key":"message","args":["hello"]}]}`,
		`{"locale":"en_us","messages":[{"key":"message","args":[{"key":"anything"}]}]}`,
		`{"locale":"en_us","messages":[{"key":"message","args":["hello"]}]} {}`,
	} {
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(http.MethodPost, "/text/format", bytes.NewBufferString(body)))
		if response.Code != http.StatusBadRequest {
			t.Errorf("invalid request was accepted: %s -> %d", body, response.Code)
		}
	}
}
