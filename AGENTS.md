# Meloport development rules

- Implement only the platforms supported by the current ChKSz contract: NetEase, QQ Music and Kugou. Keep other platforms behind the `provider.Source` extension interface without registering placeholder implementations.
- Fetch playlists, search results and song metadata directly from the music platform first. Use ChKSz for missing-file audio resolution. Metadata may fall back to a documented ChKSz endpoint only after a native failure and when `metadata.fallback_to_api` is enabled.
- A valid empty native result is successful. Cancellation must not trigger a paid fallback. Do not invent QQ or Kugou playlist endpoints on ChKSz.
- Treat native NetEase IDs, QQ MIDs and Kugou hashes/audio identifiers separately. Confirm an identifier accepted by the audio resolver; reject ambiguous versions rather than silently substituting a different song.
- Native metadata and an all-skipped sync must work without a ChKSz key. Read keys and optional platform cookies from environment variables only. Never include them in YAML, SQLite, logs, UI responses or Git.
- Preserve incremental synchronization: download missing files, retain songs removed from remote playlists, and never delete audio when deleting a subscription.
- Serve the embedded Web UI from the same Go binary. Persist task/default-rule changes to YAML and update scheduling safely. Keep the local management listener on loopback and retain same-origin write protection.
- Develop and verify locally; Docker packaging is deferred until requested. Every test or preview must stop its owned processes and release its ports afterward.
- Put Chinese design notes in `tmp/`. Keep reference clones under ignored `tmp/references/`. Do not commit caches, binaries, local configuration, keys or runtime data.
- Use English Git commit messages. The project remote is `https://github.com/yzfh-ty/meloport.git`.
- After completing and validating each change, create a local Git commit. Push only when the user requests it.
- For Web UI development, use `scripts/dev.ps1 -Action dev`; it serves the three whitelisted source assets live. Restart this Go process after backend changes.
- Run `scripts/dev.ps1 -Action test` and build before delivery. Concurrency changes also need race checks when the local CGO toolchain is usable. Distinguish fixture verification from live upstream or real audio verification.
