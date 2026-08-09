package deezer

import (
	"strconv"
	"strings"
)

// Numeric Deezer format codes (TrackFormats in the deezer-sdk).
const (
	FormatMP3_128 = 1
	FormatMP3_320 = 3
	FormatMP3_256 = 5
	FormatFLAC    = 9
	FormatMP3_64  = 10
	FormatAAC_64  = 8
)

// formatCode maps a config format name to its numeric code (legacy URL) and the
// media.deezer.com format string (identical name).
func formatCode(name string) (int, bool) {
	switch name {
	case "MP3_128":
		return FormatMP3_128, true
	case "MP3_320":
		return FormatMP3_320, true
	case "MP3_256":
		return FormatMP3_256, true
	case "FLAC":
		return FormatFLAC, true
	case "MP3_64":
		return FormatMP3_64, true
	case "AAC_64":
		return FormatAAC_64, true
	default:
		return 0, false
	}
}

// GWArtist is an artist entry inside a GWTrack's ARTISTS array.
type GWArtist struct {
	ArtID      string `json:"ART_ID"`
	ArtName    string `json:"ART_NAME"`
	ArtPicture string `json:"ART_PICTURE"`
	RoleID     string `json:"ROLE_ID"` // "0" = main, "5" = featured
}

// GWTrack is the subset of a gw-light track object deebeets needs. Deezer's
// gateway returns most numeric fields as strings.
type GWTrack struct {
	SngID               string              `json:"SNG_ID"`
	SngTitle            string              `json:"SNG_TITLE"`
	Version             string              `json:"VERSION"`
	ArtName             string              `json:"ART_NAME"`
	ArtID               string              `json:"ART_ID"`
	Artists             []GWArtist          `json:"ARTISTS"`
	AlbTitle            string              `json:"ALB_TITLE"`
	AlbID               string              `json:"ALB_ID"`
	AlbPicture          string              `json:"ALB_PICTURE"`
	TrackToken          string              `json:"TRACK_TOKEN"`
	Duration            string              `json:"DURATION"`
	TrackNumber         string              `json:"TRACK_NUMBER"`
	DiskNumber          string              `json:"DISK_NUMBER"`
	MD5Origin           string              `json:"MD5_ORIGIN"`
	MediaVersion        string              `json:"MEDIA_VERSION"`
	ISRC                string              `json:"ISRC"`
	Gain                string              `json:"GAIN"`
	ExplicitLyrics      string              `json:"EXPLICIT_LYRICS"`
	LyricsID            int64               `json:"LYRICS_ID"`
	GenreID             string              `json:"GENRE_ID"`
	PhysicalReleaseDate string              `json:"PHYSICAL_RELEASE_DATE"`
	DigitalReleaseDate  string              `json:"DIGITAL_RELEASE_DATE"`
	Copyright           string              `json:"COPYRIGHT"`
	// Contributors maps contributor role → list of artist names.
	// Known roles: "main_artist", "featuring", "author", "composer", "conductor", etc.
	Contributors        map[string][]string `json:"SNG_CONTRIBUTORS"`
	Fallback            *GWTrack            `json:"FALLBACK"`
}

// MainArtistPicture returns the ART_PICTURE hash of the primary (ROLE_ID "0") artist,
// falling back to an empty string if not available.
func (t *GWTrack) MainArtistPicture() string {
	for _, a := range t.Artists {
		if a.RoleID == "0" {
			return a.ArtPicture
		}
	}
	return ""
}

// GWAlbum is the subset of a gw-light album object deebeets needs.
type GWAlbum struct {
	AlbID               string `json:"ALB_ID"`
	AlbTitle            string `json:"ALB_TITLE"`
	LabelName           string `json:"LABEL_NAME"`
	NumberTrack         string `json:"NUMBER_TRACK"`
	NumberDisk          string `json:"NUMBER_DISK"`
	PhysicalReleaseDate string `json:"PHYSICAL_RELEASE_DATE"`
	DigitalReleaseDate  string `json:"DIGITAL_RELEASE_DATE"`
	OriginalReleaseDate string `json:"ORIGINAL_RELEASE_DATE"`
	Copyright           string `json:"COPYRIGHT"`
	GenreID             string `json:"GENRE_ID"`
	UPC                 string `json:"UPC"`
}

