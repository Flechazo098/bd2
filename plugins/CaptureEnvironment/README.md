# BD2 official capture environment plugin

This BepInEx plugin is only for the separately extracted official 2.34.13 client.

- redirects `Application.persistentDataPath` to `<game>/IsolatedUserData`;
- prefixes game `PlayerPrefs` keys with `BD2OfficialCapture23413:`;
- keeps account/profile/Neo/Intro data isolated while patching
  `NetworkManager.GetPachedGameDataPath()` to the existing `Data/t` and while
  the launcher validates junctions for shared `SoundData`, Addressables,
  VideoData and the localized `StreamingAssets/aa` bundle store;
- captures BD2 API request/response bodies without a proxy or TLS certificate;
- captures protobuf requests before encryption and responses after AES decryption;
- omits Cookie/Authorization headers and URL query strings;
- skips GET/static resources and bodies larger than 16 MiB.

HTTP events and protobuf events have independent IDs. A Batch response can
decrypt many protobuf messages under one HTTP request ID; repeated bodies get a
numeric filename suffix and are never overwritten.

The raw body files may still contain private account data. Do not publish the
generated `Capture` directory. This plugin does not redirect the game API and
must not be installed together with `BD2LocalIdentity.dll` in the official
comparison client.

Unity starts writing its default Player log before BepInEx can patch the data
path. Always launch through `tools/python/start-official-capture.ps1`; it supplies an
explicit isolated `-logFile` path, passes the shared GameData location to the
plugin and validates all static-data junctions. The launcher requires explicit
official-client and local-client directory arguments.

Pass them explicitly, for example:

```powershell
.\tools\python\start-official-capture.ps1 `
  -GameDir "<official-client-directory>" `
  -LocalClientDir "<local-client-directory>"
```

The launcher derives the shared user-data directory for the current Windows
account; `-SharedDataDir` overrides it when the game uses another location.
