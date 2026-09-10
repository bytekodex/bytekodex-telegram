package main

import (
	"slices"
	"testing"
)

// javac prints its release list two different ways, and reading only the indented form loses the
// first version off the list — which reads as the manifest being wrong about the floor rather than
// as a parsing bug. Both real shapes are here verbatim.
func TestSupportedReleasesReadsBothShapesOfJavacHelp(t *testing.T) {
	cases := []struct {
		name string
		help string
		want []int
	}{
		{
			name: "on the same line, after other text, as javac 12 through 20 print it",
			help: "  --release <release>\n" +
				"        Compile for a specific release. Supported releases: 7, 8, 9, 10, 11, 12\n" +
				"  -s <directory>               Specify where to place generated source files\n",
			want: []int{7, 8, 9, 10, 11, 12},
		},
		{
			name: "called targets rather than releases, as javac 9 prints it",
			help: "  --release <release>\n" +
				"        Compile for a specific VM version. Supported targets: 6, 7, 8, 9\n",
			want: []int{6, 7, 8, 9},
		},
		{
			name: "wrapped onto the next line, as javac 25 prints it",
			help: "  --release <release>\n" +
				"        Compile for the specified Java SE release.\n" +
				"        Supported releases: \n" +
				"            8, 9, 10, 11, 12\n",
			want: []int{8, 9, 10, 11, 12},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := supportedReleases(test.help); !slices.Equal(got, test.want) {
				t.Errorf("supportedReleases = %v, want %v", got, test.want)
			}
		})
	}
}

// javac 7 and 8 have no release list at all, and a help text without one must not read as an empty
// list that would make the floor look like zero.
func TestHelpWithoutAReleaseListReadsAsNothing(t *testing.T) {
	if got := supportedReleases("Usage: javac <options> <source files>\n"); got != nil {
		t.Errorf("supportedReleases = %v, want nothing", got)
	}
}
