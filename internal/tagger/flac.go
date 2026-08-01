package tagger

import (
	"fmt"

	"github.com/go-flac/flacpicture/v2"
	"github.com/go-flac/flacvorbis/v2"
	"github.com/go-flac/go-flac/v2"
)

// writeFLAC writes Vorbis comments (and an embedded picture) to a FLAC file.
func writeFLAC(path string, md Metadata, f FieldSet) error {
	file, err := flac.ParseFile(path)
	if err != nil {
		return fmt.Errorf("parse flac: %w", err)
	}

	cmt := flacvorbis.New()
	add := func(field, key, val string) {
		if val == "" || !f.on(field) {
			return
		}
		_ = cmt.Add(key, val)
	}

	add("title", "TITLE", md.Title)
	add("artist", "ARTIST", md.Artist)
	add("albumartist", "ALBUMARTIST", md.AlbumArtist)
	add("album", "ALBUM", md.Album)
	add("genre", "GENRE", md.Genre)
	add("composer", "COMPOSER", md.Composer)
	add("isrc", "ISRC", md.ISRC)
	add("barcode", "BARCODE", md.Barcode)
	add("copyright", "COPYRIGHT", md.Copyright)
	add("replaygain", "REPLAYGAIN_TRACK_GAIN", md.ReplayGain)
	add("comment", "COMMENT", md.Comment)
	add("lyrics", "LYRICS", md.Lyrics)
	if md.Date != "" {
		add("date", "DATE", md.Date)
	} else if md.Year > 0 {
		add("date", "DATE", fmt.Sprintf("%d", md.Year))
	}
	if f.on("tracknumber") && md.TrackNumber > 0 {
		add("tracknumber", "TRACKNUMBER", fmt.Sprintf("%d", md.TrackNumber))
	}
	if f.on("totaltracks") && md.TotalTracks > 0 {
		add("totaltracks", "TRACKTOTAL", fmt.Sprintf("%d", md.TotalTracks))
	}
	if f.on("discnumber") && md.DiscNumber > 0 {
		add("discnumber", "DISCNUMBER", fmt.Sprintf("%d", md.DiscNumber))
	}
	if f.on("disctotal") && md.TotalDiscs > 0 {
		add("disctotal", "DISCTOTAL", fmt.Sprintf("%d", md.TotalDiscs))
	}
	if f.on("bpm") && md.BPM > 0 {
		add("bpm", "BPM", fmt.Sprintf("%d", md.BPM))
	}

	replaceComments(file, cmt)

	if f.on("cover") && len(md.Cover) > 0 {
		pic, err := flacpicture.NewFromImageData(
			flacpicture.PictureTypeFrontCover, "Front cover", md.Cover, coverMIME(md))
		if err == nil {
			block := pic.Marshal()
			file.Meta = append(file.Meta, &block)
		}
	}

	return file.Save(path)
}

// replaceComments removes any existing Vorbis comment block and appends the new
// one, so re-tagging is idempotent.
func replaceComments(file *flac.File, cmt *flacvorbis.MetaDataBlockVorbisComment) {
	kept := file.Meta[:0]
	for _, m := range file.Meta {
		if m.Type != flac.VorbisComment {
			kept = append(kept, m)
		}
	}
	block := cmt.Marshal()
	file.Meta = append(kept, &block)
}
