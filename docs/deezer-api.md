# Deezer GW API Reference

Deezer exposes an internal JSON-RPC-style gateway (`gw-light.php`) used by
their web player. This document describes every endpoint deebeets calls, the
request/response shapes, and how to verify them manually or via the integration
test suite.

> **Stability:** this is an undocumented internal API. Field names and
> availability can change without notice. The integration tests in
> `internal/deezer/api_integration_test.go` serve as a canary — run them
> after a suspected API change to see exactly what broke.

---

## Authentication

### Session bootstrap

Every call except `deezer.getUserData` requires a valid `api_token`. Obtain
one by:

1. Setting the `arl` cookie (your Deezer authentication token, found in
   browser DevTools → Application → Cookies → `deezer.com` → `arl`).
2. Calling `deezer.getUserData` with `api_token=null`. The response's
   `results.checkForm` field is the `api_token` to use for all subsequent
   calls.

The HTTP client **must carry cookies across requests** (session cookies are
set on the `getUserData` response). Losing the session cookies causes
subsequent calls to return `{"VALID_TOKEN_REQUIRED": "Invalid CSRF token"}`.

### Request format

All calls are HTTP POST to:

```
https://www.deezer.com/ajax/gw-light.php
  ?method=<method>
  &input=3
  &api_version=1.0
  &api_token=<token>          ← URL-encoded; ~ is safe (RFC 3986 unreserved)
```

Body: JSON object of method arguments.  
Headers: `Content-Type: application/json`, `User-Agent: <any browser UA>`.

### Response envelope

```json
{
  "error":   {},          // empty object/array = no error; otherwise an error object
  "results": { ... },     // method-specific payload
  "payload": null
}
```

---

## Endpoints

### `deezer.getUserData`

Bootstrap login. Always use `api_token=null`.

**Request body:** `{}`

**Key response fields (`results`):**

| Field | Type | Description |
|---|---|---|
| `checkForm` | string | API token for all subsequent calls |
| `USER.USER_ID` | int | Logged-in user's numeric ID |
| `USER.OPTIONS.license_token` | string | Token for media URL resolution |
| `USER.OPTIONS.web_lossless` | bool | Account can stream FLAC |
| `USER.OPTIONS.web_hq` | bool | Account can stream MP3 320 |

---

### `song.getData`

Full metadata for a single track, including contributors and lyrics ID.

**Request body:** `{"SNG_ID": <int>}`

**Key response fields (`results`):**

| Field | Type | Description |
|---|---|---|
| `SNG_ID` | string | Track ID |
| `SNG_TITLE` | string | Track title |
| `ART_NAME` | string | Primary artist name |
| `ART_ID` | string | Primary artist ID |
| `ARTISTS` | array | All artists with roles (see below) |
| `ALB_ID` | string | Album ID |
| `ALB_TITLE` | string | Album title |
| `ALB_PICTURE` | string | Album cover MD5 hash |
| `TRACK_NUMBER` | string | Track position on disc |
| `DISK_NUMBER` | string | Disc number |
| `DURATION` | string | Duration in seconds |
| `ISRC` | string | ISRC code |
| `GAIN` | string | Replay gain value (e.g. `"-14.8"`) |
| `EXPLICIT_LYRICS` | string | `"1"` if explicit |
| `LYRICS_ID` | int | `0` if no lyrics available |
| `GENRE_ID` | string | Numeric genre ID |
| `PHYSICAL_RELEASE_DATE` | string | `YYYY-MM-DD` |
| `DIGITAL_RELEASE_DATE` | string | `YYYY-MM-DD` |
| `COPYRIGHT` | string | Copyright string |
| `SNG_CONTRIBUTORS` | object | Contributors by role (see below) |
| `TRACK_TOKEN` | string | Short-lived token for media URL resolution |
| `TRACK_TOKEN_EXPIRE` | int | Unix timestamp of token expiry |
| `MD5_ORIGIN` | string | Used for legacy CDN URL construction |
| `MEDIA_VERSION` | string | Used for legacy CDN URL construction |

**`ARTISTS` array entry:**

