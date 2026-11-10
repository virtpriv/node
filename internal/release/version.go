package release

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

// releaseVersion is the accepted shape for a release version.
// Anchored and strict: this is the single choke point between
// "string from the network" and "string in a release URL".
var releaseVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$`)

// ValidVersion accepts canonical stable versions and numbered release candidates.
// Other prerelease forms and build metadata are outside VPN's release contract.
func ValidVersion(version string) bool { return releaseVersion.MatchString(version) }

func IsCandidate(version string) bool {
	return ValidVersion(version) && strings.Contains(version, "-rc.")
}

func IsStable(version string) bool {
	return ValidVersion(version) && !strings.Contains(version, "-")
}

// Newer reports release ordering, including RCs before their stable version.
// Development builds and noncanonical strings cannot authorize an update.
// Compatibility of a newer release is a separate check.
func Newer(current, target string) (bool, error) {
	if !ValidVersion(current) {
		return false, fmt.Errorf(
			"running version %q is not a release build, "+
				"updating needs one", current)
	}
	if !ValidVersion(target) {
		return false, fmt.Errorf(
			"%q is not a valid release version", target)
	}
	return semver.Compare("v"+target, "v"+current) > 0, nil
}
