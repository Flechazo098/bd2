package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"bd2server/internal/common/release"
	"bd2server/internal/common/versionconfig"
)

type updateCompatibility struct {
	fingerprint *release.Fingerprint
	envelope    json.RawMessage
}

func (u updateCompatibility) summary() *release.Fingerprint {
	if u.fingerprint == nil {
		return nil
	}
	f := *u.fingerprint
	if f.Game != nil {
		g := *f.Game
		g.ResourceFiles = nil
		g.BuildOrigins = nil
		g.ResourceOrigins = nil
		f.Game = &g
	}
	return &f
}

func loadUpdateCompatibility(versions versionconfig.Config) (updateCompatibility, error) {
	raw, err := os.ReadFile(versions.Resolve(release.FingerprintFileName))
	if errors.Is(err, os.ErrNotExist) {
		return updateCompatibility{}, nil
	}
	if err != nil {
		return updateCompatibility{}, err
	}
	fingerprint, err := release.VerifyFingerprint(raw, release.BuiltinChannel().TrustedKeys)
	if err != nil {
		return updateCompatibility{}, err
	}
	if err := fingerprint.MatchesVersions(versions); err != nil {
		return updateCompatibility{}, fmt.Errorf("active update contract: %w", err)
	}
	return updateCompatibility{fingerprint: &fingerprint, envelope: raw}, nil
}

func (u updateCompatibility) allowed(headers http.Header) bool {
	if u.fingerprint == nil {
		return true
	}
	identity := release.ClientIdentity{Version: headers.Get("X-BD2-Client-Version"), GameVersion: headers.Get("X-BD2-Game-Version"), BundleVersion: headers.Get("X-BD2-Bundle-Version"), GameDataVersion: headers.Get("X-BD2-Game-Data-Version"), Fingerprint: headers.Get("X-BD2-Update-Fingerprint")}
	identity.ResourceFingerprint = headers.Get("X-BD2-Resource-Fingerprint")
	plugins := headers.Get("X-BD2-Plugins")
	if len(plugins) > 4096 {
		return false
	}
	if plugins != "" {
		if err := json.Unmarshal([]byte(plugins), &identity.Plugins); err != nil {
			return false
		}
	}
	return u.fingerprint.Accepts(identity)
}
