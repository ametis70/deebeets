package deezer

import "strconv"

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

// GWTrack is the subset of a gw-light track object deebeets needs. Deezer's
// gateway returns most numeric fields as strings.
type GWTrack struct {
	SngID              string `json:"SNG_ID"`
	SngTitle           string `json:"SNG_TITLE"`
	Version            string `json:"VERSION"`
	ArtName            string `json:"ART_NAME"`
	ArtID              string `json:"ART_ID"`
	AlbTitle           string `json:"ALB_TITLE"`
	AlbID              string `json:"ALB_ID"`
	AlbPicture         string `json:"ALB_PICTURE"`
	TrackToken         string `json:"TRACK_TOKEN"`
	Duration           string `json:"DURATION"`
	TrackNumber        string `json:"TRACK_NUMBER"`
	DiskNumber         string `json:"DISK_NUMBER"`
	MD5Origin          string `json:"MD5_ORIGIN"`
	MediaVersion       string `json:"MEDIA_VERSION"`
	ISRC               string `json:"ISRC"`
	Gain               string `json:"GAIN"`
	PhysicalReleaseDate string `json:"PHYSICAL_RELEASE_DATE"`
	DigitalReleaseDate string `json:"DIGITAL_RELEASE_DATE"`
	Copyright          string `json:"COPYRIGHT"`
	Fallback           *GWTrack `json:"FALLBACK"`
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

// userData is the parsed deezer.getUserData result.
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
