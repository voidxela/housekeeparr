package domain

import "testing"

func TestClassifyPayload(t *testing.T) {
	sel := func(p string, size int64, wanted bool) TransferFile {
		return TransferFile{Path: p, Size: size, Wanted: KnownValue(wanted)}
	}
	unk := func(p string) TransferFile { return TransferFile{Path: p, Size: 1, Wanted: UnknownValue[bool]("x")} }
	for name, tc := range map[string]struct {
		files []TransferFile
		want  PayloadKind
	}{
		"rar set only": {[]TransferFile{sel("S/s.rar", 9, true), sel("S/s.r00", 9, true), sel("S/s.r01", 9, true), sel("S/s.nfo", 1, true)}, PayloadArchiveOnly},
		"split 7z":     {[]TransferFile{sel("S/s.7z.001", 9, true), sel("S/s.7z.002", 9, true)}, PayloadArchiveOnly},
		"sample only":  {[]TransferFile{sel("S/Sample/s.mkv", 9, true), sel("S/s.nfo", 1, true)}, PayloadSampleOnly},
		"sample name":  {[]TransferFile{sel("S/show-sample.mkv", 9, true)}, PayloadSampleOnly},
		// "Sampler" in a title is not a sample.
		"title word":                  {[]TransferFile{sel("The.Sampler.S01E01.mkv", 9, true)}, PayloadVideo},
		"video and archives":          {[]TransferFile{sel("S/s.mkv", 9, true), sel("S/extras.rar", 9, true)}, PayloadMixed},
		"video beside its sample":     {[]TransferFile{sel("S/s.mkv", 9, true), sel("S/Sample/s.mkv", 1, true)}, PayloadVideo},
		"deselected video ignored":    {[]TransferFile{sel("S/s.mkv", 9, false), sel("S/s.rar", 9, true)}, PayloadArchiveOnly},
		"only a deselected video":     {[]TransferFile{sel("S/s.mkv", 9, false), sel("S/s.nfo", 1, true)}, PayloadUnknown},
		"selection unknown, archives": {[]TransferFile{unk("S/s.rar"), unk("S/s.r00")}, PayloadUnknown},
		"selection unknown, video":    {[]TransferFile{unk("S/s.mkv")}, PayloadVideo},
		"nothing recognised":          {[]TransferFile{sel("S/s.iso", 9, true)}, PayloadUnknown},
		"empty":                       {nil, PayloadUnknown},
	} {
		if got := ClassifyPayload(tc.files); got.Kind != tc.want {
			t.Errorf("%s: %+v, want %s", name, got, tc.want)
		}
	}
}
