package release

type ClientIdentity struct {
	Version             string            `json:"client_version"`
	Plugins             map[string]string `json:"plugins"`
	GameVersion         string            `json:"game_version"`
	BundleVersion       string            `json:"bundle_version"`
	GameDataVersion     string            `json:"game_data_version"`
	Fingerprint         string            `json:"fingerprint"`
	ResourceFingerprint string            `json:"resource_fingerprint"`
}

func AtLeast(current, minimum string) bool {
	a, err := ParseVersion(current)
	if err != nil {
		return false
	}
	b, err := ParseVersion(minimum)
	return err == nil && a.Kind == b.Kind && a.HasGame == b.HasGame && a.Compare(b) >= 0
}

func (f Fingerprint) Accepts(client ClientIdentity) bool {
	if f.RequireClientUpdate {
		if !AtLeast(client.Version, f.ClientVersion) {
			return false
		}
		for id, version := range f.MinimumPlugins {
			if !AtLeast(client.Plugins[id], version) {
				return false
			}
		}
	}
	if f.RequireGameUpdate {
		if f.Game == nil || client.ResourceFingerprint != f.GameFingerprint {
			return false
		}
		if client.GameVersion != f.Game.GameVersion || client.BundleVersion != f.Game.BundleVersion || client.GameDataVersion != f.Game.GameDataVersion {
			return false
		}
	}
	return true
}