// GWLyrics holds plain and synced lyrics from song.getLyrics.
type GWLyrics struct {
	LyricsID   string         `json:"LYRICS_ID"`
	LyricsText string         `json:"LYRICS_TEXT"`
	SyncJSON   []LyricsSyncEntry `json:"LYRICS_SYNC_JSON"`
	Copyright  string         `json:"LYRICS_COPYRIGHTS"`
	Writers    string         `json:"LYRICS_WRITERS"`
}

// LyricsSyncEntry is one line in the synced lyrics payload.
type LyricsSyncEntry struct {
	LRCTimestamp string `json:"lrc_timestamp"`
	Milliseconds string `json:"milliseconds"`
	Duration     string `json:"duration"`
	Line         string `json:"line"`
}

// ToLRC converts synced lyrics to LRC format string.
func (l *GWLyrics) ToLRC() string {
	if len(l.SyncJSON) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, e := range l.SyncJSON {
		if e.LRCTimestamp == "" {
			sb.WriteString("\n")
			continue
		}
		sb.WriteString(e.LRCTimestamp)
		sb.WriteString(e.Line)
		sb.WriteString("\n")
	}
	return sb.String()
}

// ID returns the numeric SNG_ID.
func (t *GWTrack) ID() int64 {
	n, _ := strconv.ParseInt(t.SngID, 10, 64)
	return n
}

// MediaVersionInt returns MEDIA_VERSION as an int.
func (t *GWTrack) MediaVersionInt() int {
	n, _ := strconv.Atoi(t.MediaVersion)
	return n
}

// TrackNumberInt returns the track position.
func (t *GWTrack) TrackNumberInt() int {
	n, _ := strconv.Atoi(t.TrackNumber)
	return n
}

// DiscNumberInt returns the disc number.
func (t *GWTrack) DiscNumberInt() int {
	n, _ := strconv.Atoi(t.DiskNumber)
	return n
}

// ReleaseYear returns the 4-digit release year, preferring the physical then
// digital release date, or 0 if unknown.
func (t *GWTrack) ReleaseYear() int {
	for _, d := range []string{t.PhysicalReleaseDate, t.DigitalReleaseDate} {
		if len(d) >= 4 {
			if y, err := strconv.Atoi(d[:4]); err == nil && y > 0 {
				return y
			}
		}
	}
	return 0
}

// ReleaseDate returns the best available release date string (YYYY-MM-DD).
func (t *GWTrack) ReleaseDate() string {
	if t.PhysicalReleaseDate != "" {
		return t.PhysicalReleaseDate
	}
	return t.DigitalReleaseDate
}

// MainArtists returns the list of main artists from SNG_CONTRIBUTORS, falling
// back to ART_NAME if contributors are absent.
func (t *GWTrack) MainArtists() []string {
	if main, ok := t.Contributors["main_artist"]; ok && len(main) > 0 {
		return main
	}
	return []string{t.ArtName}
}

// FeaturedArtists returns the list of featured artists from SNG_CONTRIBUTORS.
func (t *GWTrack) FeaturedArtists() []string {
	return t.Contributors["featuring"]
}

// AllArtists returns the flat list of all artists (main + featured) in order.
func (t *GWTrack) AllArtists() []string {
	all := make([]string, 0, len(t.MainArtists())+len(t.FeaturedArtists()))
	all = append(all, t.MainArtists()...)
	all = append(all, t.FeaturedArtists()...)
	return all
}

