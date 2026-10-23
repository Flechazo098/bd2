//go:build integration

package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"bd2server/internal/server/design/gameconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/gateway/transport"
)

// This encodes the 2.36.7 client's RecordData.Serialize/Compress format,
// independently of the server parser: click events on prestige illustration
// 1065193, point 1/1, tool 0 from GameData 20261021182514.
func nativeInteractionRecording(t *testing.T, delays ...int32) []byte {
	t.Helper()
	raw := binary.LittleEndian.AppendUint32(nil, uint32(len(delays)))
	for _, delay := range delays {
		raw = append(raw, 1)
		for _, value := range []int32{delay, 1, 1, 0} {
			raw = binary.AppendUvarint(raw, uint64(uint32(value<<1)^uint32(value>>31)))
		}
	}
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	out := binary.LittleEndian.AppendUint32(nil, crc32.ChecksumIEEE(raw))
	return append(append(out, 1), compressed.Bytes()...)
}

func TestInteractionRecordingPersistsNativePlaybackAndRevokesReplacedData(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	for _, account := range accounts {
		s := &trapPlayerScenario{factory: factory, account: account, owner: openNicknamePlayer(t, factory, account)}
		s.run(t, true, func(ctx command.Context, p *playerAssembly) {
			if _, err := p.ownedItems.GrantOnce(ctx, "recording-illustration", []gamedata.BattleReward{{Type: 45, ID: 65193, Count: 1}}); err != nil {
				t.Fatal(err)
			}
		})
		closeNicknamePlayer(t, s.owner)
	}
	registry := newPlayerRegistry(factory, time.Minute)
	t.Cleanup(func() {
		if err := registry.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.Replace(r.URL.Path, "/replay/", spineRecordingPath, 1)
		transport.HTTP{SpineRecordings: spineRecordings{players: registry}}.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	configured, err := gameconfig.Document.Parse([]byte(`{"schema_version":1,"spine_recording":{"download_base_url":"` + server.URL + `/replay"}}`))
	if err != nil {
		t.Fatal(err)
	}
	factory.options.gameRules.SpineRecording = configured.SpineRecording
	a, b := friendHTTPLogin(t, registry, accounts[0]), friendHTTPLogin(t, registry, accounts[1])
	reject := func(client *friendHTTPClient, path string, fields []byte) {
		t.Helper()
		client.seq++
		body := nicknameEncrypt(t, append(appScalar(1, client.seq), fields...), client.key)
		request := httptest.NewRequest(http.MethodPut, "/game"+path, bytes.NewReader(body))
		request.Header.Set("Cookie", client.cookie)
		response := httptest.NewRecorder()
		client.handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s accepted invalid recording operation: HTTP%d", path, response.Code)
		}
	}
	download := func(address string, status int, want []byte) {
		t.Helper()
		response, err := server.Client().Get(address)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status || status == http.StatusOK && !bytes.Equal(body, want) {
			t.Fatalf("native playback download: status=%d want=%d bytes=%d error=%v", response.StatusCode, status, len(body), err)
		}
	}
	data := nativeInteractionRecording(t, 250)
	save := append(appScalar(2, 1065193), appMessage(3, data)...)
	beforeAchievements := a.send("/SpineInteractionAchievementInfo", nil, 532, 0)
	beforeRewards := a.send("/SpineInteractionRewardInfo", nil, 548, 0)
	reject(a, "/SpineInteractionRecordSave", append(appScalar(2, 1065193), appMessage(3, nativeInteractionRecording(t, 601000))...))
	saved := a.send("/SpineInteractionRecordSave", save, 573, 0)
	index, address := appValue(saved, 1), nicknameField(t, saved, 3)
	if index == 0 || nicknameField(t, saved, 2) == "" || !strings.HasPrefix(address, server.URL+"/replay/") {
		t.Fatal("save did not provide a usable native slot and configured playback address")
	}
	a.seq--
	if retry := a.send("/SpineInteractionRecordSave", save, 573, 0); !bytes.Equal(retry, saved) {
		t.Fatal("retry allocated another recording or changed the playback address")
	}
	list := appRows(a.send("/SpineInteractionRecordInfo", nil, 571, 0), 1)
	if len(list) != 1 || appValue(list[0], 1) != index || appValue(list[0], 2) != 1065193 || nicknameField(t, list[0], 4) != address {
		t.Fatal("saved recording cannot be selected in the native list")
	}
	download(address, http.StatusOK, data)
	if details := appRows(a.send("/SpineInteractionRecordDetailInfo", appScalar(2, index), 572, 0), 1); len(details) != 1 || !bytes.Equal(details[0], data) {
		t.Fatal("design-client playback differs from normal HTTP playback")
	}
	if rows := appRows(b.send("/SpineInteractionRecordInfo", nil, 571, 0), 1); len(rows) != 0 {
		t.Fatal("recording list leaked another player's recordings")
	}
	reject(b, "/SpineInteractionRecordNameUpdate", append(appScalar(2, index), appMessage(3, []byte("ForeignName"))...))
	otherSaved := b.send("/SpineInteractionRecordSave", save, 573, 0)
	foreign, err := url.Parse(nicknameField(t, otherSaved, 3))
	if err != nil {
		t.Fatal(err)
	}
	foreign.Path = foreign.Path[:strings.LastIndexByte(foreign.Path, '/')+1] + address[strings.LastIndexByte(address, '/')+1:]
	download(foreign.String(), http.StatusNotFound, nil)
	corrupt := append([]byte(nil), data...)
	corrupt[0] ^= 1
	reject(a, "/SpineInteractionRecordDataUpdate", append(appScalar(2, index), appMessage(3, corrupt)...))
	download(address, http.StatusOK, data)
	a.send("/SpineInteractionRecordNameUpdate", append(appScalar(2, index), appMessage(3, []byte("我的回放"))...), 576, 0)
	updatedData := nativeInteractionRecording(t, 250, 500)
	updated := a.send("/SpineInteractionRecordDataUpdate", append(appScalar(2, index), appMessage(3, updatedData)...), 575, 0)
	newAddress := nicknameField(t, updated, 1)
	if newAddress == address {
		t.Fatal("replacing a recording preserved the previous download capability")
	}
	download(address, http.StatusNotFound, nil)
	download(newAddress, http.StatusOK, updatedData)
	if err := registry.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	registry = newPlayerRegistry(factory, time.Minute)
	a = friendHTTPLogin(t, registry, accounts[0])
	list = appRows(a.send("/SpineInteractionRecordInfo", nil, 571, 0), 1)
	if len(list) != 1 || nicknameField(t, list[0], 3) != "我的回放" || nicknameField(t, list[0], 4) != newAddress {
		t.Fatal("server restart lost the renamed recording or its playback link")
	}
	download(newAddress, http.StatusOK, updatedData)
	if !bytes.Equal(a.send("/SpineInteractionAchievementInfo", nil, 532, 0), beforeAchievements) || !bytes.Equal(a.send("/SpineInteractionRewardInfo", nil, 548, 0), beforeRewards) {
		t.Fatal("recording/replaying incorrectly changed interaction discoveries or reward claims")
	}
	for range 3 {
		a.send("/SpineInteractionRecordSave", save, 573, 0)
	}
	reject(a, "/SpineInteractionRecordSave", save)
	a.send("/SpineInteractionRecordDelete", appScalar(2, index), 574, 0)
	a.seq--
	a.send("/SpineInteractionRecordDelete", appScalar(2, index), 574, 0)
	download(newAddress, http.StatusNotFound, nil)
	newSave := a.send("/SpineInteractionRecordSave", save, 573, 0)
	if appValue(newSave, 1) <= index || len(appRows(a.send("/SpineInteractionRecordInfo", nil, 571, 0), 1)) != 4 {
		t.Fatal("deleting a recording did not release a slot, or reused a deleted recording identity")
	}
	if err := registry.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	registry = newPlayerRegistry(factory, time.Minute)
	download(newAddress, http.StatusNotFound, nil)
}
