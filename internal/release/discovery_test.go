package release

import "testing"

func TestStableDiscoveryRejectsCandidatesAndUnpublishedReleases(t *testing.T) {
	for _, tc := range []struct {
		response string
		want     string
	}{
		{`{"tag_name":"v0.7.1","prerelease":false,"draft":false}`, "0.7.1"},
		{`{"tag_name":"v0.7.1-rc.1","prerelease":true}`, ""},
		{`{"tag_name":"v0.7.1-rc.1","prerelease":false}`, ""},
		{`{"tag_name":"v0.7.1","prerelease":true}`, ""},
		{`{"tag_name":"v0.7.1","draft":true}`, ""},
		{`{"tag_name":"v0.07.1"}`, ""},
		{`{"tag_name":"v0.7.1/other"}`, ""},
		{`{"tag_name":"0.7.1"}`, ""},
		{`{"message":"Not Found"}`, ""},
		{`not JSON`, ""},
	} {
		if got := stableReleaseVersion([]byte(tc.response)); got != tc.want {
			t.Errorf("discovery from %s = %q; want %q", tc.response, got, tc.want)
		}
	}
}