// ArtistString returns a human-readable display string for the ARTIST tag:
// "Main1 / Main2" or "Main1 feat. Feat1 / Feat2".
// When both ARTIST and ARTISTS are set, Navidrome uses ARTIST only as the
// display name and never splits it — so this can read naturally.
func (t *GWTrack) ArtistString() string {
	main := strings.Join(t.MainArtists(), " / ")
	feat := t.FeaturedArtists()
	if len(feat) == 0 {
		return main
	}
	return main + " feat. " + strings.Join(feat, " / ")
}

// AlbumArtistString returns main artists only, joined with " / ".
func (t *GWTrack) AlbumArtistString() string {
	return strings.Join(t.MainArtists(), " / ")
}

// ReplayGainString returns a formatted replaygain string from the GAIN field,
// e.g. "-14.8" → "-14.80 dB".
func (t *GWTrack) ReplayGainString() string {
	if t.Gain == "" || t.Gain == "0" {
		return ""
	}
	return t.Gain + " dB"
}

// AlbumID returns the numeric ALB_ID.
func (t *GWTrack) AlbumID() int64 {
	n, _ := strconv.ParseInt(t.AlbID, 10, 64)
	return n
}

// NumberTrackInt returns the total track count.
func (a *GWAlbum) NumberTrackInt() int {
	n, _ := strconv.Atoi(a.NumberTrack)
	return n
}

// NumberDiskInt returns the total disc count.
func (a *GWAlbum) NumberDiskInt() int {
	n, _ := strconv.Atoi(a.NumberDisk)
	return n
}

// deezerGenres maps Deezer GENRE_ID to a human-readable genre name.
// Only the most common IDs are listed; unknown IDs fall back to empty string.
var deezerGenres = map[string]string{
	"0":   "",
	"132": "Pop",
	"116": "Hip-Hop",
	"152": "Rock",
	"113": "Dance",
	"165": "R&B",
	"106": "Electro",
	"85":  "Alternative",
	"98":  "Classical",
	"144": "Reggae",
	"197": "Singer-Songwriter",
	"466": "Metal",
	"464": "Hard Rock",
	"169": "Soul",
	"75":  "Jazz",
	"173": "Country",
	"134": "World",
	"7":   "Electronic",
	"734": "Alternative",
}

// GenreName returns the human-readable genre name for the given GENRE_ID.
func GenreName(genreID string) string {
	return deezerGenres[genreID]
}

// GenreName returns the human-readable genre for the track's GENRE_ID.
func (t *GWTrack) GenreName() string {
	return deezerGenres[t.GenreID]
}
type userData struct {
	User struct {
		UserID  int64 `json:"USER_ID"`
		Options struct {
			LicenseToken    string `json:"license_token"`
			WebHQ           bool   `json:"web_hq"`
			MobileHQ        bool   `json:"mobile_hq"`
			WebLossless     bool   `json:"web_lossless"`
			MobileLossless  bool   `json:"mobile_lossless"`
			LicenseCountry  string `json:"license_country"`
		} `json:"OPTIONS"`
	} `json:"USER"`
	CheckForm string `json:"checkForm"`
}

// listData is a generic {data: [...], total, count} gw list payload.
type listData struct {
	Data  []GWTrack `json:"data"`
	Total int       `json:"total"`
	Count int       `json:"count"`
}

// favoriteIDs is the song.getFavoriteIds payload.
type favoriteIDs struct {
	Data []struct {
		SngID string `json:"SNG_ID"`
	} `json:"data"`
	Total int `json:"total"`
}

// profilePage is the subset of deezer.pageProfile we read.
type profilePage struct {
	Tab struct {
		Albums    listRef `json:"albums"`
		Artists   listRef `json:"artists"`
		Playlists listRef `json:"playlists"`
	} `json:"TAB"`
}

type listRef struct {
	Data []map[string]any `json:"data"`
}

// discography is the album.getDiscography payload.
type discography struct {
	Data []struct {
		AlbID string `json:"ALB_ID"`
	} `json:"data"`
	Total int `json:"total"`
}
