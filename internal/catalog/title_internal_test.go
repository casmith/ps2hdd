package catalog

import "testing"

// The display title normally comes from the archive member, because that is
// the image's own name. Not every archive is packed that way: a member called
// "SLUS-20152 (1.00).iso" leaves nothing once the serial is taken out, and the
// game reached the console called "(1 00)". The container's name is what the
// user chose, so it is the second answer.
func TestTitleForArchive(t *testing.T) {
	const archive = "/roms/Ace Combat 04 - Shattered Skies (USA).7z"
	cases := map[string]struct{ member, want string }{
		"a member with a real name wins": {
			"Ar tonelico - Melody of Elemia (USA)", "Ar tonelico - Melody of Elemia (USA)",
		},
		"a member that is only a serial falls back": {
			"(1 00)", "Ace Combat 04 - Shattered Skies (USA)",
		},
		"an empty member falls back": {
			"", "Ace Combat 04 - Shattered Skies (USA)",
		},
		"digits and punctuation alone are not a title": {
			"(1.00)", "Ace Combat 04 - Shattered Skies (USA)",
		},
		// One letter is a title. The test is "did anything survive", not a
		// judgement about what makes a good name.
		"a single letter is enough": {"Q", "Q"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := titleForArchive(tc.member, archive); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
