# jellybot

jellybot organizes media files into a [Jellyfin](https://jellyfin.org) library. It identifies each file by its contents, not by its name. Thus, a file called `the other movie with Mace Windu.mkv` becomes:

```
Movies/The Matrix (1999) [tmdbid-603]/The Matrix (1999) [tmdbid-603].mkv
```

jellybot uses only deterministic methods: file hashes, exact file sizes, embedded tags, and audio fingerprints. Every lookup goes into a local database, so the first run is slow and the next runs are fast.

## Install

```
go install github.com/amwolff/jellybot/cmd/jellybot@latest
```

For audio files without tags, also install `fpcalc` (Chromaprint), for example `apt install libchromaprint-tools` or `brew install chromaprint`. jellybot finds it in `PATH`.

## Usage

```
jellybot tui  SOURCE DESTINATION   # two-pane preview; apply with "a"
jellybot plan SOURCE DESTINATION   # dry run: print the plan, change nothing
```

`DESTINATION` is the root of your libraries. jellybot writes into these subdirectories:

| Directory | Jellyfin library type |
|---|---|
| `Movies/` | Movies |
| `Shows/` | Shows |
| `Music/` | Music |
| `Unidentified/` | Home Videos and Photos (Jellyfin does no lookups there) |

Unmatched files stay in `SOURCE`. To send one to `Unidentified/`, press `u` on its row.

### Flags

Each flag also reads an environment variable: `--tmdb-api-key` reads `JELLYBOT_TMDB_API_KEY`, and so on.

| Flag | Default | |
|---|---|---|
| `--move` | copy | Start in move mode. The `m` key changes the mode. |
| `-r`, `--recursive` | off | Scan subdirectories. The `r` key changes it. |
| `--chmod` | | Octal mode overrides: `D0750` for directories, `F0640` for files, a bare `0755` for both. |
| `--chown` | | Owner override as numeric `UID:GID`, for example `1000:1000`. |
| `--jobs` | 4 | Files to identify at the same time. |
| `--db` | `~/.cache/jellybot/jellybot.db` | The database. |
| `--log-file` | | Log of the TUI (with `--debug` for more detail). |
| `--tmdb-api-key` | | TMDB key or read access token. Needed for videos. |
| `--opensubtitles-api-key` | | OpenSubtitles key. |
| `--acoustid-api-key` | | AcoustID application key. |
| `--movies-dir`, `--shows-dir`, `--music-dir`, `--unidentified-dir` | `Movies`, … | Subdirectory names. |

The overrides apply to the files that jellybot copies or moves, their sidecars, and the directories that jellybot creates. jellybot does not change directories that exist.

### API keys

All keys are free for non-commercial use:

- **TMDB:** <https://www.themoviedb.org/settings/api>
- **OpenSubtitles:** <https://www.opensubtitles.com/en/consumers>
- **AcoustID:** <https://acoustid.org/new-application>

srrDB and MusicBrainz do not need keys.

### TUI keys

| Key | Action |
|---|---|
| `↑`/`↓`, `k`/`j`, `pgup`/`pgdn`, `g`/`G` | Move |
| `space` | Skip or include the row |
| `m` | Copy or move |
| `r` | Recursive on or off (scans again) |
| `u` | Send an unmatched file to `Unidentified/` |
| `c` | Next candidate of an ambiguous file (jellybot records the choice) |
| `R` | Look up the row again, without the cache |
| `a` | Apply (asks first) |
| `q`, `ctrl+c` | Quit (`ctrl+c` during the apply step stops after the current file) |

## How identification works

**Videos:**
1. The [OpenSubtitles hash](https://trac.opensubtitles.org/projects/opensubtitles/wiki/HashSourceCodes) (size and the first and last 64 KiB) goes to OpenSubtitles, which knows the hashes of many releases.
2. If OpenSubtitles has no match, srrDB finds scene releases by exact file size. jellybot confirms each candidate with the CRC32 of the full file.
3. TMDB gives the names.

**Audio:**
1. Embedded tags (artist, album, and title) give the path directly.
2. Without tags, `fpcalc` makes a fingerprint, AcoustID finds the recording, and MusicBrainz gives the release.

**Sidecars:** subtitles (`.srt`, `.ass`, …) and lyrics (`.lrc`) go with their main file and keep their suffix, for example `.en.forced.srt`.

## Safety

- The destination file is byte-identical to the source. A copy goes to a temporary file. jellybot then reads the copy again and compares SHA-256 hashes before it renames the copy into place.
- A move is a rename. Across filesystems, it is a verified copy, and jellybot removes the source only after the copy is in place.
- jellybot never overwrites a file. If a different file is at the destination, the row shows a conflict.

## The database

- The database holds file hashes, the identity of each file (IDs only), and the raw provider responses.
- "Not found" responses expire after 30 days. TMDB responses expire after 180 days, as the TMDB terms require.
- jellybot discards an expired entry when it reads it.

## Data sources

- This product uses the TMDB API but is not endorsed or certified by TMDB. ![TMDB](https://www.themoviedb.org/assets/2/v4/logos/v2/blue_short-8e7b30f73a4020692ccca9c88bafe5dcb6f8a62a4c6bc55cd9ba82bb2cd95f6c.svg)
- Subtitle hash data: [OpenSubtitles](https://www.opensubtitles.com).
- Scene release data: [srrDB](https://www.srrdb.com).
- Fingerprint data: [AcoustID](https://acoustid.org) (CC BY-SA 3.0).
- Music metadata: [MusicBrainz](https://musicbrainz.org) (CC0).

## License

MIT
