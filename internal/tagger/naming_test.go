package tagger

import "testing"

const navidromeTmpl = `{{.AlbumArtist}}/{{.Album}}{{if .Year}} ({{.Year}}){{end}}/{{if .MultiDisc}}{{.Disc}}-{{end}}{{printf "%02d" .Track}} {{.Title}}.{{.Ext}}`

func TestRenderPathSingleDisc(t *testing.T) {
	got, err := RenderPath(navidromeTmpl, NameData{
		AlbumArtist: "Radiohead", Album: "OK Computer", Title: "Airbag",
		Year: 1997, Track: 1, Ext: "flac",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "Radiohead/OK Computer (1997)/01 Airbag.flac"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRenderPathMultiDiscAndSanitize(t *testing.T) {
	got, err := RenderPath(navidromeTmpl, NameData{
		AlbumArtist: "AC/DC", Album: "Back: In/Black", Title: `He"llo?`,
		Year: 1980, Track: 7, Disc: 2, MultiDisc: true, Ext: "mp3",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Illegal characters (/ : ? ") become underscores; dir separators preserved.
	want := "AC_DC/Back_ In_Black (1980)/2-07 He_llo_.mp3"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRenderPathNoYear(t *testing.T) {
	got, err := RenderPath(navidromeTmpl, NameData{
		AlbumArtist: "X", Album: "Y", Title: "Z", Track: 3, Ext: "mp3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "X/Y/03 Z.mp3" {
		t.Fatalf("got %q", got)
	}
}