```json
{
  "ART_ID": "14",
  "ART_NAME": "Gorillaz",
  "ART_PICTURE": "<md5>",
  "ROLE_ID": "0"        // "0" = main artist, "5" = featured
}
```

**`SNG_CONTRIBUTORS` object:**

```json
{
  "main_artist":      ["Gorillaz"],
  "featuring":        ["Sinfonia ViVa"],
  "author":           ["Damon Albarn"],
  "composer":         ["Damon Albarn"],
  "conductor":        ["André De Ridder"],
  "orchestra":        ["Sinfonia ViVa"],
  "recordingengineer":["Jason Cox"],
  "masteringengineer":["Howie Weinberg"]
}
```

> **Note:** `SNG_CONTRIBUTORS` is `null` on some tracks. Always check for nil.
> Key names use underscores (`main_artist`, `featuring`), not camelCase.

---

### `song.getListData`

Batch track hydration. Returns the same fields as `song.getData` but for
multiple tracks at once (max ~100 per call).

**Request body:** `{"SNG_IDS": [<int>, ...]}`

**Response:** `{"data": [...], "total": N, "count": N}`

> **Note:** `SNG_CONTRIBUTORS` is **not** returned by this endpoint.
> Only available via `song.getData`.

---

### `song.getListByAlbum`

All tracks on an album.

**Request body:** `{"ALB_ID": <int>, "nb": -1}`

**Response:** `{"data": [...], "total": N, "count": N}`

Same track shape as `song.getListData`. `SNG_CONTRIBUTORS` not present.

---

### `song.getLyrics`

Plain and synced lyrics for a track. Only call if `LYRICS_ID != 0`.

**Request body:** `{"SNG_ID": <int>}`

**Key response fields (`results`):**

| Field | Type | Description |
|---|---|---|
| `LYRICS_ID` | string | Lyrics ID |
| `LYRICS_TEXT` | string | Plain text, lines separated by `\r\n` |
| `LYRICS_SYNC_JSON` | array | Synced lyrics entries (see below) |
| `LYRICS_COPYRIGHTS` | string | Rights holder |
| `LYRICS_WRITERS` | string | Songwriter credits |

**`LYRICS_SYNC_JSON` entry:**

```json
{
  "lrc_timestamp": "[00:05.11]",
  "milliseconds":  "5110",
  "duration":      "2960",
  "line":          "Some kind of nature"
}
```

Entries with no timestamp (`lrc_timestamp` absent) represent blank lines
between stanzas. Assemble into LRC format by writing `<lrc_timestamp><line>\n`
for each timestamped entry and `\n` for blank lines.

---

### `album.getData`

Album-level metadata including label, total tracks/discs, and genre.

**Request body:** `{"ALB_ID": <int>}`

**Key response fields (`results`):**

| Field | Type | Description |
|---|---|---|
| `ALB_ID` | string | Album ID |
| `ALB_TITLE` | string | Album title |
| `LABEL_NAME` | string | Record label |
| `NUMBER_TRACK` | string | Total tracks |
| `NUMBER_DISK` | string | Total discs |
| `GENRE_ID` | string | Numeric genre ID |
| `COPYRIGHT` | string | Copyright string |
| `PHYSICAL_RELEASE_DATE` | string | `YYYY-MM-DD` |
| `DIGITAL_RELEASE_DATE` | string | `YYYY-MM-DD` |
| `ORIGINAL_RELEASE_DATE` | string | `YYYY-MM-DD` |

---

### `song.getFavoriteIds`

Paged list of the logged-in user's loved track IDs.

**Request body:** `{"nb": 2000, "start": 0, "checksum": null}`

**Response:** `{"data": [{"SNG_ID": "..."}], "total": N}`

Paginate by incrementing `start` by `nb` until `len(data) < nb`.

---

### `deezer.pageProfile`

Fetches a profile tab (albums, artists, or playlists) for a user.

**Request body:** `{"USER_ID": <int>, "tab": "albums"|"artists"|"playlists", "nb": 10000}`

**Response:** `{"TAB": {"albums": {"data": [...]}}}`

Each row is a raw map; extract `ALB_ID` / `ART_ID` / `PLAYLIST_ID` to expand.

---

