# BD2 official capture environment plugin

This BepInEx plugin is only for the separately extracted official client selected by the repository-root `versions.json`.

- redirects `Application.persistentDataPath` to `<game>/IsolatedUserData`;
- prefixes game `PlayerPrefs` keys with a namespace derived from the configured client version;
- keeps account/profile/Neo/Intro and the installed GameData working set in
  `<game>/IsolatedUserData`; `NetworkManager.GetPachedGameDataPath()` uses the
  private `Data/t` directory and refuses a linked GameData path;
- uses `PluginLocalRes.dll` to read the explicitly supplied, reviewed
  ServerData/GameData source; SoundData and VideoData are shared static-data
  junctions, while the versioned Addressables catalog cache remains private;
  player saves and account data are never linked or copied from the primary
  client;
- uses the official client archive's own `StreamingAssets/aa` bundle store;
- captures BD2 API request/response bodies without a proxy or TLS certificate;
- captures protobuf requests before encryption and responses after AES decryption;
- writes through a bounded asynchronous queue; queue overflow or any writer
  failure creates `INCOMPLETE.txt`, rejects later packets with an error in the
  BepInEx log, and prevents a partial capture from looking complete;
- drains the writer queue during normal application shutdown; if the drain
  cannot finish within the shutdown safety deadline, the capture is marked
  incomplete instead of silently dropping the tail;
- omits Cookie/Authorization headers and URL query strings;
- skips GET/static resources and bodies larger than 16 MiB.

Request and response protobuf records share one correlation ID when the client
provides the request path. Batch responses are captured after each item is
decrypted. JSONL retains the complete path and type; the human-readable log
truncates those two display columns only. Treat a capture containing
`INCOMPLETE.txt` as unusable evidence and repeat it.

The raw body files may still contain private account data. Do not publish the
generated `Capture` directory. This plugin does not redirect the game API and
must not be installed together with `BD2LocalIdentity.dll` in the official
comparison client.

Unity starts writing its default Player log before BepInEx can patch the data
path. Always launch through `tools/python/start-official-capture.ps1`; it supplies an
explicit isolated `-logFile` path and validates static-data junctions. The
launcher requires the official-client directory argument.

Pass them explicitly, for example:

```powershell
.\tools\python\start-official-capture.ps1 `
  -GameDir "<official-client-directory>" `
  -ResourceRoot "<downloaded-resource-root>"
```

The launcher reads bundle and GameData versions from the repository-root
`versions.json`. `-VersionConfig`, `-BundleVersion`, and `-GameDataVersion`
remain explicit development overrides.

The launcher derives the shared user-data directory for the current Windows
account; `-SharedDataDir` overrides it when the game uses another location.
