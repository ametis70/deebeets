package tagger

import (
	"fmt"

	"github.com/bogem/id3v2/v2"
)

// writeMP3 writes ID3v2.4 frames to an MP3 file.
func writeMP3(path string, md Metadata, f FieldSet) error {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return fmt.Errorf("open mp3 tags: %w", err)
	}
	defer tag.Close()
	tag.SetVersion(4)

	set := func(field, id, val string) {
		if val == "" || !f.on(field) {
			return
		}
		tag.AddTextFrame(id, tag.DefaultEncoding(), val)
	}
	setUser := func(field, desc, val string) {
		if val == "" || !f.on(field) {
			return
		}
		tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{
			Encoding: tag.DefaultEncoding(), Description: desc, Value: val,
		})
	}

	if f.on("title") && md.Title != "" {
		tag.SetTitle(md.Title)
	}

	// TPE1: artist display name + TXXX:ARTISTS multi-value.
	if f.on("artist") && md.Artist != "" {
		tag.SetArtist(md.Artist)
		for _, a := range md.Artists {
			if a != "" {
				tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{
					Encoding: tag.DefaultEncoding(), Description: "ARTISTS", Value: a,
				})
			}
		}
	}

	// TPE2: album artist display name + TXXX:ALBUMARTISTS multi-value.
	set("albumartist", "TPE2", md.AlbumArtist)
	if f.on("albumartist") {
		for _, a := range md.AlbumArtists {
			if a != "" {
				tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{
					Encoding: tag.DefaultEncoding(), Description: "ALBUMARTISTS", Value: a,
				})
			}
		}
	}

	if f.on("album") && md.Album != "" {
		tag.SetAlbum(md.Album)
	}
	set("genre", "TCON", md.Genre)
	set("label", "TPUB", md.Label)
	set("composer", "TCOM", md.Composer)
	set("copyright", "TCOP", md.Copyright)

	if md.Date != "" {
		set("date", "TDRC", md.Date)
	} else if md.Year > 0 {
		set("date", "TDRC", fmt.Sprintf("%d", md.Year))
	}
	if f.on("tracknumber") && md.TrackNumber > 0 {
		tag.AddTextFrame("TRCK", tag.DefaultEncoding(), numPair(md.TrackNumber, tracktotalOrZero(f, md)))
	}
	if f.on("discnumber") && md.DiscNumber > 0 {
		tag.AddTextFrame("TPOS", tag.DefaultEncoding(), numPair(md.DiscNumber, disctotalOrZero(f, md)))
	}
	if f.on("bpm") && md.BPM > 0 {
		set("bpm", "TBPM", fmt.Sprintf("%d", md.BPM))
	}

	setUser("isrc", "ISRC", md.ISRC)
	setUser("barcode", "BARCODE", md.Barcode)
	setUser("replaygain", "REPLAYGAIN_TRACK_GAIN", md.ReplayGain)

	if f.on("comment") && md.Comment != "" {
		tag.AddCommentFrame(id3v2.CommentFrame{
			Encoding: tag.DefaultEncoding(), Language: "eng", Text: md.Comment,
		})
	}
	if f.on("lyrics") && md.Lyrics != "" {
		tag.AddUnsynchronisedLyricsFrame(id3v2.UnsynchronisedLyricsFrame{
			Encoding: tag.DefaultEncoding(), Language: "eng", Lyrics: md.Lyrics,
		})
	}
	if f.on("lyrics") && md.SyncedLyrics != "" {
		tag.AddUnsynchronisedLyricsFrame(id3v2.UnsynchronisedLyricsFrame{
			Encoding:    tag.DefaultEncoding(),
			Language:    "eng",
			ContentDescriptor: "Synced",
			Lyrics:      md.SyncedLyrics,
		})
	}
	if f.on("cover") && len(md.Cover) > 0 {
		tag.AddAttachedPicture(id3v2.PictureFrame{
			Encoding:    tag.DefaultEncoding(),
			MimeType:    coverMIME(md),
			PictureType: id3v2.PTFrontCover,
			Description: "Front cover",
			Picture:     md.Cover,
		})
	}

	return tag.Save()
}

func tracktotalOrZero(f FieldSet, md Metadata) int {
	if f.on("totaltracks") {
		return md.TotalTracks
	}
	return 0
}

func disctotalOrZero(f FieldSet, md Metadata) int {
	if f.on("disctotal") {
		return md.TotalDiscs
	}
	return 0
}

func coverMIME(md Metadata) string {
	if md.CoverMIME != "" {
		return md.CoverMIME
	}
	return "image/jpeg"
}
