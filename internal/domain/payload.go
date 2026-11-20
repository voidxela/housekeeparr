package domain

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// PayloadKind is what a download's selected files are, as far as their
// names and sizes show. It is diagnosis evidence only: Housekeeparr never
// extracts, moves or deletes files.
type PayloadKind string

const (
	// PayloadArchiveOnly: archives (RAR sets, zip, 7z) and no video file.
	PayloadArchiveOnly PayloadKind = "archive_only"
	// PayloadSampleOnly: every video is positively named a sample (a
	// "sample" file name or folder) and there is no archive.
	PayloadSampleOnly PayloadKind = "sample_only"
	// PayloadMixed: a full video beside archives.
	PayloadMixed PayloadKind = "mixed"
	// PayloadVideo: full videos and no archive; the file names show no
	// specific cause.
	PayloadVideo PayloadKind = "video"
	// PayloadUnknown: no selected file is a recognised video or archive,
	// or which files are selected is not known.
	PayloadUnknown PayloadKind = "unknown"
)

var (
	videoExt   = map[string]bool{".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".ts": true, ".m2ts": true, ".wmv": true, ".mov": true, ".webm": true, ".mpg": true, ".mpeg": true}
	archiveExt = regexp.MustCompile(`(?i)\.(rar|r\d{2}|zip|7z|\d{3})$`)
	sampleName = regexp.MustCompile(`(?i)(^|[._ -])sample([._ -]|$)`)
)

// PayloadContents summarises a download's selected files.
type PayloadContents struct {
	Kind     PayloadKind `json:"kind"`
	Videos   int         `json:"videos"`
	Samples  int         `json:"samples"`
	Archives int         `json:"archives"`
	Other    int         `json:"other"`
	// Deselected counts files the user did not select; they are ignored.
	Deselected int `json:"deselected"`
	// SelectionUnknown: the client did not say which files are selected,
	// so every file was counted.
	SelectionUnknown bool `json:"selection_unknown,omitempty"`
}

// ClassifyPayload classifies the selected files of a download.
func ClassifyPayload(files []TransferFile) PayloadContents {
	var c PayloadContents
	for _, f := range files {
		wanted, known := f.Wanted.Get()
		if !known {
			c.SelectionUnknown = true
		} else if !wanted {
			c.Deselected++
			continue
		}
		p := strings.ToLower(f.Path)
		ext := path.Ext(p)
		switch {
		case videoExt[ext] && isSample(p):
			c.Samples++
		case videoExt[ext]:
			c.Videos++
		case archiveExt.MatchString(p):
			c.Archives++
		default:
			c.Other++
		}
	}
	switch {
	case c.SelectionUnknown && c.Videos == 0:
		// A deselected full video may exist; nothing specific is known.
		c.Kind = PayloadUnknown
	case c.Archives > 0 && c.Videos == 0 && c.Samples == 0:
		c.Kind = PayloadArchiveOnly
	case c.Samples > 0 && c.Videos == 0 && c.Archives == 0:
		c.Kind = PayloadSampleOnly
	case c.Videos > 0 && c.Archives > 0:
		c.Kind = PayloadMixed
	case c.Videos > 0:
		c.Kind = PayloadVideo
	default:
		c.Kind = PayloadUnknown
	}
	return c
}

func isSample(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "sample" || seg == "samples" {
			return true
		}
	}
	return sampleName.MatchString(strings.TrimSuffix(path.Base(p), path.Ext(p)))
}

// Describe is a one-line account of the contents.
func (c PayloadContents) Describe() string {
	s := fmt.Sprintf("%d video, %d sample, %d archive and %d other selected file(s)", c.Videos, c.Samples, c.Archives, c.Other)
	if c.Deselected > 0 {
		s += fmt.Sprintf("; %d not selected", c.Deselected)
	}
	if c.SelectionUnknown {
		s += "; the client does not report which files are selected"
	}
	return s
}