### `playlist.getSongs`

All tracks in a playlist.

**Request body:** `{"PLAYLIST_ID": <int>, "nb": -1}`

**Response:** `{"data": [...], "total": N}`

---

### `album.getDiscography`

All album IDs in an artist's discography.

**Request body:** `{"ART_ID": <int>, "discography_mode": "all", "nb": 100, "nb_songs": 0, "start": 0}`

**Response:** `{"data": [{"ALB_ID": "..."}], "total": N}`

---

## Image URLs

Images are served from `https://e-cdns-images.dzcdn.net/images/`.

| Type | Path pattern |
|---|---|
| Album cover | `cover/<ALB_PICTURE>/<W>x<H>-000000-80-0-0.jpg` |
| Artist image | `artist/<ART_PICTURE>/<W>x<H>-000000-80-0-0.jpg` |

Common sizes: `56`, `250`, `500`, `1000`, `1200`.

---

## Media URL Resolution

Track audio is resolved via:

```
POST https://media.deezer.com/v1/get_url
```

Body:
```json
{
  "license_token": "<from getUserData>",
  "media": [{"type": "FULL", "formats": [{"cipher": "BF_CBC_STRIPE", "format": "FLAC"}]}],
  "track_tokens": ["<TRACK_TOKEN>"]
}
```

Returns a signed CDN URL. The audio stream is encrypted with
Blowfish (BF_CBC_STRIPE): every 6144-byte block, the first 2048 bytes are
Blowfish-decrypted using a per-track key derived from `md5(ascii(SNG_ID)) XOR
"g4el58wc0zvf9na1"`.

---

## Known Genre IDs

| ID | Genre |
|---|---|
| 0 | (none) |
| 7 | Electronic |
| 75 | Jazz |
| 85 | Alternative |
| 98 | Classical |
| 106 | Electro |
| 113 | Dance |
| 116 | Hip-Hop |
| 132 | Pop |
| 134 | World |
| 144 | Reggae |
| 152 | Rock |
| 165 | R&B |
| 169 | Soul |
| 173 | Country |
| 197 | Singer-Songwriter |
| 464 | Hard Rock |
| 466 | Metal |
| 734 | Alternative |

---

## Manual API exploration

Use the helper script below. It handles session cookies and token encoding
correctly:

```python
import json, urllib.parse, http.cookiejar, urllib.request, time

ARL = "<your ARL here>"
BASE = "https://www.deezer.com/ajax/gw-light.php"

jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
c = http.cookiejar.Cookie(
    0, "arl", ARL, None, False, ".deezer.com", True, True, "/",
    False, False, int(time.time()) + 86400, False, None, None, {}
)
jar.set_cookie(c)

def gw(method, args, token="null"):
    url = (f"{BASE}?method={urllib.parse.quote(method)}"
           f"&input=3&api_version=1.0"
           f"&api_token={urllib.parse.quote(str(token), safe='~-_.')}")
    req = urllib.request.Request(
        url, json.dumps(args).encode(),
        {"Content-Type": "application/json", "User-Agent": "Mozilla/5.0"}
    )
    return json.loads(opener.open(req).read())

# Bootstrap
token = gw("deezer.getUserData", {})["results"]["checkForm"]

# Example: fetch track
track = gw("song.getData", {"SNG_ID": 5490698}, token)["results"]
print(json.dumps(track, indent=2))

# Example: fetch album
album = gw("album.getData", {"ALB_ID": 502723}, token)["results"]
print(json.dumps(album, indent=2))

# Example: fetch lyrics
lyrics = gw("song.getLyrics", {"SNG_ID": 5490698}, token)["results"]
print(json.dumps(lyrics, indent=2))
```

> **Key gotcha:** the `api_token` often contains `~` characters. Python's
> `urllib.parse.quote` encodes `~` as `%7E` by default, which Deezer rejects.
> Always pass `safe='~-_.'` to `quote()`. The same applies to any HTTP client
> that URL-encodes query parameters — ensure `~` is left unencoded.
> Go's `url.Values.Encode()` has the same issue; deebeets works around this
> because Go's `net/url` package treats `~` as safe per RFC 3986.
